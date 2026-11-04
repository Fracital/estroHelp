import type { ComponentChildren } from 'preact';
import { useEffect, useState } from 'preact/hooks';
import { BookOpen, CircleUser, CreditCard, Settings, Sparkles, Syringe } from 'lucide-preact';
import '../styles/shell.css';

export type RailItem = 'home' | 'ai' | 'docs' | 'pricing' | 'account' | 'settings' | 'admin' | 'none';

export function railItemFor(pathname: string): RailItem {
  const p = pathname.replace(/\.html$/, '').replace(/\/+$/, '') || '/';
  if (p === '/') return 'home';
  if (p === '/chat') return 'ai';
  if (p === '/docs') return 'docs';
  if (p === '/pricing') return 'pricing';
  if (p === '/account' || p === '/admin' || p === '/login' || p === '/register' || p === '/verify' || p === '/forgot') return 'account';
  if (p === '/settings') return 'settings';
  return 'none';
}

function RailLink({ href, label, active, children }: { href: string; label: string; active?: boolean; children: ComponentChildren }) {
  return <a class={`kali-rail-btn${active ? ' active' : ''}`} href={href} title={label} aria-label={label} aria-current={active ? 'page' : undefined}>{children}</a>;
}

/**
 * The icon rail. Pre-rendered and persisted across page navigations, so the
 * active tile is derived from the URL rather than from props, which a
 * persisted island cannot rely on receiving.
 */
export default function Rail({ active: initial = 'none' }: { active?: RailItem }) {
  const [active, setActive] = useState<RailItem>(initial);
  useEffect(() => {
    const sync = () => setActive(railItemFor(location.pathname));
    sync();
    document.addEventListener('astro:after-swap', sync);
    window.addEventListener('popstate', sync);
    return () => {
      document.removeEventListener('astro:after-swap', sync);
      window.removeEventListener('popstate', sync);
    };
  }, []);
  return (
    <aside class="kali-rail" aria-label="Sidebar">
      <div class="kali-rail-top">
        <RailLink href="/" label="Home" active={active === 'home'}><Syringe size={21} strokeWidth={1.7} aria-hidden="true" /></RailLink>
        <RailLink href="/chat" label="AI" active={active === 'ai'}><Sparkles size={21} strokeWidth={1.7} aria-hidden="true" /></RailLink>
        <RailLink href="/docs" label="Docs" active={active === 'docs'}><BookOpen size={21} strokeWidth={1.7} aria-hidden="true" /></RailLink>
        <RailLink href="/pricing" label="Pricing" active={active === 'pricing'}><CreditCard size={21} strokeWidth={1.7} aria-hidden="true" /></RailLink>
      </div>
      <div class="kali-rail-bottom">
        <RailLink href="/account" label="Account" active={active === 'account' || active === 'admin'}><CircleUser size={21} strokeWidth={1.7} aria-hidden="true" /></RailLink>
        <RailLink href="/settings" label="Settings" active={active === 'settings'}><Settings size={21} strokeWidth={1.7} aria-hidden="true" /></RailLink>
      </div>
    </aside>
  );
}
