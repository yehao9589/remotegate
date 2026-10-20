const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
 const nodes=new Map(),calls=[],listeners={},timers=new Map();let timer=0,renders=0;
 const node=id=>{if(!nodes.has(id))nodes.set(id,{innerHTML:'',textContent:'',hidden:false,attributes:{},classes:new Set(),classList:{add(c){nodes.get(id).classes.add(c)},toggle(c,on){on?nodes.get(id).classes.add(c):nodes.get(id).classes.delete(c)}},setAttribute(k,v){this.attributes[k]=v}});return nodes.get(id)};
 const esc=x=>String(x??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 const result={current:{version:'0.1.15',packageVersion:'0.1.15-1',commit:'development'},status:'available',checkedAt:'2026-10-19T12:00:00Z',latest:{version:'0.1.16',title:'新版本说明',notes:'- 修复连接\n### 升级说明\n保留数据',publishedAt:'2026-10-19T10:00:00Z'},history:[{version:'0.1.15',notes:'- 当前版本变化'},{version:'0.1.14',notes:'- 历史说明'}]};
 const context=vm.createContext({console,Date,Number,String,AbortController,authTransition:1,names:{},consolePageVisuals:{},page:'overview',state:{package:{server:result.current}},query:'',filter:'',location:{hash:'',pathname:'/admin',search:''},history:{pushState(){context.location.hash=''},replaceState(){context.location.hash=''}},window:{scrollTo(){},addEventListener(k,v){listeners[k]=v}},document:{querySelectorAll(){return []}},$:node,esc,uiIcon:()=>'<svg></svg>',setTimeout(fn){timers.set(++timer,fn);return timer},clearTimeout(id){timers.delete(id)},api:async(path,method,body,options)=>{calls.push({path,method,body,options});return structuredClone(path==='version/notes'?{current:result.current,history:result.history,status:'unchecked'}:result)},refresh:()=>Promise.resolve(),render(){renders++;if(context.page==='version')context.renderServerVersion()}});
 vm.runInContext(fs.readFileSync('cmd/server/web/server-version.js','utf8'),context);
 return {context,node,calls,listeners,result,timers,renders:()=>renders,eval:code=>vm.runInContext(code,context)};
}
test('footer opens a dedicated page with current, latest and offline release notes',async()=>{
 const f=fixture();f.node('#serverVersionEntry').onclick();await f.context.loadServerRelease();
 assert.equal(f.context.page,'version');assert.equal(f.context.location.hash,'/version');
 const html=f.node('#content').innerHTML;
 assert.match(html,/v0\.1\.15/);assert.match(html,/v0\.1\.16/);assert.match(html,/当前版本变化/);assert.match(html,/历史说明/);assert.match(html,/先备份数据/);assert.match(html,/随本机程序提供/);
 assert.match(f.node('#serverUpdateStatus').textContent,/发现新版本 v0.1.16/);assert.equal(f.node('#serverVersionEntry').classes.has('has-update'),true);assert.equal(f.calls.length,2);
 f.node('#versionBack').onclick();assert.equal(f.context.page,'overview');
});
test('remote notes and titles are escaped and untrusted URLs cannot become links',async()=>{
 const f=fixture();f.result.latest.version='0.1.16';f.result.latest.url='javascript:alert(1)';f.result.latest.title='<img src=x onerror=alert(1)>';f.result.latest.notes='<script>alert(1)</script>\n![bad](https://tracker.invalid/x)\n```html\n<img src=x>\n```';
 f.context.page='version';await f.context.loadServerRelease();const html=f.node('#content').innerHTML;
 assert.match(html,/&lt;img/);assert.match(html,/&lt;script/);assert.doesNotMatch(html,/<img|<script|href="javascript:/);assert.match(html,/https:\/\/github.com\/yehao9589\/remotegate\/releases\/tag\/v0.1.16/);
 assert.equal(f.context.releasePageURL('evil/../x'),'https://github.com/yehao9589/remotegate/releases/latest');
});
test('failure cannot be displayed as current and retains last known notes',async()=>{
 const f=fixture();f.context.page='version';await f.context.loadServerRelease();
 f.context.api=async()=>{throw Error('网络不可用')};await f.context.loadServerRelease(true);
 const html=f.node('#content').innerHTML;assert.match(html,/检测失败/);assert.match(html,/网络不可用/);assert.match(html,/当前版本变化/);assert.match(html,/上次获取的正式版说明/);assert.doesNotMatch(f.node('#serverUpdateStatus').textContent,/已是最新/);assert.equal(f.node('#serverVersionEntry').classes.has('has-update'),false);
 assert.equal(f.context.versionDate('0001-01-01T00:00:00Z'),'尚未成功检测');
});
test('checks join in flight, auto polling is cached and late logout results are discarded',async()=>{
 const f=fixture();let release,started,calls=0;const ready=new Promise(r=>started=r);f.context.api=async(path)=>{if(path==='version/notes')return {current:f.result.current,history:f.result.history,status:'unchecked'};calls++;started();await new Promise(r=>release=r);return f.result};
 const pending=f.context.loadServerRelease();f.context.ensureServerReleaseCheck();f.context.loadServerRelease(true);await ready;assert.equal(calls,1);
 f.context.authTransition++;f.node('#serverUpdateStatus').textContent='logged out';release();await pending;
 assert.equal(f.node('#serverUpdateStatus').textContent,'logged out');assert.equal(f.eval('serverReleaseSnapshot.status'),'unchecked');assert.equal(f.eval('serverReleaseSnapshot.latest'),undefined);
 f.context.api=async()=>f.result;await f.context.loadServerRelease();f.context.ensureServerReleaseCheck();assert.equal(f.timers.size,0);assert.equal(f.eval('serverReleaseSnapshot.status'),'available');
});
test('an interrupted check clears its button and a new account has its own state',async()=>{
 const f=fixture();f.context.api=async(path,method,body,{signal})=>new Promise((resolve,reject)=>signal.addEventListener('abort',()=>reject(Object.assign(Error('abort'),{name:'AbortError'}))));
 f.context.page='version';f.eval('serverReleaseSnapshot={history:[],status:"unchecked"};serverReleaseOwner=authTransition');const pending=f.context.loadServerRelease();f.context.renderServerVersion();assert.match(f.node('#content').innerHTML,/disabled aria-busy/);
 [...f.timers.values()][0]();await pending;assert.match(f.node('#content').innerHTML,/版本检测超时/);assert.doesNotMatch(f.node('#content').innerHTML,/disabled aria-busy/);
 f.context.authTransition++;f.context.resetServerReleaseOwner();assert.equal(f.eval('serverReleaseSnapshot'),null);
});

test('offline notes render before a slow release service and equal versions are not duplicated',async()=>{
 const f=fixture();let finish,started;const ready=new Promise(r=>started=r);
 f.context.page='version';f.context.api=async path=>{
  if(path==='version/notes')return {current:f.result.current,history:f.result.history,status:'unchecked'};
  started();await new Promise(r=>finish=r);return {...f.result,latest:{...f.result.latest,version:'0.1.15'},status:'current'};
 };
 const pending=f.context.loadServerRelease();await ready;
 assert.match(f.node('#content').innerHTML,/当前版本变化/);assert.match(f.node('#content').innerHTML,/正在检测/);
 finish();await pending;
 const html=f.node('#content').innerHTML;assert.equal((html.match(/当前版本变化/g)||[]).length,1);assert.doesNotMatch(html,/最新正式版说明/);assert.match(html,/官方发布页/);
 assert.equal(f.context.releaseInline('**重点** `命令` <img>'),'<strong>重点</strong> <code>命令</code> &lt;img&gt;');
});
