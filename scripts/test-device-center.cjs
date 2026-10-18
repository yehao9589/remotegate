const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
 const nodes=new Map(),requests=[],baseActions=[],opened=[],timers=new Map();let timer=0,confirm=true;
 const esc=x=>String(x??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 function node(id){if(!nodes.has(id))nodes.set(id,{innerHTML:'',textContent:'',disabled:false,hidden:false,value:'',classList:{remove(){},toggle(){}},querySelectorAll(){return []},querySelector(){return null},setAttribute(){},appendChild(){},showModal(){},close(){},focus(){},setSelectionRange(){}});return nodes.get(id)}
 const context=vm.createContext({console,Map,Set,Date,Number,String,URL,AbortController,encodeURIComponent,authTransition:1,customMappingDomain:'__custom__',location:{host:'console.example.com'},query:'',filter:'',editing:null,state:{devices:[{id:'router-one',name:'主路由器',online:true,version:'0.1.3',connectedAt:new Date(Date.now()-65000).toISOString(),info:{}}],mappings:[{id:'map-one',deviceId:'router-one',host:'service.example.com',target:'http://192.168.1.1:80',note:'管理页',enabled:true,publicScheme:'https',publicPort:443}]},$:node,esc,uiIcon:()=>'',expandedFeatures:new Map(),renderWorkbench(){},render(){context.deviceCards()},deviceCards(){},mappingRow:m=>'<tr><td>'+esc(m.host)+'</td><td><button class="danger" data-action="deleteMapping">删除</button></td></tr>',pluginUpdateButton:()=>'',toggleFeature(){},action:async(a,id)=>{baseActions.push({a,id})},openEditor:(type,item)=>{opened.push({type,item});context.editing={type,item}},copyText:async()=>{},toast(){},refresh:async()=>{},api:async(path,method,body,options)=>{requests.push({path,method,body,options});return {latencyMs:27,checkedAt:new Date().toISOString()}},confirm:()=>confirm,setTimeout:fn=>{timers.set(++timer,fn);return timer},clearTimeout:id=>timers.delete(id),document:{activeElement:null,getElementById:node,createElement:()=>node('option')},FormData:class{constructor(){this.fields=new Map([['name','新名称'],['note','路由器备注'],['pinned','on']])}get(k){return this.fields.get(k)}has(k){return this.fields.has(k)}}});
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

test('device and mapping traffic show their own cumulative counters even while offline',()=>{
 const f=fixture();f.context.state.traffic={since:'2026-10-19T00:00:00Z',devices:{'router-one':{uploadBytes:1024,downloadBytes:2048}},mappings:{'map-one':{uploadBytes:256,downloadBytes:768},other:{uploadBytes:999999,downloadBytes:999999}}};
 f.context.state.devices[0].online=false;f.context.deviceCards();
 const html=f.node('#deviceCards').innerHTML;
 assert.match(html,/累计流量 <b>3.00 KiB/);assert.match(html,/↑ 上传 <b>1.00 KiB/);assert.match(html,/↓ 下载 <b>2.00 KiB/);
 assert.match(html,/<th>使用流量<\/th>/);
 const row=f.context.mappingTrafficHTML('map-one');assert.match(row,/1.00 KiB/);assert.match(row,/↑ 256 B/);assert.match(row,/↓ 768 B/);assert.doesNotMatch(row,/999999/);
 assert.match(html,/之前的流量不回填/);assert.match(html,/SSH 和文件传输/);
});

test('traffic formatting handles zero, missing, invalid counters and large transfers',()=>{
 const f=fixture();
 for(const n of [0,undefined,-1,NaN,Infinity,'<script>'])assert.equal(f.context.trafficBytes(n),'0 B');
 assert.equal(f.context.trafficBytes(1),'1 B');assert.equal(f.context.trafficBytes(1024**3),'1.00 GiB');assert.equal(f.context.trafficBytes(1024**4*4),'4.00 TiB');
 assert.match(f.context.mappingTrafficHTML('missing'),/0 B/);
 f.context.state.mappings=[];assert.match(f.context.deviceMappingRows(f.context.state.devices[0]),/colspan="5"/);
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

test('each device has a named refresh action available offline without expanding domains',async()=>{
 const f=fixture();f.context.state.devices[0].online=false;f.context.deviceCards();const html=f.node('#deviceCards').innerHTML;
 assert.match(html,/data-action="refreshDevice"[^>]*>刷新状态/);assert.doesNotMatch(html,/data-action="refreshDevice"[^>]*disabled/);
 assert.match(html,/id="domains-router-one" hidden/);assert.match(html,/data-feature="history"/);
 let globalRefreshes=0,renderedOnline=-1;f.context.refresh=async()=>globalRefreshes++;f.context.render=()=>{renderedOnline=f.context.state.devices.filter(d=>d.online).length;f.context.deviceCards()};
 f.context.state.devices.push({id:'other',name:'另一台',online:false});
 f.context.state.traffic={devices:{'router-one':{uploadBytes:0},other:{uploadBytes:99}},mappings:{}};
 f.context.api=async(path,method)=>{f.requests.push({path,method});return {device:{id:'router-one',name:'主路由器',online:true,info:{}},traffic:{uploadBytes:123,downloadBytes:456},probe:'ok',checkedAt:new Date().toISOString()}};
 await f.context.action('refreshDevice','router-one');
 assert.deepEqual(f.requests.map(r=>r.path),['devices/router-one/status']);assert.equal(f.requests[0].method,'POST');
 assert.equal(f.context.state.devices[0].online,true);assert.equal(f.context.state.devices[1].online,false);assert.equal(f.context.state.traffic.devices.other.uploadBytes,99);assert.equal(f.context.state.traffic.devices['router-one'].downloadBytes,456);assert.equal(globalRefreshes,0);assert.equal(renderedOnline,1);assert.equal(f.timers.size,0);
 assert.match(f.node('#deviceCards').innerHTML,/设备在线 · 隧道响应正常/);
});
test('single refresh prevents duplicate requests and rejects late responses after logout or deletion',async()=>{
 for(const ending of ['logout','delete']){
  const f=fixture();let resolve,calls=0;f.context.api=()=>{calls++;return new Promise(r=>resolve=r)};
  const pending=f.context.action('refreshDevice','router-one');await f.context.action('refreshDevice','router-one');assert.equal(calls,1);
  assert.match(f.node('#deviceCards').innerHTML,/aria-busy="true"/);
  if(ending==='logout'){f.context.authTransition++;f.node('#deviceCards').innerHTML='logged out'}else f.context.state.devices=[];
  resolve({device:{id:'router-one',online:true},probe:'ok'});await pending;
  if(ending==='logout')assert.equal(f.node('#deviceCards').innerHTML,'logged out');else assert.equal(f.context.state.devices.length,0);
  assert.equal(f.timers.size,0);
 }
});
test('single refresh reports probe failure without inventing offline and preserves data on errors',async()=>{
 const f=fixture();f.context.api=async()=>({device:{...f.context.state.devices[0]},probe:'unresponsive',checkedAt:new Date().toISOString()});
 await f.context.action('refreshDevice','router-one');assert.equal(f.context.state.devices[0].online,true);assert.match(f.node('#deviceCards').innerHTML,/本次响应检测未通过/);
 f.context.api=async()=>{throw Error('网络异常')};await f.context.action('refreshDevice','router-one');assert.equal(f.context.state.devices[0].online,true);assert.match(f.node('#deviceCards').innerHTML,/网络异常/);
 f.context.api=async()=>({device:{id:'wrong'},probe:'ok'});await f.context.action('refreshDevice','router-one');assert.equal(f.context.state.devices[0].id,'router-one');assert.match(f.node('#deviceCards').innerHTML,/状态返回异常/);
});
test('history is fetched separately, escapes text and omits unknown crash durations',async()=>{
 const f=fixture();const now='2026-10-19T12:01:30Z';f.context.api=async(path,method)=>{f.requests.push({path,method});return {limit:100,checkedAt:now,events:[{state:'offline',at:now,connectedAt:'2026-10-19T12:00:00Z',reason:'reconnect'},{state:'offline',at:now,connectedAt:'2026-10-19T12:00:00Z',reason:'server_restart'},{state:'online',at:'<img src=x>',reason:'<script>'}]}};
 f.context.deviceCards();assert.equal(f.requests.length,0);
 await f.context.action('refreshHistory','router-one');assert.equal(f.requests[0].path,'devices/router-one/events');assert.equal(f.requests[0].method,'GET');
 const html=f.context.deviceHistoryPanel(f.context.state.devices[0]);assert.match(html,/1 分钟 30 秒/);assert.match(html,/管理员请求重新连接/);assert.match(html,/重启时发现原连接已失效/);assert.doesNotMatch(html,/<script>|<img/);
 assert.equal(f.context.deviceSessionDuration({state:'offline',at:now,connectedAt:'2026-10-19T12:00:00Z',reason:'server_restart'}),'—');
 assert.equal(f.context.deviceSessionDuration({state:'offline',at:'invalid'}),'—');assert.equal(f.timers.size,0);
 f.context.authTransition++;f.context.deviceCards();assert.doesNotMatch(f.context.deviceHistoryPanel(f.context.state.devices[0]),/1 分钟 30 秒/);
});
test('history errors are recoverable and late history after logout cannot restore private records',async()=>{
 const f=fixture();f.context.api=async()=>{throw Error('<网络错误>')};await f.context.action('refreshHistory','router-one');assert.match(f.context.deviceHistoryPanel(f.context.state.devices[0]),/&lt;网络错误&gt;/);
 let resolve;f.context.api=()=>new Promise(r=>resolve=r);const pending=f.context.action('refreshHistory','router-one');f.context.authTransition++;f.context.deviceCards();
 resolve({events:[{state:'offline',at:new Date().toISOString(),reason:'reconnect'}]});await pending;
 assert.doesNotMatch(f.context.deviceHistoryPanel(f.context.state.devices[0]),/管理员请求重新连接/);assert.equal(f.timers.size,0);
});
