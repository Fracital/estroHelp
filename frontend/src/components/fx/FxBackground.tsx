import { Component, type ComponentType, type ComponentChildren } from 'preact';
import { useEffect, useMemo, useState } from 'preact/hooks';
import { FX } from './registry';
import { colorProps, pageAccent, palette } from './theme';
import { apply, load, motionReduced, prefs, type Performance } from '../../lib/preferences';
import '../../styles/fx.css';

type Tier = 'quality' | 'balanced' | 'saver';
// Render scale and device-pixel cap per tier: shader cost scales with pixel
// count, so saver draws a quarter of the pixels and lets the browser upscale.
const TIERS: Record<Tier, { scale: number; dpr: number }> = {
  quality: { scale: 1, dpr: 2 },
  balanced: { scale: 1, dpr: 1.25 },
  saver: { scale: 0.5, dpr: 1 },
};

function resolveTier(p: Performance): Tier {
  if (p !== 'auto') return p;
  const nav = navigator as Navigator & { deviceMemory?: number };
  const weak = (nav.hardwareConcurrency ?? 8) <= 4 || (nav.deviceMemory ?? 8) <= 4;
  const small = window.matchMedia('(max-width: 820px)').matches;
  return weak || small ? 'saver' : 'balanced';
}

// A background that cannot start (no WebGL, shader error) renders nothing
// instead of taking the page down.
class Guard extends Component<{ children: ComponentChildren; resetKey: string }, { failed: boolean; key: string }> {
  state = { failed: false, key: this.props.resetKey };
  static getDerivedStateFromProps(p: { resetKey: string }, s: { failed: boolean; key: string }) {
    return p.resetKey !== s.key ? { failed: false, key: p.resetKey } : null;
  }
  componentDidCatch(err: unknown) {
    console.warn('[background] failed to start', err);
    this.setState({ failed: true });
  }
  render() { return this.state.failed ? null : this.props.children; }
}

export default function FxBackground() {
  const [Bg, setBg] = useState<{ id: string; comp: ComponentType<any> } | null>(null);
  const [hidden, setHidden] = useState(() => typeof document !== 'undefined' && document.hidden);
  const [reduce, setReduce] = useState(false);
  const p = prefs.value;

  useEffect(() => {
    load();
    apply();
    setReduce(motionReduced());
    const onVis = () => setHidden(document.hidden);
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    const onMq = () => { setReduce(motionReduced()); apply(); };
    document.addEventListener('visibilitychange', onVis);
    mq.addEventListener('change', onMq);
    return () => { document.removeEventListener('visibilitychange', onVis); mq.removeEventListener('change', onMq); };
  }, []);

  useEffect(() => { setReduce(motionReduced()); }, [p.reduceMotion]);

  const id = p.background;
  const entry = FX[id];
  useEffect(() => {
    if (!entry) { setBg(null); return; }
    let live = true;
    entry.load().then((m) => { if (live) setBg({ id, comp: m.default }); }).catch((err) => console.warn('[background] load failed', err));
    return () => { live = false; };
  }, [id]);

  const accent = p.accent ?? pageAccent();
  const tier = resolveTier(p.performance);
  const paused = hidden || reduce;
  const props = useMemo(() => {
    if (!entry) return {};
    const out: Record<string, unknown> = colorProps[id]?.(palette(accent)) ?? {};
    const t = TIERS[tier];
    if (entry.dprProp) out[entry.dprProp] = Math.min(window.devicePixelRatio || 1, t.dpr);
    if (entry.pausable) out.paused = paused;
    if (id === 'pixel-snow') {
      out.density = 0.4;
      out.flakeSize = 1;
      out.maxFps = tier === 'quality' ? 0 : 30;
      if (tier === 'saver') out.glow = 0;
    }
    if (id === 'galaxy') out.mouseInteraction = false;
    if (id === 'light-rays') out.followMouse = false;
    return out;
  }, [entry, id, accent, tier, paused]);

  const scene = Bg && Bg.id === id && entry && !(paused && !entry.pausable);
  const s = TIERS[tier].scale;
  const inner = s === 1
    ? { position: 'absolute', inset: 0 }
    : { position: 'absolute', left: 0, top: 0, width: `${s * 100}%`, height: `${s * 100}%`, transform: `scale(${1 / s})`, transformOrigin: '0 0' };

  return (
    <div class="fx-root" aria-hidden="true">
      <div class="fx-scene">
        {scene && (
          <Guard resetKey={`${id}|${accent}`}>
            <div style={inner as any}><Bg.comp key={`${id}|${accent}`} {...props} /></div>
          </Guard>
        )}
      </div>
      <div class="fx-veil" />
    </div>
  );
}
