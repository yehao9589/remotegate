const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
 const nodes=new Map(),requests=[],baseActions=[],opened=[],timers=new Map();let timer=0,confirm=true;
 const esc=x=>String(x??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 function node(id){if(!nodes.has(id))nodes.set(id,{innerHTML:'',textContent:'',disabled:false,hidden:false,value:'',classList:{remove(){},toggle(){}},querySelectorAll(){return []},querySelector(){return null},setAttribute(){},appendChild(){},showModal(){},close(){},focus(){},setSelectionRange(){}});return nodes.get(id)}
 const context=vm.createContext({console,Map,Set,Date,Number,String,URL,AbortController,encodeURIComponent,authTransition:1,customMappingDomain:'__custom__',location:{host:'console.example.com'},query:'',filter:'',editing:null,state:{devices:[{id:'router-one',name:'主路由器',online:true,version:'0.1.3',connectedAt:new Date(Date.now()-65000).toISOString(),info:{}}],mappings:[{id:'map-one',deviceId:'router-one',host:'service.example.com',target:'http://192.168.1.1:80',note:'管理页',enabled:true,publicScheme:'https',publicPort:443}]},$:node,esc,uiIcon:()=>'',expandedFeatures:new Map(),renderWorkbench(){},deviceCards(){},mappingRow:m=>'<tr><td>'+esc(m.host)+'</td><td><button class="danger" data-action="deleteMapping">删除</button></td></tr>',pluginUpdateButton:()=>'',toggleFeature(){},action:async(a,id)=>{baseActions.push({a,id})},openEditor:(type,item)=>{opened.push({type,item});context.editing={type,item}},copyText:async()=>{},toast(){},refresh:async()=>{},api:async(path,method,body,options)=>{requests.push({path,method,body,options});return {latencyMs:27,checkedAt:new Date().toISOString()}},confirm:()=>confirm,setTimeout:fn=>{timers.set(++timer,fn);return timer},clearTimeout:id=>timers.delete(id),document:{activeElement:null,getElementById:node,createElement:()=>node('option')},FormData:class{constructor(){this.fields=new Map([['name','新名称'],['note','路由器备注'],['pinned','on']])}get(k){return this.fields.get(k)}has(k){return this.fields.has(k)}}});
 node('#editor').onsubmit=async()=>{};
 vm.runInContext(fs.readFileSync('cmd/server/web/device-center.js','utf8'),context);
 return {context,node,requests,baseActions,opened,timers,confirm:value=>confirm=value,eval:code=>vm.runInContext(code,context)};
}
test('cards mask IDs, show honest legacy information and escape device notes',()=>{
 const f=fixture();f.context.state.devices[0].note='<img src=x onerror=alert(1)>';f.context.deviceCards();
 const html=f.node('#deviceCards').innerHTML;
 assert.match(html,/rou••••one/);assert.match(html,/&lt;img/);assert.doesNotMatch(html,/<img/);assert.match(html,/升级插件后/);assert.match(html,/隧道延迟 待检测/);
 assert.match(html,/复制新增映射/);assert.match(html,/本次在线 1 分钟/);
});
test('pin sorting, saved-note search and pinned filter work together',()=>{
 const f=fixture();f.context.state.devices.push({id:'pinned',name:'办公室',note:'NAS',online:false,pinned:true,info:{}});
 f.context.deviceCards();assert.ok(f.node('#deviceCards').innerHTML.indexOf('办公室')<f.node('#deviceCards').innerHTML.indexOf('主路由器'));
 f.context.query='nas';f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/办公室/);assert.doesNotMatch(f.node('#deviceCards').innerHTML,/主路由器/);
 f.context.query='';f.context.filter='pinned';f.context.deviceCards();assert.doesNotMatch(f.node('#deviceCards').innerHTML,/主路由器/);
});
test('mapping duplicate creates an unsaved draft retaining the target and ports',async()=>{
 const f=fixture();f.context.state.domains=[{baseDomain:'other.test'},{baseDomain:'example.com'}];await f.context.action('duplicate','map-one');
 assert.equal(f.requests.length,0);const {type,item}=f.opened[0];assert.equal(type,'mapping');assert.equal(item.host,'');assert.equal(item.id,undefined);assert.equal(item.target,'http://192.168.1.1:80');assert.equal(item.publicPort,443);assert.equal(item.note,'管理页');assert.equal(item._mappingDraft.domainSuffix,'example.com');
 await f.context.action('updatePlugin','router-one');assert.equal(f.baseActions[0].a,'updatePlugin');
});
test('device filters search notes and pause state without replacing the search input',()=>{
 const f=fixture();f.context.state.mappings.push({id:'map-two',deviceId:'router-one',host:'nas.example.com',target:'http://192.168.1.2',note:'备份 NAS',enabled:false});
 f.context.updateDeviceMappingFilter('router-one',{query:'备份',status:'paused'});assert.match(f.node('device-mapping-rows-router-one').innerHTML,/nas.example/);assert.doesNotMatch(f.node('device-mapping-rows-router-one').innerHTML,/service.example/);
 f.context.updateDeviceMappingFilter('router-one',{status:'enabled'});assert.match(f.node('device-mapping-rows-router-one').innerHTML,/没有符合条件/);
});
test('connection health uses its own POST, displays a measured result and never changes online on failure',async()=>{
 const f=fixture();await f.context.action('health','router-one');
 assert.equal(f.requests[0].path,'devices/router-one/health');assert.equal(f.requests[0].method,'POST');assert.equal(f.context.state.devices[0].latencyMs,27);assert.match(f.node('#deviceCards').innerHTML,/往返 27 ms/);
 f.context.api=async()=>{throw Error('隧道响应超时')};await f.context.action('health','router-one');assert.match(f.node('#deviceCards').innerHTML,/隧道响应超时/);assert.equal(f.context.state.devices[0].online,true);assert.equal(f.timers.size,0);
});
test('reconnect requires confirmation and offline devices cannot execute it',async()=>{
 const f=fixture();f.confirm(false);await f.context.action('reconnect','router-one');assert.equal(f.requests.length,0);
 f.confirm(true);await f.context.action('reconnect','router-one');assert.equal(f.requests[0].path,'devices/router-one/reconnect');assert.match(f.node('#deviceCards').innerHTML,/等待客户端恢复/);
 f.context.state.devices[0].online=false;await f.context.action('reconnect','router-one');assert.equal(f.requests.length,1);
});
test('late health response after logout cannot repopulate device status',async()=>{
 const f=fixture();let resolve;f.context.api=()=>new Promise(r=>resolve=r);const pending=f.context.action('health','router-one');f.context.authTransition++;f.node('#deviceCards').innerHTML='logged out';resolve({latencyMs:19,checkedAt:new Date().toISOString()});await pending;assert.equal(f.node('#deviceCards').innerHTML,'logged out');assert.equal(f.context.state.devices[0].latencyMs,undefined);
});
test('pin is a partial update and editing saves name, note and pin together',async()=>{
 const f=fixture();await f.context.action('pinDevice','router-one');assert.deepEqual(JSON.parse(JSON.stringify(f.requests[0].body)),{pinned:true});
 f.context.openEditor('device',f.context.state.devices[0]);assert.match(f.node('#fields').innerHTML,/name="note"/);
 await f.node('#editor').onsubmit({preventDefault(){},target:{}});
 assert.deepEqual(JSON.parse(JSON.stringify(f.requests[1].body)),{name:'新名称',note:'路由器备注',pinned:true});
});
test('private display and filters reset when a different login opens the workspace',async()=>{
 const f=fixture();await f.context.action('revealDevice','router-one');f.context.updateDeviceMappingFilter('router-one',{query:'secret'});f.context.authTransition++;f.context.deviceCards();
 assert.match(f.node('#deviceCards').innerHTML,/rou••••one/);assert.doesNotMatch(f.node('#deviceCards').innerHTML,/value="secret"/);
});

