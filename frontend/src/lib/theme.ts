// Theme presets and the token pack they expand to, ported from Abstract AI.
// A theme is a background, an accent and a foreground; every shell/ui token
// is derived from those three with color-mix, so one call recolours the app.

export interface ThemePreset {
  name: string;
  label: string;
  description: string;
  background: string;
  accent: string;
  fg?: string;
}

export const THEME_PRESETS: ThemePreset[] = [
  { name: 'estro', label: 'Estro', description: "Estro's own.", background: '#0d0a10', accent: '#ff2d8a', fg: '#f6eff4' },
  { name: 'dark', label: 'Dark', description: 'Black and white.', background: '#000000', accent: '#ffffff' },
  { name: 'light', label: 'Light', description: 'Paper white, ink black.', background: '#f5f5f4', accent: '#111111' },
  { name: 'blue', label: 'Blue', description: 'Deep navy, electric accent.', background: '#070b16', accent: '#5b8cff' },
  { name: 'amoled-blue', label: 'AMOLED Blue', description: 'True black, blue accent.', background: '#000000', accent: '#3b82f6' },
  { name: 'slate', label: 'Slate', description: 'Cool grey, steel accent.', background: '#0f172a', accent: '#94a3b8', fg: '#e2e8f0' },
  { name: 'midnight', label: 'Midnight', description: 'Ink blue, soft white.', background: '#0a0f1e', accent: '#c7d2fe', fg: '#e6e9f5' },
  { name: 'ocean', label: 'Ocean', description: 'Deep sea, cyan accent.', background: '#06141d', accent: '#4cc9f0', fg: '#e3f4fb' },
  { name: 'nord', label: 'Nord', description: 'Arctic blues.', background: '#2e3440', accent: '#88c0d0', fg: '#eceff4' },
  { name: 'tokyo', label: 'Tokyo Night', description: 'Night city blues.', background: '#1a1b26', accent: '#7aa2f7', fg: '#c0caf5' },
  { name: 'onedark', label: 'One Dark', description: 'Atom classic.', background: '#282c34', accent: '#61afef', fg: '#abb2bf' },
  { name: 'palenight', label: 'Palenight', description: 'Material purple.', background: '#292d3e', accent: '#c792ea', fg: '#a6accd' },
  { name: 'dracula', label: 'Dracula', description: 'Purple on charcoal.', background: '#282a36', accent: '#bd93f9', fg: '#f8f8f2' },
  { name: 'mocha', label: 'Mocha', description: 'Catppuccin mocha.', background: '#1e1e2e', accent: '#cba6f7', fg: '#cdd6f4' },
  { name: 'kanagawa', label: 'Kanagawa', description: 'Wave ink, wisteria.', background: '#1f1f28', accent: '#957fb8', fg: '#dcd7ba' },
  { name: 'everforest', label: 'Everforest', description: 'Soft woodland greens.', background: '#2d353b', accent: '#a7c080', fg: '#d3c6aa' },
  { name: 'gruvbox', label: 'Gruvbox', description: 'Warm retro.', background: '#282828', accent: '#fabd2f', fg: '#ebdbb2' },
  { name: 'ayu', label: 'Ayu Mirage', description: 'Dusk with amber.', background: '#1f2430', accent: '#ffcc66', fg: '#cccac2' },
  { name: 'rosepine', label: 'Rosé Pine', description: 'Soft rose on pine.', background: '#191724', accent: '#ebbcba', fg: '#e0def4' },
  { name: 'solarized', label: 'Solarized', description: 'Classic dark teal.', background: '#002b36', accent: '#2aa198', fg: '#93a1a1' },
  { name: 'monokai', label: 'Monokai', description: 'Green on olive.', background: '#272822', accent: '#a6e22e', fg: '#f8f8f2' },
  { name: 'forest', label: 'Forest', description: 'Deep green, moss accent.', background: '#0b1410', accent: '#7bd389', fg: '#e3efe6' },
  { name: 'terminal', label: 'Terminal', description: 'Phosphor green on black.', background: '#000000', accent: '#33ff33', fg: '#c8ffc8' },
  { name: 'ember', label: 'Ember', description: 'Near-black, orange accent.', background: '#120c0a', accent: '#ff7a45', fg: '#f3ece8' },
  { name: 'crimson', label: 'Crimson', description: 'Dark wine, red accent.', background: '#120508', accent: '#e63946', fg: '#f7e8ea' },
  { name: 'cyberpunk', label: 'Cyberpunk', description: 'Neon pink on void.', background: '#0b0b14', accent: '#ff2a6d', fg: '#e8e8ff' },
  { name: 'synthwave', label: 'Synthwave', description: 'Retro violet, hot pink.', background: '#241b3a', accent: '#ff7edb', fg: '#f4ecff' },
  { name: 'coffee', label: 'Coffee', description: 'Espresso brown, caramel.', background: '#1b1512', accent: '#d4a373', fg: '#f1e9e2' },
  { name: 'sepia', label: 'Sepia', description: 'Warm paper.', background: '#f3ead8', accent: '#8a5a2b', fg: '#2b2118' },
  { name: 'lavender', label: 'Lavender', description: 'Light lilac, violet accent.', background: '#f3f0fa', accent: '#6b4fbb', fg: '#221a33' },
  { name: 'mint', label: 'Mint', description: 'Light mint, green accent.', background: '#eef7f2', accent: '#1f8a5b', fg: '#10261b' },
  { name: 'peach', label: 'Peach', description: 'Light cream, coral accent.', background: '#fff4ec', accent: '#d9532b', fg: '#33201a' },
  { name: 'nordlight', label: 'Nord Light', description: 'Snow storm, frost blue.', background: '#eceff4', accent: '#5e81ac', fg: '#2e3440' },
  { name: 'christmas', label: 'Christmas', description: 'Pine green, berry red, falling snow.', background: '#0c1f17', accent: '#e23a3a', fg: '#fbf3e4' },
  { name: 'custom', label: 'Custom', description: 'Pick your own colours.', background: '#0b0b10', accent: '#ffffff' },
];

