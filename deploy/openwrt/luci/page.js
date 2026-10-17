function remotegateConnectionView(status){
 var connection=status.connection||{},state=connection.state||(!status.enabled?'disabled':!status.running?'stopped':'unknown');
 var labels={connected:'已连接服务器',connecting:'正在连接',reconnecting:'连接已断开 · 重连中',disabled:'未启用',stopped:'客户端未运行',unconfigured:'连接配置未完成',unknown:'等待连接状态上报',unavailable:'连接状态读取失败'};
 function time(value){var date=value&&new Date(value);return date&&!isNaN(date.getTime())?date.toLocaleString('zh-CN',{hour12:false}):'尚未成功连接'}
 var note=state==='connected'?(connection.dnsMode==='encrypted'?'检测到 Fake-IP 或系统解析失败，已通过加密 DNS 获取真实地址并建立直连。':'隧道已建立，连接状态由客户端实际握手与心跳上报。'):state==='unconfigured'?'请保存服务器地址、设备 ID 和令牌后启用服务。':state==='disabled'?'开启远程访问并保存应用后连接。':state==='stopped'?'服务已启用，但客户端进程未运行，请检查路由器系统日志。':state==='unavailable'?'暂时无法确认隧道状态，请检查路由器连接或重新登录。':state==='unknown'?'客户端进程正在运行，等待真实连接状态上报。':'客户端会自动重试，无需重复安装或重新添加设备。';
 if(state==='connected'&&connection.serverIP)note+=' 直连地址：'+connection.serverIP;
 if(state==='reconnecting'&&connection.nextRetryAt)note+=' 下次重试：'+time(connection.nextRetryAt)+'。';
 return {state:state,label:labels[state]||labels.unknown,className:state==='connected'?'is-running':state==='reconnecting'||state==='connecting'?'is-retrying':'',connectedAt:time(connection.connectedAt),note:note,error:state==='connected'||state==='disabled'?'':connection.lastError||''};
}
(function(){
 'use strict';
 var script=document.currentScript,statusURL=script ? script.getAttribute('data-status-url') : location.pathname.replace(/\/$/,'')+'/status';
 var updateURLs={check:script&&script.getAttribute('data-update-check'),start:script&&script.getAttribute('data-update-start'),status:script&&script.getAttribute('data-update-status')},csrfToken=script&&script.getAttribute('data-token');
 function initialize(){
 var root=document.getElementById('cbi-remotegate');if(!root)return;
 var busy=false;
 document.body.classList.add('rg-page');
 function switchTab(name){root.querySelectorAll('[data-rg-tab]').forEach(function(b){b.setAttribute('aria-pressed',b.getAttribute('data-rg-tab')===name?'true':'false')});document.getElementById('rg-basic').hidden=name!=='basic';document.getElementById('rg-advanced').hidden=name!=='advanced';}
 root.querySelectorAll('[data-rg-tab]').forEach(function(b){b.addEventListener('click',function(){switchTab(b.getAttribute('data-rg-tab'))})});
 var advanced=document.getElementById('cbi-remotegate-main-interface');if(advanced)document.getElementById('rg-advanced-fields').appendChild(advanced);
 document.getElementById('rg-start').addEventListener('click',function(){switchTab('basic');document.getElementById('rg-guide').open=false;root.querySelector('.rg-config-heading').scrollIntoView({behavior:'smooth',block:'start'});var field=root.querySelector('[id="widget.cbid.remotegate.main.server"],input[name="cbid.remotegate.main.server"]');if(field)field.focus()});
 function consoleLink(address){var parsed;try{parsed=new URL(address);if(!/^https?:$/.test(parsed.protocol)||parsed.username||parsed.password)throw Error('bad URL');if(parsed.hostname==='10.0.2.2'&&/^(localhost|127\.0\.0\.1)$/.test(location.hostname))parsed.hostname=location.hostname}catch(e){parsed=null}root.querySelectorAll('.rg-console').forEach(function(a){a.hidden=!parsed;if(parsed)a.href=parsed.origin+'/'})}
 consoleLink(document.getElementById('rg-server').textContent.trim());
 function message(text){var el=document.getElementById('rg-message');el.textContent=text;el.hidden=!text}
 function renderConnection(s){var view=remotegateConnectionView(s),badge=document.getElementById('rg-state');badge.className=view.className;badge.replaceChildren();badge.appendChild(document.createElement('i'));badge.appendChild(document.createTextNode(view.label));if(view.state!=='unavailable'){document.getElementById('rg-server').textContent=s.server||'尚未配置';document.getElementById('rg-device').textContent=s.deviceId||'尚未绑定';document.getElementById('rg-connection').textContent=view.connectedAt;consoleLink(s.server)}document.getElementById('rg-status-note').textContent=view.note;var error=document.getElementById('rg-connection-error');error.textContent=view.error;error.hidden=!view.error}
 function refresh(){if(busy||document.hidden)return;busy=true;var button=document.getElementById('rg-refresh');button.disabled=true;button.textContent='刷新中…';var controller=new AbortController(),timeout=setTimeout(function(){controller.abort()},10000);fetch(statusURL,{credentials:'same-origin',cache:'no-store',signal:controller.signal}).then(function(r){if(!r.ok)throw Error('请重新登录路由器后台');return r.json()}).then(function(s){renderConnection(s);message('')}).catch(function(e){renderConnection({connection:{state:'unavailable'}});message('状态读取失败：'+(e.name==='AbortError'?'请求超时，请检查网络后重试':e.message))}).finally(function(){clearTimeout(timeout);busy=false;button.disabled=false;button.textContent='↻ 刷新状态'})}
 document.getElementById('rg-refresh').addEventListener('click',refresh);
 window.setInterval(refresh,5000);
 refresh();
 document.addEventListener('visibilitychange',function(){if(!document.hidden)refresh()});
 var updateBusy=false,updateView=null,updateTimer=null;
 var checkButton=document.getElementById('rg-update-check'),startButton=document.getElementById('rg-update-start'),updateText=document.getElementById('rg-update-state');
 function activeUpdate(view){return !!view&&['checking','downloading','installing','restarting'].indexOf((view.state||{}).phase)>=0}
 function renderUpdate(view){
  updateView=view;var state=view.state||{},active=activeUpdate(view);
  checkButton.disabled=updateBusy||active;startButton.disabled=updateBusy||active;startButton.hidden=!view.available||active||!!view.error;
  var target=view.manifest&&view.manifest.packageVersion;
  updateText.textContent=view.error||(active||state.phase==='failed'?state.message:target&&view.available?'发现新版 '+target+' · 当前 '+view.installedVersion:state.message|| (target?'当前 '+view.installedVersion+'，无需更新':'点击“检查更新”对比服务器提供的插件版本。'));
  updateText.classList.toggle('is-error',!!view.error||state.phase==='failed');
  if(view.installedVersion)document.getElementById('rg-plugin-version').textContent='插件 '+view.installedVersion;
  clearTimeout(updateTimer);if(active)updateTimer=setTimeout(function(){updateRequest('status')},3000);
 }
 function updateRequest(kind){
  if(updateBusy)return Promise.resolve();var expected=updateView&&updateView.manifest;
  if(kind==='start'&&(!expected||!confirm('更新到 '+expected.packageVersion+'？连接会暂时中断，现有设备和连接配置会保留。')))return Promise.resolve();
  updateBusy=true;checkButton.disabled=true;startButton.disabled=true;
  var controller=new AbortController(),timeout=setTimeout(function(){controller.abort()},25000),options={credentials:'same-origin',cache:'no-store',signal:controller.signal};
  if(kind==='start'){options.method='POST';options.headers={'Content-Type':'application/x-www-form-urlencoded'};options.body=new URLSearchParams({token:csrfToken||'',version:expected.packageVersion,sha256:expected.sha256}).toString()}
  if(kind!=='status')updateText.textContent=kind==='start'?'正在发起更新…':'正在检查更新…';
  return fetch(updateURLs[kind],options).then(function(r){if(!r.ok)throw Error(r.status===403?'登录已过期，请重新登录路由器后台':'更新请求失败（HTTP '+r.status+'）');return r.json()}).then(function(view){updateBusy=false;renderUpdate(view)}).catch(function(e){updateBusy=false;var view=Object.assign({},updateView||{},{error:e.name==='AbortError'?'请求超时，请稍后检查更新状态':e.message});renderUpdate(view)}).finally(function(){clearTimeout(timeout);updateBusy=false});
 }
 if(checkButton&&startButton){checkButton.addEventListener('click',function(){updateRequest('check')});startButton.addEventListener('click',function(){updateRequest('start')});updateRequest('status')}
 window.addEventListener('pagehide',function(){clearTimeout(updateTimer)});
 }
 if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',initialize);else initialize();
})();
