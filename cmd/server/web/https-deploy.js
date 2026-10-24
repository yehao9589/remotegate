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
