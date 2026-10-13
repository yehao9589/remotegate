const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
 const nodes=new Map(),requests=[],deleted=[],timers=new Map();let timer=0,context;
 const esc=x=>String(x??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 function node(id){if(!nodes.has(id))nodes.set(id,{value:'',innerHTML:'',textContent:'',hidden:false,disabled:false,checked:false,open:id==='#remoteDialog',querySelectorAll:()=>[],replaceChildren(){this.innerHTML=''},close(){this.open=false},showModal(){this.open=true},classList:{toggle(){}},addEventListener(){}});return nodes.get(id)}
 context=vm.createContext({console,Set,Map,URL,Number,String,Date,Uint8Array,AbortController,btoa:x=>Buffer.from(x,'binary').toString('base64'),esc,$:node,uiIcon:()=>'',authTransition:1,state:{devices:[{id:'router',name:'路由器',online:true,canRemote:true}]},action:async()=>{},renderWorkbench(){},api:async(path,method,body)=>{requests.push({path,method,body:JSON.parse(JSON.stringify(body||{})),source:body});if(path.endsWith('/files'))return {path:'/',home:'/',entries:[]};return {id:'session-one',target:'127.0.0.1:22',terminalTicket:'fixture-ticket'}},fetch:async(url)=>{deleted.push(url);return {}},toast(){},prompt:()=>null,confirm:()=>false,window:{addEventListener(){}},document:{body:{insertAdjacentHTML(){}},head:{appendChild(){}},createElement:()=>({})},WebSocket:{OPEN:1},location:{origin:'https://console.example.com',protocol:'https:'},setTimeout:fn=>{timers.set(++timer,fn);return timer},clearTimeout:id=>timers.delete(id)});
 vm.runInContext(fs.readFileSync('cmd/server/web/remote-access.js','utf8'),context);
 node('#remoteHost').value='127.0.0.1';node('#remotePort').value='22';node('#remoteUsername').value='fixture';node('#remoteAuthMethod').value='password';node('#remotePassword').value='fixture-password';
 return {context,node,requests,deleted,timers,eval:code=>vm.runInContext(code,context)};
}
test('remote tools are escaped and offline devices cannot open a connection',()=>{
 const f=fixture();const html=f.context.remoteDeviceTabs({id:'"><img src=x>',online:false});
 assert.match(html,/&quot;&gt;&lt;img/);assert.doesNotMatch(html,/<img/);assert.equal((html.match(/disabled/g)||[]).length,2);
 f.context.state.devices[0].online=false;f.context.openRemote('router','files');assert.equal(f.requests.length,0);
});
test('binary upload encoding preserves zero bytes, high bytes and UTF-8',()=>{
 const f=fixture(),bytes=Uint8Array.from([0,1,127,128,255,228,184,173]);
 assert.deepEqual(Buffer.from(f.context.remoteBase64(bytes.buffer),'base64'),Buffer.from(bytes));
 assert.equal(f.context.remoteParent('/root/data/'),'/root');assert.equal(f.context.remoteJoin('/','a.txt'),'/a.txt');
});
test('connecting requires confirmed fingerprint and changing target invalidates it',async()=>{
 const f=fixture();f.context.openRemote('router','files');const c=f.eval('remoteContext');
 await f.context.connectRemote(c);assert.equal(f.requests.length,0);assert.match(f.node('#remoteLoginError').textContent,/主机指纹/);
 c.fp={host:'127.0.0.1',port:22,fingerprint:'SHA256:fixture'};f.node('#remoteTrust').checked=true;
 f.node('#remoteHost').value='192.168.1.2';f.node('#remoteHost').oninput();assert.equal(c.fp,null);assert.equal(f.node('#remoteTrust').checked,false);
});
test('connected UI clears private credentials and never puts them in session state',async()=>{
 const f=fixture();f.context.openRemote('router','files');const c=f.eval('remoteContext');
 c.fp={host:'127.0.0.1',port:22,fingerprint:'SHA256:fixture'};f.node('#remoteTrust').checked=true;
 await f.context.connectRemote(c);
 assert.equal(f.requests[0].body.password,'fixture-password');assert.equal(f.requests[0].source.password,'');
 assert.equal(f.node('#remotePassword').value,'');assert.equal(c.session.password,undefined);assert.equal(f.timers.size,0);
 f.context.closeRemote(c);assert.equal(f.deleted[0],'/api/admin/remote/session-one');
});
test('a changed administrator login closes its terminal and file session',async()=>{
 const f=fixture();f.context.openRemote('router','files');const c=f.eval('remoteContext');c.session={id:'old-session'};let disposed=0;c.terminal={dispose(){disposed++}};
 f.context.authTransition++;f.context.renderWorkbench();
 assert.equal(disposed,1);assert.equal(c.closed,true);assert.equal(f.eval('remoteContext'),null);assert.equal(f.deleted[0],'/api/admin/remote/old-session');
});
test('expired SSH sessions give a recovery message instead of a raw 404',async()=>{
 const f=fixture();f.context.openRemote('router','files');const c=f.eval('remoteContext');c.session={id:'expired'};
 f.context.api=async()=>{const error=Error('404 page not found');error.status=404;throw error};
 await assert.rejects(f.context.remoteCall(c,'remote/expired/files','POST',{action:'list'}),/SSH 会话已结束/);
 assert.equal(f.timers.size,0);assert.equal(c.expired,true);assert.equal(f.node('#remoteDisconnect').textContent,'重新连接');
 f.node('#remoteDisconnect').onclick();assert.equal(c.closed,true);assert.equal(f.eval('remoteContext').session,null);assert.equal(f.node('#remoteDisconnect').textContent,'断开连接');
});
test('offline device status ends remote UI and closes the terminal connection',()=>{
 const f=fixture();f.context.openRemote('router','files');const c=f.eval('remoteContext');c.session={id:'offline'};let closed=0;c.socket={close(){closed++}};
 f.context.state.devices[0].online=false;f.context.renderWorkbench();
 assert.equal(c.expired,true);assert.equal(closed,1);assert.equal(f.node('#remoteSessionBadge').textContent,'会话已结束');
});
test('a lost upload completion reply does not claim the target file was unchanged',async()=>{
 const f=fixture();f.context.openRemote('router','files');const c=f.eval('remoteContext');c.session={id:'upload'};
 f.context.remoteFileCall=async(c,body)=>{
  if(body.action==='upload-start')return {uploadId:'pending'};
  if(body.action==='upload-finish')throw Error('completion reply lost');
  if(body.action==='list')return {path:'/',home:'/',entries:[]};
  return {ok:true};
 };
 await f.context.uploadRemoteFile(c,{name:'empty.txt',size:0});
 assert.match(f.node('#remoteUploadProgress').textContent,/结果未确认/);assert.equal(c.upload,false);
});
