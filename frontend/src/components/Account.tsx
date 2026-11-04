import { useEffect, useState } from 'preact/hooks';
import type { ComponentChildren } from 'preact';
import { Check, Copy, EyeOff, KeyRound, Lock, LogOut, MonitorSmartphone, Shield, ShieldCheck, Smartphone, Syringe, Trash2, User, Zap } from 'lucide-preact';
import { readShellKey, writeShellKey } from '../lib/run';
import { api } from '../lib/api';
import { authReady, loadMe, logout, user, requireVerifiedEmail, type User as KaliUser } from '../lib/auth';
import { load } from '../lib/preferences';
import { fetchPlanMe, formatResetsIn, openPortal, planStatusLine, type Meter, type PlanMe } from '../lib/billing';
import { formatShekels, planName } from '../lib/plans';
import AuthGate, { LegalFooter } from './AuthGate';
import SecurityPasskeys from './SecurityPasskeys';
import SecurityTotp from './SecurityTotp';
import '../styles/account.css';

interface Session { id: string; created: string; lastSeen: string; ip: string | null; userAgent: string | null; current: boolean; }
interface Usage { requestsToday: number; dailyLimit: number; }
interface ApiKey { id: number; name: string; prefix: string; created: string; lastUsed: string | null; }
type NoticeState = { ok?: string; err?: string };

const errText = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

function Notice({ notice }: { notice: NoticeState }) {
  if (notice.err) return <p class="account-status err" role="alert">{notice.err}</p>;
  if (notice.ok) return <p class="account-status ok" role="status">{notice.ok}</p>;
  return null;
}

function Section({ icon, title, id, children, className = '' }: { icon: ComponentChildren; title: string; id: string; children: ComponentChildren; className?: string }) {
  return (
    <section class={`settings-section account-card ${className}`.trim()} aria-labelledby={id}>
      <h2 id={id}>{icon}{title}</h2>
      {children}
    </section>
  );
}

function deviceLabel(ua: string | null): string {
  if (!ua) return 'Unknown device';
  const browser = /Edg\//.test(ua) ? 'Edge' : /OPR\//.test(ua) ? 'Opera' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox' : /Safari\//.test(ua) ? 'Safari' : /curl|python|Go-http/i.test(ua) ? 'API client' : 'Browser';
  const os = /Windows/.test(ua) ? 'Windows' : /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Mac OS/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : '';
  return os ? `${browser} on ${os}` : browser;
}

export default function Account() {
  const [ready, setReady] = useState(false);
  useEffect(() => { load(); loadMe().then(() => setReady(true)); }, []);
  // The dashboard mounts after hydration, so a #section link needs a manual scroll.
  useEffect(() => {
    if (!ready || !location.hash) return;
    const el = document.getElementById(location.hash.slice(1));
    if (el) requestAnimationFrame(() => el.scrollIntoView({ block: 'start', behavior: 'smooth' }));
  }, [ready]);

  const u = user.value;
  return (
    <>
      {!ready || !authReady.value
        ? <main class="account-shell"><div class="account-loading"><Syringe size={24} /></div></main>
        : !u
          ? <main class="account-shell"><AuthGate icon={<Syringe size={20} />} title="Your account" subtitle="Sign in to manage your profile, security, and plan." onDone={() => location.reload()} /><LegalFooter /></main>
          : <Dashboard user={u} />}
    </>
  );
}

function UsageMeter({ label, meter, now }: { label: string; meter: Meter; now: number }) {
  const left = Math.max(0, meter.cap - meter.used);
  return (
    <div class="settings-row settings-row-stack">
      <div class="account-meter-head">
        <strong>{label}</strong>
        <small>{formatShekels(left)} left · resets {formatResetsIn(meter.resetsAt, now)}</small>
      </div>
      <div class="account-meter"><div style={{ width: `${Math.min(100, (meter.used / Math.max(1, meter.cap)) * 100)}%` }} /></div>
      <small>{formatShekels(meter.used)} of {formatShekels(meter.cap)} used</small>
    </div>
  );
}

