const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const editor={onsubmit:()=>{}};
const context=vm.createContext({Number,Date,Map,URL,state:{domains:[],mappings:[]},openEditor:()=>{},$:()=>editor,esc:String});
for(const name of ['mapping-editor.js','domain-center.js','https-deploy.js'])vm.runInContext(fs.readFileSync('cmd/server/web/'+name,'utf8'),context);

test('Baota wildcard instructions cover binding, DNS verification and activation',()=>{
 const guide=context.baotaCertificateGuide({baseDomain:'gate.example.com'});
 for(const text of ['*.gate.example.com','gate.example.com','DNS 验证','保存并启用','自动续期','一级子域名'])assert.ok(guide.includes(text),text);
 assert.doesNotMatch(guide,/主机助手|授权宝塔 API|迁移所有|9443|8443/);
});
test('mapping advice makes no certificate API request or change to the selected endpoint',()=>{
 const editorContext={},node={hidden:true,innerHTML:''},mapping={host:'router.gate.example.com',publicScheme:'https',publicPort:443};
 context.editing=editorContext;context.state={domains:[{baseDomain:'gate.example.com'}]};
 context.document={getElementById:()=>node};context.$=()=>({open:true});
 context.api=()=>{throw Error('read-only advice must not request or change certificates')};
 const advice=context.createMappingHTTPSAdvice(editorContext,()=>mapping,()=>{throw Error('must not rewrite mapping')});
 advice.refresh();assert.equal(mapping.publicPort,443);
 assert.match(node.innerHTML,/待检测/);assert.match(node.innerHTML,/在宝塔 \/ 反向代理申请并启用/);
 mapping.publicScheme='http';advice.refresh();assert.equal(node.hidden,true);
});
test('standard public URLs retain implicit ports and custom ports remain visible',()=>{
 assert.equal(context.publicMappingURL({host:'router.example.com',publicScheme:'https',publicPort:443}),'https://router.example.com/');
 assert.equal(context.publicMappingURL({host:'router.example.com',publicScheme:'http',publicPort:80}),'http://router.example.com/');
 assert.equal(context.publicMappingURL({host:'router.example.com',publicScheme:'https',publicPort:8443}),'https://router.example.com:8443/');
});
