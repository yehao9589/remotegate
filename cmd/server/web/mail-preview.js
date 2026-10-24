// Preview and editing share one workspace and the server's outgoing composer.
names['mail-preview']='邮件模板与预览';
consolePageVisuals['mail-preview']={icon:'mail',caption:'EMAIL TEMPLATES'};
let mailPreviewKind='offline',mailPreviewGeneration=0,mailPreviewDialog=null;
const mailPreviewKinds=[['offline','设备离线','network'],['certificate','证书到期','shield'],['update','更新失败','package'],['recovery','恢复通知','check'],['test','测试邮件','mail']];
function mailPreviewDialogCurrent(c){return !!c&&mailPreviewDialog===c&&c.owner===authTransition&&c.page===page&&c.dialog.open}
function closeMailPreviewDialog(){mailPreviewDialog?.dialog.close()}
function openMailPreviewDialog(){
 if(mailPreviewDialogCurrent(mailPreviewDialog))return;
 let dialog=$('#mailTemplateDialog');if(!dialog){document.body.insertAdjacentHTML('beforeend','<dialog id="mailTemplateDialog"></dialog>');dialog=$('#mailTemplateDialog')}
 clearMailTemplateWorkspace();const c={dialog,owner:authTransition,page};mailPreviewDialog=c;
 dialog.onclose=()=>{mailPreviewGeneration++;if(mailPreviewDialog===c)mailPreviewDialog=null;clearMailTemplateWorkspace();dialog.replaceChildren()};
 dialog.oncancel=e=>{if(mailTemplateEditor?.saving)e.preventDefault()};dialog.showModal();void renderMailPreview();
}
function openMailPreview(inDialog=false){if(inDialog){openMailPreviewDialog();return}page='mail-preview';if(location.hash!=='#/mail-preview')location.hash='/mail-preview';render();window.scrollTo(0,0)}
function returnToMailSettings(){clearMailTemplateWorkspace();settingsTab='mail';settingsOwner=authTransition;page='security';history.replaceState(null,'',location.pathname+location.search);render();window.scrollTo(0,0)}
async function renderMailPreview(){
 if(mailTemplateEditor?.saving&&mailTemplateWorkspaceCurrent(mailTemplateWorkspace))return;
 const modal=mailPreviewDialogCurrent(mailPreviewDialog)?mailPreviewDialog:null,generation=++mailPreviewGeneration;
 if(!mailTemplateWorkspaceCurrent(mailTemplateWorkspace))clearMailTemplateWorkspace();
 const w=mailTemplateWorkspace||(mailTemplateWorkspace={owner:authTransition,modal,data:null,loading:null,drafts:{}});
 clearMailTemplateEditor();let container=modal?.dialog||$('#content');
 if(modal){container.className='mail-preview-dialog';container.setAttribute('aria-label','邮件模板与预览')}
 else{container.classList.add('mail-preview-page');$('#stats').innerHTML='';$('#pageDescription').textContent='编辑通知内容，实时查看邮件效果。'}
 container.innerHTML=`<div class="mail-preview-toolbar"><div class="mail-preview-heading"><div><h2>邮件模板与预览</h2><p>修改左侧内容，右侧实时预览。保存后用于后续通知。</p></div></div><div class="mail-preview-tools"><span class="mail-preview-example"><i></i>示例数据 · 不会发送</span><button type="button" id="mailPreviewBack" aria-label="${modal?'关闭邮件预览':'返回邮件设置'}">${uiIcon(modal?'close':'arrow')}</button></div></div><div class="mail-composer-types" role="group" aria-label="邮件通知类型">${mailPreviewKinds.map(([kind,label,icon])=>`<button type="button" data-mail-preview-kind="${kind}" aria-pressed="${kind===mailPreviewKind}">${uiIcon(icon)}${label}<span data-mail-draft-dot="${kind}" hidden aria-label="有未保存修改"></span></button>`).join('')}</div><div id="mailComposerArea"><div class="mail-preview-loading">正在读取邮件模板…</div></div>`;
 $('#mailPreviewBack').onclick=modal?closeMailPreviewDialog:returnToMailSettings;
 container.querySelectorAll('[data-mail-preview-kind]').forEach(b=>b.onclick=()=>{if(mailTemplateEditor?.saving)return;mailPreviewKind=b.dataset.mailPreviewKind;void renderMailPreview()});
 const current=()=>mailTemplateWorkspaceCurrent(w)&&generation===mailPreviewGeneration;
 try{if(!w.data){w.loading??=api('mail/templates');w.data=await w.loading}if(!current())return;return await mountMailTemplateEditor(w)}
 catch(error){w.loading=null;if(!current())return;$('#mailComposerArea').innerHTML=`<div class="mail-preview-failure"><p>${esc(error.message||'模板暂时无法加载')}</p><button id="mailPreviewRetry" type="button">重新加载</button></div>`;$('#mailPreviewRetry').onclick=renderMailPreview}
}
function drawMailPreview(data){
 const label=mailPreviewKinds.find(([kind])=>kind===data.kind)?.[1]||'邮件通知';
 $('#mailPreviewContent').innerHTML=`<article class="mail-preview-envelope"><div class="mail-preview-message-header"><div class="mail-preview-sender"><span class="mail-preview-avatar">R</span><div><strong>RemoteGate</strong><span>${esc(data.from||'通知发件人')}</span></div></div><span class="mail-preview-kind ${data.kind==='recovery'?'is-recovery':''}">${esc(label)}</span></div><div class="mail-preview-subject"><h3>${esc(data.subject)}</h3><div class="mail-preview-metadata"><span>${Number(data.recipientCount)>0?'隐藏收件人 · 共 '+Number(data.recipientCount)+' 个邮箱':'收件邮箱尚未配置'}</span><span>${esc(deviceTime(data.generatedAt))}</span></div></div><div class="mail-preview-paper"><pre>${esc(data.body)}</pre></div><div class="mail-preview-message-footer"><span>${uiIcon('file')}纯文本邮件</span><span>示例预览 · 不会发送</span></div></article>`;
}
if(location.hash==='#/mail-preview')page='mail-preview';
window.addEventListener('hashchange',()=>{if($('#shell').hidden)return;if(mailPreviewDialog)closeMailPreviewDialog();if(location.hash==='#/mail-preview'){page='mail-preview';render()}else if(page==='mail-preview'){clearMailTemplateWorkspace();page='security';settingsTab='mail';render()}});
document.querySelectorAll('nav [data-page]').forEach(b=>b.addEventListener('click',()=>{if(location.hash==='#/mail-preview'){clearMailTemplateWorkspace();history.replaceState(null,'',location.pathname+location.search)}}));
