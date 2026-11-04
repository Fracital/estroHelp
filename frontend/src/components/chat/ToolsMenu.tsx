import { useEffect, useState } from 'preact/hooks';
import { createPortal } from 'preact/compat';
import type { ComponentType } from 'preact';
import { Check, ChevronDown, ChevronRight, Eye, FolderSearch, Minus, Pencil, Search, SlidersHorizontal, Terminal, Trash2 } from 'lucide-preact';
import type { ToolDef } from '../../lib/workspace-contract';
import '../../styles/tools-menu.css';

// The Tools menu: VS Code's "Configure Tools" list, opened from the composer.
// The checked set is what each message carries as `tools`.

const TOOL_ICONS: Record<string, ComponentType<{ size?: number; class?: string }>> = {
  run_shell: Terminal, read_file: Eye, list_dir: Eye, write_file: Pencil, delete_path: Trash2, search_files: Search, find_files: FolderSearch,
};

export const TOOLS_KEY = 'kali:tools-off';
const TOOLS_NOTE = 'The selected tools will be applied globally for all chat sessions that use the default agent.';

/** Only "off" is stored; a tool with no entry is on. */
export function readToolsOff(): Record<string, boolean> {
  try {
    const raw = JSON.parse(localStorage.getItem(TOOLS_KEY) ?? '{}');
    return raw && typeof raw === 'object' ? raw : {};
  } catch { return {}; }
}
export function writeToolsOff(off: Record<string, boolean>) {
  try { localStorage.setItem(TOOLS_KEY, JSON.stringify(off)); } catch {}
}
export function toolOn(off: Record<string, boolean>, id: string): boolean { return off[id] !== false; }

type Check3 = 'on' | 'off' | 'mixed';
function stateOf(ids: string[], off: Record<string, boolean>): Check3 {
  const on = ids.filter((id) => toolOn(off, id)).length;
  if (on === 0) return 'off';
  return on === ids.length ? 'on' : 'mixed';
}

function ToolRow({ depth, state, icon: Icon, label, description, expandable, expanded, onExpand, onToggle }: {
  depth: 0 | 1 | 2; state: Check3; icon?: ComponentType<{ size?: number; class?: string }>; label: string; description?: string;
  expandable?: boolean; expanded?: boolean; onExpand?: () => void; onToggle: () => void;
}) {
  return (
    <div class={`kali-tools-row d${depth}`}>
      {expandable
        ? <button type="button" class="kali-tools-chev" aria-expanded={expanded} onClick={onExpand}>{expanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}</button>
        : <span class="kali-tools-chev-spacer" aria-hidden="true" />}
      <button type="button" role="checkbox" aria-checked={state === 'mixed' ? 'mixed' : state === 'on'} class="kali-tools-pick" onClick={onToggle}>
        <span class={`kali-tools-box ${state}`} aria-hidden="true">
          {state === 'on' && <Check size={9} strokeWidth={3.5} />}
          {state === 'mixed' && <Minus size={9} strokeWidth={3.5} />}
        </span>
        {Icon && <Icon size={14} class="kali-tools-icon" />}
        <span class="kali-tools-label">{label}</span>
        {description && <span class="kali-tools-desc">{description}</span>}
      </button>
    </div>
  );
}

export default function ToolsMenu({ tools, supported, off, onChange }: { tools: ToolDef[]; supported: boolean; off: Record<string, boolean>; onChange: (off: Record<string, boolean>) => void }) {
  const [open, setOpen] = useState(false);
  const [host, setHost] = useState<HTMLElement | null>(null);
  const [query, setQuery] = useState('');
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(['workspace']));

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    const onDown = (e: MouseEvent) => {
      const t = e.target as Element | null;
      if (t?.closest?.('.kali-tools-pop, .kali-tools-trigger')) return;
      setOpen(false);
    };
    document.addEventListener('keydown', onKey);
    document.addEventListener('mousedown', onDown);
    return () => { document.removeEventListener('keydown', onKey); document.removeEventListener('mousedown', onDown); };
  }, [open]);

  const ids = tools.map((t) => t.function.name);
  const selected = supported ? ids.filter((id) => toolOn(off, id)).length : 0;
  const q = query.trim().toLowerCase();
  const hit = (s: string) => !q || s.toLowerCase().includes(q);
  const visible = hit('workspace') ? tools : tools.filter((t) => hit(t.function.name) || hit(t.function.description));
  const isExpanded = (id: string) => Boolean(q) || expanded.has(id);
  const toggleExpanded = (id: string) => setExpanded((cur) => { const n = new Set(cur); n.has(id) ? n.delete(id) : n.add(id); return n; });
  const apply = (targets: string[], on: boolean) => {
    const next = { ...off };
    for (const id of targets) { if (on) delete next[id]; else next[id] = false; }
    onChange(next);
  };
  const groupState = stateOf(ids, off);

  return (
    <>
      <button type="button" class="kali-icon-btn kali-tools-trigger" title="Configure tools" aria-label="Configure tools" aria-haspopup="dialog" aria-expanded={open} onClick={(e) => { if (!open) setHost((e.currentTarget as HTMLElement).closest('.kali-ai') as HTMLElement | null); setOpen(!open); }}>
        <SlidersHorizontal size={15} />
      </button>
      {open && host && createPortal(
        <section class="kali-tools-pop" role="dialog" aria-labelledby="kali-tools-title">
          <h2 id="kali-tools-title" class="kali-tools-title">Configure Tools</h2>
          <div class="kali-tools-search">
            <div class="kali-tools-field">
              <input type="search" autoFocus value={query} onInput={(e) => setQuery((e.target as HTMLInputElement).value)} placeholder="Select tools that are available to chat." />
              <span class="kali-tools-count">{selected} Selected</span>
            </div>
            <button type="button" class="kali-cs-primary" onClick={() => setOpen(false)}><span>OK</span></button>
          </div>
          <p class="kali-tools-note">{supported ? TOOLS_NOTE : 'Workspace tools need a browser with cross-origin isolation (SharedArrayBuffer) and OPFS, so they are unavailable here.'}</p>
          <div class="kali-tools-list">
            <ToolRow depth={0} state={supported ? groupState : 'off'} label="Workspace" expandable expanded={isExpanded('workspace')}
              onExpand={() => toggleExpanded('workspace')} onToggle={() => supported && apply(ids, groupState !== 'on')} />
            {isExpanded('workspace') && visible.map((t) => (
              <ToolRow key={t.function.name} depth={1} state={supported ? stateOf([t.function.name], off) : 'off'} icon={TOOL_ICONS[t.function.name]}
                label={t.function.name} description={t.function.description}
                onToggle={() => supported && apply([t.function.name], !toolOn(off, t.function.name))} />
            ))}
          </div>
        </section>,
        host,
      )}
    </>
  );
}
