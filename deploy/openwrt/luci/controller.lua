module("luci.controller.remotegate", package.seeall)
function index()
 entry({"admin", "services", "remotegate"}, cbi("remotegate"), "RemoteGate", 60).dependent = false
 entry({"admin", "services", "remotegate", "status"}, call("status")).leaf = true
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
