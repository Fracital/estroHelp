import { useEffect, useMemo, useState } from 'preact/hooks';
import { ArrowLeft, ChevronLeft, ChevronRight, FlaskConical, LogOut, MailCheck, RefreshCw, Search, ShieldCheck, UserCog, X } from 'lucide-preact';
import { api } from '../lib/api';
import { gate, user } from '../lib/auth';
import type { User, Ban } from '../lib/auth';
import { load } from '../lib/preferences';
import '../styles/account.css';
import '../styles/admin.css';

interface AdminUser extends User { ban: Ban | null; requestsToday: number; threads: number; }
interface Stats { users: number; verified: number; sandbox: number; banned: number; requestsToday: number; requests7d: number; activeToday: number; threads: number; }
interface UsageRow { day: string; model: string; requests: number; promptChars: number; users: number; }
interface AuditEntry { id: number; actor: string; action: string; target: string | null; detail: string | null; created: string; }
type Tab = 'overview' | 'users' | 'audit';
type NoticeState = { ok?: string; err?: string };

const TABS: [Tab, string][] = [['overview', 'Overview'], ['users', 'Users'], ['audit', 'Audit']];
const fmt = (d: string) => new Date(d).toLocaleString();
const day = (d: string) => new Date(d).toLocaleDateString();
const errText = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

function tabFromHash(): Tab {
  const h = location.hash.replace('#', '');
  return TABS.some(([k]) => k === h) ? (h as Tab) : 'overview';
}

export default function AdminPanel() {
  const [ready, setReady] = useState(false);
  const [tab, setTabState] = useState<Tab>('overview');
  const [status, setStatus] = useState<NoticeState>({});
  const [refreshing, setRefreshing] = useState(false);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    load();
    gate('admin').then((ok) => { if (ok) { setTabState(tabFromHash()); setReady(true); } });
    const onHash = () => setTabState(tabFromHash());
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

  const setTab = (next: Tab) => {
    setTabState(next);
    setStatus({});
    history.replaceState(null, '', next === 'overview' ? location.pathname : `#${next}`);
  };
  const onTabKey = (e: KeyboardEvent, i: number) => {
    const delta = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
    if (!delta) return;
    e.preventDefault();
    const next = TABS[(i + delta + TABS.length) % TABS.length][0];
    setTab(next);
    (document.querySelector(`.admin-tab[data-tab="${next}"]`) as HTMLElement | null)?.focus();
  };
  const reloadAll = () => { setRefreshing(true); setTick((n) => n + 1); setTimeout(() => setRefreshing(false), 400); };

  if (!ready) return <main class="account-shell" />;
  return (
    <main class="account-shell">
      <header class="admin-header">
        <div class="admin-header-title">
          <div class="admin-header-icon"><ShieldCheck size={22} /></div>
          <div>
            <h1>Admin Dashboard</h1>
            <p>Manage users, bans, sandbox and admin flags, usage, and the audit trail.</p>
          </div>
        </div>
        <div class="admin-header-actions">
          <a class="settings-action" href="/account"><ArrowLeft size={14} /> Account</a>
          <button class="settings-action" type="button" disabled={refreshing} onClick={reloadAll}><RefreshCw size={14} class={refreshing ? 'spin' : ''} />{refreshing ? ' Refreshing' : ' Refresh'}</button>
        </div>
      </header>

      <section class="settings-section account-section-wide">
        <div class="admin-tabs" role="tablist" aria-label="Admin sections">
          {TABS.map(([key, label], i) => (
            <button key={key} type="button" role="tab" data-tab={key} aria-selected={tab === key} tabIndex={tab === key ? 0 : -1}
              class={`admin-tab${tab === key ? ' active' : ''}`} onClick={() => setTab(key)} onKeyDown={(e) => onTabKey(e, i)}>{label}</button>
          ))}
        </div>
        {(status.ok || status.err) && <p class={`account-status admin-status ${status.err ? 'err' : 'ok'}`}>{status.err || status.ok}</p>}
        <div role="tabpanel">
          {tab === 'overview' && <Overview key={tick} />}
          {tab === 'users' && <Users key={tick} onStatus={setStatus} />}
          {tab === 'audit' && <Audit key={tick} />}
        </div>
      </section>
    </main>
  );
}

