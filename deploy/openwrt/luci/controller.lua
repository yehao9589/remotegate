module("luci.controller.remotegate", package.seeall)
function index()
 entry({"admin", "services", "remotegate"}, cbi("remotegate"), "RemoteGate", 60).dependent = false
 entry({"admin", "services", "remotegate", "status"}, call("status")).leaf = true
 entry({"admin", "services", "remotegate", "update_check"}, call("update_check")).leaf = true
 entry({"admin", "services", "remotegate", "update_status"}, call("update_status")).leaf = true
 entry({"admin", "services", "remotegate", "update_start"}, post("update_start")).leaf = true
 -- Multipart bodies must not be consumed by dispatcher.post() before the file handler.
 -- Both endpoints explicitly require a root session and the session CSRF header.
 entry({"admin", "services", "remotegate", "update_upload"}, call("update_upload")).leaf = true
 entry({"admin", "services", "remotegate", "update_local_start"}, call("update_local_start")).leaf = true
end
function status()
 local u = require("uci").cursor()
 local c = u:get_all("remotegate", "main") or {}
 local version = require("nixio.fs").readfile("/usr/share/remotegate/version") or "未知"
 luci.http.header("Cache-Control", "no-store")
 luci.http.prepare_content("application/json")
 local running = luci.sys.call("pidof remotegate-agent >/dev/null 2>&1")==0
 luci.http.write_json({running=running, enabled=c.enabled=="1", configured=bool(c.server) and bool(c.device_id) and bool(c.token), server=c.server or "", deviceId=c.device_id or "", version=version:gsub("%s+$", ""), connection=connection_state(c,running)})
end
function bool(value)
 return value ~= nil and value ~= ""
end

function connection_state(c, running)
 if c.enabled~="1" then return {state="disabled"} end
 if not bool(c.server) or not bool(c.device_id) or not bool(c.token) then return {state="unconfigured"} end
 if not running then return {state="stopped"} end
 local fs=require("nixio.fs")
 local raw=fs.readfile("/var/run/remotegate/connection.json")
 if not raw or #raw>8192 then return {state="unknown"} end
 local ok, value=pcall(require("luci.jsonc").parse,raw)
 if not ok or type(value)~="table" then return {state="unknown"} end
 local pid=tonumber(value.pid)
 if not pid or pid<1 or pid~=math.floor(pid) or pid>1073741824 then return {state="unknown"} end
 local command=fs.readfile("/proc/"..string.format("%d",pid).."/cmdline") or ""
 local expected="/usr/sbin/remotegate-agent\0"
 if command~=expected.."/var/run/remotegate/agent.json\0" and command~=expected.."/etc/remotegate/agent.json\0" then return {state="unknown"} end
 if value.deviceId~=c.device_id or value.server~=(c.server or ""):gsub("/+$","") then return {state="unknown"} end
 local allowed={connecting=true,connected=true,reconnecting=true,stopped=true}
 if not allowed[value.state] then return {state="unknown"} end
 local result={state=value.state}
 for _,key in ipairs({"connectedAt","lastAttemptAt","lastHeartbeatAt","nextRetryAt","lastError","lastErrorAt","recoveryStartedAt","serverIP","dnsMode","egressInterface"}) do
  if type(value[key])=="string" and #value[key]<=1500 then result[key]=value[key] end
 end
 result.routeProtected=value.routeProtected==true
 local age=os.time()-(tonumber(value.updatedUnix) or 0)
 if value.state=="connected" and (age>90 or age< -90) then
  result.state="reconnecting";result.lastError="隧道心跳状态超过 90 秒未更新，等待客户端重新连接"
 end
 return result
end

local function update_reply(value)
 luci.http.header("Cache-Control", "no-store")
 luci.http.prepare_content("application/json")
 luci.http.write_json(value)
end

local function update_command(command, fallback)
 local raw = luci.sys.exec(command)
 local ok, value = pcall(require("luci.jsonc").parse, raw or "")
 if not ok or type(value) ~= "table" then value = fallback or {error="无法读取更新状态，请检查插件是否完整安装"} end
 update_reply(value)
end

