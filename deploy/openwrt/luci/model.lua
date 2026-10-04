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
o = s:option(Value, "interface", "出口网卡", "通常留空，由系统选择出口。需要指定时填写 eth1 或 pppoe-wan 等实际设备名。")
o.datatype = "string"
o.placeholder = "自动选择（推荐）"
function m.on_after_commit(self)
 luci.sys.call("chmod 600 /etc/config/remotegate; /etc/init.d/remotegate enable; /etc/init.d/remotegate restart >/dev/null 2>&1")
end
return m
