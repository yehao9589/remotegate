-- Run with Lua 5.1, as used by the LuCI compatibility layer.
local model_path = arg[1] or "deploy/openwrt/luci/model.lua"
local function model(devices, saved, fail)
 local options = {}
 local section = {option=function(self, kind, name)
  local o = {kind=kind, values={}, order={}}
  function o:value(key, label) self.values[key]=label; self.order[#self.order+1]=key end
  options[name]=o; return o
 end}
 local env = setmetatable({Map=function() return {uci={get=function() return saved end},section=function() return section end} end,NamedSection="NamedSection",Flag="Flag",Value="Value",ListValue="ListValue",require=function(name)
  assert(name=="nixio")
  return {getifaddrs=function() if fail then error("interface read failed") end; return devices end}
 end}, {__index=_G})
 local chunk=assert(loadfile(model_path));setfenv(chunk,env);chunk()
 return options.interface
end
local function contains(text, expected) assert(text and text:find(expected,1,true), tostring(text).." missing "..expected) end
local devices={
 {name="eth1",family="packet",flags={up=false}},
 {name="eth0",family="packet",flags={up=true}},
 {name="eth0",family="inet",addr="192.0.2.10",flags={up=true}},
 {name="eth0",family="inet6",addr="2001:db8::10",flags={up=true}},
 {name="pppoe-wan",family="inet",addr="198.51.100.10",flags={up=true}},
 {name="wg0",family="inet6",addr="fe80::1",flags={up=true}},
 {name="wg0",family="inet6",addr="2001:db8::1",flags={up=true}},
 {name="lo",family="inet",addr="127.0.0.1",flags={up=true,loopback=true}}
}
local selected=model(devices,"eth0")
assert(selected.kind=="ListValue" and selected.widget=="select")
assert(selected.order[1]=="" and selected.default=="" and selected.rmempty)
contains(selected.values[""],"自动选择")
contains(selected.values.eth0,"192.0.2.10")
contains(selected.values.eth1,"未启用")
contains(selected.values["pppoe-wan"],"198.51.100.10")
contains(selected.values.wg0,"2001:db8::1")
assert(not selected.values.lo)
local count=0;for _,name in ipairs(selected.order) do if name=="eth0" then count=count+1 end end;assert(count==1)
contains(model(devices,"missing0").values.missing0,"原配置")
local failed=model(nil,"saved0",true)
assert(failed.values[""]);contains(failed.values.saved0,"原配置");contains(failed.description,"暂时无法读取")
assert(model(nil,"").values[""])
print("Router interface model checks passed")
