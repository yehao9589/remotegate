#!/bin/sh
set -eu

# Select the package/userland ABI before falling back to the kernel machine.
remotegate_arch() {
 native=''
 if command -v opkg >/dev/null 2>&1; then
  native=$(opkg print-architecture 2>/dev/null | awk '$1 == "arch" && $2 != "all" && $2 != "noarch" && $3 ~ /^[0-9]+$/ {if (!found || $3+0 > priority) {name=$2; priority=$3+0; found=1}} END {print name}')
 fi
 if [ -n "$native" ]; then
  case "$native" in
   x86_64|amd64) printf '%s\n' amd64;;
   aarch64|aarch64_*|arm64) printf '%s\n' arm64;;
   arm_cortex-a*|armv7l|armv8l) printf '%s\n' armv7;;
   *) echo "Unsupported package architecture: $native (supported: x86_64, ARM64, ARMv7)" >&2; return 1;;
  esac
 else
  case "$(uname -m)" in
   x86_64) printf '%s\n' amd64;;
   aarch64|arm64) printf '%s\n' arm64;;
   armv7l|armv8l) printf '%s\n' armv7;;
   *) echo 'Unsupported CPU architecture (supported: x86_64, ARM64, ARMv7)' >&2; return 1;;
  esac
 fi
}

arch=$(remotegate_arch) || exit 1
BINARY="remotegate-agent-linux-$arch"

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
