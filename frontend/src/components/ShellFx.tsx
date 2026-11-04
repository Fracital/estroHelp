import { useEffect } from 'preact/hooks';
import { effect } from '@preact/signals';
import { load, prefs } from '../lib/preferences';

/**
 * Theme side effects for the shell frame: loads preferences, mirrors the blur
 * and snow flags onto the static `.kali-shell` container, and draws the
 * holiday decorations when the christmas theme is on. Persisted across
 * navigations so none of this re-runs per page.
 */
export default function ShellFx() {
  useEffect(() => {
    load();
    return effect(() => {
      const p = prefs.value;
      for (const shell of document.querySelectorAll<HTMLElement>('.kali-shell')) {
        shell.dataset.surface = 'clear';
        shell.dataset.blur = p.surfaceBlur ? 'on' : 'off';
        shell.dataset.snow = p.theme === 'christmas' ? 'on' : 'off';
      }
    });
  }, []);
  if (prefs.value.theme !== 'christmas') return null;
  return (
    <>
      <img class="christmas-tree" src="/christmas-tree.png" alt="" aria-hidden="true" draggable={false} />
      <svg class="snow-drift" viewBox="0 0 1440 40" preserveAspectRatio="none" aria-hidden="true">
        <path d="M0 22 C60 10 120 12 180 20 S300 30 360 18 S480 6 560 16 S700 30 780 20 S900 8 980 17 S1120 30 1200 19 S1340 9 1440 18 V40 H0 Z" />
      </svg>
    </>
  );
}
