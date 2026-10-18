const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
 const nodes=new Map(),requests=[];
 const node=id=>{if(!nodes.has(id))nodes.set(id,{innerHTML:'',textContent:'',value:'',hidden:false,disabled:false,open:false,checked:false,indeterminate:false,querySelectorAll:()=>[],classList:{add(){},remove(){}},addEventListener(){},close(){this.open=false},showModal(){this.open=true}});return nodes.get(id)};
 const esc=x=>String(x??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 const context=vm.createContext({console,$:node,esc,uiIcon:()=>'',authTransition:1,page:'packages',state:{devices:[],mappings:[],package:{manifest:{packageVersion:'0.1.10-3'},available:true}},document:{querySelector:()=>null,activeElement:null,createTextNode:x=>x},api:async(path,method,body)=>{requests.push({path,method,body});return path==='state'?{devices:[],mappings:[]}:{profiles:[],history:[],policies:{},downloads:[],catalog:[]}},refresh:async()=>{},toast(){},copyText:async()=>{},openEditor(){},openPluginUpdate(){},closePluginUpdate(){},guide:null,installPoll:null,editing:null,clearInterval(){},clearTimeout(){},setTimeout:()=>1,confirm:()=>false});
 vm.runInContext(fs.readFileSync('cmd/server/web/package-center.js','utf8'),context);
 return {context,node,requests,eval:s=>vm.runInContext(s,context)};
}
test('an online device cannot confirm installation before the target version is verified',async()=>{
 const f=fixture();
 f.context.action=async()=>{};
 vm.runInContext(fs.readFileSync('cmd/server/web/install-guide.js','utf8'),f.context);
 f.eval("guide={r:{device:{id:'router'}},step:2,operationId:'install',checking:false};renderGuide=()=>{};render=()=>{};");
 f.node('#modal').open=true;
 let phase='waiting';
 f.context.api=async path=>path==='state'?{devices:[{id:'router',online:true}]}:{history:[{id:'install',state:{phase}}]};
 await f.context.checkGuide();assert.equal(f.eval('guide.step'),2);
 phase='succeeded';await f.context.checkGuide();assert.equal(f.eval('guide.step'),3,f.node('#detectMessage').textContent);
});

test('manual enrollment after a failed SSH install does not wait for the failed operation',async()=>{
 const f=fixture(),manual={dataset:{method:'manual'}};
 f.context.document.querySelectorAll=()=>[manual];
 f.context.render=()=>{};
 f.context.action=async()=>{};
 vm.runInContext(fs.readFileSync('cmd/server/web/install-guide.js','utf8'),f.context);
 f.eval("guide={r:{device:{id:'router',name:'Fixture'},token:'fixture-only'},step:1,mode:'ssh',server:'https://example.test',profiles:[{id:'standard',name:'Standard'}],profileId:'standard',command:'consumed command',expires:'expired',operationId:'failed-install',operation:{state:{phase:'failed'}},checking:false}");
 f.context.renderGuide();manual.onclick();
 assert.equal(f.eval('guide.operationId'),'');assert.equal(f.eval('guide.command'),'');assert.equal(f.eval('guide.expires'),'');
 assert.equal(f.eval('guide.r.device.id'),'router');assert.equal(f.eval('guide.r.token'),'fixture-only');
 f.eval('guide.step=2');f.node('#modal').open=true;
 f.context.api=async path=>{assert.equal(path,'state','manual detection queried an obsolete SSH operation');return {devices:[{id:'router',online:true}]}};
 await f.context.checkGuide();assert.equal(f.eval('guide.step'),3);
});

test('selecting the current install method preserves its active operation',()=>{
 const f=fixture(),ssh={dataset:{method:'ssh'}};
 f.context.document.querySelectorAll=()=>[ssh];
 f.context.action=async()=>{};
 vm.runInContext(fs.readFileSync('cmd/server/web/install-guide.js','utf8'),f.context);
 f.eval("guide={r:{device:{id:'router',name:'Fixture'}},step:1,mode:'ssh',server:'https://example.test',profiles:[{id:'standard',name:'Standard'}],profileId:'standard',command:'active command',operationId:'active-install'}");
 f.context.renderGuide();ssh.onclick();
 assert.equal(f.eval('guide.operationId'),'active-install');assert.equal(f.eval('guide.command'),'active command');
});