/** Plan & usage: the plan line, the two windows (or the free daily pool), and billing. */
function PlanSection({ user: u, usage, planMe }: { user: KaliUser; usage: Usage | null; planMe: PlanMe | null }) {
  const [billingError, setBillingError] = useState('');
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(t);
  }, []);
  const plan = planMe?.plan ?? null;
  const openBilling = async () => {
    setBusy(true); setBillingError('');
    const r = await openPortal();
    if (!r.ok) { setBillingError(r.error); setBusy(false); return; }
    location.assign(r.url);
  };
  const requests = usage ? `${usage.requestsToday} request${usage.requestsToday === 1 ? '' : 's'} today` : '';
  return (
    <Section id="acc-plan" icon={<Zap size={16} />} title="Plan & usage">
      <div class="settings-row">
        <span><strong>Plan</strong><small>{planMe ? (u.admin ? `Admin: unlimited usage. ${planStatusLine(plan)}` : planStatusLine(plan)) : 'Loading…'}</small></span>
        <div class="settings-row-actions"><a class="settings-action primary" href="/pricing">{plan ? 'Change plan' : 'Upgrade'}</a></div>
      </div>
      {planMe?.admin && !planMe.usage && (
        <div class="settings-row settings-row-stack">
          <div class="account-meter-head"><strong>AI usage</strong><small>Unlimited (admin)</small></div>
          <div class="account-meter"><div style={{ width: '0%' }} /></div>
          <small>{requests || 'No cap on this account.'}</small>
        </div>
      )}
      {planMe?.usage && (
        <>
          <UsageMeter label="5-hour window" meter={planMe.usage.fiveHour} now={now} />
          <UsageMeter label="Weekly window" meter={planMe.usage.weekly} now={now} />
        </>
      )}
      {planMe && !planMe.usage && planMe.free && !planMe.admin && (
        <div class="settings-row settings-row-stack">
          <div class="account-meter-head">
            <strong>Free pool today</strong>
            <small>{formatShekels(Math.max(0, planMe.free.cap - planMe.free.used))} left · resets {formatResetsIn(planMe.free.resetsAt, now)}</small>
          </div>
          <div class="account-meter"><div style={{ width: `${Math.min(100, (planMe.free.used / Math.max(1, planMe.free.cap)) * 100)}%` }} /></div>
          <small>{formatShekels(planMe.free.used)} of {formatShekels(planMe.free.cap)} used on Kali Epsilon{requests ? ` · ${requests}` : ''}</small>
        </div>
      )}
      <div class="settings-row">
        <span><strong>Billing</strong><small>{plan?.managed ? 'Payment method, invoices and cancellation.' : plan ? 'This plan was granted; there is nothing to bill.' : 'No active subscription.'}</small></span>
        <div class="settings-row-actions">
          {plan?.managed
            ? <button class="settings-action" type="button" disabled={busy} onClick={() => void openBilling()}>Manage billing</button>
            : <a class="settings-action" href="/pricing">See plans</a>}
        </div>
        {billingError && <p class="account-status err">{billingError}</p>}
      </div>
    </Section>
  );
}

function Dashboard({ user: u }: { user: KaliUser }) {
  const [usage, setUsage] = useState<Usage | null>(null);
  const [planMe, setPlanMe] = useState<PlanMe | null>(null);
  const [sessions, setSessions] = useState<Session[]>([]);
  const refresh = () => loadMe(true);
  const reloadSessions = () => api<{ sessions: Session[] }>('/api/account/sessions').then((d) => setSessions(d.sessions)).catch(() => {});
  useEffect(() => {
    api<Usage>('/api/usage').then(setUsage).catch(() => {});
    fetchPlanMe().then(setPlanMe);
    reloadSessions();
  }, []);

  return (
    <main class="account-shell">
      <header class="account-hero">
        <span class="account-avatar" aria-hidden="true"><User size={22} /></span>
        <div>
          <h1>Estro account{u.admin && <span class="account-badge warn" style={{ marginLeft: 10 }}>Admin</span>}{u.sandbox && <span class="account-badge" style={{ marginLeft: 6 }}>Sandbox</span>}</h1>
          <p>
            <span class="account-meta-name">{u.username}</span>
            <span class="account-plan-badge">{u.admin ? 'Unlimited' : planMe ? planName(planMe.plan?.id) : ''}</span>
            <span>member since {new Date(u.created).toLocaleDateString()}</span>
          </p>
        </div>
        <div class="account-hero-actions">
          {u.admin && <a class="settings-action" href="/admin"><ShieldCheck size={14} /> Admin panel</a>}
          <a class="settings-action" href="/settings">Settings</a>
          <button class="settings-action account-signout" type="button" onClick={logout}><LogOut size={14} /> Sign out</button>
        </div>
      </header>

      <ProfileSection user={u} onChanged={refresh} />

      <PlanSection user={u} usage={usage} planMe={planMe} />

      <ApiKeysSection />

      {u.admin && (
        <Section id="acc-admin" icon={<ShieldCheck size={16} />} title="Administration">
          <div class="settings-row">
            <span><strong>Admin dashboard</strong><small>Users, bans, sandbox and admin flags, usage, and the audit trail.</small></span>
            <div class="settings-row-actions"><a class="settings-action primary" href="/admin"><ShieldCheck size={13} /> Open dashboard</a></div>
          </div>
        </Section>
      )}

      <SecuritySection onChanged={() => { refresh(); reloadSessions(); }} />
      <SessionsSection sessions={sessions} onChanged={reloadSessions} />
      <DangerSection user={u} />
      <LegalFooter />
    </main>
  );
}

