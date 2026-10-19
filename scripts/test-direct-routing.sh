#!/bin/sh
set -eu
# Run as root on Linux; all network changes are restricted to fresh namespaces.
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
test_binary="${DIRECT_TEST_BINARY:?compile cmd/agent with go test -c first}"
temporary="$(mktemp -d /tmp/remotegate-routing.XXXXXX)"
router="rg-r-$$"
wan="rg-w-$$"
server_pid=
cleanup() {
 [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
 ip netns del "$router" 2>/dev/null || true
 ip netns del "$wan" 2>/dev/null || true
 rm -f "$temporary"/ubus "$temporary"/wan.json "$temporary"/server.py "$temporary"/route-guard
 rmdir "$temporary" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM
ip netns add "$router"
ip netns add "$wan"
ip link add "rg$$" type veth peer name "rgw$$"
ip link set "rg$$" netns "$router"
ip link set "rgw$$" netns "$wan"
ip -n "$router" link set "rg$$" name wan0
ip -n "$wan" link set "rgw$$" name gw0
for ns in "$router" "$wan"; do
 ip -n "$ns" link set lo up
 ip netns exec "$ns" sysctl -qw net.ipv6.conf.all.disable_ipv6=0
done
ip -n "$router" link set wan0 up
ip netns exec "$router" sysctl -qw net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.wan0.rp_filter=0
ip -n "$wan" link set gw0 up
ip -n "$router" addr add 10.23.1.2/24 dev wan0
ip -n "$wan" addr add 10.23.1.1/24 dev gw0
ip -n "$router" -6 addr add 2001:db8:10::2/64 dev wan0 nodad
ip -n "$wan" -6 addr add 2001:db8:10::1/64 dev gw0 nodad
ip -n "$wan" addr add 203.0.113.10/32 dev lo
ip -n "$wan" -6 addr add 2001:db8:30::10/128 dev lo nodad
ip -n "$router" route add default via 10.23.1.1 dev wan0 metric 50
ip -n "$router" -6 route add default via 2001:db8:10::1 dev wan0 metric 50
ip -n "$router" link add utun0 type dummy
ip -n "$router" link set utun0 up
for family in -4 -6; do
 ip -n "$router" "$family" route add default dev utun0 metric 1
 ip -n "$router" "$family" route add default dev utun0 table 354
 ip -n "$router" "$family" rule add priority 100 lookup 354
done
cat > "$temporary/wan.json" <<'EOF'
{"interface":[{"interface":"wan","up":true,"l3_device":"wan0","route":[{"target":"0.0.0.0","mask":0,"nexthop":"10.23.1.1"}]},{"interface":"wan6","up":true,"l3_device":"wan0","route":[{"target":"::","mask":0,"nexthop":"2001:db8:10::1"}]}]}
EOF
cat > "$temporary/ubus" <<'EOF'
#!/bin/sh
cat "$REMOTEGATE_WAN_FIXTURE"
EOF
chmod 755 "$temporary/ubus"
cp "$root/deploy/openwrt/remotegate-route-guard" "$temporary/route-guard"
chmod 755 "$temporary/route-guard"
cat > "$temporary/server.py" <<'EOF'
import socket, socketserver
from http.server import HTTPServer, BaseHTTPRequestHandler
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        peer=self.client_address[0].removeprefix('::ffff:')
        body=('WAN '+peer).encode()
        self.send_response(200)
        self.send_header('Content-Length',str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self,*args): pass
class Server(HTTPServer):
    address_family=socket.AF_INET6
    def server_bind(self):
        self.socket.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,0)
        socketserver.TCPServer.server_bind(self)
        self.server_name='isolated-fixture'
        self.server_port=18088
Server(('::',18088),Handler).serve_forever()
EOF
ip netns exec "$wan" python3 "$temporary/server.py" &
server_pid=$!
sleep 1
ip netns exec "$wan" curl --noproxy '*' --fail --silent --max-time 2 http://203.0.113.10:18088/
printf '\n'
for backend in nft iptables; do
 # Reset only the fixture's own table/rules, inside this disposable namespace.
 ip netns exec "$router" nft delete table inet fw4 2>/dev/null || true
 for command in iptables ip6tables; do
  ip netns exec "$router" "$command" -t nat -F OUTPUT
  ip netns exec "$router" "$command" -t mangle -F OUTPUT
 done
 if [ "$backend" = nft ]; then
  ip netns exec "$router" nft -f - <<'EOF'
table inet fw4 {
 chain openclash_output { tcp dport 18088 counter redirect to :7892; }
 chain openclash_output_v6 { tcp dport 18088 counter redirect to :7892; }
 chain openclash_mangle_output { }
 chain openclash_mangle_output_v6 { }
 chain mangle_output { type route hook output priority -150; tcp dport 18088 meta mark set 0x162; }
 chain nat_output { type nat hook output priority -1; meta nfproto ipv4 jump openclash_output; meta nfproto ipv6 jump openclash_output_v6; }
}
EOF
 else
  for command in iptables ip6tables; do
   ip netns exec "$router" "$command" -t mangle -A OUTPUT -p tcp --dport 18088 -j MARK --set-mark 0x162
   ip netns exec "$router" "$command" -t nat -A OUTPUT -p tcp --dport 18088 -j REDIRECT --to-ports 7892
  done
 fi
 # The test deliberately marks WAN down at the end; reset its fixture per run.
 printf '%s\n' '{"interface":[{"interface":"wan","up":true,"l3_device":"wan0","route":[{"target":"0.0.0.0","mask":0,"nexthop":"10.23.1.1"}]},{"interface":"wan6","up":true,"l3_device":"wan0","route":[{"target":"::","mask":0,"nexthop":"2001:db8:10::1"}]}]}' > "$temporary/wan.json"
 echo "Testing WAN routing with $backend interception"
 ip netns exec "$router" env PATH="$temporary:$PATH" REMOTEGATE_DIRECT_NETNS_TEST=1 REMOTEGATE_FIREWALL_BACKEND="$backend" REMOTEGATE_WAN_FIXTURE="$temporary/wan.json" REMOTEGATE_GUARD_PATH="$temporary/route-guard" "$test_binary" -test.run '^TestDirectWANIsolatedNetwork$' -test.v
done
