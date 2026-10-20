let pluginUpdateDialog=null;
function pluginUpdateButton(device){
 return `<button type="button" data-action="updatePlugin" data-id="${esc(device.id)}">${uiIcon('download')}更新插件</button>`;
}
const pluginPackageRender=renderPackages;
renderPackages=()=>{pluginPackageRender();$('#content').querySelectorAll('[data-action="updatePlugin"]').forEach(button=>button.onclick=()=>openPluginUpdate(button.dataset.id))};
function pluginUpdateActive(view){return ['checking','downloading','backing_up','installing','restarting','verifying','rolling_back'].includes(view?.state?.phase)}
function pluginUpdateCurrent(context){return pluginUpdateDialog===context&&editing?.type==='pluginUpdate'&&$('#modal').open&&context.transition===authTransition}
function closePluginUpdate(){
 const context=pluginUpdateDialog;pluginUpdateDialog=null;if(!context)return;
 clearTimeout(context.timer);clearTimeout(context.requestTimeout);clearTimeout(context.bootstrapExpiryTimer);context.controller?.abort();
}
$('#modal').addEventListener('close',closePluginUpdate);
const pluginUpdateEditorSubmit=$('#editor').onsubmit;
$('#editor').onsubmit=e=>{if(editing?.type==='pluginUpdate'){e.preventDefault();return}return pluginUpdateEditorSubmit(e)};
const pluginUpdateAction=action;
action=async(a,id)=>a==='updatePlugin'?openPluginUpdate(id):pluginUpdateAction(a,id);
function openPluginUpdate(id){
 const device=state.devices.find(d=>d.id===id);if(!device)return;
 closePluginUpdate();clearInterval(installPoll);
 const context={id,device,transition:authTransition,view:null,busy:false,timer:null,controller:null,bootstrap:null,bootstrapAttempted:false,bootstrapError:'',bootstrapServer:location.origin};pluginUpdateDialog=context;editing={type:'pluginUpdate'};
 $('#modal').classList.remove('setup-dialog');$('#modalTitle').textContent='更新插件 · '+device.name;
 $('#cancel').textContent='关闭';$('#cancel').onclick=()=>$('#modal').close();$('#save').type='button';$('#save').hidden=true;$('#save').onclick=null;$('#formError').textContent='';
 $('#fields').innerHTML='<div class="plugin-update-loading" role="status">正在检查设备版本…</div>';
 if(!$('#modal').open)$('#modal').showModal();return requestPluginUpdate(context,'GET');
}
function renderPluginUpdate(context){
 if(!pluginUpdateCurrent(context))return;
 const view=context.view||{},manifest=view.manifest,progress=view.state||{},active=pluginUpdateActive(view);
 const legacy=view.online&&!view.canUpdate,canStart=view.online&&view.canUpdate&&view.available&&manifest&&!active&&!view.error&&!progress.rollbackFailed;
 const summary=view.error||(active?progress.message||'正在更新插件':!view.online?'设备离线，恢复连接后可在线更新':legacy?'当前插件还没有在线更新功能，需要先升级一次':progress.phase==='failed'?progress.message:view.available?'发现新版 '+manifest?.packageVersion:progress.message||'当前已是服务器提供的版本，或版本更高');
 $('#fields').innerHTML=`<section class="plugin-update-summary"><div><span>当前插件</span><strong>${esc(view.installedVersion||context.device.packageVersion||'旧版未上报')}</strong></div><div><span>服务器提供</span><strong>${esc(manifest?.packageVersion||'暂无可用安装包')}</strong></div></section><div class="plugin-update-progress ${view.error||progress.phase==='failed'?'has-error':''}" role="status" aria-live="polite"><b>${esc(active?(progress.phase==='rolling_back'?'正在自动回滚':progress.phase==='verifying'?'正在验证连接':'更新进行中'):progress.phase==='succeeded'?'更新完成':progress.rolledBack?'更新失败 · 已自动回滚':progress.rollbackFailed?'需要手动恢复':'插件更新')}</b><p>${esc(summary)}</p></div><p class="muted">更新时连接会暂时中断，设备 ID、令牌、连接配置和已有映射会保留。关闭窗口后，已发起的更新会继续执行。</p><p class="muted">${view.recoverySupported?'已启用失败自动回滚：更新前备份旧插件，重连和心跳验收失败会恢复旧版。':'当前插件尚未支持失败自动回滚，需先升级到支持此功能的版本，之后的在线更新才受保护。'}</p>${view.error?`<p class="danger">${esc(view.error)}</p>`:''}<div class="plugin-update-tools"><button type="button" id="pluginUpdateCheck" ${context.busy||active?'disabled':''}>${uiIcon('refresh')}重新检查</button>${manifest?'<a class="package-link" href="/downloads/remotegate.run" download>下载安装包</a>':''}</div>${legacy&&manifest&&view.available?`<details class="plugin-update-bootstrap" open><summary>旧版首次升级 · 保留配置</summary><p class="muted">复制下面的命令，在路由器 SSH 中执行一次；之后即可从本页面或路由器“服务 → RemoteGate”在线更新。</p><div id="pluginUpdateCommand" aria-live="polite"></div><div class="plugin-update-tools"><button type="button" id="pluginUpdateCopy" hidden>复制升级命令</button><button type="button" id="pluginUpdateBootstrap">重新生成升级命令</button></div><details class="plugin-update-server"><summary>修改路由器可访问的服务端地址</summary><label>服务端地址<input id="pluginUpdateServer" type="url" value="${esc(context.bootstrapServer)}" placeholder="https://console.example.com" autocomplete="off"></label><p class="muted">保存的插件服务器地址应使用 HTTPS；公网 HTTP 不支持后续在线更新。</p></details></details>`:''}`;
 $('#pluginUpdateCheck').onclick=()=>requestPluginUpdate(context,'GET');
 const bootstrap=$('#pluginUpdateBootstrap');if(legacy&&manifest&&view.available&&bootstrap){
  bootstrap.onclick=()=>createPluginBootstrap(context);
  $('#pluginUpdateServer').oninput=()=>{context.bootstrapServer=$('#pluginUpdateServer').value.trim();context.bootstrap=null;context.bootstrapError='服务端地址已修改，请重新生成升级命令。';renderPluginBootstrap(context)};
  renderPluginBootstrap(context);
 }
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
  if(!pluginUpdateCurrent(context))return;
  if(context.view?.manifest?.sha256!==result.manifest?.sha256||context.view?.manifest?.packageVersion!==result.manifest?.packageVersion){context.bootstrap=null;context.bootstrapAttempted=false;context.bootstrapError=''}
  context.view=result;context.busy=false;renderPluginUpdate(context);
 }catch(e){
  if(!pluginUpdateCurrent(context))return;
  context.busy=false;const message=e.name==='AbortError'?'检查超时，请稍后重新检查更新状态':e.message;
  if(!context.view)context.view={online:context.device.online,canUpdate:context.device.canUpdate,state:{phase:'idle'},error:message};
  renderPluginUpdate(context);$('#formError').textContent=message;
 }finally{
  clearTimeout(timeout);if(context.controller===controller)context.controller=null;context.busy=false;
  if(pluginUpdateCurrent(context)){$('#save').disabled=false;const check=$('#pluginUpdateCheck');if(check)check.disabled=pluginUpdateActive(context.view)}
 }
 if(pluginUpdateCurrent(context)&&context.view?.online&&!context.view.canUpdate&&context.view.available&&context.view.manifest&&!context.bootstrapAttempted)await createPluginBootstrap(context);
}
async function startPluginUpdate(context){
 if(!pluginUpdateCurrent(context)||context.busy||!context.view?.available||!context.view?.canUpdate||!context.view?.online||pluginUpdateActive(context.view))return;
 if(!confirm('将 '+context.device.name+' 更新到 '+context.view.manifest.packageVersion+'？连接会暂时中断，现有配置会保留。'))return;
 await requestPluginUpdate(context,'POST');
}
function renderPluginBootstrap(context){
 if(!pluginUpdateCurrent(context))return;
 const container=$('#pluginUpdateCommand'),button=$('#pluginUpdateBootstrap'),copy=$('#pluginUpdateCopy'),input=$('#pluginUpdateServer');if(!container||!button||!copy||!input)return;
 const command=context.bootstrap,expired=command&&Date.parse(command.expiresAt)<=Date.now();
 button.disabled=!!context.busy;button.textContent=context.bootstrapGenerating?'正在生成…':'重新生成升级命令';input.disabled=!!context.busy;copy.hidden=!command||!!expired||!!context.bootstrapGenerating;copy.disabled=!!context.busy;
 if(context.bootstrapGenerating)container.innerHTML='<p class="muted" role="status">正在生成首次升级命令…</p>';
 else if(context.bootstrapError)container.innerHTML=`<p class="danger" role="alert">${esc(context.bootstrapError)}</p>`;
 else if(expired)container.innerHTML='<p class="danger" role="alert">升级命令已过期，请点击“重新生成升级命令”。</p>';
 else if(command){container.innerHTML='<pre id="pluginUpdateCommandText"></pre><p class="muted">只升级现有插件，保留设备与连接配置。命令 15 分钟内有效，只可下载一次；使用或失效后可重新生成。</p>';$('#pluginUpdateCommandText').textContent=command.command;copy.onclick=()=>{if(Date.parse(command.expiresAt)<=Date.now()){renderPluginBootstrap(context);return}return copyText(command.command,{button:copy,source:$('#pluginUpdateCommandText')})}}
 else container.innerHTML='<p class="muted" role="status">正在准备首次升级命令…</p>';
 const check=$('#pluginUpdateCheck');if(check)check.disabled=!!context.busy||pluginUpdateActive(context.view);
 clearTimeout(context.bootstrapExpiryTimer);if(command&&!expired)context.bootstrapExpiryTimer=setTimeout(()=>renderPluginBootstrap(context),Math.max(1,Date.parse(command.expiresAt)-Date.now()));
}
async function createPluginBootstrap(context){
 if(!pluginUpdateCurrent(context)||context.busy)return;
 const server=$('#pluginUpdateServer').value.trim();context.bootstrapServer=server;context.bootstrap=null;context.bootstrapAttempted=true;context.bootstrapError='';context.bootstrapGenerating=true;context.busy=true;$('#formError').textContent='';renderPluginBootstrap(context);
 const controller=new AbortController(),timeout=setTimeout(()=>controller.abort(),15000);context.controller=controller;
 context.requestTimeout=timeout;
 try{
  const result=await api('devices/'+encodeURIComponent(context.id)+'/update/bootstrap','POST',{server},{signal:controller.signal});
  if(!pluginUpdateCurrent(context))return;
  if(!result.command||!Number.isFinite(Date.parse(result.expiresAt)))throw Error('服务端没有返回有效的升级命令，请重试');
  if(result.packageVersion!==context.view.manifest.packageVersion)throw Error('服务端安装包已变化，请重新检查更新');
  context.bootstrap=result;
 }catch(e){if(pluginUpdateCurrent(context))context.bootstrapError=e.name==='AbortError'?'生成命令超时，请点击“重新生成升级命令”重试':e.message}
 finally{clearTimeout(timeout);context.busy=false;context.bootstrapGenerating=false;if(context.controller===controller)context.controller=null;if(pluginUpdateCurrent(context))renderPluginBootstrap(context)}
}
