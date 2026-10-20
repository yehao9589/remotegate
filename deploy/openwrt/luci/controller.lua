module("luci.controller.remotegate", package.seeall)
function index()
 entry({"admin", "services", "remotegate"}, cbi("remotegate"), "RemoteGate", 60).dependent = false
 entry({"admin", "services", "remotegate", "status"}, call("status")).leaf = true
 entry({"admin", "services", "remotegate", "update_check"}, call("update_check")).leaf = true
 entry({"admin", "services", "remotegate", "update_status"}, call("update_status")).leaf = true
 entry({"admin", "services", "remotegate", "update_start"}, post("update_start")).leaf = true
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
 for _,key in ipairs({"connectedAt","lastAttemptAt","lastHeartbeatAt","nextRetryAt","lastError","lastErrorAt","serverIP","dnsMode","egressInterface"}) do
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

local function update_configuration()
 -- The fixed command reads saved UCI settings; credentials are never sent to the browser.
 return luci.sys.call("umask 077; mkdir -p /var/run/remotegate-update && /usr/libexec/remotegate-config --for-update > /var/run/remotegate-update/agent.json.tmp && mv /var/run/remotegate-update/agent.json.tmp /var/run/remotegate-update/agent.json") == 0
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
