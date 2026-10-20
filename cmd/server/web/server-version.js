// Release checks are independent of device polling and never install software.
let serverReleaseSnapshot=null,serverReleasePending=null,serverReleaseOwner=-1,serverReleaseAttempt=0;
names.version='版本与更新';
consolePageVisuals.version={icon:'history',caption:'VERSION & RELEASE NOTES'};
function releasePageURL(version){return /^\d+\.\d+\.\d+$/.test(version||'')?'https://github.com/yehao9589/remotegate/releases/tag/v'+version:'https://github.com/yehao9589/remotegate/releases/latest'}
function resetServerReleaseOwner(){
 if(serverReleaseOwner===authTransition)return;
 serverReleaseOwner=authTransition;serverReleaseSnapshot=null;serverReleasePending=null;serverReleaseAttempt=0;
}
function releaseStatusText(data){
 if(!data)return '检查新版本与版本说明';
 if(data.status==='available')return '发现新版本 v'+data.latest.version;
 if(data.status==='current')return '已是最新正式版';
 if(data.status==='ahead')return '当前版本领先正式版';
 if(data.status==='unchecked')return '尚未检测最新版本';
 return '检测失败 · 点击查看';
}
function renderServerReleaseFooter(){
 const entry=$('#serverVersionEntry'),status=$('#serverUpdateStatus');
 if(!entry||!status)return;
 entry.classList.toggle('has-update',serverReleaseSnapshot?.status==='available');
 entry.setAttribute('title','版本与更新 · '+releaseStatusText(serverReleaseSnapshot));
 entry.setAttribute('aria-current',page==='version'?'page':'false');
 status.textContent=serverReleasePending?'正在检测新版本…':releaseStatusText(serverReleaseSnapshot);
}
async function loadServerRelease(force=false){
 resetServerReleaseOwner();
 if(serverReleasePending)return serverReleasePending;
 const transition=authTransition,controller=new AbortController(),timeout=setTimeout(()=>controller.abort(),12000);
 serverReleaseAttempt=Date.now();
 const operation=(async()=>{
  try{
   if(!serverReleaseSnapshot?.history){
    try{const local=await api('version/notes','GET',undefined,{signal:controller.signal});if(transition!==authTransition)return;serverReleaseSnapshot=local;if(page==='version')renderServerVersion()}catch(error){if(transition!==authTransition)return}
   }
   const result=await api('version/check',force?'POST':'GET',force?{}:undefined,{signal:controller.signal});
   if(transition!==authTransition)return;
   serverReleaseSnapshot=result;
  }catch(error){
   if(transition!==authTransition)return;
   serverReleaseSnapshot={...(serverReleaseSnapshot||{}),status:'unknown',message:error.name==='AbortError'?'版本检测超时，请稍后重试':error.message};
  }finally{
   clearTimeout(timeout);
   if(transition===authTransition&&serverReleasePending===operation){
    serverReleasePending=null;renderServerReleaseFooter();if(page==='version')renderServerVersion();
   }
  }
 })();
 serverReleasePending=operation;renderServerReleaseFooter();
 return operation;
}
function ensureServerReleaseCheck(){
 resetServerReleaseOwner();
 const interval=serverReleaseSnapshot?.status==='unknown'?5*60*1000:6*60*60*1000;
 if(!serverReleasePending&&(!serverReleaseAttempt||Date.now()-serverReleaseAttempt>=interval))void loadServerRelease();
}
function versionDate(value){const date=new Date(value);return value&&!String(value).startsWith('0001-')&&!Number.isNaN(date.valueOf())?date.toLocaleString():'尚未成功检测'}
function releaseInline(value){return esc(value).replace(/`([^`]+)`/g,'<code>$1</code>').replace(/\*\*([^*]+)\*\*/g,'<strong>$1</strong>')}
function releaseNotesHTML(value){
 // Render only headings, bullets, code fences and text. Remote Markdown/HTML
 // never becomes executable markup, images or unverified external links.
 let code=false;return String(value||'暂无版本说明。').split('\n').map(line=>{
  if(line.startsWith('```')){code=!code;return ''}
  if(code)return '<div class="version-code-line">'+esc(line)+'</div>';
  const heading=line.match(/^#{1,6}\s+(.+)$/);if(heading)return '<h3>'+releaseInline(heading[1])+'</h3>';
  if(/^\s*[-*]\s+/.test(line))return '<p class="version-note-item">'+releaseInline(line.replace(/^\s*[-*]\s+/,''))+'</p>';
  return line.trim()?'<p>'+releaseInline(line)+'</p>':'';
 }).join('');
}
function renderServerVersion(){
 resetServerReleaseOwner();
 const data=serverReleaseSnapshot,current=data?.current||state.package?.server||{},latest=data?.latest;
 $('#content').classList.add('version-center');
 $('#pageDescription').textContent='查看当前版本、检测正式更新，了解每次更新的变化。';
 const busy=Boolean(serverReleasePending),available=data?.status==='available',failed=data?.status==='unknown';
 const currentNotes=(data?.history||[]).find(item=>item.version===current.version);
 $('#content').innerHTML=`<div class="version-heading"><div><span class="version-eyebrow">REMOTEGATE</span><h2>版本信息</h2><p>当前运行版本与最新正式发布版本分别展示。</p></div><button type="button" class="primary" id="checkServerRelease" ${busy?'disabled aria-busy="true"':''}>${uiIcon('refresh')}${busy?'正在检测…':'检查新版本'}</button></div><div class="version-summary-grid"><article class="version-summary"><span>当前服务端</span><strong>v${esc(current.version||'—')}</strong><p>随附插件 ${esc(current.packageVersion||'—')}</p><small>${current.commit==='development'?'本地开发构建':current.commit?'构建 '+esc(current.commit.slice(0,12)):'正在读取版本信息'}</small></article><article class="version-summary ${available?'version-new':''}"><span>最新正式版</span><strong>${latest?'v'+esc(latest.version):'待检测'}</strong><p>${latest?esc(latest.title||'RemoteGate 正式发布'):'仅检测已正式发布的版本'}</p><small>${latest?'发布于 '+esc(versionDate(latest.publishedAt)):'不将测试版或未完成的发布视为可更新'}</small></article></div><div class="version-check-result ${failed?'version-check-error':available?'version-check-new':''}" role="status">${uiIcon(failed?'info':available?'download':'shield')}<div><strong>${busy?'正在检测最新正式版本…':data?esc(releaseStatusText(data)):'正在获取版本信息…'}</strong><p>${failed?esc(data.message||'无法获取最新版本，请稍后重试'):'最近成功检测：'+esc(versionDate(data?.checkedAt))}${failed&&latest?'；下方保留上次获取的版本说明。':''}</p></div></div><div class="version-privacy">检测仅访问项目官方发布接口，不发送设备令牌、账号或配置。自动检测间隔 6 小时，手动检测间隔 1 分钟。</div><section class="version-upgrade"><div><h3>${available?'如何更新到 v'+esc(latest.version):'如何更新'}</h3><p>先备份数据，再重新拉取镜像并重新部署。只重启容器不会升级。涉及插件的新功能，还需在“安装与更新中心”更新路由器插件。</p></div><a href="https://github.com/yehao9589/remotegate/blob/main/docs/deployment/maintenance.md" target="_blank" rel="noopener noreferrer">更新与备份教程 ${uiIcon('external')}</a></section>${latest&&latest.version!==current.version?`<section class="version-release"><div class="version-release-heading"><div><span class="version-eyebrow">${failed?'上次获取的正式版说明':'最新正式版说明'}</span><h2>${esc(latest.title||'RemoteGate v'+latest.version)}</h2></div><a href="${releasePageURL(latest.version)}" target="_blank" rel="noopener noreferrer">官方发布页 ${uiIcon('external')}</a></div><div class="version-notes">${releaseNotesHTML(latest.notes)}</div></section>`:''}<section class="version-release"><div class="version-release-heading"><div><span class="version-eyebrow">随本机程序提供 · 无需联网</span><h2>当前版本说明${currentNotes?' · v'+esc(current.version):''}</h2></div>${latest?.version===current.version?`<a href="${releasePageURL(current.version)}" target="_blank" rel="noopener noreferrer">官方发布页 ${uiIcon('external')}</a>`:''}</div><div class="version-notes">${currentNotes?releaseNotesHTML(currentNotes.notes):'<p>'+(!data?'正在读取本机版本说明…':'本构建未附带当前版本说明。')+'</p>'}</div></section><details class="version-history"><summary>历史版本说明</summary>${(data?.history||[]).filter(item=>item.version!==current.version).map(item=>`<details><summary>v${esc(item.version)} <span>${esc(item.publishedAt)}</span></summary><div class="version-notes">${releaseNotesHTML(item.notes)}</div></details>`).join('')||'<p class="muted">暂无其他本机版本说明。</p>'}</details><button type="button" id="versionBack">${uiIcon('arrow')}返回设备工作台</button>`;
 $('#checkServerRelease').onclick=()=>{void loadServerRelease(true);renderServerVersion()};
 $('#versionBack').onclick=()=>{page='overview';if(location.hash==='#/version')history.pushState(null,'',location.pathname+location.search);render();window.scrollTo(0,0)};
 renderServerReleaseFooter();
 if(!data&&!busy)void loadServerRelease();
}
function openServerVersion(){page='version';query='';filter='';if(location.hash!=='#/version')location.hash='/version';render();window.scrollTo(0,0);ensureServerReleaseCheck()}
$('#serverVersionEntry').onclick=openServerVersion;
if(location.hash==='#/version')page='version';
window.addEventListener('hashchange',()=>{if($('#shell').hidden)return;if(location.hash==='#/version')page='version';else if(page==='version')page='overview';render()});
document.querySelectorAll('nav [data-page]').forEach(button=>button.addEventListener('click',()=>{if(location.hash==='#/version')history.replaceState(null,'',location.pathname+location.search);renderServerReleaseFooter()}));
const refreshBeforeServerVersion=refresh;
refresh=function(){const operation=refreshBeforeServerVersion();operation.then(()=>ensureServerReleaseCheck()).catch(()=>{});return operation};
