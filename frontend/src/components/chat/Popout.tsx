import { useEffect, useRef, useState } from 'preact/hooks';
import { ArrowUp, PictureInPicture2, Square, Syringe } from 'lucide-preact';
import { AssistantMessage, IconButton, UserMessage, type Msg } from './Messages';
import type { ToolRun } from './Trace';

// A thread in its own window, the way Abstract AI does it: a Document
// Picture-in-Picture window when the browser has it, else a classic popup.
// The view is portalled from the chat page, so it shares the page's live state
// and send path; only the window and its styles are separate.

const GEOM_KEY = 'kali:popout-window';

export interface PopoutHandle {
  win: Window;
  target: HTMLElement;
  close(): void;
}

function readGeom(): { w: number; h: number } {
  try {
    const raw = localStorage.getItem(GEOM_KEY);
    if (raw) {
      const p = JSON.parse(raw) as { w?: number; h?: number };
      if (p && typeof p.w === 'number' && typeof p.h === 'number' && p.w >= 320 && p.h >= 200) return { w: p.w, h: p.h };
    }
  } catch {}
  return { w: 430, h: 520 };
}

function writeGeom(w: number, h: number): void {
  if (w < 200 || h < 150) return;
  try { localStorage.setItem(GEOM_KEY, JSON.stringify({ w, h })); } catch {}
}

/**
 * Copies the page's stylesheets, fonts and theme attributes into the new
 * document. Same-origin sheets are inlined as text: an about:blank popup under
 * this page's COOP/COEP does not reliably load them through <link>.
 */
async function adoptStyles(doc: Document): Promise<void> {
  const links = Array.from(document.head.querySelectorAll<HTMLLinkElement>('link[rel="preconnect"], link[rel="stylesheet"]'));
  await Promise.all(links.map(async (link) => {
    const href = new URL(link.getAttribute('href') ?? '', location.href);
    if (link.rel === 'stylesheet' && href.origin === location.origin) {
      try {
        const css = await (await fetch(href.href)).text();
        const style = doc.createElement('style');
        style.textContent = css;
        doc.head.appendChild(style);
        return;
      } catch {}
    }
    const el = doc.createElement('link');
    el.rel = link.rel;
    el.href = href.href;
    if (link.hasAttribute('crossorigin')) el.crossOrigin = '';
    doc.head.appendChild(el);
  }));
  for (const style of Array.from(document.querySelectorAll('style'))) {
    doc.head.appendChild(doc.importNode(style, true));
  }
  const src = document.documentElement;
  for (const attr of Array.from(src.attributes)) {
    if (attr.name.startsWith('data-') || attr.name === 'style' || attr.name === 'lang') doc.documentElement.setAttribute(attr.name, attr.value);
  }
}

const POPOUT_CSS = `
html, body { margin: 0; height: 100%; overflow: hidden; }
body { background: var(--theme-bg, #111); color: var(--theme-fg, #eee); font-family: 'Instrument Sans', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; font-size: 14px; }
.kali-popout { display: flex; flex-direction: column; height: 100%; }
.kali-popout-head { display: flex; align-items: center; gap: 8px; flex: 0 0 auto; height: 40px; padding: 0 8px 0 12px; border-bottom: 1px solid color-mix(in srgb, var(--theme-fg, #eee) 14%, transparent); }
.kali-popout-head strong { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 600; }
.kali-popout-head small { color: color-mix(in srgb, var(--theme-fg, #eee) 55%, transparent); font-size: 11px; white-space: nowrap; }
.kali-popout-head button { display: grid; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 7px; background: transparent; color: color-mix(in srgb, var(--theme-fg, #eee) 65%, transparent); cursor: pointer; }
.kali-popout-head button:hover { background: color-mix(in srgb, var(--theme-fg, #eee) 10%, transparent); color: var(--theme-fg, #eee); }
.kali-popout-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 12px 12px 8px; }
.kali-popout-scroll .kali-message { max-width: 100%; }
.kali-popout-empty { display: grid; place-items: center; height: 100%; color: color-mix(in srgb, var(--theme-fg, #eee) 55%, transparent); font-size: 13px; }
.kali-popout-composer { display: flex; align-items: flex-end; gap: 6px; flex: 0 0 auto; padding: 8px; border-top: 1px solid color-mix(in srgb, var(--theme-fg, #eee) 14%, transparent); }
.kali-popout-composer textarea { flex: 1; min-height: 36px; max-height: 140px; padding: 8px 10px; resize: none; border: 1px solid color-mix(in srgb, var(--theme-fg, #eee) 16%, transparent); border-radius: 12px; background: color-mix(in srgb, var(--theme-fg, #eee) 6%, transparent); color: inherit; font: inherit; outline: none; }
.kali-popout-composer textarea:focus { border-color: var(--theme-accent, #e23a3a); }
.kali-popout-send { display: grid; place-items: center; width: 34px; height: 34px; padding: 0; border: 0; border-radius: 999px; background: var(--theme-accent, #e23a3a); color: var(--theme-on-accent, #fff); cursor: pointer; }
.kali-popout-send:disabled { opacity: .45; cursor: default; }
.kali-popout-send.stop { background: color-mix(in srgb, var(--theme-fg, #eee) 14%, transparent); color: var(--theme-fg, #eee); }
`;

