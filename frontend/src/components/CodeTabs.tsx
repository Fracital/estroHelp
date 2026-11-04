import { useRef, useState, useEffect } from 'preact/hooks';
import RunButton from './RunButton';
import { runnableLang } from '../lib/run';

export interface CodeSample {
  id: string;
  label: string;
  code: string;
  lang?: string;
}

export default function CodeTabs({ samples }: { samples: CodeSample[] }) {
  const [active, setActive] = useState(0);
  const [copied, setCopied] = useState(false);
  const timer = useRef<number>(0);
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const sample = samples[active];
  const run = runnableLang(sample.lang ?? sample.id);

  useEffect(() => () => clearTimeout(timer.current), []);

  function onKey(e: KeyboardEvent, i: number) {
    let next: number | null = null;
    const last = samples.length - 1;
    if (e.key === 'ArrowRight') next = i === last ? 0 : i + 1;
    else if (e.key === 'ArrowLeft') next = i === 0 ? last : i - 1;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = last;
    if (next === null) return;
    e.preventDefault();
    setActive(next);
    tabRefs.current[next]?.focus();
  }

  async function copy() {
    try { await navigator.clipboard.writeText(sample.code); } catch { return; }
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setCopied(false), 1500);
  }

  return (
    <div class="code-tabs">
      <div class="code-tabs-head">
        <div class="tabs-row" role="tablist">
          {samples.map((s, i) => (
            <button key={s.id} type="button" role="tab" aria-selected={i === active} tabIndex={i === active ? 0 : -1}
              ref={(el) => { tabRefs.current[i] = el; }} class={`tab ${i === active ? 'active' : ''}`}
              onClick={() => setActive(i)} onKeyDown={(e) => onKey(e, i)}>
              {s.label}
            </button>
          ))}
        </div>
        {run && <RunButton code={sample.code} lang={run} />}
        <button type="button" class={`copy-code ${copied ? 'copied' : ''}`} onClick={copy}>
          {copied ? '✓ Copied' : 'Copy'}
        </button>
      </div>
      <div role="tabpanel">
        <pre><code>{sample.code}</code></pre>
      </div>
    </div>
  );
}
