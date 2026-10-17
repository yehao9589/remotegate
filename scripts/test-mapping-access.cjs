const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');

function page(search){
 const nodes=new Map();const redirects=[];let entered=0;
 const $=selector=>{if(!nodes.has(selector))nodes.set(selector,{textContent:'',value:'secret'});return nodes.get(selector)};
 const context=vm.createContext({$,URLSearchParams,location:{search,replace:value=>redirects.push(value)},enterAdmin:async()=>{entered++}});
 vm.runInContext(fs.readFileSync('cmd/server/web/mapping-access.js','utf8'),context);
 return {context,$,redirects,entered:()=>entered};
}

test('mapping authorization survives successful login without loading console data',async()=>{
 const request='a'.repeat(64),p=page('?access_request='+request);
 await p.context.enterAdmin('/private-admin');
 assert.deepEqual(p.redirects,['/.remotegate/access/authorize?request='+request]);
 assert.equal(p.entered(),0);assert.equal(p.$('#password').value,'');
 assert.match(p.$('#login .auth-description').textContent,/自动返回原网站/);
});
test('normal console login and malformed continuations use the regular workbench',async()=>{
 for(const search of ['', '?access_request=https://evil.example.com','?access_request='+('a'.repeat(63))]){
  const p=page(search);await p.context.enterAdmin('/private-admin');
  assert.equal(p.entered(),1);assert.deepEqual(p.redirects,[]);
 }
});
