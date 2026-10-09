const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const context=vm.createContext({Date,Map,Number,URL,state:{mappings:[]},esc:s=>String(s??'').replaceAll('<','&lt;'),uiIcon:()=>'',clearTimeout});
vm.runInContext(fs.readFileSync('cmd/server/web/domain-center.js','utf8'),context);
vm.runInContext(fs.readFileSync('cmd/server/web/certificates.js','utf8'),context);
const now=Date.now();
function domain(){return {id:'test',baseDomain:'gate.example.com',rootHTTPSProvider:'external',publicHTTPS:{host:'gate.example.com',port:443,valid:true,issuer:'Test CA',names:['gate.example.com'],checkedAt:new Date(now).toISOString(),expiresAt:new Date(now+89*86400000).toISOString()}}}

test('external public HTTPS remains verified with no installed internal certificate',()=>{
 const d=domain(),c={installed:false};
 assert.equal(context.rootPublicHealth(d,now).key,'valid');
 const panel=context.domainCertificatePanel(d,c);
 assert.match(panel,/公网 HTTPS 已验证/);
 assert.match(panel,/宝塔 \/ 反向代理管理证书与续期/);
 assert.match(panel,/RemoteGate 内置证书/);
 assert.match(panel,/申请子域名证书/);
 assert.equal(context.domainHealth(c).key,'attention');
});

test('root status cannot inherit child or another-port verification',()=>{
 const d=domain();d.publicHTTPS.host='router.gate.example.com';
 assert.equal(context.rootPublicHealth(d,now).key,'attention');
 d.publicHTTPS.host=d.baseDomain;d.publicHTTPS.port=8443;
 assert.equal(context.rootPublicHealth(d,now).key,'attention');
 const old=JSON.stringify(d.publicHTTPS);
 context.applyPublicHTTPSCheck(d,{checkedAt:new Date(now).toISOString(),dns:[{host:'router.gate.example.com',port:443,certificate:{valid:true}}]});
 assert.equal(JSON.stringify(d.publicHTTPS),old);
});

test('stale, expired and failed results do not report current HTTPS success',()=>{
 let d=domain();d.publicHTTPS.checkedAt=new Date(now-25*3600000).toISOString();
 assert.equal(context.rootPublicHealth(d,now).text,'公网待复检');
 d=domain();d.publicHTTPS.expiresAt=new Date(now-1000).toISOString();
 assert.equal(context.rootPublicHealth(d,now).text,'公网证书已过期');
 context.applyPublicHTTPSCheck(d,{checkedAt:new Date(now).toISOString(),dns:[{host:d.baseDomain,port:443,certificate:{valid:false,error:'hostname mismatch'}}]});
 assert.equal(context.rootPublicHealth(d,now).text,'公网 HTTPS 异常');
});

test('external root is omitted only from new ACME defaults; saved scope is retained',()=>{
 const d=domain();
 assert.deepEqual(Array.from(context.certificateDefaultNames(d)),['*.gate.example.com']);
 assert.deepEqual(Array.from(context.certificateDefaultNames(d,{names:[d.baseDomain,'*.gate.example.com']})),[d.baseDomain,'*.gate.example.com']);
 assert.deepEqual(Array.from(context.certificateDefaultNames({baseDomain:'example.com'})),['example.com','*.example.com']);
 d.rootHTTPSProvider='';
 assert.deepEqual(Array.from(context.certificateDefaultNames(d)),['gate.example.com','*.gate.example.com']);
 assert.equal(context.certificateCoversHost(['*.example.com'],'gate.example.com'),true);
 assert.equal(context.certificateCoversHost(['*.gate.example.com'],'gate.example.com'),false);
 assert.equal(context.certificateCoversHost(['*.example.com'],'router.gate.example.com'),false);
});

test('saved public verification renders expiry without inventing a DNS match',()=>{
 const panel=context.domainDNSPanel(domain());
 assert.match(panel,/上次检测记录/);
 assert.match(panel,/HTTPS 验证通过/);
 assert.doesNotMatch(panel,/undefined 天|IP 匹配/);
});
test('installed certificates expose disabled and failed HTTPS listeners before advanced details',()=>{
 const d=domain(),c={installed:true,source:'acme',certificate:{names:['*.gate.example.com'],valid:true,daysRemaining:89,expiresAt:new Date(now+89*86400000).toISOString(),notBefore:new Date(now-86400000).toISOString()}};
 const panel=context.domainCertificatePanel(d,c);
 assert.match(panel,/证书已安装，HTTPS 入口未启用/);
 assert.ok(panel.indexOf('证书已安装，HTTPS 入口未启用')<panel.indexOf('证书详情与最近结果'));
 assert.match(context.certificateEntryNotice({...c,httpsError:'address already in use'}),/内置 HTTPS 启动失败/);
 assert.match(context.certificateEntryNotice({...c,httpsAddress:':8443'}),/内置 HTTPS 监听 :8443/);
 assert.doesNotMatch(context.certificateEntryNotice({...c,httpsAddress:':8443'}),/公网 HTTPS 已验证/);
 assert.match(context.certificateEntryNotice({...c,httpsAddress:':443',httpsSettings:{mode:'shared'}}),/共用 HTTPS 入口 :443/);
 assert.match(context.certificateEntryNotice({...c,httpsAddress:':443',httpsSettings:{mode:'shared'}}),/其他域名转给宝塔，保留原证书/);
});