test('public IPv4 shows masked IP and Chinese operator, revealing and copying retain the full address',async()=>{
 const f=fixture(),d=f.context.state.devices[0];d.info.peerIP='110.81.12.34';d.network={ip:d.info.peerIP,kind:'public',status:'known',operator:'中国电信',organization:'CHINANET',asns:[4134],source:'RIPEstat',checkedAt:new Date().toISOString()};
 f.context.deviceCards();let html=f.node('#deviceCards').innerHTML;assert.match(html,/外网 IP/);assert.match(html,/110\.81\.\*\.\*/);assert.match(html,/运营商：中国电信/);assert.match(html,/data-copy="110\.81\.12\.34"/);
 await f.context.action('revealIP',d.id);assert.match(f.node('#deviceCards').innerHTML,/<code>110\.81\.12\.34<\/code>/);
 await f.context.action('revealIP',d.id);assert.match(f.node('#deviceCards').innerHTML,/110\.81\.\*\.\*/);
 f.context.query='中国电信';f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/主路由器/);
});
test('IPv6, private IP, offline IP and missing operator remain accurately labeled',()=>{
 const f=fixture(),d=f.context.state.devices[0];d.info.peerIP='240e:1234:5678::1';d.network={ip:d.info.peerIP,kind:'public',status:'pending'};d.online=false;
 f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/上次外网 IP/);assert.match(f.node('#deviceCards').innerHTML,/240e:1234:••••/);assert.match(f.node('#deviceCards').innerHTML,/识别中/);
 d.info.peerIP='127.0.0.1';d.network={ip:d.info.peerIP,kind:'loopback',status:'local'};f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/本机 IP/);assert.match(f.node('#deviceCards').innerHTML,/运营商：不适用/);
 d.info.peerIP='110.81.1.2';d.network={ip:d.info.peerIP,kind:'public',status:'unknown'};f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/运营商：未知/);assert.doesNotMatch(f.node('#deviceCards').innerHTML,/中国电信/);
});
test('an IP change or a new login hides the new address and rejects stale operator metadata',async()=>{
 const f=fixture(),d=f.context.state.devices[0];d.info.peerIP='110.81.12.34';d.network={ip:d.info.peerIP,kind:'public',status:'known',operator:'中国电信'};
 await f.context.action('revealIP',d.id);d.info.peerIP='1.1.1.1';f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/<code>1\.1\.\*\.\*<\/code>/);assert.match(f.node('#deviceCards').innerHTML,/运营商：未知/);
 await f.context.action('revealIP',d.id);f.context.authTransition++;f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/<code>1\.1\.\*\.\*<\/code>/);
});
