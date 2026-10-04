#!/bin/sh
set -eu

ARCH="$(uname -m)"
case "$ARCH" in
	x86_64) BINARY=remotegate-agent-linux-amd64 ;;
	aarch64) BINARY=remotegate-agent-linux-arm64 ;;
	armv7l) BINARY=remotegate-agent-linux-armv7 ;;
	*) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

BASE_URL="${BASE_URL:-https://downloads.example.com/remotegate}"
mkdir -p /etc/remotegate /usr/libexec
wget -O /usr/sbin/remotegate-agent "$BASE_URL/$BINARY"
wget -O /etc/init.d/remotegate "$BASE_URL/remotegate.init"
wget -O /usr/libexec/remotegate-route-guard "$BASE_URL/remotegate-route-guard"
chmod 0755 /usr/sbin/remotegate-agent /etc/init.d/remotegate /usr/libexec/remotegate-route-guard
[ -f /etc/remotegate/agent.json ] || wget -O /etc/remotegate/agent.json "$BASE_URL/agent.json.example"
chmod 0600 /etc/remotegate/agent.json
/etc/init.d/remotegate enable
echo "Edit /etc/remotegate/agent.json, then run: /etc/init.d/remotegate start"
