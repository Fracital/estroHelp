import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { Check, ChevronLeft, ChevronRight, Copy, File as FileIcon, FileText, Pencil, RotateCcw, Sparkles, Trash2, X } from 'lucide-preact';
import type { Attachment, MessageData, MessageRole } from '../../lib/workspace-contract';
import { onMarkdownClick, renderMarkdown } from '../../lib/markdown';
import { TraceView, type ToolRun } from './Trace';

export interface Msg {
  seq?: number;
  role: MessageRole;
  content: string;
  model?: string | null;
  data?: MessageData | null;
  /** Live-only: a reply still streaming. */
  streaming?: boolean;
  /** Live-only: reasoning is still arriving. */
  thinking?: boolean;
}

export function IconButton({ label, class: cls, children, ...rest }: { label: string; class?: string; children: any; [k: string]: any }) {
  return <button type="button" class={cls} title={label} aria-label={label} {...rest}>{children}</button>;
}

export function AttachmentThumb({ file }: { file: Attachment }) {
  if (file.kind === 'image' && file.dataUrl) return <img src={file.dataUrl} alt={file.name} />;
  const text = file.kind === 'file';
  return (
    <span class="kali-file-thumb" title={file.name}>
      {text ? <FileText size={26} /> : <FileIcon size={26} />}
      <em>{file.name.split('.').pop()?.toUpperCase().slice(0, 5)}</em>
    </span>
  );
}

export function AttachmentChips({ files, onRemove, onOpen }: { files: Attachment[]; onRemove: (index: number) => void; onOpen: (index: number) => void }) {
  if (!files.length) return null;
  return (
    <div class="kali-attach-row">
      {files.map((file, i) => (
        <div class="kali-attach-chip" key={i} role="button" tabIndex={0} title={`View ${file.name}`} onClick={() => onOpen(i)}
          onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onOpen(i); } }}>
          <AttachmentThumb file={file} />
          <button type="button" class="kali-attach-remove" title="Remove" aria-label={`Remove ${file.name}`} onClick={(e) => { e.stopPropagation(); onRemove(i); }}><X size={12} /></button>
        </div>
      ))}
    </div>
  );
}

export function MessageAttachments({ files, onOpen }: { files: Attachment[]; onOpen: (index: number) => void }) {
  if (!files.length) return null;
  return (
    <div class="kali-message-attachments">
      {files.map((file, i) => <button key={i} type="button" title={`View ${file.name}`} onClick={() => onOpen(i)}><AttachmentThumb file={file} /></button>)}
    </div>
  );
}

export function Lightbox({ files, index, onIndex, onClose }: { files: Attachment[]; index: number; onIndex: (i: number) => void; onClose: () => void }) {
  const file = files[index];
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
      if (e.key === 'ArrowLeft') onIndex((index - 1 + files.length) % files.length);
      if (e.key === 'ArrowRight') onIndex((index + 1) % files.length);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [index, files.length]);
  if (!file) return null;
  const stop = (e: Event) => e.stopPropagation();
  const image = file.kind === 'image' && file.dataUrl;
  return (
    <div class="kali-lightbox" role="dialog" aria-modal="true" aria-label={file.name} onClick={onClose}>
      <div class="kali-lightbox-top" onClick={stop}>
        <span class="kali-lightbox-count">{index + 1} of {files.length}</span>
        <span class="kali-lightbox-name" title={file.name}>{file.name}</span>
        <button type="button" title="Close" aria-label="Close viewer" onClick={onClose}><X size={18} /></button>
      </div>
      {files.length > 1 && <button type="button" class="kali-lightbox-nav prev" aria-label="Previous image" onClick={(e) => { e.stopPropagation(); onIndex((index - 1 + files.length) % files.length); }}><ChevronLeft size={24} /></button>}
      {image
        ? <img class="kali-lightbox-img" src={file.dataUrl} alt={file.name} onClick={stop} />
        : <div class="kali-lightbox-file" onClick={stop}><AttachmentThumb file={file} /><span class="kali-lightbox-file-name">{file.name}</span><span class="kali-lightbox-file-mime">{file.mime || 'unknown type'}</span></div>}
      {files.length > 1 && <button type="button" class="kali-lightbox-nav next" aria-label="Next image" onClick={(e) => { e.stopPropagation(); onIndex((index + 1) % files.length); }}><ChevronRight size={24} /></button>}
      {files.length > 1 && (
        <div class="kali-lightbox-strip" onClick={stop}>
          {files.map((f, i) => <button key={i} type="button" class={i === index ? 'active' : ''} title={f.name} onClick={() => onIndex(i)}><AttachmentThumb file={f} /></button>)}
        </div>
      )}
    </div>
  );
}

