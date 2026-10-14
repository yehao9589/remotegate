let packageCenterData=null,packageCenterPending=null,packageCenterTransition=-1,packageCenterTimer=null;
let packageCenterTab='install',packageDeviceQuery='',packageDeviceFilter='all',packageDownloadArch='';
const packageSelection=new Set();
const packagePhaseNames={ready:'待执行',preflight:'安装检查',checking:'准备更新',downloading:'下载中',verifying:'校验中',installing:'安装中',configuring:'应用模板',starting:'启动中',waiting:'等待连接',restarting:'重新连接',succeeded:'已完成',failed:'未完成',expired:'已过期',idle:'未执行',pending:'等待执行',running:'执行中',queued:'排队中',paused:'已暂停',cancelled:'已取消',completed:'已结束',interrupted:'等待核对'};
function packageCompare(a,b){
 const parse=x=>/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([1-9]\d*))?$/.exec(x||'');
 const x=parse(a),y=parse(b);if(!x||!y)return null;
 for(let i=1;i<5;i++){const n=BigInt(x[i]||0),m=BigInt(y[i]||0);if(n!==m)return n>m?1:-1}return 0;
}
function packageSize(bytes){return Number.isFinite(Number(bytes))?(Number(bytes)/(1024*1024)).toFixed(2)+' MB':'—'}
function packageDate(value){return value&&!String(value).startsWith('0001-')?new Date(value).toLocaleString():'—'}
function packageProfileSummary(p){return `${p.remoteAccess?'SSH / 文件管理已开启':'SSH / 文件管理关闭'} · ${p.autoStart?'开机启动':'手动启动'} · ${p.interfaceMode==='select'?'安装时选择网卡':'自动选择出口'}`}
function packageEligible(d){return !!(d.online&&d.canUpdate&&!packageCenterData?.policies?.[d.id]?.locked&&packageCompare(state.package?.manifest?.packageVersion,d.packageVersion)>0)}
function packageSafeDownload(url){return /^\/downloads\/remotegate\.run(?:\?|$)/.test(url||'')?url:'/downloads/remotegate.run'}
async function loadPackageCenter(draw=true){
 if(packageCenterPending&&packageCenterTransition===authTransition)return packageCenterPending;
 const transition=authTransition;packageCenterTransition=transition;
 const request=(async()=>{
  try{
   const [data,snapshot]=await Promise.all([api('package-center'),api('state')]);
   if(transition!==authTransition)return;
   packageCenterData=data;state={...state,...snapshot};
   if(draw&&page==='packages'&&!document.querySelector('dialog[open]'))renderPackages();
   return data;
  }catch(e){if(transition===authTransition&&page==='packages'){$('#packageCenterError')?.replaceChildren(document.createTextNode(e.message));toast(e.message)}throw e}
  finally{if(packageCenterPending===request)packageCenterPending=null}
 })();packageCenterPending=request;return request;
}
function packagePolling(){
 clearTimeout(packageCenterTimer);
 if(page!=='packages'||$('#shell').hidden)return;
 const active=['running','queued'].includes(packageCenterData?.batch?.phase)||(packageCenterData?.history||[]).some(op=>['checking','preflight','downloading','verifying','installing','configuring','starting','waiting','restarting'].includes(op.state.phase));
 if(active)packageCenterTimer=setTimeout(async()=>{try{await loadPackageCenter(!document.activeElement?.matches('input,select,textarea'))}catch{}packagePolling()},4000);
}
renderPackages=()=>{
 if(packageCenterTransition!==authTransition){packageCenterData=null;packageSelection.clear()}
 const data=packageCenterData,p=state.package||{},m=p.manifest,build=p.server;
 const ready=state.devices.filter(packageEligible),locked=Object.values(data?.policies||{}).filter(x=>x.locked).length,done=(data?.history||[]).filter(x=>x.state.phase==='succeeded').length;
 $('#content').classList.add('install-center');
 $('#stats').innerHTML=[['当前插件',m?.packageVersion||'—','package'],['可更新设备',ready.length,'download'],['已锁定版本',locked,'shield'],['完成记录',done,'check']].map(([label,value,icon])=>`<div class="stat"><span class="stat-label">${esc(label)}</span><span class="stat-icon">${uiIcon(icon)}</span><strong>${esc(value)}</strong></div>`).join('');
 $('#content').innerHTML=`<div class="pc-heading"><div class="pc-quickstats"><span>当前插件<strong>${esc(m?.packageVersion||'—')}</strong></span><span>可更新设备<strong>${ready.length}</strong></span><span>版本锁定<strong>${locked}</strong></span><span>完成记录<strong>${done}</strong></span></div><button type="button" class="primary" id="pcConnect">${uiIcon('plus')}连接新设备</button></div><div class="pc-tabs" role="tablist" aria-label="安装与更新中心">${[['install','安装设备','package'],['devices','设备更新','refresh'],['profiles','安装模板','settings'],['history','历史记录','history']].map(([id,label,icon])=>`<button type="button" role="tab" aria-selected="${packageCenterTab===id}" data-pc-tab="${id}">${uiIcon(icon)}${label}</button>`).join('')}</div><p id="packageCenterError" class="danger" role="alert">${esc(data?.packageError||'')}</p><div id="pcBody">${!data?'<div class="pc-loading" role="status">正在读取安装资源与更新记录…</div>':packageCenterTab==='install'?packageInstallHTML(data,p,m,build):packageCenterTab==='devices'?packageDevicesHTML(data):packageCenterTab==='profiles'?packageProfilesHTML(data):packageHistoryHTML(data)}</div>`;
 $('#pcConnect').onclick=()=>openEditor('device');
 $('#content').querySelectorAll('[data-pc-tab]').forEach(button=>button.onclick=()=>{packageCenterTab=button.dataset.pcTab;renderPackages()});
 $('#content').querySelectorAll('[data-pc-action]').forEach(button=>button.onclick=()=>packageCenterAction(button.dataset.pcAction,button.dataset.id,button).catch(e=>toast(e.message)));
 if(packageCenterTab==='devices'&&data){
  $('#pcDeviceSearch').oninput=e=>{packageDeviceQuery=e.target.value;renderPackageDeviceRows()};$('#pcDeviceFilter').onchange=e=>{packageDeviceFilter=e.target.value;renderPackageDeviceRows()};renderPackageDeviceRows();
 }
 if(packageCenterTab==='install'&&data){$('#pcDownloadArch').onchange=e=>{packageDownloadArch=e.target.value;renderPackages()}}
 if(!data&&!packageCenterPending)loadPackageCenter().catch(()=>{});
 packagePolling();
};
function packageInstallHTML(data,p,m,build){
 const downloads=data.downloads||[],selected=downloads.find(x=>x.arch===packageDownloadArch)||downloads[0];
 const slim=downloads.find(x=>x.arch==='amd64'),saved=slim&&p.size?Math.round((1-slim.size/p.size)*100):0;
 return `<div class="pc-install-grid"><article class="pc-release"><div class="pc-release-top"><div class="pc-release-icon">${uiIcon('package')}</div><span class="pc-tag">服务器提供的安装包</span></div><h3>RemoteGate <span>${esc(m?.packageVersion||'未部署')}</span></h3><p>支持 iStoreOS / OpenWrt，使用 opkg 安装。</p><label>下载类型<select id="pcDownloadArch">${downloads.map(x=>`<option value="${esc(x.arch)}" ${x.arch===(selected?.arch||'')?'selected':''}>${esc(x.label)} · ${packageSize(x.size)}</option>`).join('')}</select></label>${selected?`<a class="primary pc-download-button" href="${esc(packageSafeDownload(selected.url))}" download>${uiIcon('download')}下载 ${esc(selected.label)}</a><details class="pc-technical"><summary>查看文件校验值</summary><code>${esc(selected.sha256)}</code><button type="button" data-pc-action="copyHash">复制校验值</button></details>`:'<p class="danger">服务端尚未提供可下载安装包</p>'}<div class="pc-release-facts"><span>${uiIcon('shield')}SHA-256 完整性校验</span><span>${uiIcon('network')}自动识别设备架构</span><span>${uiIcon('check')}升级保留原配置</span></div>${saved>0?`<div class="pc-download-saving">按架构下载约减少 <strong>${saved}%</strong> 的传输量<small>以 x86_64 包与通用包对比；一键安装自动选择</small></div>`:''}</article><div class="pc-install-aside"><article class="pc-panel"><span class="pc-panel-icon mint">${uiIcon('terminal')}</span><h3>一键接入</h3><p>填写设备名称，选择安装模板，复制命令到路由器执行。后台会显示安装进度和连接结果。</p><button type="button" data-pc-action="connect">创建安装命令 ${uiIcon('arrow')}</button></article><article class="pc-panel"><span class="pc-panel-icon violet">${uiIcon('settings')}</span><h3>按你的习惯安装</h3><p>把服务器地址、远程管理和启动设置保存成模板，下一台设备直接复用。</p><button type="button" data-pc-action="profiles">管理安装模板 ${uiIcon('arrow')}</button></article><details class="pc-panel pc-build"><summary>服务端版本与历史安装包</summary><p>服务端 v${esc(build?.version||'—')} · 构建 ${esc(build?.commit==='development'?'本地开发构建':build?.commit?.slice(0,12)||'未知')}</p><p>构建时间：${packageDate(build?.builtAt==='unknown'?'':build?.builtAt)}</p>${build?.releaseURL?`<a href="${esc(build.releaseURL)}" target="_blank" rel="noopener">查看发布说明 ↗</a>`:''}<p>固定版本镜像</p><code>${esc(build?.image||'—')}</code><ul class="pc-catalog">${(data.catalog||[]).map(x=>`<li><strong>${esc(x.version)}</strong><span>${x.current?'当前提供':x.compatible?'可用于兼容回退':'需要兼容性检查'}</span></li>`).join('')}</ul><p class="muted">保留最近 5 个已校验历史包；回退需要设备支持新版更新工具。</p></details></div></div>`;
}
function packageProfilesHTML(data){return `<div class="pc-section-heading"><div><h3>安装模板</h3><p>模板只保存接入选项，设备 ID 和令牌在安装时单独生成。</p></div><button type="button" class="primary" data-pc-action="newProfile">${uiIcon('plus')}新建模板</button></div><div class="pc-profile-grid">${(data.profiles||[]).map(p=>`<article class="pc-profile"><div class="pc-profile-head"><span class="pc-panel-icon ${p.remoteAccess?'violet':'mint'}">${uiIcon('settings')}</span>${p.id==='standard'?'<span class="pc-tag">内置模板</span>':''}</div><h3>${esc(p.name)}</h3><p class="pc-profile-server">${esc(p.server||'使用安装时填写的服务器地址')}</p><ul><li>${uiIcon(p.remoteAccess?'check':'pause')}${p.remoteAccess?'启用 SSH / 文件管理':'关闭 SSH / 文件管理'}</li><li>${uiIcon('play')}${p.autoStart?'开机自动启动':'不随开机启动'}</li><li>${uiIcon('network')}${p.interfaceMode==='select'?'安装时选择系统网卡':'系统自动选择出口'}</li></ul><div class="pc-profile-actions"><button type="button" data-pc-action="${p.id==='standard'?'cloneProfile':'editProfile'}" data-id="${esc(p.id)}">${uiIcon('edit')}${p.id==='standard'?'复制并自定义':'编辑模板'}</button>${p.id!=='standard'?`<button type="button" data-pc-action="removeProfile" data-id="${esc(p.id)}" aria-label="删除模板 ${esc(p.name)}">${uiIcon('trash')}</button>`:''}</div></article>`).join('')}</div>`}
function packageDevicesHTML(data){
 const job=data.batch;
 return `${job?`<section class="pc-batch"><div class="pc-section-heading"><div><span class="pc-tag">${esc(packagePhaseNames[job.phase]||job.phase)}</span><h3>逐台更新到 ${esc(job.version)}</h3><p>已完成 ${job.items.filter(x=>x.phase==='succeeded').length} / ${job.items.length} 台；当前设备完成后再处理下一台。</p></div><div class="pc-inline-actions">${['queued','running'].includes(job.phase)?'<button type="button" data-pc-action="pauseBatch">暂停队列</button>':''}${job.phase==='paused'?'<button type="button" data-pc-action="resumeBatch">继续剩余设备</button>':''}${['queued','running','paused'].includes(job.phase)?'<button type="button" data-pc-action="cancelBatch">取消剩余任务</button>':''}</div></div><div class="pc-batch-items">${job.items.map(x=>`<div><strong>${esc(x.name)}</strong><span class="pc-phase ${esc(x.phase)}">${esc(packagePhaseNames[x.phase]||x.phase)}</span><small>${esc(x.message)}</small></div>`).join('')}</div></section>`:''}<div class="pc-device-toolbar"><input id="pcDeviceSearch" type="search" aria-label="搜索更新设备" placeholder="搜索设备名称或版本" value="${esc(packageDeviceQuery)}"><select id="pcDeviceFilter" aria-label="筛选更新设备">${[['all','全部设备'],['available','可更新'],['locked','已锁定'],['offline','离线设备']].map(([value,label])=>`<option value="${value}" ${packageDeviceFilter===value?'selected':''}>${label}</option>`).join('')}</select><button type="button" class="primary" id="pcStartBatch" data-pc-action="startBatch" ${packageSelection.size?'':'disabled'}>逐台更新（${packageSelection.size}）</button></div><label class="check pc-batch-option"><input id="pcStopOnFailure" type="checkbox" checked> 某台设备失败时暂停队列</label><div class="tablewrap"><table class="pc-device-table"><thead><tr><th><input type="checkbox" id="pcSelectAll" aria-label="选择当前可更新设备"></th><th>设备</th><th>插件版本</th><th>版本策略</th><th>操作</th></tr></thead><tbody id="pcDeviceRows"></tbody></table></div><p class="pc-footnote">每台设备更新时会短暂断开连接。离线、已锁定或无需更新的设备不会加入队列。</p>`;
}
function packageVisibleDevices(){return state.devices.filter(d=>[d.name,d.packageVersion,d.version].join(' ').toLowerCase().includes(packageDeviceQuery.toLowerCase())&&(packageDeviceFilter==='all'||packageDeviceFilter==='available'&&packageEligible(d)||packageDeviceFilter==='locked'&&packageCenterData?.policies?.[d.id]?.locked||packageDeviceFilter==='offline'&&!d.online))}
function renderPackageDeviceRows(){
 const list=packageVisibleDevices();for(const id of packageSelection)if(!state.devices.some(d=>d.id===id&&packageEligible(d)))packageSelection.delete(id);
 $('#pcDeviceRows').innerHTML=list.map(d=>{const locked=packageCenterData?.policies?.[d.id]?.locked,eligible=packageEligible(d);return `<tr><td><input type="checkbox" data-pc-select="${esc(d.id)}" aria-label="选择设备 ${esc(d.name)}" ${packageSelection.has(d.id)?'checked':''} ${eligible?'':'disabled'}></td><td><strong>${esc(d.name)}</strong><small><span class="mapping-dot ${d.online?'enabled':''}"></span>${d.online?'在线':'离线'} · ${esc(d.info?.arch||'架构未上报')}</small></td><td><strong>${esc(d.packageVersion||'尚未上报')}</strong><small>${eligible?'可更新到 '+esc(state.package?.manifest?.packageVersion):locked?'版本保持锁定':'已是当前版本或暂不可更新'}</small></td><td><button type="button" class="pc-lock ${locked?'locked':''}" data-pc-action="lock" data-id="${esc(d.id)}">${uiIcon('shield')}${locked?'已锁定 · 解锁':'跟随服务端 · 锁定'}</button></td><td><div class="pc-row-actions"><button type="button" data-pc-action="preflight" data-id="${esc(d.id)}" ${d.online&&d.canManageUpdate?'':'disabled'}>安装检查</button><button type="button" data-pc-action="update" data-id="${esc(d.id)}" ${locked?'disabled':''}>更新</button><button type="button" data-pc-action="rollback" data-id="${esc(d.id)}" ${d.online&&d.canManageUpdate&&!locked&&packageRollbackVersions(d).length?'':'disabled'}>回退</button></div></td></tr>`}).join('')||'<tr><td colspan="5" class="empty">没有匹配的设备</td></tr>';
 $('#pcDeviceRows').querySelectorAll('[data-pc-action]').forEach(b=>b.onclick=()=>packageCenterAction(b.dataset.pcAction,b.dataset.id,b).catch(e=>toast(e.message)));
 $('#pcDeviceRows').querySelectorAll('[data-pc-select]').forEach(c=>c.onchange=()=>{c.checked?packageSelection.add(c.dataset.pcSelect):packageSelection.delete(c.dataset.pcSelect);packageSelectionControls()});
 $('#pcSelectAll').onchange=e=>{for(const d of list.filter(packageEligible)){e.target.checked?packageSelection.add(d.id):packageSelection.delete(d.id)}renderPackageDeviceRows()};packageSelectionControls();
}
function packageSelectionControls(){const eligible=packageVisibleDevices().filter(packageEligible),selected=eligible.filter(d=>packageSelection.has(d.id));$('#pcSelectAll').checked=eligible.length>0&&selected.length===eligible.length;$('#pcSelectAll').indeterminate=selected.length>0&&selected.length<eligible.length;$('#pcStartBatch').disabled=!packageSelection.size;$('#pcStartBatch').textContent='逐台更新（'+packageSelection.size+'）'}
function packageRollbackVersions(d){return (packageCenterData?.catalog||[]).filter(x=>x.compatible&&packageCompare(x.version,d.packageVersion)<0)}
function packageHistoryHTML(data){return `<div class="pc-section-heading"><div><h3>每一次执行都有记录</h3><p>保留最近 200 条安装、升级与回退记录，关闭页面或重启服务后仍可查看。</p></div><button type="button" data-pc-action="reload">${uiIcon('refresh')}重新检查</button></div><div class="pc-history">${[...(data.history||[])].reverse().map(op=>`<article class="pc-history-item"><span class="pc-history-icon ${op.state.phase==='succeeded'?'mint':op.state.phase==='failed'?'amber':'violet'}">${uiIcon(op.state.phase==='succeeded'?'check':op.state.phase==='failed'?'info':'refresh')}</span><div class="pc-history-copy"><div><strong>${esc(op.deviceName)}</strong><span>${esc({install:'安装',update:'升级',rollback:'回退'}[op.kind]||op.kind)}</span><span class="pc-phase ${esc(op.state.phase)}">${esc(packagePhaseNames[op.state.phase]||op.state.phase)}</span></div><p>${esc(op.state.fromVersion||'首次安装')} → ${esc(op.state.toVersion||'—')}</p><small>${packageDate(op.state.startedAt)} · ${esc(op.state.message||'')}</small><details><summary>查看执行过程</summary><ol class="pc-timeline">${(op.events||[]).map(e=>`<li><span>${new Date(e.at).toLocaleTimeString()}</span><div><strong>${esc(packagePhaseNames[e.phase]||e.phase)}</strong><p>${esc(e.message)}</p></div></li>`).join('')}</ol></details></div><a class="pc-report-link" href="/api/admin/package-center/report?id=${encodeURIComponent(op.id)}" download aria-label="下载 ${esc(op.deviceName)} 的诊断报告">${uiIcon('download')}诊断报告</a></article>`).join('')||'<div class="pc-empty">'+uiIcon('history')+'<h3>还没有执行记录</h3><p>生成安装命令或更新设备后，结果会出现在这里。</p></div>'}</div>`}
async function packageCenterAction(action,id,button){
 if(action==='connect')return openEditor('device');
 if(action==='profiles'){packageCenterTab='profiles';renderPackages();return}
 if(action==='update')return openPluginUpdate(id);
 if(action==='reload')return loadPackageCenter();
 if(action==='newProfile')return openPackageProfile();
 if(action==='editProfile'||action==='cloneProfile')return openPackageProfile(packageCenterData.profiles.find(p=>p.id===id),action==='cloneProfile');
 if(action==='copyHash'){const item=packageCenterData.downloads.find(d=>d.arch===packageDownloadArch)||packageCenterData.downloads[0];return copyText(item.sha256,{button})}
 if(action==='preflight')return openPackagePreflight(id);
 if(action==='rollback')return openPackageRollback(id);
 if(action==='removeProfile'){
  const profile=packageCenterData.profiles.find(x=>x.id===id);if(!profile||!confirm('删除安装模板“'+profile.name+'”？已安装设备不受影响。'))return;
  await api('package-center/profile/remove','POST',{id});await loadPackageCenter();toast('模板已删除');return;
 }
 if(action==='lock'){button.disabled=true;try{await api('package-center/policy','PUT',{deviceId:id,locked:!packageCenterData.policies?.[id]?.locked});await loadPackageCenter()}finally{button.disabled=false}return}
 if(action==='startBatch'){
  if(!packageSelection.size||!confirm('按顺序更新选中的 '+packageSelection.size+' 台设备？每台设备会短暂断开连接。'))return;
  button.disabled=true;try{await api('package-center/batch','POST',{deviceIds:[...packageSelection],stopOnFailure:$('#pcStopOnFailure').checked});packageSelection.clear();await loadPackageCenter()}finally{button.disabled=false}return;
 }
 if(['pauseBatch','resumeBatch','cancelBatch'].includes(action)){await api('package-center/batch/control','POST',{id:packageCenterData.batch.id,action:{pauseBatch:'pause',resumeBatch:'resume',cancelBatch:'cancel'}[action]});await loadPackageCenter()}
}
function preparePackageDialog(type,title){closePluginUpdate();clearInterval(installPoll);$('#modal').classList.remove('setup-dialog');editing={type};$('#modalTitle').textContent=title;$('#formError').textContent='';$('#cancel').textContent='关闭';$('#cancel').onclick=()=>$('#modal').close();$('#save').type='submit';$('#save').onclick=null;$('#save').hidden=false;$('#save').disabled=false}
function openPackageProfile(profile=standardInstallProfile(),clone=false){
 preparePackageDialog('packageProfile',profile.id&&profile.id!=='standard'&&!clone?'编辑安装模板':'新建安装模板');editing.id=clone||profile.id==='standard'?'':profile.id||'';
 $('#fields').innerHTML=`<p class="muted">安装时复用这些设置，设备凭据单独生成。</p><label>模板名称<input name="profileName" required maxlength="40" value="${esc(clone?profile.name+'副本':profile.name)}" placeholder="例如 家庭路由器"></label><label>默认服务器地址<input name="profileServer" type="url" value="${esc(profile.server||'')}" placeholder="留空时由安装向导填写"></label><label>出口网卡<select name="profileInterface"><option value="auto">自动选择（推荐）</option><option value="select" ${profile.interfaceMode==='select'?'selected':''}>安装时从系统网卡列表选择</option></select></label><label class="check"><input name="profileRemote" type="checkbox" ${profile.remoteAccess?'checked':''}>启用 SSH / 文件管理</label><label class="check"><input name="profileAutoStart" type="checkbox" ${profile.autoStart?'checked':''}>开机自动启动客户端</label>`;
 $('#save').textContent='保存安装模板';$('#modal').showModal();
}
function standardInstallProfile(){return {id:'',name:'',server:'',remoteAccess:true,autoStart:true,interfaceMode:'auto'}}
function openPackageRollback(id){const d=state.devices.find(d=>d.id===id),versions=d?packageRollbackVersions(d):[];if(!versions.length)return;preparePackageDialog('packageRollback','兼容版本回退 · '+d.name);editing.deviceId=id;
 $('#fields').innerHTML=`<p>当前插件 <strong>${esc(d.packageVersion)}</strong></p><label>回退到<select name="rollbackVersion">${versions.map(x=>`<option value="${esc(x.version)}">${esc(x.version)}</option>`).join('')}</select></label><p class="muted">仅提供客户端版本相同的已校验历史包。执行时会短暂断开连接，设备 ID、令牌和连接配置会保留。</p>`;$('#save').textContent='回退并保留配置';$('#modal').showModal()}
