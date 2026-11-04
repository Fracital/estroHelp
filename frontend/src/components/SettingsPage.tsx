import { useEffect, useState } from 'preact/hooks';
import type { ComponentChildren } from 'preact';
import { RotateCcw, Trash2, Wrench } from 'lucide-preact';
import { LegalFooter } from './AuthGate';
import { load, prefs, reset, save, type Performance } from '../lib/preferences';
import { THEME_PRESETS, isLightColor } from '../lib/theme';
import { FX, FX_IDS } from './fx/registry';
import '../styles/account.css';
import '../styles/settings.css';

function Section({ id, title, description, children }: { id: string; title: string; description?: string; children: ComponentChildren }) {
  return (
    <section class="settings-section" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {description && <p class="settings-section-note">{description}</p>}
      {children}
    </section>
  );
}

function Toggle({ label, description, checked, disabled, onChange }: { label: string; description?: string; checked: boolean; disabled?: boolean; onChange: (v: boolean) => void }) {
  return (
    <label class={`settings-row settings-toggle${disabled ? ' is-disabled' : ''}`}>
      <span><strong>{label}</strong>{description && <small>{description}</small>}</span>
      <input type="checkbox" role="switch" class="switch" checked={checked} disabled={disabled} onChange={(e) => onChange((e.target as HTMLInputElement).checked)} />
    </label>
  );
}

function Slider({ label, description, value, min, max, step = 1, unit = '%', onChange }: { label: string; description?: string; value: number; min: number; max: number; step?: number; unit?: string; onChange: (v: number) => void }) {
  return (
    <div class="settings-row settings-row-stack">
      <div class="settings-slider-head">
        <span><strong>{label}</strong>{description && <small>{description}</small>}</span>
        <output aria-label={`${label} value`}>{value}{unit}</output>
      </div>
      <input type="range" class="range" aria-label={label} min={min} max={max} step={step} value={value} onInput={(e) => onChange(Number((e.target as HTMLInputElement).value))} />
    </div>
  );
}

