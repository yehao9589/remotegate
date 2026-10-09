function nativeHTTPSPort(address){const match=String(address||'').match(/:(\d+)$/);const n=Number(match?.[1]);return n>=1&&n<=65535?n:0}
function nativeMappingPlan(mapping,c,settings){
 const address=settings.address||'',port=nativeHTTPSPort(address),host=address.slice(0,address.lastIndexOf(':')).replace(/^\[|\]$/g,'');
 const local=/^127\./.test(host)||host==='::1'||host.toLowerCase().startsWith('::ffff:127.');
 const covered=!!c.installed&&!!c.certificate?.valid&&certificateCoversHost(c.certificate.names,mapping.host);
 const available=!!settings.running&&!!port&&!local&&covered;
 const current=mappingPublicAccess(mapping);
 return {port,covered,available,url:port?publicMappingURL({host:mapping.host,publicScheme:'https',publicPort:port}):'',applied:available&&current.scheme==='https'&&current.port===port,reason:!covered?'证书未覆盖 / 已失效':!settings.running?'HTTPS 入口未启用':local?'仅监听本机，需代理转发':!port?'监听端口无效':'证书覆盖'};
}
function deploymentRootNote(d,c){return certificateCoversHost(c.certificate?.names,d.baseDomain)?'证书同时覆盖主域名，可在包含这些名称的站点部署。':'这张证书不覆盖 '+d.baseDomain+' 本身。请给子域名建立独立站点，保留现有主域名站点证书。'}
function downloadHTTPSBlob(blob,filename){const url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download=filename;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000)}

function sharedBaotaConfig(address,proxyProtocol){
 const port=nativeHTTPSPort(address),host=String(address).slice(0,String(address).lastIndexOf(':'));
 if(!port||!['127.0.0.1','[::1]'].includes(host))return '';
 return `# 修改各 HTTPS server 的原 443 监听；原证书及站点配置保留\nlisten ${address} ssl${proxyProtocol?' proxy_protocol':''};${proxyProtocol?'\nset_real_ip_from 127.0.0.1;\nset_real_ip_from ::1;\nreal_ip_header proxy_protocol;':''}\n# 原有 http2、default_server 等参数按站点保留；IPv6 443 也须移到回环地址。`;
}
function deploymentListenerPanel(settings,shared){
 const address=shared?(settings.mode==='shared'?settings.listenAddress:':443'):(settings.listenAddress||':443');
 const backend=settings.fallbackAddress||'127.0.0.1:9443',proxy=settings.mode==='shared'?!!settings.proxyProtocol:true;
 const status=settings.running?'已监听 '+settings.address:settings.error?'启动失败':settings.enabled?'未运行':'未启用';
 return `<section class="cert-section"><h3>1 · ${shared?'与宝塔共用公网 443':'启用独立 HTTPS 入口'} <span class="dc-badge ${settings.running?'green':'warn'}">${esc(status)}</span></h3><p class="cert-field-hint">${shared?'公网统一使用 443：映射由 RemoteGate 提供证书，其余域名转给宝塔，保留原有网站与证书。以后添加映射或续期不用逐个修改宝塔。':'后台域名与映射按访问域名自动匹配证书；默认使用公网 443，地址无需写端口，续期后新连接自动使用新证书。'}</p><form data-deploy-listener><div class="https-listener-form">${shared?'':'<details class="cert-settings https-listener-advanced"><summary>高级：自定义监听地址</summary>'}<label>公网监听地址<input name="listenAddress" value="${esc(address||':443')}" required placeholder=":443"></label>${shared?'':'<p class="cert-field-hint">默认 :443 即可；只有自行规划端口时才需要修改。</p></details>'}${shared?`<label>宝塔内部 HTTPS 地址<input name="fallbackAddress" value="${esc(backend)}" required placeholder="127.0.0.1:9443"></label>`:''}<button type="submit" class="primary">${shared?'保存并启用共用入口':settings.running?'保存入口设置':'保存并启用 HTTPS'}</button></div>${shared?`<label class="https-check"><input name="proxyProtocol" type="checkbox" ${proxy?'checked':''}> 传递真实客户端 IP（宝塔监听须启用 proxy_protocol）</label><details class="cert-settings" ${settings.mode==='shared'&&settings.running?'':'open'}><summary>首次启用：一次性调整宝塔监听</summary><p class="cert-field-hint">先备份所有网站的 Nginx 配置，将原有 443 监听迁移到上述回环地址；保留 SSL 证书、反代和网站配置。包括 IPv6 与默认站点，不能有站点继续占用公网 TCP 443。仅删证书不会释放端口。</p><pre data-shared-config>${esc(sharedBaotaConfig(backend,proxy))}</pre><button type="button" data-shared-copy>复制监听示例</button><p class="cert-field-hint">在服务器执行 nginx -t，检查通过后重载 Nginx，再启用此入口。这里不会自动改动宝塔；如果需要回退，停用共用入口并恢复备份、重载 Nginx。</p></details><label class="https-check"><input name="migrationReady" type="checkbox" required ${settings.mode==='shared'&&settings.running?'checked':''}> 已完成宝塔内部监听设置并通过配置检查</label>`:''}</form><p class="cert-field-hint">配置持久保存，入口作用于全部域名。放行公网 TCP ${nativeHTTPSPort(address)||443}。${shared?'回源端口仅绑定回环地址，不向公网开放。':'系统直接提供后台与映射 HTTPS，无需安装 Nginx 或宝塔。'}Docker 使用 host 网络，bridge 网络需发布公网入口端口。</p>${settings.error?`<p class="cert-form-error">${esc(settings.error)}</p>`:''}${settings.running?'<button type="button" data-deploy-stop>停止 HTTPS 入口</button>':''}</section>`;
}

