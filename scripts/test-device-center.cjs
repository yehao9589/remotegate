const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
 const nodes=new Map(),requests=[],baseActions=[],opened=[],timers=new Map();let timer=0,confirm=true;
 const esc=x=>String(x??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 function node(id){if(!nodes.has(id)){const listeners=new Map();nodes.set(id,{innerHTML:'',textContent:'',disabled:false,hidden:false,open:false,value:'',classList:{add(){},remove(){},toggle(){}},querySelectorAll(){return []},querySelector(){return null},setAttribute(){},appendChild(){},addEventListener(type,fn){if(!listeners.has(type))listeners.set(type,[]);listeners.get(type).push(fn)},showModal(){this.open=true},close(){this.open=false;for(const fn of listeners.get('close')||[])fn()},focus(){},setSelectionRange(){}})}return nodes.get(id)}
 const context=vm.createContext({console,Map,Set,Date,Number,String,URL,AbortController,encodeURIComponent,authTransition:1,customMappingDomain:'__custom__',location:{host:'console.example.com'},query:'',filter:'',editing:null,state:{devices:[{id:'router-one',name:'主路由器',online:true,version:'0.1.3',connectedAt:new Date(Date.now()-65000).toISOString(),info:{}}],mappings:[{id:'map-one',deviceId:'router-one',host:'service.example.com',target:'http://192.168.1.1:80',note:'管理页',enabled:true,publicScheme:'https',publicPort:443}]},$:node,esc,uiIcon:()=>'',expandedFeatures:new Map(),renderWorkbench(){},render(){context.deviceCards()},deviceCards(){},mappingRow:m=>'<tr><td>'+esc(m.host)+'</td><td><button class="danger" data-action="deleteMapping">删除</button></td></tr>',pluginUpdateButton:()=>'',toggleFeature(){},action:async(a,id)=>{baseActions.push({a,id})},openEditor:(type,item)=>{opened.push({type,item});context.editing={type,item};node('#modal').showModal()},copyText:async()=>{},toast(){},refresh:async()=>{},api:async(path,method,body,options)=>{requests.push({path,method,body,options});return {latencyMs:27,checkedAt:new Date().toISOString()}},confirm:()=>confirm,setTimeout:fn=>{timers.set(++timer,fn);return timer},clearTimeout:id=>timers.delete(id),document:{activeElement:null,getElementById:node,createElement:()=>node('option')},FormData:class{constructor(){this.fields=new Map([['name','新名称'],['note','路由器备注'],['pinned','on']])}get(k){return this.fields.get(k)}has(k){return this.fields.has(k)}}});
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


test('egress distinguishes automatic preference from actual socket and last offline interface',()=>{
 const f=fixture(),d=f.context.state.devices[0];d.info={os:'linux',egressInterface:'pppoe-wan'};
 assert.equal(f.context.deviceEgressText(d),'自动选择 · 实际：pppoe-wan');
 d.online=false;assert.equal(f.context.deviceEgressText(d),'自动选择 · 上次实际：pppoe-wan');
 d.info.interface='eth1';assert.equal(f.context.deviceEgressText(d),'指定 eth1 · 上次实际：pppoe-wan');
 delete d.info.egressInterface;assert.equal(f.context.deviceEgressText(d),'指定 eth1 · 实际网卡未上报');
 delete d.info.interface;assert.equal(f.context.deviceEgressText(d),'自动选择 · 实际网卡未上报');
 d.info={};assert.equal(f.context.deviceEgressText(d),'尚未上报');
 d.info={os:'linux',egressInterface:'<img src=x onerror=alert(1)>'};
 assert.match(f.context.deviceConnectionPanel(d),/&lt;img/);assert.doesNotMatch(f.context.deviceConnectionPanel(d),/<img/);
});

function manyDevices(f){
 f.context.state.devices=Array.from({length:25},(_,i)=>({id:'device-'+i,name:'设备 '+String(i+1).padStart(2,'0'),online:i%2===0,info:{}}));
 f.context.state.mappings=[];
}
function historyEvents(count){return Array.from({length:count},(_,i)=>({state:'offline',at:new Date(Date.UTC(2026,9,21,0,0,count-i)).toISOString(),connectedAt:'2026-10-21T00:00:00Z',reason:'disconnected'}))}
test('page slots keep a constant width from the first page through the last',()=>{
 const f=fixture();
 for(const pages of [1,2,7,8,10,25,100])for(let page=0;page<pages;page++){
  const slots=Array.from(f.context.devicePageSlots(pages,page)),numbers=slots.filter(Number.isInteger);
  assert.equal(slots.length,Math.min(7,pages));assert.ok(numbers.includes(page));
  assert.equal(numbers[0],0);assert.equal(numbers.at(-1),pages-1);
  assert.deepEqual(numbers,[...new Set(numbers)].sort((a,b)=>a-b));
 }
 const html=f.context.devicePagerHTML('history','router-one',80,{page:0,size:10});
 assert.match(html,/10 条 \/ 页/);assert.match(html,/role="listbox"/);assert.match(html,/aria-haspopup="listbox"/);assert.doesNotMatch(html,/<select/);
 assert.equal((html.match(/class="device-page-number"/g)||[]).length,6);
});
test('device pagination slices after global sorting and searches across every page',()=>{
 const f=fixture();manyDevices(f);f.context.state.devices[24].pinned=true;f.context.deviceCards();
 let html=f.node('#deviceCards').innerHTML;
 assert.equal((html.match(/<article /g)||[]).length,10);assert.match(html,/共 25 台设备/);assert.match(html,/第 1 \/ 3 页/);
 assert.ok(html.indexOf('<strong>设备 25')<html.indexOf('<strong>设备 01'));
 f.context.changeDevicePage('devices','',1);html=f.node('#deviceCards').innerHTML;
 assert.match(html,/<strong>设备 10<\/strong>/);assert.doesNotMatch(html,/<strong>设备 25/);
 f.context.query='设备 23';f.context.deviceCards();html=f.node('#deviceCards').innerHTML;
 assert.match(html,/<strong>设备 23/);assert.match(html,/共 1 台设备/);assert.match(html,/第 1 \/ 1 页/);
 f.context.query='';f.context.filter='no';f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/共 12 台设备/);
 f.context.changeDevicePage('devices','',1);assert.match(f.node('#deviceCards').innerHTML,/<strong>设备 24/);
 f.context.filter='pinned';f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/<strong>设备 25/);
});
test('device page and size survive refresh, clamp deletion, and reset for another login',()=>{
 const f=fixture();manyDevices(f);f.context.deviceCards();f.context.changeDevicePage('devices','',0,5);f.context.changeDevicePage('devices','',4);f.context.renderWorkbench();f.context.deviceCards();
 assert.match(f.node('#deviceCards').innerHTML,/第 5 \/ 5 页/);
 f.context.state.devices.splice(20);f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/第 4 \/ 4 页/);
 f.context.changeDevicePage('devices','',0,999);assert.equal(f.eval('deviceListPaging.size'),5);
 f.context.authTransition++;f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/第 1 \/ 2 页/);assert.equal(f.eval('deviceListPaging.size'),10);
 f.context.query='missing';f.context.deviceCards();const html=f.node('#deviceCards').innerHTML;assert.match(html,/共 0 台设备/);assert.match(html,/暂无匹配设备/);assert.doesNotMatch(html,/当前 1–0/);
});
test('history pages are independent per device, bounded and retained during late refresh',async()=>{
 const f=fixture(),d=f.context.state.devices[0];f.context.state.devices.push({id:'other',name:'另一台',online:false});
 f.context.api=async()=>({events:historyEvents(25),limit:100});await f.context.loadDeviceHistory(d.id);await f.context.loadDeviceHistory('other');
 let html=f.context.deviceHistoryPanel(d);assert.match(html,/<td>25 秒<\/td>/);assert.doesNotMatch(html,/<td>15 秒<\/td>/);assert.match(html,/最多保留最近 100 条/);
 f.context.changeDevicePage('history',d.id,2);html=f.context.deviceHistoryPanel(d);assert.match(html,/<td>5 秒<\/td>/);assert.doesNotMatch(html,/<td>25 秒<\/td>/);assert.match(html,/第 3 \/ 3 页/);
 assert.match(f.context.deviceHistoryPanel({id:'other'}),/<td>25 秒<\/td>/);
 let resolve;f.context.api=()=>new Promise(r=>resolve=r);const pending=f.context.loadDeviceHistory(d.id);
 f.context.changeDevicePage('history',d.id,1);resolve({events:historyEvents(26),limit:100});await pending;
 html=f.context.deviceHistoryPanel(d);assert.match(html,/第 2 \/ 3 页/);assert.match(html,/<td>16 秒<\/td>/);
 f.context.api=async()=>({events:historyEvents(3),limit:100});await f.context.loadDeviceHistory(d.id);assert.match(f.context.deviceHistoryPanel(d),/第 1 \/ 1 页/);
 f.context.changeDevicePage('history','other',0,20);assert.equal(f.context.deviceHistoryPaging('other').size,20);assert.equal(f.context.deviceHistoryPaging(d.id).size,10);
 f.context.authTransition++;f.context.deviceCards();assert.equal(f.context.deviceHistoryPaging('other').size,10);assert.equal(f.timers.size,0);
});
test('expanded history on another device page is not fetched until that page is visible',async()=>{
 const f=fixture();manyDevices(f);f.eval("expandedFeatures.set('device-24','history')");f.context.deviceCards();assert.equal(f.requests.length,0);
 f.context.changeDevicePage('devices','',2);await new Promise(resolve=>setImmediate(resolve));
 assert.deepEqual(f.requests.map(r=>r.path),['devices/device-24/events']);assert.equal(f.timers.size,0);
});

