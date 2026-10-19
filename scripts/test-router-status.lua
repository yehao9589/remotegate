-- Exercise real controller status selection on LuCI's Lua 5.1 runtime.
local path=arg[1] or "deploy/openwrt/luci/controller.lua"
local original_require=require
local record, command, raw
require=function(name)
 if name=="nixio.fs" then return {readfile=function(name) if name=="/var/run/remotegate/connection.json" then return raw end; if name=="/proc/42/cmdline" then return command end end} end
 if name=="luci.jsonc" then return {parse=function() return record end} end
 return original_require(name)
end
dofile(path)
local controller=package.loaded["luci.controller.remotegate"]
local config={enabled="1",server="https://example.com",device_id="device",token="fixture-token"}
local function reset()
 raw="fixture"
 command="/usr/sbin/remotegate-agent\0/var/run/remotegate/agent.json\0"
 record={pid=42,state="connected",server=config.server,deviceId=config.device_id,updatedUnix=os.time(),connectedAt="2026-10-17T17:00:00Z",token="must-not-leak"}
end
reset();local result=controller.connection_state(config,true);assert(result.state=="connected" and result.connectedAt==record.connectedAt and result.token==nil and result.pid==nil)
reset();record.egressInterface="pppoe-wan";record.routeProtected=true;result=controller.connection_state(config,true);assert(result.egressInterface=="pppoe-wan" and result.routeProtected==true)
reset();record.routeProtected="true";assert(controller.connection_state(config,true).routeProtected==false)
reset();command=nil;assert(controller.connection_state(config,true).state=="unknown","dead PID reported online")
reset();command="/usr/sbin/remotegate-agent\0--update-check\0/var/run/remotegate-update/agent.json\0";assert(controller.connection_state(config,true).state=="unknown","updater reported as the tunnel")
reset();record.updatedUnix=os.time()-1000;result=controller.connection_state(config,true);assert(result.state=="reconnecting" and result.lastError:find("90",1,true))
reset();record.updatedUnix=os.time()+1000;assert(controller.connection_state(config,true).state=="reconnecting")
reset();record.deviceId="other";assert(controller.connection_state(config,true).state=="unknown")
reset();record.server="https://other.example.com";assert(controller.connection_state(config,true).state=="unknown")
reset();record.pid="42; malicious";assert(controller.connection_state(config,true).state=="unknown")
reset();raw=nil;assert(controller.connection_state(config,true).state=="unknown")
reset();assert(controller.connection_state(config,false).state=="stopped")
config.enabled="0";assert(controller.connection_state(config,true).state=="disabled")
config.enabled="1";config.token="";assert(controller.connection_state(config,true).state=="unconfigured")
require=original_require
print("Router connection state, process ownership, stale heartbeat and credential omission checks passed")
