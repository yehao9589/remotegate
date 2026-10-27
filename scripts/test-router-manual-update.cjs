const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
function fixture(){
 const nodes=new Map(),listeners=new Map(),requests=[];
 function node(id){if(!nodes.has(id))nodes.set(id,{id,textContent:'',innerHTML:'',value:'',files:[],hidden:id==='rg-local-preview',disabled:false,className:'',classList:{add(){},toggle(){}},addEventListener(type,fn){listeners.set(id+':'+type,fn)},appendChild(){},replaceChildren(){},querySelectorAll(){return []}});return nodes.get(id)}
 const metadata={available:true,installedVersion:'0.1.18-2',fileSHA256:'a'.repeat(64),manifest:{packageVersion:'0.1.18-4',architectures:['ARM64']}};
 let upload=async()=>metadata,start=async()=>({installedVersion:'0.1.18-2',state:{phase:'checking',autoRollback:true}});
 const attributes={'data-status-url':'/connection','data-update-status':'/status','data-update-check':'/check','data-update-start':'/online-start','data-update-upload':'/upload','data-update-local-start':'/local-start','data-token':'csrf'};
 class Data {constructor(){this.fields=new Map()}append(key,value){this.fields.set(key,value)}get(key){return this.fields.get(key)}}
 const context=vm.createContext({document:{currentScript:{getAttribute:key=>attributes[key]},readyState:'complete',hidden:true,body:{classList:{add(){}}},getElementById:node,addEventListener(){},createElement:()=>({}),createTextNode:text=>({text})},location:{pathname:'/cgi-bin/luci/admin/services/remotegate'},window:{setInterval(){},addEventListener(){}},setTimeout:()=>1,clearTimeout(){},AbortController,URL,URLSearchParams,FormData:Data,Promise,fetch:async(url,options={})=>{
  requests.push({url,options});const value=url==='/upload'?await upload():url==='/local-start'?await start():{installedVersion:'0.1.18-2',state:{phase:'idle'}};return {ok:true,json:async()=>value};
 }});
 vm.runInContext(fs.readFileSync('deploy/openwrt/luci/page.js','utf8'),context);
 return {node,requests,metadata,setUpload(fn){upload=fn},setStart(fn){start=fn},async ready(){await new Promise(setImmediate)},async select(name='update.ipk',size=1024){node('rg-local-file').files=[{name,size}];await listeners.get('rg-local-file:change')()},check:()=>listeners.get('rg-local-check:click')(),start:()=>listeners.get('rg-local-start:click')()};
}

test('manual upload checks a selected file before sending an exact reviewed version and hash',async()=>{
 const f=fixture();await f.ready();assert.equal(f.node('rg-local-start').disabled,true);await f.select();await f.check();
 const upload=f.requests.find(r=>r.url==='/upload');assert.equal(upload.options.method,'POST');assert.equal(upload.options.credentials,'same-origin');assert.equal(upload.options.headers['X-RemoteGate-Token'],'csrf');assert.equal(upload.options.body.get('package').name,'update.ipk');assert.equal(f.node('rg-local-preview').hidden,false);assert.equal(f.node('rg-local-target').textContent,'0.1.18-4');
 await f.start();const request=f.requests.find(r=>r.url==='/local-start');assert.equal(request.options.headers['X-RemoteGate-Token'],'csrf');const values=new URLSearchParams(request.options.body);assert.equal(values.get('version'),'0.1.18-4');assert.equal(values.get('sha256'),'a'.repeat(64));assert.equal(f.node('rg-local-preview').hidden,true);assert.equal(f.node('rg-local-start').disabled,true);
 assert.equal(f.requests.filter(r=>r.url==='/local-start').length,1);assert.equal(f.requests.filter(r=>r.url==='/online-start').length,0);
});
test('invalid and oversized files never leave the browser and changed selection invalidates review',async()=>{
 const f=fixture();await f.ready();for(const [name,size] of [['script.sh',5],['empty.ipk',0],['large.run',16*1024*1024]]){await f.select(name,size);await f.check()};assert.equal(f.requests.filter(r=>r.url==='/upload').length,0);
 await f.select('update.run');await f.check();assert.equal(f.node('rg-local-preview').hidden,false);await f.select('other.ipk');await f.start();assert.equal(f.node('rg-local-preview').hidden,true);assert.equal(f.requests.filter(r=>r.url==='/local-start').length,0);
});
test('an upload blocks duplicate requests and errors restore controls without replaying writes',async()=>{
 const f=fixture();await f.ready();let finish;f.setUpload(()=>new Promise(resolve=>{finish=resolve}));await f.select();const pending=f.check();assert.equal(f.node('rg-local-file').disabled,true);assert.equal(f.node('rg-update-check').disabled,true);await f.check();assert.equal(f.requests.filter(r=>r.url==='/upload').length,1);finish({error:'wrong architecture'});await pending;assert.equal(f.node('rg-local-file').disabled,false);assert.equal(f.node('rg-local-preview').hidden,true);assert.match(f.node('rg-local-message').textContent,/wrong architecture/);
 f.setUpload(async()=>f.metadata);await f.check();f.setStart(async()=>{throw new TypeError('network lost')});await f.start();await f.ready();assert.equal(f.requests.filter(r=>r.url==='/local-start').length,1);assert.equal(f.node('rg-local-file').disabled,false);
});
test('package metadata and error messages stay text, and incomplete verification cannot enable install',async()=>{
 const f=fixture();await f.ready();await f.select();f.setUpload(async()=>({...f.metadata,manifest:{...f.metadata.manifest,architectures:['<img src=x onerror=alert(1)>']}}));await f.check();assert.equal(f.node('rg-local-arch').innerHTML,'');assert.match(f.node('rg-local-arch').textContent,/<img/);
 f.setUpload(async()=>({error:'<script>bad</script>'}));await f.check();assert.equal(f.node('rg-local-message').innerHTML,'');assert.equal(f.node('rg-local-start').disabled,true);
 f.setUpload(async()=>({...f.metadata,fileSHA256:'bad'}));await f.check();await f.start();assert.equal(f.requests.filter(r=>r.url==='/local-start').length,0);
});