test('a recent real disconnection shows recovery without claiming the device is online',()=>{
 const f=fixture(),d=f.context.state.devices[0];d.online=false;d.lastDisconnectedAt=new Date(Date.now()-10000).toISOString();f.context.deviceCards();
 assert.match(f.node('#deviceCards').innerHTML,/pill recovering/);assert.match(f.node('#deviceCards').innerHTML,/恢复中/);assert.equal(d.online,false);
 d.lastDisconnectedAt=new Date(Date.now()-31000).toISOString();f.context.deviceCards();assert.doesNotMatch(f.node('#deviceCards').innerHTML,/pill recovering/);
});

test('addition order survives device renaming, online changes, pinning and pagination',()=>{
 const f=fixture();manyDevices(f);
 // Names deliberately oppose addition order, so alphabetical sorting fails.
 f.context.state.devices.forEach((d,i)=>{d.name='路由器 '+String(25-i).padStart(2,'0')});
 f.context.deviceCards();let html=f.node('#deviceCards').innerHTML;
 assert.ok(html.indexOf('<strong>路由器 25')<html.indexOf('<strong>路由器 24'));
 assert.doesNotMatch(html,/<strong>路由器 01/);
 f.context.state.devices[0].name='AAA 更名';f.context.state.devices[1].online=false;
 f.context.deviceCards();html=f.node('#deviceCards').innerHTML;
 assert.ok(html.indexOf('<strong>AAA 更名')<html.indexOf('<strong>路由器 24'));
 f.context.changeDevicePage('devices','',1);html=f.node('#deviceCards').innerHTML;
 assert.match(html,/<strong>路由器 15/);assert.doesNotMatch(html,/<strong>AAA 更名/);
 f.context.state.devices[24].pinned=true;f.context.changeDevicePage('devices','',0);
 html=f.node('#deviceCards').innerHTML;
 assert.ok(html.indexOf('<strong>路由器 01')<html.indexOf('<strong>AAA 更名'));
 f.context.state.devices[24].pinned=false;f.context.deviceCards();html=f.node('#deviceCards').innerHTML;
 assert.match(html,/<strong>AAA 更名/);assert.doesNotMatch(html,/<strong>路由器 01/);
});

