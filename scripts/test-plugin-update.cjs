const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
function fixture(){
 const nodes=new Map(),timers=new Map();let nextTimer=0,requests=[],confirmResult=true;
 function node(id){if(!nodes.has(id))nodes.set(id,{hidden:false,disabled:false,innerHTML:'',textContent:'',value:'https://console.example.com',open:true,type:'',listeners:{},classList:{remove(){}},addEventListener(name,fn){this.listeners[name]=fn},showModal(){this.open=true},close(){this.open=false;this.listeners.close?.()},querySelectorAll(){return []}});return nodes.get(id)}
 const context=vm.createContext({console,Map,Date,AbortController,encodeURIComponent,clearInterval(){},setTimeout(fn){let id=++nextTimer;timers.set(id,fn);return id},clearTimeout(id){timers.delete(id)},state:{devices:[{id:'router',name:'测试路由器',online:true,canUpdate:true,packageVersion:'0.1.7-1'}]},authTransition:1,installPoll:null,editing:null,location:{origin:'https://console.example.com'},$:node,esc:String,uiIcon:()=>'',renderPackages(){},action(){},api:async(path,method,body,options)=>{requests.push({path,method,body,options});return context.nextResult},copyText:async()=>{},confirm:()=>confirmResult});
 vm.runInContext(fs.readFileSync('cmd/server/web/plugin-update.js','utf8'),context);
 const view={online:true,canUpdate:true,available:true,installedVersion:'0.1.7-1',manifest:{packageVersion:'0.1.8-1',sha256:'a'.repeat(64)},state:{phase:'idle'}};context.nextResult=view;
 return {context,node,timers,requests,view,confirm(value){confirmResult=value}};
}
test('checking is read-only, updating requires explicit confirmation and pins the package',async()=>{
 const f=fixture();await f.context.openPluginUpdate('router');assert.equal(f.requests.length,1);assert.equal(f.requests[0].method,'GET');assert.equal(f.node('#save').hidden,false);
 f.confirm(false);await f.node('#save').onclick();assert.equal(f.requests.length,1);
 f.confirm(true);f.context.nextResult={...f.view,state:{phase:'downloading',message:'正在下载'}};await f.node('#save').onclick();
 assert.equal(f.requests[1].method,'POST');assert.equal(f.requests[1].body.sha256,'a'.repeat(64));assert.equal(f.node('#save').hidden,true);assert.equal(f.node('#pluginUpdateCheck').disabled,true);assert.ok(f.timers.size);
});
test('request failure unlocks retry and does not claim success',async()=>{
 const f=fixture();await f.context.openPluginUpdate('router');f.context.api=async()=>{throw Error('校验失败')};await f.node('#save').onclick();
 assert.equal(f.node('#save').disabled,false);assert.equal(f.node('#pluginUpdateCheck').disabled,false);assert.equal(f.node('#formError').textContent,'校验失败');assert.doesNotMatch(f.node('#fields').innerHTML,/更新完成/);
});
test('old plugin gets a preserving bootstrap command instead of an unsupported remote update',async()=>{
 const f=fixture();f.context.nextResult={...f.view,canUpdate:false};await f.context.openPluginUpdate('router');assert.equal(f.node('#save').hidden,true);assert.match(f.node('#fields').innerHTML,/旧版首次升级/);
 f.context.nextResult={command:'temporary single-use command'};await f.node('#pluginUpdateBootstrap').onclick();assert.equal(f.requests[1].path,'devices/router/update/bootstrap');assert.equal(f.requests[1].body.server,'https://console.example.com');assert.equal(f.node('#pluginUpdateCommandText').textContent,'temporary single-use command');assert.equal(f.node('#pluginUpdateBootstrap').disabled,false);
});
test('offline device cannot be updated and does not imply it needs a new installation',async()=>{
 const f=fixture();f.context.nextResult={...f.view,online:false,canUpdate:false};await f.context.openPluginUpdate('router');assert.equal(f.node('#save').hidden,true);assert.match(f.node('#fields').innerHTML,/设备离线/);assert.doesNotMatch(f.node('#fields').innerHTML,/旧版首次升级/);
});
test('closing the dialog aborts polling and a late result cannot overwrite another editor',async()=>{
 const f=fixture();f.context.nextResult={...f.view,state:{phase:'installing'}};await f.context.openPluginUpdate('router');assert.ok(f.timers.size);
 let resolve;f.context.api=()=>new Promise(r=>{resolve=r});const poll=vm.runInContext("requestPluginUpdate(pluginUpdateDialog,'GET')",f.context);f.node('#modal').close();assert.equal(f.timers.size,0);
 f.node('#fields').innerHTML='new mapping editor';resolve({...f.view,state:{phase:'succeeded'}});await poll;assert.equal(f.node('#fields').innerHTML,'new mapping editor');
});
test('logging out ignores stale update responses',async()=>{
 const f=fixture();let resolve;f.context.api=()=>new Promise(r=>{resolve=r});const check=f.context.openPluginUpdate('router');f.context.authTransition++;resolve(f.view);await check;assert.doesNotMatch(f.node('#fields').innerHTML,/服务器提供/);
});