function createMappingHTTPSAdvice(editorContext,currentMapping,apply){
 const loaded=new Map(),node=document.getElementById('mappingHTTPSAdvice');
 function refresh(){
  if(editing!==editorContext||!$('#modal').open)return;
  const mapping=currentMapping(),d=mapping&&[...(state.domains||[])].sort((a,b)=>b.baseDomain.length-a.baseDomain.length).find(d=>mapping.host===d.baseDomain||mapping.host.endsWith('.'+d.baseDomain));
  node.hidden=true;if(!d)return;
  const c=loaded.get(d.id);
  if(c===undefined){loaded.set(d.id,null);api(certificatePath(d)).then(value=>{loaded.set(d.id,value);refresh()}).catch(()=>{loaded.set(d.id,false)});return}
  if(!c?.installed)return;
  const plan=nativeMappingPlan(mapping,c,c.httpsSettings||{running:!!c.httpsAddress,address:c.httpsAddress});
  node.hidden=false;
  node.innerHTML=plan.available?`<span>${plan.applied?'这张证书将由系统 HTTPS 入口提供；保存后请检测公网与内网服务。':'系统证书可用于 HTTPS 入口 '+esc(plan.url)+'。当前选择的入口需自行提供匹配证书。'}</span>${!plan.applied?'<button type="button" data-use-native>使用系统 HTTPS 入口</button>':''}`:`<span>系统已有证书，但${esc(plan.reason)}。请在域名的“接入 HTTPS”中配置入口；默认由系统直接提供 HTTPS，无需将证书导入其他软件。</span>`;
  node.querySelector('[data-use-native]')?.addEventListener('click',()=>apply(plan));
 }
 return {refresh};
}