function connectionSnapshot(overrides={}){return {limits:{webSockets:8,httpReserve:8},connections:{capacity:128,effectiveWebSockets:8,effectiveHTTPReserve:8,activeHTTP:2,activeWebSockets:4,streaming:true},...overrides}}
async function connectionEditor(f,snapshot=connectionSnapshot()){
 f.context.api=async(path,method,body)=>{f.requests.push({path,method,body});return snapshot};
 await f.context.action('connectionLimits','router-one');await new Promise(resolve=>setImmediate(resolve));
}

test('connection settings distinguish target, active usage and client effective limits',async()=>{
 const f=fixture(),d=f.context.state.devices[0];d.name='<img src=x>';
 await connectionEditor(f,connectionSnapshot({limits:{webSockets:24,httpReserve:8},connections:{capacity:16,effectiveWebSockets:8,effectiveHTTPReserve:8,activeHTTP:2,activeWebSockets:5,streaming:true}}));
 assert.equal(f.requests[0].path,'devices/router-one/connection-limits');assert.equal(f.requests[0].method,'GET');
 assert.equal(f.node('#connectionWebSockets').value,'24');assert.equal(f.node('#connectionHTTPReserve').value,'8');assert.equal(f.node('#save').disabled,false);
 assert.match(f.node('#connectionUsage').innerHTML,/5 \/ 8/);assert.match(f.node('#connectionUsage').innerHTML,/16 条/);
 assert.match(f.node('#connectionLimitNotice').textContent,/超出部分需升级插件后生效/);assert.match(f.node('#connectionLimitTotal').textContent,/32 \/ 128/);
 assert.equal(f.node('#modalTitle').textContent,'连接设置 · <img src=x>');assert.doesNotMatch(f.node('#fields').innerHTML,/<img/);
 assert.match(f.node('#fields').innerHTML,/SSH 和文件管理使用单独额度/);assert.equal(f.timers.size,0);
});