function Overview() {
  const [stats, setStats] = useState<Stats | null>(null);
  const [usage, setUsage] = useState<UsageRow[]>([]);
  useEffect(() => {
    api<Stats>('/api/admin/stats').then(setStats).catch(() => {});
    api<{ rows: UsageRow[] }>('/api/admin/usage?days=14').then((d) => setUsage(d.rows.reverse())).catch(() => {});
  }, []);
  if (!stats) return <p class="account-empty">Loading</p>;
  const tiles: [string, number, boolean?][] = [
    ['Users', stats.users, true], ['Verified', stats.verified], ['Active today', stats.activeToday], ['Requests today', stats.requestsToday, true],
    ['Requests 7d', stats.requests7d], ['Threads', stats.threads], ['Sandbox', stats.sandbox], ['Banned', stats.banned],
  ];
  return (
    <div class="ads-dash">
      <div class="admin-overview">
        <div class="ads-stat-grid">
          {tiles.map(([label, v, accent]) => (
            <div class={`ads-stat-card${accent ? ' accent' : ''}`} key={label}><small>{label}</small><strong>{v.toLocaleString()}</strong></div>
          ))}
        </div>
      </div>
      <div class="admin-toolbar"><strong class="admin-toolbar-title">Usage, last 14 days</strong></div>
      <div class="admin-list">
        <div class="admin-head usage" aria-hidden="true"><span>Day</span><span>Model</span><span class="num">Requests</span><span class="num">Users</span><span class="num">Prompt chars</span></div>
        {usage.length === 0 && <p class="account-empty">No usage yet.</p>}
        {usage.map((r) => (
          <div class="admin-row usage" key={r.day + r.model}>
            <div class="admin-cell"><span class="cell-main">{day(r.day)}</span></div>
            <div class="admin-cell"><span class="account-badge">{r.model}</span></div>
            <div class="admin-cell admin-num"><span class="cell-main">{r.requests}</span></div>
            <div class="admin-cell admin-num">{r.users}</div>
            <div class="admin-cell admin-num"><span class="cell-sub">{r.promptChars.toLocaleString()}</span></div>
          </div>
        ))}
      </div>
    </div>
  );
}

function Pagination({ page, pageSize, total, onPage, hasMore }: { page: number; pageSize: number; total?: number; onPage: (p: number) => void; hasMore?: boolean }) {
  const totalPages = total !== undefined ? Math.max(1, Math.ceil(total / pageSize)) : (hasMore ? page + 1 : page);
  const start = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const end = total !== undefined ? Math.min(page * pageSize, total) : page * pageSize;
  const pages = useMemo(() => {
    const out: (number | 'gap')[] = [];
    if (totalPages <= 7) { for (let i = 1; i <= totalPages; i++) out.push(i); return out; }
    out.push(1);
    if (page > 3) out.push('gap');
    for (let i = Math.max(2, page - 1); i <= Math.min(totalPages - 1, page + 1); i++) out.push(i);
    if (page < totalPages - 2) out.push('gap');
    out.push(totalPages);
    return out;
  }, [page, totalPages]);
  return (
    <div class="admin-pagination">
      <div class="admin-pagination-info">
        {total !== undefined ? <>Showing <strong>{start} to {end}</strong> of <strong>{total}</strong></> : <>Page <strong>{page}</strong></>}
      </div>
      <div class="admin-pagination-controls">
        <div class="admin-pagination-pages">
          <button type="button" class="admin-pagination-btn" disabled={page <= 1} onClick={() => onPage(page - 1)} aria-label="Previous page"><ChevronLeft size={14} /></button>
          {pages.map((p, i) => p === 'gap'
            ? <span key={`gap${i}`} class="admin-pagination-gap">...</span>
            : <button key={p} type="button" class={`admin-pagination-btn${p === page ? ' admin-pagination-current' : ''}`} aria-current={p === page ? 'page' : undefined} onClick={() => onPage(p)}>{p}</button>)}
          <button type="button" class="admin-pagination-btn" disabled={page >= totalPages} onClick={() => onPage(page + 1)} aria-label="Next page"><ChevronRight size={14} /></button>
        </div>
      </div>
    </div>
  );
}

