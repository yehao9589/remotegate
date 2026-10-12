let pluginUpdateDialog=null;
function pluginUpdateButton(device){
 return `<button type="button" data-action="updatePlugin" data-id="${esc(device.id)}">${uiIcon('download')}更新插件</button>`;
}
const pluginPackageRender=renderPackages;
renderPackages=()=>{pluginPackageRender();$('#content').querySelectorAll('[data-action="updatePlugin"]').forEach(button=>button.onclick=()=>openPluginUpdate(button.dataset.id))};
function pluginUpdateActive(view){return ['checking','downloading','installing','restarting'].includes(view?.state?.phase)}
function pluginUpdateCurrent(context){return pluginUpdateDialog===context&&editing?.type==='pluginUpdate'&&$('#modal').open&&context.transition===authTransition}
function closePluginUpdate(){
 const context=pluginUpdateDialog;pluginUpdateDialog=null;if(!context)return;
 clearTimeout(context.timer);clearTimeout(context.requestTimeout);context.controller?.abort();
}
$('#modal').addEventListener('close',closePluginUpdate);
const pluginUpdateEditorSubmit=$('#editor').onsubmit;
$('#editor').onsubmit=e=>{if(editing?.type==='pluginUpdate'){e.preventDefault();return}return pluginUpdateEditorSubmit(e)};
const pluginUpdateAction=action;
action=async(a,id)=>a==='updatePlugin'?openPluginUpdate(id):pluginUpdateAction(a,id);
function openPluginUpdate(id){
 const device=state.devices.find(d=>d.id===id);if(!device)return;
 closePluginUpdate();clearInterval(installPoll);
 const context={id,device,transition:authTransition,view:null,busy:false,timer:null,controller:null};pluginUpdateDialog=context;editing={type:'pluginUpdate'};
 $('#modal').classList.remove('setup-dialog');$('#modalTitle').textContent='更新插件 · '+device.name;
 $('#cancel').textContent='关闭';$('#cancel').onclick=()=>$('#modal').close();$('#save').type='button';$('#save').hidden=true;$('#save').onclick=null;$('#formError').textContent='';
 $('#fields').innerHTML='<div class="plugin-update-loading" role="status">正在检查设备版本…</div>';
 if(!$('#modal').open)$('#modal').showModal();return requestPluginUpdate(context,'GET');
}
function renderPluginUpdate(context){
 if(!pluginUpdateCurrent(context))return;
 const view=context.view||{},manifest=view.manifest,progress=view.state||{},active=pluginUpdateActive(view);
 const legacy=view.online&&!view.canUpdate,canStart=view.online&&view.canUpdate&&view.available&&manifest&&!active&&!view.error;
 const summary=view.error||(active?progress.message||'正在更新插件':!view.online?'设备离线，恢复连接后可在线更新':legacy?'当前插件还没有在线更新功能，需要先升级一次':progress.phase==='failed'?progress.message:view.available?'发现新版 '+manifest?.packageVersion:progress.message||'当前已是服务器提供的版本，或版本更高');
 $('#fields').innerHTML=`<section class="plugin-update-summary"><div><span>当前插件</span><strong>${esc(view.installedVersion||context.device.packageVersion||'旧版未上报')}</strong></div><div><span>服务器提供</span><strong>${esc(manifest?.packageVersion||'暂无可用安装包')}</strong></div></section><div class="plugin-update-progress ${view.error||progress.phase==='failed'?'has-error':''}" role="status" aria-live="polite"><b>${esc(active?'更新进行中':progress.phase==='succeeded'?'更新完成':'插件更新')}</b><p>${esc(summary)}</p></div><p class="muted">更新时连接会暂时中断，设备 ID、令牌、连接配置和已有映射会保留。关闭窗口后，已发起的更新会继续执行。</p>${view.error?`<p class="danger">${esc(view.error)}</p>`:''}<div class="plugin-update-tools"><button type="button" id="pluginUpdateCheck" ${context.busy||active?'disabled':''}>${uiIcon('refresh')}重新检查</button>${manifest?'<a class="package-link" href="/downloads/remotegate.run" download>下载安装包</a>':''}</div>${legacy&&manifest&&view.available?`<details class="plugin-update-bootstrap" open><summary>旧版首次升级 · 保留配置</summary><p class="muted">在路由器 SSH 中执行一次升级命令，之后即可从本页面或路由器“服务 → RemoteGate”在线更新。</p><label>路由器可访问的服务端地址<input id="pluginUpdateServer" type="url" value="${esc(location.origin)}" placeholder="https://console.example.com" autocomplete="off"></label><p class="muted">保存的插件服务器地址应使用 HTTPS；公网 HTTP 不支持后续在线更新。</p><button type="button" id="pluginUpdateBootstrap">生成首次升级命令</button><div id="pluginUpdateCommand"></div></details>`:''}`;
 $('#pluginUpdateCheck').onclick=()=>requestPluginUpdate(context,'GET');
 const bootstrap=$('#pluginUpdateBootstrap');if(bootstrap)bootstrap.onclick=()=>createPluginBootstrap(context,bootstrap);
 $('#save').hidden=!canStart;$('#save').disabled=context.busy;$('#save').textContent='更新到 '+(manifest?.packageVersion||'新版');$('#save').onclick=()=>startPluginUpdate(context);
 clearTimeout(context.timer);if(active)context.timer=setTimeout(()=>requestPluginUpdate(context,'GET'),3000);
}
async function requestPluginUpdate(context,method){
 if(!pluginUpdateCurrent(context)||context.busy)return;
 context.busy=true;clearTimeout(context.timer);context.controller=new AbortController();
 const controller=context.controller,timeout=setTimeout(()=>controller.abort(),25000);
 context.requestTimeout=timeout;
 $('#save').disabled=true;const check=$('#pluginUpdateCheck');if(check)check.disabled=true;
 $('#formError').textContent='';
 try{
  const result=await api('devices/'+encodeURIComponent(context.id)+'/update',method,method==='POST'?context.view.manifest:undefined,{signal:controller.signal});
  if(!pluginUpdateCurrent(context))return;context.view=result;context.busy=false;renderPluginUpdate(context);
 }catch(e){
  if(!pluginUpdateCurrent(context))return;
  context.busy=false;const message=e.name==='AbortError'?'检查超时，请稍后重新检查更新状态':e.message;
  if(!context.view)context.view={online:context.device.online,canUpdate:context.device.canUpdate,state:{phase:'idle'},error:message};
  renderPluginUpdate(context);$('#formError').textContent=message;
 }finally{
  clearTimeout(timeout);if(context.controller===controller)context.controller=null;context.busy=false;
  if(pluginUpdateCurrent(context)){$('#save').disabled=false;const check=$('#pluginUpdateCheck');if(check)check.disabled=pluginUpdateActive(context.view)}
 }
}
async function startPluginUpdate(context){
 if(!pluginUpdateCurrent(context)||context.busy||!context.view?.available||!context.view?.canUpdate||!context.view?.online||pluginUpdateActive(context.view))return;
 if(!confirm('将 '+context.device.name+' 更新到 '+context.view.manifest.packageVersion+'？连接会暂时中断，现有配置会保留。'))return;
 await requestPluginUpdate(context,'POST');
}
async function createPluginBootstrap(context,button){
 if(!pluginUpdateCurrent(context)||context.busy)return;
 context.busy=true;button.disabled=true;$('#formError').textContent='';
 const server=$('#pluginUpdateServer').value.trim(),controller=new AbortController(),timeout=setTimeout(()=>controller.abort(),15000);context.controller=controller;
 context.requestTimeout=timeout;
 try{
  const result=await api('devices/'+encodeURIComponent(context.id)+'/update/bootstrap','POST',{server},{signal:controller.signal});
  if(!pluginUpdateCurrent(context))return;
  $('#pluginUpdateCommand').innerHTML='<p class="muted">仅用于升级现有插件，不重新绑定设备。命令 15 分钟内有效，只可下载一次。</p><pre id="pluginUpdateCommandText"></pre><button type="button" id="pluginUpdateCopy">复制升级命令</button>';
  $('#pluginUpdateCommandText').textContent=result.command;$('#pluginUpdateCopy').onclick=()=>copyText(result.command,{button:$('#pluginUpdateCopy'),source:$('#pluginUpdateCommandText')});
 }catch(e){if(pluginUpdateCurrent(context))$('#formError').textContent=e.name==='AbortError'?'生成命令超时，请重试':e.message}
 finally{clearTimeout(timeout);context.busy=false;if(context.controller===controller)context.controller=null;if(pluginUpdateCurrent(context))button.disabled=false}
}