async function openPackagePreflight(id){const d=state.devices.find(d=>d.id===id);if(!d)return;preparePackageDialog('packagePreflight','安装检查 · '+d.name);editing.deviceId=id;$('#save').hidden=true;$('#fields').innerHTML='<div class="pc-loading" role="status">正在读取设备的安装条件…</div>';$('#modal').showModal();const transition=authTransition;
 try{const result=await api('package-center/preflight','POST',{deviceId:id});if(editing?.type!=='packagePreflight'||editing.deviceId!==id||transition!==authTransition||!$('#modal').open)return;$('#fields').innerHTML=`<p class="pc-preflight-heading ${result.ok?'success':'danger'}">${uiIcon(result.ok?'check':'info')}${result.ok?'安装条件检查通过':'需要先处理以下项目'} · ${esc(result.architecture)}</p><ul class="pc-preflight">${result.checks.map(c=>`<li>${uiIcon(c.ok?'check':'info')}<div><strong>${esc(c.label)}</strong><p>${esc(c.detail)}</p></div><span>${c.ok?'通过':'需处理'}</span></li>`).join('')}</ul>`}catch(e){if(editing?.type==='packagePreflight'&&editing.deviceId===id&&transition===authTransition)$('#formError').textContent=e.message}
}
const packageCenterEditorSubmit=$('#editor').onsubmit;
$('#editor').onsubmit=async e=>{
 if(!['packageProfile','packageRollback','packagePreflight'].includes(editing?.type))return packageCenterEditorSubmit(e);
 e.preventDefault();if(editing.type==='packagePreflight')return;$('#save').disabled=true;const context=editing;
 try{const f=new FormData(e.target);
  if(context.type==='packageProfile')await api('package-center/profile','PUT',{id:context.id,name:f.get('profileName').trim(),server:f.get('profileServer').trim(),remoteAccess:f.has('profileRemote'),autoStart:f.has('profileAutoStart'),interfaceMode:f.get('profileInterface')});
  else await api('package-center/rollback','POST',{deviceId:context.deviceId,version:f.get('rollbackVersion')});
  if(editing===context){$('#modal').close();if(context.type==='packageRollback')packageCenterTab='history';await loadPackageCenter();toast(context.type==='packageProfile'?'安装模板已保存':'回退任务已发起，完成后自动重新连接')}
 }catch(x){if(editing===context)$('#formError').textContent=x.message}finally{if(editing===context)$('#save').disabled=false}
};