function ProfileSection({ user: u, onChanged }: { user: KaliUser; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [notice, setNotice] = useState<NoticeState>({});
  const [busy, setBusy] = useState(false);
  const [email, setEmail] = useState('');
  const [pw, setPw] = useState('');
  const letter = (u.username.trim()[0] || '?').toUpperCase();

  const save = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setNotice({});
    try {
      await api('/api/account/email', { body: { email, password: pw } });
      setNotice({ ok: 'Email updated. A verification code is on its way.' });
      setOpen(false); setEmail(''); setPw('');
      onChanged();
    } catch (err) { setNotice({ err: errText(err, 'Could not update email.') }); }
    setBusy(false);
  };

  return (
    <Section id="acc-profile" icon={<User size={16} />} title="Profile">
      <div class="account-profile-head">
        <span class="account-avatar" aria-hidden="true" style={{ width: 64, height: 64, fontSize: 26, fontWeight: 650 }}>{letter}</span>
        <div class="account-profile-name">
          <span class="account-profile-label">Username</span>
          <strong>{u.username}</strong>
          <small>Joined {new Date(u.created).toLocaleDateString()}{u.lastLogin ? ` · last sign-in ${new Date(u.lastLogin).toLocaleString()}` : ''}</small>
        </div>
      </div>
      <div class="settings-row">
        <span>
          <strong>Email</strong>
          <small>{u.email ? <>{u.email} {u.emailVerified ? <span class="account-badge on">Verified</span> : <span class="account-badge warn">Unverified</span>}</> : 'No email set.'}</small>
        </span>
        <div class="settings-row-actions">
          {u.email && !u.emailVerified && <a class="settings-action primary" href="/verify">Verify</a>}
          <button class="settings-action" type="button" onClick={() => { setOpen(!open); setNotice({}); }}>{open ? 'Close' : 'Change'}</button>
        </div>
      </div>
      {open && (
        <form class="account-form" onSubmit={save}>
          <label>New email address</label>
          <input type="email" value={email} onInput={(e) => setEmail((e.target as HTMLInputElement).value)} autoComplete="email" />
          <label>Confirm with password</label>
          <input type="password" value={pw} onInput={(e) => setPw((e.target as HTMLInputElement).value)} autoComplete="current-password" />
          <div class="account-form-row">
            <button class="settings-action primary" type="submit" disabled={busy || !email || !pw}>Send code</button>
            <button class="settings-action" type="button" onClick={() => setOpen(false)}>Cancel</button>
          </div>
        </form>
      )}
      {!u.emailVerified && requireVerifiedEmail.value && !u.admin && <p class="account-empty">Chat unlocks once your email is verified.</p>}
      {(notice.ok || notice.err) && <div class="account-card-foot"><Notice notice={notice} /></div>}
    </Section>
  );
}

