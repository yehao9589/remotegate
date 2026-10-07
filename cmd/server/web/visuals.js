// One local icon set keeps navigation, status and actions visually consistent.
const consoleIconPaths={
 monitor:'<rect x="3" y="3" width="18" height="13" rx="2.5"/><path d="M8 21h8m-4-5v5"/>',
 globe:'<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3.2 4 6.2 4 9s-1 5.8-4 9c-3-3.2-4-6.2-4-9s1-5.8 4-9Z"/>',
 package:'<path d="m12 3 9 5-9 5-9-5 9-5Zm-9 5v9l9 5 9-5V8M12 13v9M7.5 5.5l9 5v4"/>',
 network:'<rect x="8" y="2" width="8" height="6" rx="1.5"/><rect x="2" y="16" width="7" height="6" rx="1.5"/><rect x="15" y="16" width="7" height="6" rx="1.5"/><path d="M12 8v4M5.5 16v-4h13v4"/>',
 shield:'<path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6l8-3Z"/><path d="m8.5 12 2.5 2.5 4.5-5"/>',
 activity:'<path d="M2 12h4l3-7 5 14 3-7h5"/>',
 link:'<path d="m10 13 4-4m-6 6-1 1a4.2 4.2 0 0 1-6-6l4-4a4.2 4.2 0 0 1 6 0m2 3 1-1a4.2 4.2 0 0 1 6 6l-4 4a4.2 4.2 0 0 1-6 0"/>',
 refresh:'<path d="M20 7v5h-5M4 17v-5h5"/><path d="M6.2 6.2A8 8 0 0 1 20 12M4 12a8 8 0 0 0 13.8 5.8"/>',
 logout:'<path d="M9 4H5a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h4m7-12 4 4-4 4m-8-4h12"/>',
 plus:'<path d="M12 5v14M5 12h14"/>',
 search:'<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 5 5"/>',
 edit:'<path d="m16 3 5 5-12 12-6 1 1-6L16 3Zm-2 2 5 5"/>',
 copy:'<rect x="8" y="8" width="13" height="13" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"/>',
 external:'<path d="M14 3h7v7m0-7L10 14M10 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-5"/>',
 pause:'<rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/>',
 play:'<path d="m7 4 14 8-14 8V4Z"/>',
 trash:'<path d="M3 6h18M9 6V3h6v3m-10 0 1 15h12l1-15M10 10v7m4-7v7"/>',
 settings:'<path d="m9 3-1 3-3 1 1 3-2 2 2 2-1 3 3 1 1 3h6l1-3 3-1-1-3 2-2-2-2 1-3-3-1-1-3H9Z"/><circle cx="12" cy="12" r="3"/>',
 history:'<path d="M3 3v6h6m-6 0a9 9 0 1 1 .6 8M12 7v5l3 2"/>',
 dns:'<rect x="3" y="3" width="18" height="6" rx="2"/><rect x="3" y="15" width="18" height="6" rx="2"/><path d="M7 6h.01M7 18h.01M12 9v6"/>',
 download:'<path d="M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5"/>',
 upload:'<path d="M12 16V3m-5 5 5-5 5 5M4 16v5h16v-5"/>',
 folder:'<path d="M3 7V4h7l2 3h9v14H3V7Z"/>',
 key:'<circle cx="8" cy="8" r="5"/><path d="m11.5 11.5 9 9M16 16l3-3m-1 5 3-3"/>',
 terminal:'<rect x="2" y="4" width="20" height="16" rx="3"/><path d="m6 8 4 4-4 4m7 0h5"/>',
 info:'<circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10h.01"/>',
 close:'<path d="m6 6 12 12M18 6 6 18"/>',
 check:'<path d="m5 12 4 4L19 6"/>',
 arrow:'<path d="M4 12h16m-6-6 6 6-6 6"/>'
};
function uiIcon(name){return `<svg class="ui-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${consoleIconPaths[name]||consoleIconPaths.globe}</svg>`}
function consoleBrand(){return `<span class="brand-symbol" aria-hidden="true"><svg viewBox="0 0 32 32" fill="none"><path d="M9 25V9a4 4 0 0 1 4-4h10v20" stroke="currentColor" stroke-width="2.2" stroke-linejoin="round"/><path d="M5 25h22M14 12h13m-4-4 4 4-4 4M14 18h6" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg></span><span>RemoteGate</span>`}
const consolePageVisuals={overview:{icon:'monitor',caption:'DEVICE WORKSPACE'},domains:{icon:'globe',caption:'DOMAINS & CERTIFICATES'},packages:{icon:'package',caption:'PACKAGES & VERSIONS'},deploy:{icon:'network',caption:'CONNECT & DEPLOY'},security:{icon:'shield',caption:'ACCOUNT & SECURITY'}};
function updateConsoleVisuals(){
 const visual=consolePageVisuals[page]||consolePageVisuals.overview;
 $('#pageEmblem').innerHTML=uiIcon(visual.icon);
 $('.page-intro .eyebrow').textContent=visual.caption;
 document.querySelectorAll('nav [data-page]').forEach(b=>{if(b.dataset.page===page)b.setAttribute('aria-current','page');else b.removeAttribute('aria-current')});
}
function setupConsoleVisuals(){
 $('.brand').innerHTML=consoleBrand();$('.auth-wordmark').innerHTML=consoleBrand();
 document.querySelectorAll('nav [data-page]').forEach(b=>{b.innerHTML=`<span class="nav-icon">${uiIcon(consolePageVisuals[b.dataset.page].icon)}</span><span>${b.textContent}</span>`});
 $('#refresh').innerHTML=uiIcon('refresh')+'<span>刷新</span>';$('#logout').innerHTML=uiIcon('logout')+'<span>退出</span>';
 $('.intro-mark').innerHTML=uiIcon('shield')+'私有部署 · 自主掌控';
 const renderBeforeVisuals=render;render=()=>{renderBeforeVisuals();updateConsoleVisuals()};
}
setupConsoleVisuals();
