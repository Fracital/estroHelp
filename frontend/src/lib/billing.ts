// Plan billing for the browser. The routes are in internal/billing/handlers.go.
import { api, ApiError } from './api';
import { FREE_UNITS_PER_DAY, PLANS, tier, type Interval, type Plan, type PlanId } from './plans';

export type PendingChange = { plan: PlanId; name: string; interval: Interval; at: string };

export interface Meter { used: number; cap: number; resetsAt: string | null }

/** GET /api/account/plan/me, as the browser uses it. */
export interface PlanMe {
  plan: {
    id: PlanId;
    name: string;
    until: string;
    /** A Stripe subscription stands behind the plan. False for a grant. */
    managed: boolean;
    interval: Interval | null;
    cancelAt: string | null;
    pending: PendingChange | null;
  } | null;
  usage: { fiveHour: Meter; weekly: Meter } | null;
  free: Meter | null;
  salesOpen: boolean;
  admin: boolean;
}

export interface PlansPayload {
  plans: (Plan & { prices: { monthly: { chargeCents: number; perMonthCents: number }; annual: { chargeCents: number; perMonthCents: number } } })[];
  annualMonthsCharged: number;
  freeUnitsPerDay: number;
  freeModels: string[];
  salesOpen: boolean;
}

/** What a plan change costs or when it takes effect, from GET /api/stripe/change/preview. */
export type ChangePreview =
  | { kind: 'upgrade'; plan: PlanId; name: string; interval: Interval; nextChargeCents: number; dueTodayCents: number }
  | { kind: 'downgrade'; plan: PlanId; name: string; interval: Interval; nextChargeCents: number; effectiveAt: string };

type Failure = { ok: false; status: number; error: string; paymentFailed: boolean; code?: string };

/** A billing call's result. Failures carry the server's message; paymentFailed marks a declined card. */
export type Outcome<T extends object> = ({ ok: true } & T) | Failure;

const BILLING_FALLBACK = 'Could not reach billing. Try again.';

async function attempt<T extends object>(run: () => Promise<T>): Promise<Outcome<T>> {
  try {
    return { ok: true, ...(await run()) };
  } catch (e) {
    if (e instanceof ApiError) {
      return { ok: false, status: e.status, error: e.message || BILLING_FALLBACK, paymentFailed: e.data?.paymentFailed === true, code: e.data?.code };
    }
    return { ok: false, status: 0, error: BILLING_FALLBACK, paymentFailed: false };
  }
}

/** The signed-in user's plan, or null when signed out or the request fails. */
export async function fetchPlanMe(): Promise<PlanMe | null> {
  try {
    const d = await api<Partial<PlanMe>>('/api/account/plan/me');
    return { plan: d.plan ?? null, usage: d.usage ?? null, free: d.free ?? null, salesOpen: d.salesOpen === true, admin: d.admin === true };
  } catch {
    return null;
  }
}

/** The plan table as the server has it. Falls back to the built-in copy. */
export async function fetchPlans(): Promise<PlansPayload> {
  try {
    return await api<PlansPayload>('/api/billing/plans');
  } catch {
    return {
      plans: PLANS.map((p) => ({ ...p, prices: { monthly: { chargeCents: p.monthlyCents, perMonthCents: p.monthlyCents }, annual: { chargeCents: p.monthlyCents * 10, perMonthCents: Math.round((p.monthlyCents * 10) / 12) } } })),
      annualMonthsCharged: 10, freeUnitsPerDay: FREE_UNITS_PER_DAY, freeModels: ['kali-epsilon'], salesOpen: true,
    };
  }
}

/** What moving to this plan and period costs, or when it takes effect. Nothing is changed. */
export function previewChange(plan: PlanId, interval: Interval): Promise<Outcome<ChangePreview>> {
  return attempt(() => api<ChangePreview>(`/api/stripe/change/preview?tier=${tier(plan, interval)}`));
}

/** Applies an upgrade now, or schedules a downgrade for period end. */
export function changePlan(plan: PlanId, interval: Interval): Promise<Outcome<{ done: boolean }>> {
  return attempt(async () => { await api(`/api/stripe/change?tier=${tier(plan, interval)}`, { method: 'POST', body: {} }); return { done: true }; });
}

/** Drops a scheduled downgrade. The current plan continues. */
export function undoChange(): Promise<Outcome<{ done: boolean }>> {
  return attempt(async () => { await api('/api/stripe/change/undo', { method: 'POST', body: {} }); return { done: true }; });
}

/** Stripe Checkout for a plan the subscriber does not have yet. */
export function startCheckout(plan: PlanId, interval: Interval): Promise<Outcome<{ url: string }>> {
  return attempt(async () => ({ url: String((await api<{ url?: string }>(`/api/stripe/checkout?tier=${tier(plan, interval)}`, { method: 'POST', body: {} })).url ?? '') }));
}

/** The Stripe billing portal: payment method, invoices, and cancelling. */
export function openPortal(): Promise<Outcome<{ url: string }>> {
  return attempt(async () => ({ url: String((await api<{ url?: string }>('/api/stripe/portal?tier=estro_account', { method: 'POST', body: {} })).url ?? '') }));
}

export function formatPlanDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

/** A plan_until decades away means "no end": a hand-set or permanent grant. */
export function isOpenEnded(iso: string): boolean {
  return new Date(iso).getTime() - Date.now() > 20 * 365.25 * 24 * 3600_000;
}

/** One line for the subscriber's plan: what they are on, and what happens next. */
export function planStatusLine(plan: PlanMe['plan']): string {
  if (!plan) return 'Free tier: Kali Epsilon only.';
  const period = plan.interval ? ` (${plan.interval === 'annual' ? 'annual' : 'monthly'})` : '';
  if (plan.pending) {
    const next = plan.pending;
    return `You are on ${plan.name}${period}. On ${formatPlanDate(next.at)} you move to ${next.name} (${next.interval === 'annual' ? 'annual' : 'monthly'}).`;
  }
  if (plan.cancelAt) return `${plan.name}${period} cancels on ${formatPlanDate(plan.cancelAt)}.`;
  if (!plan.managed) {
    if (isOpenEnded(plan.until)) return `${plan.name}, no renewal date.`;
    return `${plan.name} until ${formatPlanDate(plan.until)}.`;
  }
  return `You are on ${plan.name}${period}. Renews ${formatPlanDate(plan.until)}.`;
}

/** "2h 14m" or "38m" until a window resets; "now" once it has. */
export function formatResetsIn(resetsAt: string | null | undefined, now = Date.now()): string {
  if (!resetsAt) return 'on first use';
  const ms = new Date(resetsAt).getTime() - now;
  if (ms <= 0) return 'now';
  const minutes = Math.ceil(ms / 60_000);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}
