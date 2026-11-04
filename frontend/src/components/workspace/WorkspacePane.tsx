import { useEffect, useRef, useState } from 'preact/hooks';
import type { Terminal as XTerminal } from '@xterm/xterm';
import { ArrowLeft, ArrowUp, ChevronRight, Download, File as FileIcon, FilePlus, Files, Folder, FolderPlus, Plus, RefreshCw, SquareTerminal, Trash2, Upload, X } from 'lucide-preact';
import type { WorkspacePaneProps } from '../../lib/workspace-contract';
import {
  HOME, WORKSPACE_ROOT, bootPhase, closeShell, createShell, displayPath, isSupported, listEntries, listShells, looksBinary, makeDirs,
  manager, onBootPhase, onShellsChanged, openShellSession, parentOf, readBytes, removePath, resolvePath, writeBytes, writeText,
  type BootPhase, type FsEntry, type ShellInfo,
} from './dusk';
import '@xterm/xterm/css/xterm.css';

function IconButton({ label, class: cls, children, ...rest }: { label: string; class?: string; children: any; [k: string]: any }) {
  return <button type="button" class={cls ?? 'kali-ws-icon'} title={label} aria-label={label} {...rest}>{children}</button>;
}

export default function WorkspacePane({ tab, onTabChange, onClose }: WorkspacePaneProps) {
  const [boot, setBoot] = useState<{ phase: BootPhase; error: string }>(() => bootPhase());

  useEffect(() => {
    const off = onBootPhase((phase, error) => setBoot({ phase, error }));
    if (isSupported()) manager().catch(() => {});
    else setBoot({ phase: 'error', error: 'This page is not cross-origin isolated (SharedArrayBuffer unavailable), so the workspace cannot start.' });
    return off;
  }, []);

  return (
    <section class="kali-ws" aria-label="Workspace">
      <header class="kali-ws-head">
        <div class="kali-ws-tabs" role="tablist">
          <button type="button" role="tab" aria-selected={tab === 'files'} class={tab === 'files' ? 'active' : ''} onClick={() => onTabChange('files')}><Files size={14} aria-hidden="true" />Files</button>
          <button type="button" role="tab" aria-selected={tab === 'shell'} class={tab === 'shell' ? 'active' : ''} onClick={() => onTabChange('shell')}><SquareTerminal size={14} aria-hidden="true" />Shell</button>
        </div>
        <IconButton class="kali-ws-icon kali-ws-close" label="Close the workspace pane" onClick={onClose}><X size={15} aria-hidden="true" /></IconButton>
      </header>
      <div class="kali-ws-body">
        {boot.phase === 'ready'
          ? (tab === 'files' ? <FilesView /> : <ShellView />)
          : <BootStatus phase={boot.phase} error={boot.error} onRetry={() => manager().catch(() => {})} />}
      </div>
    </section>
  );
}

function BootStatus({ phase, error, onRetry }: { phase: BootPhase; error: string; onRetry: () => void }) {
  if (phase === 'error') {
    return <p class="kali-ws-note kali-ws-error">{error} <button type="button" class="kali-cs-secondary" onClick={onRetry}>Retry</button></p>;
  }
  const label = phase === 'loading' ? 'Loading the workspace runtime…' : phase === 'booting' ? 'Starting the shell and filesystem…' : 'Starting workspace…';
  return <p class="kali-ws-note"><i class="kali-spinner" /> {label}</p>;
}

