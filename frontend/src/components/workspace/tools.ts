// The workspace tools the model can call. Each runs in this tab against the
// Dusk workspace and returns text the model reads.
import type { ExecuteOptions, ToolCall, ToolDef, ToolResult } from '../../lib/workspace-contract';
import {
  WORKSPACE_ROOT, displayPath, exists, isDirectory, listEntries, looksBinary, readBytes, removePath,
  resolvePath, runCommand, walkFiles, writeText,
} from './dusk';

const OUTPUT_LIMIT = 16_000;
const SHELL_TIMEOUT_MS = 120_000;

type Args = Record<string, unknown>;

export const TOOL_DEFS: ToolDef[] = [
  {
    type: 'function',
    function: {
      name: 'run_shell',
      description: `Run a shell command in the user's browser workspace (a POSIX shell with coreutils, node, python3, curl, jq, git-less). The workspace folder is ${WORKSPACE_ROOT}; files persist between calls. Returns stdout+stderr and the exit code. Commands are killed after 120 seconds, so start long-running servers in the background.`,
      parameters: {
        type: 'object',
        properties: {
          command: { type: 'string', description: 'The command line to run with dsh -c.' },
          cwd: { type: 'string', description: 'Working directory. Relative paths resolve from the workspace folder. Default: the workspace folder.' },
        },
        required: ['command'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'read_file',
      description: 'Read a text file from the workspace. Optionally a line range (1-based, inclusive). Large files are truncated.',
      parameters: {
        type: 'object',
        properties: {
          path: { type: 'string', description: 'File path, absolute or relative to the workspace folder.' },
          start_line: { type: 'integer', description: 'First line to return (1-based).' },
          end_line: { type: 'integer', description: 'Last line to return (inclusive).' },
        },
        required: ['path'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'write_file',
      description: 'Create or overwrite a text file in the workspace, creating parent folders as needed.',
      parameters: {
        type: 'object',
        properties: {
          path: { type: 'string', description: 'File path, absolute or relative to the workspace folder.' },
          content: { type: 'string', description: 'The complete file contents.' },
        },
        required: ['path', 'content'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'list_dir',
      description: 'List the entries of a workspace folder. Folders end with "/".',
      parameters: {
        type: 'object',
        properties: { path: { type: 'string', description: 'Folder path. Default: the workspace folder.' } },
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'delete_path',
      description: 'Delete a file or folder (recursively) from the workspace.',
      parameters: {
        type: 'object',
        properties: { path: { type: 'string', description: 'Path to delete.' } },
        required: ['path'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'search_files',
      description: 'Search file contents under a folder for a pattern (regular expression, case-insensitive). Returns path:line: text matches. Skips dot-folders, node_modules and binary files.',
      parameters: {
        type: 'object',
        properties: {
          pattern: { type: 'string', description: 'Regular expression to search for.' },
          path: { type: 'string', description: 'Folder to search. Default: the workspace folder.' },
          glob: { type: 'string', description: 'Only search files whose workspace-relative path matches this glob (e.g. "**/*.ts").' },
          max_results: { type: 'integer', description: 'Maximum matches to return. Default 100.' },
        },
        required: ['pattern'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'find_files',
      description: 'Find files by name under a folder using a glob (** crosses folders, * within a name).',
      parameters: {
        type: 'object',
        properties: {
          glob: { type: 'string', description: 'Glob matched against the path relative to the search folder, e.g. "src/**/*.go".' },
          path: { type: 'string', description: 'Folder to search. Default: the workspace folder.' },
        },
        required: ['glob'],
      },
    },
  },
];

function str(value: unknown, field: string): string {
  if (typeof value !== 'string' || !value.length) throw new Error(`${field} is required.`);
  return value;
}

function clip(text: string): string {
  return text.length > OUTPUT_LIMIT ? `${text.slice(0, OUTPUT_LIMIT)}\n...[truncated at ${OUTPUT_LIMIT} characters]` : text;
}

export function globToRegExp(pattern: string): RegExp {
  let source = '';
  let inChoice = false;
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i];
    if (ch === '*' && pattern[i + 1] === '*') {
      const slash = pattern[i + 2] === '/';
      source += slash ? '(?:.*/)?' : '.*';
      i += slash ? 2 : 1;
    } else if (ch === '*') source += '[^/]*';
    else if (ch === '?') source += '[^/]';
    else if (ch === '{') { source += '(?:'; inChoice = true; }
    else if (ch === '}' && inChoice) { source += ')'; inChoice = false; }
    else if (ch === ',' && inChoice) source += '|';
    else source += ch.replace(/[.+^${}()|[\]\\]/g, '\\$&');
  }
  return new RegExp(`^${source}$`);
}

async function runShell(args: Args, opts: ExecuteOptions): Promise<ToolResult> {
  const command = str(args.command, 'command');
  const cwd = resolvePath(args.cwd);
  if (!(await isDirectory(cwd))) throw new Error(`${displayPath(cwd)} is not a folder.`);
  const run = await runCommand(command, cwd, { timeoutMs: SHELL_TIMEOUT_MS, onOutput: opts.onOutput, signal: opts.signal });
  const status = run.aborted ? 'stopped by the user' : run.timedOut ? 'killed: timed out after 120s' : `exit code ${run.exitCode}`;
  const body = run.output.replace(/\r\n/g, '\n').replace(/\n+$/, '');
  return {
    ok: run.exitCode === 0,
    exitCode: run.exitCode ?? undefined,
    output: clip(`$ ${command}\n${body || '(no output)'}\n[${status}]`),
  };
}

async function readFile(args: Args): Promise<ToolResult> {
  const path = resolvePath(str(args.path, 'path'));
  if (!(await exists(path))) throw new Error(`${displayPath(path)} does not exist.`);
  if (await isDirectory(path)) throw new Error(`${displayPath(path)} is a folder. Use list_dir.`);
  const bytes = await readBytes(path);
  if (looksBinary(bytes)) throw new Error(`${displayPath(path)} is a binary file (${bytes.length} bytes).`);
  const lines = new TextDecoder().decode(bytes).split('\n');
  const start = typeof args.start_line === 'number' ? Math.max(1, Math.floor(args.start_line)) : 1;
  const end = typeof args.end_line === 'number' ? Math.min(lines.length, Math.floor(args.end_line)) : lines.length;
  if (end < start) throw new Error(`end_line ${end} is before start_line ${start}.`);
  const slice = lines.slice(start - 1, end).map((line, i) => `${String(start + i).padStart(5)}  ${line}`).join('\n');
  return { ok: true, output: clip(`${displayPath(path)} (lines ${start}-${end} of ${lines.length})\n${slice}`) };
}

async function writeFile(args: Args): Promise<ToolResult> {
  const path = resolvePath(str(args.path, 'path'));
  if (typeof args.content !== 'string') throw new Error('content is required.');
  await writeText(path, args.content);
  return { ok: true, output: `Wrote ${new TextEncoder().encode(args.content).length} bytes to ${displayPath(path)}.` };
}

async function listDir(args: Args): Promise<ToolResult> {
  const path = resolvePath(args.path);
  if (!(await exists(path))) throw new Error(`${displayPath(path)} does not exist.`);
  if (!(await isDirectory(path))) throw new Error(`${displayPath(path)} is a file.`);
  const entries = await listEntries(path);
  if (!entries.length) return { ok: true, output: `${displayPath(path)} is empty.` };
  const rows = entries.slice(0, 500).map((e) => (e.isDirectory ? `${e.name}/` : `${e.name}  (${e.size} bytes)`));
  return { ok: true, output: clip(`${displayPath(path)}:\n${rows.join('\n')}`) };
}

async function deletePath(args: Args): Promise<ToolResult> {
  const path = resolvePath(str(args.path, 'path'));
  if (path === WORKSPACE_ROOT) throw new Error('Refusing to delete the workspace folder itself.');
  if (!(await exists(path))) throw new Error(`${displayPath(path)} does not exist.`);
  await removePath(path);
  return { ok: true, output: `Deleted ${displayPath(path)}.` };
}

async function searchFiles(args: Args): Promise<ToolResult> {
  const pattern = str(args.pattern, 'pattern');
  let matcher: RegExp;
  try { matcher = new RegExp(pattern, 'i'); } catch { throw new Error('pattern is not a valid regular expression.'); }
  const root = resolvePath(args.path);
  const include = typeof args.glob === 'string' && args.glob ? globToRegExp(args.glob) : null;
  const limit = typeof args.max_results === 'number' && args.max_results > 0 ? Math.min(args.max_results, 1000) : 100;
  const hits: string[] = [];
  for (const file of await walkFiles(root, 3000)) {
    const rel = file.path.slice(root.length + 1);
    if (include && !include.test(rel)) continue;
    if (file.size > 1_000_000) continue;
    const bytes = await readBytes(file.path);
    if (looksBinary(bytes)) continue;
    const lines = new TextDecoder().decode(bytes).split('\n');
    for (let i = 0; i < lines.length && hits.length < limit; i++) {
      if (matcher.test(lines[i])) hits.push(`${rel}:${i + 1}: ${lines[i].trim().slice(0, 300)}`);
    }
    if (hits.length >= limit) break;
  }
  if (!hits.length) return { ok: true, output: `No matches for /${pattern}/ under ${displayPath(root)}.` };
  return { ok: true, output: clip(`${hits.length}${hits.length >= limit ? '+' : ''} matches:\n${hits.join('\n')}`) };
}

async function findFiles(args: Args): Promise<ToolResult> {
  const glob = str(args.glob, 'glob');
  const root = resolvePath(args.path);
  const matcher = globToRegExp(glob);
  const found = (await walkFiles(root, 5000)).map((f) => f.path.slice(root.length + 1)).filter((rel) => matcher.test(rel));
  if (!found.length) return { ok: true, output: `No files match ${glob} under ${displayPath(root)}.` };
  return { ok: true, output: clip(`${found.length} files:\n${found.slice(0, 500).join('\n')}`) };
}

const HANDLERS: Record<string, (args: Args, opts: ExecuteOptions) => Promise<ToolResult>> = {
  run_shell: runShell,
  read_file: readFile,
  write_file: writeFile,
  list_dir: listDir,
  delete_path: deletePath,
  search_files: searchFiles,
  find_files: findFiles,
};

export async function executeTool(call: ToolCall, opts: ExecuteOptions = {}): Promise<ToolResult> {
  const handler = HANDLERS[call.name];
  if (!handler) return { ok: false, output: `Unknown workspace tool: ${call.name}` };
  let args: Args = {};
  try {
    const parsed = JSON.parse(call.arguments || '{}');
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) args = parsed as Args;
  } catch {
    return { ok: false, output: 'The tool arguments were not valid JSON.' };
  }
  try {
    return await handler(args, opts);
  } catch (error) {
    return { ok: false, output: `Error: ${error instanceof Error ? error.message : String(error)}` };
  }
}
