import { useEffect, useMemo, useState } from 'preact/hooks';
import { ArrowLeft, ChevronLeft, ChevronRight, FlaskConical, LogOut, MailCheck, RefreshCw, Search, ShieldCheck, Sparkles, UserCog, X } from 'lucide-preact';
import { api } from '../lib/api';
import { gate, user } from '../lib/auth';
import type { User, Ban } from '../lib/auth';
import { load } from '../lib/preferences';
import { PLANS, formatShekels, formatUsd, planName } from '../lib/plans';
import '../styles/account.css';
import '../styles/admin.css';

interface AdminUser extends User { ban: Ban | null; requestsToday: number; threads: number; plan: string | null; planUntil: string | null; }
interface Stats { users: number; verified: number; sandbox: number; banned: number; requestsToday: number; requests7d: number; activeToday: number; threads: number; }
interface UsageRow { day: string; model: string; requests: number; promptChars: number; users: number; }
interface AuditEntry { id: number; actor: string; action: string; target: string | null; detail: string | null; created: string; }
interface BillingOverview { plans: { id: string; name: string; active: number }[]; requestsToday: number; unitsToday: number; accountsPaidFor: number; revenue30dCents: number; salesOpen: boolean; }
interface Series { days: string[]; requests: number[]; units: number[]; activeUsers: number[]; signups: number[]; revenueCents: number[]; }
interface RevenueBucket { period: string; tier: string; cents: number; count: number; }
interface Revenue { monthly: RevenueBucket[]; daily: RevenueBucket[]; totalCents: number; transactions: number; }
interface Transaction { id: string; username: string; tier: string; amountCents: number | null; currency: string; source: string; actor: string | null; periodEnd: string | null; created: string; }
type Tab = 'overview' | 'users' | 'revenue' | 'audit';
type NoticeState = { ok?: string; err?: string };

