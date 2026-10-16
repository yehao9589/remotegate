local m = Map("remotegate")
m.template = "remotegate/map"
local s = m:section(NamedSection, "main", "remotegate", "连接配置")
s.addremove = false
local o = s:option(Flag, "enabled", "启用远程访问")
o.rmempty = false
o = s:option(Value, "server", "服务器地址", "填写部署了 RemoteGate 管理后台的服务器地址。")
o.placeholder = "https://remote.example.com"
function o.validate(self, value)
 if value == "" or value:match("^https?://[%w%.%-:%[%]]+/?$") then return value end
 return nil, "请输入完整的 HTTP 或 HTTPS 地址，例如 https://remote.example.com"
end
o = s:option(Value, "device_id", "设备 ID", "从管理后台的添加设备向导获取。")
o.placeholder = "填写设备 ID"
o = s:option(Value, "token", "设备令牌", "令牌用于验证这台设备，已保存在路由器本地，请勿分享。")
o.password = true
o.placeholder = "填写设备 Token"
o = s:option(Flag, "remote_access", "启用 SSH / 文件管理", "允许已登录的管理后台通过此设备连接内网 SSH 服务，仍需输入 SSH 账号并核对主机指纹。文件管理要求目标 SSH 服务支持 SFTP。公网服务器地址需为 HTTPS。")
o.default = "1"
o.rmempty = false
o = s:option(ListValue, "interface", "出口网卡", "默认由系统选择出口。需要固定出口时，从路由器当前的实际网卡中选择；重新打开页面可刷新列表。")
o.widget = "select"
o.default = ""
o.rmempty = true
o:value("", "自动选择（推荐）")
local detected, devices = pcall(require("nixio").getifaddrs)
local readable = detected and type(devices) == "table"
local by_name, choices = {}, {}
for _, device in ipairs(readable and devices or {}) do
 local name, flags = device.name, device.flags or {}
 if name and name ~= "lo" and not flags.loopback then
  local item = by_name[name]
  if not item then item = {name=name, up=false}; by_name[name] = item; choices[#choices+1] = item end
  item.up = item.up or flags.up == true
  if device.family == "inet" and device.addr then item.ipv4 = item.ipv4 or device.addr end
  if device.family == "inet6" and device.addr then
   local link_local = device.addr:lower():match("^fe[89ab]")
   if not item.ipv6 or (item.link_local and not link_local) then item.ipv6, item.link_local = device.addr, link_local end
  end
 end
end
table.sort(choices, function(a, b)
 if a.up ~= b.up then return a.up end
 return a.name < b.name
end)
for _, item in ipairs(choices) do
 local label = item.name .. (item.up and " · 已启用" or " · 未启用")
 if item.ipv4 or item.ipv6 then label = label .. " · " .. (item.ipv4 or item.ipv6) end
 o:value(item.name, label)
end
local saved_interface = m.uci:get("remotegate", "main", "interface") or ""
if saved_interface ~= "" and not by_name[saved_interface] then
 o:value(saved_interface, saved_interface .. " · 原配置（当前不在可选网卡列表中）")
end
if not readable then
 o.description = "暂时无法读取系统网卡，原配置已保留。请重新打开页面重试，或选择自动出口。"
end
function m.on_after_commit(self)
 luci.sys.call("chmod 600 /etc/config/remotegate; /etc/init.d/remotegate enable; /etc/init.d/remotegate restart >/dev/null 2>&1")
end
return m
