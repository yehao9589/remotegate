"""Check real installer payloads without extracting archive-controlled paths."""
import hashlib
import io
import json
from pathlib import Path
import struct
import subprocess
import sys
import tarfile
import tempfile

package = Path(sys.argv[1])
raw = package.read_bytes()
manifest = json.loads(Path(str(package) + ".json").read_text(encoding="utf-8"))
assert hashlib.sha256(raw).hexdigest() == manifest["sha256"]
assert len(raw) == manifest["size"]
header, marker, payload = raw.partition(b"\n__PAYLOAD__\n")
assert marker and header.startswith(b"#!/bin/sh\n")

def read(archive, name):
    member = archive.getmember(name)
    assert member.isfile() and 0 <= member.size <= 128 * 1024 * 1024, name
    return archive.extractfile(member).read()

with tempfile.TemporaryDirectory() as directory, tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as installer:
    for arch, machine in (("amd64", 62), ("arm64", 183), ("armv7", 40)):
        ipk = read(installer, f"remotegate-{arch}.ipk")
        with tarfile.open(fileobj=io.BytesIO(ipk), mode="r:gz") as outer:
            control = read(outer, "control.tar.gz")
            data = read(outer, "data.tar.gz")
        with tarfile.open(fileobj=io.BytesIO(control), mode="r:gz") as files:
            assert f"Version: {manifest['packageVersion']}\n".encode() in read(files, "control")
            assert read(files, "conffiles") == b"/etc/config/remotegate\n"
            postinst = read(files, "postinst")
            assert postinst == Path("deploy/openwrt/remotegate.postinst").read_bytes().replace(b"\r\n", b"\n")
            target = Path(directory) / (arch + "-postinst")
            target.write_bytes(postinst)
            subprocess.run(["sh", "-n", str(target)], check=True)
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as files:
            binary = read(files, "usr/sbin/remotegate-agent")
            assert binary[:4] == b"\x7fELF" and binary[5] == 1
            assert struct.unpack_from("<H", binary, 18)[0] == machine, arch
            assert read(files, "usr/share/remotegate/version").decode().strip() == manifest["packageVersion"]
            configuration = read(files, "etc/config/remotegate")
            assert b"option token ''" in configuration and b"option enabled '0'" in configuration
            for name in ("usr/libexec/remotegate-route-guard", "etc/init.d/remotegate"):
                script = read(files, name)
                assert b"\r\n" not in script, name
                target = Path(directory) / (arch + "-" + Path(name).name)
                target.write_bytes(script)
                subprocess.run(["sh", "-n", str(target)], check=True)
            generator = Path(directory) / "config.lua"
            generator.write_bytes(read(files, "usr/libexec/remotegate-config"))
            subprocess.run(["lua5.1", "scripts/test-router-config.lua", str(generator)], check=True)
        print(f"PASS: {arch}, ELF machine {machine}, version {manifest['packageVersion']}, preserved UCI config and valid service scripts")
