// The browser workspace: Dusk, a Node-compatible runtime with a POSIX shell,
// running in this tab. Its filesystem persists in OPFS, so files survive
// reloads. Nothing here touches the host machine. Networking (curl, fetch)
// goes through the site's own /wisp relay.

type DuskModule = typeof import('@nightnetwork/dusk');
type Repl = Awaited<ReturnType<DuskModule['bootRepl']>>;
type ProcessManager = Repl['processManager'];
type ProcessHandle = Awaited<ReturnType<ProcessManager['spawn']>>;

export const HOME = '/home/user';
export const WORKSPACE_ROOT = `${HOME}/workspace`;
export const HISTORY_FILE = `${HOME}/.dsh_history`;
const SHELL = '/bin/dsh';
const HOSTNAME = 'kali';
const ENV: Record<string, string> = {
  HOME, PATH: '/bin', USER: 'user', TERM: 'xterm-256color', SHELL, HOSTNAME,
  DSH_PROMPT: '⬢ user@kali:', HISTFILE: HISTORY_FILE, HISTSIZE: '1000',
};
const OUTPUT_KEEP = 200_000;

export class WorkspaceUnavailableError extends Error {}

export function isSupported(): boolean {
  return typeof window !== 'undefined'
    && globalThis.crossOriginIsolated === true
    && typeof SharedArrayBuffer !== 'undefined'
    && typeof navigator.storage?.getDirectory === 'function';
}

export type BootPhase = 'idle' | 'loading' | 'booting' | 'ready' | 'error';
let phase: BootPhase = 'idle';
let phaseError = '';
const phaseListeners = new Set<(phase: BootPhase, error: string) => void>();

function setPhase(next: BootPhase, error = '') {
  phase = next;
  phaseError = error;
  for (const l of phaseListeners) l(next, error);
}

export function bootPhase(): { phase: BootPhase; error: string } {
  return { phase, error: phaseError };
}

export function onBootPhase(listener: (phase: BootPhase, error: string) => void): () => void {
  phaseListeners.add(listener);
  return () => { phaseListeners.delete(listener); };
}

type DuskWindow = Window & { kaliDusk?: DuskModule };
let loading: Promise<DuskModule> | undefined;

/** Loads /dusk/loader.js once per tab; a failed load is retried on the next call. */
function loadDusk(): Promise<DuskModule> {
  loading ??= new Promise<DuskModule>((resolve, reject) => {
    const existing = (window as DuskWindow).kaliDusk;
    if (existing) { resolve(existing); return; }
    const script = document.createElement('script');
    script.type = 'module';
    script.src = new URL('/dusk/loader.js', location.origin).href;
    script.onload = () => {
      const dusk = (window as DuskWindow).kaliDusk;
      if (dusk) resolve(dusk);
      else { loading = undefined; reject(new Error('The workspace runtime loaded without its module.')); }
    };
    script.onerror = () => {
      loading = undefined;
      script.remove();
      reject(new Error('The workspace runtime could not be fetched.'));
    };
    document.head.append(script);
  });
  return loading;
}

export function relayUrl(): string {
  return `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/wisp`;
}

function networkOptions() {
  const loadLibcurl = async () => {
    const url = new URL('/dusk/nova/nova_wasm.js', location.origin).href;
    const nova = await import(/* @vite-ignore */ url);
    await nova.default();
    return new nova.LibCurl();
  };
  return { loadLibcurl, proxyUrl: relayUrl() };
}

let booting: Promise<ProcessManager> | undefined;

/** Boots Dusk once per tab. Later calls share the runtime and filesystem. */
export function manager(): Promise<ProcessManager> {
  if (!isSupported()) {
    return Promise.reject(new WorkspaceUnavailableError('This page is not cross-origin isolated, so the workspace cannot start.'));
  }
  booting ??= (async () => {
    setPhase('loading');
    const dusk = await loadDusk();
    setPhase('booting');
    await navigator.storage?.persist?.().catch(() => false);
    const repl = await dusk.bootRepl(() => {}, {
      skipPidZero: true, fs: 'tfs', user: 'user', hostname: HOSTNAME, net: networkOptions() as never,
    });
    await repl.processManager.spawnSync(SHELL, ['-c', `mkdir -p ${WORKSPACE_ROOT} /etc && echo ${HOSTNAME} > /etc/hostname`], { cwd: HOME, env: ENV });
    registerExtraCommands(repl.processManager);
    setPhase('ready');
    return repl.processManager;
  })().catch((error: unknown) => {
    booting = undefined;
    setPhase('error', error instanceof Error ? error.message : String(error));
    throw error;
  });
  return booting;
}