test('connection presets remain drafts, refresh retains edits and save updates only the chosen device',async()=>{
 const f=fixture();f.context.state.devices.push({id:'other',name:'办公室',connectionLimits:{webSockets:8,httpReserve:8}});await connectionEditor(f);
 f.node('#connectionMultiPanel').onclick();assert.equal(f.node('#connectionWebSockets').value,'24');assert.equal(f.requests.length,1);
 await f.node('#connectionRefresh').onclick();assert.equal(f.node('#connectionWebSockets').value,'24');assert.equal(f.requests.length,2);
 f.node('#connectionDefaults').onclick();assert.equal(f.node('#connectionWebSockets').value,'8');assert.equal(f.requests.length,2);
 f.node('#connectionMultiPanel').onclick();let refreshes=0;f.context.refresh=async()=>refreshes++;
 f.context.api=async(path,method,body)=>{f.requests.push({path,method,body});return connectionSnapshot({limits:body})};
 await f.node('#editor').onsubmit({preventDefault(){}});
 assert.equal(f.requests.at(-1).method,'PUT');assert.deepEqual(JSON.parse(JSON.stringify(f.requests.at(-1).body)),{webSockets:24,httpReserve:8});
 assert.equal(f.context.state.devices[0].connectionLimits.webSockets,24);assert.equal(f.context.state.devices[1].connectionLimits.webSockets,8);
 assert.equal(f.node('#modal').open,false);assert.equal(f.node('#save').disabled,false);assert.equal(refreshes,1);assert.equal(f.timers.size,0);
 assert.ok(f.requests.every(r=>!r.path.endsWith('/reconnect')));
});

test('connection quota validation blocks invalid totals and numeric values before any save',async()=>{
 const f=fixture();await connectionEditor(f);
 for(const [web,http] of [['0','8'],['125','4'],['8','3'],['8','65'],['100','29'],['2.5','8'],['','8'],['8','NaN']]){
  f.node('#connectionWebSockets').value=web;f.node('#connectionHTTPReserve').value=http;await f.node('#editor').onsubmit({preventDefault(){}});
  assert.notEqual(f.node('#formError').textContent,'');assert.equal(f.requests.length,1);assert.equal(f.node('#modal').open,true);
 }
 f.node('#connectionWebSockets').value='124';f.node('#connectionHTTPReserve').value='4';f.context.api=async(path,method,body)=>{f.requests.push({path,method,body});return connectionSnapshot({limits:body})};
 await f.node('#editor').onsubmit({preventDefault(){}});assert.equal(f.requests.length,2);
});

