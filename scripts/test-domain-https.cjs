const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const context=vm.createContext({Date,Map,Number,URL,state:{devices:[],mappings:[]},openEditor:()=>{},esc:s=>String(s??'').replaceAll('<','&lt;'),uiIcon:()=>'',clearTimeout,$:()=>({})});
for(const file of ['mapping-editor.js','certificates.js','domain-center.js','https-deploy.js'])vm.runInContext(fs.readFileSync('cmd/server/web/'+file,'utf8'),context);
const now=Date.now();
function domain(){return {id:'test',baseDomain:'gate.example.com',rootHTTPSProvider:'external',publicHTTPS:{host:'gate.example.com',port:443,valid:true,issuer:'Test CA',names:['gate.example.com','*.gate.example.com'],checkedAt:new Date(now).toISOString(),expiresAt:new Date(now+89*86400000).toISOString()}}}

test('public HTTPS panel only checks the external certificate and has no issuance controls',()=>{
 const panel=context.domainCertificatePanel(domain(),{installed:true,certificate:{valid:true}});
 assert.match(panel,/公网 HTTPS 已验证/);
 assert.match(panel,/宝塔 \/ 反向代理管理证书与续期/);
 assert.match(panel,/Test CA/);assert.match(panel,/证书覆盖域名/);
 assert.doesNotMatch(panel,/RemoteGate 内置证书|data-dc-source|data-dc-issue|data-dc-renew|data-dc-config|主机助手/);
});
test('root and wildcard SAN success does not claim a child or another port is verified',()=>{
 const d=domain();
 assert.equal(context.publicEndpointHealth(d,'router.gate.example.com',443,now).text,'待检测');
 assert.equal(context.publicEndpointHealth(d,d.baseDomain,8443,now).text,'待检测');
 const old=JSON.stringify(d.publicHTTPS);
 context.applyPublicHTTPSCheck(d,{checkedAt:new Date(now).toISOString(),dns:[{host:'router.gate.example.com',port:443,certificate:{valid:false,error:'wrong certificate'}}]});
 assert.equal(JSON.stringify(d.publicHTTPS),old);
 assert.equal(context.publicEndpointHealth(d,'router.gate.example.com',443,now).text,'HTTPS 异常');
 assert.equal(context.rootPublicHealth(d,now).key,'valid');
});
test('stale, expired, failed and soon-expiring observations need attention',()=>{
 let d=domain();d.publicHTTPS.checkedAt=new Date(now-25*3600000).toISOString();
 assert.equal(context.rootPublicHealth(d,now).text,'公网待复检');
 d=domain();d.publicHTTPS.expiresAt=new Date(now-1000).toISOString();
 assert.equal(context.rootPublicHealth(d,now).text,'公网证书已过期');
 d=domain();d.publicHTTPS.expiresAt=new Date(now+10*86400000).toISOString();
 assert.equal(context.rootPublicHealth(d,now).text,'证书即将到期');
 context.applyPublicHTTPSCheck(d,{checkedAt:new Date(now).toISOString(),dns:[{host:d.baseDomain,port:443,certificate:{valid:false,error:'hostname mismatch'}}]});
 assert.equal(context.rootPublicHealth(d,now).text,'公网 HTTPS 异常');
});
test('mapping checks survive a state reload and remain separate by port',()=>{
 const d=domain(),host='router.gate.example.com';
 context.applyPublicHTTPSCheck(d,{checkedAt:new Date(now).toISOString(),dns:[{host,port:443,certificate:{valid:true,issuer:'Mapping CA',expiresAt:new Date(now+80*86400000).toISOString()}}]});
 context.applyPublicHTTPSCheck(d,{checkedAt:new Date(now).toISOString(),dns:[{host,port:8443,certificate:{valid:false,error:'untrusted'}}]});
 const saved=JSON.parse(JSON.stringify(d));
 assert.equal(context.publicEndpointHealth(saved,host,443,now).key,'valid');
 assert.equal(context.publicEndpointHealth(saved,host,8443,now).text,'HTTPS 异常');
 context.state={devices:[{id:'device',online:true}],mappings:[{id:'mapping',deviceId:'device',host,publicScheme:'https',publicPort:443,enabled:true}]};
 assert.match(context.domainCertificatePanel(saved),/Mapping CA/);
 assert.match(context.domainCertificatePanel(saved),/data-dc-mapping-check="mapping"/);
});
test('HTTP mappings offer no certificate check and unknown certificates have no fake dates',()=>{
 const d=domain();context.state={devices:[],mappings:[{id:'plain',host:'plain.gate.example.com',publicScheme:'http',publicPort:80}]};
 assert.match(context.domainCertificatePanel(d),/data-dc-mapping-check="plain" disabled/);
 assert.doesNotMatch(context.publicCertificateDetails({checkedAt:new Date(now).toISOString(),expiresAt:'0001-01-01T00:00:00Z',error:'connection refused'}),/到期时间/);
});
test('failed check restores its button and leaves the last saved result intact',async()=>{
 const d=domain(),old=JSON.stringify(d),button={disabled:false,textContent:'检测 HTTPS'};
 context.state={domains:[d],devices:[],mappings:[]};context.drawDomainList=()=>{};context.toast=()=>{};
 context.api=async()=>{throw Error('Failed to fetch')};
 await context.runDomainCheck(d,d.baseDomain,443,button);
 assert.equal(button.disabled,false);assert.equal(button.textContent,'检测 HTTPS');assert.equal(JSON.stringify(d),old);
 assert.match(context.domainCertificatePanel(d),/Failed to fetch/);
});
