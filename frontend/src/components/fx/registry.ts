// One lazy chunk per background; only the selected one is downloaded.
import type { ComponentType } from 'preact';

export interface FxEntry {
  name: string;
  load: () => Promise<{ default: ComponentType<any> }>;
  /** Prop that caps device pixel ratio, when the component exposes one. */
  dprProp: 'dpr' | 'pixelRatio' | 'maxDevicePixelRatio' | null;
  /** True when the component takes `paused` and can hold a still frame; otherwise it unmounts while paused. */
  pausable: boolean;
  /** Static CSS gradient used as the picker thumbnail. */
  thumb: string;
}

export const FX: Record<string, FxEntry> = {
  'pixel-snow': {
    name: 'Pixel Snow', load: () => import('./PixelSnow/PixelSnow'), dprProp: 'dpr', pausable: true,
    thumb: 'radial-gradient(circle at 20% 30%, #fff8 1px, transparent 2px), radial-gradient(circle at 70% 60%, #fff6 1.5px, transparent 3px), radial-gradient(circle at 45% 80%, #fff5 1px, transparent 2px), linear-gradient(180deg, #0c0c14, #1a1024)',
  },
  aurora: {
    name: 'Aurora', load: () => import('./Aurora/Aurora'), dprProp: 'dpr', pausable: true,
    thumb: 'linear-gradient(160deg, #08080c 20%, #5b21b6 55%, #ef3a6d 80%, #08080c)',
  },
  silk: {
    name: 'Silk', load: () => import('./Silk/Silk'), dprProp: 'dpr', pausable: true,
    thumb: 'linear-gradient(120deg, #2a1230, #7a2650 40%, #3b1d4d 70%, #1c0f22)',
  },
  'dark-veil': {
    name: 'Dark Veil', load: () => import('./DarkVeil/DarkVeil'), dprProp: 'dpr', pausable: true,
    thumb: 'radial-gradient(ellipse at 30% 40%, #8b5cf6aa, transparent 60%), radial-gradient(ellipse at 75% 70%, #ef3a6d88, transparent 55%), #08080c',
  },
  particles: {
    name: 'Particles', load: () => import('./Particles/Particles'), dprProp: 'pixelRatio', pausable: true,
    thumb: 'radial-gradient(circle at 25% 35%, #f0abfc 2px, transparent 3px), radial-gradient(circle at 60% 25%, #ef3a6d 1.5px, transparent 3px), radial-gradient(circle at 80% 65%, #c4b5fd 2px, transparent 3px), radial-gradient(circle at 40% 75%, #fff 1px, transparent 2px), #0b0b12',
  },
  'light-rays': {
    name: 'Light Rays', load: () => import('./LightRays/LightRays'), dprProp: 'maxDevicePixelRatio', pausable: false,
    thumb: 'conic-gradient(from 160deg at 50% -10%, transparent 0deg, #f9a8d455 8deg, transparent 16deg, #f9a8d433 24deg, transparent 32deg, #f9a8d455 40deg, transparent 48deg), #08080c',
  },
  threads: {
    name: 'Threads', load: () => import('./Threads/Threads'), dprProp: null, pausable: true,
    thumb: 'repeating-linear-gradient(170deg, transparent 0 9px, #ef3a6d66 9px 10px, transparent 10px 19px, #fb923c55 19px 20px), #0a0a10',
  },
  galaxy: {
    name: 'Galaxy', load: () => import('./Galaxy/Galaxy'), dprProp: 'dpr', pausable: false,
    thumb: 'radial-gradient(circle at 50% 50%, #fde68a 1px, transparent 2px), radial-gradient(circle at 20% 70%, #fff 1px, transparent 2px), radial-gradient(circle at 80% 30%, #f9a8d4 1.5px, transparent 3px), radial-gradient(ellipse at 50% 50%, #7c3aed55, transparent 60%), #06060a',
  },
};

export const FX_IDS = Object.keys(FX);
