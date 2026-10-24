names.security='账号与授权';
let settingsTab='account',settingsGeneration=0,settingsOwner=-1;
function settingsCurrent(generation,owner){return page==='security'&&generation===settingsGeneration&&owner===authTransition&&Boolean($('#settingsPane'))}
const renderBeforeSecurity=render;
render=()=>{renderBeforeSecurity();if(page!=='security')return;$('#pageDescription').textContent='管理账号安全、普通用户设备授权和邮件通知。';renderSecurity()};
async function renderSecurity(){
 if(settingsOwner!==authTransition){settingsOwner=authTransition;settingsTab='account'}
 const generation=++settingsGeneration,owner=authTransition;
 $('#content').classList.add('account-settings');
 $('#content').innerHTML=`<div class="settings-tabs" role="group" aria-label="设置分类">${[['account','账号安全'],['users','普通用户'],['mail','邮件通知']].map(([key,label])=>`<button type="button" data-settings-tab="${key}" aria-pressed="${settingsTab===key}">${uiIcon(key==='mail'?'mail':key==='users'?'users':'shield')}${label}</button>`).join('')}</div><div id="settingsPane"><p class="muted">正在读取设置…</p></div>`;
 document.querySelectorAll('[data-settings-tab]').forEach(button=>button.onclick=()=>{settingsTab=button.dataset.settingsTab;void renderSecurity()});
 if(settingsTab==='users'){await renderUserSettings(generation,owner);return}
 if(settingsTab==='mail'){await renderMailSettings(generation,owner);return}
 try{
 const s=await api('security');if(!settingsCurrent(generation,owner))return;
 $('#settingsPane').innerHTML=`<div class="security-account"><span class="pill on">超级管理员</span><strong>${esc(s.username)}</strong><p class="muted">修改入口或密码后，所有已登录会话退出，需要重新登录。</p></div><div class="security-grid"><section><h2>${uiIcon('shield')}后台入口</h2><p class="muted">入口是网址中的路径，例如 /my-panel。保存前请记住新地址。</p><form id="entrySettings"><label>入口路径<input name="entryPath" value="${esc(s.entryPath)}" required maxlength="65" placeholder="/my-panel" autocomplete="off"></label><p class="security-url" id="entryPreview"></p><label>当前管理员密码<input name="currentPassword" type="password" required autocomplete="current-password"></label><p class="danger" role="alert" id="entryError"></p><button class="primary" type="submit">保存入口并重新登录</button></form></section><section><h2>${uiIcon('key')}修改管理员密码</h2><p class="muted">密码至少 8 个字符，保存后原密码失效。</p><form id="passwordSettings"><label>当前密码<input name="currentPassword" type="password" required autocomplete="current-password"></label><label>新密码<input name="newPassword" type="password" required minlength="8" maxlength="72" autocomplete="new-password"></label><label>确认新密码<input name="confirmPassword" type="password" required minlength="8" maxlength="72" autocomplete="new-password"></label><p class="danger" role="alert" id="passwordError"></p><button class="primary" type="submit">更新密码并重新登录</button></form></section></div>`;
 const preview=()=>{const v=$('#entrySettings [name=entryPath]').value.trim();$('#entryPreview').textContent='新后台地址：'+location.origin+(v.startsWith('/')?v:'/'+v)};$('#entrySettings [name=entryPath]').oninput=preview;preview();
 $('#entrySettings').onsubmit=e=>saveSecurity(e,'entry','entryError');$('#passwordSettings').onsubmit=e=>saveSecurity(e,'password','passwordError');
 }catch(e){if(settingsCurrent(generation,owner))$('#settingsPane').innerHTML=`<p class="danger" role="alert">${esc(e.message)}</p>`}
}
async function saveSecurity(e,action,errorId){
 e.preventDefault();const form=e.target,button=form.querySelector('button'),fields=Object.fromEntries(new FormData(form));$('#'+errorId).textContent='';
 if(action==='password'&&fields.newPassword!==fields.confirmPassword){$('#'+errorId).textContent='两次输入的新密码不一致';return}
 if(action==='entry'&&!confirm('新后台地址为 '+$('#entryPreview').textContent.replace('新后台地址：','')+'，请记住此地址。保存后所有会话将退出，确认修改？'))return;
 button.disabled=true;try{const result=await api('security','POST',{action,...fields});form.reset();location.assign(result.entryPath)}catch(error){$('#'+errorId).textContent=error.message;button.disabled=false}
}
