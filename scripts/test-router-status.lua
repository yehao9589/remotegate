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
-- A broken/replaced agent must not hide the independent updater's result.
local saved_raw,modified="fixture",os.time()
require=function(name)
 if name=="nixio.fs" then return {
  readfile=function(name) if name=="/var/run/remotegate-update/status.json" then return saved_raw end;if name=="/usr/share/remotegate/version" then return "0.1.15-1\n" end end,
  stat=function()return modified end
 } end
 if name=="luci.jsonc" then return {parse=function()return record end} end
 return original_require(name)
end
record={phase="rolling_back",message="正在恢复旧版",autoRollback=true,token="never-export",plan={token="never-export"}}
result=controller.saved_update_view()
assert(result.state.phase=="rolling_back" and result.recoverySupported and result.installedVersion=="0.1.15-1")
assert(result.state.token==nil and result.state.plan==nil)
local replied
controller.luci={http={getenv=function()return "GET" end,header=function()end,prepare_content=function()end,write_json=function(value)replied=value end},sys={exec=function()error("must not execute a broken agent to read rollback progress")end}}
controller.update_status();assert(replied.state.phase=="rolling_back")
record={phase="failed",autoRollback=true,rolledBack=true,rollbackVerified=true,rollbackFailed="true"}
result=controller.saved_update_view();assert(result.state.rolledBack and result.state.rollbackVerified and not result.state.rollbackFailed)
record.phase="verifying";modified=os.time()-601;result=controller.saved_update_view();assert(result.state.phase=="failed")
record.phase="unknown";assert(controller.saved_update_view()==nil)
record.phase="failed";saved_raw=string.rep("x",8193);assert(controller.saved_update_view()==nil)
require=original_require
print("Router connection state, process ownership, stale heartbeat and credential omission checks passed")