function ApiKeysSection() {
  const [keys, setKeys] = useState<ApiKey[] | null>(null);
  const [name, setName] = useState('');
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [secret, setSecret] = useState<{ name: string; value: string } | null>(null);
  const [copied, setCopied] = useState(false);
  const [notice, setNotice] = useState<NoticeState>({});

  const reload = () => api<{ keys: ApiKey[] }>('/api/account/api-keys').then((d) => setKeys(d.keys)).catch((e) => { setKeys([]); setNotice({ err: errText(e, 'Could not load API keys.') }); });
  useEffect(() => { reload(); }, []);

  const create = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setNotice({});
    try {
      const d = await api<{ key: ApiKey; secret: string }>('/api/account/api-keys', { body: { name: name.trim() || 'API key' } });
      setSecret({ name: d.key.name, value: d.secret });
      setCopied(false);
      setOpen(false); setName('');
      reload();
    } catch (err) { setNotice({ err: errText(err, 'Could not create the key.') }); }
    setBusy(false);
  };

  const revoke = async (k: ApiKey) => {
    if (!confirm(`Revoke "${k.name}"? Clients using it will stop working.`)) return;
    try {
      await api(`/api/account/api-keys/${k.id}`, { method: 'DELETE' });
      const cached = readShellKey();
      if (cached && cached.startsWith(k.prefix)) writeShellKey(null);
      setNotice({ ok: 'Key revoked.' });
      reload();
    } catch (err) { setNotice({ err: errText(err, 'Could not revoke the key.') }); }
  };

  const copy = async () => {
    if (!secret) return;
    try { await navigator.clipboard.writeText(secret.value); setCopied(true); } catch {}
  };

  return (
    <Section id="acc-keys" icon={<KeyRound size={16} />} title="API keys">
      <div class="settings-row">
        <span><strong>Bearer keys for clients</strong><small>Use one as <code>Authorization: Bearer</code> from SDKs, editors and the in-chat shell. The secret is shown once.</small></span>
        <div class="settings-row-actions"><button class="settings-action primary" type="button" onClick={() => { setOpen(!open); setNotice({}); }}>{open ? 'Cancel' : 'Create key'}</button></div>
      </div>
      {open && (
        <form class="account-form" onSubmit={create}>
          <label>Key name</label>
          <input value={name} placeholder="e.g. laptop, Cursor, Shell" onInput={(e) => setName((e.target as HTMLInputElement).value)} autoFocus />
          <div class="account-form-row">
            <button class="settings-action primary" type="submit" disabled={busy}>Create</button>
            <button class="settings-action" type="button" onClick={() => setOpen(false)}>Cancel</button>
          </div>
        </form>
      )}
      {secret && (
        <div class="account-secret" role="status">
          <strong>New key “{secret.name}”</strong>
          <small>Copy it now. It is not stored and cannot be shown again.</small>
          <div class="account-secret-row">
            <code>{secret.value}</code>
            <button class="settings-action" type="button" onClick={copy}>{copied ? <><Check size={13} /> Copied</> : <><Copy size={13} /> Copy</>}</button>
            <button class="settings-action" type="button" onClick={() => setSecret(null)}>Done</button>
          </div>
        </div>
      )}
      {keys === null && <p class="account-empty">Loading…</p>}
      {keys && keys.length === 0 && <p class="account-empty">No API keys yet.</p>}
      {keys?.map((k) => (
        <div class="account-list-row" key={k.id}>
          <span class="account-list-icon"><KeyRound size={16} /></span>
          <div class="account-list-body">
            <strong>{k.name} <code class="account-key-prefix">{k.prefix}…</code></strong>
            <small>created {new Date(k.created).toLocaleDateString()}{k.lastUsed ? ` · last used ${new Date(k.lastUsed).toLocaleString()}` : ' · never used'}</small>
          </div>
          <button class="settings-action danger" type="button" onClick={() => revoke(k)}><Trash2 size={13} /> Revoke</button>
        </div>
      ))}
      {(notice.ok || notice.err) && <div class="account-card-foot"><Notice notice={notice} /></div>}
    </Section>
  );
}

function SecuritySection({ onChanged }: { onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [notice, setNotice] = useState<NoticeState>({});
  const [busy, setBusy] = useState(false);
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');

  const save = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setNotice({});
    try {
      await api('/api/account/password', { body: { current, password: next } });
      setNotice({ ok: 'Password changed. Other sessions were signed out.' });
      setOpen(false); setCurrent(''); setNext('');
      onChanged();
    } catch (err) { setNotice({ err: errText(err, 'Could not change password.') }); }
    setBusy(false);
  };

  return (
    <Section id="acc-security" icon={<Shield size={16} />} title="Security & sign-in">
      <div class="settings-row">
        <span><strong>Password</strong><small>Changing it signs out your other devices.</small></span>
        <div class="settings-row-actions"><button class="settings-action" type="button" onClick={() => { setOpen(!open); setNotice({}); }}><Lock size={13} />{open ? ' Close' : ' Change'}</button></div>
      </div>
      {open && (
        <form class="account-form" onSubmit={save}>
          <label>Current password</label>
          <input type="password" value={current} onInput={(e) => setCurrent((e.target as HTMLInputElement).value)} autoComplete="current-password" />
          <label>New password (8+ characters)</label>
          <input type="password" value={next} onInput={(e) => setNext((e.target as HTMLInputElement).value)} autoComplete="new-password" />
          <div class="account-form-row">
            <button class="settings-action primary" type="submit" disabled={busy || !current || next.length < 8}>Save</button>
            <button class="settings-action" type="button" onClick={() => setOpen(false)}>Cancel</button>
          </div>
        </form>
      )}
      {(notice.ok || notice.err) && <div class="account-card-foot"><Notice notice={notice} /></div>}
      <SecurityPasskeys onChanged={onChanged} />
      <SecurityTotp enabled={!!user.value?.totpEnabled} onChanged={onChanged} />
    </Section>
  );
}