const TABS: [Tab, string][] = [['overview', 'Overview'], ['users', 'Users'], ['revenue', 'Revenue'], ['audit', 'Audit']];
const money = (c: number | null | undefined) => c == null ? '-' : `$${(c / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;

/** "Plus until 3 Dec 2026", "Ultra, no end date", "Pro expired 1 Nov 2026" or "none". */
function planStatus(plan: string | null, until: string | null): { text: string; active: boolean } {
  if (!plan) return { text: 'none', active: false };
  const ends = until ? Date.parse(until) : Number.NaN;
  if (Number.isFinite(ends) && ends > Date.now()) {
    if (ends - Date.now() > 20 * 365.25 * 24 * 3600_000) return { text: `${planName(plan)}, no end date`, active: true };
    return { text: `${planName(plan)} until ${new Date(until!).toLocaleDateString()}`, active: true };
  }
  return { text: `${planName(plan)} expired${until ? ` ${new Date(until).toLocaleDateString()}` : ''}`, active: false };
}
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
            <p>Manage users, plans, bans, sandbox and admin flags, usage, revenue, and the audit trail.</p>
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
          {tab === 'revenue' && <RevenueTab key={tick} />}
          {tab === 'audit' && <Audit key={tick} />}
        </div>
      </section>
    </main>
  );
}

/** An area chart with a hover readout, one value per day. The same shape as the per-user sparkline. */
function DailyChart({ title, labels, values, format = (v: number) => v.toLocaleString(), width = 300, height = 64 }: {
  title: string; labels: string[]; values: number[]; format?: (v: number) => string; width?: number; height?: number;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const data = values.length ? values : [0];
  const max = Math.max(1, ...data);
  const stepX = width / Math.max(1, data.length - 1);
  const y = (v: number) => height - (v / max) * (height - 6) - 3;
  const points = data.map((v, i) => `${(i * stepX).toFixed(1)},${y(v).toFixed(1)}`).join(' ');
  const total = data.reduce((s, v) => s + v, 0);
  const labelFor = (i: number) => labels[i] ? new Date(labels[i] + 'T00:00:00Z').toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' }) : '';
  return (
    <div class="admin-chart">
      <div class="admin-chart-head"><strong>{title}</strong><small>{format(total)} in {data.length} days</small></div>
      <div class="spark-wrap" onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const rect = (e.currentTarget as HTMLDivElement).getBoundingClientRect();
          if (!rect.width) return;
          const i = Math.round(((e.clientX - rect.left) / rect.width) * (data.length - 1));
          setHover(Math.max(0, Math.min(data.length - 1, i)));
        }}>
        <div class="spark-tip">{hover == null ? `Last ${data.length} days, per UTC day` : `${format(data[hover])} · ${labelFor(hover)}`}</div>
        <svg class="sparkline" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
          <polygon points={`0,${height} ${points} ${width},${height}`} />
          <polyline points={points} vectorEffect="non-scaling-stroke" />
          {hover != null && <circle class="spark-dot" cx={hover * stepX} cy={y(data[hover])} r={3} />}
        </svg>
      </div>
      <div class="admin-chart-axis"><span>{labelFor(0)}</span><span>{labelFor(data.length - 1)}</span></div>
    </div>
  );
}

function Overview() {
  const [stats, setStats] = useState<Stats | null>(null);
  const [billing, setBilling] = useState<BillingOverview | null>(null);
  const [series, setSeries] = useState<Series | null>(null);
  const [usage, setUsage] = useState<UsageRow[]>([]);
  useEffect(() => {
    api<Stats>('/api/admin/stats').then(setStats).catch(() => {});
    api<BillingOverview>('/api/admin/billing/overview').then(setBilling).catch(() => {});
    api<Series>('/api/admin/series?days=30').then(setSeries).catch(() => {});
    api<{ rows: UsageRow[] }>('/api/admin/usage?days=14').then((d) => setUsage(d.rows.reverse())).catch(() => {});
  }, []);
  if (!stats) return <p class="account-empty">Loading</p>;
  const tiles: [string, number, boolean?][] = [
    ['Users', stats.users, true], ['Verified', stats.verified], ['Active today', stats.activeToday], ['Requests today', stats.requestsToday, true],
    ['Requests 7d', stats.requests7d], ['Threads', stats.threads], ['Sandbox', stats.sandbox], ['Banned', stats.banned],
  ];
  const activePlans = billing ? billing.plans.reduce((s, p) => s + p.active, 0) : 0;
  return (
    <div class="ads-dash">
      <div class="admin-overview">
        <div class="ads-stat-grid">
          {tiles.map(([label, v, accent]) => (
            <div class={`ads-stat-card${accent ? ' accent' : ''}`} key={label}><small>{label}</small><strong>{v.toLocaleString()}</strong></div>
          ))}
        </div>
      </div>
      {billing && (
        <div class="admin-overview">
          <div class="ads-stat-grid">
            <div class="ads-stat-card accent"><small>Revenue, 30 days</small><strong>{money(billing.revenue30dCents)}</strong><em>Real payments only</em></div>
            <div class="ads-stat-card"><small>Active plans</small><strong>{activePlans.toLocaleString()}</strong><em>{billing.plans.filter((p) => p.active).map((p) => `${p.active} ${p.name}`).join(', ') || 'none yet'}</em></div>
            <div class="ads-stat-card"><small>Usage today</small><strong>{formatShekels(billing.unitsToday)}</strong><em>{billing.requestsToday.toLocaleString()} metered requests</em></div>
            <div class="ads-stat-card"><small>Accounts paid for</small><strong>{billing.accountsPaidFor.toLocaleString(undefined, { maximumFractionDigits: 2 })}</strong><em>{billing.salesOpen ? 'Sales open' : 'Sales closed'}</em></div>
          </div>
        </div>
      )}
      {series && (
        <div class="admin-charts">
          <DailyChart title="Requests per day" labels={series.days} values={series.requests} />
          <DailyChart title="Usage per day" labels={series.days} values={series.units} format={formatShekels} />
          <DailyChart title="Active users per day" labels={series.days} values={series.activeUsers} />
          <DailyChart title="New sign-ups per day" labels={series.days} values={series.signups} />
        </div>
      )}
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
        <div class="admin-head" aria-hidden="true"><span>User</span><span>Email</span><span>Plan</span><span>Flags</span><span class="num">Today</span><span class="num">Threads</span><span>Joined</span><span /></div>
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
            <div class="admin-cell admin-plan">
              <span class="admin-label">Plan</span>
              {(() => { const s = planStatus(u.plan, u.planUntil); return s.active ? <span class="account-badge on" title={s.text}>{planName(u.plan)}</span> : <span class="cell-dim">{s.text}</span>; })()}
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
  const [tab, setTab] = useState<'plan' | 'access' | 'danger'>('plan');
  const [banReason, setBanReason] = useState('');
  const [banDays, setBanDays] = useState('');
  const [grantPlan, setGrantPlan] = useState(u.plan && PLANS.some((p) => p.id === u.plan) ? u.plan : 'plus');
  const [grantDays, setGrantDays] = useState('30');
  const planState = planStatus(u.plan, u.planUntil);
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
          {planState.active && <span class="manage-chip"><Sparkles size={12} /> {planState.text}</span>}
          {u.ban && <span class="manage-chip"><span class="account-badge danger">Banned{u.ban.expires ? ` until ${day(u.ban.expires)}` : ''}</span></span>}
        </div>
        <div class="admin-tabs manage-tabs" role="tablist">
          <button type="button" role="tab" aria-selected={tab === 'plan'} class={`admin-tab${tab === 'plan' ? ' active' : ''}`} onClick={() => setTab('plan')}>Plan</button>
          <button type="button" role="tab" aria-selected={tab === 'access'} class={`admin-tab${tab === 'access' ? ' active' : ''}`} onClick={() => setTab('access')}>Access</button>
          <button type="button" role="tab" aria-selected={tab === 'danger'} class={`admin-tab danger-tab${tab === 'danger' ? ' active' : ''}`} onClick={() => setTab('danger')}>Danger</button>
        </div>
        <div class="account-modal-body">
          {(notice.ok || notice.err) && <p class={`account-status ${notice.err ? 'err' : 'ok'}`}>{notice.err || notice.ok}</p>}
          {tab === 'plan' && (
            <div class="manage-block">
              <div class="manage-ent">
                <div class="manage-ent-row">
                  <span class="manage-switch-label"><Sparkles size={13} /> Plan</span>
                  <b class={planState.active ? 'ent-on' : ''}>{planState.text}</b>
                  {u.plan && <button class="settings-action danger" type="button" disabled={busy} onClick={() => { if (confirm(`Remove ${u.username}'s plan?`)) act('/plan', { plan: null }, 'Plan removed.'); }}>Remove plan</button>}
                </div>
              </div>
              <p class="manage-hint">A grant gives the plan's windows without a subscription. Stripe-managed plans are better changed from the user's billing portal; a grant here overrides the plan column until it expires.</p>
              <form class="manage-grant" onSubmit={(e) => { e.preventDefault(); act('/plan', { plan: grantPlan, days: Number(grantDays) || 30 }, `${planName(grantPlan)} granted for ${Number(grantDays) || 30} days.`); }}>
                <select value={grantPlan} onChange={(e) => setGrantPlan((e.target as HTMLSelectElement).value)} disabled={busy} aria-label="Plan">
                  {PLANS.map((p) => <option key={p.id} value={p.id}>{p.name} ({formatShekels(p.weeklyUnits)} a week)</option>)}
                </select>
                <input placeholder="Days" type="number" min={1} value={grantDays} onInput={(e) => setGrantDays((e.target as HTMLInputElement).value)} style={{ width: 110 }} aria-label="Days" />
                <button class="settings-action primary" type="submit" disabled={busy}>Grant plan</button>
              </form>
            </div>
          )}
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

