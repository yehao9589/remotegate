const {test}=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');
function fixture({idle=60,background=1}={}){
 let now=1000,reloads=0;const requests=[],sockets=[],sources=[],timers=[],listeners=new Map(),elements=new Map();
 function element(){return {children:[],setAttribute(){},append(...nodes){this.children.push(...nodes);for(const node of nodes)if(node.id)elements.set(node.id,node)}}}
 const body=element();body.append=function(node){this.children.push(node);if(node.id)elements.set(node.id,node)};
 const document={currentScript:{dataset:{token:'a'.repeat(64),idle:String(idle),background:String(background)}},hidden:false,body,createElement:element,getElementById:id=>elements.get(id),addEventListener:(name,fn)=>listeners.set(name,fn)};
 class Socket{static OPEN=1;constructor(url){this.url=String(url);this.closed=false;this.listeners=new Map();sockets.push(this)}addEventListener(name,fn){this.listeners.set(name,fn)}close(){this.closed=true;this.listeners.get('close')?.()}}
 class Source{constructor(url){this.url=url;this.closed=false;sources.push(this)}close(){this.closed=true}}
 class XHR{open(method,url){this.method=method;this.url=url}send(){requests.push({xhr:this.url})}}
 const window={WebSocket:Socket,EventSource:Source,XMLHttpRequest:XHR,fetch:async(input,init)=>{requests.push({input,init});return {ok:true,status:204}},addEventListener:(name,fn)=>listeners.set(name,fn)};
 const context=vm.createContext({window,document,location:{href:'https://router.test/ui/metacubexd/',host:'router.test',reload(){reloads++}},URL,DOMException,AbortController,TypeError,Number,String,Boolean,Promise,Set,Date:class extends Date{static now(){return now}},setInterval:fn=>timers.push(fn),encodeURIComponent,decodeURIComponent});
 vm.runInContext(fs.readFileSync('cmd/server/web/browser-activity.js','utf8'),context);
 return {window,document,requests,sockets,sources,elements,reloads:()=>reloads,elapse(ms){now+=ms},advance(ms){now+=ms;for(const fn of timers)fn()},event(name,data={}){listeners.get(name)?.(data)}};
}
test('idle page closes realtime streams and blocks automatic fetch XHR and socket retries',async()=>{
 const f=fixture({idle:2});const ws=new f.window.WebSocket('wss://router.test/traffic?token=a%20b+%2F');const es=new f.window.EventSource('https://router.test/logs');
 assert.match(ws.url,/token=a%20b\+%2F/);assert.match(ws.url,/__remotegate_activity=/);
 f.advance(2100);assert.equal(ws.closed,true);assert.equal(es.closed,true);assert.ok(f.elements.get('remotegate-idle-notice'));
 for(let i=0;i<10;i++)assert.throws(()=>new f.window.WebSocket('wss://router.test/traffic'),/页面已暂停/);
 await assert.rejects(f.window.fetch('https://router.test/connections'),/页面已暂停/);
 const xhr=new f.window.XMLHttpRequest();xhr.open('GET','https://router.test/version');assert.throws(()=>xhr.send(),/页面已暂停/);
 await new Promise(resolve=>setImmediate(resolve));const count=f.requests.length;f.advance(100000);assert.equal(f.requests.length,count);
});
test('only real foreground user input extends the lease and timer reports do not renew by themselves',async()=>{
 const f=fixture({idle:60});const ws=new f.window.WebSocket('wss://router.test/traffic');
 f.advance(50000);f.event('pointerdown',{isTrusted:false});f.advance(11000);assert.equal(ws.closed,true);
 const active=fixture({idle:60});const live=new active.window.WebSocket('wss://router.test/traffic');
 active.advance(50000);active.event('keydown',{isTrusted:true});active.advance(1000);await new Promise(resolve=>setImmediate(resolve));
 assert.equal(JSON.parse(active.requests[0].init.body).active,true);active.advance(20000);assert.equal(live.closed,false);active.advance(40000);assert.equal(live.closed,true);
});
test('background grace pauses the page and visibility alone never resumes it',async()=>{
 const f=fixture();const ws=new f.window.WebSocket('wss://router.test/memory');f.document.hidden=true;f.event('visibilitychange');await new Promise(resolve=>setImmediate(resolve));
 f.advance(1100);assert.equal(ws.closed,true);f.document.hidden=false;f.event('visibilitychange');assert.throws(()=>new f.window.WebSocket('wss://router.test/memory'));
 const button=f.elements.get('remotegate-idle-notice').children[2];button.onclick({isTrusted:false});assert.equal(f.reloads(),0);button.onclick({isTrusted:true});assert.equal(f.reloads(),1);
});
test('unrelated origins remain untouched and explicit disable keeps the native constructors',async()=>{
 const f=fixture({idle:1});const external=new f.window.WebSocket('wss://other.test/live');f.advance(2000);assert.equal(external.closed,false);assert.doesNotMatch(external.url,/remotegate_activity/);
 await f.window.fetch('https://other.test/ip');assert.equal(f.requests.at(-1).input,'https://other.test/ip');
 const disabled=fixture({idle:0,background:0});const ws=new disabled.window.WebSocket('wss://router.test/live');disabled.advance(99999999);assert.equal(ws.closed,false);assert.doesNotMatch(ws.url,/remotegate_activity/);
});
test('a frozen background timer cannot reset its expired grace just by becoming visible',async()=>{
 const f=fixture();const ws=new f.window.WebSocket('wss://router.test/traffic');f.document.hidden=true;f.event('visibilitychange');await new Promise(resolve=>setImmediate(resolve));
 f.elapse(2000);f.document.hidden=false;f.event('visibilitychange');assert.equal(ws.closed,true);assert.ok(f.elements.get('remotegate-idle-notice'));
});