test('offline device keeps configured targets without inventing live usage or forcing reconnection',async()=>{
 const f=fixture();f.context.state.devices[0].online=false;await connectionEditor(f,connectionSnapshot({limits:{webSockets:24,httpReserve:8},connections:{capacity:0,effectiveWebSockets:0,effectiveHTTPReserve:0,activeHTTP:0,activeWebSockets:0,streaming:false}}));
 assert.match(f.node('#connectionUsage').innerHTML,/设备离线/);assert.match(f.node('#connectionLimitNotice').textContent,/离线时也可以保存配置/);assert.equal(f.node('#connectionWebSockets').value,'24');
 const d=f.context.state.devices[0];d.online=true;d.connections={capacity:16,streaming:false};
 assert.match(f.context.deviceConnectionLimitsNotice(d,{webSockets:8,httpReserve:8}),/不支持网页流式连接/);
});

test('connection settings are absent and cannot be opened by ordinary users',async()=>{
 const f=fixture();f.context.state.account={role:'user'};f.context.deviceCards();assert.doesNotMatch(f.node('#deviceCards').innerHTML,/class="device-manage"|data-action="connectionLimits"/);
 await f.context.action('connectionLimits','router-one');f.context.openEditor('connectionLimits',f.context.state.devices[0]);assert.equal(f.requests.length,0);assert.equal(f.opened.length,0);
 f.context.state.account={role:'admin'};f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/data-action="connectionLimits"[^>]*>连接设置/);
 f.context.memberPortal=true;await f.context.action('connectionLimits','router-one');assert.equal(f.requests.length,0);
});

test('connection GET errors offer retry, presets cannot save before data arrives, cancellation rejects late reads',async()=>{
 const f=fixture();f.context.api=async()=>{throw Error('读取失败')};await f.context.action('connectionLimits','router-one');await new Promise(resolve=>setImmediate(resolve));
 assert.match(f.node('#formError').textContent,/读取失败/);assert.equal(f.node('#save').disabled,true);assert.equal(f.node('#connectionRefresh').disabled,false);
 let saves=0;f.context.api=async()=>{saves++;return connectionSnapshot({limits:{webSockets:12,httpReserve:8}})};
 await f.node('#editor').onsubmit({preventDefault(){}});assert.equal(saves,0);
 await f.node('#connectionRefresh').onclick();assert.equal(f.node('#connectionWebSockets').value,'12');assert.equal(f.node('#save').disabled,false);
 let resolve;f.context.api=()=>new Promise(r=>resolve=r);const pending=f.node('#connectionRefresh').onclick();f.node('#cancel').onclick();
 resolve(connectionSnapshot({limits:{webSockets:99,httpReserve:8}}));await pending;assert.equal(f.node('#connectionWebSockets').value,'12');assert.equal(f.node('#save').disabled,false);assert.equal(f.timers.size,0);
});

test('connection save prevents duplicate submissions and ignores responses after logout or device removal',async()=>{
 for(const ending of ['logout','delete']){
  const f=fixture();await connectionEditor(f);let resolve,calls=0;f.context.api=()=>{calls++;return new Promise(r=>resolve=r)};
  f.node('#connectionWebSockets').value='24';const pending=f.node('#editor').onsubmit({preventDefault(){}});await f.node('#editor').onsubmit({preventDefault(){}});assert.equal(calls,1);
  if(ending==='logout')f.context.authTransition++;else f.context.state.devices=[];
  resolve(connectionSnapshot({limits:{webSockets:24,httpReserve:8}}));await pending;
  if(ending==='logout')assert.equal(f.context.state.devices[0].connectionLimits.webSockets,8);else assert.equal(f.context.state.devices.length,0);
  assert.equal(f.timers.size,0);
 }
});

