import { useEffect, useRef, useState } from 'preact/hooks';
import { INTEGRATIONS, buildIntegrations, anchorFor, iconUrl, type Integration, type IntegrationConfig, type ModelRow } from '../lib/integrations';
import RunButton from './RunButton';
import { runnableSample } from '../lib/run';
import '../styles/integrations.css';

const HEADING = 'Estro is available on your favorite platforms:';

/** Snippets rebuilt from the live catalogue so limits and names never go stale. */
function useLiveIntegrations(): Integration[] {
  const [list, setList] = useState<Integration[]>(INTEGRATIONS);
  useEffect(() => {
    let alive = true;
    fetch('/api/models', { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((d: { models?: ModelRow[] } | null) => {
        if (!alive || !d?.models?.length) return;
        setList(buildIntegrations(d.models.map((m) => ({ ...m, levels: m.levels ?? [], context_window: m.context_window ?? null, max_output: m.max_output ?? null }))));
      })
      .catch(() => {});
    return () => { alive = false; };
  }, []);
  return list;
}


function idFromHash(): string | null {
  const h = typeof location !== 'undefined' ? location.hash.replace(/^#/, '') : '';
  const m = h.match(/^integrations-(.+)$/);
  return m && INTEGRATIONS.some((i) => i.id === m[1]) ? m[1] : null;
}

function CopyButton({ code, title }: { code: string; title: string }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef(0);
  useEffect(() => () => clearTimeout(timer.current), []);
  return (
    <button type="button" class={`copy-code ${copied ? 'copied' : ''}`} aria-label={`Copy ${title}`}
      onClick={async () => {
        try { await navigator.clipboard.writeText(code); } catch { return; }
        setCopied(true); clearTimeout(timer.current); timer.current = window.setTimeout(() => setCopied(false), 1500);
      }}>
      {copied ? '✓ Copied' : 'Copy'}
    </button>
  );
}

function ConfigBlock({ config }: { config: IntegrationConfig }) {
  const run = runnableSample(config.language, config.code);
  return (
    <div class="int-config">
      <div class="int-config-head">
        <span class="int-config-title">{config.title}</span>
        <span class="int-config-lang">{config.language}</span>
        {run && <RunButton code={config.code} lang={run} />}
        <CopyButton code={config.code} title={config.title} />
      </div>
      <pre><code>{config.code}</code></pre>
    </div>
  );
}

export default function Integrations({ compact = false }: { compact?: boolean }) {
  const list = useLiveIntegrations();
  const [selected, setSelected] = useState<string | null>(null);
  const tabRefs = useRef<(HTMLElement | null)[]>([]);
  const panelRef = useRef<HTMLDivElement>(null);

  /** Scrolls the config panel into view inside whichever container scrolls the page, with a little headroom. */
  const scrollToPanel = () => {
    requestAnimationFrame(() => requestAnimationFrame(() => {
      const panel = panelRef.current;
      if (!panel) return;
      const container = (panel.closest('.kali-page-scroll') as HTMLElement | null) ?? (document.scrollingElement as HTMLElement | null);
      if (!container) return;
      const top = container === document.scrollingElement ? panel.getBoundingClientRect().top + container.scrollTop : panel.getBoundingClientRect().top - container.getBoundingClientRect().top + container.scrollTop;
      container.scrollTo({ top: Math.max(0, top - 24), behavior: 'smooth' });
    }));
  };

  useEffect(() => {
    if (compact) return;
    // A deep link (first load or a client-side navigation from the landing tiles) selects the tool and
    // brings its panel into view; a plain hash change from clicking a tile only selects.
    const sync = (scroll: boolean) => { const id = idFromHash(); if (id) { setSelected(id); if (scroll) scrollToPanel(); } };
    sync(true);
    const onHash = () => sync(false);
    const onLoad = () => sync(true);
    addEventListener('hashchange', onHash);
    document.addEventListener('astro:page-load', onLoad);
    return () => { removeEventListener('hashchange', onHash); document.removeEventListener('astro:page-load', onLoad); };
  }, [compact]);

  const select = (i: Integration) => {
    setSelected(i.id);
    history.replaceState(null, '', `#${anchorFor(i.id)}`);
  };

  const onKey = (e: KeyboardEvent, idx: number) => {
    const last = list.length - 1;
    let next: number | null = null;
    if (e.key === 'ArrowRight') next = idx === last ? 0 : idx + 1;
    else if (e.key === 'ArrowLeft') next = idx === 0 ? last : idx - 1;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = last;
    if (next === null) return;
    e.preventDefault();
    select(list[next]);
    tabRefs.current[next]?.focus();
  };

  const current = list.find((i) => i.id === selected) ?? null;

  return (
    <div class={`integrations ${compact ? 'compact' : ''}`} id={compact ? undefined : 'integrations'}>
      <p class="int-heading">{HEADING}</p>
      <div class="int-strip" role={compact ? undefined : 'tablist'} aria-label="Platforms">
        {list.map((i, idx) => {
          const isSel = !compact && i.id === selected;
          const inner = (
            <>
              <span class="int-tile-box"><img src={iconUrl(i)} alt="" width={28} height={28} loading="lazy" /></span>
              <span class="int-tile-label">{i.label}</span>
            </>
          );
          return compact
            ? <a key={i.id} class="int-tile" href={`/docs#${anchorFor(i.id)}`} title={`Set up ${i.label}`}>{inner}</a>
            : (
              <button key={i.id} type="button" role="tab" id={`int-tab-${i.id}`} aria-selected={isSel} aria-controls="int-panel"
                tabIndex={isSel || (selected === null && idx === 0) ? 0 : -1}
                ref={(el) => { tabRefs.current[idx] = el; }}
                class={`int-tile ${isSel ? 'selected' : ''}`}
                onClick={() => select(i)} onKeyDown={(e) => onKey(e, idx)}>
                {inner}
              </button>
            );
        })}
      </div>
      {!compact && (
        <div id="int-panel" role="tabpanel" aria-labelledby={current ? `int-tab-${current.id}` : undefined} class="int-panel" ref={panelRef}>
          {current ? (
            <>
              <div class="int-panel-head">
                <img src={iconUrl(current)} alt="" width={22} height={22} />
                <h3>{current.label}</h3>
              </div>
              <p class="int-desc">{current.description}</p>
              {current.configs.map((c) => <ConfigBlock key={c.title} config={c} />)}
            </>
          ) : (
            <p class="int-desc muted">Pick a platform above to see its setup.</p>
          )}
        </div>
      )}
    </div>
  );
}
