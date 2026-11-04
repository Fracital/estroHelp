import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { createPortal } from 'preact/compat';
import { Files, Gauge, KeyRound, MessageSquare, Monitor, PanelLeftClose, PanelLeftOpen, Pencil, Plus, Search, Sparkles, SquareArrowDownLeft, SquareArrowOutUpRight, SquareTerminal, Syringe, Trash2, X } from 'lucide-preact';
import { api, ApiError } from '../lib/api';
import { gate, user } from '../lib/auth';
import { fetchPlanMe, formatResetsIn, type Meter, type PlanMe } from '../lib/billing';
import { formatShekels } from '../lib/plans';
import { localizeSample, readRunParam, readShellKey, toShellLine, writeShellKey, type RunLang } from '../lib/run';
import { ThreadSession, useSession, type Thread } from '../lib/thread-session';
import { WORKSPACE_ROOT, listShells, onShellsChanged, openShellSession, writeText } from './workspace/dusk';
import { openPopoutWindow, PopoutView, type PopoutHandle } from './chat/Popout';
import type { Attachment, ToolDef } from '../lib/workspace-contract';
import { workspace } from './workspace';
import WorkspacePane from './workspace/WorkspacePane';
import Composer, { type Draft } from './Composer';
import type { MenuModel } from './chat/ModelMenu';
import { readToolsOff, toolOn, writeToolsOff } from './chat/ToolsMenu';
import { AssistantMessage, IconButton, Lightbox, UserMessage, type Msg } from './chat/Messages';
import '../styles/ai.css';
import '../styles/chat-extra.css';

const MODEL_KEY = 'kali:model';
type PaneTab = 'files' | 'shell';
interface Usage { requestsToday: number; dailyLimit: number; }

/** A thread living in its own window. */
interface PopoutSession {
  threadId: string;
  session: ThreadSession;
  handle: PopoutHandle;
  /** Created just for the popout: discarded when closed without a message. */
  created: boolean;
  docked: boolean;
}

const NARROW_QUERY = '(max-width: 900px)';
function isNarrow(): boolean { try { return window.matchMedia(NARROW_QUERY).matches; } catch { return false; } }

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/** Resolves with a live shell id once the Shell pane has opened one. */
function waitForShell(timeoutMs: number): Promise<string | null> {
  return new Promise((resolve) => {
    let off = () => {};
    const timer = window.setTimeout(() => { off(); resolve(null); }, timeoutMs);
    const check = () => {
      const s = listShells().find((x) => !x.exited);
      if (s) { window.clearTimeout(timer); off(); resolve(s.id); }
    };
    off = onShellsChanged(check);
    check();
  });
}

let runClaimed = false;
let shellKeyInFlight: Promise<string> | null = null;

/** One "Shell" key per account: reuse the cached secret, otherwise replace any stale Shell keys with a fresh one. */
function ensureShellKey(): Promise<string> {
  if (shellKeyInFlight) return shellKeyInFlight;
  shellKeyInFlight = (async () => {
    const cached = readShellKey();
    if (cached) {
      const r = await fetch('/api/usage', { headers: { Authorization: `Bearer ${cached}` } });
      if (r.status !== 401) return cached;
      writeShellKey(null);
    }
    const existing = await api<{ keys: { id: number; name: string }[] }>('/api/account/api-keys');
    for (const k of existing.keys.filter((k) => k.name === 'Shell')) {
      await api(`/api/account/api-keys/${k.id}`, { method: 'DELETE' }).catch(() => {});
    }
    const d = await api<{ secret: string }>('/api/account/api-keys', { body: { name: 'Shell' } });
    writeShellKey(d.secret);
    return d.secret;
  })().finally(() => { shellKeyInFlight = null; });
  return shellKeyInFlight;
}

