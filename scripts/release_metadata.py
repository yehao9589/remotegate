"""Read the shared version and the matching Chinese changelog entry."""
import json
import os
from datetime import date, datetime, timezone
from pathlib import Path
import re


def load_release(root=Path("."), tag=None):
    root = Path(root)
    release = json.loads((root / "internal/buildinfo/release.json").read_text(encoding="utf-8"))
    version = release.get("version", "")
    if not isinstance(version, str) or not re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", version):
        raise ValueError("Invalid release version")
    revision = release.get("packageRevision")
    if type(revision) is not int or revision < 1:
        raise ValueError("Invalid plugin package revision")
    title = release.get("title", "")
    if not isinstance(title, str) or not title.strip() or len(title) > 80 or any(ord(c) < 32 or ord(c) == 127 for c in title):
        raise ValueError("Add a short release title without control characters")
    expected_tag = "v" + version
    if tag is not None and tag != expected_tag:
        raise ValueError("Git tag and release.json must match")
    changelog = (root / "CHANGELOG.md").read_text(encoding="utf-8")
    section = re.search(r"(?ms)^## " + re.escape(expected_tag) + r" · ([^\n]+)\n(.*?)(?=^## |\Z)", changelog)
    if not section or not section.group(2).strip():
        raise ValueError("Add update notes for the current release to CHANGELOG.md")
    release_date = section.group(1).strip()
    if release_date == "未发布":
        if tag is not None:
            raise ValueError("A tagged release needs a publication date")
    else:
        if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", release_date):
            raise ValueError("Use a YYYY-MM-DD release date")
        date.fromisoformat(release_date)
    return {
        "version": version,
        "tag": expected_tag,
        "title": title.strip(),
        "package_version": f"{version}-{revision}",
        "date": release_date,
        "notes": section.group(2).strip(),
    }


def main():
    tag = os.environ.get("GITHUB_REF_NAME") if os.environ.get("GITHUB_REF_TYPE") == "tag" else None
    release = load_release(tag=tag)
    if path := os.environ.get("GITHUB_OUTPUT"):
        with open(path, "a", encoding="utf-8") as output:
            for key in ("version", "tag", "title", "package_version"):
                output.write(f"{key}={release[key]}\n")
            output.write("built_at=" + datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") + "\n")
    if path := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(path, "a", encoding="utf-8") as output:
            output.write(f"## RemoteGate {release['tag']} · {release['title']}\n\n")
            output.write(f"路由器插件：`{release['package_version']}` · 更新说明日期：{release['date']}\n\n")
            output.write(release["notes"] + "\n")
    print(f"RemoteGate {release['tag']} · {release['title']}")
    print(f"Router plugin: {release['package_version']}")


if __name__ == "__main__":
    main()