/** Last 12 months of revenue: stacked bars by tier with an MRR line (annual charges spread over twelve months). */
function RevenueChart({ monthly }: { monthly: RevenueBucket[] }) {
  const [hover, setHover] = useState<number | null>(null);
  const months: string[] = [];
  const now = new Date();
  for (let i = 11; i >= 0; i--) {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 1));
    months.push(d.toISOString().slice(0, 7));
  }
  const tiers = PLANS.map((p) => p.id);
  const byMonth = months.map((m) => {
    const cells = tiers.map((t) => monthly.filter((b) => b.period === m && b.tier.startsWith(`estro_${t}_`)).reduce((s, b) => s + b.cents, 0));
    const mrr = monthly.filter((b) => b.period === m).reduce((s, b) => s + (b.tier.endsWith('_year') ? b.cents / 12 : b.cents), 0);
    return { month: m, cells, total: cells.reduce((s, c) => s + c, 0), mrr };
  });
  const width = 600, height = 160, pad = 8;
  const max = Math.max(1, ...byMonth.map((m) => Math.max(m.total, m.mrr)));
  const slot = width / months.length;
  const barW = slot * 0.62;
  const y = (v: number) => height - pad - (v / max) * (height - pad * 2);
  const label = (m: string) => new Date(m + '-01T00:00:00Z').toLocaleDateString(undefined, { month: 'short', year: '2-digit', timeZone: 'UTC' });
  const line = byMonth.map((m, i) => `${(i * slot + slot / 2).toFixed(1)},${y(m.mrr).toFixed(1)}`).join(' ');
  const h = hover == null ? null : byMonth[hover];
  return (
    <div class="admin-chart admin-chart-wide">
      <div class="admin-chart-head"><strong>Monthly revenue by tier</strong><small>Line: monthly recurring, annual charges spread over 12 months</small></div>
      <div class="spark-wrap" onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const rect = (e.currentTarget as HTMLDivElement).getBoundingClientRect();
          if (!rect.width) return;
          setHover(Math.max(0, Math.min(months.length - 1, Math.floor(((e.clientX - rect.left) / rect.width) * months.length))));
        }}>
        <div class="spark-tip">{h ? `${label(h.month)}: ${money(h.total)} billed, ${money(Math.round(h.mrr))} recurring${h.total ? ' · ' + tiers.map((t, i) => h.cells[i] ? `${planName(t)} ${money(h.cells[i])}` : '').filter(Boolean).join(', ') : ''}` : 'Hover a month for the breakdown'}</div>
        <svg class="sparkline revenue-svg" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
          {byMonth.map((m, i) => {
            let acc = 0;
            return m.cells.map((c, t) => {
              if (!c) return null;
              const top = y(acc + c), bottom = y(acc);
              acc += c;
              return <rect key={`${m.month}-${t}`} class={`revenue-seg tier-${t}`} x={(i * slot + (slot - barW) / 2).toFixed(1)} y={top.toFixed(1)} width={barW.toFixed(1)} height={Math.max(1, bottom - top).toFixed(1)} />;
            });
          })}
          <polyline class="revenue-mrr" points={line} vectorEffect="non-scaling-stroke" />
          {h && <circle class="spark-dot" cx={hover! * slot + slot / 2} cy={y(h.mrr)} r={3} />}
        </svg>
      </div>
      <div class="admin-chart-axis revenue-axis">{months.map((m) => <span key={m}>{label(m)}</span>)}</div>
      <div class="revenue-legend">{PLANS.map((p, i) => <span key={p.id}><i class={`tier-${i}`} /> {p.name}</span>)}<span><i class="mrr" /> Recurring</span></div>
    </div>
  );
}

