// Device workspace: saved preferences, live tunnel diagnostics and mapping shortcuts.
const deviceRevealed=new Set(),deviceMapFilters=new Map(),deviceOperations=new Map();
const deviceIPRevealed=new Map();
const deviceHistories=new Map();
const deviceHistoryPages=new Map(),deviceListPaging={page:0,size:10,signature:''};
let deviceWorkspaceTransition=authTransition;
const previousDeviceAction=action,previousDeviceEditor=openEditor,previousDeviceSubmit=$('#editor').onsubmit;
function deviceTime(value){const time=new Date(value);return value&&time.getFullYear()>2000?time.toLocaleString():'尚无记录'}
function deviceDuration(value){const start=Date.parse(value);if(!Number.isFinite(start)||start<946684800000)return '—';const seconds=Math.max(0,Math.floor((Date.now()-start)/1000));return seconds>=86400?Math.floor(seconds/86400)+' 天 '+Math.floor(seconds%86400/3600)+' 小时':seconds>=3600?Math.floor(seconds/3600)+' 小时 '+Math.floor(seconds%3600/60)+' 分钟':seconds>=60?Math.floor(seconds/60)+' 分钟':seconds+' 秒'}
function deviceMasked(value){return value.length>6?value.slice(0,3)+'••••'+value.slice(-3):'••••••'}
function deviceMaskedIP(value){const parts=value.split('.');if(parts.length===4&&parts.every(p=>/^\d{1,3}$/.test(p)&&Number(p)<=255))return parts.slice(0,2).join('.')+'.*.*';if(value.includes(':'))return value.split(':').filter(Boolean).slice(0,2).join(':')+':••••';return '••••'}
function deviceOperatorText(d){
 const ip=d.info?.peerIP,network=d.network;
 if(!ip)return '未上报';
 if(!network||network.ip!==ip)return '未知';
 if(['loopback','private','shared','reserved'].includes(network.kind))return '不适用';
 if(network.status==='known'&&network.operator)return network.operator;
 if(network.status==='pending')return '识别中…';
 if(network.status==='disabled')return '未启用';
 return '未知';
}
function deviceNetworkMetadata(d){
 const ip=d.info?.peerIP||'',network=d.network,shown=deviceIPRevealed.get(d.id)===ip;
 const kind=network?.ip===ip?network.kind:'unknown',label=({public:'外网 IP',private:'内网 IP',loopback:'本机 IP',shared:'共享 IP',reserved:'保留 IP'})[kind]||'连接 IP';
 const operator=deviceOperatorText(d),details=network?.status==='known'&&network.ip===ip?`${network.organization||operator} · ${(network.asns||[]).map(asn=>'AS'+asn).join(' / ')} · ${network.source||''} · ${deviceTime(network.checkedAt)}`:operator==='不适用'?'本机、内网、共享和保留地址不进行公网运营商查询。':operator==='未知'?'暂未取得所属网络信息，网络恢复后会自动重试。':'按当前连接来源的公网 IP 识别所属网络；代理或 VPN 出口可能不同于宽带运营商。';
 return `<span class="device-ip-meta"><span>${d.online?'':'上次'}${label}：</span><code>${ip?esc(shown?ip:deviceMaskedIP(ip)):'未上报'}</code>${ip?`<button type="button" data-action="revealIP" data-id="${esc(d.id)}" title="${shown?'隐藏 IP':'显示完整 IP'}" aria-label="${shown?'隐藏 IP':'显示完整 IP'}" aria-pressed="${shown}">${uiIcon('eye')}</button><button type="button" data-copy="${esc(ip)}" title="复制完整 IP" aria-label="复制完整 IP">${uiIcon('copy')}</button>`:''}</span><span class="device-operator ${network?.status==='known'&&network.ip===ip?'identified':''}" title="${esc(details)}">运营商：${esc(operator)}</span>`;
}
function deviceLatencyText(ms){return ms===0?'<1 ms':ms+' ms'}
function trafficBytes(value){
 const n=Number.isFinite(value)&&value>0?value:0,units=['B','KiB','MiB','GiB','TiB','PiB','EiB'];
 let size=n,unit=0;while(size>=1024&&unit<units.length-1){size/=1024;unit++}
 return (unit?size.toFixed(size>=100?1:2):Math.floor(size))+' '+units[unit];
}
function trafficCounter(scope,id){
 const c=state.traffic?.[scope]?.[id]||{};
 const upload=Number.isFinite(c.uploadBytes)&&c.uploadBytes>0?c.uploadBytes:0,download=Number.isFinite(c.downloadBytes)&&c.downloadBytes>0?c.downloadBytes:0;
 return {upload,download,total:upload+download};
}
function mappingTrafficHTML(id){
 const c=trafficCounter('mappings',id);
 return `<div class="mapping-traffic" title="↑ 发往内网 · ↓ 从内网接收；统计网页请求与响应正文"><b>${trafficBytes(c.total)}</b><small><span>↑ ${trafficBytes(c.upload)}</span><span>↓ ${trafficBytes(c.download)}</span></small></div>`;
}
function deviceTrafficSummary(d){
 const c=trafficCounter('devices',d.id);
 return `<div class="device-traffic-summary" aria-label="设备累计流量"><span>累计流量 <b>${trafficBytes(c.total)}</b></span><span title="上传至设备内网">↑ 上传 <b>${trafficBytes(c.upload)}</b></span><span title="从设备内网下载">↓ 下载 <b>${trafficBytes(c.download)}</b></span></div>`;
}
function trafficHint(device=false){return `<p class="traffic-hint">${device?'设备汇总包含网页、SSH 和文件传输；网页统计正文，SSH / 文件统计通道数据。':'映射统计网页请求与响应正文。'}↑ 上传至内网，↓ 从内网下载。自 ${esc(deviceTime(state.traffic?.since))} 起累计，之前的流量不回填。</p>`}
function deviceLatency(d){return d.online&&Number.isFinite(d.latencyMs)&&Date.now()-Date.parse(d.latencyAt)<90000?deviceLatencyText(d.latencyMs):'待检测'}
function deviceInfoCell(label,value){return `<div><dt>${esc(label)}</dt><dd>${esc(value||'尚未上报')}</dd></div>`}
function deviceButton(actionName,id,label,icon,extra=''){return `<button type="button" data-action="${actionName}" data-id="${esc(id)}" ${extra}>${uiIcon(icon)}${esc(label)}</button>`}
function devicePageWindow(total,paging){
 const pages=Math.max(1,Math.ceil(total/paging.size));
 paging.page=Math.max(0,Math.min(pages-1,paging.page));
 return {pages,start:paging.page*paging.size,end:Math.min(total,(paging.page+1)*paging.size)};
}
function deviceHistoryPaging(id){if(!deviceHistoryPages.has(id))deviceHistoryPages.set(id,{page:0,size:10});return deviceHistoryPages.get(id)}
function devicePageSlots(pages,page){
 if(pages<=7)return Array.from({length:pages},(_,i)=>i);
 if(page<=3)return [0,1,2,3,4,'gap',pages-1];
 if(page>=pages-4)return [0,'gap',pages-5,pages-4,pages-3,pages-2,pages-1];
 return [0,'gap',page-1,page,page+1,'gap',pages-1];
}
function devicePagerHTML(scope,id,total,paging,position='bottom'){
 const {pages,start,end}=devicePageWindow(total,paging),unit=scope==='devices'?'台设备':'条记录',sizeUnit=scope==='devices'?'台':'条',sizes=scope==='devices'?[5,10,20,50]:[10,20,50];
 const attrs=`data-paging-scope="${scope}" data-paging-id="${esc(id)}" data-paging-position="${position}"`;
 const button=(page,label,extra='')=>`<button type="button" ${attrs} data-paging-page="${page}" ${extra}>${label}</button>`;
 const arrow=back=>`<svg class="ui-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${back?'m14 7-5 5 5 5':'m10 7 5 5-5 5'}"/></svg>`;
 const slots=devicePageSlots(pages,paging.page),label=`每页${scope==='devices'?'设备':'记录'}数量`;
 return `<div role="navigation" class="device-pagination ${pages===1?'device-pagination-single':''}" aria-label="${scope==='devices'?'设备列表':'上下线记录'}分页"><span class="device-page-total" role="status">共 ${total} ${unit}${total?`<span class="device-page-range"> · 当前 ${start+1}–${end}</span>`:''}</span><div class="device-page-controls"><details class="device-page-size"><summary ${attrs} data-paging-size aria-label="${label}" aria-haspopup="listbox" aria-expanded="false"><span>${paging.size} ${sizeUnit} / 页</span><svg class="ui-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m7 10 5 5 5-5"/></svg></summary><div class="device-page-size-menu" role="listbox" aria-label="${label}选项">${sizes.map(size=>`<button type="button" role="option" ${attrs} data-paging-size="${size}" data-paging-size-option="${size}" aria-selected="${size===paging.size}"><span>${size} ${sizeUnit} / 页</span><span class="device-page-size-check" aria-hidden="true">${size===paging.size?'✓':''}</span></button>`).join('')}</div></details><div class="device-page-buttons">${button(paging.page-1,arrow(true),`class="device-page-arrow" aria-label="上一页" title="上一页" ${paging.page===0?'disabled':''}`)}<div class="device-page-numbers">${slots.map(n=>n==='gap'?'<span class="device-page-gap" aria-hidden="true">…</span>':button(n,n+1,`class="device-page-number" aria-label="第 ${n+1} 页" ${n===paging.page?'aria-current="page"':''}`)).join('')}</div><span class="device-page-mobile" aria-hidden="true">${paging.page+1} / ${pages}</span>${button(paging.page+1,arrow(false),`class="device-page-arrow" aria-label="下一页" title="下一页" ${paging.page===pages-1?'disabled':''}`)}</div><span class="device-page-count">第 ${paging.page+1} / ${pages} 页</span></div></div>`;
}
function bindDevicePagination(container){
 container.querySelectorAll('[data-paging-page]').forEach(b=>b.onclick=()=>changeDevicePage(b.dataset.pagingScope,b.dataset.pagingId,b.dataset.pagingPage));
 container.querySelectorAll('.device-page-size').forEach(menu=>{
  const trigger=menu.querySelector('summary'),options=[...menu.querySelectorAll('[role="option"]')];
  const close=()=>{menu.open=false;trigger.focus({preventScroll:true})};
  menu.ontoggle=()=>{trigger.setAttribute('aria-expanded',String(menu.open));if(menu.open)container.querySelectorAll('.device-page-size').forEach(other=>{if(other!==menu)other.open=false})};
  menu.onfocusout=e=>{if(!menu.contains(e.relatedTarget))menu.open=false};
  trigger.onkeydown=e=>{if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();menu.open=true;(options.find(n=>n.getAttribute('aria-selected')==='true')||options[0])?.focus()}else if(e.key==='Escape'){e.preventDefault();close()}};
  options.forEach((option,i)=>{
   option.onclick=()=>{close();changeDevicePage(option.dataset.pagingScope,option.dataset.pagingId,0,option.dataset.pagingSizeOption)};
   option.onkeydown=e=>{let next;if(e.key==='ArrowDown')next=(i+1)%options.length;else if(e.key==='ArrowUp')next=(i+options.length-1)%options.length;else if(e.key==='Home')next=0;else if(e.key==='End')next=options.length-1;else if(e.key==='Escape'){e.preventDefault();close();return}if(next!==undefined){e.preventDefault();options[next].focus()}};
  });
 });
}
// Dismiss custom page-size menus without changing the selected size.
document.addEventListener?.('click',e=>document.querySelectorAll('.device-page-size[open]').forEach(menu=>{if(!menu.contains(e.target))menu.open=false}));
function devicePagingFocus(){
 const d=document.activeElement?.dataset;
 return d?.pagingScope?{scope:d.pagingScope,id:d.pagingId,position:d.pagingPosition,page:d.pagingPage,size:d.pagingSize!==undefined,option:d.pagingSizeOption,open:Boolean(document.activeElement.closest?.('.device-page-size')?.open)}:null;
}
function restoreDevicePagingFocus(focus){
 if(!focus)return;
 const controls=[...$('#deviceCards').querySelectorAll('[data-paging-scope]')].filter(n=>n.dataset.pagingScope===focus.scope&&n.dataset.pagingId===focus.id&&n.dataset.pagingPosition===focus.position);
 const control=controls.find(n=>!n.disabled&&(focus.size?n.dataset.pagingSize!==undefined&&n.dataset.pagingSizeOption===(focus.open?focus.option:undefined):n.dataset.pagingPage===focus.page))||controls.find(n=>n.getAttribute('aria-current')==='page');
 if(focus.open&&focus.size){const menu=control?.closest('.device-page-size');if(menu)menu.open=true}
 control?.focus({preventScroll:true});
}
function changeDevicePage(scope,id,page,size){
 if(scope!=='devices'&&(scope!=='history'||!state.devices.some(d=>d.id===id)))return;
 const paging=scope==='devices'?deviceListPaging:deviceHistoryPaging(id),sizes=scope==='devices'?[5,10,20,50]:[10,20,50];
 if(size!==undefined){if(!sizes.includes(Number(size)))return;paging.size=Number(size);paging.page=0}
 else{if(!Number.isInteger(Number(page))||Number(page)<0)return;paging.page=Number(page)}
 deviceCards();
 if(scope==='devices')$('#deviceCards').scrollIntoView?.({block:'start',behavior:'instant'});
}
function deviceSessionDuration(event){
 if(event.state!=='offline'||event.reason==='server_restart')return '—';
 const ms=Date.parse(event.at)-Date.parse(event.connectedAt);if(!Number.isFinite(ms)||ms<0)return '—';
 const seconds=Math.floor(ms/1000),days=Math.floor(seconds/86400),hours=Math.floor(seconds%86400/3600),minutes=Math.floor(seconds%3600/60);
 return days?`${days} 天 ${hours} 小时`:hours?`${hours} 小时 ${minutes} 分钟`:minutes?`${minutes} 分钟 ${seconds%60} 秒`:`${seconds} 秒`;
}
function deviceHistoryPanel(d){
 const history=deviceHistories.get(d.id),events=history?.events||[],reason={connected:'隧道连接建立',replaced:'新连接替换了原连接',disconnected:'隧道连接断开',reconnect:'管理员请求重新连接',server_stop:'服务器正常停止',server_restart:'服务器重启时发现原连接已失效'};
 const paging=deviceHistoryPaging(d.id),{start,end}=devicePageWindow(events.length,paging);
 return `<section class="device-history-panel"><div class="connection-panel-heading"><div><h2>${uiIcon('history')}上下线记录</h2><p class="muted">最多保留最近 ${esc(history?.limit||100)} 条，最新记录在前。</p></div>${deviceButton('refreshHistory',d.id,history?.loading?'读取中…':'刷新记录','refresh',history?.loading?'disabled aria-busy="true"':'')}</div>${history?.error?`<p class="device-operation error" role="status">${esc(history.error)}</p>`:''}<div class="tablewrap"><table class="device-history-table"><thead><tr><th>状态</th><th>时间</th><th>本次在线时长</th><th>说明</th></tr></thead><tbody>${events.slice(start,end).map(event=>`<tr><td><span class="pill ${event.state==='online'?'on':''}">${event.state==='online'?'上线':'下线'}</span></td><td>${esc(deviceTime(event.at))}</td><td>${esc(deviceSessionDuration(event))}</td><td>${esc(reason[event.reason]||'连接状态变化')}</td></tr>`).join('')||`<tr><td colspan="4" class="empty">${history?.loading?'正在读取记录…':history?.error?'记录读取失败，请重试。':'暂无上下线记录，后续连接变化会自动记录。'}</td></tr>`}</tbody></table></div>${events.length?devicePagerHTML('history',d.id,events.length,paging):''}<p class="traffic-hint">记录随后台数据保存，重启后保留。时间以服务器检测为准；服务器异常停止期间的实际断开时间未知，之前未记录的历史不补填。</p>${history?.checkedAt?`<p class="traffic-hint">最近读取：${esc(deviceTime(history.checkedAt))}</p>`:''}</section>`;
}
async function loadDeviceHistory(id){
 if(deviceHistories.get(id)?.loading||!state.devices.some(d=>d.id===id))return;
 const transition=authTransition,previous=deviceHistories.get(id)||{};
 deviceHistories.set(id,{...previous,loading:true,error:''});deviceCards();
 const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),12000);
 try{
  const result=await api('devices/'+encodeURIComponent(id)+'/events','GET',undefined,{signal:controller.signal});
  if(transition!==authTransition||!state.devices.some(d=>d.id===id))return;
  deviceHistories.set(id,{events:result.events||[],limit:result.limit,checkedAt:result.checkedAt,checkedUnix:Date.now()});
 }catch(e){if(transition===authTransition&&state.devices.some(d=>d.id===id))deviceHistories.set(id,{...previous,checkedUnix:Date.now(),error:e.name==='AbortError'?'读取记录超时，请重试。':e.message})}
 finally{clearTimeout(timer);controller.abort();if(transition===authTransition)deviceCards()}
}
function deviceEgressText(d){
 const info=d.info||{},mode=info.interface?'指定 '+info.interface:'自动选择';
 if(!info.os&&!info.egressInterface)return '尚未上报';
 return mode+' · '+(info.egressInterface?(d.online?'实际：':'上次实际：')+info.egressInterface:'实际网卡未上报');
}
function deviceConnectionPanel(d){
 const info=d.info||{},operation=deviceOperations.get(d.id);
 return `<section class="device-connection-panel"><div class="connection-panel-heading"><div><h2>${uiIcon('network')}连接详情</h2><p class="muted">在线表示隧道已连接；内网服务和公网入口需要分别检测。</p></div><span class="pill ${d.online?'on':''}">${d.online?'隧道已连接':'隧道未连接'}</span></div><dl class="device-facts">${deviceInfoCell('连接来源 IP',info.peerIP||'尚无记录')}${deviceInfoCell('网络运营商',deviceOperatorText(d))}${deviceInfoCell('接入服务器',location.host)}${deviceInfoCell('系统 / 架构',[info.os,info.arch].filter(Boolean).join(' / '))}${deviceInfoCell('设备主机名',info.hostname)}${deviceInfoCell('出口网卡',deviceEgressText(d))}${deviceInfoCell('插件 / 客户端',d.packageVersion||d.version)}${deviceInfoCell('最近连接',deviceTime(d.lastConnectedAt))}${deviceInfoCell('最近断开',deviceTime(d.lastDisconnectedAt))}${deviceInfoCell('本次在线时长',d.online?deviceDuration(d.connectedAt):'—')}${deviceInfoCell('最近收到响应',d.online?deviceTime(d.lastActiveAt):'—')}${deviceInfoCell('隧道往返延迟',deviceLatency(d))}${deviceInfoCell('延迟检测时间',d.online?deviceTime(d.latencyAt):'—')}</dl>${!info.os?'<p class="device-legacy-hint">'+uiIcon('info')+'旧版插件可检测连接和重新连接；升级插件后可显示系统、架构与出口网卡。</p>':''}<div class="device-connection-actions">${deviceButton('health',d.id,'检测连接','activity',!d.online||operation?.busy?'disabled':'')}${deviceButton('reconnect',d.id,'重新连接','refresh',!d.online||operation?.busy?'disabled':'')}<span class="muted">重新连接会短暂中断访问，客户端随后自动重连。</span></div>${trafficHint(true)}${operation?`<p class="device-operation ${operation.error?'error':''}" role="status">${esc(operation.text)}</p>`:''}</section>`;
}
function deviceMappingMatches(m,id){const f=deviceMapFilters.get(id)||{};return (!f.status||(m.enabled?'enabled':'paused')===f.status)&&[m.host,m.target,m.note||''].join(' ').toLowerCase().includes((f.query||'').toLowerCase())}
function deviceMappingRow(m,d){
 // Retain existing URL formatting, copy, probe, enable and delete behavior.
 const row=mappingRow(m,d,mappingTrafficHTML(m.id)),button=deviceButton('duplicate',m.id,'','copy','title="复制新增映射" aria-label="复制新增映射"');
 return row.replace('<button class="danger" data-action="deleteMapping"',button+'<button class="danger" data-action="deleteMapping"');
}
function deviceMappingRows(d){const mappings=state.mappings.filter(m=>m.deviceId===d.id),matched=mappings.filter(m=>deviceMappingMatches(m,d.id));return matched.map(m=>deviceMappingRow(m,d)).join('')||`<tr><td colspan="5" class="empty">${mappings.length?'没有符合条件的映射，试试修改搜索或状态。':'还没有映射，添加一个域名即可接入内网 Web 服务。'}</td></tr>`}
function deviceMappingsPanel(d){
 const mappings=state.mappings.filter(m=>m.deviceId===d.id),enabled=mappings.filter(m=>m.enabled).length,f=deviceMapFilters.get(d.id)||{};
 return `<div class="toolbar"><div><h2>域名映射 <small class="muted">已启用 ${enabled} / ${mappings.length}</small></h2></div><button class="primary" data-action="addMapping" data-id="${esc(d.id)}">${uiIcon('plus')}添加映射</button></div><div class="device-mapping-tools"><label class="device-mapping-search">${uiIcon('search')}<input data-mapping-search="${esc(d.id)}" aria-label="搜索此设备的映射" placeholder="搜索域名、内网地址或备注" value="${esc(f.query||'')}"></label><select data-mapping-status="${esc(d.id)}" aria-label="筛选映射状态"><option value="" ${!f.status?'selected':''}>全部映射</option><option value="enabled" ${f.status==='enabled'?'selected':''}>已启用</option><option value="paused" ${f.status==='paused'?'selected':''}>已停用</option></select></div><div class="tablewrap"><table class="mapping-table"><thead><tr><th>公网访问地址</th><th>内网地址</th><th>备注与状态</th><th>使用流量</th><th>操作</th></tr></thead><tbody id="device-mapping-rows-${esc(d.id)}">${deviceMappingRows(d)}</tbody></table></div>${trafficHint()}`;
}
deviceCards=()=>{
 const container=$('#deviceCards');if(!container)return;
 const pagingFocus=devicePagingFocus();
 if(deviceWorkspaceTransition!==authTransition){deviceRevealed.clear();deviceIPRevealed.clear();deviceMapFilters.clear();expandedFeatures.clear();deviceOperations.clear();deviceHistories.clear();deviceHistoryPages.clear();Object.assign(deviceListPaging,{page:0,size:10,signature:''});deviceWorkspaceTransition=authTransition}
 const signature=JSON.stringify([query.toLowerCase(),filter]);
 if(deviceListPaging.signature!==signature){deviceListPaging.page=0;deviceListPaging.signature=signature}
 const matched=state.devices.filter(d=>{
  const maps=state.mappings.filter(m=>m.deviceId===d.id);
  return (!filter||filter==='pinned'&&d.pinned||filter==='yes'&&d.online||filter==='no'&&!d.online)&&[d.name,d.id,d.note,d.info?.hostname,d.info?.peerIP,d.network?.operator,JSON.stringify(maps)].join(' ').toLowerCase().includes(query.toLowerCase());
 }).sort((a,b)=>Number(Boolean(b.pinned))-Number(Boolean(a.pinned))||a.name.localeCompare(b.name));
 const {start,end,pages}=devicePageWindow(matched.length,deviceListPaging),visible=matched.slice(start,end);
 container.innerHTML=devicePagerHTML('devices','',matched.length,deviceListPaging,'top')+(visible.map(d=>{
  const mappings=state.mappings.filter(m=>m.deviceId===d.id),operation=deviceOperations.get(d.id);
  const active=expandedFeatures.get(d.id),revealed=deviceRevealed.has(d.id);
  return `<article class="device-card device-workspace-card ${d.pinned?'device-pinned':''}"><div class="device-head"><div class="device-icon" aria-hidden="true">${uiIcon('monitor')}</div><div class="device-info"><div class="device-title"><strong>${esc(d.name)}</strong><span class="pill ${d.online?'on':''}">${d.online?'在线':'离线'}</span>${d.pinned?'<span class="device-pin-label">'+uiIcon('pin')+'置顶</span>':''}</div><div class="device-meta"><span class="device-id-meta">ID：<code>${esc(revealed?d.id:deviceMasked(d.id))}</code><button type="button" data-action="revealDevice" data-id="${esc(d.id)}" title="${revealed?'隐藏设备 ID':'显示设备 ID'}" aria-label="${revealed?'隐藏设备 ID':'显示设备 ID'}" aria-pressed="${revealed}">${uiIcon('eye')}</button><button type="button" data-copy="${esc(d.id)}" title="复制设备 ID" aria-label="复制设备 ID">${uiIcon('copy')}</button></span>${deviceNetworkMetadata(d)}<span>插件：${esc(d.packageVersion||d.version||'尚未上报')}</span><span>${d.online?'本次在线 '+esc(deviceDuration(d.connectedAt)):'最近连接 '+esc(deviceTime(d.lastConnectedAt))}</span></div>${deviceTrafficSummary(d)}${d.note?`<p class="device-saved-note">${esc(d.note)}</p>`:''}</div><div class="actions device-header-actions">${deviceButton('pinDevice',d.id,'','pin',`title="${d.pinned?'取消置顶':'置顶设备'}" aria-label="${d.pinned?'取消置顶':'置顶设备'}" aria-pressed="${Boolean(d.pinned)}"`)}${deviceButton('refreshDevice',d.id,operation?.kind==='refresh'&&operation.busy?'刷新中…':'刷新状态','refresh',`title="刷新此设备的连接状态" ${operation?.busy?'disabled':''} ${operation?.kind==='refresh'&&operation.busy?'aria-busy="true"':''}`)}${pluginUpdateButton(d)}${deviceButton('rename',d.id,'编辑','edit')}</div></div><div class="device-tabs"><button type="button" class="feature-tab" data-feature="domains" data-device="${esc(d.id)}" aria-expanded="${active==='domains'}" aria-controls="domains-${esc(d.id)}">${uiIcon('globe')}访问域名（${mappings.length}）<i aria-hidden="true"></i></button><button type="button" class="feature-tab" data-feature="connection" data-device="${esc(d.id)}" aria-expanded="${active==='connection'}" aria-controls="connection-${esc(d.id)}">${uiIcon('activity')}连接详情<i aria-hidden="true"></i></button><button type="button" class="feature-tab" data-feature="history" data-device="${esc(d.id)}" aria-expanded="${active==='history'}" aria-controls="history-${esc(d.id)}">${uiIcon('history')}上下线记录<i aria-hidden="true"></i></button><span class="device-tab-summary">${d.online?'隧道延迟 '+esc(deviceLatency(d)):'等待设备连接'}</span></div><div class="device-body" id="domains-${esc(d.id)}" ${active==='domains'?'':'hidden'}>${deviceMappingsPanel(d)}</div><div class="device-body" id="connection-${esc(d.id)}" ${active==='connection'?'':'hidden'}>${deviceConnectionPanel(d)}</div><div class="device-body" id="history-${esc(d.id)}" ${active==='history'?'':'hidden'}>${deviceHistoryPanel(d)}</div>${operation&&active!=='connection'?`<p class="device-operation device-operation-footer ${operation.error?'error':''}" role="status">${esc(operation.text)}</p>`:''}<details class="device-manage"><summary>${uiIcon('settings')}设备管理</summary><div>${deviceButton('rename',d.id,'编辑名称与备注','edit')}${deviceButton('reconnect',d.id,'重新连接','refresh',!d.online||operation?.busy?'disabled':'')}${deviceButton('deleteDevice',d.id,'删除设备','trash','class="danger"')}</div></details></article>`;
 }).join('')||'<div class="workbench-empty"><h2>暂无匹配设备</h2><p class="muted">可搜索设备名称、备注、设备 ID、IP 或映射地址。</p></div>')+(pages>1?devicePagerHTML('devices','',matched.length,deviceListPaging):'');
 if(typeof remoteDeviceTabs==='function')container.querySelectorAll('[data-feature="connection"]').forEach(button=>{const d=state.devices.find(x=>x.id===button.dataset.device);if(d)button.insertAdjacentHTML('afterend',remoteDeviceTabs(d))});
 container.querySelectorAll('[data-feature]').forEach(b=>b.onclick=()=>toggleFeature(b.dataset.device,b.dataset.feature));
 bindDeviceActions(container);
 bindDevicePagination(container);
 restoreDevicePagingFocus(pagingFocus);
 container.querySelectorAll('[data-mapping-search]').forEach(input=>input.oninput=()=>updateDeviceMappingFilter(input.dataset.mappingSearch,{query:input.value}));
 container.querySelectorAll('[data-mapping-status]').forEach(input=>input.onchange=()=>updateDeviceMappingFilter(input.dataset.mappingStatus,{status:input.value}));
 for(const d of visible){const h=deviceHistories.get(d.id);if(expandedFeatures.get(d.id)==='history'&&(!h||!h.loading&&Date.now()-(h.checkedUnix||0)>15000))void loadDeviceHistory(d.id)}
};
function bindDeviceActions(container){
 container.querySelectorAll('[data-action]').forEach(b=>b.onclick=()=>action(b.dataset.action,b.dataset.id).catch(e=>toast(e.message)));
 container.querySelectorAll('[data-copy]').forEach(b=>b.onclick=()=>copyText(b.dataset.copy,{button:b,source:b.parentElement.querySelector('a,code')}));
}
function updateDeviceMappingFilter(id,patch){
 deviceMapFilters.set(id,{...deviceMapFilters.get(id),...patch});const d=state.devices.find(x=>x.id===id),body=document.getElementById('device-mapping-rows-'+id);
 if(d&&body){body.innerHTML=deviceMappingRows(d);bindDeviceActions(body)}
}
const previousDeviceWorkbench=renderWorkbench;
renderWorkbench=()=>{
 const pagingFocus=devicePagingFocus(),focused=document.activeElement,searchDevice=focused?.dataset?.mappingSearch,selection=searchDevice?[focused.selectionStart,focused.selectionEnd]:null;
 previousDeviceWorkbench();restoreDevicePagingFocus(pagingFocus);const select=$('#filter');if(select&&!select.querySelector('option[value="pinned"]')){const option=document.createElement('option');option.value='pinned';option.textContent='置顶设备';select.appendChild(option);select.value=filter}const search=$('#search');if(search){search.placeholder='搜索设备、备注、IP 或映射';search.setAttribute('aria-label',search.placeholder)}
 if(searchDevice){const input=[...$('#deviceCards').querySelectorAll('[data-mapping-search]')].find(x=>x.dataset.mappingSearch===searchDevice);if(input){input.focus();input.setSelectionRange(...selection)}}
};
action=async(a,id)=>{
 const d=state.devices.find(x=>x.id===id),m=state.mappings.find(x=>x.id===id);
 if(a==='revealIP'&&d?.info?.peerIP){if(deviceIPRevealed.get(id)===d.info.peerIP)deviceIPRevealed.delete(id);else deviceIPRevealed.set(id,d.info.peerIP);deviceCards();return}
 if(a==='revealDevice'){if(deviceRevealed.has(id))deviceRevealed.delete(id);else deviceRevealed.add(id);deviceCards();return}
 if(a==='duplicate'&&m){const {id:oldID,host,...draft}=m;const domain=[...(state.domains||[])].sort((a,b)=>b.baseDomain.length-a.baseDomain.length).find(d=>host===d.baseDomain||host.endsWith('.'+d.baseDomain));openEditor('mapping',{...draft,host:'',_mappingDraft:{host:'',domainSuffix:domain?.baseDomain||customMappingDomain}});$('#modalTitle').textContent='复制新增映射';return}
 if(a==='pinDevice'&&d){await api('devices/'+encodeURIComponent(id),'PUT',{pinned:!d.pinned});await refresh();toast(d.pinned?'已取消置顶':'设备已置顶');return}
 if(a==='refreshHistory'&&d){await loadDeviceHistory(id);return}
 if(a==='refreshDevice'&&d){
  if(deviceOperations.get(id)?.busy)return;
  const transition=authTransition;deviceOperations.set(id,{busy:true,kind:'refresh',text:'正在刷新此设备的状态…'});deviceCards();
  const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),12000);
  try{
   const result=await api('devices/'+encodeURIComponent(id)+'/status','POST',{}, {signal:controller.signal});
   if(transition!==authTransition)return;
   const index=state.devices.findIndex(x=>x.id===id);if(index<0)return;
   if(!result.device||result.device.id!==id)throw Error('设备状态返回异常，请重试。');
   state.devices[index]=result.device;
   if(state.traffic&&result.traffic){state.traffic.devices||={};state.traffic.devices[id]=result.traffic}
   const message=!result.device.online?'设备离线':result.probe==='ok'?'设备在线 · 隧道响应正常':result.probe==='changed'?'设备已重新连接，等待下一次响应检测':'隧道仍连接，但本次响应检测未通过，请稍后重试';
   deviceOperations.set(id,{error:result.probe==='unresponsive',text:message+' · '+deviceTime(result.checkedAt)});
   if(expandedFeatures.get(id)==='history')await loadDeviceHistory(id);
  }catch(e){if(transition===authTransition)deviceOperations.set(id,{error:true,text:e.name==='AbortError'?'刷新超时，请稍后重试。':e.message})}
  finally{clearTimeout(timer);controller.abort();if(transition===authTransition)render()}
  return;
 }
 if((a==='health'||a==='reconnect')&&d){
  if(!d.online||deviceOperations.get(id)?.busy)return;
  if(a==='reconnect'&&!confirm('确认重新连接“'+d.name+'”？访问会短暂中断，设备 ID、令牌和映射配置会保留。'))return;
  const transition=authTransition;deviceOperations.set(id,{busy:true,text:a==='health'?'正在检测隧道连接…':'正在请求重新连接…'});deviceCards();
  const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),12000);
  try{
   const result=await api('devices/'+encodeURIComponent(id)+'/'+a,'POST',{}, {signal:controller.signal});
   if(transition!==authTransition)return;
   deviceOperations.set(id,{text:a==='health'?'隧道响应正常 · 往返 '+deviceLatencyText(result.latencyMs)+' · '+deviceTime(result.checkedAt):'已请求重新连接，等待客户端恢复；可点击右上角刷新查看。'});
   if(a==='health'){const current=state.devices.find(x=>x.id===id);if(current){current.latencyMs=result.latencyMs;current.latencyAt=result.checkedAt;current.lastActiveAt=result.checkedAt}}
   if(a==='reconnect')await refresh();
  }catch(e){if(transition===authTransition)deviceOperations.set(id,{error:true,text:e.name==='AbortError'?'连接检测超时，请稍后重试。':e.message})}
  finally{clearTimeout(timer);controller.abort();if(transition===authTransition)deviceCards()}
  return;
 }
 return previousDeviceAction(a,id);
};
openEditor=(type,item={})=>{
 previousDeviceEditor(type,item);
 if(type==='device'&&item.id){$('#modalTitle').textContent='编辑设备';$('#fields').innerHTML=`<label>设备名称<input name="name" required maxlength="100" value="${esc(item.name)}"></label><label>设备备注<textarea name="note" maxlength="200" rows="3" placeholder="例如：家中主路由器、办公室 NAS">${esc(item.note||'')}</textarea></label><label class="check"><input name="pinned" type="checkbox" ${item.pinned?'checked':''}> 置顶这台设备</label><p class="muted">修改名称或备注会保留设备连接凭据与全部映射。</p>`}
};
$('#editor').onsubmit=async e=>{
 if(editing?.type!=='device'||!editing.item.id)return previousDeviceSubmit(e);
 e.preventDefault();const context=editing,transition=authTransition;$('#save').disabled=true;
 try{const f=new FormData(e.target);await api('devices/'+encodeURIComponent(context.item.id),'PUT',{name:f.get('name'),note:f.get('note'),pinned:f.has('pinned')});if(editing!==context||transition!==authTransition)return;$('#modal').close();await refresh();toast('设备已保存')}
 catch(e){if(editing===context&&transition===authTransition)$('#formError').textContent=e.message}
 finally{if(editing===context&&transition===authTransition)$('#save').disabled=false}
};
