import { browserSupportsWebAuthn, browserSupportsWebAuthnAutofill, startAuthentication, startRegistration } from '@simplewebauthn/browser';
import { api } from './api';

export const passkeysSupported = () => typeof window !== 'undefined' && browserSupportsWebAuthn();

/** Registers a new passkey for the signed-in account and returns its row. */
export async function registerPasskey(name: string) {
  const begin = await api<{ challengeId: string; options: any }>('/api/account/passkeys/register/begin', { body: {} });
  const credential = await startRegistration({ optionsJSON: begin.options });
  return api<{ passkey: { id: number; name: string } }>('/api/account/passkeys/register/finish', {
    body: { challengeId: begin.challengeId, name, credential },
  });
}

/**
 * Signs in with a discoverable passkey. With `autofill` the request waits in
 * the background for the browser's username-field suggestion (conditional UI).
 */
export async function signInWithPasskey(autofill = false) {
  const begin = await api<{ challengeId: string; options: any }>('/api/auth/passkey/begin', { body: {} });
  const credential = await startAuthentication({ optionsJSON: begin.options, useBrowserAutofill: autofill });
  return api('/api/auth/passkey/finish', { body: { challengeId: begin.challengeId, credential } });
}

export async function passkeyAutofillAvailable(): Promise<boolean> {
  try { return passkeysSupported() && (await browserSupportsWebAuthnAutofill()); } catch { return false; }
}