function SessionsSection({ sessions, onChanged }: { sessions: Session[]; onChanged: () => void }) {
  const [notice, setNotice] = useState<NoticeState>({});
  const others = sessions.filter((s) => !s.current);
  return (
    <Section id="acc-sessions" icon={<MonitorSmartphone size={16} />} title="Sessions">
      {sessions.length === 0 && <p class="account-empty">Loading…</p>}
      {sessions.map((s) => (
        <div class="account-list-row" key={s.id}>
          <span class="account-list-icon"><Smartphone size={16} /></span>
          <div class="account-list-body">
            <strong>{deviceLabel(s.userAgent)}{s.current && <span class="account-badge on" style={{ marginLeft: 8 }}>This device</span>}</strong>
            <small>{[s.ip, `signed in ${new Date(s.created).toLocaleDateString()}`, `last active ${new Date(s.lastSeen).toLocaleString()}`].filter(Boolean).join(' · ')}</small>
          </div>
          {!s.current && (
            <button class="settings-action danger" type="button" onClick={() => {
              api(`/api/account/sessions/${s.id}`, { method: 'DELETE' }).then(() => { setNotice({ ok: 'Session signed out.' }); onChanged(); }).catch((e) => setNotice({ err: errText(e, 'Failed') }));
            }}><EyeOff size={13} /> Sign out</button>
          )}
        </div>
      ))}
      <div class="settings-row">
        <span><strong>{sessions.length} active session{sessions.length === 1 ? '' : 's'}</strong><small>{others.length} other than this device.</small></span>
        <div class="settings-row-actions">
          <button class="settings-action" type="button" disabled={!others.length} onClick={() => {
            api('/api/account/sessions/revoke-others', { body: {} }).then(() => { setNotice({ ok: 'Signed out everywhere else.' }); onChanged(); }).catch((e) => setNotice({ err: errText(e, 'Failed') }));
          }}><KeyRound size={13} /> Sign out all others</button>
        </div>
      </div>
      {(notice.ok || notice.err) && <div class="account-card-foot"><Notice notice={notice} /></div>}
    </Section>
  );
}

function DangerSection({ user: u }: { user: KaliUser }) {
  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState('');
  const [typed, setTyped] = useState('');
  const [notice, setNotice] = useState<NoticeState>({});
  const [busy, setBusy] = useState(false);
  const confirmed = typed.trim() === u.username && password.length > 0;

  const submit = async (e: Event) => {
    e.preventDefault();
    if (!confirmed) return;
    setBusy(true); setNotice({});
    try {
      await api('/api/account', { method: 'DELETE', body: { password } });
      user.value = null;
      location.href = '/';
    } catch (err) { setNotice({ err: errText(err, 'Failed to delete account') }); setBusy(false); }
  };

  return (
    <section class="account-danger" aria-labelledby="acc-danger">
      <div class="account-danger-head">
        <span class="account-danger-icon" aria-hidden="true"><Trash2 size={16} /></span>
        <div>
          <h2 id="acc-danger">Delete account</h2>
          <p>Permanently removes your login, sessions, chats and usage history. This cannot be undone.</p>
        </div>
        {!open && <button class="settings-action danger" type="button" onClick={() => setOpen(true)}>Delete account</button>}
      </div>
      {open && (
        <form class="account-danger-form" onSubmit={submit}>
          <label for="acc-del-typed">Type <code>{u.username}</code> to confirm</label>
          <input id="acc-del-typed" value={typed} onInput={(e) => setTyped((e.target as HTMLInputElement).value)} autoComplete="off" autoFocus />
          <label for="acc-del-pw">Current password</label>
          <input id="acc-del-pw" type="password" value={password} onInput={(e) => setPassword((e.target as HTMLInputElement).value)} autoComplete="current-password" />
          {notice.err && <p class="account-status err" role="alert">{notice.err}</p>}
          <div class="account-form-row">
            <button class="settings-action danger solid" type="submit" disabled={busy || !confirmed}>{busy ? 'Deleting…' : 'Permanently delete account'}</button>
            <button class="settings-action" type="button" onClick={() => { setOpen(false); setPassword(''); setTyped(''); }} disabled={busy}>Cancel</button>
          </div>
        </form>
      )}
    </section>
  );
}
