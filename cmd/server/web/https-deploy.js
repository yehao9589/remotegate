function baotaCertificateGuide(d){return `<p><b>1 · 绑定访问域名</b> 在宝塔的 RemoteGate 站点 → 域名管理，添加 <code>${esc(d.baseDomain)}</code> 和 <code>*.${esc(d.baseDomain)}</code>。若子域名已有独立站点，证书需部署到实际接收请求的那个站点。</p><p><b>2 · 申请并启用证书</b> 进入该站点的 SSL，选择 Let's Encrypt、DNS 验证，申请范围包含 <code>${esc(d.baseDomain)}</code> 与 <code>*.${esc(d.baseDomain)}</code>。DNS 授权在宝塔填写，申请后保存并启用，确认宝塔自动续期设置。</p><p><b>3 · 验证实际映射</b> 映射使用 HTTPS / 443，反代到 RemoteGate 并保留访问域名。返回本页分别检测主域名与具体映射地址。</p><p class="dc-help">泛域名只覆盖一级子域名，不覆盖后缀自身或更深一层的域名。这里只做公网检测。</p>`}
function createMappingHTTPSAdvice(editorContext,currentMapping){
 const node=document.getElementById('mappingHTTPSAdvice');
 function refresh(){
  if(editing!==editorContext||!$('#modal').open||!node)return;
  const m=currentMapping(),d=m&&[...(state.domains||[])].sort((a,b)=>b.baseDomain.length-a.baseDomain.length).find(x=>m.host===x.baseDomain||m.host.endsWith('.'+x.baseDomain));
  if(!m||!d||m.publicScheme!=='https'){node.hidden=true;return}
  const h=publicEndpointHealth(d,m.host,m.publicPort||443);
  node.hidden=false;
  node.innerHTML=`<b>${esc(h.text)}</b><p>${h.result?esc(h.detail):'证书在宝塔 / 反向代理申请并启用。保存映射后，可在“域名与 HTTPS”检查此地址的实际证书。'}</p><small>主域名检测通过不代表这个子域名入口已通过；访问端口按实际公网入口填写。</small>`;
 }
 return {refresh};
}
function renderHTTPSDeploymentGuide(){
 const domains=state.domains||[],d=domains[0]||{baseDomain:'gate.example.com'};
 $('#content').innerHTML=`<h2>宝塔提供 HTTPS，RemoteGate 检测访问状态</h2><p>在现有宝塔站点完成证书申请、启用与续期，公网继续使用标准 443。RemoteGate 检测实际返回的证书。</p><div class="steps"><article><b>${uiIcon('globe')}1 · 配置解析</b><p>主域名与需要的子域名指向部署服务器。添加映射只是保存转发规则，DNS 在解析平台设置。</p></article><article><b>${uiIcon('shield')}2 · 宝塔证书</b><p>给 RemoteGate 站点绑定主域名和泛域名，使用 DNS 验证申请并启用证书。</p></article><article><b>${uiIcon('activity')}3 · 检测访问</b><p>在“域名与 HTTPS”逐条检测。设备在线、内网服务可用在设备工作台另行确认。</p></article></div><section class="cert-section"><h3>只配置 RemoteGate 对应的宝塔站点</h3>${baotaCertificateGuide(d)}<p>反代目标为 <code>http://127.0.0.1:18088</code>。映射请求必须保留原始域名，把这个站点反代中的 Host 行改成下面的值，勿重复添加。</p><pre data-external-proxy>proxy_set_header Host $http_host;
proxy_set_header X-Forwarded-Proto $scheme;</pre><button data-external-copy>复制反代设置</button><p class="dc-help">保留原有 WebSocket 的 Upgrade、Connection 与 HTTP/1.1 配置。后台地址能访问，不代表所有映射域名都已配置好。</p></section><section class="cert-section"><h3>选择域名查看 HTTPS 检测</h3>${domains.map(x=>`<p><button data-external-domain="${esc(x.id)}">${uiIcon('shield')}${esc(x.baseDomain)} · HTTPS 检测</button></p>`).join('')||'<p>请先添加域名。</p>'}</section>`;
 $('#content').querySelector('[data-external-copy]').onclick=e=>copyText($('#content').querySelector('[data-external-proxy]').textContent,{button:e.currentTarget,source:$('#content').querySelector('[data-external-proxy]')});
 $('#content').querySelectorAll('[data-external-domain]').forEach(b=>b.onclick=()=>{domainSearch='';domainFilter='';domainListPage=Math.floor([...domains].findIndex(x=>x.id===b.dataset.externalDomain)/10);domainPanels.set(b.dataset.externalDomain,'certificate');page='domains';render()});
}
