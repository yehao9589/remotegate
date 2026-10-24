const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
 const nodes=new Map(),requests=[];
 function node(key){if(!nodes.has(key))nodes.set(key,{innerHTML:'',textContent:'',disabled:false,querySelector:()=>null,insertAdjacentHTML(position,html){this.innerHTML+=html},classList:{add(){}},setAttribute(){}});return nodes.get(key)}
 const context=vm.createContext({Date,Number,Math,URLSearchParams,AbortController,authTransition:1,page:'operations',state:{devices:[{id:'r',name:'家里 <img src=x>'}],mappings:[{id:'m',host:'router.example.com'}]},$:node,document:{querySelector:s=>s.startsWith('nav ')?node('nav'):null,querySelectorAll:()=>[]},esc:s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),uiIcon:()=>'',trafficBytes:n=>(n||0)+' B',deviceTime:x=>x||'—',api:async(...args)=>{requests.push(args);return {items:[]}},action:async()=>{},refresh:()=>Promise.resolve(),setInterval(){},setTimeout:()=>1,clearTimeout(){},toast(){}});
 vm.runInContext(fs.readFileSync('cmd/server/web/operations.js','utf8'),context);
 return {context,node,requests,eval:code=>vm.runInContext(code,context)};
}
test('traffic chart, accessible totals, history gaps and ranking use actual daily values',()=>{
 const f=fixture(),html=f.context.operationTrendHTML({since:'2026-10-22T00:00:00Z',period:'day',points:[{date:'2026-10-21',observed:false,uploadBytes:0,downloadBytes:0},{date:'2026-10-22',observed:true,uploadBytes:10,downloadBytes:20}],ranking:[{id:'r',uploadBytes:10,downloadBytes:20}]});
 assert.match(html,/总计 30 B/);assert.match(html,/10 B/);assert.match(html,/20 B/);assert.match(html,/尚未开始统计/);assert.match(html,/未统计/);assert.match(html,/UTC/);assert.match(html,/366 天/);assert.match(html,/op-upload/);assert.match(html,/op-download/);assert.match(html,/&lt;img/);assert.doesNotMatch(html,/<img/);
 const monthly=f.context.operationTrendHTML({period:'month',points:[{date:'2026-10',observed:true,uploadBytes:0,downloadBytes:0}]});assert.match(monthly,/每月上传与下载/);assert.match(monthly,/暂无已记录流量/);
});
test('traffic filter requests are constrained and an older response cannot overwrite a newer selection',async()=>{
 const f=fixture(),pending=[];f.context.api=path=>new Promise(resolve=>pending.push({path,resolve}));
 const first=f.context.loadOperationTrend();f.eval("operationsScope='mappings';operationsID='m';operationsPeriod='month'");const second=f.context.loadOperationTrend();
 assert.match(pending[1].path,/scope=mappings/);assert.match(pending[1].path,/id=m/);assert.match(pending[1].path,/period=month/);
 pending[1].resolve({period:'month',points:[{date:'2026-10',observed:true,uploadBytes:88}]});await second;
 pending[0].resolve({period:'day',points:[{date:'2026-10-22',observed:true,uploadBytes:999}]});await first;
 assert.match(f.node('#opTrend').innerHTML,/88 B/);assert.doesNotMatch(f.node('#opTrend').innerHTML,/999 B/);
});
test('logout discards in-flight trend responses and does not restore the previous account data',async()=>{
 const f=fixture();let resolve;f.context.api=()=>new Promise(r=>resolve=r);const pending=f.context.loadOperationTrend();f.context.authTransition=2;f.node('#opTrend').innerHTML='new account';resolve({points:[{uploadBytes:123}]});await pending;assert.equal(f.node('#opTrend').innerHTML,'new account');
});
test('alerts distinguish active, unread and recovered entries and paginate long history',()=>{
 const f=fixture();f.eval(`operationAlerts=Array.from({length:25},(_,i)=>({id:String(i),title:'<script>'+i,detail:'Detail',kind:'offline',read:i>0,createdAt:'2026-10-22',resolvedAt:i>=20?'2026-10-23':null}));operationsAlertFilter='all'`);f.context.drawOperationAlerts();
 let html=f.node('#opAlertList').innerHTML;assert.match(html,/共 25 条 · 1 \/ 3 页/);assert.match(html,/&lt;script&gt;/);assert.doesNotMatch(html,/<script>/);assert.equal((html.match(/class="op-alert /g)||[]).length,10);
 f.eval('operationsAlertPage=2');f.context.drawOperationAlerts();html=f.node('#opAlertList').innerHTML;assert.match(html,/已恢复 \/ 条件已解除/);assert.equal((html.match(/class="op-alert /g)||[]).length,5);
 f.eval("operationsAlertFilter='unread';operationsAlertPage=0");f.context.drawOperationAlerts();assert.equal((f.node('#opAlertList').innerHTML.match(/class="op-alert /g)||[]).length,1);
});
test('late notification responses cannot reverse a read update or leak across login sessions',async()=>{
 const f=fixture(),pending=[];f.context.page='overview';f.context.api=()=>new Promise(resolve=>pending.push(resolve));
 const first=f.context.loadOperationAlerts(),second=f.context.loadOperationAlerts();pending[1]({items:[{id:'new',read:true}]});await second;pending[0]({items:[{id:'old',read:false}]});await first;assert.equal(f.eval('operationAlerts[0].id'),'new');
 const third=f.context.loadOperationAlerts();f.context.authTransition=2;pending[2]({items:[{id:'secret'}]});await third;f.eval('resetOperationsOwner()');assert.equal(f.eval('operationAlerts.length'),0);
});
test('network failures show a recoverable error instead of inventing zero traffic',async()=>{
 const f=fixture();f.context.api=async()=>{throw Error('网络暂时不可用')};await f.context.loadOperationTrend();assert.equal(f.node('#opTrend').textContent,'网络暂时不可用');
});
