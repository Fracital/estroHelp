import { useEffect, useState } from 'preact/hooks';
import { api, streamChat, ApiError } from './api';
import { accumulateToolCall, buildHistory, finishedToolCalls, truncateOutput, MAX_ITERATIONS } from './agent';
import type { Attachment, MessageData, ToolCall, ToolDef } from './workspace-contract';
import { workspace } from '../components/workspace';
import type { Msg } from '../components/chat/Messages';
import type { ToolRun } from '../components/chat/Trace';

// One thread's live state and its agent loop, independent of any view. The
// chat page owns one for the main column and one per popped-out window, so a
// popout keeps streaming on its own thread while the page moves on.

export interface Thread { id: string; title: string; model: string; updated: string; }
export interface Draft { text: string; attachments: Attachment[]; }

export interface SessionState {
  threadId: string | null;
  model: string;
  messages: Msg[];
  live: Msg | null;
  busy: boolean;
  toolRuns: Record<string, ToolRun>;
  error: string;
  /** A link shown beside the error, e.g. to pricing when a plan or window is the reason. */
  errorAction: { href: string; label: string } | null;
}

/** True when the request was refused for billing reasons: no plan, or a usage window that is full. */
function isBillingRefusal(e: ApiError): boolean {
  if (e.code === 'billing_error') return true;
  const t = e.data?.error?.type;
  return e.status === 429 && t === 'usage_limit_reached';
}

export interface SessionHooks {
  /** Tool schemas to send with the next request (empty = plain chat). */
  tools: () => ToolDef[];
  /** A thread row changed (new message, title, order). */
  onThread?: (t: Thread) => void;
  /** A thread was created by this session. */
  onThreadCreated?: (t: Thread) => void;
  onUsage?: () => void;
}

export function runsFromMessages(ms: Msg[]): Record<string, ToolRun> {
  const runs: Record<string, ToolRun> = {};
  for (const m of ms) {
    if (m.role === 'tool' && m.data?.toolCallId) {
      const code = m.data.exitCode;
      runs[m.data.toolCallId] = { output: m.content, running: false, exitCode: code, ok: (code ?? 0) === 0 };
    }
  }
  return runs;
}

export class ThreadSession {
  state: SessionState;
  private listeners = new Set<() => void>();
  private abort: AbortController | null = null;
  private stopped = false;
  private sending = false;

  constructor(private hooks: SessionHooks, model = '') {
    this.state = { threadId: null, model, messages: [], live: null, busy: false, toolRuns: {}, error: '', errorAction: null };
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  }

  private set(patch: Partial<SessionState>) {
    this.state = { ...this.state, ...patch };
    for (const fn of this.listeners) fn();
  }

  setError(error: string) { this.set({ error, errorAction: null }); }
  setModel(model: string) { this.set({ model }); }

  reset() {
    this.set({ threadId: null, messages: [], live: null, toolRuns: {}, error: '' });
  }

  async load(id: string) {
    const d = await api<{ thread: Thread; messages: Msg[] }>(`/api/threads/${id}`);
    this.set({ threadId: id, messages: d.messages, toolRuns: runsFromMessages(d.messages), model: d.thread.model, error: '' });
    return d.thread;
  }

  stop() {
    this.stopped = true;
    this.abort?.abort();
  }

  /** Saves a message and appends it (locally only when the save fails). */
  private async persist(tid: string, msg: Msg): Promise<Msg> {
    try {
      const d = await api<{ message: Msg; thread: Thread }>(`/api/threads/${tid}/messages`, {
        body: { role: msg.role, content: msg.content, model: msg.model, data: msg.data ?? undefined },
      });
      const saved: Msg = { ...d.message, data: msg.data ?? d.message.data };
      this.set({ messages: [...this.state.messages, saved] });
      this.hooks.onThread?.(d.thread);
      return saved;
    } catch (err) {
      this.set({ messages: [...this.state.messages, msg] });
      throw err;
    }
  }

  /** Sends a draft. Returns false when the message could not be saved (the caller may restore the draft). */
  async send(draft: Draft): Promise<boolean> {
    // `busy` only flips after the thread/message round-trips, so a second
    // Enter during that window must be refused here.
    if (this.state.busy || !this.state.model || this.sending) return true;
    this.sending = true;
    this.set({ error: '' });
    let tid = this.state.threadId;
    try {
      if (!tid) {
        const t = await api<Thread>('/api/threads', { body: { model: this.state.model } });
        tid = t.id;
        this.set({ threadId: tid });
        this.hooks.onThreadCreated?.(t);
      }
      const data: MessageData | undefined = draft.attachments.length ? { attachments: draft.attachments } : undefined;
      await this.persist(tid, { role: 'user', content: draft.text, data });
    } catch (err) {
      this.sending = false;
      this.set({ error: (err as Error).message });
      return false;
    }
    try {
      await this.generate(tid);
    } finally {
      this.sending = false;
    }
    return true;
  }

  private async truncateFrom(msg: Msg): Promise<boolean> {
    const tid = this.state.threadId;
    if (this.state.busy || !tid || !msg.seq) return false;
    this.set({ error: '' });
    try { await api(`/api/threads/${tid}/messages/${msg.seq}`, { method: 'DELETE' }); }
    catch (err) { this.set({ error: (err as Error).message }); return false; }
    const kept = this.state.messages.filter((m) => (m.seq ?? Infinity) < msg.seq!);
    this.set({ messages: kept, toolRuns: runsFromMessages(kept) });
    return true;
  }