function RevenueTab() {
  const [revenue, setRevenue] = useState<Revenue | null>(null);
  const [series, setSeries] = useState<Series | null>(null);
  const [tx, setTx] = useState<Transaction[]>([]);
  const [page, setPage] = useState(1);
  useEffect(() => {
    api<Revenue>('/api/admin/revenue').then(setRevenue).catch(() => {});
    api<Series>('/api/admin/series?days=30').then(setSeries).catch(() => {});
  }, []);
  useEffect(() => { api<{ transactions: Transaction[] }>(`/api/admin/transactions?page=${page}`).then((d) => setTx(d.transactions)).catch(() => {}); }, [page]);
  if (!revenue) return <p class="account-empty">Loading</p>;
  const byTier = PLANS.map((p) => ({ plan: p, cents: revenue.monthly.filter((b) => b.tier.startsWith(`estro_${p.id}_`)).reduce((s, b) => s + b.cents, 0), count: revenue.monthly.filter((b) => b.tier.startsWith(`estro_${p.id}_`)).reduce((s, b) => s + b.count, 0) }));
  return (
    <div class="ads-dash">
      <div class="revenue-summary">
        <div><small>Total revenue</small><strong>{money(revenue.totalCents)}</strong></div>
        <div><small>Transactions</small><strong>{revenue.transactions.toLocaleString()}</strong></div>
        <div><small>Last 30 days</small><strong>{money(revenue.daily.reduce((s, b) => s + b.cents, 0))}</strong></div>
      </div>
      <div class="admin-charts">
        <RevenueChart monthly={revenue.monthly} />
        {series && <DailyChart title="Revenue per day, last 30 days" labels={series.days} values={series.revenueCents} format={(v) => money(v)} />}
      </div>
      <div class="admin-toolbar"><strong class="admin-toolbar-title">By tier, all time (real payments only, grants excluded)</strong></div>
      <div class="admin-list">
        <div class="admin-head tier" aria-hidden="true"><span>Plan</span><span class="num">Payments</span><span class="num">Revenue</span></div>
        {byTier.map((t) => (
          <div class="admin-row tier" key={t.plan.id}>
            <div class="admin-cell"><span class="cell-main">{t.plan.name}</span> <span class="cell-sub">{formatUsd(t.plan.monthlyCents)} a month</span></div>
            <div class="admin-cell admin-num">{t.count}</div>
            <div class="admin-cell admin-num"><span class="cell-main">{money(t.cents)}</span></div>
          </div>
        ))}
      </div>
      <div class="admin-toolbar"><strong class="admin-toolbar-title">Transactions</strong></div>
      <div class="admin-list">
        <div class="admin-head tx" aria-hidden="true"><span>User</span><span>Tier</span><span class="num">Amount</span><span>Source</span><span>Until</span><span>When</span></div>
        {tx.length === 0 && <p class="account-empty">No transactions yet.</p>}
        {tx.map((t) => (
          <div class="admin-row tx" key={t.id}>
            <div class="admin-cell"><span class="cell-main">{t.username}</span></div>
            <div class="admin-cell"><span class="cell-sub">{t.tier}</span></div>
            <div class="admin-cell admin-num">{t.source === 'grant' ? <span class="account-badge">grant</span> : money(t.amountCents)}</div>
            <div class="admin-cell"><span class={`account-badge${t.source === 'grant' ? ' warn' : ' on'}`}>{t.source}{t.actor ? ` · ${t.actor}` : ''}</span></div>
            <div class="admin-cell"><span class="cell-dim">{t.periodEnd ? day(t.periodEnd) : '-'}</span></div>
            <div class="admin-cell"><span class="cell-dim">{fmt(t.created)}</span></div>
          </div>
        ))}
      </div>
      <Pagination page={page} pageSize={50} onPage={setPage} hasMore={tx.length >= 50} />
    </div>
  );
}