/**
 * Extra shell commands registered after boot. Most coreutils (grep, head, tail, sort, cp, mv, etc.) are already
 * built into dsh and do NOT need registerBinary. Only commands dsh lacks go here. Each source is a single JS
 * program the engine runs directly (not through the shell). Ported 1:1 from the abulxt workspace commands/.
 */
function registerExtraCommands(pm: ProcessManager): void {
  // Process commands. The engine passes [node, scriptPath, ...args], so arguments start at index 2.
  // The process list comes from the host (process.list); signals go through process.kill.
  const procLib = [
    'var SIGNALS={1:"SIGHUP",2:"SIGINT",3:"SIGQUIT",9:"SIGKILL",15:"SIGTERM",18:"SIGCONT",19:"SIGSTOP",20:"SIGTSTP"};',
    'function listProcs(){var r=ipc.send({f:"process.list"});var l=r&&r.value!==undefined?r.value:r;return (l||[]).slice().sort(function(a,b){return a.pid-b.pid});}',
    'function sigName(s){s=String(s).toUpperCase();if(s==="0")return "SIG0";if(/^[0-9]+$/.test(s))return SIGNALS[s]||s;return s.indexOf("SIG")===0?s:"SIG"+s;}',
    'function killPid(pid,sig){var r=ipc.send({f:"process.kill",pid:pid,signal:sig});if(r&&r.error){process.stderr.write("kill: ("+pid+") "+r.error+"\\n");return false;}return true;}',
  ].join('\n');
  pm.registerBinary('/bin/ps', [
    '#!/bin/node',
    procLib,
    'var rows=listProcs();process.stdout.write("  PID  PPID  PGID S COMMAND\\n");',
    'for(var i=0;i<rows.length;i++){var p=rows[i];var cmd=(p.argv||[]).slice(1).join(" ")||"-";process.stdout.write(String(p.pid).padStart(5)+String(p.ppid).padStart(6)+String(p.pgid).padStart(6)+" "+(p.state||"R")+" "+cmd+"\\n");}',
  ].join('\n'));
  pm.registerBinary('/bin/top', [
    '#!/bin/node',
    procLib,
    'process.stdout.write("Dusk top: a snapshot. Run top again to refresh.\\n");',
    'var rows=listProcs();process.stdout.write(rows.length+" processes\\n  PID  PPID  PGID S COMMAND\\n");',
    'for(var i=0;i<rows.length;i++){var p=rows[i];process.stdout.write(String(p.pid).padStart(5)+String(p.ppid).padStart(6)+String(p.pgid).padStart(6)+" "+(p.state||"R")+" "+((p.argv||[]).slice(1).join(" ")||"-")+"\\n");}',
  ].join('\n'));
  pm.registerBinary('/bin/kill', [
    '#!/bin/node',
    procLib,
    'var args=process.argv.slice(2),sig="SIGTERM";',
    'if(args[0]==="-l"){process.stdout.write("HUP INT QUIT KILL TERM CONT STOP TSTP TTIN TTOU\\n");process.exit(0);}',
    'if(args[0]==="-s"||args[0]==="--signal"){if(args.length<2){process.stderr.write("kill: missing signal argument\\n");process.exit(2);}args.shift();sig=sigName(args.shift());}',
    'else if(args.length&&/^-[A-Za-z0-9]+$/.test(args[0]))sig=sigName(args.shift().slice(1));',
    'if(args[0]==="--")args.shift();',
    'if(!args.length){process.stderr.write("usage: kill [-SIGNAL|-s SIGNAL] PID...\\n");process.exit(2);}',
    'var ok=true;for(var i=0;i<args.length;i++){var pid=Number(args[i]);if(!Number.isSafeInteger(pid)){process.stderr.write("kill: "+args[i]+": arguments must be process IDs\\n");ok=false;continue;}if(!killPid(pid,sig))ok=false;}',
    'process.exit(ok?0:1);',
  ].join('\n'));
  pm.registerBinary('/bin/pkill', [
    '#!/bin/node',
    procLib,
    'var args=process.argv.slice(2);var sig="SIGTERM";if(args.length&&args[0].charAt(0)==="-"){sig=sigName(args.shift().slice(1));}',
    'if(!args.length){process.stderr.write("usage: pkill [-SIGNAL] PATTERN\\n");process.exit(2);}',
    'var re=new RegExp(args[args.length-1]);var hits=listProcs().filter(function(p){return p.argv&&p.argv.length>1&&re.test(p.argv.slice(1).join(" "));});',
    'if(!hits.length)process.exit(1);var ok=true;for(var i=0;i<hits.length;i++){if(!killPid(hits[i].pid,sig))ok=false;}',
    'process.exit(ok?0:1);',
  ].join('\n'));
  // Only the lightweight process core is eager. Command families load on first invocation, so the compiled
  // app entry chunk does not carry their source bodies.
  const families = {
    core: ['yes', 'realpath', 'mktemp', 'cmp', 'sha256sum', 'xxd'],
    archives: ['gzip', 'gunzip', 'tar', 'zip', 'unzip'],
    build: ['make', 'patch'],
    git: ['git'],
    sshKeys: ['ssh-keygen'],
  } as const;
  for (const command of families.core) {
    pm.registerLazyBinary(`/bin/${command}`, () => import('./commands/core').then((module) => module.sources[command]));
  }
  for (const command of families.archives) {
    pm.registerLazyBinary(`/bin/${command}`, () => import('./commands/archives').then((module) => module.sources[command]));
  }
  for (const command of families.build) {
    pm.registerLazyBinary(`/bin/${command}`, () => import('./commands/build').then((module) => module.sources[command]));
  }
  for (const command of families.git) {
    pm.registerLazyBinary(`/bin/${command}`, () => import('./commands/git').then((module) => module.sources[command]));
  }
  for (const command of families.sshKeys) {
    pm.registerLazyBinary(`/bin/${command}`, () => import('./commands/ssh-keys').then((module) => module.sources[command]));
  }
  // uname: not in dsh. The #!/bin/node shebang tells dsh to run it through the JS engine.
  pm.registerBinary('/bin/uname', '#!/bin/node\nvar args=process.argv.filter(function(a){return a.charAt(0)==="-"});var s="Dusk",n=process.env.HOSTNAME||"vm",r="1.0.0",v="Kali Workspace",m="wasm32";if(!args.length){process.stdout.write(s+"\\n")}else{var o=[];for(var i=0;i<args.length;i++){var a=args[i];if(a==="-s")o.push(s);else if(a==="-n")o.push(n);else if(a==="-r")o.push(r);else if(a==="-v")o.push(v);else if(a==="-m")o.push(m);else if(a==="-a"){o.push(s,n,r,v,m)}else{process.stderr.write("uname: unknown option "+a+"\\n");process.exit(1)}}process.stdout.write(o.join(" ")+"\\n")}');
  // id: not in dsh
  pm.registerBinary('/bin/id', '#!/bin/node\nprocess.stdout.write("uid=1000("+(process.env.USER||"user")+") gid=1000("+(process.env.USER||"user")+")\\n");');
}