// ---- Files ---------------------------------------------------------------------

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function FilesView() {
  const [path, setPath] = useState(WORKSPACE_ROOT);
  const [entries, setEntries] = useState<FsEntry[] | null>(null);
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [file, setFile] = useState<{ path: string; text: string; binary: boolean; size: number } | null>(null);
  const upload = useRef<HTMLInputElement>(null);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);

  const reload = () => setRefresh((n) => n + 1);
  const fail = (reason: unknown, fallback: string) => setError(reason instanceof Error ? reason.message : fallback);

  useEffect(() => {
    setError('');
    listEntries(path)
      .then((items) => { if (alive.current) setEntries(items); })
      .catch((reason) => { if (alive.current) { setEntries(null); fail(reason, 'This folder could not be read.'); } });
  }, [path, refresh]);

  useEffect(() => {
    const onChange = () => reload();
    window.addEventListener('kali:workspace-changed', onChange);
    return () => window.removeEventListener('kali:workspace-changed', onChange);
  }, []);

  const openFile = async (entry: FsEntry) => {
    try {
      const bytes = await readBytes(entry.path);
      if (!alive.current) return;
      const binary = looksBinary(bytes);
      setFile({ path: entry.path, text: binary ? '' : new TextDecoder().decode(bytes), binary, size: bytes.length });
    } catch (reason) { fail(reason, 'The file could not be read.'); }
  };

  const newFile = async () => {
    const name = prompt('New file name');
    if (!name?.trim()) return;
    try {
      const target = resolvePath(name.trim(), path);
      await writeText(target, '');
      reload();
      setFile({ path: target, text: '', binary: false, size: 0 });
    } catch (reason) { fail(reason, 'Could not create the file.'); }
  };

  const newFolder = async () => {
    const name = prompt('New folder name');
    if (!name?.trim()) return;
    try { await makeDirs(resolvePath(name.trim(), path)); reload(); } catch (reason) { fail(reason, 'Could not create the folder.'); }
  };

  const remove = async (entry: FsEntry) => {
    if (!confirm(`Delete ${entry.name}${entry.isDirectory ? ' and everything in it' : ''}?`)) return;
    try { await removePath(entry.path); reload(); } catch (reason) { fail(reason, 'Could not delete.'); }
  };

  const onUpload = async (e: Event) => {
    const input = e.target as HTMLInputElement;
    const files = Array.from(input.files ?? []);
    input.value = '';
    for (const f of files) {
      try { await writeBytes(`${path}/${f.name}`, new Uint8Array(await f.arrayBuffer())); } catch (reason) { fail(reason, `Could not upload ${f.name}.`); }
    }
    reload();
  };

  if (file) return <Editor file={file} onBack={() => { setFile(null); reload(); }} onError={(m) => setError(m)} />;

  const underHome = path === HOME || path.startsWith(`${HOME}/`);
  const base = underHome ? HOME : '/';
  const crumbs = path.slice(base === '/' ? 1 : HOME.length).split('/').filter(Boolean);

  return (
    <div class="kali-ws-files">
      <div class="kali-ws-bar">
        <IconButton label="Up one folder" disabled={path === '/'} onClick={() => setPath(parentOf(path))}><ArrowUp size={14} aria-hidden="true" /></IconButton>
        <nav class="kali-ws-crumbs" aria-label="Folder">
          <button type="button" onClick={() => setPath(base)}>{underHome ? '~' : '/'}</button>
          {crumbs.map((name, i) => {
            const target = `${base === '/' ? '' : base}/${crumbs.slice(0, i + 1).join('/')}`;
            return <span key={target}><span class="kali-ws-sep">/</span><button type="button" onClick={() => setPath(target)}>{name}</button></span>;
          })}
        </nav>
        <IconButton label="New file" onClick={newFile}><FilePlus size={14} aria-hidden="true" /></IconButton>
        <IconButton label="New folder" onClick={newFolder}><FolderPlus size={14} aria-hidden="true" /></IconButton>
        <IconButton label="Upload files" onClick={() => upload.current?.click()}><Upload size={14} aria-hidden="true" /></IconButton>
        <IconButton label="Refresh the folder" onClick={reload}><RefreshCw size={14} aria-hidden="true" /></IconButton>
        <input ref={upload} type="file" multiple hidden onChange={onUpload} />
      </div>
      {error && <p class="kali-ws-note kali-ws-error">{error}</p>}
      {!entries && !error && <p class="kali-ws-note">Loading…</p>}
      {entries && entries.length === 0 && <p class="kali-ws-note">This folder is empty.</p>}
      {entries && entries.length > 0 && (
        <ul class="kali-ws-list">
          {entries.map((entry) => (
            <li key={entry.path} class="kali-ws-row">
              <button type="button" onClick={() => (entry.isDirectory ? setPath(entry.path) : void openFile(entry))}>
                {entry.isDirectory ? <Folder size={14} aria-hidden="true" /> : <FileIcon size={14} aria-hidden="true" />}
                <span class="kali-ws-entry">{entry.name}</span>
                {!entry.isDirectory && <span class="kali-ws-size">{fmtSize(entry.size)}</span>}
                {entry.isDirectory && <ChevronRight size={14} class="kali-ws-go" aria-hidden="true" />}
              </button>
              <IconButton class="kali-ws-icon kali-ws-rowdel" label={`Delete ${entry.name}`} onClick={(e: Event) => { e.stopPropagation(); remove(entry); }}><Trash2 size={13} aria-hidden="true" /></IconButton>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Editor({ file, onBack, onError }: { file: { path: string; text: string; binary: boolean; size: number }; onBack: () => void; onError: (m: string) => void }) {
  const [text, setText] = useState(file.text);
  const [saved, setSaved] = useState(file.text);
  const [busy, setBusy] = useState(false);
  const dirty = text !== saved;
  const name = file.path.split('/').pop() ?? file.path;

  const save = async () => {
    setBusy(true);
    try { await writeText(file.path, text); setSaved(text); } catch (reason) { onError(reason instanceof Error ? reason.message : 'Could not save.'); } finally { setBusy(false); }
  };

  const download = async () => {
    const bytes = await readBytes(file.path);
    const url = URL.createObjectURL(new Blob([bytes as BlobPart]));
    const a = document.createElement('a');
    a.href = url; a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  const remove = async () => {
    if (!confirm(`Delete ${name}?`)) return;
    try { await removePath(file.path); onBack(); } catch (reason) { onError(reason instanceof Error ? reason.message : 'Could not delete.'); }
  };

  useEffect(() => {
    const key = (e: KeyboardEvent) => { if ((e.metaKey || e.ctrlKey) && e.key === 's') { e.preventDefault(); if (dirty) void save(); } };
    document.addEventListener('keydown', key);
    return () => document.removeEventListener('keydown', key);
  }, [dirty, text]);

  return (
    <div class="kali-ws-viewer">
      <div class="kali-ws-bar">
        <IconButton label="Back to the folder" onClick={() => { if (!dirty || confirm('Discard unsaved changes?')) onBack(); }}><ArrowLeft size={14} aria-hidden="true" /></IconButton>
        <span class="kali-ws-name" title={file.path}>{displayPath(file.path)}{dirty && <span class="kali-ws-dirty"> ●</span>}</span>
        {!file.binary && <button type="button" class="kali-cs-primary" disabled={!dirty || busy} onClick={save}>Save</button>}
        <IconButton label="Download" onClick={download}><Download size={14} aria-hidden="true" /></IconButton>
        <IconButton label="Delete file" onClick={remove}><Trash2 size={14} aria-hidden="true" /></IconButton>
      </div>
      {file.binary
        ? <p class="kali-ws-note">This is a binary file ({fmtSize(file.size)}) and cannot be shown as text. Download it to view.</p>
        : <textarea class="kali-ws-editor" spellcheck={false} value={text} onInput={(e) => setText((e.target as HTMLTextAreaElement).value)}
            onKeyDown={(e) => {
              if (e.key === 'Tab') {
                e.preventDefault();
                const el = e.target as HTMLTextAreaElement;
                const { selectionStart: s, selectionEnd: en } = el;
                const next = el.value.slice(0, s) + '  ' + el.value.slice(en);
                setText(next);
                requestAnimationFrame(() => { el.selectionStart = el.selectionEnd = s + 2; });
              }
            }} />}
    </div>
  );
}

// ---- Shell ---------------------------------------------------------------------

function ShellView() {
  const [shells, setShells] = useState<ShellInfo[]>(() => listShells());
  const [activeId, setActiveId] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [opening, setOpening] = useState(false);
  const host = useRef<HTMLDivElement>(null);
  const started = useRef(false);

  useEffect(() => onShellsChanged(() => setShells(listShells())), []);

  const addShell = async () => {
    setOpening(true);
    setError('');
    try { setActiveId(await createShell(80, 24)); } catch (reason) { setError(reason instanceof Error ? reason.message : 'The shell could not start.'); } finally { setOpening(false); }
  };

  useEffect(() => {
    if (started.current || shells.length > 0) return;
    started.current = true;
    void addShell();
  }, []);

  const active = shells.find((s) => s.id === activeId) ?? shells[shells.length - 1] ?? null;
  const activeKey = active?.id ?? '';

  useEffect(() => {
    if (!activeKey || !host.current) return;
    let disposed = false;
    let dispose = () => {};
    void (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')]);
      const el = host.current;
      if (disposed || !el) return;
      const cs = getComputedStyle(document.documentElement);
      const fg = cs.getPropertyValue('--theme-fg').trim() || '#f2f2f2';
      const accent = cs.getPropertyValue('--theme-accent').trim() || '#ffffff';
      const term: XTerminal = new Terminal({
        cursorBlink: true,
        fontFamily: "'JetBrains Mono', ui-monospace, Menlo, monospace",
        fontSize: 13,
        scrollback: 5000,
        theme: { background: '#111111', foreground: fg, cursor: accent, selectionBackground: 'rgba(255,255,255,.25)' },
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(el);
      fit.fit();
      const session = openShellSession(activeKey);
      term.write(session?.scrollback() ?? '', () => term.scrollToBottom());
      session?.resize(term.cols, term.rows);
      const off = session?.onData((data) => term.write(data)) ?? (() => {});
      const input = term.onData((data) => session?.write(data));
      const resize = term.onResize(({ cols, rows }) => session?.resize(cols, rows));
      const observer = new ResizeObserver(() => { try { fit.fit(); } catch {} });
      observer.observe(el);
      term.focus();
      dispose = () => { observer.disconnect(); off(); input.dispose(); resize.dispose(); term.dispose(); };
    })();
    return () => { disposed = true; dispose(); };
  }, [activeKey]);

  return (
    <div class="kali-ws-shell">
      <div class="kali-ws-shelltabs" role="tablist" aria-label="Shells">
        {shells.map((shell) => (
          <div key={shell.id} role="tab" tabIndex={0} aria-selected={shell.id === activeKey}
            class={`kali-ws-shelltab${shell.id === activeKey ? ' active' : ''}${shell.exited ? ' exited' : ''}`}
            onClick={() => setActiveId(shell.id)} onKeyDown={(e) => { if (e.key === 'Enter') setActiveId(shell.id); }}
            onAuxClick={(e) => { if (e.button === 1) { e.preventDefault(); closeShell(shell.id); } }}
            onMouseDown={(e) => { if (e.button === 1) e.preventDefault(); }}>
            <SquareTerminal size={13} aria-hidden="true" />
            <span class="kali-ws-shellname">{shell.name}</span>
            <IconButton class="kali-ws-icon kali-ws-shellclose" label="Close this shell" onClick={(e: Event) => { e.stopPropagation(); closeShell(shell.id); }}><X size={12} aria-hidden="true" /></IconButton>
          </div>
        ))}
        <IconButton class="kali-ws-icon kali-ws-newshell" label="New shell" disabled={opening} onClick={addShell}><Plus size={14} aria-hidden="true" /></IconButton>
      </div>
      {error && <p class="kali-ws-note kali-ws-error">{error}</p>}
      {active?.exited && <p class="kali-ws-note">This shell has exited. Close it, or open a new one.</p>}
      {!active && !error && <p class="kali-ws-note">No shell is open. Use + to open one.</p>}
      <div class="kali-ws-term" ref={host} />
    </div>
  );
}