async function loadInstallProfiles(g){
 try{const data=await loadPackageCenter(false);if(guide!==g||g.step!==1||!$('#modal').open)return;g.profiles=data?.profiles||[];const select=$('#installProfile');if(select)select.innerHTML=g.profiles.map(p=>`<option value="${esc(p.id)}" ${p.id===g.profileId?'selected':''}>${esc(p.name)}</option>`).join('')}catch{}
}
function installProgressHTML(record){
 if(!record)return '';
 const stages=[['preflight','安装检查'],['downloading','下载'],['verifying','校验'],['installing','安装'],['configuring','应用模板'],['starting','启动'],['waiting','连接结果']];
 const rank=record.state.phase==='succeeded'?stages.length:stages.findIndex(([phase])=>phase===record.state.phase);
 return `<p class="${record.state.phase==='failed'?'danger':'muted'}" role="status">${esc(record.state.message)}</p><ol class="pc-install-progress">${stages.map(([phase,label],i)=>`<li class="${i<rank?'done':i===rank?'current':''}">${uiIcon(i<rank?'check':i===rank?'refresh':'activity')}${label}</li>`).join('')}</ol>`;
}
const packageOriginalRefresh=refresh;
refresh=()=>{const result=packageOriginalRefresh();if(page!=='packages')return result;const transition=authTransition;return result.then(async()=>{if(transition===authTransition&&page==='packages'){await loadPackageCenter(false);if(!document.querySelector('dialog[open]'))renderPackages()}})};
