"""Collect versioned Release assets from the exact published Docker image."""
import hashlib
import io
import json
from pathlib import Path
import re
import shutil
import tarfile

release = json.loads(Path("internal/buildinfo/release.json").read_text(encoding="utf-8"))
version = release["version"]
assert re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", version)
package_version = f"{version}-{release['packageRevision']}"
tag = "v" + version
output = Path("dist/releases")
output.mkdir(parents=True, exist_ok=True)
filename = f"RemoteGate-{package_version}-istore.run"
shutil.copy2(Path("dist") / filename, output / filename)
shutil.copy2(Path("dist") / (filename + ".json"), output / (filename + ".json"))
raw = (output / filename).read_bytes()
manifest = json.loads((output / (filename + ".json")).read_text(encoding="utf-8"))
assert hashlib.sha256(raw).hexdigest() == manifest["sha256"], "Installer checksum mismatch"
assert manifest["packageVersion"] == package_version and manifest["agentVersion"] == version
assert manifest["release"]["version"] == version
_, marker, payload = raw.partition(b"__PAYLOAD__\n")
assert marker, "Installer payload missing"
with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as archive:
    for arch in ("amd64", "arm64", "armv7"):
        entry = archive.getmember(f"remotegate-{arch}.ipk")
        assert entry.isfile(), "Expected a regular IPK file"
        (output / f"RemoteGate-{package_version}-{arch}.ipk").write_bytes(archive.extractfile(entry).read())

bundle = output / f"RemoteGate-{tag}-compose.tar.gz"
with tarfile.open(bundle, "w:gz") as archive:
    for name in ("docker-compose.yml", "docker-compose.https.yml", "DEPLOYMENT.md", "README.md", "CHANGELOG.md"):
        archive.add(name, arcname=name)
    environment = Path(".env.example").read_text(encoding="utf-8")
    environment = re.sub(r"(?m)^REMOTE_GATE_IMAGE=.*$", f"REMOTE_GATE_IMAGE=ghcr.io/yehao9589/remotegate:{tag}", environment)
    for name, content in ((".env.example", environment), ("VERSION.json", json.dumps(release, indent=2)+"\n")):
        encoded = content.encode()
        entry = tarfile.TarInfo(name)
        entry.size, entry.mode = len(encoded), 0o644
        archive.addfile(entry, io.BytesIO(encoded))

checksum_file = output / f"SHA256SUMS-{tag}.txt"
checksum_file.write_text("".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n"
                                  for path in sorted(output.iterdir()) if path != checksum_file), encoding="utf-8")
changelog = Path("CHANGELOG.md").read_text(encoding="utf-8")
section = re.search(r"(?ms)^## " + re.escape(tag) + r"(?:[^\n]*)\n(.*?)(?=^## |\Z)", changelog)
assert section, "Release notes missing from CHANGELOG.md"
notes = section.group(1).strip() + f"\n\n下载附件后可使用 `sha256sum -c SHA256SUMS-{tag}.txt` 检查完整性。\n"
Path("dist/release-notes.md").write_text(notes, encoding="utf-8")
print("Prepared", tag, "Release assets and checksums")
