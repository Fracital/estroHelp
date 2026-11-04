import { useEffect, useState } from 'preact/hooks';
import { Check, CheckCircle2 } from 'lucide-preact';
import { authReady, loadMe, user } from '../lib/auth';
import { INTEGRATIONS, anchorFor, iconUrl, BASE_HOST, BASE_URL } from '../lib/integrations';
import { PLANS, compareChange, formatShekels, formatUsd, priceFor, type Interval, type Plan } from '../lib/plans';
import { changePlan, fetchPlanMe, fetchPlans, formatPlanDate, openPortal, planStatusLine, previewChange, startCheckout, undoChange, type ChangePreview, type PlanMe } from '../lib/billing';
import { LegalFooter } from './AuthGate';
import '../styles/pricing.css';

// Estro pricing. Plans, prices and window caps come from internal/billing/plans.go
// through /api/billing/plans; lib/plans.ts holds the same table for first paint.
// Checkout, plan changes and the billing portal live on this page only.

const API_REFERENCE = `openai base url:     ${BASE_URL}
anthropic base url:  ${BASE_HOST}
api key:             estro_YOUR_KEY_HERE
model:               kali-gamma`;

const CURL_EXAMPLE = `curl ${BASE_URL}/chat/completions \\
  -H "Authorization: Bearer $ESTRO_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"kali-gamma","messages":[{"role":"user","content":"hi"}]}'`;

const LEAD = 'Every paid plan has the same features. Higher tiers give you more usage in every window.';

const COMPARE_ROWS = [
  { label: 'Kali Epsilon chat', free: true, paid: true },
  { label: 'Kali Gamma and Kali Beta', free: false, paid: true },
  { label: 'Chat history and reasoning streams', free: true, paid: true },
  { label: 'Dusk workspace and the in-chat shell', free: true, paid: true },
  { label: 'OpenAI and Anthropic compatible API', free: false, paid: true },
  { label: 'API keys', free: false, paid: true },
  { label: 'IDE integrations', free: false, paid: true },
  { label: 'Usage meters and history', free: false, paid: true },
];

// Everything a paid plan includes. The first plan lists these, and every plan above it inherits them ("Everything in … and:").
const STARTER_FEATURES = [
  'Kali Gamma, Beta and Epsilon',
  'OpenAI and Anthropic compatible API',
  'IDE integrations',
  'Usage meters',
  'Chat history and API keys',
];

const USAGE_ROWS = [
  { tier: 'Kali Gamma', examples: 'Deep reasoning for complex work', counts: '1x' },
  { tier: 'Kali Beta', examples: 'Balanced, long context', counts: '1x' },
  { tier: 'Kali Epsilon', examples: 'Fast answers', counts: '1x' },
];

/** How much more weekly usage a plan gives than the base plan: "2.4", "4". */
function usageMultiple(plan: Plan, base: Plan): string {
  const multiple = Math.round((plan.weeklyUnits / base.weeklyUnits) * 10) / 10;
  return Number.isInteger(multiple) ? String(multiple) : multiple.toFixed(1);
}

type Target = { plan: Plan; interval: Interval };
type Cta = { label: string; disabled: boolean; change: boolean };

/**
 * The button for one plan at one billing period, once the subscriber has a plan. Null when they have
 * none, and the cards keep their own "Choose" labels.
 */
function ctaFor(target: Target, plan: PlanMe['plan'], salesOpen: boolean): Cta | null {
  if (!plan) return null;
  const { id, name } = target.plan;
  if (plan.pending && plan.pending.plan === id && plan.pending.interval === target.interval) {
    return { label: 'Scheduled', disabled: true, change: false };
  }
  // No Stripe subscription: an admin grant or a hand-set plan. The plan itself is current at either period.
  if (!plan.managed || plan.interval === null) {
    if (plan.id === id) return { label: 'Current plan', disabled: true, change: false };
    return { label: `Change to ${name}`, disabled: false, change: true };
  }
  if (plan.id === id && plan.interval === target.interval) return { label: 'Current plan', disabled: true, change: false };
  const kind = compareChange({ plan: plan.id, interval: plan.interval }, { plan: id, interval: target.interval });
  if (kind === 'upgrade' && !salesOpen) return { label: 'Plans open soon', disabled: true, change: false };
  if (plan.id === id) {
    return { label: target.interval === 'annual' ? 'Switch to annual' : 'Switch to monthly', disabled: false, change: true };
  }
  return { label: kind === 'upgrade' ? `Upgrade to ${name}` : `Downgrade to ${name}`, disabled: false, change: true };
}

