/** Source shared by lazy in-VM binaries. Nothing in this module is executed in the host page. */
export const runtime = String.raw`
var fs = __fs;
var argv = process.argv.slice(2);
var enc = new TextEncoder();
var dec = new TextDecoder();
// The Dusk Node engine's process.exit() does not stop the script, so a reported failure throws and every program body
// runs inside a try block (see script()). finish() reports anything else once.
function fail(s, status) { process.stderr.write(String(s)+'\n'); var e = new Error(String(s)); e.reported = true; e.status = status || 1; throw e; }
// Any thrown value becomes one line of text. Plain objects such as {code, message} from the host never print "[object Object]".
function errText(e) { if (typeof e === 'string' && e) return e; if (e && typeof e.message === 'string' && e.message) return e.message; if (e && typeof e.code === 'string') return e.code; return 'unexpected error'; }
function finish(e) { process.exitCode = (e && e.status) || 1; if (e && e.reported) return; process.stderr.write(base(process.argv[1] || 'node') + ': ' + errText(e) + '\n'); }
function cwd() { return process.cwd ? process.cwd() : (process.env.PWD || '/home/user'); }
function pathOf(p) { p=String(p||'.'); if(p.charAt(0)==='~')p=(process.env.HOME||'/home/user')+p.slice(1); var parts=(p.charAt(0)==='/'?p:cwd()+'/'+p).split('/');var a=[];for(var i=0;i<parts.length;i++){if(!parts[i]||parts[i]==='.')continue;if(parts[i]==='..'){a.pop();continue;}a.push(parts[i]);}return '/'+a.join('/'); }
function dirname(p){return p.substring(0,p.lastIndexOf('/'))||'/';}
function exists(p){try{return Boolean(fs.exists(pathOf(p)));}catch(e){return false;}}
function bytes(p){var b=fs.readFile(pathOf(p));if(typeof b==='string')return enc.encode(b);if(b instanceof Uint8Array)return b;return new Uint8Array(b);}
function read(p){return dec.decode(bytes(p));}
function mkdirp(p){p=pathOf(p);if(exists(p))return;mkdirp(dirname(p));fs.mkdir(p);}
function write(p,b){p=pathOf(p);mkdirp(dirname(p));fs.writeFile(p,typeof b==='string'?enc.encode(b):b);}
function names(p){return fs.readdir(pathOf(p));}
function readInput(){var out=[];for(;;){var r=ipc.send({f:'proc.readStdin'});var d=r&&r.value!==undefined?r.value:r;if(d===null||d===undefined||d.length===0)break;out.push(typeof d==='string'?enc.encode(d):new Uint8Array(d));}return joinBytes(out);}
function joinBytes(chunks){var n=0;for(var i=0;i<chunks.length;i++)n+=chunks[i].length;var o=new Uint8Array(n);var at=0;for(var j=0;j<chunks.length;j++){o.set(chunks[j],at);at+=chunks[j].length;}return o;}
function emit(b){process.stdout.write(typeof b==='string'?b:b);}
function hex(b){return Array.prototype.map.call(b,function(x){return x.toString(16).padStart(2,'0')}).join('');}
function base(p){return p.split('/').pop()||'.';}
function u32(x){return x>>>0;}
function crc32(a){var c=-1;for(var i=0;i<a.length;i++){c^=a[i];for(var j=0;j<8;j++)c=(c>>>1)^((c&1)?0xedb88320:0);}return(c^-1)>>>0;}
function safeTarget(name,root){if(!name||name.charAt(0)==='/'||name.indexOf('\\')>=0||name.indexOf('\0')>=0)fail('unsafe archive path: '+name);var p=pathOf(root+'/'+name);var dir=pathOf(root);if(dir!=='/home/user/workspace'&&dir.indexOf('/home/user/workspace/')!==0)fail('archive extraction must stay in the workspace');if(p!==dir&&p.indexOf(dir+'/')!==0)fail('archive escapes extraction root: '+name);return p;}
`;

/** Build plain shebang scripts; no multi-line source template at runtime. */
// The status goes to the host as an exit message, the same one the worker sends when a program ends with a code. It is
// sent from the finally block, so an early return, a failure or a normal end all report the same status to the shell.
export function script(body: string): string { return '#!/bin/node\n' + runtime + '\n(function () { try {\n' + body + '\n} catch (e) { finish(e) } finally { ipc.send({ type: "exit", exitCode: process.exitCode || 0 }) } })()\n' }
