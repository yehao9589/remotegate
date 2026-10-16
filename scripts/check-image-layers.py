"""Check an actual docker image save archive without extracting its files."""
import json
from pathlib import PurePosixPath
import sys
import tarfile


SERVER = "usr/local/bin/remotegate-server"
ENTRYPOINT = "usr/local/bin/docker-entrypoint.sh"


def check_image(path):
    server_copies, entrypoint_modes, installers = [], [], []
    with tarfile.open(path, "r:*") as image:
        with image.extractfile("manifest.json") as manifest_file:
            manifests = json.load(manifest_file)
        assert len(manifests) == 1, "Save one platform of the test image"
        for layer_name in manifests[0]["Layers"]:
            with image.extractfile(layer_name) as layer_file:
                with tarfile.open(fileobj=layer_file, mode="r|*") as layer:
                    for member in layer:
                        name = str(PurePosixPath(member.name)).removeprefix("/")
                        if name == SERVER and member.isfile():
                            server_copies.append(member.size)
                        if name == ENTRYPOINT and member.isfile():
                            entrypoint_modes.append(member.mode & 0o777)
                        if name.startswith("opt/remotegate/packages/") and name.endswith("-istore.run") and member.isfile():
                            installers.append(member.size)
    assert len(server_copies) == 1 and server_copies[0] > 0, (
        "The server must appear in exactly one image layer", server_copies)
    assert entrypoint_modes == [0o755], (
        "The entrypoint must be executable without a second chmod layer", entrypoint_modes)
    assert len(installers) == 1 and installers[0] > 0, (
        "The bundled router installer must remain present", installers)
    print("PASS: one server copy, executable entrypoint and bundled router installer")


if __name__ == "__main__":
    check_image(sys.argv[1])
