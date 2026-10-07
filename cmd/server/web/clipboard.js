const copyButtonTimers=new WeakMap();

function copyWithSelection(text){
 const focused=document.activeElement,selection=window.getSelection(),ranges=[];
 if(selection)for(let i=0;i<selection.rangeCount;i++)ranges.push(selection.getRangeAt(i).cloneRange());
 const inputSelection=focused&&typeof focused.selectionStart==='number'?{start:focused.selectionStart,end:focused.selectionEnd,direction:focused.selectionDirection}:null;
 const field=document.createElement('textarea');
 field.value=text;field.readOnly=true;field.tabIndex=-1;
 field.style.cssText='position:fixed;top:0;left:0;width:1px;height:1px;padding:0;border:0;opacity:0;pointer-events:none';
 // Controls outside a modal dialog are inert, so select inside the active dialog.
 const parent=focused?.closest('dialog[open]')||document.querySelector('dialog[open]')||document.body;
 try{
  parent.appendChild(field);field.focus({preventScroll:true});field.select();field.setSelectionRange(0,text.length);
  return typeof document.execCommand==='function'&&document.execCommand('copy')===true;
 }finally{
  field.remove();
  if(focused?.isConnected){focused.focus({preventScroll:true});if(inputSelection)focused.setSelectionRange(inputSelection.start,inputSelection.end,inputSelection.direction)}
  if(selection){selection.removeAllRanges();for(const range of ranges)selection.addRange(range)}
 }
}

function selectCopySource(source){
 if(!source?.isConnected)return false;
 try{
  if(typeof source.select==='function'){source.focus({preventScroll:true});source.select()}
  else{const range=document.createRange();range.selectNodeContents(source);const selection=window.getSelection();selection.removeAllRanges();selection.addRange(range)}
  return true;
 }catch{return false}
}

async function copyText(text,{button,source,status,success='已复制'}={}){
 const prior=button&&copyButtonTimers.get(button),label=prior?.label??button?.innerHTML,disabled=button?.disabled,focused=document.activeElement;
 if(button){clearTimeout(prior?.timer);button.disabled=true;button.textContent='复制中…'}
 let copied=false;
 if(typeof text==='string'&&text.length){
  try{if(window.isSecureContext&&typeof navigator.clipboard?.writeText==='function'){await navigator.clipboard.writeText(text);copied=true}}catch{}
  if(!copied)try{copied=copyWithSelection(text)}catch{}
 }
 const selected=!copied&&selectCopySource(source);
 const message=copied?success:selected?'已选中文本，请按 Ctrl+C 或 ⌘C 复制。':'复制未成功，请手动选中文本并复制。';
 if(status)status.textContent=message;
 if(typeof toast==='function')toast(message);
 if(button){
  button.disabled=disabled;button.textContent=copied?'已复制':'复制失败';
  if(focused===button&&button.isConnected)button.focus({preventScroll:true});
  copyButtonTimers.set(button,{label,timer:setTimeout(()=>{if(button.isConnected)button.innerHTML=label;copyButtonTimers.delete(button)},1800)});
 }
 return copied;
}
