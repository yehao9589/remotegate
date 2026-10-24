// Operational data belongs to the current administrator session. Never render a
// response that arrived after logout, navigation or a newer filter selection.
let operationsGeneration=0,operationsOwner=-1,operationsScope='devices',operationsID='',operationsPeriod='day',operationsAlertFilter='active',operationsAlertPage=0;
let operationAlerts=[],operationTrend=null,operationAlertsGeneration=0;
function resetOperationsOwner(){if(operationsOwner===authTransition)return;operationsOwner=authTransition;operationAlerts=[];operationTrend=null;operationsID='';operationsScope='devices';operationsPeriod='day';operationsAlertPage=0}
function updateNotificationBadge(){
 const button=document.querySelector('nav [data-page="operations"]');if(!button)return;
 button.querySelector('.operations-unread')?.remove();
 const count=operationAlerts.filter(x=>!x.read).length;
 if(count)button.insertAdjacentHTML('beforeend',`<span class="operations-unread" aria-label="${count} 条未读提醒">${count>99?'99+':count}</span>`);
}
async function loadOperationAlerts(){
 resetOperationsOwner();const owner=authTransition,generation=++operationAlertsGeneration;
 const result=await api('notifications');if(owner!==authTransition||generation!==operationAlertsGeneration)return;
 const next=result.items||[],changed=JSON.stringify(next)!==JSON.stringify(operationAlerts);operationAlerts=next;updateNotificationBadge();if(changed&&page==='operations')drawOperationAlerts();
}
function operationEntityName(id){return operationsScope==='devices'?state.devices.find(d=>d.id===id)?.name||id:state.mappings.find(m=>m.id===id)?.host||id}
function renderOperations(){
 resetOperationsOwner();++operationsGeneration;
 $('#content').classList.add('operations-center');$('#pageDescription').textContent='集中查看流量变化与异常提醒，在设备卡片中一键诊断。';
 const entities=operationsScope==='devices'?state.devices:state.mappings;
 if(operationsID&&!entities.some(x=>x.id===operationsID))operationsID='';
 $('#content').innerHTML=`<section class="op-traffic"><div class="op-heading"><div><h2>${uiIcon('activity')}流量趋势</h2><p>按天或按月查看实际传输量，快速找出用量较高的设备与映射。</p></div><button id="opReload">${uiIcon('refresh')}刷新数据</button></div><div class="op-filters"><label>统计对象<select id="opScope"><option value="devices">设备</option><option value="mappings">映射</option></select></label><label class="op-entity">选择范围<select id="opEntity"><option value="">全部${operationsScope==='devices'?'设备':'映射'}</option>${entities.map(x=>`<option value="${esc(x.id)}">${esc(operationEntityName(x.id))}</option>`).join('')}</select></label><div class="op-period" role="group" aria-label="统计周期"><button data-op-period="day" aria-pressed="${operationsPeriod==='day'}">近 30 天</button><button data-op-period="month" aria-pressed="${operationsPeriod==='month'}">近 12 个月</button></div></div><div id="opTrend" aria-live="polite"><p class="muted">正在读取流量…</p></div></section><section class="op-alerts"><div class="op-heading"><div><h2>${uiIcon('shield')}异常提醒 <span id="opAlertCount"></span></h2><p>持续离线超过 3 分钟、证书 30 天内到期、插件更新失败。后台自动记录。</p></div><button id="opReadAll">全部标为已读</button></div><div class="op-alert-filters" role="group" aria-label="提醒筛选">${[['active','待处理'],['unread','未读'],['all','全部记录']].map(([key,text])=>`<button data-op-alert-filter="${key}" aria-pressed="${operationsAlertFilter===key}">${text}</button>`).join('')}</div><div id="opAlertList" aria-live="polite"></div><p class="op-footnote">站内提醒最多保存 200 条，重启后保留。恢复连接或条件解除后自动归档；不会发送邮件或外部消息。</p></section>`;
 $('#opScope').value=operationsScope;$('#opEntity').value=operationsID;
 $('#opScope').onchange=e=>{operationsScope=e.target.value;operationsID='';renderOperations()};
 $('#opEntity').onchange=e=>{operationsID=e.target.value;void loadOperationTrend()};
 document.querySelectorAll('[data-op-period]').forEach(b=>b.onclick=()=>{operationsPeriod=b.dataset.opPeriod;document.querySelectorAll('[data-op-period]').forEach(x=>x.setAttribute('aria-pressed',String(x===b)));void loadOperationTrend()});
 document.querySelectorAll('[data-op-alert-filter]').forEach(b=>b.onclick=()=>{operationsAlertFilter=b.dataset.opAlertFilter;operationsAlertPage=0;document.querySelectorAll('[data-op-alert-filter]').forEach(x=>x.setAttribute('aria-pressed',String(x===b)));drawOperationAlerts()});
 $('#opReadAll').onclick=async()=>{const owner=authTransition,button=$('#opReadAll');button.disabled=true;try{await api('notifications','POST',{});if(owner===authTransition)await loadOperationAlerts()}catch(e){toast(e.message)}finally{button.disabled=false}};
 $('#opReload').onclick=()=>{void loadOperationTrend();void loadOperationAlerts().catch(e=>toast(e.message))};
 drawOperationAlerts();void loadOperationTrend();void loadOperationAlerts().catch(e=>{if(page==='operations')$('#opAlertList').textContent=e.message});
}
async function loadOperationTrend(){
 const generation=++operationsGeneration,owner=authTransition,controller=new AbortController(),timer=setTimeout(()=>controller.abort(),15000);
 if($('#opTrend'))$('#opTrend').innerHTML='<p class="muted">正在读取流量…</p>';
 try{const data=await api('traffic/trends?'+new URLSearchParams({scope:operationsScope,id:operationsID,period:operationsPeriod}),'GET',undefined,{signal:controller.signal});if(owner!==authTransition||page!=='operations'||generation!==operationsGeneration)return;operationTrend=data;$('#opTrend').innerHTML=operationTrendHTML(data)}
 catch(e){if(owner===authTransition&&page==='operations'&&generation===operationsGeneration)$('#opTrend').textContent=e.name==='AbortError'?'读取超时，请刷新重试':e.message}
 finally{clearTimeout(timer)}
}
function operationTrendHTML(data){
 const points=data.points||[],upload=points.reduce((n,p)=>n+Number(p.uploadBytes||0),0),download=points.reduce((n,p)=>n+Number(p.downloadBytes||0),0),max=Math.max(1,...points.map(p=>Number(p.uploadBytes||0)+Number(p.downloadBytes||0)));
 const chart=points.map((p,i)=>{const total=Number(p.uploadBytes||0)+Number(p.downloadBytes||0),height=total/max*150,u=total?height*Number(p.uploadBytes||0)/total:0,x=30+i*(700/points.length),width=Math.max(3,700/points.length-6);return `<g><title>${esc(p.date)} · ${p.observed?'↑ '+trafficBytes(p.uploadBytes)+' / ↓ '+trafficBytes(p.downloadBytes):'尚未开始统计'}</title><rect x="${x}" y="${175-height}" width="${width}" height="${height-u}" rx="2" class="op-download"/><rect x="${x}" y="${175-u}" width="${width}" height="${u}" rx="2" class="op-upload"/>${i===0||i===points.length-1||i%Math.ceil(points.length/6)===0?`<text x="${x+width/2}" y="202" text-anchor="middle">${esc(data.period==='month'?p.date.slice(2):p.date.slice(5))}</text>`:''}</g>`}).join('');
 return `<div class="op-totals"><div><span>所选期间合计</span><strong>${trafficBytes(upload+download)}</strong></div><div><span><i class="op-key upload"></i>↑ 上传至内网</span><strong>${trafficBytes(upload)}</strong></div><div><span><i class="op-key download"></i>↓ 从内网下载</span><strong>${trafficBytes(download)}</strong></div></div><div class="op-chart"><svg viewBox="0 0 770 215" role="img" aria-label="${data.period==='month'?'每月':'每日'}上传与下载流量，所选期间总计 ${trafficBytes(upload+download)}"><path d="M25 25H740M25 100H740M25 175H740" class="op-grid"/>${chart}</svg>${upload+download===0?'<span class="op-chart-empty">所选期间暂无已记录流量</span>':''}</div><p class="op-footnote">按 UTC 划分日期，保留最近 366 天。自 ${esc(deviceTime(data.since))} 开始记录趋势，较早日期未统计，当天及首月可能不完整。设备汇总包含网页、SSH 与文件通道；映射仅统计网页正文，两者不可相加。</p><details class="op-data-table"><summary>查看每日 / 每月明细</summary><div class="tablewrap"><table><thead><tr><th>日期（UTC）</th><th>上传</th><th>下载</th></tr></thead><tbody>${points.map(p=>`<tr><td>${esc(p.date)}</td><td>${p.observed?trafficBytes(p.uploadBytes):'未统计'}</td><td>${p.observed?trafficBytes(p.downloadBytes):'未统计'}</td></tr>`).join('')}</tbody></table></div></details><div class="op-ranking"><h3>${operationsScope==='devices'?'设备':'映射'}用量排名 <small>所选期间 · 前 10 名</small></h3>${(data.ranking||[]).map((r,i)=>`<div><span class="op-rank-index">${i+1}</span><span class="op-rank-name">${esc(operationEntityName(r.id))}</span><span>↑ ${trafficBytes(r.uploadBytes)}　↓ ${trafficBytes(r.downloadBytes)}</span><b>${trafficBytes(Number(r.uploadBytes)+Number(r.downloadBytes))}</b></div>`).join('')||'<p class="muted">有实际传输后会显示排名。</p>'}</div>`;
}
function drawOperationAlerts(){
 if(page!=='operations'||!$('#opAlertList'))return;
 const list=operationAlerts.filter(a=>operationsAlertFilter==='unread'?!a.read:operationsAlertFilter==='active'?!a.resolvedAt:true),pages=Math.max(1,Math.ceil(list.length/10));operationsAlertPage=Math.min(operationsAlertPage,pages-1);
 $('#opAlertCount').textContent=operationAlerts.filter(a=>!a.resolvedAt).length+' 项待处理';
 $('#opAlertList').innerHTML=list.slice(operationsAlertPage*10,operationsAlertPage*10+10).map(a=>`<article class="op-alert ${a.resolvedAt?'resolved':''} ${a.read?'':'unread'}"><span class="op-alert-icon">${uiIcon(a.kind==='offline'?'monitor':a.kind==='update'?'package':'shield')}</span><div><h3>${esc(a.title)} <small>${a.resolvedAt?'已恢复 / 条件已解除':'待处理'}${a.read?'':' · 未读'}</small></h3><p>${esc(a.detail)}</p><time>${esc(deviceTime(a.createdAt))}${a.resolvedAt?' · 解除于 '+esc(deviceTime(a.resolvedAt)):''}</time></div>${a.deviceId&&state.devices.some(d=>d.id===a.deviceId)?`<button data-op-device="${esc(a.deviceId)}">查看设备</button>`:a.kind==='certificate'?'<button data-op-domains>查看证书</button>':''}</article>`).join('')||'<div class="op-empty">'+(operationsAlertFilter==='active'?'当前没有待处理提醒':'暂无符合条件的提醒')+'</div>';
 if(pages>1)$('#opAlertList').insertAdjacentHTML('beforeend',`<div class="op-alert-pager"><span>共 ${list.length} 条 · ${operationsAlertPage+1} / ${pages} 页</span><button data-op-alert-page="-1" ${operationsAlertPage===0?'disabled':''}>上一页</button><button data-op-alert-page="1" ${operationsAlertPage===pages-1?'disabled':''}>下一页</button></div>`);
 document.querySelectorAll('[data-op-alert-page]').forEach(b=>b.onclick=()=>{operationsAlertPage+=Number(b.dataset.opAlertPage);drawOperationAlerts()});
 document.querySelectorAll('[data-op-device]').forEach(b=>b.onclick=()=>{page='overview';query=b.dataset.opDevice;filter='';render()});
 document.querySelectorAll('[data-op-domains]').forEach(b=>b.onclick=()=>{page='domains';render()});
}

