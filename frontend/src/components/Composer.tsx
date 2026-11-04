import { useEffect, useRef, useState } from 'preact/hooks';
import type { ComponentChildren, RefObject } from 'preact';
import { ArrowUp, Paperclip, Square } from 'lucide-preact';
import type { Attachment, ToolDef } from '../lib/workspace-contract';
import { filesFromDataTransfer, readAttachments } from '../lib/attachments';
import ModelMenu, { type MenuModel } from './chat/ModelMenu';
import ToolsMenu from './chat/ToolsMenu';
import { AttachmentChips } from './chat/Messages';

export interface Draft { text: string; attachments: Attachment[]; }

interface Props {
  variant: 'hero' | 'docked';
  placeholder: string;
  busy: boolean;
  disabled?: boolean;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  /** Replaces the draft (used when an edit is cancelled back into the composer). */
  seed?: Draft | null;
  onSend: (draft: Draft) => void;
  onStop: () => void;
  onNotice?: (text: string) => void;
  onOpenImage: (attachments: Attachment[], index: number) => void;
  models: MenuModel[];
  model: string;
  /** False locks the models that need a plan. */
  hasPlan?: boolean;
  onModelChange: (id: string) => void;
  tools: ToolDef[];
  toolsSupported: boolean;
  toolsOff: Record<string, boolean>;
  onToolsChange: (off: Record<string, boolean>) => void;
  extra?: ComponentChildren;
}

export default function Composer({ variant, placeholder, busy, disabled, inputRef, seed, onSend, onStop, onNotice, onOpenImage, models, model, hasPlan = true, onModelChange, tools, toolsSupported, toolsOff, onToolsChange, extra }: Props) {
  const [text, setText] = useState('');
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!seed) return;
    setText(seed.text);
    setAttachments(seed.attachments);
    requestAnimationFrame(() => inputRef.current?.focus());
  }, [seed]);

  async function addFiles(files: File[]) {
    if (!files.length) return;
    const { attachments: added, errors } = await readAttachments(files, attachments.length);
    if (added.length) setAttachments((a) => [...a, ...added]);
    if (errors.length) onNotice?.(errors.join(' '));
  }

  function send() {
    const t = text.trim();
    if ((!t && !attachments.length) || busy || disabled) return;
    onSend({ text: t, attachments });
    setText('');
    setAttachments([]);
  }

  const canSend = (text.trim().length > 0 || attachments.length > 0) && !disabled;

  return (
    <form class={`kali-composer ${variant}`} onSubmit={(e) => { e.preventDefault(); send(); }}
      onDragOver={(e) => e.preventDefault()} onDrop={(e) => { e.preventDefault(); addFiles(filesFromDataTransfer(e.dataTransfer)); }}>
      <AttachmentChips files={attachments} onRemove={(i) => setAttachments((s) => s.filter((_, j) => j !== i))} onOpen={(i) => onOpenImage(attachments, i)} />
      <div class="kali-composer-field">
        <textarea ref={inputRef} rows={1} placeholder={placeholder} value={text} disabled={disabled}
          onInput={(e) => setText((e.target as HTMLTextAreaElement).value)}
          onPaste={(e) => { const files = filesFromDataTransfer(e.clipboardData); if (files.length) { e.preventDefault(); addFiles(files); } }}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey && !(e as any).isComposing) { e.preventDefault(); send(); } }} />
      </div>
      <div class="kali-composer-bar">
        <div class="kali-composer-left">
          <button type="button" class="kali-icon-btn" title="Attach file" aria-label="Attach file" onClick={() => fileRef.current?.click()}><Paperclip size={15} /></button>
          <input ref={fileRef} type="file" multiple hidden onChange={(e) => { const el = e.target as HTMLInputElement; addFiles(Array.from(el.files ?? [])); el.value = ''; }} />
          <ModelMenu models={models} value={model} hasPlan={hasPlan} onChange={onModelChange} />
          <ToolsMenu tools={tools} supported={toolsSupported} off={toolsOff} onChange={onToolsChange} />
        </div>
        {busy
          ? <button type="button" class="kali-send stop" title="Stop generating" aria-label="Stop generating" onClick={onStop}><Square size={12} fill="currentColor" /></button>
          : <button type="submit" class="kali-send" title="Send message" aria-label="Send message" disabled={!canSend}><ArrowUp size={17} /></button>}
      </div>
      {extra}
    </form>
  );
}
