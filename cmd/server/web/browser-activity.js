(()=>{
 'use strict';
 if(window.__remoteGateActivity)return;
 const tag=document.currentScript,token=tag?.dataset.token,idle=Number(tag?.dataset.idle)*1000,background=Number(tag?.dataset.background)*1000;
 if(!token||(!idle&&!background))return;
 window.__remoteGateActivity=true;
 const nativeFetch=window.fetch?.bind(window),NativeSocket=window.WebSocket,NativeSource=window.EventSource;
 const endpoint='/.remotegate/activity/update',query='__remotegate_activity',sockets=new Set(),sources=new Set(),reads=new Set();
 let touched=Date.now(),hiddenSince=document.hidden?Date.now():0,paused=false,dirty=false,lastReport=0,reporting=false;
 const local=url=>{try{const parsed=new URL(typeof url==='string'||url instanceof URL?url:url.url,location.href);return parsed.host===location.host&&['http:','https:','ws:','wss:'].includes(parsed.protocol)}catch{return false}};
 const expired=()=>Boolean(idle&&Date.now()-touched>=idle||background&&hiddenSince&&Date.now()-hiddenSince>=background);
 async function report(active=false,pause=false){
  if(!nativeFetch||reporting)return;
  reporting=true;lastReport=Date.now();
  try{const response=await nativeFetch(endpoint,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({token,active,hidden:document.hidden,pause}),keepalive:pause});if(!response.ok&&!paused)stop('页面连接或验证已过期，请点击继续查看。')}
  catch{}finally{reporting=false}
 }
 function banner(message){
  if(document.getElementById('remotegate-idle-notice'))return;
  const create=()=>{
   if(!document.body)return;
   const panel=document.createElement('section');panel.id='remotegate-idle-notice';panel.setAttribute('role','dialog');panel.setAttribute('aria-label','网页连接已暂停');
   const title=document.createElement('strong');title.textContent='连接已暂停';const text=document.createElement('p');text.textContent=message||'页面长时间未操作，已暂停实时数据和自动刷新，避免持续消耗流量。';
   const button=document.createElement('button');button.type='button';button.textContent='继续查看';button.onclick=event=>{if(event.isTrusted)location.reload()};panel.append(title,text,button);document.body.append(panel);
  };
  if(document.body)create();else document.addEventListener('DOMContentLoaded',create,{once:true});
 }
 function stop(message){
  if(paused)return;paused=true;
  for(const socket of sockets)try{socket.close(4001,'RemoteGate: inactive page')}catch{}
  for(const source of sources)source.close();
  for(const controller of reads)controller.abort();
  void report(false,true);banner(message);
 }
 function allowed(){if(!paused&&expired())stop();return !paused}
 if(NativeSocket){
  window.WebSocket=class extends NativeSocket{
   constructor(url,protocols){
    const managed=local(url);if(managed&&!allowed())throw new DOMException('RemoteGate: 页面已暂停，请点击继续查看','InvalidStateError');
    if(managed){const u=new URL(url,location.href);const parts=u.search.slice(1).split('&').filter(part=>{try{return decodeURIComponent(part.split('=')[0])!==query}catch{return true}});u.search=parts.filter(Boolean).concat(query+'='+encodeURIComponent(token)).join('&');url=u.href}
    if(protocols===undefined)super(url);else super(url,protocols);
    if(managed){sockets.add(this);this.addEventListener('close',()=>sockets.delete(this),{once:true})}
   }
  };
 }
 if(NativeSource){window.EventSource=class extends NativeSource{constructor(url,options){const managed=local(url);if(managed&&!allowed())throw new DOMException('RemoteGate: 页面已暂停','InvalidStateError');super(url,options);if(managed)sources.add(this)}}}
 if(nativeFetch)window.fetch=(input,init)=>{
  if(!local(input))return nativeFetch(input,init);
  if(!allowed())return Promise.reject(new TypeError('RemoteGate: 页面已暂停，请点击继续查看'));
  const method=String(init?.method||input?.method||'GET').toUpperCase();
  if(!['GET','HEAD'].includes(method))return nativeFetch(input,init);
  const controller=new AbortController(),signal=init?.signal||input?.signal;
  const abort=()=>controller.abort();if(signal?.aborted)abort();else signal?.addEventListener('abort',abort,{once:true});
  reads.add(controller);
  return nativeFetch(input,{...init,signal:controller.signal}).finally(()=>{reads.delete(controller);signal?.removeEventListener('abort',abort)});
 };
 const xhr=window.XMLHttpRequest?.prototype;
 if(xhr){const open=xhr.open,send=xhr.send;xhr.open=function(method,url,...args){this.__remoteGateManaged=local(url);return open.call(this,method,url,...args)};xhr.send=function(...args){if(this.__remoteGateManaged&&!allowed())throw new DOMException('RemoteGate: 页面已暂停','InvalidStateError');return send.apply(this,args)}}
 const activity=event=>{if(!event.isTrusted||document.hidden||paused)return;if(expired()){stop();return}touched=Date.now();dirty=true};
 for(const name of ['pointerdown','keydown','wheel','touchstart'])document.addEventListener(name,activity,{capture:true,passive:true});
 document.addEventListener('visibilitychange',()=>{if(paused)return;if(expired()){stop();return}hiddenSince=document.hidden?Date.now():0;void report(false)});
 window.addEventListener('pagehide',()=>stop());
 setInterval(()=>{if(!allowed())return;if(dirty&&!document.hidden&&Date.now()-lastReport>=15000){dirty=false;void report(true)}},1000);
 if(document.hidden)void report(false);
})();
