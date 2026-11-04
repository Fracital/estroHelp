import { Play } from 'lucide-preact';
import { runUrl, type RunLang } from '../lib/run';

/** Sends a docs sample to the chat page, which opens the shell and types it. */
export default function RunButton({ code, lang, title }: { code: string; lang: RunLang; title?: string }) {
  const label = lang === 'node' ? 'Run with node in the chat shell' : 'Run in the chat shell';
  return (
    <a class="run-code" href={runUrl(code, lang)} title={title ?? label} aria-label={label}>
      <Play size={12} aria-hidden="true" />Run
    </a>
  );
}
