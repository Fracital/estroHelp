import { useEffect, useState } from 'preact/hooks';
import { Fingerprint, Pencil, Plus, Trash2 } from 'lucide-preact';
import { api } from '../lib/api';
import { passkeysSupported, registerPasskey } from '../lib/passkeys';

interface Passkey { id: number; name: string; created: string; lastUsed: string | null; backedUp: boolean; }
type NoticeState = { ok?: string; err?: string };

const errText = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

export default function SecurityPasskeys({ onChanged }: { onChanged: () => void }) {
  const [keys, setKeys] = useState<Passkey[] | null>(null);
  const [notice, setNotice] = useState<NoticeState>({});
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState('');
  const supported = passkeysSupported();

  const reload = () => api<{ passkeys: Passkey[] }>('/api/account/passkeys').then((d) => setKeys(d.passkeys)).catch((e) => { setKeys([]); setNotice({ err: errText(e, 'Could not load passkeys.') }); });
  useEffect(() => { reload(); }, []);

  const add = async (e: Event) => {
    e.preventDefault();
    setBusy(true); setNotice({});
    try {
      const d = await registerPasskey(name.trim() || 'Passkey');
      setNotice({ ok: `Passkey "${d.passkey.name}" added.` });
      setAdding(false); setName('');
      await reload(); onChanged();
    } catch (err) {
      const n = (err as Error)?.name;
      setNotice({ err: n === 'NotAllowedError' ? 'The passkey prompt was dismissed.' : n === 'InvalidStateError' ? 'This device already has a passkey for your account.' : errText(err, 'Could not add the passkey.') });
    }
    setBusy(false);
  };

  const rename = async (k: Passkey) => {
    const next = prompt('Rename passkey', k.name);
    if (!next || next.trim() === k.name) return;
    try { await api(`/api/account/passkeys/${k.id}`, { method: 'PATCH', body: { name: next.trim() } }); await reload(); }
    catch (err) { setNotice({ err: errText(err, 'Could not rename.') }); }
  };

  const remove = async (k: Passkey) => {
    if (!confirm(`Remove the passkey "${k.name}"? You will no longer be able to sign in with it.`)) return;
    try { await api(`/api/account/passkeys/${k.id}`, { method: 'DELETE' }); setNotice({ ok: 'Passkey removed.' }); await reload(); onChanged(); }
    catch (err) { setNotice({ err: errText(err, 'Could not remove.') }); }
  };

  return (
    <>
      <div class="settings-row">
        <span><strong>Passkeys</strong><small>Sign in with your fingerprint, face, device PIN or a security key. A passkey replaces the password and the two-factor code.</small></span>
        <div class="settings-row-actions">
          <button class="settings-action" type="button" disabled={!supported || busy} title={supported ? '' : 'This browser does not support passkeys'} onClick={() => { setAdding(!adding); setNotice({}); }}>
            <Plus size={13} />{adding ? ' Cancel' : ' Add a passkey'}
          </button>
        </div>
      </div>
      {adding && (
        <form class="account-form" onSubmit={add}>
          <label>Name this passkey (for example "Laptop" or "YubiKey")</label>
          <input value={name} onInput={(e) => setName((e.target as HTMLInputElement).value)} placeholder="Passkey" maxLength={60} autoFocus />
          <div class="account-form-row">
            <button class="settings-action primary" type="submit" disabled={busy}><Fingerprint size={13} /> {busy ? 'Waiting for your device…' : 'Create passkey'}</button>
          </div>
        </form>
      )}
      {keys && keys.length > 0 && (
        <ul class="account-list">
          {keys.map((k) => (
            <li key={k.id} class="account-list-row">
              <span class="account-list-icon"><Fingerprint size={15} /></span>
              <span class="account-list-main">
                <strong>{k.name}{k.backedUp && <span class="account-badge" style={{ marginLeft: 8 }}>Synced</span>}</strong>
                <small>Added {new Date(k.created).toLocaleDateString()}{k.lastUsed ? ` · last used ${new Date(k.lastUsed).toLocaleString()}` : ' · never used'}</small>
              </span>
              <span class="account-list-actions">
                <button class="settings-action" type="button" onClick={() => rename(k)} aria-label={`Rename ${k.name}`}><Pencil size={13} /></button>
                <button class="settings-action danger" type="button" onClick={() => remove(k)} aria-label={`Remove ${k.name}`}><Trash2 size={13} /></button>
              </span>
            </li>
          ))}
        </ul>
      )}
      {keys && keys.length === 0 && !adding && <p class="account-empty">No passkeys yet.</p>}
      {notice.err && <p class="account-status err" role="alert">{notice.err}</p>}
      {notice.ok && <p class="account-status ok" role="status">{notice.ok}</p>}
    </>
  );
}
