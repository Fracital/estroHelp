import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { Check, ChevronDown, Eye, FolderSearch, Pencil, Search, Terminal, Trash2, Wrench, X } from 'lucide-preact';
import type { ToolCall } from '../../lib/workspace-contract';
import { parseArguments } from '../../lib/agent';
import { renderMarkdown } from '../../lib/markdown';

export interface ToolRun {
  output: string;
  running: boolean;
  ok?: boolean;
  exitCode?: number;
}

const SHELL_TOOLS = new Set(['run_shell', 'shell', 'bash', 'exec', 'run_command']);
const WORKSPACE_ROOT = '~/workspace';

/** A terminal result from the workspace, split into its command echo, exit status and output. */
export function terminalParts(text: string): { output: string; exitCode: number | null } {
  const exit = /^\[exit code (\d+)\]|^exit status (\d+)/m.exec(text);
  const lines = text.split('\n');
  const body = lines.filter((line, index) => {
    if (index === 0 && line.startsWith('$ ')) return false;
    if (/^\[exit code \d+\]|^exit status \d+/.test(line)) return false;
    if (line === '--- stdout ---' || line === '--- stderr ---' || line === '(no output)') return false;
    return true;
  });
  return { output: body.join('\n').replace(/^\n+/, '').replace(/\n+$/, ''), exitCode: exit ? Number(exit[1] ?? exit[2]) : null };
}

function toolGlyph(name: string) {
  const Icon = name === 'read_file' || name === 'list_dir' ? Eye
    : name === 'write_file' ? Pencil
    : name === 'delete_path' ? Trash2
    : name === 'search_files' ? Search
    : name === 'find_files' ? FolderSearch
    : SHELL_TOOLS.has(name) ? Terminal : Wrench;
  return <Icon size={13} aria-hidden="true" />;
}

/** The card under a terminal command, as VS Code draws it: the command with a dot, the prompt line, and the output. */
function TerminalCardBody({ command, output, running }: { command: string; output: string; running: boolean }) {
  const out = useRef<HTMLPreElement>(null);
  useEffect(() => { if (out.current && running) out.current.scrollTop = out.current.scrollHeight; }, [output, running]);
  return (
    <div class="kali-term-card">
      <div class="kali-term-input"><span class="kali-term-dot" aria-hidden="true" /><code>{command}</code></div>
      <div class="kali-term-prompt"><code><span class="kali-term-glyph">{'⬢'}</span> user@vm:{WORKSPACE_ROOT}$ {command}</code></div>
      <pre ref={out} class="kali-term-output">{output}</pre>
    </div>
  );
}

/** A terminal command as one row that expands into its card: "Ran <command>", exit status, chevron. */
export function TerminalToolBlock({ call, run }: { call: ToolCall; run?: ToolRun }) {
  const running = run?.running ?? false;
  const [open, setOpen] = useState(running);
  useEffect(() => { if (running) setOpen(true); }, [running]);
  const args = parseArguments(call);
  const command = typeof args.command === 'string' ? args.command : call.name;
  const parts = terminalParts(run?.output ?? '');
  const exitCode = run && !running ? (typeof run.exitCode === 'number' ? run.exitCode : parts.exitCode ?? (run.ok === false ? 1 : 0)) : null;
  return (
    <div class={`kali-trace kali-tool kali-tool-term${running ? ' running' : ''}${open ? ' open' : ''}`}>
      <button type="button" class="kali-trace-label" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span class="kali-trace-icon">{toolGlyph(call.name)}</span>
        <span class="kali-trace-title"><span class="kali-term-verb">{running ? 'Running' : 'Ran'}</span> <code class="kali-term-command">{command}</code></span>
        {running ? <i class="kali-spinner" /> : exitCode !== null && exitCode !== 0 ? <span class="kali-term-exit">exit {exitCode}</span> : null}
        <ChevronDown size={12} class="kali-trace-chevron" />
      </button>
      {open && <div class="kali-trace-body"><TerminalCardBody command={command} output={running ? (run?.output ?? '') : parts.output} running={running} /></div>}
    </div>
  );
}

/** One tool call, drawn the same way for every tool: an icon, a label, a status, and a body that expands. */
export function ToolCallBlock({ call, run }: { call: ToolCall; run?: ToolRun }) {
  if (SHELL_TOOLS.has(call.name)) return <TerminalToolBlock call={call} run={run} />;
  const running = run?.running ?? false;
  const [open, setOpen] = useState(false);
  const args = parseArguments(call);
  const first = args.path ?? args.file ?? args.pattern ?? args.glob ?? args.query;
  const summary = typeof first === 'string' ? first : '';
  const status = running ? <i class="kali-spinner" /> : !run ? null : run.ok === false ? <X size={11} /> : <Check size={11} />;
  return (
    <div class={`kali-trace kali-tool${running ? ' running' : ''}${open ? ' open' : ''}`}>
      <button type="button" class="kali-trace-label" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span class="kali-trace-icon">{toolGlyph(call.name)}</span>
        <span class={`kali-trace-title${running ? ' shimmer' : ''}`}>{call.name}{summary ? <em> · {summary}</em> : null}</span>
        <span class="kali-trace-status">{status}</span>
        <ChevronDown size={12} class="kali-trace-chevron" />
      </button>
      {open && (
        <div class="kali-trace-body">
          {Object.keys(args).length > 0 && <pre class="kali-tool-args">{Object.entries(args).map(([k, v]) => `${k}: ${typeof v === 'string' ? v : JSON.stringify(v)}`).join('\n')}</pre>}
          {run?.output && <pre class="kali-tool-output">{run.output}</pre>}
        </div>
      )}
    </div>
  );
}

/** One stretch of reasoning, collapsed under its duration. */
export function ThoughtBlock({ text, active, durationMs }: { text: string; active: boolean; durationMs?: number }) {
  const [open, setOpen] = useState(false);
  const title = active ? 'Thinking…' : durationMs ? `Thought for ${(durationMs / 1000).toFixed(1)}s` : 'Thought';
  const html = useMemo(() => (open ? renderMarkdown(text) : ''), [open, text]);
  return (
    <div class={`kali-trace kali-think${active ? ' running' : ''}${open ? ' open' : ''}`}>
      <button type="button" class="kali-trace-label" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span class="kali-trace-icon">{active ? <i class="kali-pulse-dot" /> : <Check size={11} />}</span>
        <span class={`kali-trace-title${active ? ' shimmer' : ''}`}>{title}</span>
        <ChevronDown size={12} class="kali-trace-chevron" />
      </button>
      {open && <div class="kali-trace-body"><div class="kali-think-md kali-markdown" dangerouslySetInnerHTML={{ __html: html }} /></div>}
    </div>
  );
}

/** The trace of one reply: its thinking, then each tool call in order. */
export function TraceView({ thinking, thinkingActive, thinkingMs, calls, runs }: { thinking?: string; thinkingActive: boolean; thinkingMs?: number; calls: ToolCall[]; runs?: Record<string, ToolRun> }) {
  if (!thinking?.trim() && !calls.length) return null;
  return (
    <div class="kali-trace-stack">
      {thinking?.trim() && <ThoughtBlock text={thinking.trim()} active={thinkingActive} durationMs={thinkingMs} />}
      {calls.map((c) => <ToolCallBlock key={c.id} call={c} run={runs?.[c.id]} />)}
    </div>
  );
}
