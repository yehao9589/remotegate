let mailTemplateEditor=null,mailTemplateWorkspace=null;
function mailTemplateWorkspaceCurrent(w){return !!w&&mailTemplateWorkspace===w&&w.owner===authTransition&&(w.modal?mailPreviewDialogCurrent(w.modal):page==='mail-preview')}
function clearMailTemplateEditor(){const c=mailTemplateEditor;if(!c)return;clearTimeout(c.timer);c.controller?.abort();c.saveController?.abort();c.sequence++;c.subject.value='';c.body.value='';mailTemplateEditor=null}
function clearMailTemplateWorkspace(){clearMailTemplateEditor();mailTemplateWorkspace=null}
function mailTemplateEditorCurrent(c){return mailTemplateEditor===c&&mailTemplateWorkspaceCurrent(c.workspace)}
function mailTemplateDirty(w,kind){const d=w.drafts[kind],s=w.data.templates[kind];return !!d&&(d.subject!==s.subject||d.body!==s.body)}
function updateMailTemplateStatus(c){
 if(!mailTemplateEditorCurrent(c))return;const dirty=mailTemplateDirty(c.workspace,c.kind);
 $('#mailTemplateDirty').textContent=c.saving?'正在保存…':dirty?'有未保存修改':'已保存';$('#mailTemplateSave').disabled=c.saving||!dirty;$('#mailTemplateDiscard').disabled=c.saving||!dirty;
 $('#mailTemplateSave').textContent=c.saving?'正在保存…':c.saved&&!dirty?'已保存':'保存模板';$('#mailTemplateForm').setAttribute('aria-busy',String(c.saving));
 for(const [kind] of mailPreviewKinds){const dot=$('[data-mail-draft-dot="'+kind+'"]');if(dot)dot.hidden=!mailTemplateDirty(c.workspace,kind)}
}
function mountMailTemplateEditor(w){
 const kind=mailPreviewKind,label=mailPreviewKinds.find(([key])=>key===kind)?.[1]||'通知';
 const draft=w.drafts[kind]||(w.drafts[kind]={...w.data.templates[kind]});
 $('#mailComposerArea').innerHTML=`<form id="mailTemplateForm"><div class="mail-composer-columns"><section class="mail-template-fields"><div class="mail-composer-section-heading"><h3>编辑内容 <span>${esc(label)}</span></h3><span id="mailTemplateDirty" aria-live="polite"></span></div><label for="mailTemplateSubject">邮件标题 <small>最多 200 字</small></label><input id="mailTemplateSubject" maxlength="200" required autocomplete="off"><label for="mailTemplateBody">正文模板 <small>最多 10000 字</small></label><textarea id="mailTemplateBody" rows="10" maxlength="10000" required spellcheck="false"></textarea><details class="mail-template-variable-panel"><summary>插入变量 <span>设备名称、状态、时间等</span></summary><div class="mail-template-variables">${(w.data.variables||[]).map(v=>`<button type="button" data-mail-variable="${esc(v.name)}" title="{{${esc(v.name)}}}">${esc(v.label)}</button>`).join('')}</div><p>点击插入到光标处；不适用的变量显示为“—”。</p></details></section><section class="mail-composer-preview"><div class="mail-composer-section-heading"><h3>实时预览</h3><span id="mailTemplatePreviewStatus" aria-live="polite">正在预览…</span></div><div id="mailPreviewContent" aria-live="polite"></div><p class="mail-composer-preview-note">预览与实际发送共用同一套模板。示例数据会替换为实际记录，发件人与收件邮箱来自已保存设置。</p></section></div><p class="mail-template-feedback" role="status" id="mailTemplateFeedback"></p><p class="danger" role="alert" id="mailTemplateError"></p><div class="mail-template-footer"><button type="button" id="mailTemplateReset">恢复默认</button><span>切换类型保留草稿，关闭后放弃。仅保存当前类型。</span><button type="button" id="mailTemplateDiscard">撤销修改</button><button type="submit" id="mailTemplateSave" class="primary">保存模板</button><button type="button" id="mailTemplateDone">完成</button></div></form>`;
 const c={workspace:w,kind,sequence:0,controller:null,saveController:null,timer:null,target:null,saving:false,saved:false,subject:$('#mailTemplateSubject'),body:$('#mailTemplateBody')};mailTemplateEditor=c;
 const {subject,body}=c;subject.value=draft.subject;body.value=draft.body;c.target=body;
 const change=()=>{if(!mailTemplateEditorCurrent(c)||c.saving)return;w.drafts[kind]={subject:subject.value,body:body.value};c.controller?.abort();c.sequence++;clearTimeout(c.timer);$('#mailTemplateFeedback').textContent='';updateMailTemplateStatus(c);$('#mailTemplatePreviewStatus').textContent='等待更新…';c.timer=setTimeout(()=>void previewMailTemplate(c),350)};
 for(const input of [subject,body]){input.onfocus=()=>c.target=input;input.oninput=change}
 $('#mailComposerArea').querySelectorAll('[data-mail-variable]').forEach(button=>{button.onmousedown=e=>e.preventDefault();button.onclick=()=>{const input=c.target||body,token='{{'+button.dataset.mailVariable+'}}',start=input.selectionStart??input.value.length,end=input.selectionEnd??start;if(input.value.length-(end-start)+token.length>input.maxLength){$('#mailTemplateError').textContent='已达到字段长度上限';return}input.setRangeText(token,start,end,'end');input.focus();change()}});
 $('#mailTemplateReset').onclick=()=>{subject.value=w.data.defaults[kind].subject;body.value=w.data.defaults[kind].body;$('#mailTemplateError').textContent='';change()};
 $('#mailTemplateDiscard').onclick=()=>{subject.value=w.data.templates[kind].subject;body.value=w.data.templates[kind].body;$('#mailTemplateError').textContent='';change()};
 $('#mailTemplateDone').onclick=w.modal?closeMailPreviewDialog:returnToMailSettings;
 $('#mailTemplateForm').onsubmit=async e=>{
  e.preventDefault();if(!mailTemplateEditorCurrent(c)||c.saving||!mailTemplateDirty(w,kind))return;c.saving=true;clearTimeout(c.timer);c.controller?.abort();c.sequence++;
  let saved=false;const controller=new AbortController();c.saveController=controller;const timeout=setTimeout(()=>controller.abort(),15000);
  const controls=[subject,body,$('#mailTemplateReset'),$('#mailTemplateDiscard'),$('#mailPreviewBack'),$('#mailTemplateDone'),...document.querySelectorAll('[data-mail-preview-kind],[data-mail-variable]')];controls.forEach(input=>input.disabled=true);updateMailTemplateStatus(c);$('#mailTemplateError').textContent='';$('#mailTemplateFeedback').textContent='';
  try{const result=await api('mail/templates','POST',{action:'save',kind,template:{subject:subject.value,body:body.value}},{signal:controller.signal});if(!mailTemplateEditorCurrent(c))return;w.data.templates[kind]=result.templates[kind];w.data.custom[kind]=result.custom[kind];w.drafts[kind]={...result.templates[kind]};subject.value=w.drafts[kind].subject;body.value=w.drafts[kind].body;saved=c.saved=true;$('#mailTemplateFeedback').textContent=label+'模板已保存，可继续编辑或点击“完成”关闭。'}
  catch(error){if(mailTemplateEditorCurrent(c))$('#mailTemplateError').textContent=error.name==='AbortError'?'保存超时，结果尚未确认。草稿已保留，请重新打开核对。':error.message}
  finally{clearTimeout(timeout);c.saveController=null;c.saving=false;controls.forEach(input=>input.disabled=false);if(mailTemplateEditorCurrent(c)){updateMailTemplateStatus(c);if(saved){$('#mailTemplateDone').focus();void previewMailTemplate(c)}}}
 };
 updateMailTemplateStatus(c);return previewMailTemplate(c);
}
async function previewMailTemplate(c){
 if(!mailTemplateEditorCurrent(c))return;c.controller?.abort();const controller=new AbortController(),sequence=++c.sequence;c.controller=controller;const timeout=setTimeout(()=>controller.abort(),15000);
 const current=()=>mailTemplateEditorCurrent(c)&&!c.saving&&sequence===c.sequence;
 $('#mailTemplatePreviewStatus').textContent='正在预览…';
 try{const data=await api('mail/preview','POST',{kind:c.kind,template:{subject:c.subject.value,body:c.body.value}},{signal:controller.signal});if(!current())return;drawMailPreview(data);$('#mailTemplatePreviewStatus').textContent=mailTemplateDirty(c.workspace,c.kind)?'未保存的示例':'已保存的示例';$('#mailTemplateError').textContent=''}
 catch(error){if(current()){$('#mailTemplatePreviewStatus').textContent='预览未更新';$('#mailTemplateError').textContent=error.name==='AbortError'?'预览超时，请稍后修改或重试':error.message;$('#mailPreviewContent').textContent='当前内容尚未完成预览，请修改后重试。'}}
 finally{clearTimeout(timeout)}
}