function PlanCard({ plan, previous, base, billing, salesOpen, cta, busy, onPick }: {
  plan: Plan; previous: Plan | null; base: Plan; billing: Interval; salesOpen: boolean; cta: Cta | null; busy: boolean; onPick: () => void;
}) {
  const { chargeCents, perMonthCents } = priceFor(plan, billing);
  const label = cta ? cta.label : !salesOpen ? 'Plans open soon' : busy ? 'Opening…' : `Choose ${plan.name}`;
  const disabled = cta ? cta.disabled || busy : !salesOpen || busy;
  return (
    <article class={`pricing-card${plan.recommended ? ' is-recommended' : ''}`} data-plan={plan.id}>
      {plan.recommended && <span class="pricing-recommended">Recommended</span>}
      <h3 class="pricing-name">{plan.name}</h3>
      <p class="pricing-audience">{plan.audience}</p>
      <p class="pricing-price"><strong>{formatUsd(perMonthCents)}</strong><span>per month</span></p>
      <p class="pricing-billed">{billing === 'annual' && `Billed annually, ${formatUsd(chargeCents)} a year`}</p>
      <div class="pricing-features">
        <p class="pricing-features-head">{previous ? `Everything in ${previous.name} and:` : "What's included:"}</p>
        <ul>
          {!previous && STARTER_FEATURES.map((item) => <li key={item}><Check size={14} strokeWidth={2.2} aria-hidden="true" />{item}</li>)}
          <li><Check size={14} strokeWidth={2.2} aria-hidden="true" />{plan.id === base.id ? 'Base usage' : `${usageMultiple(plan, base)}x the usage of ${base.name}`}</li>
        </ul>
      </div>
      <button type="button" class={`pricing-cta${plan.recommended ? ' primary' : ''}`} disabled={disabled} onClick={onPick}>{label}</button>
    </article>
  );
}

