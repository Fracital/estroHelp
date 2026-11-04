import { signal } from '@preact/signals';
import { DEFAULT_THEME, THEME_PRESETS, applyTheme, themeRuntime } from './theme';

export type Performance = 'auto' | 'quality' | 'saver';
export type ReduceMotion = 'system' | boolean;

export interface Preferences {
  theme: string;
  background: string;
  backgroundDim: number;
  surfaceBlur: boolean;
  performance: Performance;
  reduceMotion: ReduceMotion;
  /** The custom theme's accent; null on every preset. */
  accent: string | null;
  /** The custom theme's background; null on every preset. */
  customBackground: string | null;
}

export const STORAGE_KEY = 'kali:prefs';

export const DEFAULTS: Preferences = {
  theme: DEFAULT_THEME,
  background: 'pixel-snow',
  backgroundDim: 55,
  surfaceBlur: true,
  performance: 'auto',
  reduceMotion: 'system',
  accent: null,
  customBackground: null,
};

export const prefs = signal<Preferences>({ ...DEFAULTS });

const HEX = /^#[0-9a-f]{6}$/i;

function sanitize(raw: unknown): Preferences {
  const p = (typeof raw === 'object' && raw !== null ? raw : {}) as Partial<Record<keyof Preferences, unknown>>;
  const dim = Number(p.backgroundDim);
  const theme = typeof p.theme === 'string' && THEME_PRESETS.some((t) => t.name === p.theme) ? p.theme : DEFAULTS.theme;
  return {
    theme,
    background: typeof p.background === 'string' && p.background ? p.background : DEFAULTS.background,
    backgroundDim: Number.isFinite(dim) ? Math.min(85, Math.max(0, Math.round(dim))) : DEFAULTS.backgroundDim,
    surfaceBlur: typeof p.surfaceBlur === 'boolean' ? p.surfaceBlur : DEFAULTS.surfaceBlur,
    performance: p.performance === 'quality' || p.performance === 'saver' ? p.performance : 'auto',
    reduceMotion: typeof p.reduceMotion === 'boolean' ? p.reduceMotion : 'system',
    accent: theme === 'custom' && typeof p.accent === 'string' && HEX.test(p.accent) ? p.accent : null,
    customBackground: theme === 'custom' && typeof p.customBackground === 'string' && HEX.test(p.customBackground) ? p.customBackground : null,
  };
}

export function load(): Preferences {
  let raw: unknown = null;
  try { raw = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? 'null'); } catch {}
  prefs.value = sanitize(raw);
  return prefs.value;
}

export function save(next: Partial<Preferences>): Preferences {
  prefs.value = sanitize({ ...prefs.value, ...next });
  try { localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs.value)); } catch {}
  apply();
  return prefs.value;
}

export function reset(): Preferences {
  try { localStorage.removeItem(STORAGE_KEY); } catch {}
  prefs.value = { ...DEFAULTS };
  apply();
  return prefs.value;
}

/** True when motion should be reduced, resolving 'system' against the media query. */
export function motionReduced(p: Preferences = prefs.value): boolean {
  if (p.reduceMotion !== 'system') return p.reduceMotion;
  try { return window.matchMedia('(prefers-reduced-motion: reduce)').matches; } catch { return false; }
}

// Client-side navigation swaps <html> attributes for the new document's, so
// repaint before the new page shows.
if (typeof document !== 'undefined') {
  document.addEventListener('astro:after-swap', () => { try { apply(load()); } catch { /* storage unavailable */ } });
}

/** Writes the current preferences onto <html> as data attributes and CSS variables. */
export function apply(p: Preferences = prefs.value) {
  const el = document.documentElement;
  el.dataset.bg = p.background;
  el.dataset.blur = p.surfaceBlur ? 'on' : 'off';
  el.dataset.perf = p.performance;
  el.dataset.motion = motionReduced(p) ? 'reduce' : 'full';
  el.dataset.surface = 'clear';
  el.style.setProperty('--bg-dim', String(p.backgroundDim / 100));
  el.style.setProperty('--surface-blur', p.surfaceBlur ? '10px' : '0px');
  applyTheme({ theme: p.theme, customBackground: p.customBackground, customAccent: p.accent });
}

/**
 * Inline <head> script source: applies the stored attributes and theme tokens
 * before first paint so nothing flashes. The theme math is the same function
 * the app uses (lib/theme.ts themeRuntime), inlined by source.
 */
const compactPresets = THEME_PRESETS.map((t) => ({ name: t.name, background: t.background, accent: t.accent, fg: t.fg }));
export const HEAD_SCRIPT = `(function(){try{var p=JSON.parse(localStorage.getItem(${JSON.stringify(STORAGE_KEY)})||'{}')||{};var e=document.documentElement;var bg=typeof p.background==='string'&&p.background?p.background:'pixel-snow';var dim=Number(p.backgroundDim);dim=isFinite(dim)?Math.min(85,Math.max(0,Math.round(dim))):55;var blur=typeof p.surfaceBlur==='boolean'?p.surfaceBlur:true;var rm=typeof p.reduceMotion==='boolean'?p.reduceMotion:matchMedia('(prefers-reduced-motion: reduce)').matches;e.dataset.bg=bg;e.dataset.blur=blur?'on':'off';e.dataset.perf=p.performance==='quality'||p.performance==='saver'?p.performance:'auto';e.dataset.motion=rm?'reduce':'full';e.dataset.surface='clear';e.style.setProperty('--bg-dim',String(dim/100));e.style.setProperty('--surface-blur',blur?'10px':'0px');var rt=(${themeRuntime.toString()})(${JSON.stringify(compactPresets)});var theme=typeof p.theme==='string'?p.theme:${JSON.stringify(DEFAULT_THEME)};rt.paint(e,{theme:theme,customBackground:theme==='custom'?p.customBackground:null,customAccent:theme==='custom'?p.accent:null});}catch(_){}})();`;
