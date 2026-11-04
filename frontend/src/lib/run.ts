// "Run in shell": docs samples hand a command to the chat page, which opens the
// workspace shell and types it. The command travels in the query string as
// base64url so quotes and newlines survive.

export type RunLang = 'sh' | 'node';

const RUN_KEY = 'kali:shell-key';

export function runnableLang(hint: string | undefined): RunLang | null {
  const h = (hint ?? '').toLowerCase();
  if (/^(sh|bash|shell|zsh|curl|terminal|environment|env)$/.test(h)) return 'sh';
  if (/^(node|nodejs|node\.js|js|javascript|mjs)$/.test(h)) return 'node';
  return null;
}

function toBase64Url(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function fromBase64Url(s: string): string {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4);
  const bin = atob(b64);
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

export function runUrl(code: string, lang: RunLang): string {
  return `/chat?run=${toBase64Url(code)}&lang=${lang}`;
}

export function readRunParam(): { code: string; lang: RunLang } | null {
  const q = new URLSearchParams(location.search);
  const run = q.get('run');
  if (!run) return null;
  try {
    const lang = q.get('lang') === 'node' ? 'node' : 'sh';
    return { code: fromBase64Url(run), lang };
  } catch { return null; }
}

/**
 * Rewrites a docs sample so it targets this deployment. Values are substituted
 * literally: the workspace shell's `$VAR` expansion does not survive into its
 * curl, so the exported ESTRO_* variables are only a convenience for follow-ups.
 */
export function localizeSample(code: string, origin: string, secret: string | null, lang: RunLang): string {
  let out = code.replace(/https:\/\/estro\.help\b/g, origin);
  if (secret) {
    out = out.replace(/\$ESTRO_TOKEN\b|estro_YOUR_TOKEN/g, secret);
    if (lang === 'node') out = out.replace(/process\.env\.ESTRO_TOKEN\b|\bESTRO_TOKEN\b/g, JSON.stringify(secret));
  }
  return out;
}

/**
 * Collapses a shell sample to one line the pty can take at once: drop comments,
 * join continuations, and trim curl flags the workspace's curl does not know
 * (-N / --no-buffer, -sS, -I).
 */
export function toShellLine(code: string): string {
  const joined = code
    .split('\n')
    .filter((l) => !/^\s*#/.test(l))
    .join('\n')
    .replace(/\\\r?\n\s*/g, ' ');
  // Separate lines are separate commands, except inside an open quote
  // (a multi-line JSON body), where the line break is just whitespace.
  let out = '';
  let quote: '"' | "'" | null = null;
  for (const raw of joined.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    if (out) out += quote ? ' ' : '; ';
    out += line;
    for (let i = 0; i < line.length; i++) {
      const ch = line[i];
      if (quote) {
        if (ch === '\\' && quote === '"') { i++; continue; }
        if (ch === quote) quote = null;
      } else if (ch === '"' || ch === "'") {
        quote = ch;
      }
    }
  }
  return out.replace(/\bcurl\s+(?:(?:-N|--no-buffer|-sS|-I)\s+)+/g, 'curl ').trim();
}

/** Programs that exist inside the workspace shell. Samples that call anything else cannot run there. */
const SANDBOX_BINS = new Set(['curl', 'node', 'npm', 'npx', 'echo', 'printf', 'cat', 'ls', 'mkdir', 'cd', 'export', 'env', 'sh', 'bash', 'true', 'set', 'unset', 'read', 'test', 'sleep', 'head', 'tail', 'grep', 'sed', 'awk', 'wc', 'tee', 'jq']);

/** True when every command in a shell sample uses a program the sandbox has. */
export function sandboxCanRun(code: string): boolean {
  const line = toShellLine(code);
  // Split on command separators outside quotes only.
  const cmds: string[] = [];
  let cur = '';
  let quote: '"' | "'" | null = null;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (quote) {
      if (ch === '\\' && quote === '"') { cur += ch + (line[i + 1] ?? ''); i++; continue; }
      if (ch === quote) quote = null;
      cur += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch; cur += ch;
    } else if (ch === ';' || ch === '|' || (ch === '&' && line[i + 1] === '&')) {
      cmds.push(cur); cur = '';
      if (ch === '&' || (ch === '|' && line[i + 1] === '|')) i++;
    } else {
      cur += ch;
    }
  }
  cmds.push(cur);
  for (const cmd of cmds) {
    const words = cmd.trim().split(/\s+/).filter(Boolean);
    let i = 0;
    while (i < words.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[i])) i++;
    if (i >= words.length) continue;
    if (!SANDBOX_BINS.has(words[i].replace(/^.*\//, ''))) return false;
  }
  return true;
}

/** The run language for a sample, or null when the sample cannot run in the workspace. */
export function runnableSample(hint: string | undefined, code: string): RunLang | null {
  const lang = runnableLang(hint);
  if (!lang) return null;
  if (lang === 'sh' && !sandboxCanRun(code)) return null;
  return lang;
}

export function readShellKey(): string | null {
  try { return localStorage.getItem(RUN_KEY); } catch { return null; }
}
export function writeShellKey(secret: string | null): void {
  try { if (secret) localStorage.setItem(RUN_KEY, secret); else localStorage.removeItem(RUN_KEY); } catch {}
}
