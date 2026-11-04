import { Marked } from 'marked';
import markedKatex from 'marked-katex-extension';
import hljs from 'highlight.js/lib/common';
import DOMPurify from 'dompurify';
import 'katex/dist/katex.min.css';

const COPY_ICON = '<svg xmlns="http://www.w3.org/2000/svg" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>';
const CHECK_ICON = '<svg xmlns="http://www.w3.org/2000/svg" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>';

const marked = new Marked({
  gfm: true,
  breaks: false,
  renderer: {
    code({ text, lang }) {
      const language = lang && hljs.getLanguage(lang) ? lang : '';
      const html = language ? hljs.highlight(text, { language }).value : escapeHtml(text);
      const label = language || 'text';
      return `<div class="kali-codeblock"><div class="kali-codeblock-head"><span class="kali-codeblock-lang">${label}</span><button type="button" class="kali-codeblock-copy" data-copy aria-label="Copy code">${COPY_ICON}<span>Copy</span></button></div><pre><code class="hljs language-${label}">${html}</code></pre></div>`;
    },
    table(token) {
      const header = token.header.map((c) => `<th>${this.parser.parseInline(c.tokens)}</th>`).join('');
      const rows = token.rows.map((r) => `<tr>${r.map((c) => `<td>${this.parser.parseInline(c.tokens)}</td>`).join('')}</tr>`).join('');
      return `<div class="kali-table-wrap"><table><thead><tr>${header}</tr></thead><tbody>${rows}</tbody></table></div>`;
    },
    link({ href, title, tokens }) {
      const text = this.parser.parseInline(tokens);
      return `<a href="${escapeHtml(href)}"${title ? ` title="${escapeHtml(title)}"` : ''} target="_blank" rel="noreferrer noopener">${text}</a>`;
    },
  },
});
marked.use(markedKatex({ throwOnError: false, nonStandard: true }));

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
}

export function renderMarkdown(src: string): string {
  const raw = marked.parse(src, { async: false }) as string;
  // mathMl keeps KaTeX's <math> fallback; style stays on KaTeX spans (sizes, strut heights).
  return DOMPurify.sanitize(raw, { USE_PROFILES: { html: true, mathMl: true, svg: true }, ADD_ATTR: ['target', 'data-copy'] });
}

/** Wires copy buttons inside a rendered markdown container. */
export function onMarkdownClick(e: Event) {
  const btn = (e.target as HTMLElement).closest('[data-copy]') as HTMLButtonElement | null;
  if (!btn) return;
  const code = btn.closest('.kali-codeblock')?.querySelector('code')?.textContent ?? '';
  navigator.clipboard.writeText(code).then(() => {
    btn.innerHTML = `${CHECK_ICON}<span>Copied</span>`;
    setTimeout(() => { btn.innerHTML = `${COPY_ICON}<span>Copy</span>`; }, 1200);
  });
}