const SIDEBAR_KEY = 'kali:sidebar-width';
const SIDEBAR_DEFAULT = 260, SIDEBAR_MIN = 220, SIDEBAR_MAX = 480;
const WS_KEY = 'kali:workspace-width';
const WS_MIN = 360;
function readWsWidth(): number | null {
  try { const n = Number(localStorage.getItem(WS_KEY)); return n >= WS_MIN ? n : null; } catch { return null; }
}
function readSidebarWidth(): number {
  try { const n = Number(localStorage.getItem(SIDEBAR_KEY)); return n >= SIDEBAR_MIN && n <= SIDEBAR_MAX ? n : SIDEBAR_DEFAULT; } catch { return SIDEBAR_DEFAULT; }
}

const HISTORY_BUCKETS = ['Today', 'Yesterday', 'Previous 7 days', 'Previous 30 days', 'Older'] as const;
const DAY_MS = 24 * 60 * 60 * 1000;
function historyBucket(updated: string, now: Date): (typeof HISTORY_BUCKETS)[number] {
  const when = new Date(updated).getTime();
  if (Number.isNaN(when)) return 'Older';
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  if (when >= start) return 'Today';
  if (when >= start - DAY_MS) return 'Yesterday';
  if (when >= start - 7 * DAY_MS) return 'Previous 7 days';
  if (when >= start - 30 * DAY_MS) return 'Previous 30 days';
  return 'Older';
}

function threadTitle(t: Thread | undefined, messages: Msg[]): string {
  return t?.title || messages.find((m) => m.role === 'user')?.content.split(/\r?\n/)[0] || 'New chat';
}

/** Renders one popped-out thread into its window, bound to that thread's own session. */
function PopoutPortal({ entry, thread, modelName, onDock, onOpenImage }: {
  entry: PopoutSession; thread: Thread | undefined; modelName: (id?: string | null) => string;
  onDock: () => void; onOpenImage: (files: Attachment[], index: number) => void;
}) {
  const s = useSession(entry.session);
  return createPortal(
    <PopoutView title={threadTitle(thread, s.messages)} messages={s.messages} live={s.live} runs={s.toolRuns} busy={s.busy} modelName={modelName}
      onSend={(text) => { void entry.session.send({ text, attachments: [] }); }} onStop={() => entry.session.stop()}
      onDock={onDock} onOpenImage={onOpenImage} />,
    entry.handle.target,
  );
}

