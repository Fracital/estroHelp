// Builds each background's colour props from the page accent, so every
// background is drawn in the Kali palette instead of its stock colours.

type Rgb = [number, number, number];

function hexToRgb(hex: string): Rgb {
  const h = hex.replace('#', '').trim();
  const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h.slice(0, 6);
  const n = parseInt(full, 16);
  return Number.isFinite(n) ? [(n >> 16) & 255, (n >> 8) & 255, n & 255] : [255, 255, 255];
}
const toHex = ([r, g, b]: Rgb) => '#' + [r, g, b].map((v) => Math.round(Math.min(255, Math.max(0, v))).toString(16).padStart(2, '0')).join('');

function rgbToHsl([r, g, b]: Rgb): [number, number, number] {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, l];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  const h = max === r ? (g - b) / d + (g < b ? 6 : 0) : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
  return [h * 60, s, l];
}

function hslToHex(h: number, s: number, l: number): string {
  h = ((h % 360) + 360) % 360;
  const c = (1 - Math.abs(2 * l - 1)) * s;
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1));
  const m = l - c / 2;
  const [r, g, b] = h < 60 ? [c, x, 0] : h < 120 ? [x, c, 0] : h < 180 ? [0, c, x] : h < 240 ? [0, x, c] : h < 300 ? [x, 0, c] : [c, 0, x];
  return toHex([(r + m) * 255, (g + m) * 255, (b + m) * 255]);
}

const mix = (a: string, b: string, t: number) => {
  const x = hexToRgb(a), y = hexToRgb(b);
  return toHex([x[0] + (y[0] - x[0]) * t, x[1] + (y[1] - x[1]) * t, x[2] + (y[2] - x[2]) * t]);
};

export interface Palette { main: string; warm: string; cool: string; soft: string; glow: string; bg: string; }

/** Five related colours around the accent, tuned for a near-black page. */
export function palette(accent: string, bg = '#08080c'): Palette {
  const [h, s] = rgbToHsl(hexToRgb(accent));
  const grey = s < 0.12;
  const L = 0.62;
  const S = grey ? 0 : Math.min(0.75, Math.max(0.35, s * 0.85));
  const main = grey ? '#d4d4d8' : hslToHex(h, S, L);
  const warm = grey ? '#a1a1aa' : hslToHex(h + 32, S, L);
  const cool = grey ? '#71717a' : hslToHex(h - 32, S * 0.9, L - 0.06);
  const soft = mix(main, bg, 0.55);
  const glow = grey ? '#fafafa' : hslToHex(h, Math.min(0.6, S), 0.82);
  return { main, warm, cool, soft, glow, bg };
}

/** Reads the live accent from the document so custom accents re-tint the scene. */
export function pageAccent(): string {
  try {
    const v = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim();
    if (/^#[0-9a-f]{3,6}$/i.test(v)) return v;
  } catch {}
  return '#ef3a6d';
}

export const colorProps: Record<string, (p: Palette) => Record<string, unknown>> = {
  aurora: (p) => ({ colorStops: [p.cool, p.main, p.warm] }),
  silk: (p) => ({ color: mix(p.main, p.bg, 0.35), sheenColor: p.glow }),
  particles: (p) => ({ particleColors: [p.glow, p.main, p.warm] }),
  'pixel-snow': (p) => ({ color: p.glow }),
  'dark-veil': (p) => ({ color: p.main }),
  'light-rays': (p) => ({ raysColor: p.glow }),
  threads: (p) => ({ color: p.main, accentColor: p.warm }),
  galaxy: (p) => ({ starColors: [p.glow, p.main, p.warm], backgroundColor: 'transparent', transparent: true }),
};