test('connection save errors retain the draft and timeout cleanup allows a safe retry',async()=>{
 const f=fixture();await connectionEditor(f);f.node('#connectionWebSockets').value='24';
 f.context.api=async()=>{throw Error('保存失败')};await f.node('#editor').onsubmit({preventDefault(){}});
 assert.equal(f.node('#modal').open,true);assert.equal(f.node('#connectionWebSockets').value,'24');assert.equal(f.context.state.devices[0].connectionLimits.webSockets,8);
 assert.match(f.node('#formError').textContent,/保存失败/);assert.equal(f.node('#save').disabled,false);assert.equal(f.timers.size,0);
 f.context.api=async(path,method,body,options)=>new Promise((resolve,reject)=>options.signal.addEventListener('abort',()=>{const error=Error('aborted');error.name='AbortError';reject(error)}));
 const pending=f.node('#editor').onsubmit({preventDefault(){}});assert.equal(f.timers.size,1);[...f.timers.values()][0]();await pending;
 assert.match(f.node('#formError').textContent,/刷新确认结果后重试/);assert.equal(f.node('#save').disabled,false);assert.equal(f.node('#connectionWebSockets').disabled,false);assert.equal(f.timers.size,0);
});

test('opening another editor prevents a late connection read from replacing its fields or save state',async()=>{
 const f=fixture();let resolve;f.context.api=()=>new Promise(r=>resolve=r);await f.context.action('connectionLimits','router-one');
 f.node('#cancel').onclick();f.context.openEditor('device',f.context.state.devices[0]);const fields=f.node('#fields').innerHTML;
 assert.equal(f.node('#save').disabled,false);resolve(connectionSnapshot({limits:{webSockets:24,httpReserve:8}}));await new Promise(r=>setImmediate(r));
 assert.equal(f.node('#fields').innerHTML,fields);assert.equal(f.node('#save').disabled,false);assert.equal(f.context.editing.type,'device');assert.equal(f.timers.size,0);
});

test('device management expansion survives refresh independently per device and resets for another login',()=>{
 const f=fixture();f.context.state.devices.push({id:'other',name:'办公室',online:false,info:{}});
 const first={dataset:{deviceManage:'router-one'},open:false,isConnected:true},second={dataset:{deviceManage:'other'},open:false,isConnected:true};let menus=[first,second];
 f.node('#deviceCards').querySelectorAll=selector=>selector==='[data-device-manage]'?menus:[];f.context.deviceCards();
 first.open=true;first.ontoggle();f.context.deviceCards();let html=f.node('#deviceCards').innerHTML;
 assert.match(html,/data-device-manage="router-one" open/);assert.doesNotMatch(html,/data-device-manage="other" open/);
 // Capture the native state even if the scheduled toggle event has not run.
 second.open=true;f.context.renderWorkbench();f.context.deviceCards();html=f.node('#deviceCards').innerHTML;assert.match(html,/data-device-manage="other" open/);
 first.open=false;first.ontoggle();f.context.deviceCards();assert.doesNotMatch(f.node('#deviceCards').innerHTML,/data-device-manage="router-one" open/);
 first.open=true;first.ontoggle();menus=[];f.context.deviceCards();assert.match(f.node('#deviceCards').innerHTML,/data-device-manage="router-one" open/);
 const staleToggle=first.ontoggle;f.context.authTransition++;menus=[first,second];f.context.deviceCards();staleToggle();
 html=f.node('#deviceCards').innerHTML;assert.doesNotMatch(html,/data-device-manage="(?:router-one|other)" open/);assert.equal(f.eval('deviceManageOpen.size'),0);
 f.eval("deviceManageOpen.add('other')");f.context.state.devices.splice(1);menus=[];f.context.deviceCards();assert.equal(f.eval("deviceManageOpen.has('other')"),false);
});

test('refresh restores a focused management action without moving focus across logins',()=>{
 const f=fixture();let focused=0;
 const action={dataset:{action:'connectionLimits'},disabled:false,focus(options){focused++;assert.equal(options.preventScroll,true)}};
 const menu={dataset:{deviceManage:'router-one'},open:true,isConnected:true,querySelectorAll:()=>[action],querySelector:()=>({focus(){focused++}})};
 f.node('#deviceCards').querySelectorAll=selector=>selector==='[data-device-manage]'?[menu]:[];
 f.context.document.activeElement={dataset:{action:'connectionLimits'},closest:selector=>selector==='[data-device-manage]'?menu:null};
 f.context.deviceCards();assert.equal(focused,1);assert.match(f.node('#deviceCards').innerHTML,/data-device-manage="router-one" open/);
 f.context.authTransition++;f.context.deviceCards();assert.equal(focused,1);
});
