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
 const timeout=setTimeout(()=>controller.abort(),25000);
 const transition=authTransition;
 refreshTransition=transition;refreshController=controller;
 setRefreshProgress(true);$('#sync').textContent='正在同步…';
 const operation=(async()=>{
  try{
   const [snapshot,pkg,domains]=await Promise.all(['state','package','domains'].map(path=>api(path,'GET',undefined,{signal:controller.signal})));
   // A response started before logout must not repopulate the new login screen.
   if(transition!==authTransition)throw Object.assign(Error('登录状态已变化，请重新刷新'),{status:401});
   state={...snapshot,package:pkg,domains:domains||[],domain:domains?.[0]||{},certificates:{},devices:snapshot.devices||[],mappings:snapshot.mappings||[]};
   if(pkg?.server)$('#serverBuildLabel').textContent='RemoteGate v'+pkg.server.version;
   render();$('#sync').textContent='已同步 '+new Date().toLocaleTimeString();
  }catch(e){
   if(e.name==='AbortError')e=Error('刷新超时，请检查网络后重试');
   if(transition===authTransition)$('#sync').textContent='同步失败：'+e.message;throw e;
  }finally{
   clearTimeout(timeout);controller.abort();
   if(refreshPending===operation){refreshPending=null;refreshController=null;setRefreshProgress(false)}
  }
 })();
 refreshPending=operation;return operation;
}
async function refreshManually(){
 try{await refresh();toast('已刷新最新数据')}
 catch(e){toast(e.message||'刷新失败，请稍后重试')}
}
