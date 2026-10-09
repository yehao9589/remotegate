const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const context=vm.createContext({Number,Date,Map,URL,location:{port:''},openEditor:()=>{},$:()=>({onsubmit:()=>{}})});
for(const name of ['mapping-editor.js','domain-center.js','https-deploy.js']) vm.runInContext(fs.readFileSync('cmd/server/web/'+name,'utf8'),context);
const certificate={installed:true,certificate:{valid:true,names:['*.gate.example.com']}};
const settings={running:true,address:':8443'};
const mapping={host:'router.gate.example.com',publicScheme:'https',publicPort:443};

test('native mapping uses the running port and actual certificate coverage',()=>{
 const plan=context.nativeMappingPlan(mapping,certificate,settings);
 assert.equal(plan.available,true);assert.equal(plan.applied,false);
 assert.equal(plan.url,'https://router.gate.example.com:8443/');
 assert.equal(context.nativeMappingPlan({...mapping,publicPort:8443},certificate,settings).applied,true);
 assert.equal(context.nativeMappingPlan({...mapping,host:'gate.example.com'},certificate,settings).available,false);
 assert.equal(context.nativeMappingPlan({...mapping,host:'deep.router.gate.example.com'},certificate,settings).available,false);
 assert.equal(context.nativeMappingPlan(mapping,{...certificate,certificate:{...certificate.certificate,valid:false}},settings).available,false);
});

test('disabled, failed and local listeners cannot be offered as direct public access',()=>{
 for(const value of [{running:false,address:''},{running:false,address:':8443'},{running:true,address:'127.0.0.1:8443'},{running:true,address:'[::1]:8443'},{running:true,address:':65536'}]){
  assert.equal(context.nativeMappingPlan(mapping,certificate,value).available,false);
 }
 assert.equal(context.nativeMappingPlan(mapping,certificate,{running:true,address:'[::]:443'}).available,true);
 assert.equal(context.nativeMappingPlan(mapping,certificate,{running:false,listenAddress:':8443',error:'in use'}).url,'');
});

test('deployment instructions preserve the separately managed root certificate',()=>{
 const d={baseDomain:'gate.example.com'};
 assert.match(context.deploymentRootNote(d,certificate),/不覆盖 gate.example.com 本身/);
 assert.match(context.deploymentRootNote(d,certificate),/保留现有主域名站点证书/);
 assert.match(context.deploymentRootNote(d,{certificate:{names:[d.baseDomain,'*.gate.example.com']}}),/同时覆盖主域名/);
});

test('mapping advice offers the native port without silently replacing entered settings',async()=>{
 const editor={},modal={open:true},node={hidden:true,innerHTML:'',querySelector(){return this.innerHTML.includes('data-use-native')?{addEventListener:(_type,callback)=>{this.apply=callback}}:null}};
 context.editing=editor;context.state={domains:[{id:'domain',baseDomain:'gate.example.com'}]};
 context.document={getElementById:()=>node};context.$=()=>modal;context.esc=String;context.certificatePath=()=>'';
 context.api=async()=>({...certificate,httpsSettings:settings});
 let current={...mapping},changes=0;
 const advice=context.createMappingHTTPSAdvice(editor,()=>current,plan=>{changes++;current={...current,publicScheme:'https',publicPort:plan.port};advice.refresh()});
 advice.refresh();await Promise.resolve();
 assert.equal(current.publicPort,443);assert.equal(changes,0);
 assert.match(node.innerHTML,/router.gate.example.com:8443/);assert.match(node.innerHTML,/使用系统 HTTPS 入口/);
 node.apply();assert.equal(current.publicPort,8443);assert.equal(changes,1);
 assert.match(node.innerHTML,/由系统 HTTPS 入口提供/);
 current={...current,host:'gate.example.com'};advice.refresh();
 assert.match(node.innerHTML,/证书未覆盖/);assert.doesNotMatch(node.innerHTML,/data-use-native/);
});

test('standard HTTPS and HTTP URLs omit ports while custom ports remain explicit',()=>{
 assert.equal(context.publicMappingURL(mapping),'https://router.gate.example.com/');
 assert.equal(context.publicMappingURL({...mapping,publicScheme:'http',publicPort:80}),'http://router.gate.example.com/');
 assert.equal(context.publicMappingURL({...mapping,publicPort:8443}),'https://router.gate.example.com:8443/');
 assert.equal(context.nativeMappingPlan(mapping,certificate,{mode:'shared',running:true,address:':443'}).url,'https://router.gate.example.com/');
});

test('shared ingress migration preserves Baota TLS and pairs real-IP support',()=>{
 const snippet=context.sharedBaotaConfig('127.0.0.1:9443',true);
 assert.match(snippet,/listen 127\.0\.0\.1:9443 ssl proxy_protocol;/);
 assert.match(snippet,/real_ip_header proxy_protocol;/);
 assert.match(snippet,/set_real_ip_from 127\.0\.0\.1;/);
 assert.doesNotMatch(context.sharedBaotaConfig('[::1]:9443',false),/proxy_protocol/);
 assert.equal(context.sharedBaotaConfig('192.0.2.1:9443',true),'');
 assert.equal(context.sharedBaotaConfig('127.0.0.1:9443; injection',true),'');
 context.esc=String;
 const panel=context.deploymentListenerPanel({enabled:false,running:false,listenAddress:':8443'},true);
 assert.match(panel,/name="listenAddress" value=":443"/);
 assert.match(panel,/name="migrationReady" type="checkbox" required/);
 assert.match(panel,/保留 SSL 证书、反代和网站配置/);
 assert.match(panel,/不会自动改动宝塔/);
});

test('independent listener requires no external proxy fields and keeps address changes advanced',()=>{
 const panel=context.deploymentListenerPanel({enabled:false,running:false,listenAddress:':443'},false);
 assert.match(panel,/无需安装 Nginx 或宝塔/);
 assert.match(panel,/高级：自定义监听地址/);
 assert.doesNotMatch(panel,/name="fallbackAddress"|name="migrationReady"/);
 assert.doesNotMatch(panel,/已有宝塔网站请选择|建议使用 :8443/);
});
