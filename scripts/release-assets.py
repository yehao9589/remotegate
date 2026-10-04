"""Validate bundled router packages and prepare server-only Release assets."""
import hashlib
import io
import json
from pathlib import Path
import re
import tarfile

release = json.loads(Path("internal/buildinfo/release.json").read_text(encoding="utf-8"))
version = release["version"]
assert re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", version)
package_version = f"{version}-{release['packageRevision']}"
tag = "v" + version
output = Path("dist/releases") / tag
output.mkdir(parents=True, exist_ok=True)
filename = f"RemoteGate-{package_version}-istore.run"
raw = (Path("dist") / filename).read_bytes()
manifest = json.loads((Path("dist") / (filename + ".json")).read_text(encoding="utf-8"))
assert hashlib.sha256(raw).hexdigest() == manifest["sha256"], "Installer checksum mismatch"
assert manifest["packageVersion"] == package_version and manifest["agentVersion"] == version
assert manifest["release"]["version"] == version
_, marker, payload = raw.partition(b"__PAYLOAD__\n")
assert marker, "Installer payload missing"
with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as archive:
    for arch in ("amd64", "arm64", "armv7"):
        entry = archive.getmember(f"remotegate-{arch}.ipk")
        assert entry.isfile(), "Expected a regular IPK file"
        assert entry.size > 0, "Empty router package"

bundle = output / f"RemoteGate-{tag}-compose.tar.gz"
with tarfile.open(bundle, "w:gz") as archive:
    for name in ("docker-compose.yml", "docker-compose.https.yml", "DEPLOYMENT.md", "README.md", "CHANGELOG.md"):
        archive.add(name, arcname=name)
    environment = Path(".env.example").read_text(encoding="utf-8")
    environment = re.sub(r"(?m)^REMOTE_GATE_IMAGE=.*$", "REMOTE_GATE_IMAGE=ghcr.io/yehao9589/remotegate:stable", environment)
    for name, content in ((".env.example", environment), ("VERSION.json", json.dumps(release, indent=2)+"\n")):
        encoded = content.encode()
        entry = tarfile.TarInfo(name)
        entry.size, entry.mode = len(encoded), 0o644
        archive.addfile(entry, io.BytesIO(encoded))

checksum_file = output / f"SHA256SUMS-{tag}.txt"
assert {path.name for path in output.iterdir()} <= {bundle.name, checksum_file.name}, "Unexpected Release asset"
checksum_file.write_text(f"{hashlib.sha256(bundle.read_bytes()).hexdigest()}  {bundle.name}\n", encoding="utf-8")
changelog = Path("CHANGELOG.md").read_text(encoding="utf-8")
section = re.search(r"(?ms)^## " + re.escape(tag) + r"(?:[^\n]*)\n(.*?)(?=^## |\Z)", changelog)
assert section, "Release notes missing from CHANGELOG.md"
notes = section.group(1).strip() + f"\n\nRelease 附件仅提供服务端部署包。路由器插件请从管理后台下载，或执行后台生成的一键安装命令。\n\n下载附件后可使用 `sha256sum -c SHA256SUMS-{tag}.txt` 检查完整性。\n"
Path("dist/release-notes.md").write_text(notes, encoding="utf-8")
print("Prepared", tag, "Release assets and checksums")