local function update_configuration(local_update)
 -- The fixed command reads saved UCI settings; credentials are never sent to the browser.
 return luci.sys.call("umask 077; mkdir -p /var/run/remotegate-update && /usr/libexec/remotegate-config " .. (local_update and "--for-local-update" or "--for-update") .. " > /var/run/remotegate-update/agent.json.tmp && mv /var/run/remotegate-update/agent.json.tmp /var/run/remotegate-update/agent.json") == 0
end

function saved_update_view()
 -- The status page must remain readable while opkg replaces the executable,
 -- including when a new client cannot run its own --update-status command.
 local fs=require("nixio.fs")
 local raw=fs.readfile("/var/run/remotegate-update/status.json")
 if not raw or #raw>8192 then return nil end
 local ok,value=pcall(require("luci.jsonc").parse,raw)
 local active={checking=true,downloading=true,backing_up=true,installing=true,restarting=true,verifying=true,rolling_back=true}
 if not ok or type(value)~="table" or not (active[value.phase] or value.phase=="failed" or value.phase=="succeeded") then return nil end
 local state={phase=value.phase}
 for _,key in ipairs({"id","fromVersion","toVersion","message","startedAt","updatedAt"}) do
  local limit=key=="message" and 1500 or 128
  if type(value[key])=="string" and #value[key]<=limit then state[key]=value[key] end
 end
 for _,key in ipairs({"autoRollback","rolledBack","rollbackVerified","rollbackFailed"}) do state[key]=value[key]==true end
 local modified=fs.stat("/var/run/remotegate-update/status.json","mtime")
 if active[state.phase] and modified and os.time()-modified>600 then
  state.phase="failed";state.message="更新状态超过 10 分钟未更新，请查看路由器日志并核对本机备份"
 end
 local version=(fs.readfile("/usr/share/remotegate/version") or ""):gsub("%s+$","")
 return {installedVersion=version,state=state,recoverySupported=state.autoRollback,available=false}
end

function update_check()
 if luci.http.getenv("REQUEST_METHOD") ~= "GET" then luci.http.status(405, "Method Not Allowed"); return end
 if not update_configuration() then update_reply({error="请先保存服务器地址、设备 ID 和令牌"}); return end
 update_command("/usr/sbin/remotegate-agent --update-check /var/run/remotegate-update/agent.json")
end

function update_status()
 if luci.http.getenv("REQUEST_METHOD") ~= "GET" then luci.http.status(405, "Method Not Allowed"); return end
 local saved=saved_update_view()
 if saved and saved.state.autoRollback then update_reply(saved);return end
 update_command("/usr/sbin/remotegate-agent --update-status",saved)
end

function update_start()
 -- dispatcher.post() requires both POST and the LuCI session CSRF token.
 local version = luci.http.formvalue("version") or ""
 local sha = luci.http.formvalue("sha256") or ""
 if #version > 64 or not version:match("^%d+%.%d+%.%d+%-%d+$") or #sha ~= 64 or not sha:match("^[a-f0-9]+$") then
  update_reply({error="更新参数无效，请重新检查更新"}); return
 end
 if not update_configuration() then update_reply({error="请先保存服务器地址、设备 ID 和令牌"}); return end
 update_command("/usr/sbin/remotegate-agent --update-start /var/run/remotegate-update/agent.json '" .. version .. "' '" .. sha .. "'")
end


local function manual_auth()
 local http=luci.http
 if http.getenv("REQUEST_METHOD")~="POST" then http.status(405,"Method Not Allowed");return false end
 local context=require("luci.dispatcher").context or {}
 local token=http.getenv("HTTP_X_REMOTEGATE_TOKEN") or ""
 if token=="" or token~=context.authtoken or not context.authsession then http.status(403,"Forbidden");return false end
 local util=require("luci.util")
 local user=context.authuser
 if not user then
  local ok,session=pcall(util.ubus,"session","get",{ubus_rpc_session=context.authsession})
  if ok and type(session)=="table" and type(session.data)=="table" then user=session.data.username end
 end
 local ok,access=pcall(util.ubus,"session","access",{ubus_rpc_session=context.authsession,scope="uci",object="remotegate",["function"]="write"})
 if user~="root" or not ok or type(access)~="table" or access.access~=true then http.status(403,"Forbidden");return false end
 return true
