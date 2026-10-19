-- Run against the packaged /usr/libexec/remotegate-config with LuCI's Lua 5.1.
local config_path = assert(arg[1], "config generator path required")
for _, case in ipairs({{want=true},{value="1",want=true},{value="0",want=false}}) do
 local config = {enabled="1",server="https://example.test",device_id="fixture",token="fixture",remote_access=case.value}
 local result
 local env = setmetatable({arg={},io={write=function() end},require=function(name)
  if name=="uci" then return {cursor=function() return {get_all=function() return config end} end} end
  assert(name=="luci.jsonc")
  return {stringify=function(value) result=value;return "fixture" end}
 end},{__index=_G})
 local chunk=assert(loadfile(config_path));setfenv(chunk,env);chunk()
 assert(result and result.remoteAccess==case.want,"remote management default or explicit disable changed")
 assert(result.directWAN==true and result.mark==nil,"router must use WAN protection, not the legacy WireGuard mark")
 assert(config.remote_access==case.value,"saved UCI setting was modified")
end
print("Router remote management config defaults passed")
