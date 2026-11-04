// Pure helpers for the browser-driven agent loop: accumulate streamed tool
// calls and rebuild the OpenAI-shaped history from stored thread messages.
import type { ToolCallDelta } from './api';
import type { Attachment, MessageData, MessageRole, ToolCall } from './workspace-contract';

/** Hard cap on model turns per user message, so a confused model cannot loop forever. */
export const MAX_ITERATIONS = 25;
/** Tool output beyond this is truncated before it goes back to the model. */
export const MAX_TOOL_OUTPUT = 24_000;

export interface StoredMessage {
  seq?: number;
  role: MessageRole;
  content: string;
  model?: string | null;
  data?: MessageData | null;
}

/** Accumulates tool-call deltas keyed by index, in place. */
export function accumulateToolCall(acc: Map<number, ToolCall>, d: ToolCallDelta): void {
  let call = acc.get(d.index);
  if (!call) {
    call = { id: '', name: '', arguments: '' };
    acc.set(d.index, call);
  }
  if (d.id) call.id = d.id;
  if (d.name) call.name = d.name;
  if (d.args) call.arguments += d.args;
}

/** Finished calls in index order. Calls that never got an id get a synthetic one. */
export function finishedToolCalls(acc: Map<number, ToolCall>): ToolCall[] {
  return [...acc.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([i, c]) => ({ ...c, id: c.id || `call_${i}_${Date.now().toString(36)}` }))
    .filter((c) => c.name);
}

export function parseArguments(call: ToolCall): Record<string, unknown> {
  try {
    const v = JSON.parse(call.arguments || '{}');
    return v && typeof v === 'object' ? v : {};
  } catch {
    return {};
  }
}

export function truncateOutput(s: string, max = MAX_TOOL_OUTPUT): string {
  if (s.length <= max) return s;
  const head = Math.floor(max * 0.7), tail = max - head;
  return `${s.slice(0, head)}\n…[${s.length - max} characters truncated]…\n${s.slice(-tail)}`;
}

/** The text the model sees for a user message: typed text plus inlined text files. */
export function userText(content: string, attachments?: Attachment[]): string {
  const files = (attachments ?? []).filter((a) => a.kind === 'file' && typeof a.text === 'string');
  if (!files.length) return content;
  const blocks = files.map((f) => `\`\`\`${f.name}\n${f.text}\n\`\`\``);
  return [content, ...blocks].filter(Boolean).join('\n\n');
}

/** OpenAI content for a user message: a string, or parts when images are attached. */
export function userContent(content: string, attachments?: Attachment[]): string | unknown[] {
  const text = userText(content, attachments);
  const images = (attachments ?? []).filter((a) => a.kind === 'image' && a.dataUrl);
  if (!images.length) return text;
  const parts: unknown[] = [];
  if (text) parts.push({ type: 'text', text });
  for (const img of images) parts.push({ type: 'image_url', image_url: { url: img.dataUrl } });
  return parts;
}

/** Rebuilds the request history (including tool rounds) from stored messages. */
export function buildHistory(msgs: StoredMessage[]): unknown[] {
  const out: unknown[] = [];
  for (const m of msgs) {
    if (m.role === 'user') {
      out.push({ role: 'user', content: userContent(m.content, m.data?.attachments) });
    } else if (m.role === 'assistant') {
      const calls = m.data?.toolCalls ?? [];
      const msg: Record<string, unknown> = { role: 'assistant', content: m.content || (calls.length ? null : '') };
      if (calls.length) {
        msg.tool_calls = calls.map((c) => ({ id: c.id, type: 'function', function: { name: c.name, arguments: c.arguments || '{}' } }));
      }
      out.push(msg);
    } else if (m.role === 'tool') {
      out.push({ role: 'tool', tool_call_id: m.data?.toolCallId ?? '', content: m.content });
    }
  }
  return out;
}
