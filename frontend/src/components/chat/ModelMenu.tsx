import { useEffect, useRef, useState } from 'preact/hooks';
import { Check, ChevronDown, Search, Sparkles } from 'lucide-preact';

export interface MenuModel {
  id: string;
  name: string;
  description: string;
  available: boolean;
}

/** The model picker: a pill in the composer toolbar; the menu opens beside it with a search box. */
export default function ModelMenu({ models, value, onChange }: { models: MenuModel[]; value: string; onChange: (id: string) => void }) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState('');
  const root = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const active = models.find((m) => m.id === value);

  useEffect(() => {
    if (!open) return;
    searchRef.current?.focus({ preventScroll: true });
    const onDown = (e: MouseEvent) => { if (!root.current?.contains(e.target as Node)) setOpen(false); };
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('mousedown', onDown); document.removeEventListener('keydown', onKey); };
  }, [open]);

  const terms = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const shown = terms.length ? models.filter((m) => terms.every((t) => `${m.name} ${m.description} ${m.id}`.toLowerCase().includes(t))) : models;
  const pick = (m: MenuModel) => { if (!m.available) return; setOpen(false); onChange(m.id); };

  return (
    <div class="kali-model" ref={root}>
      <button type="button" aria-haspopup="true" aria-expanded={open} onClick={() => { setSearch(''); setOpen(!open); }}>
        <Sparkles size={14} />
        <span class="kali-model-name">{active ? active.name : 'Choose a model'}</span>
        <ChevronDown size={14} />
      </button>
      {open && (
        <div class="kali-model-menu">
          <label class="kali-model-search">
            <Search size={14} aria-hidden="true" />
            <input ref={searchRef} type="search" placeholder="Search models" aria-label="Search models" value={search} autoComplete="off" spellcheck={false}
              onInput={(e) => setSearch((e.target as HTMLInputElement).value)}
              onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); const first = shown.find((m) => m.available); if (first) pick(first); } }} />
          </label>
          <div class="kali-model-list">
            {shown.length === 0 && <div class="kali-model-empty">No models match</div>}
            {shown.map((m) => (
              <button key={m.id} type="button" class={`${m.id === value ? 'active' : ''}${m.available ? '' : ' locked'}`} data-model-id={m.id}
                title={m.available ? undefined : 'This model is offline right now'} onClick={() => pick(m)}>
                <span><strong>{m.name}</strong><small>{m.description}</small></span>
                <span class="kali-model-row-icons">{m.id === value && <Check size={14} />}</span>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
