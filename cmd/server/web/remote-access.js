// SSH credentials remain in the current form and are never saved to storage.
let remoteContext=null,remoteTerminalAssets=null;
const previousRemoteAction=action,previousRemoteWorkbench=renderWorkbench;
function remoteDeviceTabs(d){
 return '<button type="button" class="feature-tab remote-feature" data-action="remoteSSH" data-id="'+esc(d.id)+'" '+(!d.online?'disabled':'')+'>'+uiIcon('terminal')+'SSH 终端</button><button type="button" class="feature-tab remote-feature" data-action="remoteFiles" data-id="'+esc(d.id)+'" '+(!d.online?'disabled':'')+'>'+uiIcon('folder')+'文件管理</button>';
}
function remoteCurrent(c){return remoteContext===c&&!c.closed&&c.transition===authTransition&&$('#remoteDialog')?.open}
function remoteSessionExpired(c){
 if(!remoteCurrent(c)||c.expired||!c.session)return;c.expired=true;c.socket?.close();
 const badge=$('#remoteSessionBadge');if(badge){badge.textContent='会话已结束';badge.className='pill off'}
 $('#remoteContents').querySelectorAll('button,input').forEach(input=>input.disabled=true);
 $('#remoteFooter').textContent='SSH 会话已结束；重新连接后再操作';
 $('#remoteDisconnect').textContent='重新连接';$('#remoteDisconnect').onclick=()=>openRemote(c.device.id,c.view);
}
function remoteBytes(n){if(!Number.isFinite(n))return '—';const units=['B','KB','MB','GB'];let i=0;while(n>=1024&&i<3){n/=1024;i++}return (i?n.toFixed(1):n)+' '+units[i]}
function remoteJoin(dir,name){return (dir==='/'?'':dir)+'/'+name}
function remoteParent(dir){return dir.replace(/\/+$/,'').replace(/\/[^/]*$/,'')||'/'}
function remoteBase64(buffer){const bytes=new Uint8Array(buffer);let text='';for(let i=0;i<bytes.length;i+=32768)text+=String.fromCharCode(...bytes.subarray(i,i+32768));return btoa(text)}
function remoteDialog(){
 if($('#remoteDialog'))return;
 document.body.insertAdjacentHTML('beforeend','<dialog id="remoteDialog" class="remote-dialog"><div class="remote-heading"><div><span class="remote-eyebrow">REMOTE TOOLS</span><h2 id="remoteTitle"></h2><p id="remoteSubtitle"></p></div><button type="button" id="remoteClose" aria-label="关闭远程工具">'+uiIcon('close')+'</button></div><div id="remoteContents"></div><div class="remote-footer"><span id="remoteFooter">通过设备隧道连接现有 SSH 服务</span><button type="button" id="remoteDisconnect" hidden>断开连接</button></div></dialog>');
 $('#remoteClose').onclick=()=>$('#remoteDialog').close();
 $('#remoteDisconnect').onclick=()=>$('#remoteDialog').close();
 $('#remoteDialog').addEventListener('close',()=>closeRemote(remoteContext));
}
function closeRemote(c){
 if(!c||c.closed)return;
 c.closed=true;c.nameResolve?.(null);c.controllers.forEach(controller=>controller.abort());c.observer?.disconnect();c.socket?.close();c.terminal?.dispose();
 if(c.session)fetch('/api/admin/remote/'+encodeURIComponent(c.session.id),{method:'DELETE',credentials:'same-origin',keepalive:true}).catch(()=>{});
 if(remoteContext===c){remoteContext=null;$('#remoteContents').replaceChildren();$('#remoteDisconnect').hidden=true}
}
window.addEventListener('pagehide',()=>closeRemote(remoteContext));
renderWorkbench=()=>{if(remoteContext&&remoteContext.transition!==authTransition){closeRemote(remoteContext);$('#remoteDialog')?.close()}if(remoteContext?.session&&!state.devices.some(device=>device.id===remoteContext.device.id&&device.online))remoteSessionExpired(remoteContext);previousRemoteWorkbench()};
action=async(a,id)=>{if(a==='remoteSSH'||a==='remoteFiles'){openRemote(id,a==='remoteFiles'?'files':'terminal');return}return previousRemoteAction(a,id)};
function openRemote(id,view){
 const device=state.devices.find(d=>d.id===id);if(!device||!device.online){toast('设备离线，请等待恢复连接');return}
 closeRemote(remoteContext);remoteDialog();
 const c={device,view,transition:authTransition,controllers:new Set(),closed:false,session:null,fp:null,busy:false,dir:'',entries:[],query:'',upload:false};remoteContext=c;
 $('#remoteTitle').textContent=device.name+' · 远程工具';
 $('#remoteSubtitle').textContent='使用设备现有 SSH 账号，可连接路由器本机或内网 NAS。';
 $('#remoteFooter').textContent='SSH 账号权限决定可执行命令与可访问文件';
 $('#remoteDisconnect').hidden=true;$('#remoteDisconnect').textContent='断开连接';$('#remoteDisconnect').onclick=()=>$('#remoteDialog').close();
 if(!device.canRemote){
  $('#remoteContents').innerHTML='<div class="remote-enable-guide">'+uiIcon('terminal')+'<h3>先启用远程管理</h3><ol><li>将路由器插件更新到支持 SSH / 文件管理的版本。</li><li>新版默认开启 SSH / 文件管理；若曾关闭，请在路由器“服务 → RemoteGate”重新启用并保存应用。</li><li>回到后台刷新设备，再使用 SSH 账号连接。</li></ol><p class="muted">文件管理需要目标 SSH 服务支持 SFTP；OpenWrt 可安装 openssh-sftp-server。公网后台和插件服务器地址应使用 HTTPS。</p><button type="button" id="remoteUpgrade" class="primary">检查插件更新</button></div>';
  $('#remoteUpgrade').onclick=()=>{$('#remoteDialog').close();action('updatePlugin',device.id).catch(e=>toast(e.message))};
 }else renderRemoteLogin(c);
 $('#remoteDialog').showModal();
}
function renderRemoteLogin(c){
 $('#remoteContents').innerHTML='<form id="remoteLogin" class="remote-login"><div class="remote-login-intro">'+uiIcon('shield')+'<div><strong>连接现有 SSH 服务</strong><p>默认连接路由器自身的 127.0.0.1:22。可改为设备能访问的内网地址；密码与私钥仅用于当前会话。</p></div></div><div class="remote-form-grid"><label>SSH 内网地址<input id="remoteHost" value="127.0.0.1" required maxlength="253" autocomplete="off"></label><label>SSH 端口<input id="remotePort" type="number" value="22" min="1" max="65535" required></label><label>SSH 用户名<input id="remoteUsername" value="root" required maxlength="128" autocomplete="off"></label><label>认证方式<select id="remoteAuthMethod"><option value="password">密码</option><option value="key">私钥</option></select></label></div><label id="remotePasswordRow">SSH 密码<input id="remotePassword" type="password" autocomplete="new-password" placeholder="目标 SSH 服务允许空密码时可留空"></label><div id="remoteKeyRows" hidden><label>SSH 私钥<textarea id="remoteKey" rows="6" autocomplete="off" spellcheck="false" maxlength="65536" placeholder="粘贴 OpenSSH 或 PEM 私钥"></textarea></label><label>私钥密码（如有）<input id="remotePassphrase" type="password" autocomplete="new-password"></label></div><div class="remote-host-key"><div><strong>主机指纹</strong><button type="button" id="remoteProbe">'+uiIcon('activity')+'检测主机指纹</button></div><p id="remoteFingerprint" class="muted">首次连接请与设备上的 SSH 主机指纹核对。</p><label class="check" id="remoteTrustRow" hidden><input type="checkbox" id="remoteTrust"> 我已核对并信任此主机指纹</label></div><p id="remoteLoginError" class="remote-error" role="alert"></p><div class="remote-login-actions"><span class="muted">退出后台、关闭窗口或闲置 15 分钟会断开会话</span><button type="submit" class="primary" id="remoteConnect">连接 SSH</button></div></form>';
 $('#remoteAuthMethod').onchange=()=>{const key=$('#remoteAuthMethod').value==='key';$('#remotePasswordRow').hidden=key;$('#remoteKeyRows').hidden=!key};
 for(const id of ['#remoteHost','#remotePort'])$(id).oninput=()=>{c.fp=null;$('#remoteFingerprint').textContent='地址已修改，请重新检测主机指纹。';$('#remoteTrustRow').hidden=true;$('#remoteTrust').checked=false};
 $('#remoteProbe').onclick=()=>probeRemote(c);
 $('#remoteLogin').onsubmit=e=>{e.preventDefault();connectRemote(c)};
}
function remoteTarget(){return {host:$('#remoteHost').value.trim(),port:Number($('#remotePort').value)}}
async function remoteCall(c,path,method='POST',body){
 const controller=new AbortController();c.controllers.add(controller);const timer=setTimeout(()=>controller.abort(),70000);
 try{return await api(path,method,body,{signal:controller.signal})}
 catch(e){if(e.name==='AbortError')throw Error(c.closed?'连接已关闭':'请求超时，请检查设备和 SSH 服务');if(e.status===404&&path.startsWith('remote/')){remoteSessionExpired(c);throw Error('SSH 会话已结束，请重新连接。')}throw e}
 finally{clearTimeout(timer);c.controllers.delete(controller)}
}
function remoteLoginBusy(c,active){c.busy=active;$('#remoteProbe').disabled=active;$('#remoteConnect').disabled=active;for(const input of $('#remoteLogin').querySelectorAll('input,select,textarea'))input.disabled=active}
async function probeRemote(c){
 if(!remoteCurrent(c)||c.busy)return;
 const target=remoteTarget();remoteLoginBusy(c,true);$('#remoteLoginError').textContent='';$('#remoteFingerprint').textContent='正在读取主机指纹…';
 try{const fp=await remoteCall(c,'devices/'+encodeURIComponent(c.device.id)+'/remote','POST',{action:'fingerprint',...target});if(!remoteCurrent(c))return;c.fp={...fp,...target};$('#remoteFingerprint').innerHTML='<code>'+esc(fp.fingerprint)+'</code><span>'+esc(fp.algorithm)+' · '+esc(fp.target)+'</span>';$('#remoteTrustRow').hidden=false;$('#remoteTrust').checked=false}
 catch(e){if(remoteCurrent(c)){$('#remoteLoginError').textContent=e.message;$('#remoteFingerprint').textContent='检测失败，请重试。';c.fp=null}}
 finally{if(remoteCurrent(c))remoteLoginBusy(c,false)}
}
async function connectRemote(c){
 if(!remoteCurrent(c)||c.busy)return;
 const target=remoteTarget();
 if(!c.fp||c.fp.host!==target.host||c.fp.port!==target.port||!$('#remoteTrust').checked){$('#remoteLoginError').textContent='请先检测、核对并勾选信任主机指纹。';return}
 const body={action:'connect',...target,username:$('#remoteUsername').value.trim(),fingerprint:c.fp.fingerprint};
 if($('#remoteAuthMethod').value==='key'){body.privateKey=$('#remoteKey').value;body.passphrase=$('#remotePassphrase').value}else body.password=$('#remotePassword').value;
 remoteLoginBusy(c,true);$('#remoteLoginError').textContent='';$('#remoteConnect').textContent='正在连接…';
 try{
  const session=await remoteCall(c,'devices/'+encodeURIComponent(c.device.id)+'/remote','POST',body);
  if(!remoteCurrent(c)){api('remote/'+encodeURIComponent(session.id),'DELETE').catch(()=>{});return}
  c.session=session;$('#remotePassword').value='';$('#remoteKey').value='';$('#remotePassphrase').value='';renderRemoteConnected(c);await selectRemoteView(c,c.view);
 }catch(e){if(remoteCurrent(c)&&!c.session){$('#remoteLoginError').textContent=e.message;remoteLoginBusy(c,false);$('#remoteConnect').textContent='连接 SSH'}}
 finally{body.password='';body.privateKey='';body.passphrase=''}
}
function renderRemoteConnected(c){
 $('#remoteContents').innerHTML='<div class="remote-connected"><div class="remote-session-line"><span class="pill on" id="remoteSessionBadge">SSH 已连接</span><code>'+esc(c.session.target)+'</code><span class="muted">SSH 登录会话</span></div><div class="remote-view-tabs"><button type="button" data-remote-view="terminal">'+uiIcon('terminal')+'SSH 终端</button><button type="button" data-remote-view="files">'+uiIcon('folder')+'文件管理</button></div><p class="remote-error" id="remoteSessionError" role="alert"></p><section id="remoteTerminalPanel"><div id="remoteTerminal" class="remote-terminal" aria-label="SSH 交互终端"></div><div class="remote-terminal-tools"><button type="button" id="remoteInterrupt">Ctrl+C 中断</button><button type="button" id="remoteClear">清空显示</button><span>终端中的命令会在当前 SSH 账号下执行</span></div></section><section id="remoteFilesPanel" hidden><div class="remote-file-toolbar"><button type="button" id="remoteUp" aria-label="上一级目录">'+uiIcon('arrow')+'上一级</button><form id="remotePathForm"><input id="remotePath" aria-label="当前远程目录" placeholder="目录路径"><button type="submit">前往</button></form><button type="button" id="remoteFileRefresh">'+uiIcon('refresh')+'刷新</button></div><div class="remote-file-actions"><input id="remoteFileSearch" placeholder="搜索当前目录" aria-label="搜索当前目录"><button type="button" id="remoteMkdir">'+uiIcon('plus')+'新建文件夹</button><button type="button" id="remoteUpload" class="primary">'+uiIcon('upload')+'上传文件</button><input type="file" id="remoteUploadInput" hidden></div><p id="remoteFileStatus" class="muted" role="status"></p><div id="remoteTransfer" hidden></div><div class="tablewrap remote-file-table"><table><thead><tr><th>名称</th><th>大小</th><th>修改时间 / 权限</th><th>操作</th></tr></thead><tbody id="remoteFileRows"></tbody></table></div><div id="remoteDownloadLinks"></div></section></div>';
 $('#remoteDisconnect').hidden=false;$('#remoteFooter').textContent='当前会话凭据仅保存在内存；关闭后释放连接';
 $('#remoteContents').querySelectorAll('[data-remote-view]').forEach(button=>button.onclick=()=>selectRemoteView(c,button.dataset.remoteView));
 $('#remoteInterrupt').onclick=()=>remoteTerminalSend(c,{type:'input',data:'\x03'});
 $('#remoteClear').onclick=()=>c.terminal?.clear();
 $('#remoteUp').onclick=()=>refreshRemoteFiles(c,remoteParent(c.dir));
 $('#remoteFileRefresh').onclick=()=>refreshRemoteFiles(c,c.dir);
 $('#remotePathForm').onsubmit=e=>{e.preventDefault();refreshRemoteFiles(c,$('#remotePath').value)};
 $('#remoteFileSearch').oninput=()=>{c.query=$('#remoteFileSearch').value;renderRemoteFileRows(c)};
 $('#remoteMkdir').onclick=async()=>{const name=await remoteAskName(c,'新建文件夹','');if(name)await remoteFileMutation(c,{action:'mkdir',path:c.dir,name})};
 $('#remoteUpload').onclick=()=>{if(!c.upload)$('#remoteUploadInput').click()};
 $('#remoteUploadInput').onchange=()=>{const file=$('#remoteUploadInput').files[0];$('#remoteUploadInput').value='';if(file)uploadRemoteFile(c,file)};
}
async function selectRemoteView(c,view){
 if(!remoteCurrent(c)||!c.session)return;c.view=view;
 $('#remoteTerminalPanel').hidden=view!=='terminal';$('#remoteFilesPanel').hidden=view!=='files';
 $('#remoteContents').querySelectorAll('[data-remote-view]').forEach(button=>button.classList.toggle('active',button.dataset.remoteView===view));
 try{if(view==='terminal')await startRemoteTerminal(c);else if(!c.dir)await refreshRemoteFiles(c,'')}
 catch(e){if(remoteCurrent(c))$('#remoteSessionError').textContent=e.message}
}
function loadRemoteScript(name){
 return new Promise((resolve,reject)=>{const script=document.createElement('script');script.src='/api/admin/remote-assets/'+name;script.onload=resolve;script.onerror=()=>{script.remove();reject(Error('终端组件加载失败，请重试'))};document.head.appendChild(script)});
}
async function terminalAssets(){
 if(window.Terminal&&window.FitAddon?.FitAddon)return;
 if(!remoteTerminalAssets)remoteTerminalAssets=(async()=>{await loadRemoteScript('xterm.js');await loadRemoteScript('addon-fit.js')})().catch(e=>{remoteTerminalAssets=null;throw e});
 return remoteTerminalAssets;
}
function remoteTerminalSend(c,frame){if(remoteCurrent(c)&&c.socket?.readyState===WebSocket.OPEN)c.socket.send(JSON.stringify(frame))}
async function startRemoteTerminal(c){
 if(c.terminal){c.fit.fit();c.terminal.focus();return}
 await terminalAssets();if(!remoteCurrent(c))return;
 const terminal=new window.Terminal({cursorBlink:true,fontSize:13,fontFamily:'Cascadia Mono,Consolas,monospace',scrollback:2000,screenReaderMode:true,theme:{background:'#101c30',foreground:'#d9e7f8',cursor:'#8aaef0',selectionBackground:'#42649f77'}}),fit=new window.FitAddon.FitAddon();
 c.terminal=terminal;c.fit=fit;terminal.loadAddon(fit);terminal.open($('#remoteTerminal'));fit.fit();
 const address=new URL('/api/admin/remote/'+encodeURIComponent(c.session.id)+'/terminal',location.origin);address.protocol=location.protocol==='https:'?'wss:':'ws:';address.searchParams.set('ticket',c.session.terminalTicket);c.session.terminalTicket='';
 const socket=new WebSocket(address);c.socket=socket;socket.binaryType='arraybuffer';
 socket.onopen=()=>{if(!remoteCurrent(c)){socket.close();return}remoteTerminalSend(c,{type:'resize',cols:terminal.cols,rows:terminal.rows});terminal.focus()};
 socket.onmessage=event=>{if(remoteCurrent(c))terminal.write(typeof event.data==='string'?event.data:new Uint8Array(event.data))};
 socket.onclose=()=>{if(remoteCurrent(c)&&!c.expired){terminal.write('\r\n\x1b[33m终端连接已关闭，请重新连接 SSH。\x1b[0m\r\n');$('#remoteSessionError').textContent='终端已关闭；需要新终端时请断开并重新连接。'}};
 socket.onerror=()=>{if(remoteCurrent(c))$('#remoteSessionError').textContent='终端连接失败，请检查 SSH 服务和反代 WebSocket 配置。'};
 terminal.onData(data=>remoteTerminalSend(c,{type:'input',data}));terminal.onResize(size=>remoteTerminalSend(c,{type:'resize',...size}));
 c.observer=new ResizeObserver(()=>{if(remoteCurrent(c)&&c.view==='terminal')fit.fit()});c.observer.observe($('#remoteTerminal'));
}
async function remoteFileCall(c,body){return remoteCall(c,'remote/'+encodeURIComponent(c.session.id)+'/files','POST',body)}
async function refreshRemoteFiles(c,dir){
 if(!remoteCurrent(c)||c.upload||c.fileBusy)return;c.fileBusy=true;$('#remoteFileStatus').textContent='正在读取目录…';$('#remoteSessionError').textContent='';
 try{const result=await remoteFileCall(c,{action:'list',path:dir});if(!remoteCurrent(c))return;c.dir=result.path;c.home=result.home;c.entries=result.entries;$('#remotePath').value=c.dir;$('#remoteFileStatus').textContent=result.entries.length+' 个项目'+(result.truncated?' · 仅展示前 2000 项':'')+' · 单文件上传最多 4 GB';renderRemoteFileRows(c)}
 catch(e){if(remoteCurrent(c)){$('#remoteSessionError').textContent=e.message;$('#remoteFileStatus').textContent=c.expired?'请重新连接 SSH 后读取目录。':'目录读取失败，可刷新重试。'}}
 finally{c.fileBusy=false}
}
function renderRemoteFileRows(c){
 const files=c.entries.filter(file=>file.name.toLowerCase().includes(c.query.toLowerCase()));
 $('#remoteFileRows').innerHTML=files.map((file,index)=>'<tr><td><button type="button" class="remote-file-name" data-file-action="open" data-file-index="'+index+'" '+(!file.directory&&!file.symlink?'disabled':'')+'>'+uiIcon(file.directory?'folder':'file')+'<span>'+esc(file.name)+'</span>'+(file.symlink?'<small>链接</small>':'')+'</button></td><td>'+esc(file.directory?'文件夹':remoteBytes(file.size))+'</td><td>'+esc(new Date(file.modifiedAt).toLocaleString())+'<small>'+esc(file.mode)+'</small></td><td><div class="actions">'+(!file.directory?'<button type="button" data-file-action="download" data-file-index="'+index+'">下载</button>':'')+'<button type="button" data-file-action="rename" data-file-index="'+index+'">重命名</button><button type="button" class="danger" data-file-action="delete" data-file-index="'+index+'">删除</button></div></td></tr>').join('')||'<tr><td colspan="4" class="empty">当前目录没有匹配文件</td></tr>';
 $('#remoteFileRows').querySelectorAll('[data-file-action]').forEach(button=>button.onclick=()=>remoteFileRowAction(c,button.dataset.fileAction,files[Number(button.dataset.fileIndex)]));
}
async function remoteFileRowAction(c,actionName,file){
 if(!remoteCurrent(c)||c.upload||c.fileBusy||!file)return;
 const target=remoteJoin(c.dir,file.name);
 if(actionName==='open'){await refreshRemoteFiles(c,target);return}
 if(actionName==='rename'){const name=await remoteAskName(c,'重命名',file.name);if(name&&name!==file.name)await remoteFileMutation(c,{action:'rename',path:target,name});return}
 if(actionName==='delete'){if(confirm('永久删除“'+file.name+'”？非空文件夹不会递归删除。'))await remoteFileMutation(c,{action:'delete',path:target});return}
 if(actionName==='download'){
  c.fileBusy=true;$('#remoteSessionError').textContent='';
  try{const result=await remoteFileCall(c,{action:'download',path:target});if(!remoteCurrent(c))return;const url=new URL(result.url,location.origin);if(url.origin!==location.origin)throw Error('下载地址无效');$('#remoteDownloadLinks').innerHTML='<a class="package-link" href="'+esc(url.pathname+url.search)+'" download="'+esc(result.name)+'">点击下载 '+esc(result.name)+'</a><span class="muted">单次下载链接，使用后失效</span>'}
  catch(e){if(remoteCurrent(c))$('#remoteSessionError').textContent=e.message}
  finally{c.fileBusy=false}
 }
}
async function remoteFileMutation(c,body){
 if(!remoteCurrent(c)||c.fileBusy||c.upload)return;c.fileBusy=true;$('#remoteSessionError').textContent='';
 try{await remoteFileCall(c,body);if(remoteCurrent(c)){c.fileBusy=false;await refreshRemoteFiles(c,c.dir)}}
 catch(e){if(remoteCurrent(c))$('#remoteSessionError').textContent=e.message}
 finally{c.fileBusy=false}
}
function remoteAskName(c,title,value){
 if(!remoteCurrent(c))return Promise.resolve(null);
 c.nameResolve?.(null);
 if(!$('#remoteNameForm'))$('#remoteFileStatus').insertAdjacentHTML('beforebegin','<form id="remoteNameForm" class="remote-name-form"><label><span id="remoteNameLabel"></span><input id="remoteNameInput" maxlength="255" required autocomplete="off"></label><div><button type="button" id="remoteNameCancel">取消</button><button type="submit" class="primary">确认</button></div></form>');
 $('#remoteNameForm').hidden=false;$('#remoteNameLabel').textContent=title;$('#remoteNameInput').value=value;$('#remoteNameInput').focus();$('#remoteNameInput').select();
 return new Promise(resolve=>{
  const finish=name=>{if(c.nameResolve===finish)c.nameResolve=null;$('#remoteNameForm').hidden=true;resolve(name)};
  c.nameResolve=finish;$('#remoteNameCancel').onclick=()=>finish(null);$('#remoteNameForm').onsubmit=e=>{e.preventDefault();const name=$('#remoteNameInput').value;if(name.trim())finish(name)};
 });
}
async function uploadRemoteFile(c,file){
 if(!remoteCurrent(c)||c.fileBusy||c.upload)return;
 if(file.size>4*1024**3){$('#remoteSessionError').textContent='单文件上传最多 4 GB';return}
 c.upload=true;let uploadId='',offset=0,canceled=false;
 $('#remoteSessionError').textContent='';$('#remoteTransfer').hidden=false;
 $('#remoteTransfer').innerHTML='<div><strong>'+esc(file.name)+'</strong><span id="remoteUploadProgress">正在准备…</span><button type="button" id="remoteUploadCancel">取消上传</button></div><progress id="remoteUploadBar" max="'+Math.max(1,file.size)+'" value="0"></progress>';
 $('#remoteUploadCancel').onclick=()=>{canceled=true;$('#remoteUploadProgress').textContent='正在取消…'};
 try{
  let replace=false;
  if(c.entries.some(entry=>entry.name===file.name)){if(!confirm('文件“'+file.name+'”已存在，确认完整上传后替换？')){canceled=true;return}replace=true}
  const started=await remoteFileCall(c,{action:'upload-start',path:c.dir,name:file.name,size:file.size,replace});uploadId=started.uploadId;
  while(offset<file.size&&!canceled&&remoteCurrent(c)){
   const buffer=await file.slice(offset,Math.min(offset+256*1024,file.size)).arrayBuffer();
   const part=await remoteFileCall(c,{action:'upload-part',uploadId,offset,data:remoteBase64(buffer)});
   if(!remoteCurrent(c))return;
   offset=part.offset;$('#remoteUploadBar').value=offset;$('#remoteUploadProgress').textContent=Math.floor(offset/Math.max(1,file.size)*100)+'% · '+remoteBytes(offset)+' / '+remoteBytes(file.size);
  }
  if(canceled||!remoteCurrent(c)){if(remoteCurrent(c))await remoteFileCall(c,{action:'upload-cancel',uploadId});return}
  await remoteFileCall(c,{action:'upload-finish',uploadId});uploadId='';
  if(remoteCurrent(c))$('#remoteUploadProgress').textContent='上传完成';
 }catch(e){if(remoteCurrent(c)){$('#remoteSessionError').textContent=e.message;$('#remoteUploadProgress').textContent='上传结果未确认，请检查目标文件后重试'}if(uploadId&&remoteCurrent(c))await remoteFileCall(c,{action:'upload-cancel',uploadId}).catch(()=>{})}
 finally{c.upload=false;if(remoteCurrent(c)){if(canceled)$('#remoteUploadProgress').textContent='上传已取消';$('#remoteUploadCancel').disabled=true;await refreshRemoteFiles(c,c.dir)}}
}
