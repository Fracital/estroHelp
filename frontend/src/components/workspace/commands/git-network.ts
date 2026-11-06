/** Additional Git smart-HTTP implementation, executed inside the Dusk Node engine.
 * The only network primitive is the already installed /bin/curl (libcurl over the gated Wisp relay).
 * No browser fetch(), XMLHttpRequest or other network escape paths are allowed here.
 */
export const gitNetwork = String.raw`
var REMOTE_MAX=48*1024*1024,UNPACK_MAX=192*1024*1024,PACK_OBJECTS=12000;
function pkt(s){var b=typeof s==='string'?te.encode(s):s;var n=b.length+4;if(n>65520)fail('git: packet too long');var h=n.toString(16).padStart(4,'0');return concat(te.encode(h),b);}
function unpackPkt(b,p){var at=p||0,out=[];while(at+4<=b.length){var n=parseInt(td.decode(b.slice(at,at+4)),16);if(!Number.isFinite(n))fail('git: invalid packet header');at+=4;if(n===0)return {lines:out,offset:at,flush:true};if(n<4||at+n-4>b.length)fail('git: truncated Git packet');out.push(b.slice(at,at+n-4));at+=n-4;}return {lines:out,offset:at,flush:false};}
// The Dusk Node engine has no URL global, so HTTPS remotes are split here. Only plain DNS hosts on port 443 pass.
function httpsParts(url){var m=/^https:\/\/([^\/?#]+)([^?#]*)/i.exec(url);if(!m)fail('git: only HTTPS URLs are supported');var host=m[1],port='',colon=host.lastIndexOf(':');if(colon>=0){port=host.slice(colon+1);host=host.slice(0,colon);}if(!host||/[\[\]@]/.test(host))fail('git: remote URL must name a DNS host');if(port&&port!=='443')fail('git: remote URL must use standard HTTPS');return {hostname:host.toLowerCase(),pathname:m[2]||'/'};}
function authHeader(url){var host=httpsParts(url).hostname;var config=pathOf((process.env.HOME||'/home/user')+'/.config/git/credentials');if(!exists(config))return '';var lines=gread(config).split(/\r?\n/);for(var line of lines){var part=line.trim().split(' ');if(part.length===3&&part[0]===host&&part[1]&&part[2]){var basic=te.encode(part[1]+':'+part[2]);var alphabet='ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/',out='';for(var i=0;i<basic.length;i+=3){var n=(basic[i]<<16)|((basic[i+1]||0)<<8)|(basic[i+2]||0);out+=alphabet[n>>>18&63]+alphabet[n>>>12&63]+(i+1<basic.length?alphabet[n>>>6&63]:'=')+(i+2<basic.length?alphabet[n&63]:'=');}return 'Authorization: Basic '+out;}}return '';}
// process.spawnSync resolves to {stdout, stderr, status}. A missing status is a transport failure, never success.
// Host errors arrive as objects, strings or {message}. Every git failure text must be a plain string.
function errText(e,depth){depth=depth||0;if(e===null||e===undefined)return 'unknown error';if(typeof e==='string')return e;if(depth<3&&e.message!==undefined)return errText(e.message,depth+1);if(depth<3&&e.error!==undefined)return errText(e.error,depth+1);try{var s=JSON.stringify(e);return s===undefined?'unknown error':s;}catch(x){return 'unknown error';}}
// curl writes only errors to stderr (-sS), so any stderr text is a failure even if the exit status reads 0.
function curlStatus(result){if(!result)fail('git: HTTP transport: no response');if(result.error!==undefined&&result.error!==null)fail('git: HTTP transport: '+errText(result.error));var v=result.value||{};if(typeof v.status!=='number')fail('git: HTTP transport: no exit status');var raw=v.stderr;var msg=typeof raw==='string'?raw.trim():td.decode(Uint8Array.from(raw||[])).trim();if(v.status!==0||msg){fail('git: HTTP request failed'+(msg?': '+msg.replace(/^curl: /,'').slice(0,200):''));}return 0;}
function removeTemp(p){try{if(exists(p))fs.rm(p);}catch(e){}}
// Binary-safe file access. The __fs bridge is text-only: readFile decodes UTF-8 and writeFile stores a serialized
// Uint8Array as "[object Object]", which corrupts objects, the index and packs. The byte messages carry the data as
// number arrays and round-trip exactly. Every git file access goes through these three helpers.
function ipcResult(r,what){if(!r)fail('git: '+what+': no response from the workspace');if(r.error!==undefined&&r.error!==null)fail('git: '+what+': '+errText(r.error));return r.value;}
// Files move through a host session in 1 MiB slices (base64), because an array of numbers over the IPC cannot hold a pack.
function gbytes(p){var s=ipcResult(ipc.send({f:'fs.openRead',path:pathOf(p)}),'read '+p),out=new Uint8Array(s.size);try{for(var at=0;at<out.length;at+=1048576){var part=ipcResult(ipc.send({f:'fs.readChunk',id:s.id,offset:at,length:Math.min(1048576,out.length-at)}),'read '+p);out.set(Buffer.from(part,'base64'),at);}}finally{ipc.send({f:'fs.closeRead',id:s.id});}return out;}
function gread(p){return td.decode(gbytes(p));}
// Pieces of 384 KiB go out as base64; the host writes the file once, when the session closes. A failure discards it.
function gwrite(p,data){var b=typeof data==='string'?te.encode(data):data;p=pathOf(p);mkdirp(dirname(p));var id=ipcResult(ipc.send({f:'fs.openWrite'}),'write '+p),done=false;try{for(var at=0;at<b.length;at+=393216)ipcResult(ipc.send({f:'fs.writeChunk',id:id,data:Buffer.from(b.subarray(at,at+393216)).toString('base64')}),'write '+p);ipcResult(ipc.send({f:'fs.closeWrite',id:id,path:p}),'write '+p);done=true;}finally{if(!done)ipc.send({f:'fs.closeWrite',id:id,path:null});}}
function isDirPath(p){var info=fs.stat(p);return !!(typeof info.isDirectory==='function'?info.isDirectory():info.isDirectory);}
function isFilePath(p){return exists(p)&&!isDirPath(p);}
// Recursive delete built only from primitives. Used for clone temp folders, which must never be left behind.
function removeTree(p){if(!exists(p))return;if(isDirPath(p)){for(var item of names(p)){var n=typeof item==='string'?item:item.name;if(!n||n==='.'||n==='..')continue;removeTree(p+'/'+n);}fs.rm(p,true);return;}fs.rm(p,false);}
function workspaceCurl(url,method,content,ctype,asSession){
  if(!/^https:\/\//i.test(url)||url.indexOf('@')!==-1)fail('git: only HTTPS without embedded credentials is allowed');
  var uri=httpsParts(url);
  var unique=hex(crypto.getRandomValues(new Uint8Array(12))),dir=pathOf((process.env.HOME||'/home/user')+'/.cache/git-wisp');mkdirp(dir);
  var body=dir+'/'+unique+'.body',headers=dir+'/'+unique+'.hdr',payload=dir+'/'+unique+'.payload',cfg=dir+'/'+unique+'.cfg';
  var args=['-sS','-L','--max-time','120','--max-filesize',String(REMOTE_MAX),'-D',headers,'-o',body,'-H','Git-Protocol: version=1'];
  if(method==='POST'){gwrite(payload,content);args.push('-X','POST','--data-binary','@'+payload,'-H','Content-Type: '+ctype,'-H','Accept: '+ctype.replace('-request','-result'));}
  // A temporary config file avoids secrets in /bin/curl argv and the process list. Never persist it past the request.
  var auth=authHeader(url);if(auth){args=args.filter(function(x){return x!=='-L';});gwrite(cfg,'header = '+JSON.stringify(auth)+'\n');args.push('-K',cfg);}
  args.push(url);
  try {
    // Run as "/bin/node /bin/curl ...", the same route the shell uses for the builtin's shebang. Spawning /bin/curl
    // directly inlines the body, where its shebang line is a syntax error.
    var r=ipc.send({f:'process.spawnSync',command:'/bin/node',args:['/bin/curl'].concat(args),options:{cwd:cwd(),env:process.env}});
    curlStatus(r);
    if(!exists(headers)||!exists(body))fail('git: curl did not write its response files');
    var lines=gread(headers),matches=Array.from(lines.matchAll(/(?:^|\n)HTTP\/\S+\s+(\d{3})[^\n]*/g)),code=matches.length?Number(matches[matches.length-1][1]):0;
    // GitHub answers 401 for an unknown or private repository without credentials, so both read as not found.
    if(code===401||code===404)fail("fatal: repository '"+url.replace(/\/info\/refs\?service=git-[a-z-]+$/,'').replace(/\/git-(upload|receive)-pack$/,'')+"' not found");
    if(code!==200)fail('git: HTTP '+code+' from '+uri.hostname,2);
    if(asSession){var sess=ipcResult(ipc.send({f:'fs.openRead',path:body}),'read '+body);if(sess.size>REMOTE_MAX)fail('git: response exceeds size limit');return sess;}var output=gbytes(body);if(output.length>REMOTE_MAX)fail('git: response exceeds size limit');return output;
  } finally {removeTemp(body);removeTemp(headers);removeTemp(payload);removeTemp(cfg);}
}
// Progress goes straight to the terminal, one line per real step, so a long clone never looks stuck.
function note(s){emit(s+'\n');}
// SHA-1 computed synchronously. crypto.subtle is a real async host call, and the Dusk node wrapper ends the program at
// the first real wait, so every git step must finish on microtasks. (The git runner in git.ts calls this from sha().)
// Checked against Node crypto for lengths 0 to 300001, including the padding boundaries.
function sha1Hex(msg){var len=msg.length;var total=((len+9+63)>>6)<<6;var padded=new Uint8Array(total);padded.set(msg);padded[len]=128;var dv=new DataView(padded.buffer);var bits=len*8;dv.setUint32(total-8,Math.floor(bits/4294967296));dv.setUint32(total-4,bits>>>0);var h0=1732584193,h1=4023233417,h2=2562383102,h3=271733878,h4=3285377520;var w=new Uint32Array(80);for(var off=0;off<total;off+=64){for(var i=0;i<16;i++)w[i]=dv.getUint32(off+i*4);for(i=16;i<80;i++){var x=w[i-3]^w[i-8]^w[i-14]^w[i-16];w[i]=(x<<1)|(x>>>31);}var a=h0,b=h1,c=h2,d=h3,e=h4;for(i=0;i<80;i++){var f,k;if(i<20){f=(b&c)|(~b&d);k=1518500249;}else if(i<40){f=b^c^d;k=1859775393;}else if(i<60){f=(b&c)|(b&d)|(c&d);k=2400959708;}else{f=b^c^d;k=3395469782;}var t=(((a<<5)|(a>>>27))+f+e+k+w[i])|0;e=d;d=c;c=(b<<30)|(b>>>2);b=a;a=t;}h0=(h0+a)|0;h1=(h1+b)|0;h2=(h2+c)|0;h3=(h3+d)|0;h4=(h4+e)|0;}return [h0,h1,h2,h3,h4].map(function(v){return (v>>>0).toString(16).padStart(8,'0');}).join('');}
function endpoint(url,service){url=url.replace(/\/+$/,'');return url+'/info/refs?service=git-'+service;}
function advertised(url,service){var bytes=workspaceCurl(endpoint(url,service),'GET');var info=unpackPkt(bytes),heads={};for(var line of info.lines){var s=td.decode(line).split('\0')[0].trim();var m=s.match(/^([0-9a-f]{40})\s+(refs\/(?:heads|tags)\/[^\s]+|HEAD)$/);if(m)heads[m[2]]=m[1];}if(!Object.keys(heads).length){var again=unpackPkt(bytes,info.offset);for(var line of again.lines){var s=td.decode(line).split('\0')[0].trim(),m=s.match(/^([0-9a-f]{40})\s+(refs\/(?:heads|tags)\/[^\s]+|HEAD)$/);if(m)heads[m[2]]=m[1];}}return heads;}
function remoteUrl(name){var cfg=gread(gfile('config')),pattern='[remote "'+name+'"]',i=cfg.indexOf(pattern);if(i<0)fail('git: no remote named '+name);var next=cfg.slice(i+pattern.length).split(/\n\s*\[/)[0],m=next.match(/^\s*url\s*=\s*(https:\/\/\S+)\s*$/m);if(!m)fail('git: remote has no HTTPS URL');return m[1];}
function packHeader(kind,size){var n=size,first=(kind<<4)|(n&15),buf=[];n=Math.floor(n/16);while(n){first|=128;buf.push(first);first=n&127;n=Math.floor(n/128);}buf.push(first);return new Uint8Array(buf);}
function encodePack(ids){var found=new Set(),queue=ids.slice(),parts=[te.encode('PACK'),be(2),be(0)];while(queue.length){var id=queue.shift();if(found.has(id))continue;found.add(id);if(found.size>PACK_OBJECTS)fail('git: too many objects to push');var obj=getObject(id),kind={commit:1,tree:2,blob:3,tag:4}[obj.type];if(!kind)fail('git: cannot push object type '+obj.type);var data=obj.body;
    if(obj.type==='commit'){var text=td.decode(data),tree=text.match(/^tree ([0-9a-f]{40})$/m),parents=Array.from(text.matchAll(/^parent ([0-9a-f]{40})$/gm));if(tree)queue.push(tree[1]);for(var parent of parents)queue.push(parent[1]);}
    if(obj.type==='tree'){for(var v of treeEntries(data))queue.push(v.id);}
    parts.push(packHeader(kind,data.length),transform(data,'zip'));
  }
  parts[2]=be(found.size);var payload=joinBytes(parts);if(payload.length>REMOTE_MAX)fail('git: pack exceeds upload limit');return concat(payload,fromhex(sha(payload)));
}
function treeEntries(data){var pos=0,out=[];while(pos<data.length){var space=data.indexOf(32,pos),end=data.indexOf(0,space+1);if(space<0||end<0||end+21>data.length)fail('git: corrupt tree');var mode=td.decode(data.slice(pos,space)),name=td.decode(data.slice(space+1,end)),id=hex(data.slice(end+1,end+21));checkName(name);out.push({mode,name,id});pos=end+21;}return out;}
function readSize(pack,at){var c=pack[at++],kind=(c>>>4)&7,size=c&15,mul=16;while(c&128){if(at>=pack.length)fail('git: truncated pack header');c=pack[at++];size+=(c&127)*mul;mul*=128;if(size>UNPACK_MAX)fail('git: oversized packed object');}return {kind,size,at};}
// Pure-JS zlib inflate. Host zlib rejects bytes after the end of a stream, and a pack holds many streams back to back,
// so this decoder reports where each stream's own Adler-32 trailer ends. Verified against Node zlib (see the test notes).
var LBASE=[3,4,5,6,7,8,9,10,11,13,15,17,19,23,27,31,35,43,51,59,67,83,99,115,131,163,195,227,258];
var LEXT=[0,0,0,0,0,0,0,0,1,1,1,1,2,2,2,2,3,3,3,3,4,4,4,4,5,5,5,5,0];
var DBASE=[1,2,3,4,5,7,9,13,17,25,33,49,65,97,129,193,257,385,513,769,1025,1537,2049,3073,4097,6145,8193,12289,16385,24577];
var DEXT=[0,0,0,0,1,1,2,2,3,3,4,4,5,5,6,6,7,7,8,8,9,9,10,10,11,11,12,12,13,13];
var CLORDER=[16,17,18,0,8,7,9,6,10,5,11,4,12,3,13,2,14,1,15];
function applyDelta(source,delta){var pos=0;function length(){var n=0,factor=1,c;do{if(pos>=delta.length)fail('git: truncated delta');c=delta[pos++];n+=(c&127)*factor;factor*=128;}while(c&128);return n;}var baseSize=length(),targetSize=length();if(baseSize!==source.length||targetSize>UNPACK_MAX)fail('git: delta length mismatch');var chunks=[],total=0;while(pos<delta.length){var cmd=delta[pos++];if(cmd&128){var offset=0,count=0;for(var i=0;i<4;i++)if(cmd&(1<<i)){if(pos>=delta.length)fail('git: truncated delta');offset+=delta[pos++]*Math.pow(256,i);}for(var i=0;i<3;i++)if(cmd&(1<<(i+4))){if(pos>=delta.length)fail('git: truncated delta');count+=delta[pos++]*Math.pow(256,i);}if(count===0)count=65536;if(offset+count>source.length)fail('git: delta copy outside object');chunks.push(source.slice(offset,offset+count));total+=count;}else if(cmd){if(pos+cmd>delta.length)fail('git: truncated delta insert');chunks.push(delta.slice(pos,pos+cmd));pos+=cmd;total+=cmd;}else fail('git: illegal delta instruction');if(total>targetSize)fail('git: oversized delta');}if(total!==targetSize)fail('git: delta output mismatch');return joinBytes(chunks);}
// The pack stays on the host (a read session). Slices come over the byte channel as base64; inflate and the checksum run
// on the host, which is much faster than the VM interpreter (scripts/patches/io.mjs).
function packSlice(s,at,len){var out=new Uint8Array(len),done=0;while(done<len){var n=Math.min(1048576,len-done);var part=ipcResult(ipc.send({f:'fs.readChunk',id:s.id,offset:at+done,length:n}),'read pack');out.set(Buffer.from(part,'base64'),done);done+=n;}return out;}
function unpackPack(s){var head=packSlice(s,0,Math.min(256,s.size)),base=-1;for(var h=0;h+4<=head.length;h++){if(head[h]===80&&head[h+1]===65&&head[h+2]===67&&head[h+3]===75){base=h;break;}}if(base<0)fail('git: server did not return a Git pack');var end=s.size-20;if(end-base<32)fail('git: unsupported pack');var fixed=packSlice(s,base,12);if(num(fixed,4)!==2&&num(fixed,4)!==3)fail('git: unsupported pack');var digest=ipcResult(ipc.send({f:'fs.digestRange',id:s.id,offset:base,length:end-base}),'digest pack');if(digest!==hex(packSlice(s,end,20)))fail('git: pack checksum mismatch');var count=num(fixed,8);if(count>PACK_OBJECTS)fail('git: pack has too many objects');
// The host unpacks in batches (scripts/patches/io.mjs): receive every object, then resolve the deltas. Each batch reports progress.
var job=ipcResult(ipc.send({f:'git.unpackStart',pack:s.id,gitdir:gitdir,pos:base+12,count:count}),'unpack pack');var receiving=-1,resolving=-1,received=false,st;
for(;;){st=ipcResult(ipc.send({f:'git.unpackStep',job:job,max:300}),'unpack pack');
 if(st.phase==='receive'){var pct=Math.floor(st.processed*100/count);if(pct!==receiving){receiving=pct;emit('\rReceiving objects: '+String(pct).padStart(3)+'% ('+st.processed+'/'+count+')');}}
 else if(!received){received=true;emit('\rReceiving objects: 100% ('+count+'/'+count+'), done.\n');}
 if(st.phase!=='receive'&&st.deltas){var rp=Math.floor(st.resolved*100/st.deltas);if(rp!==resolving){resolving=rp;emit('\rResolving deltas: '+String(rp).padStart(3)+'% ('+st.resolved+'/'+st.deltas+')');}}
 if(st.done)break;}
ipc.send({f:'git.unpackEnd',job:job});
if(!received)emit('\rReceiving objects: 100% ('+count+'/'+count+'), done.\n');
if(st.deltas)emit('\rResolving deltas: 100% ('+st.deltas+'/'+st.deltas+'), done.\n');
if(st.pos!==end)fail('git: pack has unexpected trailing data');
return count;}
function listCheckoutTree(id,prefix,out){var obj=getObject(id);if(obj.type!=='tree')fail('git: expected tree object');for(var e of treeEntries(obj.body)){var rel=prefix?prefix+'/'+e.name:e.name;checkName(rel);if(e.mode==='40000'||e.mode==='040000')listCheckoutTree(e.id,rel,out);else if(e.mode==='100644'||e.mode==='100755'||e.mode==='160000')out.push({path:rel,id:e.id,mode:parseInt(e.mode,8)});else if(e.mode==='120000')fail('git: symlink checkout unsupported for safety');else fail('git: unsupported tree mode '+e.mode);}}
function checkout(id){
  var obj=getObject(id);if(obj.type!=='commit')fail('git: commit not found');
  var m=td.decode(obj.body).match(/^tree ([0-9a-f]{40})$/m);if(!m)fail('git: corrupt commit');
  var files=[];listCheckoutTree(m[1],'',files);var updated=0,updShown=-1,checkoutStart=Date.now();
  var prior=entries(),tracked=new Set(prior.map(function(x){return x.path;}));
  for(var f of files){var target=under(root+'/'+f.path);if(exists(target)&&!tracked.has(f.path))fail('git: untracked file would be overwritten: '+f.path);}
  var newPaths=new Set(files.map(function(f){return f.path;}));
  for(var f of files){var target=under(root+'/'+f.path);if(f.mode===0o160000){mkdirp(target);f.size=0;}else{var b=getObject(f.id);if(b.type!=='blob')fail('git: expected blob');gwrite(target,b.body);f.size=b.body.length;}var pct=Math.floor(++updated*100/files.length);if(pct!==updShown&&Date.now()-checkoutStart>=2000){updShown=pct;emit('\rUpdating files: '+String(pct).padStart(3)+'% ('+updated+'/'+files.length+')');}}
  if(updShown>=0)emit('\rUpdating files: 100% ('+files.length+'/'+files.length+'), done.\n');for(var old of prior)if(!newPaths.has(old.path)){var target=under(root+'/'+old.path);if(exists(target))fs.rm(target);}
  saveIndex(files);
}
function remoteHead(refs){if(refs.HEAD){for(var key of Object.keys(refs))if(key.startsWith('refs/heads/')&&refs[key]===refs.HEAD)return {ref:key,id:refs[key]};}var branch=Object.keys(refs).filter(function(k){return k.startsWith('refs/heads/')}).sort()[0];if(!branch)fail('git: no advertised remote branches');return {ref:branch,id:refs[branch]};}
function wants(ids){var parts=[];for(var i=0;i<ids.length;i++)parts.push(pkt('want '+ids[i]+(i===0?' side-band-64k ofs-delta':'')+'\n'));parts.push(te.encode('0000'),pkt('done\n'));return concat.apply(null,parts);}
// Git prints the server's progress as "remote:" lines; the host splits the side band into progress text and the pack.
function remotePrefix(text){return 'remote: '+text.replace(/\n(?=[^\n])/g,'\nremote: ');}
function upload(url,ids){var output=workspaceCurl(url.replace(/\/+$/,'')+'/git-upload-pack','POST',wants(ids),'application/x-git-upload-pack-request',true);try{var side=ipcResult(ipc.send({f:'fs.demuxSideband',id:output.id}),'read response');if(side.progress)emit(remotePrefix(side.progress));try{return unpackPack(side.pack);}finally{ipc.send({f:'fs.closeRead',id:side.pack.id});}}finally{ipc.send({f:'fs.closeRead',id:output.id});}}
function isClean(){
  var indexed=entries(),previous=currentCommit(),committed=[];
  if(previous){var commit=getObject(previous),m=td.decode(commit.body).match(/^tree ([0-9a-f]{40})$/m);if(!m)fail('git: corrupt commit');listCheckoutTree(m[1],'',committed);}
  if(indexed.length!==committed.length)return false;
  var staged=new Map(committed.map(function(x){return [x.path,x.id];}));
  for(var x of indexed){
    if(staged.get(x.path)!==x.id)return false;
    if(x.mode===0o160000)continue;var p=under(root+'/'+x.path);if(!exists(p))return false;
    var body=gbytes(p),oid=sha(concat(te.encode('blob '+body.length+'\0'),body));if(oid!==x.id)return false;
  }
  return true;
}
// Clone builds the repository in place. The destination is absent or an empty directory (checked above), so a failure
// removes only what this clone created. A rename of the finished tree was too slow on a 5,000-object repository.
function cloneRepo(args){
 var url=args[0];if(!url)fail('usage: git clone HTTPS_URL [DIR]');if(!/^https:\/\/[^@]+$/.test(url))fail('git: HTTPS remotes only');
 var target=args[1]||httpsParts(url).pathname.replace(/\/+$/,'').split('/').pop().replace(/\.git$/,'');
 var leaf=target.replace(/\/+$/,'').split('/').pop();
 if(!leaf||leaf==='.'||leaf==='..')fail("fatal: could not clone into '"+target+"': name a new directory");
 var dest=pathOf(target),parent=dirname(dest);
 if(exists(dest)&&(!isDirPath(dest)||names(dest).length))fail("fatal: destination path '"+target+"' already exists and is not an empty directory.");
 emit("Cloning into '"+(args.display||target)+"'...\n");
 mkdirp(parent);
 var created=!exists(dest),built=false;
 try{
  root=dest;gitdir=dest+'/.git';mkdirp(gfile('objects'));mkdirp(gfile('refs/heads'));mkdirp(gfile('refs/remotes/origin'));
  var refs=advertised(url,'upload-pack'),head=remoteHead(refs),seen={},wanted=[];
  for(var key of Object.keys(refs)){if(key==='HEAD'||key.indexOf('^{}')>=0)continue;if(key.indexOf('refs/heads/')===0||key.indexOf('refs/tags/')===0){if(!seen[refs[key]]){seen[refs[key]]=1;wanted.push(refs[key]);}}}
  if(!seen[head.id])wanted.push(head.id);
  upload(url,wanted);
  var branch=head.ref.split('/').pop();
  gwrite(gfile('HEAD'),'ref: refs/heads/'+branch+'\n');gwrite(gfile('refs/heads/'+branch),head.id+'\n');
  for(var rk of Object.keys(refs)){if(rk.indexOf('^{}')>=0)continue;if(rk.indexOf('refs/heads/')===0)gwrite(gfile('refs/remotes/origin/'+rk.slice('refs/heads/'.length)),refs[rk]+'\n');else if(rk.indexOf('refs/tags/')===0)gwrite(gfile(rk),refs[rk]+'\n');}
  gwrite(gfile('refs/remotes/origin/HEAD'),'ref: refs/remotes/origin/'+branch+'\n');
  gwrite(gfile('config'),'[core]\n\trepositoryformatversion = 0\n\tbare = false\n[remote "origin"]\n\turl = '+url+'\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch "'+branch+'"]\n\tremote = origin\n\tmerge = refs/heads/'+branch+'\n');
  checkout(head.id);
  built=true;
 }finally{if(!built){try{if(created)removeTree(dest);else for(var item of names(dest))removeTree(dest+'/'+childName(item));}catch(e){}}}

}
function gitRemote(cmd,args){
 if(cmd==='clone'){cloneRepo(args);return;}
 repo();var name='origin';if(args[0]&&args[0][0]!=='-')name=args[0];var url=remoteUrl(name),refs=advertised(url,cmd==='push'?'receive-pack':'upload-pack');var localRef=headRef(),branch=localRef&&localRef.slice('refs/heads/'.length);if(!branch)fail('git: detached HEAD is unsupported');var target='refs/heads/'+branch;
 if(cmd==='push'){var id=currentCommit();if(!id)fail('git: nothing to push');var old=refs[target]||'0'.repeat(40),pack=encodePack([id]);var caps='report-status',command=pkt(old+' '+id+' '+target+'\0'+caps+'\n'),request=concat(command,te.encode('0000'),pack);var result=workspaceCurl(url.replace(/\/+$/,'')+'/git-receive-pack','POST',request,'application/x-git-receive-pack-request');var report=td.decode(result);if(!report.includes('unpack ok')||!report.includes('ok '+target))fail('git: push refused by server: '+report.replace(/[^\x20-\x7e\n]/g,''));gwrite(gfile('refs/remotes/'+name+'/'+branch),id+'\n');emit('Pushed '+short(id)+' to '+name+'/'+branch+'\n');return;}
 var chosen=refs[target]?{ref:target,id:refs[target]}:remoteHead(refs);upload(url,[chosen.id]);gwrite(gfile('refs/remotes/'+name+'/'+chosen.ref.split('/').pop()),chosen.id+'\n');if(cmd==='fetch'){emit('Fetched '+short(chosen.id)+' from '+name+'\n');return;}
 if(!isClean())fail('git: local working tree or index has changes; refusing pull');var before=currentCommit();if(before&&before!==chosen.id){var next=chosen.id,found=false;for(var i=0;i<200&&next;i++){if(next===before){found=true;break;}var obj=getObject(next),m=td.decode(obj.body).match(/^parent ([a-f0-9]{40})$/m);next=m?m[1]:'';}if(!found)fail('git: pull requires a merge; only fast-forward is supported');}
 checkout(chosen.id);gwrite(gfile(localRef),chosen.id+'\n');emit('Fast-forwarded to '+short(chosen.id)+'\n');
}
`;