type PipWindow = Window & { documentPictureInPicture?: { requestWindow(o?: { width?: number; height?: number }): Promise<Window> } };

/** Opens the window and returns its mount point, or null when the browser blocked it. */
export async function openPopoutWindow(threadId: string, onClosed: () => void): Promise<PopoutHandle | null> {
  const geom = readGeom();
  let win: Window | null = null;
  const pip = (window as PipWindow).documentPictureInPicture;
  if (pip) {
    try { win = await pip.requestWindow({ width: geom.w, height: geom.h }); } catch { win = null; }
  }
  if (!win) {
    const left = Math.max(24, window.screenX + window.outerWidth - geom.w - 28);
    const top = Math.max(24, window.screenY + 48);
    win = window.open('', `kali-thread-${threadId}`, `popup=yes,width=${geom.w},height=${geom.h},left=${left},top=${top},resizable=yes,scrollbars=no,status=no`);
  }
  if (!win) return null;

  const doc = win.document;
  if (!pip || doc.body === null) {
    doc.open();
    doc.write('<!doctype html><html lang="en"><head><meta charset="utf-8" /><meta name="viewport" content="width=device-width, initial-scale=1" /><title>Estro</title></head><body></body></html>');
    doc.close();
  }
  doc.title = 'Estro';
  await adoptStyles(doc);
  const style = doc.createElement('style');
  style.textContent = POPOUT_CSS;
  doc.head.appendChild(style);
  const target = doc.createElement('div');
  target.style.display = 'contents';
  doc.body.appendChild(target);

  let closed = false;
  const finish = () => { if (closed) return; closed = true; onClosed(); };
  win.addEventListener('resize', () => writeGeom(win!.innerWidth, win!.innerHeight));
  win.addEventListener('pagehide', finish);
  win.addEventListener('unload', finish);
  // Popups closed from the window manager do not always fire pagehide.
  const poll = window.setInterval(() => { if (win!.closed) { window.clearInterval(poll); finish(); } }, 500);

  return { win, target, close: () => { window.clearInterval(poll); try { win!.close(); } catch {} finish(); } };
}

interface ViewProps {
  title: string;
  messages: Msg[];
  live: Msg | null;
  runs: Record<string, ToolRun>;
  busy: boolean;
  modelName: (id?: string | null) => string;
  onSend: (text: string) => void;
  onStop: () => void;
  onDock: () => void;
  onOpenImage: (files: Msg['data'] extends infer D ? (D extends { attachments?: infer A } ? NonNullable<A> : never) : never, index: number) => void;
}

export function PopoutView({ title, messages, live, runs, busy, modelName, onSend, onStop, onDock, onOpenImage }: ViewProps) {
  const [text, setText] = useState('');
  const scroller = useRef<HTMLDivElement>(null);
  useEffect(() => { const el = scroller.current; if (el) el.scrollTop = el.scrollHeight; }, [messages, live, runs]);

  const send = () => {
    const t = text.trim();
    if (!t || busy) return;
    onSend(t);
    setText('');
  };
  const empty = messages.length === 0 && !live;

  return (
    <div class="kali-popout">
      <div class="kali-popout-head">
        <Syringe size={14} aria-hidden="true" />
        <strong title={title}>{title}</strong>
        <small>{modelName(live?.model ?? messages[messages.length - 1]?.model)}</small>
        <IconButton label="Return to the main window" onClick={onDock}><PictureInPicture2 size={15} /></IconButton>
      </div>
      <div class="kali-popout-scroll kali-chat-scroll" ref={scroller}>
        {empty && <div class="kali-popout-empty">Say something to start this chat.</div>}
        {messages.map((m) => m.role === 'user'
          ? <UserMessage key={m.seq ?? m.content} msg={m} busy={busy} onOpenImage={onOpenImage as never} />
          : m.role === 'assistant'
            ? <AssistantMessage key={m.seq ?? m.content} msg={m} modelName={modelName(m.model)} runs={runs} busy={busy} />
            : null)}
        {live && <AssistantMessage msg={live} modelName={modelName(live.model)} runs={runs} busy={busy} />}
      </div>
      <form class="kali-popout-composer" onSubmit={(e) => { e.preventDefault(); send(); }}>
        <textarea rows={1} placeholder="Ask anything" value={text}
          onInput={(e) => { const el = e.target as HTMLTextAreaElement; setText(el.value); el.style.height = 'auto'; el.style.height = Math.min(el.scrollHeight, 140) + 'px'; }}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey && !(e as KeyboardEvent & { isComposing?: boolean }).isComposing) { e.preventDefault(); send(); } }} />
        {busy
          ? <button type="button" class="kali-popout-send stop" aria-label="Stop" title="Stop" onClick={onStop}><Square size={14} /></button>
          : <button type="submit" class="kali-popout-send" aria-label="Send" title="Send" disabled={!text.trim()}><ArrowUp size={16} /></button>}
      </form>
    </div>
  );
}
