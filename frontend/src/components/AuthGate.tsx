import { useEffect, useState } from 'preact/hooks';
import type { ComponentChildren } from 'preact';
import { api, ApiError } from '../lib/api';
import { loadMe, user, canChat, nextUrl, type User } from '../lib/auth';
import { passkeyAutofillAvailable, passkeysSupported, signInWithPasskey } from '../lib/passkeys';
import '../styles/account.css';

export type GateMode = 'login' | 'register' | 'forgot' | 'reset' | 'verify' | 'totp';

export interface AuthGateProps {
  icon: ComponentChildren;
  title?: string;
  subtitle?: string;
  initialMode?: GateMode;
  /** Called with the signed-in user; the default sends them to the chat or the verify step. */
  onDone?: (u: User) => void;
}

export function finishSignIn(u: User) {
  location.href = canChat(u) ? nextUrl() : '/verify';
}

/** The sign-in / sign-up / reset card, ported from Abstract AI's AuthGate. */
export default function AuthGate({ icon, title = 'Welcome back', subtitle = 'Sign in to continue.', initialMode = 'login', onDone = finishSignIn }: AuthGateProps) {
  const [mode, setMode] = useState<GateMode>(initialMode);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [mfaToken, setMfaToken] = useState('');
  const [useBackup, setUseBackup] = useState(false);
  const [passkeyOk, setPasskeyOk] = useState(false);

  const switchMode = (next: GateMode) => { setMode(next); setError(''); setNotice(''); };
  const fail = (e: unknown, fallback: string) => {
    const ae = e as ApiError;
    setError(ae?.data?.ban ? `Banned: ${ae.data.ban.reason || 'no reason given'}` : ae?.message || fallback);
  };

  const signedIn = async () => {
    await loadMe(true);
    if (user.value) onDone(user.value);
  };

  // Conditional UI: a passkey suggestion appears in the username field when the
  // browser supports it; the pending request is cancelled when the user picks
  // the button or submits the form instead.
  useEffect(() => {
    if (mode !== 'login') return;
    setPasskeyOk(passkeysSupported());
    let cancelled = false;
    passkeyAutofillAvailable().then(async (ok) => {
      if (!ok || cancelled) return;
      try { await signInWithPasskey(true); if (!cancelled) await signedIn(); } catch { /* aborted or dismissed */ }
    });
    return () => { cancelled = true; };
  }, [mode]);

  const submit = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setError('');
    try {
      const body = mode === 'register' ? { username, password, email } : { username, password };
      const res = await api<{ mfa?: string; mfaToken?: string }>(`/api/auth/${mode === 'register' ? 'register' : 'login'}`, { body });
      if (res.mfa === 'totp' && res.mfaToken) {
        setMfaToken(res.mfaToken); setCode(''); setUseBackup(false); setBusy(false); switchMode('totp');
        return;
      }
      await signedIn();
    } catch (err) { fail(err, 'Something went wrong.'); setBusy(false); }
  };

  const submitTotp = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setError('');
    try {
      await api('/api/auth/login/totp', { body: { mfaToken, code: code.trim() } });
      await signedIn();
    } catch (err) {
      const ae = err as ApiError;
      fail(err, 'Wrong code.');
      if (ae?.status === 401 && /sign in again/i.test(ae.message)) { switchMode('login'); setPassword(''); }
      if (ae?.status === 429) { switchMode('login'); setPassword(''); }
      setBusy(false);
    }
  };

  const passkeyLogin = async () => {
    setBusy(true); setError('');
    try { await signInWithPasskey(false); await signedIn(); }
    catch (err) {
      const name = (err as Error)?.name;
      if (name !== 'NotAllowedError' && name !== 'AbortError') fail(err, 'Passkey sign-in failed.');
      setBusy(false);
    }
  };

  const submitForgot = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setError(''); setNotice('');
    try {
      await api('/api/auth/forgot-password', { body: { email } });
      setNotice('If that account exists, a reset code is on its way by email. It expires in 15 minutes.');
      setMode('reset');
    } catch (err) { fail(err, 'Could not send reset email.'); }
    setBusy(false);
  };

  const submitReset = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setError('');
    try {
      await api('/api/auth/reset-password', { body: { email, code, password } });
      setNotice('Password reset. Sign in with your new password.');
      setMode('login'); setPassword('');
    } catch (err) { fail(err, 'Reset failed.'); }
    setBusy(false);
  };

  const submitVerify = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setError('');
    try {
      await api('/api/auth/verify-email', { body: { code: code.trim() } });
      await loadMe(true);
      location.href = '/chat';
    } catch (err) { fail(err, 'Wrong or expired code.'); setBusy(false); }
  };

  const resend = async () => {
    setError(''); setNotice('');
    try { await api('/api/auth/resend-verification', { body: {} }); setNotice('A new code is on its way.'); }
    catch (err) { fail(err, 'Could not resend.'); }
  };

  if (mode === 'verify') {
    return (
      <div class="kali-auth-gate">
        <form class="kali-auth-card" onSubmit={submitVerify}>
          <div class="kali-auth-mark">{icon}</div>
          <h1>Check your inbox</h1>
          <p>{notice || `We sent a 6-digit code to ${user.value?.email ?? 'your email'}. Enter it to unlock chat.`}</p>
          <input value={code} onInput={(e) => setCode((e.target as HTMLInputElement).value.replace(/\D/g, ''))} placeholder="Verification code" inputMode="numeric" maxLength={6} autoComplete="one-time-code" autoFocus />
          {error && <span class="kali-auth-error">{error}</span>}
          <button type="submit" disabled={busy || code.length !== 6}>{busy ? 'Verifying…' : 'Verify'}</button>
          <button type="button" class="kali-auth-switch" onClick={resend}>Resend code</button>
          <a class="kali-auth-switch" href="/account">Change email</a>
        </form>
      </div>
    );
  }

  if (mode === 'totp') {
    return (
      <div class="kali-auth-gate">
        <form class="kali-auth-card" onSubmit={submitTotp}>
          <div class="kali-auth-mark">{icon}</div>
          <h1>Two-factor code</h1>
          <p>{useBackup ? 'Enter one of your backup codes. Each one works once.' : 'Enter the 6-digit code from your authenticator app.'}</p>
          {useBackup
            ? <input key="backup" value={code} onInput={(e) => setCode((e.target as HTMLInputElement).value)} placeholder="Backup code" autoComplete="off" autoFocus />
            : <input key="totp" value={code} onInput={(e) => setCode((e.target as HTMLInputElement).value.replace(/\D/g, ''))} placeholder="6-digit code" inputMode="numeric" maxLength={6} autoComplete="one-time-code" autoFocus />}
          {error && <span class="kali-auth-error">{error}</span>}
          <button type="submit" disabled={busy || (useBackup ? code.trim().length < 8 : code.length !== 6)}>{busy ? 'Checking…' : 'Continue'}</button>
          <button type="button" class="kali-auth-switch" onClick={() => { setUseBackup(!useBackup); setCode(''); setError(''); }}>{useBackup ? 'Use the authenticator app' : 'Use a backup code'}</button>
          <button type="button" class="kali-auth-switch" onClick={() => { switchMode('login'); setPassword(''); }}>Back to sign in</button>
        </form>
      </div>
    );
  }

  if (mode === 'forgot') {
    return (
      <div class="kali-auth-gate">
        <form class="kali-auth-card" onSubmit={submitForgot}>
          <div class="kali-auth-mark">{icon}</div>
          <h1>Reset your password</h1>
          <p>Enter your email and we'll send you a reset code.</p>
          <input type="email" value={email} onInput={(e) => setEmail((e.target as HTMLInputElement).value)} placeholder="Email" autoComplete="email" autoFocus />
          {error && <span class="kali-auth-error">{error}</span>}
          <button type="submit" disabled={busy || !email}>{busy ? 'Sending…' : 'Send reset code'}</button>
          <button type="button" class="kali-auth-switch" onClick={() => switchMode('login')}>Back to sign in</button>
        </form>
      </div>
    );
  }

  if (mode === 'reset') {
    return (
      <div class="kali-auth-gate">
        <form class="kali-auth-card" onSubmit={submitReset}>
          <div class="kali-auth-mark">{icon}</div>
          <h1>Choose a new password</h1>
          <p>{notice || 'Enter the reset code from your email.'}</p>
          {!email && <input type="email" value={email} onInput={(e) => setEmail((e.target as HTMLInputElement).value)} placeholder="Email" autoComplete="email" />}
          <input value={code} onInput={(e) => setCode((e.target as HTMLInputElement).value)} placeholder="Reset code" autoComplete="one-time-code" autoFocus />
          <input type="password" value={password} onInput={(e) => setPassword((e.target as HTMLInputElement).value)} placeholder="New password (8+ characters)" autoComplete="new-password" />
          {error && <span class="kali-auth-error">{error}</span>}
          <button type="submit" disabled={busy || !code || password.length < 8}>{busy ? 'Resetting…' : 'Reset password'}</button>
          <button type="button" class="kali-auth-switch" onClick={() => switchMode('login')}>Back to sign in</button>
        </form>
      </div>
    );
  }

  return (
    <div class="kali-auth-gate">
      <form class="kali-auth-card" onSubmit={submit}>
        <div class="kali-auth-mark">{icon}</div>
        <h1>{mode === 'login' ? title : 'Create an account'}</h1>
        <p>{mode === 'login' ? subtitle : 'Pick a username and an 8+ character password. A verification code goes to your email.'}</p>
        {notice && <span class="kali-auth-error" style={{ color: '#8fe6a8' }}>{notice}</span>}
        <input value={username} onInput={(e) => setUsername((e.target as HTMLInputElement).value)} placeholder={mode === 'login' ? 'Username or email' : 'Username'} autoComplete={mode === 'login' ? 'username webauthn' : 'username'} autoFocus />
        {mode === 'register' && (
          <input type="email" value={email} onInput={(e) => setEmail((e.target as HTMLInputElement).value)} placeholder="Email" autoComplete="email" required />
        )}
        <input type="password" value={password} onInput={(e) => setPassword((e.target as HTMLInputElement).value)} placeholder="Password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} />
        {error && <span class="kali-auth-error">{error}</span>}
        <button type="submit" disabled={busy || !username || !password || (mode === 'register' && !email)}>
          {busy ? 'Please wait…' : mode === 'login' ? 'Sign in' : 'Sign up'}
        </button>
        {mode === 'login' && <button type="button" class="kali-auth-switch" disabled={busy || !passkeyOk} title={passkeyOk ? 'Use a passkey saved on this device' : 'This browser does not support passkeys'} onClick={passkeyLogin}>Sign in with a passkey</button>}
        {mode === 'login' && <button type="button" class="kali-auth-switch" onClick={() => switchMode('forgot')}>Forgot password?</button>}
        <button type="button" class="kali-auth-switch" onClick={() => switchMode(mode === 'login' ? 'register' : 'login')}>
          {mode === 'login' ? "Don't have an account? Sign up" : 'Already have an account? Sign in'}
        </button>
      </form>
    </div>
  );
}

export function LegalFooter({ className = '' }: { className?: string }) {
  return (
    <footer class={`account-legal ${className}`.trim()}>
      <a href="/privacy">Privacy Policy</a>
      <span aria-hidden="true">·</span>
      <a href="/terms">Terms of Service</a>
    </footer>
  );
}
