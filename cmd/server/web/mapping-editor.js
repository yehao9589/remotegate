const customMappingDomain='__custom__';
const completeMappingHost=/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/i;

function mappingHost(host,suffix){
 host=host.trim().toLowerCase();
 if(!suffix)throw Error('请先选择域名后缀，或点击“添加域名”。');
 let full=suffix===customMappingDomain?host:host==='@'?suffix:host===suffix||host.endsWith('.'+suffix)?host:host+'.'+suffix;
 if(!host||full.length>253||!completeMappingHost.test(full)||/^\d+(?:\.\d+){3}$/.test(full))throw Error(suffix===customMappingDomain?'请填写完整访问域名，例如 router.example.com，不要只填写 router。':'请填写有效子域名，例如 router；访问域名不能包含协议、端口、路径或星号。');
 return full;
}
function mappingPublicAccess(item={},domain=''){const local=item.host==='localhost'||item.host?.endsWith('.localhost')||item.host?.endsWith('.127.0.0.1.nip.io')||(!item.id&&domain==='localhost');const scheme=item.publicScheme||(local?'http':'https');return {scheme,port:item.publicPort||(local?(Number(location.port)||80):(scheme==='https'?443:80))}}
function publicMappingURL(item){const access=mappingPublicAccess(item),host=item.host||'',standard=(access.scheme==='https'&&access.port===443)||(access.scheme==='http'&&access.port===80);return `${access.scheme}://${host}${standard?'':':'+access.port}/`}
function splitMappingTarget(value){try{const u=new URL(value),port=u.port||(u.protocol==='https:'?'443':'80');u.port='';return {address:u.pathname==='/'&&!u.search&&!u.hash?u.href.replace(/\/$/,''):u.href,port}}catch{return {address:value,port:'80'}}}
function joinMappingTarget(address,port){const u=new URL(address);if(!['http:','https:'].includes(u.protocol))throw Error('内网地址仅支持 http:// 或 https://');if(u.port)throw Error('请把端口填写在右侧端口栏');if(!/^\d+$/.test(port)||Number(port)<1||Number(port)>65535)throw Error('端口范围为 1–65535');u.port=port;return u.href}

function mappingDraft(){
 const f=new FormData($('#editor')),item=editing.item;
 return {...item,deviceId:item.deviceId||f.get('deviceId'),_mappingDraft:{host:f.get('host'),domainSuffix:f.get('domainSuffix'),publicScheme:f.get('publicScheme'),publicPort:f.get('publicPort'),targetAddress:f.get('targetAddress'),targetPort:f.get('targetPort'),note:f.get('note'),enabled:f.has('enabled')}};
}