function openDiagnostic(deviceId,mappingId=''){
 let dialog=$('#diagnosticDialog');if(!dialog){document.body.insertAdjacentHTML('beforeend','<dialog id="diagnosticDialog" class="op-diagnostic"></dialog>');dialog=$('#diagnosticDialog')}
 const device=state.devices.find(d=>d.id===deviceId),maps=state.mappings.filter(m=>m.deviceId===deviceId);if(!device)return;
 const owner=authTransition;let controller=null,run=0;
 dialog.innerHTML=`<div class="op-heading"><div><h2>一键诊断 · ${esc(device.name)}</h2><p>分别检查设备隧道、公网 DNS、HTTPS 和内网服务。</p></div><button id="diagnosticClose" aria-label="关闭诊断">${uiIcon('close')}</button></div><label>检测映射<select id="diagnosticMapping"><option value="">仅检测设备隧道</option>${maps.map(m=>`<option value="${esc(m.id)}">${esc(m.host)}${m.enabled?'':'（已停用）'}</option>`).join('')}</select></label><div id="diagnosticResult" aria-live="polite"><p class="muted">点击开始诊断，检查不会重启设备或修改配置。</p></div><div class="dialogactions"><button class="primary" id="diagnosticStart">开始诊断</button></div><p class="op-footnote">DNS / HTTPS 从服务端检测公网入口；内网服务通过现有隧道检测。业务登录和网页完整加载仍需打开访问地址确认。</p>`;
 $('#diagnosticMapping').value=mappingId;$('#diagnosticClose').onclick=()=>dialog.close();dialog.onclose=()=>{controller?.abort();++run};
 $('#diagnosticStart').onclick=async()=>{
  controller?.abort();controller=new AbortController();const current=++run,button=$('#diagnosticStart'),select=$('#diagnosticMapping'),timer=setTimeout(()=>controller.abort(),25000);button.disabled=true;select.disabled=true;button.textContent='诊断中…';$('#diagnosticResult').innerHTML='<p class="muted">正在分层检测，通常需要几秒钟…</p>';
  try{const result=await api('diagnose','POST',{deviceId,mappingId:select.value},{signal:controller.signal});if(owner!==authTransition||current!==run||!dialog.open)return;$('#diagnosticResult').innerHTML=(result.steps||[]).map(s=>`<article class="op-diagnostic-step ${esc(s.status)}"><span>${s.status==='ok'?'✓':s.status==='skipped'?'—':'!'}</span><div><h3>${esc(s.label)} <small>${({ok:'正常',warn:'需确认',error:'异常',skipped:'未检测'})[s.status]||'未知'}</small></h3><p>${esc(s.detail)}</p></div></article>`).join('')+(!result.mappingEnabled?'<p class="op-warning">此映射已停用，公网访问仍会被拒绝；以上结果只用于排查配置。</p>':'')+'<p class="op-footnote">检测时间：'+esc(deviceTime(result.checkedAt))+'</p>'}
  catch(e){if(owner===authTransition&&current===run&&dialog.open)$('#diagnosticResult').textContent=e.name==='AbortError'?'诊断超时，请重试':e.message}
  finally{clearTimeout(timer);if(current===run){button.disabled=false;select.disabled=false;button.textContent='重新诊断'}}
 };if(!dialog.open)dialog.showModal();
}
const actionBeforeOperations=action;
action=async(name,id)=>{if(name==='diagnoseDevice')return openDiagnostic(id);if(name==='diagnoseMapping'){const mapping=state.mappings.find(m=>m.id===id);if(mapping)return openDiagnostic(mapping.deviceId,id);return}return actionBeforeOperations(name,id)};
const refreshBeforeOperations=refresh;
refresh=function(){const operation=refreshBeforeOperations();operation.then(()=>loadOperationAlerts()).catch(()=>{});return operation};
setInterval(()=>{
 if($('#shell').hidden)return;
 void loadOperationAlerts().catch(()=>{});
 if(page==='domains'&&!document.querySelector('dialog[open]')&&!document.activeElement?.closest('.dc-panel input')){
  const owner=authTransition;void api('domains').then(domains=>{if(owner!==authTransition||page!=='domains'||JSON.stringify(domains||[])===JSON.stringify(state.domains||[]))return;state.domains=domains||[];state.domain=domains?.[0]||{};drawDomainList()}).catch(()=>{});
 }
},30000);
