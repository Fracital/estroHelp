import { script } from './shared'
import { gitNetwork } from './git-network'

/**
 * Small browser Git implementation with real Git object files, v2 index and
 * smart HTTP over the existing Dusk /bin/curl -> gated Wisp network stack.
 * Local commands print what git 2.x prints for the same repository state (status, commit,
 * log, branch, add, rev-parse, remote, init), so scripts and people can read the output
 * the same way. Clone and fast-forward fetch/pull/push run over smart HTTP.
 * It is intentionally a subset: no merge, rebase, stash, diff, or submodules.
 */
export const sources: Record<string, string> = {
  git: script(gitNetwork + String.raw`
var te=new TextEncoder(),td=new TextDecoder(),ZERO=new Uint8Array(0);
function concat(){return joinBytes(Array.prototype.slice.call(arguments));}
function be(n){return new Uint8Array([(n>>>24)&255,(n>>>16)&255,(n>>>8)&255,n&255]);}
function num(b,o){return ((b[o]*16777216)+(b[o+1]<<16)+(b[o+2]<<8)+b[o+3])>>>0;}
function u8(b){return b instanceof Uint8Array?b:new Uint8Array(b);}
function fromhex(s){var out=new Uint8Array(s.length/2);for(var i=0;i<out.length;i++)out[i]=parseInt(s.slice(i*2,i*2+2),16);return out;}
function sha(b){return sha1Hex(b);}
function transform(b,which){if(which==='zip')return new Uint8Array(require('zlib').deflateSync(b));var out=new Uint8Array(require('zlib').inflateSync(b));if(out.length>UNPACK_MAX)fail('git: oversized unpacked object');return out;}
var root='',gitdir='';
var DEFAULT_BRANCH='master';
function under(p){p=pathOf(p);if(p!==root&&p.indexOf(root+'/')!==0)fail('git: refusing path outside the repository: '+p);return p;}
function gfile(p){return gitdir+'/'+p;}
// A repository is a .git folder that has a HEAD file. A half-written .git (no HEAD) is not one, so the walk goes on.
function isRepoDir(dir){return isFilePath(dir+'/.git/HEAD');}
var workBase=cwd();
function fromBase(p){p=String(p||'.');return p.charAt(0)==='/'?pathOf(p):pathOf(workBase+'/'+p);}
function repo(){var dir=workBase;while(dir!=='/'&&!isRepoDir(dir))dir=dirname(dir);if(!isRepoDir(dir))fail('fatal: not a git repository (or any of the parent directories): .git');root=dir;gitdir=dir+'/.git';}
function object(type,data){var raw=concat(te.encode(type+' '+data.length+'\0'),data);var id=sha(raw),dst=gfile('objects/'+id.slice(0,2)+'/'+id.slice(2));if(!exists(dst))gwrite(dst,transform(raw,'zip'));return id;}
function getObject(id){if(!/^[a-f0-9]{40}$/.test(id))fail('git: invalid object id');var p=gfile('objects/'+id.slice(0,2)+'/'+id.slice(2));if(!exists(p))fail('git: missing object '+id);var raw=transform(gbytes(p),'unzip'),i=raw.indexOf(0);if(i<0)fail('git: corrupt object '+id);if(sha(raw)!==id)fail('git: corrupt hash '+id);var header=td.decode(raw.slice(0,i)).split(' '),body=raw.slice(i+1);if(Number(header[1])!==body.length)fail('git: invalid object size');return {type:header[0],body:body};}
function headText(){if(!isFilePath(gfile('HEAD')))fail('fatal: not a git repository: '+gitdir+' has no HEAD');return gread(gfile('HEAD')).trim();}
function headRef(){var h=headText();return h.startsWith('ref: ')?h.slice(5):null;}
function currentCommit(){var ref=headRef();if(ref)return isFilePath(gfile(ref))?gread(gfile(ref)).trim():'';return headText();}
function short(id){return id?id.slice(0,7):'(unborn)';}
function checkName(name){if(!name||name==='.'||name==='..'||name.indexOf('\\')>=0||name.indexOf('\0')>=0||name.split('/').some(function(x){return x==='..'||x==='.git'}))fail('git: unsafe path '+name);}
function entries(){if(!exists(gfile('index')))return [];var b=gbytes(gfile('index'));if(td.decode(b.slice(0,4))!=='DIRC'||num(b,4)!==2||b.length<32)fail('git: unsupported index format');var count=num(b,8),out=[],at=12;for(var i=0;i<count;i++){var start=at;if(at+62>b.length-20)fail('git: corrupt index');var mode=num(b,at+24),id=hex(b.slice(at+40,at+60)),flags=(b[at+60]<<8)|b[at+61],length=flags&4095;at+=62;var end=at;while(end<b.length&&b[end]!==0)end++;if(end>=b.length)fail('git: unterminated index path');var filename=td.decode(b.slice(at,end));if(length!==4095&&filename.length!==length)fail('git: corrupt index filename');checkName(filename);out.push({path:filename,id:id,mode:mode||33188});at=start+Math.ceil((end-start+1)/8)*8;}return out;}
function saveIndex(items){items.sort(function(a,b){return a.path<b.path?-1:a.path>b.path?1:0});var blocks=[te.encode('DIRC'),be(2),be(items.length)];for(var i=0;i<items.length;i++){var e=items[i],name=te.encode(e.path);if(name.length>4095)fail('git: path too long');var fixed=new Uint8Array(62);fixed.set(be(e.mode||0o100644),24);fixed.set(be(e.size||0),36);fixed.set(fromhex(e.id),40);fixed[60]=(name.length>>>8)&15;fixed[61]=name.length&255;var record=concat(fixed,name,new Uint8Array([0]));blocks.push(record,new Uint8Array((8-record.length%8)%8));}var payload=joinBytes(blocks),digest=sha(payload);gwrite(gfile('index'),concat(payload,fromhex(digest)));}
function relative(p){return p.slice(root.length+1);}
function assertBranch(s){if(!/^[A-Za-z0-9][A-Za-z0-9._\/-]*$/.test(s)||s.indexOf('..')>=0||s.endsWith('.lock'))fail("fatal: '"+s+"' is not a valid branch name",128);}
function createTree(items){var parts=[],dirs=new Map(),files=[];for(var i=0;i<items.length;i++){var name=items[i].path,slash=name.indexOf('/');if(slash===-1)files.push(items[i]);else{var segment=name.slice(0,slash),bucket=dirs.get(segment)||[];bucket.push({path:name.slice(slash+1),id:items[i].id,mode:items[i].mode});dirs.set(segment,bucket);}}for(var f of files)parts.push({name:f.path,mode:(f.mode===0o160000?'160000':f.mode===0o100755?'100755':'100644'),id:f.id});for(var entry of dirs)parts.push({name:entry[0],mode:'40000',id:createTree(entry[1])});parts.sort(function(a,b){return (a.name+(a.mode==='40000'?'/':''))<(b.name+(b.mode==='40000'?'/':''))?-1:1});var data=[];for(var part of parts)data.push(te.encode(part.mode+' '+part.name+'\0'),fromhex(part.id));return object('tree',joinBytes(data));}
var overrides={};
function configOverride(key){return Object.prototype.hasOwnProperty.call(overrides,key)?overrides[key]:undefined;}
// Repository config for one [section] key, e.g. user.name. A -c override wins, as it does in Git.
function userSetting(key){var o=configOverride(key);if(o!==undefined)return o;if(!isFilePath(gfile('config')))return '';var section=gread(gfile('config')).match(/\[user\]([\s\S]*?)(?=\n\[|$)/);if(!section)return '';var m=section[1].match(new RegExp('^\\s*'+key.split('.')[1]+'\\s*=\\s*(.*?)\\s*$','m'));return m?m[1]:'';}

// ---- Commits, trees and dates ----
var WEEKDAY=['Sun','Mon','Tue','Wed','Thu','Fri','Sat'],MONTHS=['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'];
function pad2(n){return (n<10?'0':'')+n;}
function zoneText(minutesEast){var sign=minutesEast<0?'-':'+',a=Math.abs(minutesEast);return sign+pad2(Math.floor(a/60))+pad2(a%60);}
function localZone(){return zoneText(-new Date().getTimezoneOffset());}
function zoneMinutes(zone){var sign=zone.charAt(0)==='-'?-1:1;return sign*(Number(zone.slice(1,3))*60+Number(zone.slice(3,5)));}
function gitDate(ts,zone){var d=new Date((ts+zoneMinutes(zone)*60)*1000);return WEEKDAY[d.getUTCDay()]+' '+MONTHS[d.getUTCMonth()]+' '+String(d.getUTCDate()).padStart(2)+' '+pad2(d.getUTCHours())+':'+pad2(d.getUTCMinutes())+':'+pad2(d.getUTCSeconds())+' '+d.getUTCFullYear()+' '+zone;}
function identityParts(line){var m=/^(.*) <([^>]*)> (-?\d+) ([+-]\d{4})$/.exec(line||'');return m?{name:m[1],email:m[2],ts:Number(m[3]),zone:m[4]}:{name:'',email:'',ts:0,zone:'+0000'};}
function commitParts(id){var o=getObject(id);if(o.type!=='commit')fail('fatal: not a commit object: '+id);var text=td.decode(o.body),cut=text.indexOf('\n\n'),head=cut<0?text:text.slice(0,cut),parts={id:id,tree:'',parents:[],author:'',committer:'',message:cut<0?'':text.slice(cut+2)};for(var line of head.split('\n')){if(line.indexOf('tree ')===0)parts.tree=line.slice(5);else if(line.indexOf('parent ')===0)parts.parents.push(line.slice(7));else if(line.indexOf('author ')===0)parts.author=line.slice(7);else if(line.indexOf('committer ')===0)parts.committer=line.slice(10);}return parts;}
function subjectOf(message){return message.split('\n')[0];}
function treeFiles(treeId){var list=[],out=new Map();listCheckoutTree(treeId,'',list);for(var f of list)out.set(f.path,f.id);return out;}
function commitFiles(commitId){if(!commitId)return new Map();var c=commitParts(commitId);return treeFiles(c.tree);}

// ---- Line diffs for commit statistics (the insertion and deletion counts git prints) ----
function blobLines(bytes){var s=td.decode(bytes);if(s==='')return [];var parts=s.split('\n');if(parts[parts.length-1]==='')parts.pop();return parts;}
function lcsLength(A,B){if(!A.length||!B.length)return 0;var prev=new Uint32Array(B.length+1),cur=new Uint32Array(B.length+1);for(var i=1;i<=A.length;i++){cur[0]=0;for(var j=1;j<=B.length;j++)cur[j]=A[i-1]===B[j-1]?prev[j-1]+1:Math.max(prev[j],cur[j-1]);var t=prev;prev=cur;cur=t;}return prev[B.length];}
function changeCounts(oldBytes,newBytes){var ids=new Map();function code(x){var v=ids.get(x);if(v===undefined){v=ids.size;ids.set(x,v);}return v;}var a=blobLines(oldBytes).map(code),b=blobLines(newBytes).map(code);var common=a.length*b.length>20000000?Math.min(a.length,b.length):lcsLength(a,b);return {ins:b.length-common,del:a.length-common};}

// ---- .gitignore (root file only): last matching rule wins, "!" negates, trailing "/" matches directories ----
function globRegExp(glob,anchored){var out='';for(var i=0;i<glob.length;i++){var c=glob.charAt(i);if(c==='*'){if(glob.charAt(i+1)==='*'){out+='.*';i++;}else out+='[^/]*';}else if(c==='?')out+='[^/]';else if('.+()|^$\\{}'.indexOf(c)>=0)out+='\\'+c;else out+=c;}return new RegExp((anchored?'^':'(?:^|/)')+out+'$');}
function ignoreRules(){var rules=[];if(!isFilePath(root+'/.gitignore'))return rules;var lines=gread(root+'/.gitignore').split('\n');for(var raw of lines){var line=raw.replace(/\r$/,'');if(!line||line.charAt(0)==='#')continue;var neg=false;if(line.charAt(0)==='!'){neg=true;line=line.slice(1);}var dirOnly=line.slice(-1)==='/';if(dirOnly)line=line.slice(0,-1);var anchored=line.indexOf('/')>=0;if(line.charAt(0)==='/')line=line.slice(1);if(line)rules.push({neg:neg,dirOnly:dirOnly,re:globRegExp(line,anchored)});}return rules;}
function ignored(rules,rel,isDir){var result=false;for(var r of rules){if(r.dirOnly&&!isDir)continue;if(r.re.test(rel))result=!r.neg;}return result;}
function childName(item){return typeof item==='string'?item:item.name;}

// ---- Working tree: untracked files, collapsed to directories as git does ----
function hasVisibleFile(rules,rel){for(var item of names(root+'/'+rel)){var name=childName(item);if(!name||name==='.git')continue;var child=rel+'/'+name,dir=isDirPath(root+'/'+child);if(ignored(rules,child,dir))continue;if(!dir||hasVisibleFile(rules,child))return true;}return false;}
function walkUntracked(rules,tracked,trackedDirs,dirRel,out){var base=dirRel?root+'/'+dirRel:root;for(var item of names(base)){var name=childName(item);if(!name||name==='.git')continue;var rel=dirRel?dirRel+'/'+name:name,isDir=isDirPath(root+'/'+rel);if(ignored(rules,rel,isDir))continue;if(isDir){if(trackedDirs.has(rel))walkUntracked(rules,tracked,trackedDirs,rel,out);else if(hasVisibleFile(rules,rel))out.push(rel+'/');}else if(!tracked.has(rel))out.push(rel);}}
function filesBelowIgnored(rules,dir){var out=[];for(var item of names(dir)){var name=childName(item);if(!name||name==='.git')continue;var full=dir+'/'+name,rel=relative(full),isDir=isDirPath(full);if(ignored(rules,rel,isDir))continue;if(isDir)out.push.apply(out,filesBelowIgnored(rules,full));else out.push(full);}return out;}
function workBlobId(rel){var b=gbytes(root+'/'+rel);return sha(concat(te.encode('blob '+b.length+'\0'),b));}
function stageTracked(index){var out=[];for(var e of index){if(e.mode===0o160000){out.push(e);continue;}var full=root+'/'+e.path;if(!exists(full)||isDirPath(full))continue;var bytes=gbytes(full);out.push({path:e.path,id:object('blob',bytes),mode:e.mode,size:bytes.length});}return out;}
function byPath(a,b){return a[1]<b[1]?-1:a[1]>b[1]?1:0;}
function statusLines(list){var out='';for(var item of list){var label=item[0]+':';out+='\t'+label+' '.repeat(Math.max(1,12-label.length))+item[1]+'\n';}return out;}

// status: the same sections, hints and final line git prints (wt-status.c).
function statusReport(){
  var head=currentCommit(),unborn=!head,ref=headRef(),label=ref?ref.replace('refs/heads/',''):null;
  var idx=entries(),indexMap=new Map(idx.map(function(e){return [e.path,e.id];})),headMap=commitFiles(head);
  var staged=[],unstaged=[];
  for(var e of idx){if(!headMap.has(e.path))staged.push(['new file',e.path]);else if(headMap.get(e.path)!==e.id)staged.push(['modified',e.path]);}
  for(var hp of headMap.keys())if(!indexMap.has(hp))staged.push(['deleted',hp]);
  for(var w of idx){var full=root+'/'+w.path;if(w.mode===0o160000)continue;if(!exists(full)||isDirPath(full))unstaged.push(['deleted',w.path]);else if(workBlobId(w.path)!==w.id)unstaged.push(['modified',w.path]);}
  staged.sort(byPath);unstaged.sort(byPath);
  var trackedDirs=new Set();for(var t of idx){var parts=t.path.split('/');for(var k=1;k<parts.length;k++)trackedDirs.add(parts.slice(0,k).join('/'));}
  var untracked=[];walkUntracked(ignoreRules(),indexMap,trackedDirs,'',untracked);untracked.sort();
  var text=label?'On branch '+label+'\n':'HEAD detached at '+short(head)+'\n';
  if(unborn)text+='\nNo commits yet\n\n';
  if(staged.length)text+='Changes to be committed:\n'+(unborn?'  (use "git rm --cached <file>..." to unstage)\n':'  (use "git restore --staged <file>..." to unstage)\n')+statusLines(staged)+'\n';
  if(unstaged.length)text+='Changes not staged for commit:\n  (use "git '+(unstaged.some(function(x){return x[0]==='deleted';})?'add/rm':'add')+' <file>..." to update what will be committed)\n  (use "git restore <file>..." to discard changes in working directory)\n'+statusLines(unstaged)+'\n';
  if(untracked.length)text+='Untracked files:\n  (use "git add <file>..." to include in what will be committed)\n'+untracked.map(function(p){return '\t'+p+'\n';}).join('')+'\n';
  var message=null;
  if(!staged.length){if(unstaged.length)message='no changes added to commit (use "git add" and/or "git commit -a")';else if(untracked.length)message='nothing added to commit but untracked files present (use "git add" to track)';else message=unborn?'nothing to commit (create/copy files and use "git add" to track)':'nothing to commit, working tree clean';}
  if(message)text+=message+'\n';
  emit(text);
}

function commitCommand(args){
  var messages=[],all=false,quiet=false;
  for(var i=0;i<args.length;i++){var a=args[i];
    if(a==='--all'){all=true;continue;}if(a==='--quiet'){quiet=true;continue;}if(a==='--message'){if(args[i+1]===undefined)fail("error: option 'message' requires a value");messages.push(args[++i]);continue;}
    if(a.indexOf('--')===0||a.charAt(0)!=='-'||a.length<2)fail("error: unknown option '"+a+"'");
    for(var k=1;k<a.length;k++){var c=a.charAt(k);
      if(c==='m'){var rest=a.slice(k+1);if(rest){messages.push(rest);}else{if(args[i+1]===undefined)fail("error: switch 'm' requires a value");messages.push(args[++i]);}break;}
      if(c==='a')all=true;else if(c==='q')quiet=true;else fail("error: unknown switch '"+c+"'");}}
  if(!messages.length)fail('fatal: no commit message given (use -m)');
  var head=currentCommit(),ref=headRef(),index=entries();
  if(all)index=stageTracked(index);
  var treeId=createTree(index),parent=head?commitParts(head):null;
  // Nothing staged: git prints the status and exits 1.
  if((parent&&parent.tree===treeId)||(!parent&&!index.length)){statusReport();process.exitCode=1;return;}
  var name=process.env.GIT_AUTHOR_NAME||userSetting('user.name')||process.env.USER||'workspace',email=process.env.GIT_AUTHOR_EMAIL||userSetting('user.email')||'user@workspace.invalid';
  var ident=name+' <'+email+'> '+Math.floor(Date.now()/1000)+' '+localZone();
  var message=messages.join('\n\n');if(message.slice(-1)!=='\n')message+='\n';
  var body='tree '+treeId+'\n'+(head?'parent '+head+'\n':'')+'author '+ident+'\ncommitter '+ident+'\n\n'+message;
  var id=object('commit',te.encode(body));
  // Statistics compare against the parent tree, as git's diffstat does.
  var oldFiles=parent?treeFiles(parent.tree):new Map(),newFiles=new Map(index.map(function(e){return [e.path,e.id];})),changed=[],ins=0,del=0;
  for(var ne of index){var was=oldFiles.get(ne.path);if(was===undefined){ins+=blobLines((getObject(ne.id)).body).length;changed.push({path:ne.path,kind:'create'});}else if(was!==ne.id){var c=changeCounts((getObject(was)).body,(getObject(ne.id)).body);ins+=c.ins;del+=c.del;changed.push({path:ne.path,kind:'change'});}}
  for(var pair of Array.from(oldFiles)){if(!newFiles.has(pair[0])){del+=blobLines((getObject(pair[1])).body).length;changed.push({path:pair[0],kind:'delete'});}}
  changed.sort(function(x,y){return x.path<y.path?-1:x.path>y.path?1:0;});
  if(ref)gwrite(gfile(ref),id+'\n');else gwrite(gfile('HEAD'),id+'\n');
  if(all)saveIndex(index);
  var summary=' '+changed.length+(changed.length===1?' file changed':' files changed');
  if(ins)summary+=', '+ins+(ins===1?' insertion(+)':' insertions(+)');
  if(del)summary+=', '+del+(del===1?' deletion(-)':' deletions(-)');
  var branch=ref?ref.replace('refs/heads/',''):'detached HEAD';
  var modes=changed.filter(function(x){return x.kind!=='change';}).map(function(x){return ' '+(x.kind==='create'?'create':'delete')+' mode 100644 '+x.path+'\n';}).join('');
  if(!quiet)emit('['+branch+(parent?'':' (root-commit)')+' '+short(id)+'] '+subjectOf(message)+'\n'+summary+'\n'+modes);
}

function addCommand(args){
  if(!args.length){emit("Nothing specified, nothing added.\nhint: Maybe you wanted to say 'git add .'?\nhint: Turn this message off by running\nhint: \"git config advice.addEmptyPathspec false\"\n");return;}
  var index=entries(),map=new Map(index.map(function(e){return [e.path,e];})),rules=ignoreRules(),plan=[];
  // Validate every pathspec before changing the index, as git does.
  for(var spec of args){var full=fromBase(spec);under(full);
    if(!exists(full)){var rel=relative(full),prefix=rel+'/';if(!index.some(function(e){return e.path===rel||e.path.indexOf(prefix)===0;}))fail("fatal: pathspec '"+spec+"' did not match any files",128);plan.push({removed:rel});}
    else if(isDirPath(full)){plan.push({files:filesBelowIgnored(rules,full)});plan.push({prune:relative(full)});}
    else plan.push({files:[full]});}
  for(var step of plan){
    if(step.removed!==undefined){for(var key of Array.from(map.keys())){if(key===step.removed||key.indexOf(step.removed+'/')===0)map.delete(key);}}
    else if(step.prune!==undefined){for(var gone of Array.from(map.keys())){if((step.prune===''||gone.indexOf(step.prune+'/')===0)&&!exists(root+'/'+gone))map.delete(gone);}}
    else for(var f of step.files){var r=relative(f);checkName(r);var bytes=gbytes(f);map.set(r,{path:r,id:object('blob',bytes),mode:33188,size:bytes.length});}
  }
  saveIndex(Array.from(map.values()));
}

// Every commit reachable from HEAD, newest committer date first (git log's default order, approximated by date).
function reachableCommits(start){var seen=new Set([start]),queue=[start],found=[];while(queue.length){var id=queue.shift(),c=commitParts(id);found.push(c);for(var p of c.parents){if(!seen.has(p)){seen.add(p);queue.push(p);}}}found.sort(function(a,b){return identityParts(b.committer).ts-identityParts(a.committer).ts;});return found;}
function logCommand(args){
  var limit=Infinity,oneline=false;
  for(var j=0;j<args.length;j++){var a=args[j];if(a==='-n'&&args[j+1]!==undefined)limit=Number(args[++j]);else if(/^-\d+$/.test(a))limit=Number(a.slice(1));else if(/^-n\d+$/.test(a))limit=Number(a.slice(2));else if(a==='--oneline')oneline=true;}
  var head=currentCommit();
  if(!head)fail("fatal: your current branch '"+((headRef()||'HEAD').replace('refs/heads/',''))+"' does not have any commits yet",128);
  var commits=(reachableCommits(head)).slice(0,limit),out=[];
  for(var c of commits){
    if(oneline){out.push(short(c.id)+' '+subjectOf(c.message)+'\n');continue;}
    var who=identityParts(c.author),text='commit '+c.id+'\n';
    if(c.parents.length>1)text+='Merge: '+c.parents.map(short).join(' ')+'\n';
    text+='Author: '+who.name+' <'+who.email+'>\nDate:   '+gitDate(who.ts,who.zone)+'\n\n';
    text+=c.message.replace(/\n$/,'').split('\n').map(function(l){return l?'    '+l:'';}).join('\n')+'\n';
    out.push(text);
  }
  // Full entries end with a newline and are separated by a blank line; --oneline entries are plain lines.
  emit(out.join(oneline?'':'\n'));
}

function lsFiles(args){var stage=false,nul=false,specs=[];for(var a of args){if(a==='-s'||a==='--stage')stage=true;else if(a==='-z')nul=true;else if(a.charAt(0)==='-'&&a.length>1){fail("error: unknown option \x60"+a.replace(/^-+/,'')+"\x27\n"+"usage: git ls-files [<options>] [<file>...]"+'\n'+""+'\n'+"    -z                    separate paths with the NUL character"+'\n'+"    -t                    identify the file status with tags"+'\n'+"    -v                    use lowercase letters for 'assume unchanged' files"+'\n'+"    -f                    use lowercase letters for 'fsmonitor clean' files"+'\n'+"    -c, --[no-]cached     show cached files in the output (default)"+'\n'+"    -d, --[no-]deleted    show deleted files in the output"+'\n'+"    -m, --[no-]modified   show modified files in the output"+'\n'+"    -o, --[no-]others     show other files in the output"+'\n'+"    -i, --[no-]ignored    show ignored files in the output"+'\n'+"    -s, --[no-]stage      show staged contents' object name in the output"+'\n'+"    -k, --[no-]killed     show files on the filesystem that need to be removed"+'\n'+"    --[no-]directory      show 'other' directories' names only"+'\n'+"    --[no-]eol            show line endings of files"+'\n'+"    --[no-]empty-directory"+'\n'+"                          don't show empty directories"+'\n'+"    -u, --[no-]unmerged   show unmerged files in the output"+'\n'+"    --[no-]resolve-undo   show resolve-undo information"+'\n'+"    -x, --exclude <pattern>"+'\n'+"                          skip files matching pattern"+'\n'+"    -X, --exclude-from <file>"+'\n'+"                          read exclude patterns from <file>"+'\n'+"    --[no-]exclude-per-directory <file>"+'\n'+"                          read additional per-directory exclude patterns in <file>"+'\n'+"    --exclude-standard    add the standard git exclusions"+'\n'+"    --full-name           make the output relative to the project top directory"+'\n'+"    --[no-]recurse-submodules"+'\n'+"                          recurse through submodules"+'\n'+"    --[no-]error-unmatch  if any <file> is not in the index, treat this as an error"+'\n'+"    --[no-]with-tree <tree-ish>"+'\n'+"                          pretend that paths removed since <tree-ish> are still present"+'\n'+"    --[no-]abbrev[=<n>]   use <n> digits to display object names"+'\n'+"    --[no-]debug          show debugging data"+'\n'+"    --[no-]deduplicate    suppress duplicate entries"+'\n'+"    --[no-]sparse         show sparse directories in the presence of a sparse index"+'\n'+"    --format <format>     format to use for the output"+'\n',129);}else specs.push(a);}var prefixes=specs.map(function(s){var rel=relative(fromBase(s));return rel==='.'?'':rel;});var sep=nul?'\0':'\n',text='';for(var e of entries()){if(prefixes.length&&!prefixes.some(function(p){return p===''||e.path===p||e.path.indexOf(p+'/')===0;}))continue;text+=(stage?e.mode.toString(8)+' '+e.id+' 0\t':'')+e.path+sep;}emit(text);}
function revParse(args){
  for(var i=0;i<args.length;i++){var a=args[i];
    if(a==='--show-toplevel')emit(root+'\n');
    else if(a==='--git-dir')emit((pathOf('.')===root?'.git':gitdir)+'\n');
    else if(a==='--is-inside-work-tree')emit('true\n');
    else if(a==='--abbrev-ref'){var ref=args[++i]||'HEAD';if(ref!=='HEAD')fail('fatal: unsupported --abbrev-ref '+ref,128);var branch=headRef();emit((branch?branch.replace('refs/heads/',''):'HEAD')+'\n');}
    else if(a==='--short'){emit(short(resolveRevision(args[++i]||'HEAD'))+'\n');}
    else emit((resolveRevision(a))+'\n');
  }
}
// A revision is HEAD or a local branch name. Anything else is git's "ambiguous argument" error.
function resolveRevision(name){
  var id='';
  if(name==='HEAD')id=currentCommit();
  else if(exists(gfile('refs/heads/'+name)))id=gread(gfile('refs/heads/'+name)).trim();
  if(!id)fail("fatal: ambiguous argument '"+name+"': unknown revision or path not in the working tree.\nUse '--' to separate paths from revisions, like this:\n'git <command> [<revision>...] -- [<file>...]'",128);
  return id;
}

function refNames(dirRel){var out=[],base=gitdir+'/'+dirRel;if(!exists(base))return out;(function walk(folder,prefix){for(var item of names(folder)){var n=childName(item);if(!n)continue;var full=folder+'/'+n;if(isDirPath(full))walk(full,prefix+n+'/');else out.push(prefix+n);}})(base,'');return out.sort();}
function branchCommand(args){
  var head=currentCommit(),ref=headRef(),current=ref?ref.replace('refs/heads/',''):null,all=false,remove=null,rest=[];
  for(var i=0;i<args.length;i++){var a=args[i];if(a==='-a'||a==='--all')all=true;else if(a==='-d'||a==='-D'||a==='--delete'){remove=args[++i];if(remove===undefined)fail('fatal: branch name required',128);}else if(a.charAt(0)==='-'&&a.length>1)fail("error: unknown option '"+a+"'",129);else rest.push(a);}
  if(remove!==null){
    if(remove===current)fail("error: Cannot delete branch '"+remove+"' checked out at '"+root+"'",1);
    if(!exists(gfile('refs/heads/'+remove)))fail("error: branch '"+remove+"' not found",1);
    var was=gread(gfile('refs/heads/'+remove)).trim();fs.rm(gfile('refs/heads/'+remove));emit('Deleted branch '+remove+' (was '+short(was)+').\n');return;
  }
  if(rest.length){var name=rest[0];assertBranch(name);if(exists(gfile('refs/heads/'+name)))fail("fatal: a branch named '"+name+"' already exists",128);if(!head)fail("fatal: not a valid object name: '"+(current||'HEAD')+"'",128);gwrite(gfile('refs/heads/'+name),head+'\n');return;}
  var text='';for(var n of refNames('refs/heads'))text+=(n===current?'* ':'  ')+n+'\n';
  // A symbolic remote ref (origin/HEAD) prints its target, as git does: "remotes/origin/HEAD -> origin/master".
  if(all)for(var r of refNames('refs/remotes')){var target=gread(gfile('refs/remotes/'+r)).trim();text+='  remotes/'+r+(target.indexOf('ref: ')===0?' -> '+target.slice(5).replace('refs/remotes/',''):'')+'\n';}
  emit(text);
}

function remoteEntries(){var cfg=isFilePath(gfile('config'))?gread(gfile('config')):'',out=[],re=/\[remote "([^"]+)"\]\s*\n\s*url = (\S+)/g,m;while((m=re.exec(cfg))!==null)out.push({name:m[1],url:m[2]});return out;}
function remoteCommand(args){
  var list=remoteEntries();
  if(!args.length){emit(list.map(function(r){return r.name+'\n';}).join(''));return;}
  if(args[0]==='-v'||args[0]==='--verbose'){emit(list.map(function(r){return r.name+'\t'+r.url+' (fetch)\n'+r.name+'\t'+r.url+' (push)\n';}).join(''));return;}
  if(args[0]==='add'){var name=args[1],url=args[2];if(!name||!url)fail('usage: git remote add <name> <url>',129);if(!/^[A-Za-z0-9_-]+$/.test(name))fail("fatal: '"+name+"' is not a valid remote name",128);if(list.some(function(r){return r.name===name;}))fail('error: remote '+name+' already exists.',3);if(!/^https:\/\/[^@\s]+$/.test(url))fail('git: HTTPS remotes only; credentials in URLs are forbidden');var cfg=isFilePath(gfile('config'))?gread(gfile('config')):'';gwrite(gfile('config'),cfg+'[remote "'+name+'"]\n\turl = '+url+'\n\tfetch = +refs/heads/*:refs/remotes/'+name+'/*\n');return;}
  fail("error: unknown subcommand: '"+args[0]+"'",129);
}

function run(){
// -C DIR runs the command as if started in DIR (repeatable, relative paths build on the previous one). -c sets a config value.
var pre=0;while(argv[pre]==='-c'||argv[pre]==='-C'){if(argv[pre]==='-C'){if(argv[pre+1]===undefined)fail('fatal: -C needs a path');workBase=fromBase(argv[pre+1]);pre+=2;continue;}var kv=argv[pre+1]||'',eq=kv.indexOf('=');if(eq<1)fail('fatal: -c expects name=value, got: '+kv);overrides[kv.slice(0,eq).toLowerCase()]=kv.slice(eq+1);pre+=2;}argv.splice(0,pre);
var cmd=argv.shift()||'help';if(cmd==='--version'||cmd==='version'){emit('git version 2.54.0 (Dusk browser)\n');return;}if(cmd==='help'||cmd==='--help'){emit('usage: git init|add|status|commit|log|branch|remote|rev-parse|clone|fetch|pull|push\n');return;}
if(cmd==='init'){root=fromBase(argv[0]);gitdir=root+'/.git';if(exists(gitdir+'/HEAD')){emit('Reinitialized existing Git repository in '+gitdir+'/\n');return;}mkdirp(gitdir+'/objects');mkdirp(gitdir+'/refs/heads');mkdirp(gitdir+'/refs/tags');gwrite(gfile('HEAD'),'ref: refs/heads/'+DEFAULT_BRANCH+'\n');gwrite(gfile('config'),'[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n');emit('Initialized empty Git repository in '+gitdir+'/\n');return;}
if(cmd==='clone'||cmd==='fetch'||cmd==='pull'||cmd==='push'){if(cmd==='clone'&&argv[1]){argv.display=argv[1];argv[1]=fromBase(argv[1]);}gitRemote(cmd,argv);return;}
repo();
if(cmd==='add'){addCommand(argv);return;}
if(cmd==='status'){statusReport();return;}
if(cmd==='commit'){commitCommand(argv);return;}
if(cmd==='log'){logCommand(argv);return;}
if(cmd==='ls-files'){lsFiles(argv);return;}
if(cmd==='rev-parse'){revParse(argv);return;}
if(cmd==='branch'){branchCommand(argv);return;}
if(cmd==='remote'){remoteCommand(argv);return;}
fail('git: '+cmd+' is not a git command',1);
}
// Everything here is synchronous (the host calls are blocking IPC), so the exit status set by finish() is read by the
// shell when the script ends, the same as the other builtins.
run();
`),
}