// ---- filesystem ---------------------------------------------------------------

interface Fs {
  readFileBytes(path: string): Promise<Uint8Array>;
  writeFileBytes(path: string, data: Uint8Array): Promise<void>;
  readdir(path: string): Promise<string[]>;
  mkdir(path: string, opts?: { recursive?: boolean }): Promise<void>;
  rm(path: string, opts?: { recursive?: boolean }): Promise<void>;
  exists(path: string): Promise<boolean>;
  stat(path: string): Promise<{ isFile: boolean; isDirectory: boolean; size: number; mtimeMs?: number }>;
  rename(from: string, to: string): Promise<void>;
}

async function fsApi(): Promise<Fs> {
  const pm = await manager();
  const fs = (pm as unknown as { fs?: Fs }).fs;
  if (!fs) throw new WorkspaceUnavailableError('The workspace filesystem is not available.');
  return fs;
}

const encoder = new TextEncoder();
const fileDecoder = new TextDecoder();

/** An absolute path inside the home folder. Relative paths start in the workspace; "~" is home. */
export function resolvePath(input: unknown, base = WORKSPACE_ROOT): string {
  const raw = typeof input === 'string' ? input.trim().replace(/^~(?=\/|$)/, HOME) : '';
  const joined = raw === '' ? base : raw.startsWith('/') ? raw : `${base}/${raw}`;
  const parts: string[] = [];
  for (const part of joined.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') { if (!parts.length) throw new Error('The path leaves the home folder.'); parts.pop(); continue; }
    parts.push(part);
  }
  const out = `/${parts.join('/')}`;
  if (out !== HOME && !out.startsWith(`${HOME}/`)) throw new Error(`Paths must stay inside ${HOME}.`);
  return out;
}

