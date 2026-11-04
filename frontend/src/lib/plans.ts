// Estro plans, as the browser knows them. internal/billing/plans.go is the source of
// truth; the pricing page re-reads /api/billing/plans and replaces these numbers,
// while the static landing table and the chat use this copy.

export type PlanId = 'starter' | 'plus' | 'pro' | 'max' | 'ultra';
export type Interval = 'monthly' | 'annual';

export interface Plan {
  id: PlanId;
  name: string;
  audience: string;
  monthlyCents: number;
  fiveHourUnits: number;
  weeklyUnits: number;
  freeModelTokensPerDay: number;
  accountShare: number;
  recommended: boolean;
}

/** Annual billing is one charge a year at this many monthly prices. */
export const ANNUAL_MONTHS_CHARGED = 10;
/** The daily pool for accounts without a plan, in units. */
export const FREE_UNITS_PER_DAY = 50_000;
/** The only models an account without a plan may use. */
export const FREE_MODELS = ['kali-epsilon'];

export const PLANS: Plan[] = [
  { id: 'starter', name: 'Starter', audience: 'To try it properly', monthlyCents: 800, fiveHourUnits: 560_000, weeklyUnits: 920_000, freeModelTokensPerDay: 250_000, accountShare: 1 / 20, recommended: false },
  { id: 'plus', name: 'Plus', audience: 'For regular use', monthlyCents: 2400, fiveHourUnits: 1_360_000, weeklyUnits: 2_200_000, freeModelTokensPerDay: 500_000, accountShare: 1 / 8, recommended: true },
  { id: 'pro', name: 'Pro', audience: 'For daily work', monthlyCents: 4400, fiveHourUnits: 2_160_000, weeklyUnits: 3_680_000, freeModelTokensPerDay: 1_000_000, accountShare: 1 / 5, recommended: false },
  { id: 'max', name: 'Max', audience: 'For heavy use', monthlyCents: 10900, fiveHourUnits: 4_320_000, weeklyUnits: 9_200_000, freeModelTokensPerDay: 2_000_000, accountShare: 1 / 2, recommended: false },
  { id: 'ultra', name: 'Ultra', audience: 'For the heaviest use', monthlyCents: 21900, fiveHourUnits: 8_000_000, weeklyUnits: 18_400_000, freeModelTokensPerDay: 5_000_000, accountShare: 1, recommended: false },
];

export function planById(id: string | null | undefined): Plan | undefined {
  return PLANS.find((p) => p.id === id);
}

export function planName(id: string | null | undefined): string {
  return planById(id)?.name ?? (id ? id[0].toUpperCase() + id.slice(1) : 'Free');
}

/** chargeCents is what Stripe bills each time; perMonthCents is what the card shows. */
export function priceFor(plan: Plan, interval: Interval): { chargeCents: number; perMonthCents: number } {
  if (interval === 'monthly') return { chargeCents: plan.monthlyCents, perMonthCents: plan.monthlyCents };
  const chargeCents = plan.monthlyCents * ANNUAL_MONTHS_CHARGED;
  return { chargeCents, perMonthCents: Math.round(chargeCents / 12) };
}

/** The string the billing routes take: estro_<plan>_<month|year>. */
export function tier(plan: PlanId, interval: Interval): string {
  return `estro_${plan}_${interval === 'annual' ? 'year' : 'month'}`;
}

export type ChangeKind = 'same' | 'upgrade' | 'downgrade';

/** A higher plan is an upgrade and a lower one a downgrade, whatever the period. Same plan: monthly to annual is an upgrade. */
export function compareChange(from: { plan: PlanId; interval: Interval }, to: { plan: PlanId; interval: Interval }): ChangeKind {
  const a = PLANS.findIndex((p) => p.id === from.plan);
  const b = PLANS.findIndex((p) => p.id === to.plan);
  if (a !== b) return b > a ? 'upgrade' : 'downgrade';
  if (from.interval === to.interval) return 'same';
  return to.interval === 'annual' ? 'upgrade' : 'downgrade';
}

export function formatUsd(cents: number): string {
  const dollars = cents / 100;
  return Number.isInteger(dollars) ? `$${dollars.toLocaleString('en-US')}` : `$${dollars.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

/** Whole units with thousands separators, prefixed with the shekel sign. */
export function formatShekels(units: number): string {
  return `₪${Math.floor(Math.max(0, units)).toLocaleString('en-US')}`;
}