test('manual credentials copy individually and together using the current server address',async()=>{
 const f=fixture(),copies=[];
 f.context.document.querySelectorAll=()=>[];
 f.context.action=async()=>{};
 f.context.copyText=async(text)=>copies.push(text);
 vm.runInContext(fs.readFileSync('cmd/server/web/install-guide.js','utf8'),f.context);
 f.eval("guide={r:{device:{id:'router-id',name:'Fixture'},token:'fixture-token<&>'},step:1,mode:'manual',server:'https://example.test',profiles:[{id:'standard',name:'Standard'}],profileId:'standard'}");
 f.context.renderGuide();
 assert.equal(f.node('#manualToken').textContent,'fixture-token<&>');
 assert.doesNotMatch(f.node('#methodBody').innerHTML,/fixture-token/,'credential was interpolated into HTML');
 f.node('#installServer').oninput({target:{value:' https://updated.example.test '}});
 assert.equal(f.node('#manualServer').textContent,'https://updated.example.test');
 for(const id of ['#copyManualServer','#copyManualDevice','#copyManualToken','#copyManualAll'])await f.node(id).onclick({currentTarget:f.node(id)});
 assert.deepEqual(copies,['https://updated.example.test','router-id','fixture-token<&>','服务器：https://updated.example.test\n设备 ID：router-id\n设备 Token：fixture-token<&>']);
});
test('package comparison handles revisions, unknown versions and large version numbers',()=>{
 const f=fixture();assert.equal(f.context.packageCompare('0.1.10-3','0.1.10-2'),1);assert.equal(f.context.packageCompare('0.1.9-9','0.1.10-1'),-1);assert.equal(f.context.packageCompare('unknown','0.1.10-1'),null);
 assert.equal(f.context.packageCompare('9007199254740993.0.0-1','9007199254740992.0.0-1'),1);
});
test('locked and offline devices are excluded from bulk updates',()=>{
 const f=fixture();f.eval("packageCenterData={policies:{locked:{locked:true}}}");
 for(const d of [{id:'locked',online:true,canUpdate:true,packageVersion:'0.1.10-1'},{id:'offline',online:false,canUpdate:true,packageVersion:'0.1.10-1'},{id:'current',online:true,canUpdate:true,packageVersion:'0.1.10-3'}])assert.equal(f.context.packageEligible(d),false);
 assert.equal(f.context.packageEligible({id:'ready',online:true,canUpdate:true,packageVersion:'0.1.10-1'}),true);
});
test('profile and history labels are escaped and download links stay on the server',()=>{
 const f=fixture(),html=f.context.packageProfilesHTML({profiles:[{id:'safe',name:'<script>alert(1)</script>',server:'"><img src=x>',remoteAccess:true,autoStart:true,interfaceMode:'auto'}]});
 assert.doesNotMatch(html,/<script>|<img/);assert.match(html,/&lt;script&gt;/);
 assert.equal(f.context.packageSafeDownload('javascript:alert(1)'),'/downloads/remotegate.run');assert.equal(f.context.packageSafeDownload('//other.example/download'),'/downloads/remotegate.run');
 assert.equal(f.context.packageSafeDownload('/downloads/remotegate.run?arch=amd64'),'/downloads/remotegate.run?arch=amd64');
});
test('rollback choices include only earlier compatible packages',()=>{
 const f=fixture();f.eval("packageCenterData={catalog:[{version:'0.1.10-3',compatible:true},{version:'0.1.10-2',compatible:true},{version:'0.1.9-1',compatible:false}]}");
 const versions=f.context.packageRollbackVersions({packageVersion:'0.1.10-3'});assert.deepEqual(Array.from(versions,x=>x.version),['0.1.10-2']);
});
test('install progress marks completion only from a verified succeeded record',()=>{
 const f=fixture();const waiting=f.context.installProgressHTML({state:{phase:'waiting',message:'等待连接'}}),done=f.context.installProgressHTML({state:{phase:'succeeded',message:'设备已连接'}});
 assert.equal((waiting.match(/class="done"/g)||[]).length,6);assert.match(waiting,/class="current"/);assert.equal((done.match(/class="done"/g)||[]).length,7);
});
