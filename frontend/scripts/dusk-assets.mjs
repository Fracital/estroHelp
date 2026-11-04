// Copies the Dusk runtime (and Nova, its HTTP/TLS engine) into public/dusk so
// the page can load it with a plain module script tag. Dusk ships a prebuilt
// worker/chunk graph that resolves siblings through import.meta.url, so it is
// served as-is rather than bundled. Runs before `astro dev` and `astro build`.
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const duskLib = join(root, 'node_modules/@nightnetwork/dusk/lib');
const novaDir = join(root, 'node_modules/@nightnetwork/nova');
const target = join(root, 'public/dusk');

if (!existsSync(duskLib)) {
  console.error('dusk-assets: @nightnetwork/dusk is not installed');
  process.exit(1);
}

const stamp = join(target, '.version');
const version = JSON.parse(readFileSync(join(root, 'node_modules/@nightnetwork/dusk/package.json'), 'utf8')).version;
if (existsSync(stamp) && readFileSync(stamp, 'utf8') === version && existsSync(join(target, 'loader.js'))) {
  process.exit(0);
}

rmSync(target, { recursive: true, force: true });
mkdirSync(target, { recursive: true });
cpSync(duskLib, target, {
  recursive: true,
  filter: (file) => !file.endsWith('.map') && relative(duskLib, file).split('/')[0] !== 'types',
});

// The shell prompt is hard-coded as "dsh$ ". Show the working directory
// instead ("~" at home). Skipped silently if the bundle changes shape.
const bundle = join(target, 'duskjs.js');
let code = readFileSync(bundle, 'utf8');
const PROMPT_WRITE = 'stdout.write("dsh$ ");';
const PROMPT_CODE = 'stdout.write((function(){var h=(process.env.HOME||""),c=(globalThis.__dshCwd||process.env.PWD||process.cwd()),d=h&&c===h?"~":h&&c.indexOf(h+"/")===0?"~"+c.slice(h.length):c;return (process.env.DSH_PROMPT||"dsh:")+d+"$ "})());';
const CD_WRITE = 'ctx.state.env.set("PWD",ctx.state.cwd),';
const CD_CODE = 'ctx.state.env.set("PWD",ctx.state.cwd),(globalThis.__dshCwd=ctx.state.cwd),';
if (code.split(PROMPT_WRITE).length === 2 && code.split(CD_WRITE).length === 2) {
  code = code.replace(PROMPT_WRITE, PROMPT_CODE).replace(CD_WRITE, CD_CODE);
  writeFileSync(bundle, code);
} else {
  console.warn('dusk-assets: prompt patch anchors not found; leaving the default prompt');
}

writeFileSync(join(target, 'loader.js'), "import * as dusk from './duskjs.js';\nwindow.kaliDusk = dusk;\n");

if (existsSync(novaDir)) {
  mkdirSync(join(target, 'nova'), { recursive: true });
  for (const name of ['nova_wasm.js', 'nova_wasm_bg.wasm']) {
    cpSync(join(novaDir, name), join(target, 'nova', name));
  }
} else {
  console.warn('dusk-assets: @nightnetwork/nova missing; workspace networking will be unavailable');
}

writeFileSync(stamp, version);
const size = (dir) => readdirSync(dir).reduce((sum, name) => {
  const p = join(dir, name);
  return sum + (statSync(p).isDirectory() ? size(p) : statSync(p).size);
}, 0);
console.log(`dusk-assets: copied Dusk ${version} to public/dusk (${(size(target) / 1e6).toFixed(1)} MB)`);