export function displayPath(path: string): string {
  if (path === HOME) return '~';
  if (path.startsWith(`${HOME}/`)) return `~${path.slice(HOME.length)}`;
  return path;
}

export function parentOf(path: string): string {
  const cut = path.lastIndexOf('/');
  return cut <= 0 ? '/' : path.slice(0, cut);
}

export async function exists(path: string): Promise<boolean> {
  return (await fsApi()).exists(path);
}

export async function stat(path: string) {
  return (await fsApi()).stat(path);
}

export async function isDirectory(path: string): Promise<boolean> {
  const fs = await fsApi();
  if (!(await fs.exists(path))) return false;
  return (await fs.stat(path)).isDirectory;
}

export async function makeDirs(path: string): Promise<void> {
  if (path === '/' || (await exists(path))) return;
  await makeDirs(parentOf(path));
  await (await fsApi()).mkdir(path);
}

export async function readBytes(path: string): Promise<Uint8Array> {
  return (await fsApi()).readFileBytes(path);
}

export async function readText(path: string): Promise<string> {
  return fileDecoder.decode(await readBytes(path));
}

export async function writeBytes(path: string, data: Uint8Array): Promise<void> {
  await makeDirs(parentOf(path));
  await (await fsApi()).writeFileBytes(path, data);
}

export async function writeText(path: string, text: string): Promise<void> {
  await writeBytes(path, encoder.encode(text));
}

export async function removePath(path: string): Promise<void> {
  await (await fsApi()).rm(path, { recursive: true });
}

export async function movePath(from: string, to: string): Promise<void> {
  await makeDirs(parentOf(to));
  await (await fsApi()).rename(from, to);
}

export interface FsEntry {
  name: string;
  path: string;
  isDirectory: boolean;
  size: number;
}

/** The entries of one folder, folders first. */
export async function listEntries(path: string): Promise<FsEntry[]> {
  const fs = await fsApi();
  const names = await fs.readdir(path);
  const entries: FsEntry[] = [];
  for (const raw of names) {
    const name = typeof raw === 'string' ? raw : (raw as { name?: string })?.name;
    if (!name || name === '.' || name === '..') continue;
    const full = `${path === '/' ? '' : path}/${name}`;
    try {
      const info = await fs.stat(full);
      entries.push({ name, path: full, isDirectory: info.isDirectory, size: info.size ?? 0 });
    } catch {
      entries.push({ name, path: full, isDirectory: false, size: 0 });
    }
  }
  return entries.sort((a, b) => Number(b.isDirectory) - Number(a.isDirectory) || a.name.localeCompare(b.name));
}

/** Every file under a folder, skipping dot-folders and node_modules. Stops at `limit`. */
export async function walkFiles(root: string, limit: number): Promise<FsEntry[]> {
  const out: FsEntry[] = [];
  const queue = [root];
  while (queue.length && out.length < limit) {
    const dir = queue.shift()!;
    for (const entry of await listEntries(dir)) {
      if (entry.isDirectory) {
        if (!entry.name.startsWith('.') && entry.name !== 'node_modules') queue.push(entry.path);
      } else {
        out.push(entry);
        if (out.length >= limit) break;
      }
    }
  }
  return out;
}

export function looksBinary(bytes: Uint8Array): boolean {
  const end = Math.min(bytes.length, 8000);
  for (let i = 0; i < end; i++) if (bytes[i] === 0) return true;
  return false;
}

// ---- one-shot commands (tools) -------------------------------------------------

export interface CommandResult {
  output: string;
  exitCode: number | null;
  timedOut: boolean;
  aborted: boolean;
}

async function drain(stream: ReadableStream<Uint8Array>, sink: (text: string) => void): Promise<void> {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (value?.length) sink(decoder.decode(value, { stream: true }));
  }
  const tail = decoder.decode();
  if (tail) sink(tail);
}

