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
 function refresh(){if(busy||document.hidden)return;busy=true;var button=document.getElementById('rg-refresh');button.disabled=true;button.textContent='刷新中…';fetch(statusURL,{credentials:'same-origin',cache:'no-store'}).then(function(r){if(!r.ok)throw Error('请重新登录路由器后台');return r.json()}).then(function(s){var badge=document.getElementById('rg-state');badge.classList.toggle('is-running',s.running);badge.replaceChildren();badge.appendChild(document.createElement('i'));badge.appendChild(document.createTextNode(s.running?'客户端运行中':s.enabled?'等待客户端启动':'服务未启用'));document.getElementById('rg-server').textContent=s.server||'尚未配置';document.getElementById('rg-device').textContent=s.deviceId||'尚未绑定';document.getElementById('rg-status-note').textContent=!s.configured?'请先填写服务器地址、设备 ID 和令牌，保存并应用后连接。':'客户端运行状态与隧道在线状态不同；可在管理后台查看设备是否在线。';consoleLink(s.server);message('')}).catch(function(e){message('状态读取失败：'+e.message)}).finally(function(){busy=false;button.disabled=false;button.textContent='↻ 刷新状态'})}
 document.getElementById('rg-refresh').addEventListener('click',refresh);
 window.setInterval(refresh,10000);
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
