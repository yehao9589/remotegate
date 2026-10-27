-- Auth, streaming limits, fixed paths and confirmation binding for the real controller.
local source=arg[1] or "deploy/openwrt/luci/controller.lua"
local original=require
local context={authtoken="csrf",authsession="session",authuser="root"}
local env,files,lock,reply,status,parsed,handler,commands,allow,view,values,parts
local function reset()
 env={REQUEST_METHOD="POST",HTTP_X_REMOTEGATE_TOKEN="csrf"};files={};lock=false;reply=nil;status=nil;parsed=false;handler=nil;commands={};allow=true
 context.authuser="root"
 view={available=true,fileSHA256=string.rep("a",64),manifest={packageVersion="0.1.18-9"}}
 values={version="0.1.18-9",sha256=string.rep("a",64)}
 parts={{meta={name="package",file="../../etc/shadow"},chunk="package-bytes",eof=true}}
end
require=function(name)
 if name=="luci.dispatcher" then return {context=context} end
 if name=="luci.util" then return {ubus=function(_,method)if method=="get" then return {data={username="root"}} end;return {access=allow} end} end
 if name=="luci.jsonc" then return {parse=function()return view end} end
 if name=="nixio.fs" then return {
  readfile=function()return nil end,
  mkdir=function()if lock then return nil end;lock=true;return true end,
  rmdir=function()lock=false;return true end,
  stat=function(path,what)if what=="mtime" then return os.time() end;if files[path] then return {size=#files[path]} end end,
  unlink=function(path)files[path]=nil;return true end,
  rename=function(a,b)files[b]=files[a];files[a]=nil;return true end,
 } end
 if name=="nixio" then return {open=function(path,mode,permissions)
  assert(path=="/var/run/remotegate-update/upload.part" and mode=="w" and permissions==384)
  files[path]="";return {writeall=function(_,chunk)files[path]=files[path]..chunk;return #chunk end,close=function()end}
 end} end
 return original(name)
end
dofile(source)
local controller=package.loaded["luci.controller.remotegate"]
controller.luci={http={
 getenv=function(name)return env[name]end,status=function(code)status=code end,header=function()end,prepare_content=function()end,write_json=function(value)reply=value end,
 setfilehandler=function(fn)handler=fn end,
 formvalue=function(name)if handler and not parsed then parsed=true;for _,part in ipairs(parts)do handler(part.meta,part.chunk,part.eof)end end;return values[name]end,
},sys={call=function()return 0 end,exec=function(command)commands[#commands+1]=command;return "json" end}}
for _,operation in ipairs({controller.update_upload,controller.update_local_start})do
 reset();env.REQUEST_METHOD="GET";operation();assert(status==405 and not parsed and #commands==0)
 reset();env.HTTP_X_REMOTEGATE_TOKEN="bad";operation();assert(status==403 and not parsed and #commands==0)
 reset();context.authuser="member";operation();assert(status==403 and not parsed and #commands==0)
 reset();allow=false;operation();assert(status==403 and not parsed and #commands==0)
end
reset();controller.update_upload();assert(reply.available and files["/var/run/remotegate-update/upload.bin"]=="package-bytes" and not lock)
assert(#commands==1 and not commands[1]:find("shadow",1,true) and commands[1]:find("--update-local-check",1,true))
reset();context.authuser=nil;controller.update_upload();assert(reply.available,"modern session username lookup failed")
reset();env.CONTENT_LENGTH=tostring(16*1024*1024+1);controller.update_upload();assert(status==413 and reply.error and #commands==0 and not lock)
reset();parts[1].eof=false;controller.update_upload();assert(reply.error and not files["/var/run/remotegate-update/upload.part"] and #commands==0 and not lock)
reset();parts[2]=parts[1];controller.update_upload();assert(reply.error and #commands==0 and not lock)
reset();view={error="wrong architecture"};controller.update_upload();assert(reply.error and not files["/var/run/remotegate-update/upload.bin"] and not lock)
reset();lock=true;controller.update_upload();assert(reply.error and not parsed and #commands==0)
reset();values.version="1.0.0-1';touch /tmp/bad";controller.update_local_start();assert(reply.error and #commands==0 and not lock)
reset();view={state={phase="checking"}};files["/var/run/remotegate-update/upload.bin"]="ready";controller.update_local_start();assert(reply.state.phase=="checking" and not files["/var/run/remotegate-update/upload.bin"] and not lock)
assert(#commands==1 and commands[1]:find("--update-local-start",1,true))
require=original
print("Manual upload root/CSRF checks, fixed paths, stream completion, size limits and confirmation input checks passed")
