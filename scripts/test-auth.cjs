const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');

function authPage(panel,refreshImpl=async()=>{}){
 const nodes=new Map();
 function $(selector){
  if(!nodes.has(selector))nodes.set(selector,{hidden:true,value:'',textContent:'',innerHTML:'',open:false,attributes:{},classList:{add(){},remove(){}},focus(){},reset(){},close(){this.open=false},setAttribute(key,value){this.attributes[key]=value}});
  return nodes.get(selector);
 }
 $('#authInitialState').textContent=JSON.stringify({panel,entryPath:'/private-panel'});
 let refreshes=0;
 const context=vm.createContext({$,document:{body:{classList:{add(){},remove(){}}},querySelectorAll:()=>[...nodes.values()].filter(node=>node.open)},location:{pathname:'/private-panel',origin:'http://console.example.com'},history:{replaceState(){}},sessionStorage:{removeItem(){}},refresh:async()=>{refreshes++;await refreshImpl($)},fetch:()=>{throw Error('unexpected auth status request')},toast(){}});
 vm.runInContext(fs.readFileSync('cmd/server/web/auth.js','utf8'),context);
 return {context,$,refreshes:()=>refreshes};
}
test('installed page opens login without requesting installation status',async()=>{
 const page=authPage('login');await page.context.initializeAuth();
 assert.equal(page.$('#login').hidden,false);assert.equal(page.$('#firstInstall').hidden,true);assert.equal(page.$('#shell').hidden,true);assert.equal(page.refreshes(),0);
});
test('fresh page shows only installation without requesting status',async()=>{
 const page=authPage('firstInstall');await page.context.initializeAuth();
 assert.equal(page.$('#firstInstall').hidden,false);assert.equal(page.$('#login').hidden,true);assert.equal(page.refreshes(),0);
});
test('valid session shows workbench while data is still loading',async()=>{
 let release;const pending=new Promise(resolve=>{release=resolve});
 const page=authPage('workbench',()=>pending);const boot=page.context.initializeAuth();
 assert.equal(page.$('#shell').hidden,false);assert.equal(page.$('#authScreen').hidden,true);assert.match(page.$('#content').innerHTML,/正在加载设备/);assert.equal(page.$('#content').attributes['aria-busy'],'true');
 release();await boot;assert.equal(page.refreshes(),1);assert.equal(page.$('#content').attributes['aria-busy'],'false');
});
test('network failure stays in workbench and provides a working retry',async()=>{
 let fail=true;
 const page=authPage('workbench',async()=>{if(fail)throw Error('network failure')});await page.context.initializeAuth();
 assert.equal(page.$('#shell').hidden,false);assert.match(page.$('#content').innerHTML,/连接服务器失败/);assert.equal(page.$('#content').attributes['aria-busy'],'false');
 fail=false;await page.$('#consoleRetry').onclick();assert.equal(page.refreshes(),2);assert.equal(page.$('#shell').hidden,false);
});
test('expired session returns to login without reopening the workbench',async()=>{
 const page=authPage('workbench',async()=>{throw Object.assign(Error('登录已过期，请重新登录'),{status:401})});page.$('#modal').open=true;page.$('#certDialog').open=true;
 await page.context.initializeAuth();assert.equal(page.$('#shell').hidden,true);assert.equal(page.$('#login').hidden,false);assert.match(page.$('#loginerror').textContent,/登录已过期/);assert.equal(page.$('#modal').open,false);assert.equal(page.$('#certDialog').open,false);
});