export const DEFAULT_THEME = 'estro';

export interface ThemeInput { theme: string; customBackground?: string | null; customAccent?: string | null }
export interface ResolvedTheme { name: string; background: string; accent: string; fg: string; light: boolean; onAccent: string }

/**
 * Pure token math, written in plain ES5 so its source can be inlined into the
 * pre-paint <head> script (see preferences.ts HEAD_SCRIPT) and run identically.
 */
export function themeRuntime(presets: ThemePreset[]) {
  function hexToRgb(hex: string): [number, number, number] | null {
    var m = /^#?([0-9a-f]{6})$/i.exec((hex || '').trim());
    if (!m) return null;
    var v = parseInt(m[1], 16);
    return [(v >> 16) & 255, (v >> 8) & 255, v & 255];
  }
  function luminance(rgb: [number, number, number]): number {
    var f = function (c: number) { var s = c / 255; return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4); };
    return 0.2126 * f(rgb[0]) + 0.7152 * f(rgb[1]) + 0.0722 * f(rgb[2]);
  }
  function isLight(hex: string): boolean { var rgb = hexToRgb(hex); return rgb ? luminance(rgb) > 0.4 : false; }
  function resolve(input: ThemeInput): ResolvedTheme {
    var preset = presets.filter(function (t) { return t.name === input.theme; })[0] || presets.filter(function (t) { return t.name === 'estro'; })[0] || presets[0];
    var custom = preset.name === 'custom';
    var background = (custom && hexToRgb(input.customBackground || '') ? (input.customBackground as string) : preset.background).toLowerCase();
    var accent = (custom && hexToRgb(input.customAccent || '') ? (input.customAccent as string) : preset.accent).toLowerCase();
    var light = isLight(background);
    var fg = (!custom && preset.fg) || (light ? '#111111' : '#f2f2f2');
    return { name: preset.name, background: background, accent: accent, fg: fg, light: light, onAccent: isLight(accent) ? '#000000' : '#ffffff' };
  }
  function veil(color: string, pct: number): string { return pct >= 100 ? color : 'color-mix(in srgb, ' + color + ' ' + pct + '%, transparent)'; }
  /** The full token pack for a resolved theme. Surfaces are "clear": translucent so a background shows through. */
  function tokens(t: ResolvedTheme): Record<string, string> {
    var bg = t.background, fg = t.fg, accent = t.accent;
    var mix = function (a: string, pa: number, b: string) { return 'color-mix(in srgb, ' + a + ' ' + pa + '%, ' + b + ')'; };
    var surface = veil(mix(bg, 96, fg), 38);
    var raised = veil(mix(bg, 93, fg), 62);
    var hover = veil(mix(bg, 91, fg), 72);
    var muted = mix(fg, 52, bg);
    return {
      '--theme-bg': bg, '--theme-accent': accent, '--theme-fg': fg, '--theme-on-accent': t.onAccent,
      '--shell-bg': veil(bg, 0), '--shell-surface': surface, '--shell-raised': raised,
      '--shell-popover': mix(bg, 93, fg), '--shell-hover': hover,
      '--shell-line': mix(bg, 90, fg), '--shell-line-strong': mix(bg, 82, fg),
      '--shell-text': fg, '--shell-muted': muted, '--shell-faint': mix(fg, 30, bg),
      '--shell-accent': accent, '--shell-on-accent': t.onAccent,
      '--ui-bg': surface, '--ui-surface': raised,
      '--ui-glass': veil(fg, 5), '--ui-glass-2': veil(fg, 10), '--ui-glass-3': veil(fg, 16), '--ui-glass-hover': veil(fg, 8),
      '--ui-border': veil(fg, 13), '--ui-border-strong': veil(fg, 30),
      '--ui-text': fg, '--ui-text-muted': muted, '--ui-text-subtle': mix(fg, 42, bg), '--ui-white': fg, '--ui-accent': accent,
      '--accent': accent, '--bg': bg, '--text': fg,
    };
  }
  function paint(el: HTMLElement, input: ThemeInput): ResolvedTheme {
    var t = resolve(input);
    var pack = tokens(t);
    for (var k in pack) el.style.setProperty(k, pack[k]);
    el.setAttribute('data-theme', t.name);
    el.setAttribute('data-scheme', t.light ? 'light' : 'dark');
    el.setAttribute('data-snow', t.name === 'christmas' ? 'on' : 'off');
    el.style.colorScheme = t.light ? 'light' : 'dark';
    return t;
  }
  return { resolve: resolve, tokens: tokens, paint: paint, isLight: isLight };
}

const runtime = themeRuntime(THEME_PRESETS);

export const resolveTheme = runtime.resolve;
export const isLightColor = runtime.isLight;

export function applyTheme(input: ThemeInput): ResolvedTheme {
  return runtime.paint(document.documentElement, input);
}