export function UserMessage({ msg, busy, onEdit, onDelete, onOpenImage }: { msg: Msg; busy: boolean; onEdit?: (text: string) => void; onDelete?: () => void; onOpenImage: (files: Attachment[], i: number) => void }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(msg.content);
  const files = msg.data?.attachments ?? [];
  const save = () => { if (!draft.trim()) return; setEditing(false); onEdit?.(draft.trim()); };
  return (
    <article class="kali-message user">
      {files.length > 0 && <MessageAttachments files={files} onOpen={(i) => onOpenImage(files, i)} />}
      {editing ? (
        <div class="kali-msg-edit">
          <textarea rows={3} value={draft} onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); save(); } if (e.key === 'Escape') { setEditing(false); setDraft(msg.content); } }} />
          <div class="kali-msg-edit-bar">
            <button type="button" onClick={() => { setEditing(false); setDraft(msg.content); }}>Cancel</button>
            <button type="button" disabled={!draft.trim() || busy} onClick={save}>Save &amp; submit</button>
          </div>
        </div>
      ) : (
        <>
          <p>{msg.content}</p>
          <div class="kali-msg-actions">
            <IconButton label="Copy message" onClick={() => navigator.clipboard?.writeText(msg.content).catch(() => {})}><Copy size={13} /></IconButton>
            {onEdit && <IconButton label="Edit message" disabled={busy} onClick={() => { setDraft(msg.content); setEditing(true); }}><Pencil size={13} /></IconButton>}
            {onDelete && <IconButton label="Delete message" disabled={busy} onClick={onDelete}><Trash2 size={13} /></IconButton>}
          </div>
        </>
      )}
    </article>
  );
}

export function AssistantMessage({ msg, modelName, runs, busy, onRetry }: { msg: Msg; modelName: string; runs?: Record<string, ToolRun>; busy: boolean; onRetry?: () => void }) {
  const [copied, setCopied] = useState(false);
  const html = useMemo(() => (msg.streaming ? '' : renderMarkdown(msg.content)), [msg.content, msg.streaming]);
  const calls = msg.data?.toolCalls ?? [];
  const hasTrace = Boolean(msg.data?.reasoning?.trim()) || calls.length > 0;
  return (
    <article class="kali-message assistant">
      <div class="kali-assistant"><Sparkles size={15} /><span>{modelName}</span></div>
      {hasTrace && <TraceView thinking={msg.data?.reasoning} thinkingActive={Boolean(msg.thinking)} thinkingMs={msg.data?.reasoningMs} calls={calls} runs={runs} />}
      {msg.streaming
        ? (msg.content
          ? <div class="kali-streaming"><div class="kali-markdown kali-stream-text">{msg.content}</div></div>
          : !hasTrace && <i class="kali-thinking"><b /><b /><b /></i>)
        : msg.content && <div class="kali-markdown" dangerouslySetInnerHTML={{ __html: html }} onClick={onMarkdownClick} />}
      {!msg.streaming && (
        <footer>
          <div class="kali-msg-actions">
            {onRetry && <IconButton label="Retry response" disabled={busy} onClick={onRetry}><RotateCcw size={14} /></IconButton>}
            <IconButton label="Copy response" onClick={() => { navigator.clipboard?.writeText(msg.content); setCopied(true); setTimeout(() => setCopied(false), 1200); }}>
              {copied ? <Check size={14} /> : <Copy size={14} />}
            </IconButton>
          </div>
        </footer>
      )}
    </article>
  );
}

/** Scrolls a container to the bottom while content streams, unless the user scrolled up. */
export function useStickToBottom(ref: { current: HTMLElement | null }, deps: unknown[]) {
  const stick = useRef(true);
  useEffect(() => { const el = ref.current; if (el && stick.current) el.scrollTop = el.scrollHeight; }, deps);
  return {
    onScroll: () => { const el = ref.current!; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80; },
    reset: () => { stick.current = true; },
  };
}
