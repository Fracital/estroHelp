import { signal } from '@preact/signals';
import { api } from './api';

export interface User {
  id: number;
  username: string;
  email: string | null;
  emailVerified: boolean;
  admin: boolean;
  sandbox: boolean;
  created: string;
  lastLogin: string | null;
}

export interface Ban {
  reason: string;
  expires: string | null;
}

export const user = signal<User | null>(null);
export const ban = signal<Ban | null>(null);
export const requireVerifiedEmail = signal(true);
export const authReady = signal(false);

let pending: Promise<void> | null = null;

export function loadMe(force = false): Promise<void> {
  if (pending && !force) return pending;
  pending = api<{ user: User | null; ban?: Ban | null; requireVerifiedEmail: boolean }>('/api/auth/me')
    .then((d) => {
      user.value = d.user;
      ban.value = d.ban ?? null;
      requireVerifiedEmail.value = d.requireVerifiedEmail;
    })
    .catch(() => { user.value = null; })
    .finally(() => { authReady.value = true; });
  return pending;
}

export async function logout() {
  try { await api('/api/auth/logout', { method: 'POST', body: {} }); } catch {}
  user.value = null;
  location.href = '/';
}

/** Verified means allowed to chat: either verified, or verification is not enforced, or admin. */
export function canChat(u: User | null): boolean {
  if (!u) return false;
  return u.admin || u.emailVerified || !requireVerifiedEmail.value;
}

/** Redirects to the right page when the viewer is not allowed to use `page`. Returns true when OK. */
export async function gate(page: 'chat' | 'admin' | 'account' | 'verify'): Promise<boolean> {
  await loadMe();
  const u = user.value;
  const next = encodeURIComponent(location.pathname + location.search);
  if (!u) { location.replace(`/login?next=${next}`); return false; }
  if (page === 'admin' && !u.admin) { location.replace('/chat'); return false; }
  if (page === 'chat' && !canChat(u)) { location.replace('/verify'); return false; }
  if (page === 'verify' && canChat(u)) { location.replace('/chat'); return false; }
  return true;
}

export function nextUrl(): string {
  const n = new URLSearchParams(location.search).get('next') ?? '/chat';
  return n.startsWith('/') && !n.startsWith('//') ? n : '/chat';
}