function Users({ onStatus }: { onStatus: (s: NoticeState) => void }) {
  const [q, setQ] = useState('');
  const [page, setPage] = useState(1);
  const [rows, setRows] = useState<AdminUser[]>([]);
  const [total, setTotal] = useState(0);
  const [pageSize, setPageSize] = useState(50);
  const [target, setTarget] = useState<AdminUser | null>(null);
  const me = user.value!;

  async function loadUsers() {
    const d = await api<{ users: AdminUser[]; total: number; pageSize: number }>(`/api/admin/users?q=${encodeURIComponent(q)}&page=${page}`);
    setRows(d.users); setTotal(d.total); setPageSize(d.pageSize);
    setTarget((t) => (t ? d.users.find((u) => u.id === t.id) ?? null : null));
  }
  useEffect(() => { loadUsers().catch(() => {}); }, [q, page]);

  return (
    <>
      <div class="admin-toolbar">
        <div class="admin-search-wrap">
          <Search size={14} class="admin-search-icon" />
          <input type="text" class="admin-search-input" placeholder="Search by username or email" value={q} onInput={(e) => { setPage(1); setQ((e.target as HTMLInputElement).value); }} />
        </div>
        <div class="admin-toolbar-note">Total: <strong>{total}</strong> user{total === 1 ? '' : 's'}</div>
      </div>
      <div class="admin-list admin-users">
        <div class="admin-head" aria-hidden="true"><span>User</span><span>Email</span><span>Flags</span><span class="num">Today</span><span class="num">Threads</span><span>Joined</span><span /></div>
        {rows.length === 0 ? <p class="account-empty">No users found.</p> : rows.map((u) => (
          <div class="admin-row" key={u.id}>
            <div class="admin-cell admin-user">
              <span class="cell-main">{u.username}</span>{u.id === me.id ? ' (you)' : ''}
              <small class="cell-dim">#{u.id}{u.lastLogin ? ` · last sign-in ${day(u.lastLogin)}` : ''}</small>
            </div>
            <div class="admin-cell admin-email">
              <span class="admin-label">Email</span>
              <span class="admin-email-addr" title={u.email ?? 'no email'}>{u.email ?? 'no email'}</span>
              {u.emailVerified ? <span class="account-badge on">Verified</span> : <span class="account-badge warn">Unverified</span>}
            </div>
            <div class="admin-cell admin-flags">
              <span class="admin-label">Flags</span>
              {u.admin && <span class="account-badge warn">Admin</span>}
              {u.sandbox && <span class="account-badge">Sandbox</span>}
              {u.ban && <span class="account-badge danger" title={u.ban.reason}>Banned</span>}
              {!u.admin && !u.sandbox && !u.ban && <span class="cell-dim">-</span>}
            </div>
            <div class="admin-cell admin-num"><span class="admin-label">Today</span><span class="cell-main">{u.requestsToday}</span> <span class="cell-sub">req</span></div>
            <div class="admin-cell admin-num"><span class="admin-label">Threads</span><span class="cell-main">{u.threads}</span></div>
            <div class="admin-cell admin-joined"><span class="admin-label">Joined</span><span class="cell-sub">{day(u.created)}</span></div>
            <div class="admin-cell admin-actions">
              <button class="settings-action" type="button" onClick={() => setTarget(u)}><UserCog size={13} /> Manage</button>
            </div>
          </div>
        ))}
      </div>
      <Pagination page={page} pageSize={pageSize} total={total} onPage={setPage} />
      {target && <ManageUserModal user={target} self={target.id === me.id} onClose={() => setTarget(null)} onChanged={(s) => { onStatus(s); loadUsers().catch(() => {}); }} />}
    </>
  );
}

