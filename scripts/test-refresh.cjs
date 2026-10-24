const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
function refreshPage(apiImpl){
 const nodes=new Map(),calls=[],notices=[];let renders=0,now=1000000,nextTimer=0;const timers=new Map();class Clock extends Date{static now(){return now}}
 const $=selector=>{if(!nodes.has(selector))nodes.set(selector,{disabled:false,textContent:'',innerHTML:'',attributes:{},classes:new Set(),classList:{toggle(name,on){on?nodes.get(selector).classes.add(name):nodes.get(selector).classes.delete(name)}},setAttribute(key,value){this.attributes[key]=value}});return nodes.get(selector)};
 const context=vm.createContext({$,AbortController,Date:Clock,authTransition:0,state:{devices:[{id:'old'}],mappings:[]},setTimeout(fn,delay){const id=++nextTimer;timers.set(id,{fn,at:now+delay});return id},clearTimeout(id){timers.delete(id)},uiIcon:()=>'<svg></svg>',toast:message=>notices.push(message),render(){renders++},api:async(path,method,body,options)=>{calls.push(path);return apiImpl(path,options)}});
 vm.runInContext(fs.readFileSync('cmd/server/web/refresh.js','utf8'),context);
 return {context,$,calls,notices,renders:()=>renders,timeout:()=>{const entry=[...timers].sort((a,b)=>a[1].at-b[1].at)[0];if(entry){timers.delete(entry[0]);now=entry[1].at;entry[1].fn()}},advance:ms=>{now+=ms}};
}
const snapshot=path=>path==='state'?{devices:[{id:'new'}],mappings:[]}:path==='package'?{server:{version:'1.2.3'}}:[{id:'domain'}];
test('manual refresh joins an ongoing update and shows progress, then completion',async()=>{
 let release;const pending=new Promise(resolve=>{release=resolve});
 const page=refreshPage(async path=>{await pending;return snapshot(path)});
 const automatic=page.context.refresh(),manual=page.context.refreshManually();
 assert.equal(page.context.refresh(),automatic);assert.equal(page.calls.length,3);assert.equal(page.$('#refresh').disabled,true);assert.match(page.$('#refresh').innerHTML,/刷新中/);assert.equal(page.$('#sync').textContent,'正在同步…');
 release();await Promise.all([automatic,manual]);
 assert.equal(page.renders(),1);assert.equal(page.context.state.devices[0].id,'new');assert.equal(page.context.state.domain.id,'domain');assert.match(page.$('#serverBuildLabel').textContent,/1.2.3/);assert.match(page.$('#sync').textContent,/已同步/);assert.equal(page.$('#refresh').disabled,false);assert.equal(page.$('#refresh').attributes['aria-busy'],'false');assert.equal(page.$('#refresh').classes.has('is-refreshing'),false);assert.deepEqual(page.notices,['已刷新最新数据']);
});
test('partial failure preserves the current data and reports the error; retry works',async()=>{
 let fail=true;const page=refreshPage(async path=>{if(path==='domains'&&fail)throw Error('域名读取失败');return snapshot(path)});
 await page.context.refreshManually();assert.equal(page.context.state.devices[0].id,'old');assert.equal(page.renders(),0);assert.deepEqual(page.notices,['域名读取失败']);assert.equal(page.$('#refresh').disabled,false);
 fail=false;await page.context.refreshManually();assert.equal(page.context.state.devices[0].id,'new');assert.equal(page.renders(),1);
});
test('a stalled refresh times out and re-enables the button',async()=>{
 const page=refreshPage((path,{signal})=>new Promise((resolve,reject)=>signal.addEventListener('abort',()=>reject(Object.assign(Error('aborted'),{name:'AbortError'})))));
 const refresh=page.context.refreshManually();for(let i=0;i<30&&!page.notices.length;i++){page.timeout();await new Promise(resolve=>setImmediate(resolve))}await refresh;
 assert.match(page.notices[0],/恢复 30 秒/);assert.equal(page.$('#refresh').disabled,false);assert.equal(page.renders(),0);
});
test('responses started before logout cannot replace the current data',async()=>{
 let release;const pending=new Promise(resolve=>{release=resolve});
 const page=refreshPage(async path=>{await pending;return snapshot(path)});const refresh=page.context.refreshManually();page.context.authTransition++;release();await refresh;
 assert.equal(page.context.state.devices[0].id,'old');assert.equal(page.renders(),0);assert.match(page.notices[0],/登录状态已变化/);
});
test('a new login starts its own refresh and old cleanup cannot enable its button',async()=>{
 let releaseOld,releaseNew;
 const oldData=new Promise(resolve=>{releaseOld=resolve}),newData=new Promise(resolve=>{releaseNew=resolve});let count=0;
 const page=refreshPage(async path=>{await (++count<=3?oldData:newData);return snapshot(path)});
 const old=page.context.refresh().catch(()=>{});page.context.authTransition++;
 const current=page.context.refresh();releaseOld();await old;
 assert.equal(page.$('#refresh').disabled,true);assert.equal(page.renders(),0);assert.equal(page.context.refresh(),current);
 releaseNew();await current;assert.equal(page.$('#refresh').disabled,false);assert.equal(page.renders(),1);
});

test('transient failures preserve data and recover quietly before the grace ends',async()=>{
 let fail=true;const page=refreshPage(async path=>{if(fail)throw Object.assign(Error('network'),{name:'TypeError'});return snapshot(path)});
 const pending=page.context.refreshManually();await new Promise(resolve=>setImmediate(resolve));
 assert.match(page.$('#sync').textContent,/正在恢复连接/);assert.equal(page.context.state.devices[0].id,'old');assert.deepEqual(page.notices,[]);
 fail=false;page.timeout();await pending;assert.equal(page.context.state.devices[0].id,'new');assert.deepEqual(page.notices,['已刷新最新数据']);
});
test('a permanent authorization failure bypasses the network grace period',async()=>{
 const page=refreshPage(async()=>{throw Object.assign(Error('权限不足'),{status:403})});await page.context.refreshManually();
 assert.deepEqual(page.notices,['权限不足']);assert.equal(page.calls.length,3);assert.equal(page.$('#refresh').disabled,false);
});
test('logout aborts recovery without allowing the old data to render',async()=>{
 const page=refreshPage(async()=>{throw Object.assign(Error('network'),{name:'TypeError'})});const pending=page.context.refresh().catch(()=>{});
 await new Promise(resolve=>setImmediate(resolve));page.context.authTransition++;vm.runInContext('refreshController.abort()',page.context);await pending;
 assert.equal(page.renders(),0);assert.equal(page.context.state.devices[0].id,'old');
});