function openHTTPSDeployment(d,c){
 let dialog=document.getElementById('deploymentDialog');
 if(!dialog){dialog=document.createElement('dialog');dialog.id='deploymentDialog';dialog.className='cert-dialog https-deploy-dialog';document.body.append(dialog)}
 let settings=c.httpsSettings||{running:!!c.httpsAddress,address:c.httpsAddress,listenAddress:c.httpsAddress||':443'},mode=settings.mode==='shared'?'shared':'native',working=false,edited=false;
 dialog.innerHTML=`<header class="cert-dialog-head"><div><span class="eyebrow">${esc(d.baseDomain)} · HTTPS 接入</span><h2>${uiIcon('shield')}直接用域名访问</h2><p>系统独立提供 HTTPS，后台和映射均可使用申请的证书。</p></div><button type="button" class="icon-button" data-deploy-close aria-label="关闭 HTTPS 接入">${uiIcon('close')}</button></header><div class="cert-dialog-body"><div class="source-choices https-mode-choices https-primary-mode"><label><input type="radio" name="httpsDeployment" value="native" ${mode==='native'?'checked':''}><b>由 RemoteGate 提供 HTTPS</b><span>后台和映射直接使用系统证书 · 自动续期生效</span></label></div><details class="cert-settings" ${mode==='shared'?'open':''}><summary>高级：兼容现有外部入口（可选）</summary><p class="cert-field-hint">独立部署不需要这些选项。只在你希望保留前置代理或其他网站时使用。</p><label class="https-check"><input type="radio" name="httpsDeployment" value="shared" ${mode==='shared'?'checked':''}> 与现有服务器共用 443（需迁移原监听）</label><label class="https-check"><input type="radio" name="httpsDeployment" value="proxy"> 手动导出给外部 HTTPS 入口（续期后需重新导入）</label></details><div data-deploy-body></div><p data-deploy-error class="cert-form-error" role="alert"></p><p data-deploy-progress class="cert-field-hint" role="status"></p></div><footer class="cert-dialog-foot"><span>证书就绪 → 443 入口 → 映射 → 检测</span><button type="button" data-deploy-close>关闭</button></footer>`;
 const close=()=>{if(!working){dialog.close();dialog.replaceChildren()}};
 dialog.querySelectorAll('[data-deploy-close]').forEach(b=>b.onclick=close);dialog.oncancel=e=>{e.preventDefault();close()};
 dialog.querySelectorAll('[name=httpsDeployment]').forEach(input=>input.onchange=()=>{if(!working){mode=input.value;paint()}});
 function paint(){
  dialog.querySelector('[data-deploy-error]').textContent='';
  const box=dialog.querySelector('[data-deploy-body]');
  if(mode==='proxy'){
   box.innerHTML=`<section class="cert-section"><h3>1 · 使用已申请的证书</h3><p class="cert-field-hint">下载完整证书链与匹配私钥，直接导入宝塔，无需重新申请。</p><p class="cert-external-notice">${esc(deploymentRootNote(d,c))}</p><form data-deploy-export><label>验证管理员密码<input name="currentPassword" type="password" autocomplete="current-password" required></label><button type="submit" class="primary">${uiIcon('download')}下载 HTTPS 部署包</button></form><p class="cert-field-hint">部署包包含 fullchain.pem、privkey.pem 和对应部署说明。仅在 HTTPS 后台或本机开发环境允许下载。</p></section><section class="cert-section"><h3>2 · 在宝塔配置子域名入口</h3><p class="cert-field-hint">绑定证书覆盖的子域名。SSL → 当前证书：PEM 填 fullchain.pem，KEY 填 privkey.pem，保存并启用。</p><p class="cert-field-hint">反代目标为 http://127.0.0.1:18088。在现有配置中替换 Host 行并传递协议，其余 WebSocket 配置保留。</p><pre data-proxy-headers>proxy_set_header Host $http_host;
proxy_set_header X-Forwarded-Proto $scheme;</pre><button type="button" data-deploy-copy>复制转发头</button></section><section class="cert-section"><h3>3 · 验证访问与续期</h3><p class="cert-field-hint">映射填写 HTTPS 和实际公网端口（默认 443）。在“解析接入”检测具体子域名；设备内网服务另用映射检测按钮检查。</p><p class="cert-external-notice">宝塔使用的是本次导入的证书。RemoteGate 续期后需重新下载、导入；此方式不会自动修改宝塔。希望续期直接生效，可使用独立或共用 HTTPS 入口。</p></section>`;
   box.querySelector('[data-deploy-copy]').onclick=e=>copyText(box.querySelector('[data-proxy-headers]').textContent,{button:e.currentTarget,source:box.querySelector('[data-proxy-headers]')});
   box.querySelector('[data-deploy-export]').onsubmit=e=>{e.preventDefault();const form=e.currentTarget,button=form.querySelector('button'),password=form.elements.currentPassword;run(button,async()=>{try{const blob=await api(certificatePath(d),'POST',{action:'deploy-export',currentPassword:password.value},{responseType:'blob'});downloadHTTPSBlob(blob,d.baseDomain+'-https-deploy.zip');toast('部署包已下载，请按包内说明导入宝塔')}finally{password.value=''}})};
   return;
  }
  const shared=mode==='shared';
  const mappings=domainMappings(d),devices=state.devices||[];
  box.innerHTML=deploymentListenerPanel(settings,shared)+`<section class="cert-section"><h3>2 · 让映射使用这张证书</h3><div class="tablewrap"><table class="https-deploy-table"><thead><tr><th>映射 / 设备</th><th>HTTPS 访问地址</th><th>操作</th></tr></thead><tbody>${mappings.map(mapping=>{const plan=nativeMappingPlan(mapping,c,settings),device=devices.find(x=>x.id===mapping.deviceId);return `<tr><td><b>${esc(mapping.host)}</b><small>${!mapping.enabled?'映射已停用':device?.online?'设备在线':'设备离线'} · ${esc(plan.reason)}</small></td><td>${plan.url?`<code>${esc(plan.url)}</code>`:'先启用入口'}<small>当前：${esc(publicMappingURL(mapping))}</small></td><td><button type="button" data-native-mapping="${esc(mapping.id)}" ${!plan.available||!mapping.enabled||plan.applied?'disabled':''}>${plan.applied?'已使用':'使用 HTTPS 入口'}</button><button type="button" data-native-check="${esc(mapping.id)}" ${!plan.applied?'disabled':''}>检测公网 HTTPS</button></td></tr>`}).join('')||'<tr><td colspan="3">暂无映射。先在设备下添加访问域名。</td></tr>'}</tbody></table></div></section><section class="cert-section"><h3>3 · 确认公网访问</h3><p class="cert-field-hint">检测通过后打开上面的 HTTPS 地址。证书覆盖、端口监听、DNS / 公网证书、设备在线与内网服务分别确认；监听成功不会直接标成“公网已验证”。</p><div data-deploy-check-result></div></section>`;
  const listenerForm=box.querySelector('[data-deploy-listener]');
  listenerForm.oninput=()=>{edited=true;if(shared){const source=box.querySelector('[data-shared-config]');source.textContent=sharedBaotaConfig(listenerForm.elements.fallbackAddress.value,listenerForm.elements.proxyProtocol.checked);box.querySelector('[data-shared-copy]').disabled=!source.textContent}};
  box.querySelector('[data-shared-copy]')?.addEventListener('click',e=>copyText(box.querySelector('[data-shared-config]').textContent,{button:e.currentTarget,source:box.querySelector('[data-shared-config]')}));
  listenerForm.onsubmit=e=>{e.preventDefault();if(settings.running&&settings.mode==='shared'&&!shared&&!confirm('切换为独立入口后，宝塔网站将不再经此入口转发。确认已安排其他入口？'))return;const fields=e.currentTarget.elements,button=e.currentTarget.querySelector('button[type=submit]'),payload={enabled:true,listenAddress:fields.listenAddress.value,mode:shared?'shared':'direct',fallbackAddress:shared?fields.fallbackAddress.value:'',proxyProtocol:shared&&fields.proxyProtocol.checked};run(button,async()=>{settings=await api('https','POST',payload);c=await api(certificatePath(d));domainCertificates.set(d.id,c);drawDomainList();paint();toast('HTTPS 入口已保存，请检测映射和原有网站')})};
  box.querySelector('[data-deploy-stop]')?.addEventListener('click',e=>{if(!confirm(settings.mode==='shared'?'停用共用入口会同时中断映射、宝塔网站和设备连接。回退时还需恢复宝塔原 443 监听，确认停用？':'停止入口会影响所有使用此 HTTPS 入口的域名和设备连接，确认停止？'))return;run(e.currentTarget,async()=>{settings=await api('https','POST',{...settings,enabled:false});await loadDomainCertificate(d);paint();toast('HTTPS 入口已停止')})});
  box.querySelectorAll('[data-native-mapping]').forEach(button=>button.onclick=()=>run(button,async()=>{const mapping=state.mappings.find(x=>x.id===button.dataset.nativeMapping);settings=await api('https');c=await api(certificatePath(d));const plan=nativeMappingPlan(mapping,c,settings);if(!plan.available)throw Error(plan.reason);const updated=await api('mappings','POST',{...mapping,publicScheme:'https',publicPort:plan.port});Object.assign(mapping,updated);domainCertificates.set(d.id,c);drawDomainList();paint();toast('映射已使用系统 HTTPS，请检测公网入口')}));
  box.querySelectorAll('[data-native-check]').forEach(button=>button.onclick=()=>run(button,async()=>{const mapping=state.mappings.find(x=>x.id===button.dataset.nativeCheck),access=mappingPublicAccess(mapping);const result=await api('domains/check?id='+encodeURIComponent(d.id)+'&host='+encodeURIComponent(mapping.host)+'&port='+access.port,'POST');applyPublicHTTPSCheck(d,result);domainChecks.set(d.id,result);box.querySelector('[data-deploy-check-result]').innerHTML=domainCheckResult(result);drawDomainList();toast(result.dns?.[0]?.certificate?.valid?'公网 HTTPS 验证通过；设备内网服务请另外检测':'公网验证未通过，请查看检测结果')}));
 }
 async function run(button,operation){
  if(working)return;edited=true;working=true;button.disabled=true;
  const progress=dialog.querySelector('[data-deploy-progress]');progress.textContent='正在处理…';
  dialog.querySelector('[data-deploy-error]').textContent='';dialog.querySelectorAll('[name=httpsDeployment]').forEach(x=>x.disabled=true);
  try{await operation();progress.textContent='操作完成'}
  catch(error){if(dialog.open)dialog.querySelector('[data-deploy-error]').textContent=error.message}
  finally{working=false;button.disabled=false;dialog.querySelectorAll('[name=httpsDeployment]').forEach(x=>x.disabled=false)}
 }
 paint();if(!dialog.open)dialog.showModal();
 api('https').then(value=>{if(dialog.open&&!working&&!edited){settings=value;if(mode!=='proxy')paint()}}).catch(error=>{if(dialog.open)dialog.querySelector('[data-deploy-error]').textContent='入口状态读取失败：'+error.message});
}
