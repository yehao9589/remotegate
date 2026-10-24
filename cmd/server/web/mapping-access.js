// The administrator cookie stays on the console host. Only the server can
// exchange this short-lived request for a mapping-specific browser session.
function mappingAccessRequest(search){
 const value=new URLSearchParams(search).get('access_request');
 return /^[a-f0-9]{64}$/.test(value||'')?value:'';
}
const enterAdminBeforeMappingAccess=enterAdmin;
if(mappingAccessRequest(location.search)){
 $('#login .auth-kicker').textContent='远程访问 · 身份验证';
 $('#login .auth-description').textContent='此访问地址已开启登录保护。验证 RemoteGate 账号及设备授权后，将自动返回原网站。';
 $('#loginSubmit').textContent='登录并继续访问 →';
}
enterAdmin=async entryPath=>{
 const request=mappingAccessRequest(location.search);
 if(request){
  $('#password').value='';
  location.replace('/.remotegate/access/authorize?request='+encodeURIComponent(request));
  return;
 }
 return enterAdminBeforeMappingAccess(entryPath);
};