/** Confirms a plan change. Shows Stripe's preview first, then applies the change or schedules it. */
function ChangePlanDialog({ target, onClose, onDone, onBilling }: { target: Target; onClose: () => void; onDone: () => void; onBilling: () => void }) {
  const [preview, setPreview] = useState<ChangePreview | null>(null);
  const [previewError, setPreviewError] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [paymentFailed, setPaymentFailed] = useState(false);

  useEffect(() => {
    let live = true;
    previewChange(target.plan.id, target.interval).then((result) => {
      if (!live) return;
      if (result.ok) setPreview(result);
      else setPreviewError(result.error);
    });
    return () => { live = false; };
  }, [target]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape' && !busy) onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [busy, onClose]);

  async function confirm() {
    setBusy(true); setError(''); setPaymentFailed(false);
    const result = await changePlan(target.plan.id, target.interval);
    if (!result.ok) { setBusy(false); setError(result.error); setPaymentFailed(result.paymentFailed); return; }
    onDone();
  }

  const period = target.interval === 'annual' ? 'year' : 'month';
  return (
    <div class="pricing-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget && !busy) onClose(); }}>
      <div class="pricing-dialog" role="dialog" aria-modal="true" aria-labelledby="pricing-dialog-title">
        <h3 id="pricing-dialog-title">Change to {target.plan.name}, billed {target.interval === 'annual' ? 'annually' : 'monthly'}</h3>
        {!preview && !previewError && <p>Working out the change…</p>}
        {previewError && <p class="pricing-error" role="alert">{previewError}</p>}
        {preview?.kind === 'upgrade' && (
          <>
            <p>You are charged {formatUsd(preview.dueTodayCents)} today.</p>
            <p>Then {formatUsd(preview.nextChargeCents)} per {period}.</p>
          </>
        )}
        {preview?.kind === 'downgrade' && (
          <>
            <p>Your current plan stays active until {formatPlanDate(preview.effectiveAt)}.</p>
            <p>Then {formatUsd(preview.nextChargeCents)} per {period}.</p>
          </>
        )}
        {error && <p class="pricing-error" role="alert">{error}</p>}
        <div class="pricing-dialog-actions">
          <button type="button" class="pricing-cta-secondary" onClick={onClose} disabled={busy}>Keep current plan</button>
          {paymentFailed
            ? <button type="button" class="pricing-cta-go" onClick={onBilling} disabled={busy}>Update payment method</button>
            : preview && <button type="button" class="pricing-cta-go" onClick={() => void confirm()} disabled={busy}>{busy ? 'Working…' : preview.kind === 'upgrade' ? 'Confirm upgrade' : 'Schedule change'}</button>}
        </div>
      </div>
    </div>
  );
}

export default function PricingPage() {
  const [plans, setPlans] = useState<Plan[]>(PLANS);
  const [freeUnits, setFreeUnits] = useState(50_000);
  const [serverSalesOpen, setServerSalesOpen] = useState(true);
  const [planMe, setPlanMe] = useState<PlanMe | null>(null);
  const [billing, setBilling] = useState<Interval>('monthly');
  const [busy, setBusy] = useState(false);
  const [undoBusy, setUndoBusy] = useState(false);
  const [error, setError] = useState('');
  const [dialog, setDialog] = useState<Target | null>(null);
  const [finishing, setFinishing] = useState(false);
  const u = user.value;
  // Sandbox accounts can buy while sales are closed for everyone else.
  const salesOpen = planMe ? planMe.salesOpen : serverSalesOpen || Boolean(u?.sandbox);
  const plan = planMe?.plan ?? null;
  const base = plans[0];

  useEffect(() => {
    loadMe().then(() => { if (user.value) fetchPlanMe().then(setPlanMe); });
    fetchPlans().then((p) => { setPlans(p.plans); setFreeUnits(p.freeUnitsPerDay); setServerSalesOpen(p.salesOpen); });
    // Checkout returns here, and the plan arrives with the webhook. Wait for it below.
    const q = new URLSearchParams(location.search);
    if (q.get('checkout') === 'complete') {
      history.replaceState(null, '', location.pathname);
      setFinishing(true);
    }
  }, []);

  useEffect(() => {
    if (!finishing) return;
    const started = Date.now();
    const timer = window.setInterval(() => {
      void fetchPlanMe().then((next) => {
        if (next) setPlanMe(next);
        if (next?.plan || Date.now() - started > 30_000) { window.clearInterval(timer); setFinishing(false); }
      });
    }, 2000);
    return () => window.clearInterval(timer);
  }, [finishing]);

  const signIn = () => location.assign('/login?next=%2Fpricing');

  async function choose(target: Target) {
    if (!salesOpen) return;
    if (!u) { signIn(); return; }
    if (!u.emailVerified) { location.assign('/verify?next=%2Fpricing'); return; }
    setBusy(true); setError('');
    const result = await startCheckout(target.plan.id, target.interval);
    if (!result.ok) {
      setBusy(false);
      // A 409 means the subscriber already has a plan. Reloading shows it, so the message is not needed.
      if (result.status === 409) void fetchPlanMe().then(setPlanMe);
      else setError(result.error);
      return;
    }
    location.assign(result.url);
  }

  function pick(target: Target, cta: Cta | null) {
    if (cta) { if (cta.change) setDialog(target); return; }
    void choose(target);
  }

  async function manageBilling() {
    if (!u) { signIn(); return; }
    setBusy(true); setError('');
    const result = await openPortal();
    if (!result.ok) { setError(result.error); setBusy(false); return; }
    location.assign(result.url);
  }

  async function undo() {
    setUndoBusy(true); setError('');
    const result = await undoChange();
    setUndoBusy(false);
    if (!result.ok) setError(result.error);
    void fetchPlanMe().then(setPlanMe);
  }

  return (
    <section class="pricing-page">
      {dialog && (
        <ChangePlanDialog target={dialog} onClose={() => setDialog(null)}
          onDone={() => { setDialog(null); void fetchPlanMe().then(setPlanMe); }}
          onBilling={() => { setDialog(null); void manageBilling(); }} />
      )}
      <header class="pricing-hero">
        <h1>Plans and usage</h1>
        <p class="pricing-lead">{LEAD}</p>
        {!salesOpen && <p class="pricing-open-soon">Plans open soon.</p>}
      </header>

      <div class="pricing-toggle-wrap">
        <button type="button" role="switch" class="pricing-slider" aria-checked={billing === 'annual'}
          onClick={() => setBilling((current) => (current === 'annual' ? 'monthly' : 'annual'))}>
          <span class="pricing-slider-thumb" aria-hidden="true" />
          <span class={`pricing-slider-option${billing === 'monthly' ? ' on' : ''}`}>Monthly</span>
          <span class={`pricing-slider-option${billing === 'annual' ? ' on' : ''}`}>Annual</span>
        </button>
        <p class="pricing-sr" aria-live="polite">{billing === 'annual' ? 'Showing annual billing, billed annually.' : 'Showing monthly billing.'}</p>
      </div>

      {plan && (
        <p class="pricing-notice" role="status">
          {planStatusLine(plan)}
          {plan.pending && <button type="button" class="pricing-cta-secondary pricing-notice-action" onClick={() => void undo()} disabled={undoBusy}>{undoBusy ? 'Undoing…' : 'Undo'}</button>}
        </p>
      )}
      {!plan && finishing && <p class="pricing-notice" role="status">Finishing your purchase…</p>}
      {error && <p class="pricing-error" role="alert">{error}</p>}

      <div class="pricing-grid">
        {plans.map((planDef, index) => {
          const target: Target = { plan: planDef, interval: billing };
          const cta = ctaFor(target, plan, salesOpen);
          return <PlanCard key={planDef.id} plan={planDef} previous={index > 0 ? plans[index - 1] : null} base={base} billing={billing}
            salesOpen={salesOpen} cta={cta} busy={busy} onPick={() => pick(target, cta)} />;
        })}
      </div>

      <section class="pricing-free">
        <h2>Free</h2>
        <p>Kali Epsilon only, {formatShekels(freeUnits)} a day. Chat history and the Dusk workspace are included. Kali Gamma, Kali Beta and the API need a plan.</p>
      </section>

      <section class="pricing-platforms">
        <p class="pricing-platforms-head">Estro is available on your favorite platforms:</p>
        <ul class="pricing-platforms-grid">
          {INTEGRATIONS.map((tool) => (
            <li key={tool.id}>
              <a class="pricing-platform-tile" href={`/docs#${anchorFor(tool.id)}`} aria-label={tool.label}>
                <img class="pricing-platform-icon" src={iconUrl(tool)} alt="" width="36" height="36" loading="lazy" />
              </a>
              <span class="pricing-platform-name">{tool.label}</span>
            </li>
          ))}
        </ul>
        <div class="pricing-quickstart">
          <h3>Quick start</h3>
          <p class="pricing-quickstart-desc">Point any OpenAI or Anthropic compatible tool at the base URL, enter your API key, and pick a model.</p>
          <pre class="pricing-code"><code>{API_REFERENCE}</code></pre>
          <pre class="pricing-code"><code>{CURL_EXAMPLE}</code></pre>
        </div>
      </section>

      <div class="pricing-account">
        <button type="button" class="pricing-cta-secondary" disabled={busy} onClick={() => void manageBilling()}>Billing</button>
        <a class="pricing-cta-secondary" href="/docs">API docs</a>
        <a class="pricing-cta-secondary" href={u ? '/account#acc-plan' : '/login?next=%2Faccount%23acc-plan'}>View usage</a>
      </div>

      <section class="pricing-compare">
        <div class="pricing-table-wrap pricing-compare-scroll">
          <table class="pricing-compare-table">
            <thead>
              <tr>
                <th scope="col" class="pricing-compare-heading">Compare features</th>
                <th scope="col" class="pricing-compare-plan">
                  <span class="pricing-compare-plan-name">Free</span>
                  <span class="pricing-compare-price">$0</span>
                  <span class="pricing-compare-period">Forever free</span>
                  <span class="pricing-compare-billing">No subscription</span>
                  <a class="pricing-compare-cta" href={u ? '/chat' : '/register'}>{u ? 'Go to chat' : 'Get started'}</a>
                </th>
                {plans.map((planDef) => {
                  const target: Target = { plan: planDef, interval: billing };
                  const { chargeCents, perMonthCents } = priceFor(planDef, billing);
                  const cta = ctaFor(target, plan, salesOpen);
                  const label = cta ? cta.label : busy ? 'Opening…' : 'Get started';
                  const disabled = cta ? cta.disabled || busy : busy || !salesOpen;
                  return (
                    <th scope="col" key={planDef.id} class={`pricing-compare-plan${planDef.recommended ? ' is-recommended' : ''}`}>
                      <span class="pricing-compare-plan-name">{planDef.name}</span>
                      <span class="pricing-compare-price">{formatUsd(perMonthCents)}</span>
                      <span class="pricing-compare-period">per month</span>
                      <span class="pricing-compare-billing">{billing === 'annual' ? `Billed ${formatUsd(chargeCents)} annually` : 'Billed monthly'}</span>
                      <button type="button" class={`pricing-compare-cta${planDef.recommended ? ' is-primary' : ''}`} disabled={disabled} onClick={() => pick(target, cta)}>{label}</button>
                    </th>
                  );
                })}
              </tr>
            </thead>
            <tbody>
              <tr class="pricing-compare-section"><th scope="row" colSpan={plans.length + 2}>Included features</th></tr>
              {COMPARE_ROWS.map((row) => (
                <tr key={row.label}>
                  <th scope="row">{row.label}</th>
                  <td class="pricing-compare-check">{row.free
                    ? <><CheckCircle2 size={16} aria-hidden="true" /><span class="pricing-sr">Included</span></>
                    : <><span class="pricing-compare-dash" aria-hidden="true">-</span><span class="pricing-sr">Not included</span></>}</td>
                  {plans.map((planDef) => <td key={planDef.id} class="pricing-compare-check"><CheckCircle2 size={16} aria-hidden="true" /><span class="pricing-sr">Included</span></td>)}
                </tr>
              ))}
              <tr class="pricing-compare-section"><th scope="row" colSpan={plans.length + 2}>Usage limits</th></tr>
              <tr>
                <th scope="row">Models</th>
                <td>Kali Epsilon</td>
                {plans.map((planDef) => <td key={planDef.id}>All three</td>)}
              </tr>
              <tr>
                <th scope="row">Daily pool</th>
                <td>{formatShekels(freeUnits)}</td>
                {plans.map((planDef) => <td key={planDef.id} class="pricing-compare-dash"><span aria-hidden="true">-</span><span class="pricing-sr">Not used</span></td>)}
              </tr>
              <tr>
                <th scope="row">5-hour usage window</th>
                <td class="pricing-compare-dash"><span aria-hidden="true">-</span><span class="pricing-sr">Not included</span></td>
                {plans.map((planDef) => <td key={planDef.id}>{formatShekels(planDef.fiveHourUnits)}</td>)}
              </tr>
              <tr>
                <th scope="row">Weekly usage window</th>
                <td class="pricing-compare-dash"><span aria-hidden="true">-</span><span class="pricing-sr">Not included</span></td>
                {plans.map((planDef) => <td key={planDef.id}>{formatShekels(planDef.weeklyUnits)}</td>)}
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="pricing-usage">
        <h2>How usage works</h2>
        <p>Each reply counts for its tokens, weighted by kind. Cached input counts at a tenth and output at five times, so a long coding session that re-sends its history stays cheap. Two limits apply at once: one every 5 hours and one every week. When a limit fills, requests pause until it resets.</p>
        <p>Usage is measured in shekels (₪). One shekel is one fresh input token. Every Kali model counts the same today.</p>
        <div class="pricing-table-wrap">
          <table class="pricing-table">
            <caption>Multipliers apply per model. Versions within a model cost the same.</caption>
            <thead><tr><th scope="col">Model</th><th scope="col">Best for</th><th scope="col">Counts as</th></tr></thead>
            <tbody>
              {USAGE_ROWS.map((row) => <tr key={row.tier}><th scope="row">{row.tier}</th><td>{row.examples}</td><td>{row.counts}</td></tr>)}
            </tbody>
          </table>
        </div>
      </section>

      <p class="pricing-foot">Cancel from the billing page. Your plan stays active until the end of the period you paid for.</p>
      {authReady.value && <LegalFooter />}
    </section>
  );
}