function Switch({ on, label, icon, disabled, title, onToggle }: { on: boolean; label: string; icon: any; disabled?: boolean; title?: string; onToggle: () => void }) {
  return (
    <div class="manage-ent-row manage-switch-row">
      <span class="manage-switch-label">{icon} {label}</span>
      <button type="button" role="switch" aria-checked={on} aria-label={label} title={title} class={`manage-toggle${on ? ' on' : ''}`} disabled={disabled} onClick={onToggle}><span /></button>
    </div>
  );
}

function ManageUserModal({ user: u, self, onClose, onChanged }: { user: AdminUser; self: boolean; onClose: () => void; onChanged: (s: NoticeState) => void }) {
  const [tab, setTab] = useState<'access' | 'danger'>('access');
  const [banReason, setBanReason] = useState('');
  const [banDays, setBanDays] = useState('');
  const [notice, setNotice] = useState<NoticeState>({});
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, []);

  const act = async (path: string, body: unknown, ok: string, method = 'POST') => {
    setBusy(true); setNotice({});
    try { await api(`/api/admin/users/${u.id}${path}`, { method, body: body ?? {} }); setNotice({ ok }); onChanged({ ok }); }
    catch (e) { setNotice({ err: errText(e, 'That did not work.') }); }
    setBusy(false);
  };

  return (
    <div class="account-modal-backdrop" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div class="account-modal manage-modal" role="dialog" aria-modal="true" aria-label={`Manage ${u.username}`}>
        <header>
          <div class="manage-head">
            <h3>{u.username}{self && <span class="account-badge">you</span>}</h3>
            <small>
              {u.email ?? 'No email'}{u.email && <> {u.emailVerified ? <span class="account-badge on">verified</span> : <span class="account-badge warn">unverified</span>}</>}
              <span> · joined {day(u.created)}</span>
            </small>
          </div>
          <button class="account-modal-close" type="button" onClick={onClose} aria-label="Close"><X size={15} /></button>
        </header>
        <div class="manage-summary">
          <span class="manage-chip"><UserCog size={12} /> {u.requestsToday} requests today</span>
          <span class="manage-chip">{u.threads} thread{u.threads === 1 ? '' : 's'}</span>
          {u.ban && <span class="manage-chip"><span class="account-badge danger">Banned{u.ban.expires ? ` until ${day(u.ban.expires)}` : ''}</span></span>}
        </div>
        <div class="admin-tabs manage-tabs" role="tablist">
          <button type="button" role="tab" aria-selected={tab === 'access'} class={`admin-tab${tab === 'access' ? ' active' : ''}`} onClick={() => setTab('access')}>Access</button>
          <button type="button" role="tab" aria-selected={tab === 'danger'} class={`admin-tab danger-tab${tab === 'danger' ? ' active' : ''}`} onClick={() => setTab('danger')}>Danger</button>
        </div>
        <div class="account-modal-body">
          {(notice.ok || notice.err) && <p class={`account-status ${notice.err ? 'err' : 'ok'}`}>{notice.err || notice.ok}</p>}
          {tab === 'access' && (
            <div class="manage-block">
              <div class="manage-ent">
                <Switch on={u.admin} label="Admin" icon={<ShieldCheck size={13} />} disabled={busy || self} title={self ? 'You cannot change your own admin flag' : undefined}
                  onToggle={() => act('/admin', { value: !u.admin }, u.admin ? 'Admin removed.' : 'Admin granted.')} />
                <Switch on={u.sandbox} label="Sandbox" icon={<FlaskConical size={13} />} disabled={busy}
                  onToggle={() => act('/sandbox', { value: !u.sandbox }, u.sandbox ? 'Sandbox off.' : 'Sandbox on. No daily cap and usage is tracked separately.')} />
                <div class="manage-ent-row">
                  <span class="manage-switch-label"><MailCheck size={13} /> Email</span>
                  {u.emailVerified
                    ? <b class="ent-on">Verified</b>
                    : <button class="settings-action" type="button" disabled={busy} onClick={() => act('/verify-email', {}, 'Email marked verified.')}>Mark verified</button>}
                </div>
                <div class="manage-ent-row">
                  <span class="manage-switch-label"><LogOut size={13} /> Sessions</span>
                  <button class="settings-action" type="button" disabled={busy} onClick={() => act('/revoke-sessions', {}, 'Signed out everywhere.')}>Sign out everywhere</button>
                </div>
              </div>
            </div>
          )}
          {tab === 'danger' && (
            <div class="manage-block manage-danger">
              {u.ban ? (
                <div class="manage-ent-row">
                  <span class="account-badge danger">Banned{u.ban.expires ? ` until ${day(u.ban.expires)}` : ' (permanent)'}</span>
                  {u.ban.reason && <b class="cell-dim">{u.ban.reason}</b>}
                  <button class="settings-action" type="button" disabled={busy} onClick={() => act('/unban', {}, 'Ban lifted.')}>Unban</button>
                </div>
              ) : (
                <>
                  <p class="manage-hint">Banning signs the user out and blocks future sign-ins. Leave days blank for a permanent ban.</p>
                  <form class="manage-grant" onSubmit={(e) => { e.preventDefault(); act('/ban', { reason: banReason, hours: (Number(banDays) || 0) * 24 }, 'User banned.'); }}>
                    <input placeholder="Reason (optional)" value={banReason} onInput={(e) => setBanReason((e.target as HTMLInputElement).value)} style={{ flex: 1, minWidth: 140 }} />
                    <input placeholder="Days (blank = permanent)" type="number" min={1} value={banDays} onInput={(e) => setBanDays((e.target as HTMLInputElement).value)} style={{ width: 170 }} />
                    <button class="settings-action danger" type="submit" disabled={busy || self}>Ban user</button>
                  </form>
                  {self && <p class="manage-hint">You cannot ban your own account.</p>}
                </>
              )}
              {!self && (
                <div class="manage-ent-row manage-delete-row">
                  <span class="manage-switch-label">Delete this account and all of its chats</span>
                  <button class="settings-action danger solid" type="button" disabled={busy} onClick={() => { if (confirm(`Delete ${u.username} and all their data?`)) act('', {}, 'User deleted.', 'DELETE').then(onClose); }}>Delete user</button>
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function Audit() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [page, setPage] = useState(1);
  useEffect(() => { api<{ entries: AuditEntry[] }>(`/api/admin/audit?page=${page}`).then((d) => setEntries(d.entries)).catch(() => {}); }, [page]);
  return (
    <>
      <div class="admin-list">
        {entries.length === 0 && <p class="account-empty">No admin actions logged yet.</p>}
        {entries.map((e) => (
          <div class="admin-row audit" key={e.id}>
            <span class="account-badge warn admin-audit-action">{e.action}</span>
            <div class="admin-cell admin-audit-body">
              <span class="cell-main">{e.actor}</span>
              {e.target && <> → <span class="cell-main">{e.target}</span></>}
              {e.detail && <small class="cell-dim">{e.detail}</small>}
            </div>
            <span class="cell-dim admin-audit-when">{fmt(e.created)}</span>
          </div>
        ))}
      </div>
      <Pagination page={page} pageSize={50} onPage={setPage} hasMore={entries.length >= 50} />
    </>
  );
}
