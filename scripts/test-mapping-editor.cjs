const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const editor={onsubmit:()=>{}};
const context=vm.createContext({URL,location:{protocol:'http:',port:'18088'},openEditor:()=>{},$:(selector)=>{assert.equal(selector,'#editor');return editor}});
vm.runInContext(fs.readFileSync('cmd/server/web/mapping-editor.js','utf8'),context);
const host=context.mappingHost,manual=vm.runInContext('customMappingDomain',context);

test('a short name needs an explicitly selected suffix',()=>{
 assert.throws(()=>host('ceshi01',''),/选择域名后缀/);
 assert.equal(host('ceshi01','gate.example.com'),'ceshi01.gate.example.com');
 assert.equal(host('ceshi01','example.com'),'ceshi01.example.com');
 assert.equal(host(' @ ','gate.example.com'),'gate.example.com');
 assert.equal(host('CESHI01.GATE.EXAMPLE.COM','gate.example.com'),'ceshi01.gate.example.com');
});
test('manual mode requires a complete domain, including local test domains',()=>{
 assert.equal(host('ceshi01.gate.example.com',manual),'ceshi01.gate.example.com');
 assert.equal(host('router.localhost',manual),'router.localhost');
 for(const value of ['ceshi01','https://router.example.com','router.example.com:443','*.example.com','router.example.com/path','127.0.0.1','-router.example.com','router..example.com',''])assert.throws(()=>host(value,manual));
});
test('mapping preview and split target preserve explicit ports and paths',()=>{
 assert.equal(context.publicMappingURL({host:host('ceshi01','gate.example.com'),publicScheme:'http',publicPort:8080}),'http://ceshi01.gate.example.com:8080/');
 const split=context.splitMappingTarget('https://192.168.1.20:8443/path');
 assert.equal(split.address,'https://192.168.1.20/path');assert.equal(split.port,'8443');
 assert.equal(context.joinMappingTarget(split.address,split.port),'https://192.168.1.20:8443/path');
});

test('target round trips preserve trailing slashes in paths, queries and fragments',()=>{
 for(const target of ['http://127.0.0.1:8080/app/','https://192.168.1.20:8443/?next=/','http://[::1]:9090/app/?next=/#section/']){
  const split=context.splitMappingTarget(target);
  assert.equal(context.joinMappingTarget(split.address,split.port),target);
 }
});