end

local function manual_lock()
 local fs=require("nixio.fs")
 if luci.sys.call("umask 077; mkdir -p /var/run/remotegate-update && chmod 700 /var/run/remotegate-update")~=0 then return false end
 local state=saved_update_view()
 local active={checking=true,downloading=true,backing_up=true,installing=true,restarting=true,verifying=true,rolling_back=true}
 if state and (active[state.state.phase] or state.state.rollbackFailed) then return false end
 local lock="/var/run/remotegate-update/upload.lock"
 if fs.mkdir(lock) then return true end
 local modified=fs.stat(lock,"mtime")
 if modified and os.time()-modified>600 and fs.rmdir(lock) then return fs.mkdir(lock) and true or false end
 return false
end
local function manual_unlock() require("nixio.fs").rmdir("/var/run/remotegate-update/upload.lock") end
local function manual_command(command)
 local raw=luci.sys.exec(command)
 local ok,value=pcall(require("luci.jsonc").parse,raw or "")
 if not ok or type(value)~="table" then return {error="手动更新工具无法读取安装包，请检查插件是否完整安装"} end
 return value
end

function update_upload()
 if not manual_auth() then return end
 if not manual_lock() then update_reply({error="已有更新或上传正在执行，或上次回滚尚未完成"});return end
 local fs=require("nixio.fs")
 local part="/var/run/remotegate-update/upload.part"
 local length=tonumber(luci.http.getenv("CONTENT_LENGTH"))
 if length and length>16*1024*1024 then manual_unlock();luci.http.status(413,"Payload Too Large");update_reply({error="安装包最大 15 MiB"});return end
 local file,finished,failure=nil,false,nil
 local size=0
 fs.unlink(part)
 luci.http.setfilehandler(function(meta,chunk,eof)
  if failure then return end
  if finished or type(meta)~="table" or meta.name~="package" then failure="请只上传一个 RemoteGate 安装包";return end
  if not file then file=require("nixio").open(part,"w",384);if not file then failure="无法保存上传文件，请检查临时空间";return end end
  if chunk and #chunk>0 then
   size=size+#chunk
   if size>15*1024*1024 then failure="安装包最大 15 MiB";return end
   local written=file:writeall(chunk)
   if written~=true and written~=#chunk then failure="写入上传文件失败，请检查临时空间";return end
  end
  if eof then file:close();file=nil;finished=true end
 end)
 local parsed=pcall(luci.http.formvalue,"package")
 if file then file:close() end
 local stat=fs.stat(part)
 if not parsed or not finished or not stat or stat.size~=size or size==0 then failure=failure or "上传未完成，请重新选择安装包" end
 local value
 if failure then value={error=failure}
 else
  value=manual_command("/usr/sbin/remotegate-agent --update-local-check /var/run/remotegate-update/upload.part")
  if not value.error then
   if type(value.manifest)~="table" or type(value.fileSHA256)~="string" or #value.fileSHA256~=64 then value={error="安装包校验结果无效"}
   elseif not fs.rename(part,"/var/run/remotegate-update/upload.bin") then value={error="无法保存已校验安装包"} end
  end
 end
 fs.unlink(part);manual_unlock();update_reply(value)
end

function update_local_start()
 if not manual_auth() then return end
 if not manual_lock() then update_reply({error="已有更新或上传正在执行，或上次回滚尚未完成"});return end
 local version=luci.http.formvalue("version") or ""
 local sha=luci.http.formvalue("sha256") or ""
 if #version>64 or not version:match("^%d+%.%d+%.%d+%-%d+$") or #sha~=64 or not sha:match("^[a-f0-9]+$") then manual_unlock();update_reply({error="更新参数无效，请重新上传并校验"});return end
 if not update_configuration(true) then manual_unlock();update_reply({error="无法读取路由器保存的插件配置"});return end
 local value=manual_command("/usr/sbin/remotegate-agent --update-local-start /var/run/remotegate-update/agent.json '"..version.."' '"..sha.."'")
 if not value.error and type(value.state)=="table" and value.state.phase=="checking" then require("nixio.fs").unlink("/var/run/remotegate-update/upload.bin") end
 manual_unlock();update_reply(value)
end