function Select({ label, description, value, options, onChange }: { label: string; description?: string; value: string; options: [string, string][]; onChange: (v: string) => void }) {
  return (
    <label class="settings-row">
      <span><strong>{label}</strong>{description && <small>{description}</small>}</span>
      <select class="settings-input settings-select" value={value} onChange={(e) => onChange((e.target as HTMLSelectElement).value)}>
        {options.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
      </select>
    </label>
  );
}

const BG_DESCRIPTIONS: Record<string, string> = {
  'pixel-snow': 'Drifting pixel flakes.', aurora: 'Slow northern lights.', silk: 'Folding silk.', 'dark-veil': 'Shifting dark veil.',
  particles: 'Floating particles.', 'light-rays': 'Volumetric rays.', threads: 'Flowing threads.', galaxy: 'A slow galaxy.',
};

export default function SettingsPage() {
  const [ready, setReady] = useState(false);
  const [query, setQuery] = useState('');
  const [clearStatus, setClearStatus] = useState('');
  useEffect(() => { load(); setReady(true); }, []);
  const p = prefs.value;

  async function clearLocalData() {
    setClearStatus('Clearing…');
    try {
      localStorage.clear(); sessionStorage.clear();
      if ('caches' in window) for (const key of await caches.keys()) await caches.delete(key);
      setClearStatus('Cleared. Reload to finish.');
    } catch { setClearStatus('Could not clear everything'); }
  }

  const q = query.trim().toLowerCase();
  const bgOptions = [{ id: 'none', title: 'None', description: 'Plain theme background.' }, ...FX_IDS.map((id) => ({ id, title: FX[id].name, description: BG_DESCRIPTIONS[id] ?? '' }))];
  const shownBg = q ? bgOptions.filter((o) => o.title.toLowerCase().includes(q) || o.description.toLowerCase().includes(q)) : bgOptions;

  return (
    <>
      <main class="settings-main">
        <header class="settings-hero">
          <span class="settings-hero-icon" aria-hidden="true"><Wrench size={20} /></span>
          <div>
            <h1>Settings</h1>
            <p>Preferences for this browser. Your account is managed on the <a class="settings-inline-link" href="/account">Estro account</a> page.</p>
          </div>
        </header>

        {ready && (
          <>
            <Section id="set-appearance" title="Appearance">
              <div class="settings-row settings-row-stack">
                <span class="settings-group-label" id="set-theme-label"><strong>Theme</strong><small>Colours for the shell, chat, settings and every page.</small></span>
                <div class="theme-grid" role="radiogroup" aria-labelledby="set-theme-label">
                  {THEME_PRESETS.map((preset) => {
                    const selected = p.theme === preset.name;
                    const bg = preset.name === 'custom' ? (p.customBackground ?? preset.background) : preset.background;
                    const accent = preset.name === 'custom' ? (p.accent ?? preset.accent) : preset.accent;
                    return (
                      <button key={preset.name} type="button" role="radio" aria-checked={selected} class={`theme-card${selected ? ' selected' : ''}`}
                        onClick={() => save(preset.name === 'christmas' ? { theme: preset.name, background: 'pixel-snow' } : { theme: preset.name })}>
                        <span class="theme-swatch" aria-hidden="true" style={{ '--swatch-bg': bg, '--swatch-accent': accent, '--swatch-fg': preset.fg ?? (isLightColor(bg) ? '#111' : '#f2f2f2') } as any} />
                        <span class="theme-card-label"><span class="radio-dot" aria-hidden="true" />{preset.label}<small>{preset.description}</small></span>
                      </button>
                    );
                  })}
                </div>
                {p.theme === 'custom' && (
                  <div class="theme-colors">
                    <label class="theme-color">
                      <input type="color" value={p.customBackground ?? '#0b0b10'} onInput={(e) => save({ customBackground: (e.target as HTMLInputElement).value })} aria-label="Background colour" />
                      <span>Background</span><code>{p.customBackground ?? '#0b0b10'}</code>
                    </label>
                    <label class="theme-color">
                      <input type="color" value={p.accent ?? '#ffffff'} onInput={(e) => save({ accent: (e.target as HTMLInputElement).value })} aria-label="Accent colour" />
                      <span>Accent</span><code>{p.accent ?? '#ffffff'}</code>
                    </label>
                  </div>
                )}
              </div>

              <Toggle label="Blur surfaces" description="Clear shows the background through the sidebar, tabs and every page with a frosted blur. Costs a little GPU." checked={p.surfaceBlur} onChange={(v) => save({ surfaceBlur: v })} />
              <Select label="Performance" description="Auto uses Battery saver on Chromebooks and other low-power devices: the background draws at lower resolution."
                value={p.performance} options={[['auto', 'Auto'], ['quality', 'High quality'], ['saver', 'Battery saver']]} onChange={(v) => save({ performance: v as Performance })} />
              <Toggle label="Animated background" description="Show an animated background behind every page." checked={p.background !== 'none'} onChange={(v) => save({ background: v ? 'pixel-snow' : 'none' })} />
              {p.background !== 'none' && (
                <Slider label="Background dim" description="Darkens the background so it is easier on the eyes and text stays readable." value={p.backgroundDim} min={0} max={85} step={5} onChange={(v) => save({ backgroundDim: v })} />
              )}
              {p.background !== 'none' && (
                <div class="settings-row settings-row-stack">
                  <span class="settings-group-label" id="set-bg-label"><strong>Background style</strong><small>Backgrounds from React Bits. Pick one to see it live behind this page. Reduce motion pauses it.</small></span>
                  <input class="settings-input bg-search" type="search" value={query} placeholder="Search backgrounds" aria-label="Search backgrounds" onInput={(e) => setQuery((e.target as HTMLInputElement).value)} />
                  <div class="bg-grid" role="radiogroup" aria-labelledby="set-bg-label">
                    {shownBg.map((o) => {
                      const selected = p.background === o.id;
                      return (
                        <button key={o.id} type="button" role="radio" aria-checked={selected} class={`bg-card${selected ? ' selected' : ''}`} onClick={() => save({ background: o.id })}>
                          <span class="theme-card-label"><span class="radio-dot" aria-hidden="true" />{o.title}<small>{o.description}</small></span>
                        </button>
                      );
                    })}
                  </div>
                </div>
              )}
            </Section>

            <Section id="set-access" title="Accessibility">
              <Toggle label="Reduce motion" description="Pause the background and turn off animations across this site." checked={p.reduceMotion === true} disabled={p.reduceMotion === 'system'} onChange={(v) => save({ reduceMotion: v })} />
              <Toggle label="Sync with device settings" description="Follow the reduced-motion setting of your operating system instead." checked={p.reduceMotion === 'system'} onChange={(v) => save({ reduceMotion: v ? 'system' : false })} />
            </Section>

            <Section id="set-advanced" title="Advanced">
              <div class="settings-row">
                <span><strong>Reset settings</strong><small>Restores the defaults on this page.</small></span>
                <button type="button" class="settings-action" onClick={() => reset()}><RotateCcw size={14} /> Reset</button>
              </div>
              <div class="settings-row">
                <span><strong>Clear site data</strong><small>{clearStatus || 'Removes everything this site stored in this browser, including your sign-in.'}</small></span>
                <button type="button" class="settings-action danger" onClick={clearLocalData}><Trash2 size={14} /> Clear</button>
              </div>
            </Section>
          </>
        )}

        <LegalFooter className="settings-legal" />
      </main>
    </>
  );
}