  /** Edit-and-resubmit: drop the message and everything after it, then send the new text. */
  async edit(msg: Msg, text: string) {
    if (await this.truncateFrom(msg)) await this.send({ text, attachments: msg.data?.attachments ?? [] });
  }

  /** Deletes a user message and everything after it. */
  async deleteMessage(msg: Msg) {
    await this.truncateFrom(msg);
  }

  async retry(msg: Msg) {
    if (await this.truncateFrom(msg) && this.state.threadId) await this.generate(this.state.threadId);
  }

  /** The agent loop: stream a reply, run any tool calls in the workspace, repeat. */
  private async generate(tid: string) {
    const enabled = this.hooks.tools();
    const useTools = enabled.length > 0;
    const model = this.state.model;
    this.set({ busy: true, error: '' });
    this.stopped = false;
    try {
      if (useTools) {
        try { await workspace.ready(); } catch (err) {
          this.set({ error: `Workspace failed to start: ${(err as Error).message}` });
          return;
        }
      }
      for (let iter = 0; iter < MAX_ITERATIONS && !this.stopped; iter++) {
        const ctrl = new AbortController();
        this.abort = ctrl;
        let text = '', reasoning = '', reasoningStart = 0, reasoningMs = 0, thinking = false, frame = 0;
        const acc = new Map<number, ToolCall>();
        const snapshot = (): MessageData => {
          const d: MessageData = {};
          if (reasoning) { d.reasoning = reasoning; if (reasoningMs) d.reasoningMs = reasoningMs; }
          const calls = finishedToolCalls(acc);
          if (calls.length) d.toolCalls = calls;
          return d;
        };
        const paint = () => {
          frame = 0;
          this.set({ live: { role: 'assistant', content: text, model, streaming: true, thinking, data: snapshot() } });
        };
        paint();
        let failure: Error | null = null;
        try {
          const req = { model, messages: buildHistory(this.state.messages), tools: useTools ? enabled : undefined };
          for await (const ev of streamChat(req, ctrl.signal)) {
            if (ev.reasoning) {
              if (!reasoningStart) reasoningStart = performance.now();
              thinking = true;
              reasoning += ev.reasoning;
            }
            if ((ev.text || ev.toolCall) && thinking) {
              thinking = false;
              reasoningMs = Math.round(performance.now() - reasoningStart);
            }
            if (ev.text) text += ev.text;
            if (ev.toolCall) accumulateToolCall(acc, ev.toolCall);
            if (!frame) frame = requestAnimationFrame(paint);
          }
        } catch (err) {
          if ((err as Error).name !== 'AbortError') failure = err as Error;
        }
        if (thinking) { thinking = false; reasoningMs = Math.round(performance.now() - reasoningStart); }
        if (frame) cancelAnimationFrame(frame);
        this.abort = null;
        const aborted = ctrl.signal.aborted;
        const calls = aborted ? [] : finishedToolCalls(acc);
        const data = snapshot();
        if (aborted) delete data.toolCalls;

        if (text.trim() || calls.length) {
          try { await this.persist(tid, { role: 'assistant', content: text, model, data }); }
          catch (err) { failure ??= err as Error; }
        }
        this.set({ live: null });
        if (failure) {
          const ae = failure as ApiError;
          const billing = ae instanceof ApiError && isBillingRefusal(ae);
          this.set({
            error: ae.code === 'email_unverified' ? 'Verify your email to keep chatting.' : ae.message,
            errorAction: billing ? { href: '/pricing', label: ae.status === 429 ? 'Upgrade' : 'See plans' } : null,
          });
          this.hooks.onUsage?.();
          break;
        }
        if (aborted || !calls.length) break;

        for (const call of calls) {
          if (this.stopped) break;
          const tctrl = new AbortController();
          this.abort = tctrl;
          this.set({ toolRuns: { ...this.state.toolRuns, [call.id]: { output: '', running: true } } });
          const result = await workspace.execute(call, {
            signal: tctrl.signal,
            onOutput: (chunk) => {
              const r = this.state.toolRuns;
              this.set({ toolRuns: { ...r, [call.id]: { ...(r[call.id] ?? { running: true }), output: (r[call.id]?.output ?? '') + chunk } } });
            },
          });
          this.abort = null;
          this.set({ toolRuns: { ...this.state.toolRuns, [call.id]: { output: result.output, running: false, ok: result.ok, exitCode: result.exitCode } } });
          const toolData: MessageData = { toolCallId: call.id, toolName: call.name };
          if (typeof result.exitCode === 'number') toolData.exitCode = result.exitCode;
          try { await this.persist(tid, { role: 'tool', content: truncateOutput(result.output), model, data: toolData }); }
          catch (err) { this.set({ error: (err as Error).message }); this.stopped = true; }
        }
        if (iter === MAX_ITERATIONS - 1) this.set({ error: `Stopped after ${MAX_ITERATIONS} tool rounds.` });
      }
    } finally {
      this.abort = null;
      this.set({ live: null, busy: false });
      this.hooks.onUsage?.();
    }
  }
}

/** Re-renders the caller whenever the session changes. */
export function useSession(s: ThreadSession): SessionState {
  const [, force] = useState(0);
  useEffect(() => s.subscribe(() => force((n) => n + 1)), [s]);
  return s.state;
}
