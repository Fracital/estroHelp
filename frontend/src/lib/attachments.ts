import type { Attachment } from './workspace-contract';

export const IMAGE_TYPES = new Set(['image/png', 'image/jpeg', 'image/webp', 'image/gif']);
export const MAX_IMAGE_BYTES = 4 * 1024 * 1024;
export const MAX_FILE_BYTES = 200 * 1024;
export const MAX_ATTACHMENTS = 8;

const TEXT_EXT = /\.(txt|md|markdown|json|jsonc|yaml|yml|toml|ini|cfg|conf|env|csv|tsv|xml|html?|css|scss|less|js|mjs|cjs|jsx|ts|tsx|py|rb|go|rs|java|kt|swift|c|h|cc|cpp|hpp|cs|php|sh|bash|zsh|fish|ps1|sql|graphql|gql|proto|lua|r|pl|ex|exs|erl|hs|scala|clj|dart|vue|svelte|astro|diff|patch|log|lock|gitignore|dockerfile|makefile)$/i;

export function isTextFile(f: File): boolean {
  if (f.type.startsWith('text/')) return true;
  if (['application/json', 'application/xml', 'application/javascript', 'application/x-sh', 'application/toml', 'application/yaml'].includes(f.type)) return true;
  return TEXT_EXT.test(f.name) || !f.name.includes('.');
}

function readAsDataUrl(f: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(r.error);
    r.readAsDataURL(f);
  });
}

export async function readAttachments(files: File[], existing = 0): Promise<{ attachments: Attachment[]; errors: string[] }> {
  const attachments: Attachment[] = [];
  const errors: string[] = [];
  for (const f of files) {
    if (existing + attachments.length >= MAX_ATTACHMENTS) { errors.push(`At most ${MAX_ATTACHMENTS} attachments per message.`); break; }
    if (IMAGE_TYPES.has(f.type)) {
      if (f.size > MAX_IMAGE_BYTES) { errors.push(`${f.name}: images must be under 4 MB.`); continue; }
      attachments.push({ kind: 'image', name: f.name || 'image', mime: f.type, size: f.size, dataUrl: await readAsDataUrl(f) });
    } else if (isTextFile(f)) {
      if (f.size > MAX_FILE_BYTES) { errors.push(`${f.name}: text files must be under 200 KB.`); continue; }
      const text = await f.text();
      if (text.includes('\u0000')) { errors.push(`${f.name}: not a text file.`); continue; }
      attachments.push({ kind: 'file', name: f.name, mime: f.type || 'text/plain', size: f.size, text });
    } else {
      errors.push(`${f.name}: only images and text files are supported.`);
    }
  }
  return { attachments, errors };
}

export function filesFromDataTransfer(dt: DataTransfer | null): File[] {
  if (!dt) return [];
  const out: File[] = [];
  if (dt.items?.length) {
    for (const item of Array.from(dt.items)) {
      if (item.kind !== 'file') continue;
      const f = item.getAsFile();
      if (f) out.push(f);
    }
  } else if (dt.files?.length) {
    out.push(...Array.from(dt.files));
  }
  return out;
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
