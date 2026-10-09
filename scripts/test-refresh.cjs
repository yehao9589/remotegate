const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
function refreshPage(apiImpl){
 const nodes=new Map(),calls=[],notices=[];let renders=0,timer;
 const $=selector=>{if(!nodes.has(selector))nodes.set(selector,{disabled:false,textContent:'',innerHTML:'',attributes:{},classes:new Set(),classList:{toggle(name,on){on?nodes.get(selector).classes.add(name):nodes.get(selector).classes.delete(name)}},setAttribute(key,value){this.attributes[key]=value}});return nodes.get(selector)};
 const context=vm.createContext({$,AbortController,Date,authTransition:0,state:{devices:[{id:'old'}],mappings:[]},setTimeout(fn){timer=fn;return 1},clearTimeout(){timer=null},uiIcon:()=>'<svg></svg>',toast:message=>notices.push(message),render(){renders++},api:async(path,method,body,options)=>{calls.push(path);return apiImpl(path,options)}});
 vm.runInContext(fs.readFileSync('cmd/server/web/refresh.js','utf8'),context);
 return {context,$,calls,notices,renders:()=>renders,timeout:()=>timer()};
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
 const refresh=page.context.refreshManually();page.timeout();await refresh;
 assert.match(page.notices[0],/刷新超时/);assert.equal(page.$('#refresh').disabled,false);assert.equal(page.renders(),0);
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
