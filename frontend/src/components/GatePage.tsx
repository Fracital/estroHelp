import { useEffect, useState } from 'preact/hooks';
import { Syringe } from 'lucide-preact';
import AuthGate, { LegalFooter, type GateMode } from './AuthGate';
import { authReady, canChat, loadMe, nextUrl, user } from '../lib/auth';
import { load } from '../lib/preferences';

/** /login, /register, /forgot and /verify: the account gate inside the browser shell. */
export default function GatePage({ mode }: { mode: GateMode }) {
  const [ready, setReady] = useState(false);
  useEffect(() => {
    load();
    loadMe().then(() => {
      const u = user.value;
      if (mode === 'verify') {
        if (!u) { location.replace('/login?next=%2Fverify'); return; }
        if (canChat(u)) { location.replace('/chat'); return; }
      } else if (u && mode !== 'forgot' && mode !== 'reset') {
        location.replace(canChat(u) ? nextUrl() : '/verify');
        return;
      }
      setReady(true);
    });
  }, []);

  const titles: Record<GateMode, [string, string]> = {
    login: ['Welcome back', 'Sign in to pick up your chats.'],
    register: ['Create an account', ''],
    forgot: ['Reset your password', ''],
    reset: ['Choose a new password', ''],
    verify: ['Check your inbox', ''],
  };
  const [title, subtitle] = titles[mode];
  return (
    <>
      <main class="account-shell">
        {ready && authReady.value ? <AuthGate icon={<Syringe size={20} />} title={title} subtitle={subtitle} initialMode={mode} /> : <div class="account-loading" />}
        <LegalFooter />
      </main>
    </>
  );
}
