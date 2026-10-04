(function(){
 'use strict';
 var script=document.currentScript,statusURL=script ? script.getAttribute('data-status-url') : location.pathname.replace(/\/$/,'')+'/status';
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
 }
 if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',initialize);else initialize();
})();
