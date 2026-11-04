import { useState } from 'preact/hooks';
import { Check, Copy, Download, ShieldCheck, ShieldOff, Smartphone } from 'lucide-preact';
import { api } from '../lib/api';

type NoticeState = { ok?: string; err?: string };
const errText = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

function BackupCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  const text = codes.join('\n');
  const copy = () => navigator.clipboard.writeText(text).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); }).catch(() => {});
  const download = () => {
    const url = URL.createObjectURL(new Blob([`Estro backup codes\n\n${text}\n\nEach code works once. Keep them somewhere safe.\n`], { type: 'text/plain' }));
    const a = document.createElement('a'); a.href = url; a.download = 'estro-backup-codes.txt'; a.click(); URL.revokeObjectURL(url);
  };
  return (
    <div class="account-form account-backup">
      <p><strong>Save these backup codes now.</strong> They are shown once. Each one signs you in a single time if you lose your authenticator.</p>
      <ul class="account-codes">{codes.map((c) => <li key={c}><code>{c}</code></li>)}</ul>
      <div class="account-form-row">
        <button class="settings-action" type="button" onClick={copy}>{copied ? <Check size={13} /> : <Copy size={13} />} {copied ? 'Copied' : 'Copy'}</button>
        <button class="settings-action" type="button" onClick={download}><Download size={13} /> Download</button>
        <button class="settings-action primary" type="button" onClick={onDone}>I saved them</button>
      </div>
    </div>
  );
}

export default function SecurityTotp({ enabled, onChanged }: { enabled: boolean; onChanged: () => void }) {
  const [step, setStep] = useState<'idle' | 'enrol' | 'codes' | 'disable' | 'regen'>('idle');
  const [enrol, setEnrol] = useState<{ secret: string; url: string; qr: string } | null>(null);
  const [code, setCode] = useState('');
  const [codes, setCodes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<NoticeState>({});

  const reset = () => { setStep('idle'); setCode(''); setEnrol(null); };

  const begin = async () => {
    setBusy(true); setNotice({});
    try { setEnrol(await api('/api/account/totp/begin', { body: {} })); setCode(''); setStep('enrol'); }
    catch (err) { setNotice({ err: errText(err, 'Could not start enrolment.') }); }
    setBusy(false);
  };

  const enable = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setNotice({});
    try {
      const d = await api<{ backupCodes: string[] }>('/api/account/totp/enable', { body: { code } });
      setCodes(d.backupCodes); setStep('codes'); onChanged();
    } catch (err) { setNotice({ err: errText(err, 'That code did not match.') }); }
    setBusy(false);
  };

  const disable = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setNotice({});
    try { await api('/api/account/totp/disable', { body: { code } }); setNotice({ ok: 'Two-factor authentication is off.' }); reset(); onChanged(); }
    catch (err) { setNotice({ err: errText(err, 'That code did not match.') }); }
    setBusy(false);
  };

  const regen = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setNotice({});
    try { const d = await api<{ backupCodes: string[] }>('/api/account/totp/backup-codes', { body: { code } }); setCodes(d.backupCodes); setStep('codes'); }
    catch (err) { setNotice({ err: errText(err, 'That code did not match.') }); }
    setBusy(false);
  };

  return (
    <>
      <div class="settings-row">
        <span>
          <strong>Two-factor authentication {enabled ? <span class="account-badge on" style={{ marginLeft: 8 }}>On</span> : <span class="account-badge" style={{ marginLeft: 8 }}>Off</span>}</strong>
          <small>{enabled ? 'A code from your authenticator app is required after your password.' : 'Add a second step with an authenticator app such as Aegis, 1Password or Google Authenticator.'}</small>
        </span>
        <div class="settings-row-actions">
          {!enabled && step === 'idle' && <button class="settings-action primary" type="button" disabled={busy} onClick={begin}><ShieldCheck size={13} /> Enable</button>}
          {enabled && step === 'idle' && <button class="settings-action" type="button" onClick={() => { setStep('regen'); setCode(''); setNotice({}); }}>New backup codes</button>}
          {enabled && step === 'idle' && <button class="settings-action danger" type="button" onClick={() => { setStep('disable'); setCode(''); setNotice({}); }}><ShieldOff size={13} /> Disable</button>}
          {step !== 'idle' && step !== 'codes' && <button class="settings-action" type="button" onClick={reset}>Cancel</button>}
        </div>
      </div>

      {step === 'enrol' && enrol && (
        <form class="account-form account-totp" onSubmit={enable}>
          <div class="account-totp-grid">
            <img class="account-qr" src={enrol.qr} alt="QR code for your authenticator app" width={180} height={180} />
            <div>
              <p>1. Scan the code with your authenticator app, or enter the key by hand:</p>
              <code class="account-secret">{enrol.secret.replace(/(.{4})/g, '$1 ').trim()}</code>
              <p>2. Enter the 6-digit code the app shows to confirm.</p>
              <input value={code} onInput={(e) => setCode((e.target as HTMLInputElement).value.replace(/\D/g, ''))} inputMode="numeric" maxLength={6} placeholder="6-digit code" autoComplete="one-time-code" autoFocus />
              <div class="account-form-row">
                <button class="settings-action primary" type="submit" disabled={busy || code.length !== 6}><Smartphone size={13} /> Turn on</button>
              </div>
            </div>
          </div>
        </form>
      )}

      {step === 'codes' && <BackupCodes codes={codes} onDone={() => { reset(); setNotice({ ok: 'Two-factor authentication is on. Your other sessions were signed out.' }); }} />}

      {(step === 'disable' || step === 'regen') && (
        <form class="account-form" onSubmit={step === 'disable' ? disable : regen}>
          <label>{step === 'disable' ? 'Enter a current code or a backup code to turn two-factor off' : 'Enter a current code to replace your backup codes'}</label>
          <input value={code} onInput={(e) => setCode((e.target as HTMLInputElement).value)} placeholder={step === 'disable' ? 'Code or backup code' : '6-digit code'} autoComplete="one-time-code" autoFocus />
          <div class="account-form-row">
            <button class={`settings-action ${step === 'disable' ? 'danger' : 'primary'}`} type="submit" disabled={busy || code.trim().length < 6}>{step === 'disable' ? 'Turn off' : 'Generate new codes'}</button>
          </div>
        </form>
      )}

      {notice.err && <p class="account-status err" role="alert">{notice.err}</p>}
      {notice.ok && <p class="account-status ok" role="status">{notice.ok}</p>}
    </>
  );
}
