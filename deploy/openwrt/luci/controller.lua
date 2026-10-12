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
 luci.http.write_json({running=luci.sys.call("pidof remotegate-agent >/dev/null 2>&1")==0, enabled=c.enabled=="1", configured=bool(c.server) and bool(c.device_id) and bool(c.token), server=c.server or "", deviceId=c.device_id or "", version=version:gsub("%s+$", "")})
end
function bool(value)
 return value ~= nil and value ~= ""
end

local function update_reply(value)
 luci.http.header("Cache-Control", "no-store")
 luci.http.prepare_content("application/json")
 luci.http.write_json(value)
end

local function update_command(command)
 local raw = luci.sys.exec(command)
 local ok, value = pcall(require("luci.jsonc").parse, raw or "")
 if not ok or type(value) ~= "table" then value = {error="无法读取更新状态，请检查插件是否完整安装"} end
 update_reply(value)
end

local function update_configuration()
 -- The fixed command reads saved UCI settings; credentials are never sent to the browser.
 return luci.sys.call("umask 077; mkdir -p /var/run/remotegate-update && /usr/libexec/remotegate-config --for-update > /var/run/remotegate-update/agent.json.tmp && mv /var/run/remotegate-update/agent.json.tmp /var/run/remotegate-update/agent.json") == 0
end

function update_check()
 if luci.http.getenv("REQUEST_METHOD") ~= "GET" then luci.http.status(405, "Method Not Allowed"); return end
 if not update_configuration() then update_reply({error="请先保存服务器地址、设备 ID 和令牌"}); return end
 update_command("/usr/sbin/remotegate-agent --update-check /var/run/remotegate-update/agent.json")
end

function update_status()
 if luci.http.getenv("REQUEST_METHOD") ~= "GET" then luci.http.status(405, "Method Not Allowed"); return end
 update_command("/usr/sbin/remotegate-agent --update-status")
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
