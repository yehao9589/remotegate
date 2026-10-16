import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from release_metadata import load_release, main


class ReleaseMetadataTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        (self.root / "internal/buildinfo").mkdir(parents=True)
        self.metadata = {"version": "0.1.10", "title": "安装更新中心与远程管理", "packageRevision": 3}
        self.notes = "# 更新记录\n\n## v0.1.10 · 2026-10-16\n\n- 新增安装模板。\n\n## v0.1.9 · 2026-10-13\n\n- 旧版本说明。\n"
        self.save()

    def save(self):
        (self.root / "internal/buildinfo/release.json").write_text(json.dumps(self.metadata), encoding="utf-8")
        (self.root / "CHANGELOG.md").write_text(self.notes, encoding="utf-8")

    def test_version_title_and_notes_share_one_source(self):
        release = load_release(self.root, "v0.1.10")
        self.assertEqual(release["title"], self.metadata["title"])
        self.assertEqual(release["package_version"], "0.1.10-3")
        self.assertIn("新增安装模板", release["notes"])
        self.assertNotIn("旧版本说明", release["notes"])

    def test_mismatched_tag_and_unreleased_tag_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "must match"):
            load_release(self.root, "v0.1.9")
        self.notes = self.notes.replace("2026-10-16", "未发布")
        self.save()
        self.assertEqual(load_release(self.root)["date"], "未发布")
        with self.assertRaisesRegex(ValueError, "publication date"):
            load_release(self.root, "v0.1.10")

    def test_missing_notes_or_invalid_date_prevent_release(self):
        for notes in (self.notes.replace("v0.1.10", "v0.1.11"), self.notes.replace("2026-10-16", "2026-02-30")):
            with self.subTest(notes=notes):
                self.notes = notes
                self.save()
                with self.assertRaises(ValueError):
                    load_release(self.root)

    def test_title_cannot_inject_github_output_and_revision_is_an_integer(self):
        for key, value in (("title", ""), ("title", "标题\nversion=9.0.0"), ("packageRevision", True), ("packageRevision", 0), ("version", "01.1.0")):
            with self.subTest(key=key, value=value):
                original = dict(self.metadata)
                self.metadata[key] = value
                self.save()
                with self.assertRaises(ValueError):
                    load_release(self.root)
                self.metadata = original

    def test_actions_output_and_summary_include_the_matching_chinese_notes(self):
        output, summary = self.root / "outputs.txt", self.root / "summary.md"
        environment = {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v0.1.10", "GITHUB_OUTPUT": str(output), "GITHUB_STEP_SUMMARY": str(summary)}
        release = load_release(self.root, "v0.1.10")
        with patch.dict(os.environ, environment), patch("release_metadata.load_release", return_value=release), contextlib.redirect_stdout(io.StringIO()):
            main()
        self.assertIn("version=0.1.10\n", output.read_text(encoding="utf-8"))
        self.assertIn("title=安装更新中心与远程管理\n", output.read_text(encoding="utf-8"))
        self.assertIn("RemoteGate v0.1.10", summary.read_text(encoding="utf-8"))
        self.assertIn("新增安装模板", summary.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