export default function ChatApp() {
  const [ready, setReady] = useState(false);
  const [models, setModels] = useState<MenuModel[]>([]);
  const [threads, setThreads] = useState<Thread[]>([]);
  const [search, setSearch] = useState('');
  const [sidebarOpen, setSidebarOpen] = useState(() => !isNarrow());
  const [sidebarWidth, setSidebarWidth] = useState(readSidebarWidth);
  const [wsWidth, setWsWidth] = useState<number | null>(readWsWidth);
  const [resizing, setResizing] = useState<'' | 'sidebar' | 'ws'>('');
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameDraft, setRenameDraft] = useState('');
  const [pane, setPane] = useState<PaneTab | null>(null);
  const [lightbox, setLightbox] = useState<{ files: Attachment[]; index: number } | null>(null);
  const [seed, setSeed] = useState<Draft | null>(null);
  const [tools, setTools] = useState<ToolDef[]>([]);
  const [toolsOff, setToolsOff] = useState<Record<string, boolean>>({});
  const [toolsSupported, setToolsSupported] = useState(false);
  const [usage, setUsage] = useState<Usage>({ requestsToday: 0, dailyLimit: 0 });
  const [planMe, setPlanMe] = useState<PlanMe | null>(null);
  const [popouts, setPopouts] = useState<PopoutSession[]>([]);
  const popoutsRef = useRef<PopoutSession[]>([]);
  popoutsRef.current = popouts;
  const popoutPending = useRef<Set<string>>(new Set());

  const scroller = useRef<HTMLDivElement>(null);
  const textRef = useRef<HTMLTextAreaElement>(null);
  const stickBottom = useRef(true);
  const renameClosing = useRef(false);

  // Tool selection is read at request time so every session sees the latest choice.
  const enabledTools = useRef<() => ToolDef[]>(() => []);
  enabledTools.current = () => (toolsSupported ? tools.filter((t) => toolOn(toolsOff, t.function.name)) : []);

  const upsertThread = (t: Thread) => setThreads((ts) => [t, ...ts.filter((x) => x.id !== t.id)]);
  const refreshUsage = () => {
    api<Usage>('/api/usage').then(setUsage).catch(() => {});
    fetchPlanMe().then((p) => { if (p) setPlanMe(p); });
  };
  const hooks = useRef({ tools: () => enabledTools.current(), onThread: upsertThread, onUsage: refreshUsage });

  const main = useMemo(() => new ThreadSession({
    ...hooks.current,
    onThreadCreated: (t) => { upsertThread(t); history.replaceState(null, '', `#${t.id}`); },
  }), []);
  const ms = useSession(main);
  const { threadId: activeId, messages, live, busy, toolRuns, error, errorAction, model } = ms;

  const modelName = (id?: string | null) => models.find((m) => m.id === id)?.name ?? id ?? 'Estro';

  useEffect(() => {
    gate('chat').then(async (ok) => {
      if (!ok) return;
      const [m, t, p] = await Promise.all([api<{ models: MenuModel[] }>('/api/models'), api<{ threads: Thread[] }>('/api/threads'), fetchPlanMe()]);
      setModels(m.models);
      setThreads(t.threads);
      if (p) setPlanMe(p);
      const entitled = Boolean(p?.admin || p?.plan);
      const usable = (x: MenuModel) => x.available && (entitled || !x.requiresPlan);
      const saved = localStorage.getItem(MODEL_KEY);
      const first = m.models.find(usable)?.id ?? m.models[0]?.id ?? '';
      main.setModel(m.models.some((x) => x.id === saved && usable(x)) ? saved! : first);
      const supported = workspace.isSupported();
      setToolsSupported(supported);
      setTools(workspace.tools());
      setToolsOff(readToolsOff());
      refreshUsage();
      const hash = location.hash.slice(1);
      if (hash && t.threads.some((x) => x.id === hash)) openThread(hash);
      setReady(true);
      const run = readRunParam();
      // The island can mount more than once; only the first mount may claim the sample.
      if (run && !runClaimed) {
        runClaimed = true;
        history.replaceState(null, '', location.pathname);
        void runSample(run.code, run.lang);
      }
    });
    return () => { for (const p of popoutsRef.current) p.handle.close(); };
  }, []);

  // The hash is the thread deep link: follow it when it changes without a reload
  // (sidebar "open in new tab" links, docking, client-side navigation).
  useEffect(() => {
    const follow = () => {
      const id = location.hash.slice(1);
      const current = main.state.threadId;
      if (id && id !== current) void openThread(id);
      else if (!id && current && !main.state.busy) main.reset();
    };
    window.addEventListener('hashchange', follow);
    document.addEventListener('astro:after-swap', follow);
    return () => { window.removeEventListener('hashchange', follow); document.removeEventListener('astro:after-swap', follow); };
  }, []);

  /** "Run in shell" from the docs: fresh chat, Shell pane, env prelude, then the sample typed into the terminal. */
  async function runSample(code: string, lang: RunLang) {
    main.reset();
    if (!workspace.isSupported()) { main.setError('The workspace needs a browser with cross-origin isolation, so this sample cannot run here. Copy it instead.'); return; }
    setPane('shell');
    try { await workspace.ready(); } catch (err) { main.setError(`Workspace failed to start: ${(err as Error).message}`); return; }
    const shellId = await waitForShell(20000);
    const session = shellId ? openShellSession(shellId) : null;
    if (!session) { main.setError('The shell did not open.'); return; }
    // Let the shell print its first prompt before anything is typed into it.
    await sleep(900);
    const type = (line: string) => session.write(line + '\r');
    const origin = location.origin;
    let secret: string | null = null;
    try {
      secret = await ensureShellKey();
      type(`export ESTRO_BASE_URL="${origin}/v1"; export ESTRO_TOKEN="${secret}"`);
    } catch (err) {
      main.setError(`Could not create an API key for the shell: ${(err as ApiError).message}. The sample will run without a token.`);
      type(`export ESTRO_BASE_URL="${origin}/v1"`);
    }
    await sleep(400);
    const localized = localizeSample(code, origin, secret, lang);
    if (lang === 'node') {
      try { await writeText(`${WORKSPACE_ROOT}/run.mjs`, localized); } catch (err) { main.setError(`Could not write run.mjs: ${(err as Error).message}`); return; }
      type('node run.mjs');
    } else {
      type(toShellLine(localized));
    }
  }

  // ---- Popouts: a thread moves into its own window; the main column moves on. ----

  async function openPopout(id: string | null) {
    const key = id ?? '__new__';
    if (popoutPending.current.has(key)) return;
    const existing = id ? popoutsRef.current.find((p) => p.threadId === id) : undefined;
    if (existing) { existing.handle.win.focus(); return; }
    if (id && id === activeId && busy) { main.setError('Wait for the reply to finish before moving this chat to a window.'); return; }
    popoutPending.current.add(key);
    try {
      let threadId = id;
      let created = false;
      if (!threadId) {
        const t = await api<Thread>('/api/threads', { body: { model } });
        threadId = t.id;
        created = true;
        upsertThread(t);
      }
      const session = new ThreadSession(hooks.current, model);
      await session.load(threadId);
      const handle = await openPopoutWindow(threadId, () => onPopoutClosed(threadId!));
      if (!handle) {
        if (created) { await api(`/api/threads/${threadId}`, { method: 'DELETE' }).catch(() => {}); setThreads((ts) => ts.filter((t) => t.id !== threadId)); }
        main.setError('Popup blocked. Allow popups for this site to open the chat window.');
        return;
      }
      setPopouts((ps) => [...ps, { threadId: threadId!, session, handle, created, docked: false }]);
      if (main.state.threadId === threadId || !main.state.threadId) {
        main.reset();
        history.replaceState(null, '', location.pathname);
      }
    } finally {
      popoutPending.current.delete(key);
    }
  }

  /** The window went away on its own: forget it, and drop a popout-only thread that never got a message. */
  function onPopoutClosed(threadId: string) {
    const entry = popoutsRef.current.find((p) => p.threadId === threadId);
    setPopouts((ps) => ps.filter((p) => p.threadId !== threadId));
    if (!entry || entry.docked) return;
    entry.session.stop();
    if (entry.created && entry.session.state.messages.length === 0) {
      void api(`/api/threads/${threadId}`, { method: 'DELETE' }).catch(() => {});
      setThreads((ts) => ts.filter((t) => t.id !== threadId));
    }
  }

  /** Bring a popped thread back: close its window and show it in the main column with its latest transcript. */
  async function dock(entry: PopoutSession) {
    entry.docked = true;
    entry.session.stop();
    entry.handle.close();
    setPopouts((ps) => ps.filter((p) => p.threadId !== entry.threadId));
    window.focus();
    try {
      await main.load(entry.threadId);
      history.replaceState(null, '', `#${entry.threadId}`);
      stickBottom.current = true;
    } catch (err) {
      main.setError((err as Error).message);
    }
  }

  useEffect(() => {
    const el = scroller.current;
    if (el && stickBottom.current) el.scrollTop = el.scrollHeight;
  }, [messages, live, toolRuns]);

  function onScroll() {
    const el = scroller.current!;
    stickBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  }

  const closeSidebarIfNarrow = () => { if (isNarrow()) setSidebarOpen(false); };

  /** Drag the sidebar's right edge; the width lives in a CSS variable on the chat root. */
  /** Pointer-drag a width. `direction` is +1 when dragging right grows the panel, -1 when dragging left does. */
  function dragWidth(e: PointerEvent, which: 'sidebar' | 'ws', startW: number, direction: 1 | -1, clamp: (w: number) => number, apply: (w: number) => void, done: (w: number) => void) {
    if (isNarrow() || e.button !== 0) return;
    const handle = e.currentTarget as HTMLElement;
    const startX = e.clientX;
    let w = startW;
    handle.setPointerCapture(e.pointerId);
    setResizing(which);
    const move = (ev: PointerEvent) => { w = clamp(startW + direction * (ev.clientX - startX)); apply(w); };
    const up = () => {
      handle.removeEventListener('pointermove', move);
      handle.removeEventListener('pointerup', up);
      handle.removeEventListener('pointercancel', up);
      setResizing('');
      done(w);
    };
    handle.addEventListener('pointermove', move);
    handle.addEventListener('pointerup', up);
    handle.addEventListener('pointercancel', up);
    e.preventDefault();
  }
  function startResize(e: PointerEvent) {
    dragWidth(e, 'sidebar', sidebarWidth, 1, (w) => Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, w)), setSidebarWidth, (w) => {
      try { localStorage.setItem(SIDEBAR_KEY, String(w)); } catch {}
    });
  }
  function resetResize() {
    setSidebarWidth(SIDEBAR_DEFAULT);
    try { localStorage.removeItem(SIDEBAR_KEY); } catch {}
  }
  function wsMax() { return Math.max(WS_MIN, Math.floor(window.innerWidth * 0.7)); }
  function startWsResize(e: PointerEvent) {
    const current = wsWidth ?? (e.currentTarget as HTMLElement).nextElementSibling?.getBoundingClientRect().width ?? WS_MIN;
    dragWidth(e, 'ws', Math.round(current), -1, (w) => Math.min(wsMax(), Math.max(WS_MIN, w)), setWsWidth, (w) => {
      try { localStorage.setItem(WS_KEY, String(w)); } catch {}
    });
  }
  function resetWsResize() {
    setWsWidth(null);
    try { localStorage.removeItem(WS_KEY); } catch {}
  }

  async function openThread(id: string) {
    if (main.state.busy) return;
    try {
      await main.load(id);
      history.replaceState(null, '', `#${id}`);
      stickBottom.current = true;
    } catch (err) {
      main.setError((err as Error).message);
    }
  }

  function newChat() {
    if (busy) return;
    main.reset();
    history.replaceState(null, '', location.pathname);
    requestAnimationFrame(() => textRef.current?.focus());
  }

  async function changeModel(id: string) {
    main.setModel(id);
    localStorage.setItem(MODEL_KEY, id);
    if (activeId) {
      const t = await api<Thread>(`/api/threads/${activeId}`, { method: 'PATCH', body: { model: id } });
      setThreads((ts) => ts.map((x) => (x.id === t.id ? t : x)));
    }
  }

  async function deleteThread(id: string) {
    if (!confirm('Delete this chat?')) return;
    const popped = popoutsRef.current.find((p) => p.threadId === id);
    if (popped) { popped.docked = true; popped.session.stop(); popped.handle.close(); setPopouts((ps) => ps.filter((p) => p.threadId !== id)); }
    await api(`/api/threads/${id}`, { method: 'DELETE' });
    setThreads((ts) => ts.filter((t) => t.id !== id));
    if (id === activeId) newChat();
  }

  function startRename(t: Thread) {
    renameClosing.current = false;
    setRenamingId(t.id);
    setRenameDraft(t.title);
  }

  async function commitRename(save: boolean) {
    if (renameClosing.current) return;
    renameClosing.current = true;
    const id = renamingId;
    const title = renameDraft.trim();
    setRenamingId(null);
    if (!save || !id || !title) return;
    const t = threads.find((x) => x.id === id);
    if (!t || title === t.title) return;
    const nt = await api<Thread>(`/api/threads/${id}`, { method: 'PATCH', body: { title } });
    setThreads((ts) => ts.map((x) => (x.id === nt.id ? nt : x)));
  }

  function changeTools(off: Record<string, boolean>) {
    setToolsOff(off);
    writeToolsOff(off);
  }

  async function send(draft: Draft) {
    setSeed(null);
    stickBottom.current = true;
    const ok = await main.send(draft);
    if (!ok) setSeed({ text: draft.text, attachments: draft.attachments });
  }

  if (!ready) return <div class="kali-ai kali-ai-loading">Loading</div>;
  const u = user.value!;
  const empty = messages.length === 0 && !live;
  const remaining = usage.dailyLimit ? Math.max(0, usage.dailyLimit - usage.requestsToday) : 0;
  // Plan windows (or the free daily pool) drive the quota line. Admins are unlimited.
  const hasPlan = Boolean(planMe?.admin || planMe?.plan);
  const meters: Meter[] = planMe?.admin ? [] : planMe?.usage ? [planMe.usage.fiveHour, planMe.usage.weekly] : planMe?.free ? [planMe.free] : [];
  const fullMeter = meters.find((m) => m.used >= m.cap) ?? null;
  const shownMeter = meters.length ? meters.reduce((a, b) => (a.cap - a.used <= b.cap - b.used ? a : b)) : null;
  const quotaLine = !planMe || planMe.admin ? 'Unlimited' : shownMeter ? `${formatShekels(Math.max(0, shownMeter.cap - shownMeter.used))}/${formatShekels(shownMeter.cap)}` : 'Unlimited';
  const quotaScope = planMe?.usage ? 'left in this window' : 'left today';
  const planLabel = planMe?.admin ? 'Admin' : planMe?.plan?.name ?? 'Free';
  const limitReached = (usage.dailyLimit > 0 && remaining <= 0) || Boolean(fullMeter);
  const limitText = fullMeter ? `Limit reached. Resets in ${formatResetsIn(fullMeter.resetsAt)}` : 'Daily limit reached';
  const workspaceTitle = toolsSupported ? undefined : 'Workspace needs a browser with cross-origin isolation (SharedArrayBuffer) and OPFS';
  const activeSummary = threads.find((t) => t.id === activeId);
  const title = threadTitle(activeSummary, messages);
  const activePopout = popouts.find((p) => p.threadId === activeId);
  const q = search.trim().toLowerCase();
  const visible = q ? threads.filter((t) => (t.title || 'New chat').toLowerCase().includes(q)) : threads;
  const now = new Date();
  const groups = HISTORY_BUCKETS.map((label) => ({ label, threads: visible.filter((t) => historyBucket(t.updated, now) === label) })).filter((g) => g.threads.length > 0);
  const lastAssistantSeq = [...messages].reverse().find((m) => m.role === 'assistant')?.seq;

  const composer = (variant: 'hero' | 'docked') => (
    <Composer variant={variant} placeholder={limitReached ? limitText : 'Ask anything'}
      busy={busy} disabled={!model || limitReached} inputRef={textRef} seed={seed}
      onSend={send} onStop={() => main.stop()} onNotice={(t) => main.setError(t)} onOpenImage={(files, index) => setLightbox({ files, index })}
      models={models} model={model} hasPlan={hasPlan} onModelChange={changeModel}
      tools={tools} toolsSupported={toolsSupported} toolsOff={toolsOff} onToolsChange={changeTools} />
  );

  const renderChatRow = (t: Thread) => {
    const active = t.id === activeId;
    const popped = popouts.find((p) => p.threadId === t.id);
    if (renamingId === t.id) {
      return (
        <div key={t.id} class={`kali-chat renaming${active ? ' active' : ''}`}>
          <MessageSquare size={14} />
          <input aria-label="Chat title" autoFocus value={renameDraft} onInput={(e) => setRenameDraft((e.target as HTMLInputElement).value)}
            onBlur={() => commitRename(true)}
            onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); commitRename(true); } if (e.key === 'Escape') { e.preventDefault(); commitRename(false); } }} />
        </div>
      );
    }
    return (
      <div key={t.id} class={`kali-chat${active ? ' active' : ''}${popped ? ' popped' : ''}`}>
        <button type="button" class="kali-chat-main" onClick={() => { if (popped) dock(popped); else openThread(t.id); closeSidebarIfNarrow(); }}
          onDblClick={() => startRename(t)} onKeyDown={(e) => { if (e.key === 'F2') { e.preventDefault(); startRename(t); } }}>
          <MessageSquare size={14} /><span>{t.title || 'New chat'}</span>
        </button>
        <div class="kali-chat-actions">
          <IconButton label="Rename chat" onClick={() => startRename(t)}><Pencil size={13} /></IconButton>
          {popped
            ? <IconButton label="Dock back into the window" onClick={() => dock(popped)}><SquareArrowDownLeft size={13} /></IconButton>
            : <IconButton label="Open in its own window" onClick={() => openPopout(t.id)}><SquareArrowOutUpRight size={13} /></IconButton>}
          <IconButton label="Delete chat" class="danger" onClick={() => deleteThread(t.id)}><Trash2 size={13} /></IconButton>
        </div>
      </div>
    );
  };

  return (
    <>
      <section class={`kali-ai${sidebarOpen ? ' sidebar-open' : ''}${resizing ? ` resizing resizing-${resizing}` : ''}`}
        style={{ '--kali-sidebar-w': `${sidebarWidth}px`, ...(wsWidth ? { '--kali-ws-w': `${wsWidth}px` } : {}) } as any}>
        <aside class="kali-ai-sidebar">
          <button class="kali-new" type="button" onClick={() => { newChat(); closeSidebarIfNarrow(); }}><Plus size={16} />New chat</button>
          <label class="kali-chat-search"><Search size={14} /><input placeholder="Search chats" value={search} onInput={(e) => setSearch((e.target as HTMLInputElement).value)} /></label>
          <div class="kali-chat-list">
            {groups.map((g) => (
              <div class="kali-chat-group" key={g.label}>
                <small class="kali-label">{g.label}</small>
                {g.threads.map(renderChatRow)}
              </div>
            ))}
            {visible.length === 0 && q !== '' && <p class="kali-chat-none">No chats match your search</p>}
          </div>
          <footer>
            <span class="kali-footer-usage"><span>{quotaLine === 'Unlimited' ? 'Unlimited' : `${quotaLine} ${quotaScope}`}</span></span>
            <a class="kali-cs-toggle kali-cs-footer-toggle" href="/account"><Gauge size={13} />View usage</a>
            <a class="kali-cs-toggle kali-cs-footer-toggle" href="/account#acc-keys"><KeyRound size={13} />API keys</a>
          </footer>
        </aside>
        <div class="kali-sidebar-handle" role="separator" aria-orientation="vertical" aria-label="Resize chat list" title="Drag to resize, double-click to reset"
          onPointerDown={startResize} onDblClick={resetResize} />
        <div class="kali-ai-scrim" aria-hidden="true" onClick={() => setSidebarOpen(false)} />

        <main class={`kali-ai-main${empty ? ' is-welcome' : ''}`}>
          <header>
            <div class="kali-ai-head">
              <IconButton class="kali-head-btn" label={sidebarOpen ? 'Hide chats' : 'Show chats'} aria-expanded={sidebarOpen} onClick={() => setSidebarOpen((o) => !o)}>
                {sidebarOpen ? <PanelLeftClose size={17} /> : <PanelLeftOpen size={17} />}
              </IconButton>
              {!empty && (
                <div class="kali-ai-title">
                  <strong>{title}</strong>
                  <small>{modelName(model)} · {planLabel}{quotaLine === 'Unlimited' ? '' : ` · ${quotaLine} ${quotaScope}`}</small>
                </div>
              )}
            </div>
            <div class="kali-header-tools">
              <IconButton class="kali-share-btn" label={workspaceTitle ?? 'Files'} disabled={!toolsSupported} onClick={() => setPane('files')}><Files size={15} /></IconButton>
              <IconButton class="kali-share-btn" label={workspaceTitle ?? 'Terminal'} disabled={!toolsSupported} onClick={() => setPane('shell')}><SquareTerminal size={15} /></IconButton>
              <a class={`kali-plan-badge ${hasPlan ? 'paid' : 'free'}`} href={hasPlan ? '/account#acc-plan' : '/pricing'} title={hasPlan ? 'Plan and usage' : 'See plans'}><Sparkles size={13} /> {planLabel}</a>
              {activePopout
                ? <IconButton class="kali-share-btn active" label="Bring this chat back into the window" onClick={() => dock(activePopout)}><SquareArrowDownLeft size={15} /></IconButton>
                : <IconButton class="kali-share-btn" label="Open this chat in its own window" disabled={busy} onClick={() => openPopout(activeId)}><SquareArrowOutUpRight size={15} /></IconButton>}
              <IconButton class="kali-share-btn" label="Screen sharing is not available" disabled><Monitor size={15} /></IconButton>
            </div>
          </header>

          <div class={`kali-chat-scroll${empty ? ' is-empty' : ''}`} ref={scroller} onScroll={onScroll}>
            {empty && (
              <div class="kali-empty">
                <div class="kali-empty-hero">
                  <span class="kali-empty-mark" aria-hidden="true"><Syringe size={26} /></span>
                  <h1>Estro</h1>
                  <p class="kali-empty-sub">What can I help with?</p>
                </div>
                {composer('hero')}
              </div>
            )}
            {messages.map((m) => m.role === 'user'
              ? <UserMessage key={m.seq ?? m.content} msg={m} busy={busy} onOpenImage={(files, index) => setLightbox({ files, index })}
                  onEdit={m.seq ? (text) => main.edit(m, text) : undefined}
                  onDelete={m.seq ? () => { if (confirm('Delete this message and everything after it?')) void main.deleteMessage(m); } : undefined} />
              : m.role === 'assistant'
                ? <AssistantMessage key={m.seq ?? m.content} msg={m} modelName={modelName(m.model)} runs={toolRuns} busy={busy}
                    onRetry={m.seq && m.seq === lastAssistantSeq ? () => main.retry(m) : undefined} />
                : null)}
            {live && <AssistantMessage msg={live} modelName={modelName(live.model)} runs={toolRuns} busy={busy} />}
          </div>

          {error && (
            <div class="kali-error" role="alert">
              <span class="kali-error-text">{error}</span>
              {errorAction && <a class="kali-error-action" href={errorAction.href}>{errorAction.label}</a>}
              <button type="button" class="kali-error-close" title="Dismiss" aria-label="Dismiss error" onClick={() => main.setError('')}><X size={14} /></button>
            </div>
          )}

          {!empty && composer('docked')}
        </main>
        {pane && <div class="kali-ws-handle" role="separator" aria-orientation="vertical" aria-label="Resize workspace" title="Drag to resize, double-click to reset"
          onPointerDown={startWsResize} onDblClick={resetWsResize} />}
        {pane && <WorkspacePane tab={pane} onTabChange={setPane} onClose={() => setPane(null)} />}
      </section>

      {lightbox && <Lightbox files={lightbox.files} index={lightbox.index} onIndex={(index) => setLightbox({ ...lightbox, index })} onClose={() => setLightbox(null)} />}
      {popouts.map((entry) => (
        <PopoutPortal key={entry.threadId} entry={entry} thread={threads.find((t) => t.id === entry.threadId)} modelName={modelName}
          onDock={() => dock(entry)} onOpenImage={(files, index) => { window.focus(); setLightbox({ files, index }); }} />
      ))}
    </>
  );
}
