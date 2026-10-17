"""Exercise the real package post-install hook with isolated router commands."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class RouterLifecycleTests(unittest.TestCase):
    def check_hook(self, configured=True, restart_ok=True, offline_root=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / "configuration"
            original = b"config remotegate 'main'\n option device_id 'existing'\n option token 'fixture-only'\n"
            config.write_bytes(original)
            log, service, generator, hook = [root / name for name in ("calls", "service", "generator", "postinst")]
            service.write_text('#!/bin/sh\nprintf "%s\\n" "$1" >> "' + str(log) + '"\n' + ('[ "$1" != restart ]\n' if not restart_ok else 'exit 0\n'), encoding="utf-8")
            generator.write_text('#!/bin/sh\nexit ' + ('0' if configured else '1') + '\n', encoding="utf-8")
            service.chmod(0o700)
            generator.chmod(0o700)
            script = Path("deploy/openwrt/remotegate.postinst").read_text(encoding="utf-8")
            for original_path, replacement in (("/etc/config/remotegate", config), ("/etc/init.d/remotegate", service), ("/usr/libexec/remotegate-config", generator), ("/tmp/luci-indexcache", root / "cache")):
                script = script.replace(original_path, str(replacement))
            hook.write_text(script, encoding="utf-8")
            env = dict(os.environ, IPKG_INSTROOT="/offline-image" if offline_root else "")
            result = subprocess.run(["sh", str(hook)], env=env, capture_output=True, text=True, check=True)
            self.assertEqual(config.read_bytes(), original, "Existing identity was changed")
            return log.read_text().splitlines() if log.exists() else [], result.stdout

    def test_configured_router_starts_without_another_save(self):
        calls, output = self.check_hook()
        self.assertEqual(calls, ["enable", "restart"])
        self.assertIn("client restarted", output)

    def test_unconfigured_or_disabled_router_is_not_started(self):
        calls, output = self.check_hook(configured=False)
        self.assertEqual(calls, ["enable"])
        self.assertIn("Save & Apply", output)

    def test_failed_start_is_visible_without_claiming_a_connection(self):
        calls, output = self.check_hook(restart_ok=False)
        self.assertEqual(calls, ["enable", "restart"])
        self.assertIn("failed to start", output)
        self.assertNotIn("client restarted", output)

    def test_offline_image_build_has_no_live_router_side_effects(self):
        calls, _ = self.check_hook(offline_root=True)
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
