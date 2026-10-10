// Public observations are always scoped to the concrete host and port checked.
const domainPanels=new Map(),domainChecks=new Map(),domainCheckInputs=new Map();
let domainSearch='',domainFilter='',domainGeneration=0,domainListPage=0;
const domainIcons={dns:'dns',certificate:'shield',history:'history'};
function domainMappings(d){return state.mappings.filter(m=>m.host===d.baseDomain||m.host.endsWith('.'+d.baseDomain))}
function certificateCoversHost(names,host){return (names||[]).some(name=>name===host||(name.startsWith('*.')&&host.endsWith(name.slice(1))&&host.split('.').length===name.split('.').length))}
function rootExternallyManaged(d){return d.rootHTTPSProvider!=='remotegate'}
function publicEndpointObservation(d,host,port){
 const checks=d.httpsChecks||[],root=d.publicHTTPS;
 return checks.find(x=>x.host===host&&Number(x.port)===Number(port))||(root?.host===host&&Number(root.port)===Number(port)?root:null);
}
function publicEndpointHealth(d,host,port,now=Date.now()){
 const r=publicEndpointObservation(d,host,port),endpoint=host+':'+port;
 if(!r)return {key:'attention',text:'待检测',tone:'quiet',detail:endpoint};
 const checked=Date.parse(r.checkedAt),expires=Date.parse(r.expiresAt),days=Math.floor((expires-now)/86400000);
 if(!Number.isFinite(checked)||checked>now+60000||now-checked>24*3600000)return {key:'attention',text:'待复检',tone:'quiet',detail:'上次检测 '+certDate(r.checkedAt),result:r};
 if(r.valid&&Number.isFinite(expires)&&expires>now)return {key:days<=30?'attention':'valid',text:days<=30?'证书即将到期':'HTTPS 已验证',tone:days<=30?'warn':'green',detail:'剩余 '+days+' 天 · '+(r.issuer||'可信证书'),result:r};
 return {key:'attention',text:r.valid?'证书已过期':'HTTPS 异常',tone:'warn',detail:r.error||endpoint,result:r};
}
function rootPublicHealth(d,now=Date.now()){
 const h=publicEndpointHealth(d,d.baseDomain,Number(d.rootHTTPSPort)||443,now);
 return {...h,text:({ '待检测':'公网待检测','待复检':'公网待复检','HTTPS 已验证':'公网 HTTPS 已验证','证书已过期':'公网证书已过期','HTTPS 异常':'公网 HTTPS 异常' })[h.text]||h.text};
}
function applyPublicHTTPSCheck(d,r){
 for(const row of r.dns||[]){
  if(!row.certificate&&!row.error)continue;
  const check={...(row.certificate||{}),host:row.host,port:row.port,addresses:row.addresses||[],checkedAt:r.checkedAt,...(row.error?{valid:false,error:row.error}:{})};
  d.httpsChecks=[...(d.httpsChecks||[]).filter(x=>x.host!==check.host||Number(x.port)!==Number(check.port)),check].slice(-40);
  if(check.host===d.baseDomain&&Number(check.port)===(Number(d.rootHTTPSPort)||443))d.publicHTTPS=check;
 }
}
function publicCertificateDetails(r){
 if(!r)return '<p class="dc-help">点击检测，读取这个地址当前实际返回的证书。</p>';
 return `<div class="dc-deployment"><p><b>最近检测</b>${certDate(r.checkedAt)}</p>${r.issuer?`<p><b>颁发机构</b>${esc(r.issuer)}</p>`:''}${r.expiresAt&&Date.parse(r.expiresAt)>0?`<p><b>到期时间</b>${certDate(r.expiresAt)}</p>`:''}${r.names?.length?`<p><b>证书覆盖域名</b>${r.names.map(n=>`<code>${esc(n)}</code>`).join(' ')}</p>`:''}${r.error?`<p class="dc-error">${esc(r.error)}</p>`:''}</div>`;
}
function publicHTTPSPanel(d){
 const h=rootPublicHealth(d);
 return `<div class="dc-public-https"><div class="dc-panel-heading"><div><h4>主域名 · 公网 HTTPS <span class="dc-badge ${h.tone}">${h.text}</span></h4><p>宝塔 / 反向代理管理证书与续期 · ${esc(d.baseDomain)}:${Number(d.rootHTTPSPort)||443}</p></div><button class="primary" data-dc-root-check>检测主域名 HTTPS</button></div>${publicCertificateDetails(h.result)}<p class="dc-help">结果只代表这个域名和端口。泛域名证书不会自动证明所有映射入口都正确。</p></div>`;
}
function renderDomainCenter(){
 ++domainGeneration;
 $('#content').classList.add('domain-center');
 $('#content').innerHTML=`<div class="dc-overview" id="domainOverview"></div><div class="dc-tools"><div class="dc-search"><span>${uiIcon('search')}</span><input id="domainSearch" placeholder="搜索域名或显示名称" aria-label="搜索域名或显示名称" value="${esc(domainSearch)}"></div><select id="domainFilter" aria-label="HTTPS 检测状态筛选"><option value="">全部域名</option><option value="valid">公网 HTTPS 已验证</option><option value="attention">待检测 / 需处理</option></select><button class="primary" id="addDomain">${uiIcon('plus')}添加域名</button></div><div id="domainList" class="dc-list"></div><details class="dc-guide"><summary>第一次接入域名？查看 3 个步骤 <span>解析 → 宝塔证书 → 检测</span></summary><div><p><b>01 · 指向服务器</b> 在 DNS 平台添加 A / AAAA 记录，把主域名和需要的子域名指向服务器。</p><p><b>02 · 在宝塔启用证书</b> 给 RemoteGate 站点绑定主域名及泛域名，使用 DNS 验证申请证书，保存并启用；续期也由宝塔负责。</p><p><b>03 · 验证实际入口</b> 映射反代保留原始 Host，指向 RemoteGate。分别检测主域名和具体映射地址。</p></div></details>`;
 $('#domainFilter').value=domainFilter;
 $('#domainSearch').oninput=e=>{domainSearch=e.target.value;domainListPage=0;drawDomainList()};
 $('#domainFilter').onchange=e=>{domainFilter=e.target.value;domainListPage=0;drawDomainList()};
 $('#addDomain').onclick=()=>openDomainEditor();
 drawDomainList();
}
function drawDomainList(){
 if(page!=='domains'||!$('#domainList'))return;
 const domains=state.domains||[],matches=domains.filter(d=>(d.baseDomain+' '+d.name).toLowerCase().includes(domainSearch.toLowerCase())&&(!domainFilter||rootPublicHealth(d).key===domainFilter));
 const totalPages=Math.max(1,Math.ceil(matches.length/10));domainListPage=Math.min(domainListPage,totalPages-1);const visible=matches.slice(domainListPage*10,domainListPage*10+10);
 const valid=domains.filter(d=>rootPublicHealth(d).key==='valid').length,attention=domains.length-valid;
 $('#domainOverview').innerHTML=`<div><span>域名总数</span><strong>${domains.length}</strong></div><div><span><i class="dc-dot green"></i>主域名 HTTPS 已验证</span><strong>${valid}</strong></div><div><span><i class="dc-dot warn"></i>待检测 / 需处理</span><strong>${attention}</strong></div><p>查看实际公网证书<br><small>申请、部署和续期在宝塔完成</small></p>`;
 $('#domainList').innerHTML=visible.map(domainCenterRow).join('')||`<div class="dc-empty"><div>${uiIcon('globe')}</div><h3>${domains.length?'没有匹配的域名':'添加你的第一个域名'}</h3><p>${domains.length?'调整搜索或筛选条件。':'例如 example.com，随后为 router.example.com 添加映射并检测 HTTPS。'}</p>${domains.length?'':'<button class="primary" data-dc-empty>＋ 添加域名</button>'}</div>`;
 if(matches.length>10)$('#domainList').insertAdjacentHTML('beforeend',`<div class="dc-pagination"><span>共 ${matches.length} 个域名 · 第 ${domainListPage+1} / ${totalPages} 页</span><button data-dc-page="prev" ${domainListPage===0?'disabled':''}>上一页</button><button data-dc-page="next" ${domainListPage===totalPages-1?'disabled':''}>下一页</button></div>`);
 $('#domainList').querySelectorAll('[data-dc-page]').forEach(b=>b.onclick=()=>{domainListPage+=b.dataset.dcPage==='prev'?-1:1;drawDomainList()});
 $('#domainList').querySelector('[data-dc-empty]')?.addEventListener('click',()=>openDomainEditor());
 $('#domainList').querySelectorAll('[data-domain-panel]').forEach(b=>b.onclick=()=>{const id=b.dataset.domain,tab=b.dataset.domainPanel;domainPanels.set(id,domainPanels.get(id)===tab?'':tab);drawDomainList()});
 $('#domainList').querySelectorAll('[data-domain-edit]').forEach(b=>b.onclick=()=>openDomainEditor(domains.find(d=>d.id===b.dataset.domainEdit)));
 visible.forEach(d=>bindDomainPanel(d));
}
function domainCenterRow(d){
 const h=rootPublicHealth(d),tab=domainPanels.get(d.id)||'',m=domainMappings(d);
 return `<article class="dc-domain"><div class="dc-row"><div class="dc-domain-mark" aria-hidden="true">${uiIcon('globe')}</div><div class="dc-domain-name"><h3>${esc(d.baseDomain)}</h3><span class="dc-domain-meta"><span>DNS：${esc((providerInfo[d.dnsProvider]||providerInfo.manual).name)}</span><span>显示名称：${esc(d.name||d.baseDomain)}</span></span></div><div class="dc-status"><span class="dc-badge ${h.tone}"><i></i>${h.text}</span><small>${esc(h.detail)}</small></div><div class="dc-mapping-count"><strong>${m.length}</strong><span>个映射</span></div><div class="dc-row-actions">${['dns','certificate','history'].map(t=>`<button data-domain="${esc(d.id)}" data-domain-panel="${t}" class="${tab===t?'selected':''}" aria-expanded="${tab===t}" title="${t==='dns'?'解析接入':t==='certificate'?'HTTPS 检测':'检测记录'}">${uiIcon(domainIcons[t])} <span>${t==='dns'?'解析接入':t==='certificate'?'HTTPS 检测':'记录'}</span></button>`).join('')}<button class="dc-edit" data-domain-edit="${esc(d.id)}" title="编辑域名" aria-label="编辑域名">${uiIcon('edit')} <span>编辑</span></button></div></div>${tab?`<section class="dc-panel" id="domainPanel-${esc(d.id)}">${tab==='dns'?domainDNSPanel(d):tab==='history'?domainHistoryPanel(d):domainCertificatePanel(d)}</section>`:''}</article>`;
}
function domainDNSPanel(d){
 const m=domainMappings(d),check=domainChecks.get(d.id)||(d.publicHTTPS?{saved:true,checkedAt:d.publicHTTPS.checkedAt,dns:[{host:d.publicHTTPS.host,port:d.publicHTTPS.port,certificate:d.publicHTTPS}]}:null),draft=domainCheckInputs.get(d.id),type=d.serverIP?.includes(':')?'AAAA':'A';
 return `<div class="dc-panel-heading"><div><h4>域名解析与公网入口</h4><p>在 DNS 平台把访问域名指向服务器，证书由宝塔 / 反向代理管理。</p></div><span class="dc-badge quiet">${esc((providerInfo[d.dnsProvider]||providerInfo.manual).name)}</span></div><div class="dc-dns-grid"><div><div class="dc-label">建议添加的解析 <small>在 DNS 平台操作</small></div><table class="dc-records"><thead><tr><th>主机记录</th><th>类型</th><th>记录值</th><th></th></tr></thead><tbody>${['@','*'].map(host=>`<tr><td><code>${host}</code><small>${host==='@'?'主域名':'一级子域名'}</small></td><td>${type}</td><td><code>${esc(d.serverIP||'请先填写公网 IP')}</code></td><td>${d.serverIP?`<button data-dc-copy="${esc(d.serverIP)}" title="复制记录值">${uiIcon('copy')}</button>`:''}</td></tr>`).join('')}</tbody></table><p class="dc-help">* 解析覆盖 router.${esc(d.baseDomain)}。具体子域名已有记录时，以其记录为准。Cloudflare 若开启代理，解析 IP 可能与服务器不同。</p></div><div class="dc-check-form"><div class="dc-label">检测实际访问入口</div><label>访问域名<input data-check-host value="${esc(draft?.host??d.baseDomain)}" list="checkHosts-${esc(d.id)}" placeholder="router.${esc(d.baseDomain)}"><datalist id="checkHosts-${esc(d.id)}">${m.map(x=>`<option value="${esc(x.host)}"></option>`).join('')}</datalist></label><label>公网 HTTPS 端口<input data-check-port type="number" min="1" max="65535" value="${esc(draft?.port??(d.rootHTTPSPort||443))}"></label><button class="primary" data-dc-check>检测解析与 HTTPS</button><p class="dc-help">读取 DNS、服务器 IP 与实际返回的证书；可填写具体映射的公网端口。</p></div></div>${check?domainCheckResult(check):''}<details class="dc-advanced"><summary>域名管理</summary><p>已有映射时，需要先删除这些映射才能移除主域名。</p><button class="danger" data-dc-delete>删除此域名配置</button></details>`;
}
function domainCheckResult(r){
 if(r.error)return `<div class="dc-check-result warn" role="alert">${esc(r.error)}</div>`;
 return `<div class="dc-check-result" role="status"><div class="dc-label">检测结果 <small>${certDate(r.checkedAt)}${r.saved?' · 上次检测记录，重新检测可更新':''}</small></div>${(r.dns||[]).map(x=>`<div><b>${esc(x.host)}:${Number(x.port)||443}</b><span>${esc((x.addresses||[]).join('、'))}</span>${r.saved?'':`<span class="dc-badge ${x.error?'warn':x.matches?'green':'quiet'}">${x.error?'未解析':!r.serverIP?'未设置参考 IP':x.matches?'IP 匹配':'IP 不同'}</span>`}${x.certificate?`<p class="${x.certificate.valid?'dc-success':'dc-error'}">${x.certificate.valid?'HTTPS 验证通过 · '+esc(x.certificate.issuer)+' · 剩余 '+Math.floor((Date.parse(x.certificate.expiresAt)-Date.now())/86400000)+' 天':'HTTPS 未通过：'+esc(x.certificate.error)}</p>`:''}</div>`).join('')}</div>`;
}
function domainCertificatePanel(d){
 const mappings=domainMappings(d);
 return publicHTTPSPanel(d)+`<div class="dc-panel-heading"><div><h4>映射域名 · HTTPS 检测</h4><p>直接读取每个映射入口的证书，核对信任链、域名与有效期。</p></div></div><div class="tablewrap"><table class="https-deploy-table"><thead><tr><th>访问地址</th><th>实际证书状态</th><th>操作</th></tr></thead><tbody>${mappings.map(m=>{const access=mappingPublicAccess(m),https=access.scheme==='https',h=https?publicEndpointHealth(d,m.host,access.port):{text:'使用 HTTP',tone:'quiet',detail:'此映射未使用 HTTPS'},device=(state.devices||[]).find(x=>x.id===m.deviceId);return `<tr><td><b>${esc(publicMappingURL(m))}</b><small>${!m.enabled?'映射已停用':device?.online?'设备在线':'设备离线'}</small></td><td><span class="dc-badge ${h.tone}">${h.text}</span><small>${esc(h.detail)}</small></td><td><button data-dc-mapping-check="${esc(m.id)}" ${https?'':'disabled'}>检测 HTTPS</button></td></tr>`}).join('')||'<tr><td colspan="3">暂无映射。添加后可逐条检测。</td></tr>'}</tbody></table></div>${domainChecks.has(d.id)?domainCheckResult(domainChecks.get(d.id)):''}<p class="dc-help">HTTPS 验证通过只代表公网证书正常，设备在线与内网目标可用需在设备工作台另外确认。</p><details class="dc-guide"><summary>宝塔泛域名证书怎么申请？</summary><div>${baotaCertificateGuide(d)}</div></details>`;
}
function domainHistoryPanel(d){
 const checks=[...(d.httpsChecks||[])].sort((a,b)=>Date.parse(b.checkedAt)-Date.parse(a.checkedAt));
 if(d.publicHTTPS&&!checks.some(x=>x.host===d.publicHTTPS.host&&x.port===d.publicHTTPS.port))checks.push(d.publicHTTPS);
 return `<div class="dc-panel-heading"><div><h4>HTTPS 检测记录</h4><p>保留最近 40 个地址的最新结果；超过 24 小时提示复检。</p></div></div>${checks.map(r=>{const h=publicEndpointHealth(d,r.host,r.port);return `<div class="dc-check-result"><b>${esc(r.host)}:${r.port}</b> <span class="dc-badge ${h.tone}">${h.text}</span>${publicCertificateDetails(r)}</div>`}).join('')||'<p class="dc-help">暂无检测记录。点击主域名或映射旁的检测按钮。</p>'}`;
}
function bindDomainPanel(d){
 const node=document.getElementById('domainPanel-'+d.id);if(!node)return;
 node.querySelectorAll('[data-dc-copy]').forEach(b=>b.onclick=()=>copyText(b.dataset.dcCopy,{button:b,source:b.closest('tr').querySelector('td:nth-child(3) code'),success:'记录值已复制'}));
 node.querySelectorAll('[data-check-host],[data-check-port]').forEach(input=>input.oninput=()=>domainCheckInputs.set(d.id,{host:node.querySelector('[data-check-host]').value,port:node.querySelector('[data-check-port]').value}));
 node.querySelector('[data-dc-check]')?.addEventListener('click',e=>runDomainCheck(d,node.querySelector('[data-check-host]').value.trim(),node.querySelector('[data-check-port]').value,e.currentTarget));
 node.querySelector('[data-dc-root-check]')?.addEventListener('click',e=>runDomainCheck(d,d.baseDomain,d.rootHTTPSPort||443,e.currentTarget));
 node.querySelectorAll('[data-dc-mapping-check]').forEach(b=>b.onclick=()=>{const m=state.mappings.find(x=>x.id===b.dataset.dcMappingCheck);if(m)runDomainCheck(d,m.host,mappingPublicAccess(m).port,b)});
 node.querySelector('[data-dc-delete]')?.addEventListener('click',async()=>{if(!confirm('删除 '+d.baseDomain+' 的域名配置？已有映射时不能删除。'))return;try{await api('domains/'+encodeURIComponent(d.id),'DELETE');domainChecks.delete(d.id);domainPanels.delete(d.id);await refresh();toast('域名配置已删除')}catch(err){toast(err.message)}});
}
async function runDomainCheck(d,host,port,b){
 const label=b.textContent;b.disabled=true;b.textContent='检测中…';
 try{const r=await api('domains/check?id='+encodeURIComponent(d.id)+'&host='+encodeURIComponent(host)+'&port='+encodeURIComponent(port),'POST');const current=(state.domains||[]).find(x=>x.id===d.id);if(current?.baseDomain===d.baseDomain){applyPublicHTTPSCheck(current,r);domainChecks.set(d.id,r)}toast(r.dns?.[0]?.certificate?.valid?'公网 HTTPS 验证通过':'检测完成，请查看结果')}
 catch(err){domainChecks.set(d.id,{error:err.message});toast(err.message==='Failed to fetch'?'检测请求未完成，请检查后台连接；宝塔强制 HTTPS 时请使用 HTTPS 后台重新登录。':err.message)}
 finally{b.disabled=false;b.textContent=label;drawDomainList()}
}
