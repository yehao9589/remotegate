const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function fixture() {
  const markup = '<svg aria-hidden="true"><path d="M3 3h6"/></svg><span>复制</span>';
  let html = markup;
  const button = {disabled:false,isConnected:true,focus(){}};
  Object.defineProperties(button, {
    innerHTML:{get:()=>html,set:value=>{html=value}},
    textContent:{get:()=>html.replace(/<[^>]*>/g,''),set:value=>{html=value}},
  });
  const timers = new Map();
  let id = 0;
  const context = vm.createContext({
    document:{activeElement:button},window:{isSecureContext:true},
    navigator:{clipboard:{writeText:async()=>{}}},toast(){},
    setTimeout(fn){timers.set(++id,fn);return id},clearTimeout(id){timers.delete(id)},
  });
  vm.runInContext(fs.readFileSync('cmd/server/web/clipboard.js','utf8'),context);
  return {context,button,markup,finish:()=>{for(const callback of [...timers.values()])callback();timers.clear()}};
}

test('copy feedback restores the SVG and text label',async()=>{
  const f=fixture();f.context.button=f.button;
  assert.equal(await vm.runInContext('copyText("test",{button})',f.context),true);
  assert.equal(f.button.textContent,'已复制');assert.equal(f.button.disabled,false);
  f.finish();assert.equal(f.button.innerHTML,f.markup);
});

test('repeated copies restore the original icon instead of the feedback label',async()=>{
  const f=fixture();f.context.button=f.button;
  await vm.runInContext('copyText("first",{button})',f.context);
  await vm.runInContext('copyText("second",{button})',f.context);
  f.finish();assert.equal(f.button.innerHTML,f.markup);
});