/** Runs one command line through `dsh -c` and collects its output, streaming chunks to `onOutput`. */
export async function runCommand(command: string, cwd: string, opts: { timeoutMs?: number; onOutput?: (chunk: string) => void; signal?: AbortSignal } = {}): Promise<CommandResult> {
  const pm = await manager();
  const handle: ProcessHandle = await pm.spawn(SHELL, ['-c', command], { cwd, env: { ...ENV, PWD: cwd } });
  let output = '';
  let timedOut = false;
  let aborted = false;
  const sink = (text: string) => {
    if (output.length < OUTPUT_KEEP) output += text;
    opts.onOutput?.(text);
  };
  const timer = setTimeout(() => { timedOut = true; handle.kill(); }, opts.timeoutMs ?? 120_000);
  const onAbort = () => { aborted = true; handle.kill(); };
  if (opts.signal?.aborted) onAbort();
  opts.signal?.addEventListener('abort', onAbort, { once: true });
  try {
    const [, , exitCode] = await Promise.all([drain(handle.stdout, sink), drain(handle.stderr, sink), handle.exit]);
    return { output, exitCode: timedOut || aborted ? null : exitCode, timedOut, aborted };
  } finally {
    clearTimeout(timer);
    opts.signal?.removeEventListener('abort', onAbort);
  }
}

// ---- interactive shells (the Shell tab) ----------------------------------------

export interface ShellInfo {
  id: string;
  name: string;
  exited: boolean;
}

export interface ShellSession {
  write(text: string): void;
  onData(listener: (text: string) => void): () => void;
  resize(cols: number, rows: number): void;
  scrollback(): string;
}

interface PtyMaster {
  masterWrite?(bytes: Uint8Array): void;
  write?(bytes: Uint8Array): void;
  onMasterData(cb: (bytes: Uint8Array) => void): unknown;
  resize?(cols: number, rows: number): void;
}

interface ShellRecord extends ShellInfo {
  pid: number;
  handle: ProcessHandle;
  master: PtyMaster;
  scrollback: string;
  decoder: TextDecoder;
  listeners: Set<(text: string) => void>;
}

const SCROLLBACK_KEEP = 300_000;
const shells = new Map<string, ShellRecord>();
const shellListeners = new Set<() => void>();
let shellCount = 0;

function notifyShells() {
  for (const l of shellListeners) l();
}

export function listShells(): ShellInfo[] {
  return [...shells.values()].map(({ id, name, exited }) => ({ id, name, exited }));
}

export function onShellsChanged(listener: () => void): () => void {
  shellListeners.add(listener);
  return () => { shellListeners.delete(listener); };
}

/** Opens an interactive shell in the workspace folder and returns its id. It keeps running while the pane is closed. */
export async function createShell(cols: number, rows: number): Promise<string> {
  const pm = await manager();
  const handle = await pm.spawn(SHELL, [], { cwd: WORKSPACE_ROOT, env: { ...ENV, PWD: WORKSPACE_ROOT }, pty: { cols, rows } });
  const master = (handle as unknown as { master?: PtyMaster }).master;
  if (!master) { handle.kill(); throw new WorkspaceUnavailableError('The shell did not open a terminal.'); }
  shellCount += 1;
  const record: ShellRecord = {
    id: crypto.randomUUID(), name: `dsh ${shellCount}`, exited: false, pid: handle.pid, handle, master,
    scrollback: '', decoder: new TextDecoder(), listeners: new Set(),
  };
  master.onMasterData((bytes: Uint8Array) => {
    const text = record.decoder.decode(bytes, { stream: true });
    record.scrollback = (record.scrollback + text).slice(-SCROLLBACK_KEEP);
    for (const l of record.listeners) l(text);
  });
  void handle.exit.then(() => {
    record.exited = true;
    const bye = '\r\n[shell exited]\r\n';
    record.scrollback += bye;
    for (const l of record.listeners) l(bye);
    notifyShells();
  });
  shells.set(record.id, record);
  notifyShells();
  return record.id;
}

export function openShellSession(id: string): ShellSession | null {
  const record = shells.get(id);
  if (!record) return null;
  const pm = (record.handle as unknown as { processManager?: unknown }).processManager;
  return {
    write: (text) => {
      if (record.exited) return;
      const bytes = encoder.encode(text);
      if (record.master.masterWrite) record.master.masterWrite(bytes);
      else if (record.master.write) record.master.write(bytes);
      else void record.handle.stdin.write(bytes);
    },
    onData: (listener) => {
      record.listeners.add(listener);
      return () => { record.listeners.delete(listener); };
    },
    resize: (cols, rows) => {
      if (record.exited) return;
      if (record.master.resize) record.master.resize(cols, rows);
      else void manager().then((m) => (m as unknown as { resizePty?: (pid: number, c: number, r: number) => void }).resizePty?.(record.pid, cols, rows));
      void pm;
    },
    scrollback: () => record.scrollback,
  };
}

export function closeShell(id: string): void {
  const record = shells.get(id);
  if (!record) return;
  if (!record.exited) record.handle.kill();
  record.listeners.clear();
  shells.delete(id);
  notifyShells();
}
