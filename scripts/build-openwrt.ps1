$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force dist | Out-Null
$targets = @(
    @{ GOARCH='amd64'; Name='remotegate-agent-linux-amd64' },
    @{ GOARCH='arm64'; Name='remotegate-agent-linux-arm64' },
    @{ GOARCH='arm'; GOARM='7'; Name='remotegate-agent-linux-armv7' }
)
foreach ($target in $targets) {
    $env:GOOS='linux'; $env:GOARCH=$target.GOARCH; $env:CGO_ENABLED='0'
    if ($target.GOARM) { $env:GOARM=$target.GOARM } else { Remove-Item Env:GOARM -ErrorAction SilentlyContinue }
    go build -trimpath -ldflags '-s -w' -o (Join-Path dist $target.Name) ./cmd/agent
}
Copy-Item deploy/openwrt/remotegate.init,deploy/openwrt/remotegate-route-guard,deploy/openwrt/agent.json.example dist/
