const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const context=vm.createContext({document:{currentScript:null,readyState:'loading',addEventListener(){}},location:{pathname:'/cgi-bin/luci/admin/services/remotegate'}});
vm.runInContext(fs.readFileSync('deploy/openwrt/luci/page.js','utf8'),context);
const view=status=>context.remotegateConnectionView(status);

test('network errors have one bounded recovery window but credential errors stay immediate',()=>{
 const status={enabled:true,running:true,connection:{state:'reconnecting',lastError:'i/o timeout',recoveryStartedAt:new Date(Date.now()-10000).toISOString()}};
 let result=view(status);assert.equal(result.label,'正在恢复连接');assert.equal(result.error,'');assert.equal(result.className,'is-retrying');
 status.connection.recoveryStartedAt=new Date(Date.now()-31000).toISOString();result=view(status);assert.match(result.error,/timeout/);
 status.connection.recoveryStartedAt=new Date().toISOString();status.connection.lastError='connect: 401 Unauthorized';assert.match(view(status).error,/401/);
});

test('a running process alone never claims a connected tunnel',()=>{
 assert.equal(view({enabled:true,running:true}).state,'unknown');
 assert.doesNotMatch(view({enabled:true,running:true}).label,/已连接/);
 assert.equal(view({enabled:false,running:true}).state,'disabled');
 assert.equal(view({enabled:true,running:false}).state,'stopped');
});
test('a confirmed tunnel reports its real address and encrypted DNS recovery',()=>{
 const result=view({enabled:true,running:true,connection:{state:'connected',serverIP:'192.0.2.1',dnsMode:'encrypted',connectedAt:'2026-10-17T17:00:00Z',lastError:'previous timeout'}});
 assert.equal(result.label,'已连接服务器');assert.equal(result.className,'is-running');assert.equal(result.error,'');
 assert.match(result.note,/加密 DNS/);assert.match(result.note,/192\.0\.2\.1/);assert.notEqual(result.connectedAt,'尚未成功连接');
 assert.doesNotMatch(result.note,/已.*建立直连|直连地址/);assert.match(result.note,/不能据此确认/);
});
test('WAN policy is shown separately from DNS and names the actual bound interface',()=>{
 const result=view({enabled:true,running:true,connection:{state:'connected',serverIP:'192.0.2.1',dnsMode:'encrypted',egressInterface:'pppoe-wan',routeProtected:true}});
 assert.match(result.note,/已绑定出口 pppoe-wan/);assert.match(result.note,/公网出口以服务端记录为准/);assert.match(result.note,/解析成功不代表已绕过代理/);
});
test('retrying shows the actual failure and retry time without losing the last successful connection',()=>{
 const result=view({enabled:true,running:true,connection:{state:'reconnecting',lastError:'dial tcp 198.18.1.112:443: i/o timeout',connectedAt:'2026-10-17T17:00:00Z',nextRetryAt:'2026-10-17T17:01:00Z'}});
 assert.equal(result.className,'is-retrying');assert.match(result.error,/198\.18\.1\.112/);assert.match(result.note,/下次重试/);assert.notEqual(result.connectedAt,'尚未成功连接');
});

test('a failed status request clears the previous connected badge',async()=>{
 const elements=new Map(),listeners={};
 function element(id){if(id==='rg-update-check'||id==='rg-update-start')return null;if(!elements.has(id))elements.set(id,{textContent:'',className:'',hidden:false,addEventListener(type,fn){listeners[id+':'+type]=fn},replaceChildren(){this.children=[]},appendChild(node){(this.children||=[]).push(node)}});return elements.get(id)}
 const root=element('cbi-remotegate');root.querySelectorAll=()=>[];
 let fail=false,legacy=false;
 const page=vm.createContext({document:{currentScript:null,readyState:'complete',hidden:false,body:{classList:{add(){}}},getElementById:element,createElement:()=>({}),createTextNode:text=>({text}),addEventListener(){}},location:{pathname:'/cgi-bin/luci/admin/services/remotegate'},window:{setInterval(){},addEventListener(){}},setTimeout,clearTimeout,AbortController,URL,fetch:async()=>{if(fail)throw Error('offline');return {ok:true,json:async()=>({enabled:true,running:true,...(legacy?{}:{connection:{state:'connected'}})})}}});
 vm.runInContext(fs.readFileSync('deploy/openwrt/luci/page.js','utf8'),page);
 await new Promise(resolve=>setImmediate(resolve));
 assert.equal(element('rg-state').className,'is-running');
 legacy=true;listeners['rg-refresh:click']();
 await new Promise(resolve=>setImmediate(resolve));
 assert.equal(element('rg-state').className,'');
 assert.match(element('rg-message').textContent,/接口未更新/);
 legacy=false;
 fail=true;listeners['rg-refresh:click']();
 await new Promise(resolve=>setImmediate(resolve));
 assert.equal(element('rg-state').className,'');
 assert.match(element('rg-state').children[1].text,/读取失败/);
 assert.match(element('rg-message').textContent,/offline/);
});
