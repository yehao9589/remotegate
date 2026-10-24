let authTransition=0;
async function authRequest(action,body,options={}){const response=await fetch('/api/auth/'+action,{method:body===undefined?'GET':'POST',credentials:'same-origin',cache:'no-store',signal:options.signal,headers:{'Content-Type':'application/json','X-Admin-Entry':location.pathname.replace(/\/$/,'')||'/'},body:body===undefined?undefined:JSON.stringify(body)});if(!response.ok){const err=new Error((await response.text()).trim()||'请求失败');err.status=response.status;throw err}return response.status===204?null:response.json()}
function showAuthPanel(panel,message=''){
 authTransition++;
 document.querySelectorAll('dialog[open]').forEach(dialog=>dialog.close());
 $('#authPending').hidden=true;$('#authScreen').hidden=false;$('#shell').hidden=true;
 ['firstInstall','login'].forEach(id=>$('#'+id).hidden=id!==panel);
 document.body.classList.add('auth-visible');
 if(panel==='login'){$('#password').value='';$('#loginerror').textContent=message;$('#username').focus()}
 if(panel==='firstInstall')$('#setupUsername').focus();
}
function enterAccount(result){
 if(result?.role==='user'){
  const request=new URLSearchParams(location.search).get('access_request');
  location.replace(/^[a-f0-9]{64}$/.test(request||'')?'/.remotegate/access/authorize?request='+encodeURIComponent(request):'/user/devices');return;
 }
 return enterAdmin(result?.entryPath);
}
async function enterAdmin(entryPath){
 const transition=++authTransition;
 $('.auth-wordmark').href=entryPath||location.pathname;
 if(entryPath&&location.pathname!==entryPath)history.replaceState(null,'',entryPath);
 $('#authPending').hidden=true;$('#authScreen').hidden=true;$('#shell').hidden=false;document.body.classList.remove('auth-visible');
 $('#password').value='';$('#setupForm').reset();
 $('#content').setAttribute('aria-busy','true');
 $('#stats').innerHTML='';$('#sync').textContent='正在连接服务器…';
 $('#content').classList.remove('workbench','domain-center');
 $('#content').innerHTML='<div class="console-loading" role="status">正在加载设备…</div>';
 try{await refresh()}
 catch(e){
  if(transition!==authTransition)return;
  if(e.status===401){showAuthPanel('login',e.message);return}
  $('#sync').textContent='连接服务器失败';
  $('#content').innerHTML='<div class="console-load-error" role="alert"><h2>连接服务器失败</h2><p>暂时无法加载后台数据，请检查网络或稍后重试。</p><button type="button" id="consoleRetry" class="primary">重新连接</button></div>';
  $('#consoleRetry').onclick=()=>enterAdmin(entryPath);
 }finally{if(transition===authTransition)$('#content').setAttribute('aria-busy','false')}
}
async function initializeAuth(){
 $('.auth-wordmark').href=location.pathname;
 try{sessionStorage.removeItem('adminUsername');sessionStorage.removeItem('adminPassword')}catch(e){}
 const initial=JSON.parse($('#authInitialState').textContent);
 if(initial?.panel==='firstInstall'){showAuthPanel('firstInstall');return}
 if(initial?.panel==='workbench'){await enterAdmin(initial.entryPath);return}
 const transition=++authTransition;
 $('#authScreen').hidden=true;$('#shell').hidden=true;$('#authPending').hidden=false;
 $('#firstInstall').hidden=true;$('#login').hidden=true;
 document.body.classList.remove('auth-visible');
 // A cached login document or the first navigation after an HTTP/HTTPS redirect
 // can disagree with the current cookie. Restore only a server-validated session.
 const controller=new AbortController();
 const timeout=setTimeout(()=>controller.abort(),8000);
 let status;
 try{
  status=await authRequest('status',undefined,{signal:controller.signal});
 }catch(e){/* A failed or timed-out check must still leave a usable login form. */}
 finally{clearTimeout(timeout)}
 if(transition!==authTransition)return;
 if(status?.authenticated)await enterAccount(status);
 else showAuthPanel('login');
}
async function loginWithAccount(){authTransition++;const button=$('#loginSubmit');button.disabled=true;$('#loginerror').textContent='';try{const result=await authRequest('login',{username:$('#username').value.trim(),password:$('#password').value});await enterAccount(result)}catch(e){$('#loginerror').textContent=e.message}finally{button.disabled=false}}
async function logoutAccount(){authTransition++;try{await authRequest('logout',{});showAuthPanel('login')}catch(e){toast('退出失败：'+e.message)}}
$('#setupShowPassword').onchange=e=>{const type=e.target.checked?'text':'password';$('#setupPassword').type=type;$('#setupConfirm').type=type};
$('#setupForm').onsubmit=async e=>{e.preventDefault();const password=$('#setupPassword').value,confirmPassword=$('#setupConfirm').value,button=$('#setupSubmit');$('#setupError').textContent='';if(password!==confirmPassword){$('#setupError').textContent='两次输入的密码不一致';$('#setupConfirm').focus();return}button.disabled=true;button.textContent='正在创建管理员…';try{const username=$('#setupUsername').value.trim();const result=await authRequest('setup',{username,password,confirmPassword,entryPath:$('#setupEntry').value.trim()});$('#setupForm').reset();$('#setupPassword').type='password';$('#setupConfirm').type='password';$('#username').value=username;await enterAdmin(result.entryPath);toast('安装完成，请收藏当前后台地址')}catch(err){if(err.status===409){showAuthPanel('login');$('#loginerror').textContent='管理员已创建，请使用已有账号登录'}else $('#setupError').textContent=err.message}finally{button.disabled=false;button.textContent='创建管理员并进入后台 →'}};

function updateSetupEntryPreview(){const value=$("#setupEntry").value.trim();$("#setupEntryPreview").textContent="安装后的后台地址："+location.origin+(value.startsWith("/")?value:"/"+value)}
$("#setupEntry").oninput=updateSetupEntryPreview;updateSetupEntryPreview();