const baseOpenMappingEditor=openEditor;
openEditor=(type,item={})=>{
 baseOpenMappingEditor(type,item);$('#modal').classList.toggle('mapping-dialog',type==='mapping');if(type!=='mapping')return;
 const context=editing,draft=item._mappingDraft,domains=state.domains||[],device=state.devices.find(d=>d.id===item.deviceId);
 const selected=[...domains].sort((a,b)=>b.baseDomain.length-a.baseDomain.length).find(d=>item.host&&(item.host===d.baseDomain||item.host.endsWith('.'+d.baseDomain)));
 const suffix=draft?.domainSuffix??(selected?.baseDomain||(item.host?customMappingDomain:domains[0]?.baseDomain||''));
 const prefix=draft?.host??(selected?(item.host===selected.baseDomain?'@':item.host.slice(0,-selected.baseDomain.length-1)):item.host||'');
 const target=splitMappingTarget(item.target||'http://127.0.0.1:80');
 const access=mappingPublicAccess(item,suffix),scheme=access.scheme,port=access.port;
 $('#fields').innerHTML=`${device?`<p class="mapping-device">接入设备：<strong>${esc(device.name)}</strong></p>`:`<label>接入设备<select name="deviceId" required>${state.devices.map(d=>`<option value="${esc(d.id)}">${esc(d.name)}</option>`).join('')}</select></label>`}
 <div class="mapping-domain-grid"><div><div class="mapping-domain-heading"><label for="mappingDomainSuffix">域名后缀</label><button type="button" id="mappingAddDomain">＋ 添加域名</button></div>
 <select id="mappingDomainSuffix" name="domainSuffix" required aria-label="域名后缀"></select>
 <p id="mappingDomainStatus" class="mapping-domain-hint"></p></div>
 <label class="mapping-host-label"><span id="mappingHostLabel">子域名</span><div class="mapping-host-field"><input name="host" required value="${esc(prefix)}" autocomplete="off"><span id="mappingHostSuffix" aria-hidden="true"></span></div></label></div>
 <div class="mapping-public"><label>公网协议<select name="publicScheme"><option value="https">HTTPS</option><option value="http">HTTP</option></select></label><label>公网端口<input name="publicPort" type="number" min="1" max="65535" step="1" required></label></div>
 <p class="mapping-preview" id="mappingPreview" role="status" aria-live="polite"></p>
 <div id="mappingHTTPSAdvice" class="mapping-https-advice" role="status" hidden></div>
 <details class="mapping-help"><summary>DNS 与证书要求</summary><p>主机名填 @ 表示使用域名后缀本身。预览仅组合地址；DNS 须解析到服务器，并配置对应公网入口。HTTPS 证书须覆盖完整访问域名。</p></details>
 <div class="mapping-target"><label>内网地址<input name="targetAddress" type="url" required value="${esc(draft?.targetAddress??target.address)}" placeholder="http://127.0.0.1"></label><label>内网端口<input name="targetPort" type="number" min="1" max="65535" step="1" required value="${esc(draft?.targetPort??target.port)}" placeholder="80"></label></div>
 <label>备注<input name="note" maxlength="200" value="${esc(draft?.note??item.note??'')}"></label><label class="check"><input name="enabled" type="checkbox" ${(draft?.enabled??item.enabled)!==false?'checked':''}> 启用映射</label>`;
 const suffixInput=$('#mappingDomainSuffix'),hostInput=$('#fields [name=host]'),schemeInput=$('#fields [name=publicScheme]'),portInput=$('#fields [name=publicPort]');
 let httpsAdvice;
 schemeInput.value=draft?.publicScheme??scheme;portInput.value=draft?.publicPort??port;
 function preview(){
  const suffix=suffixInput.value,manual=suffix===customMappingDomain;
  $('#mappingHostLabel').textContent=manual?'完整访问域名':'子域名';hostInput.placeholder=manual?'例如 router.example.com':'例如 ceshi01';
  $('#mappingHostSuffix').textContent=suffix&&!manual?'.'+suffix:'';$('#mappingHostSuffix').hidden=!suffix||manual;
  hostInput.setCustomValidity('');const output=$('#mappingPreview');
  try{
   const host=mappingHost(hostInput.value,suffix),port=Number(portInput.value);
   if(!portInput.value||!Number.isInteger(port)||port<1||port>65535)throw Error('请填写有效的公网端口（1–65535）。');
   output.textContent='公网访问：'+publicMappingURL({host,publicScheme:schemeInput.value,publicPort:port});output.dataset.valid='true';
  }catch(e){
   output.textContent=!hostInput.value.trim()&&suffix?'填写主机名后显示完整访问地址。':e.message;output.dataset.valid='false';
   if(hostInput.value.trim()&&suffix)try{mappingHost(hostInput.value,suffix)}catch(error){hostInput.setCustomValidity(error.message)}
  }
  httpsAdvice?.refresh();
 }
 function domainOptions(list,current){
  suffixInput.innerHTML=`<option value="" disabled>请选择或先添加域名</option>${list.map(d=>`<option value="${esc(d.baseDomain)}">${esc(d.baseDomain)} · 显示名称：${esc(d.name||d.baseDomain)}</option>`).join('')}<option value="${customMappingDomain}">手动填写完整域名</option>`;
  suffixInput.value=current===customMappingDomain||list.some(d=>d.baseDomain===current)?current:'';
  $('#mappingDomainStatus').textContent=list.length?'选择已有域名，再填写主机名。':'还没有添加域名。点击“添加域名”，完成后会返回当前映射；也可选择手动填写完整域名。';preview();
 }
 domainOptions(domains,suffix);
 httpsAdvice=createMappingHTTPSAdvice(context,()=>{try{return {host:mappingHost(hostInput.value,suffixInput.value),publicScheme:schemeInput.value,publicPort:Number(portInput.value)}}catch{return null}},plan=>{schemeInput.value='https';portInput.value=plan.port;preview()});
 httpsAdvice.refresh();
 hostInput.oninput=preview;suffixInput.onchange=preview;portInput.oninput=preview;
 schemeInput.onchange=()=>{if(portInput.value==='80'||portInput.value==='443')portInput.value=schemeInput.value==='https'?'443':'80';preview()};
 $('#mappingAddDomain').onclick=()=>{const draft=mappingDraft();$('#modal').close();openDomainEditor({},draft)};
 // Refresh just the domain choices, preserving any text the user is entering.
 api('domains').then(list=>{
  if(editing!==context||!$('#modal').open)return;
  state.domains=list||[];let current=suffixInput.value;
  if(!current&&state.domains.length){const host=hostInput.value.trim().toLowerCase();current=[...state.domains].sort((a,b)=>b.baseDomain.length-a.baseDomain.length).find(d=>host===d.baseDomain||host.endsWith('.'+d.baseDomain))?.baseDomain||state.domains[0].baseDomain}
  domainOptions(state.domains,current);
 }).catch(()=>{if(editing===context&&$('#modal').open)$('#mappingDomainStatus').textContent='域名列表刷新失败；可使用已显示的域名，或关闭弹窗后重试。'});
};

const submitDomainOrDevice=$('#editor').onsubmit;
$('#editor').onsubmit=async e=>{
 if(editing?.type!=='mapping')return submitDomainOrDevice(e);
 e.preventDefault();$('#save').disabled=true;
 try{
  const f=new FormData(e.target),{_mappingDraft,...item}=editing.item,host=mappingHost(f.get('host'),f.get('domainSuffix'));
  const target=joinMappingTarget(f.get('targetAddress').trim(),f.get('targetPort')),publicPort=Number(f.get('publicPort'));
  if(!Number.isInteger(publicPort)||publicPort<1||publicPort>65535)throw Error('公网端口范围为 1–65535');
  await api('mappings','POST',{...item,host,deviceId:item.deviceId||f.get('deviceId'),target,publicScheme:f.get('publicScheme'),publicPort,note:f.get('note'),enabled:f.has('enabled')});
  $('#modal').close();await refresh();toast('映射已保存');
 }catch(err){$('#formError').textContent=err.message}finally{$('#save').disabled=false}
};
