let refreshPending=null,refreshTransition=-1,refreshController=null;
function setRefreshProgress(active){
 const button=$('#refresh');button.disabled=active;
 button.setAttribute('aria-busy',String(active));
 button.classList.toggle('is-refreshing',active);
 button.innerHTML=uiIcon('refresh')+'<span>'+(active?'刷新中…':'刷新')+'</span>';
}
function refresh(){
 // Manual refresh joins an existing automatic update instead of silently returning.
 if(refreshPending&&refreshTransition===authTransition)return refreshPending;
 refreshController?.abort();
 const controller=new AbortController();
 const transition=authTransition;
 refreshTransition=transition;refreshController=controller;
 setRefreshProgress(true);$('#sync').textContent='正在同步…';
 const operation=(async()=>{
  let failureSince=null;
  try{
   let snapshot,pkg,domains;
   for(;;){
    const attempt=new AbortController(),abort=()=>attempt.abort();controller.signal.addEventListener('abort',abort,{once:true});
    const timeout=setTimeout(()=>attempt.abort(),failureSince===null?8000:Math.max(1,Math.min(8000,30000-(Date.now()-failureSince))));
    try{[snapshot,pkg,domains]=await Promise.all(['state','package','domains'].map(path=>api(path,'GET',undefined,{signal:attempt.signal})));break}
    catch(e){
     if(controller.signal.aborted||transition!==authTransition)throw Object.assign(Error('登录状态已变化，请重新刷新'),{status:401});
     const transient=e.name==='TypeError'||e.name==='AbortError'||[502,503,504].includes(e.status);
     if(!transient)throw e;
     if(failureSince===null)failureSince=Date.now();
     if(Date.now()-failureSince>=30000)throw Error('网络连接持续失败，已尝试恢复 30 秒，请检查网络后重试');
     $('#sync').textContent='正在恢复连接… · 保留当前数据，自动重试';
     // Reads only: never replay save/delete/login operations after an uncertain result.
     await new Promise((resolve,reject)=>{const onAbort=()=>{clearTimeout(timer);reject(Object.assign(Error('已取消'),{name:'AbortError'}))};const timer=setTimeout(()=>{controller.signal.removeEventListener('abort',onAbort);resolve()},Math.min(2000,30000-(Date.now()-failureSince)));controller.signal.addEventListener('abort',onAbort,{once:true});if(controller.signal.aborted)onAbort()});
    }finally{clearTimeout(timeout);attempt.abort();controller.signal.removeEventListener('abort',abort)}
   }
   // A response started before logout must not repopulate the new login screen.
   if(transition!==authTransition)throw Object.assign(Error('登录状态已变化，请重新刷新'),{status:401});
   state={...snapshot,package:pkg,domains:domains||[],domain:domains?.[0]||{},certificates:{},devices:snapshot.devices||[],mappings:snapshot.mappings||[]};
   if(pkg?.server)$('#serverBuildLabel').textContent='RemoteGate v'+pkg.server.version;
   render();$('#sync').textContent='已同步 '+new Date().toLocaleTimeString();
  }catch(e){
   if(e.name==='AbortError')e=Error('刷新超时，请检查网络后重试');
   if(transition===authTransition)$('#sync').textContent='同步失败：'+e.message;throw e;
  }finally{
   controller.abort();
   if(refreshPending===operation){refreshPending=null;refreshController=null;setRefreshProgress(false)}
  }
 })();
 refreshPending=operation;return operation;
}
async function refreshManually(){
 try{await refresh();toast('已刷新最新数据')}
 catch(e){toast(e.message||'刷新失败，请稍后重试')}
}
