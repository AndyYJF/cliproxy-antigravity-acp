"""Exercise unattended update transitions using fake releases and a fake Bridge."""
import hashlib
import importlib.util
import io
import json
import pathlib
import sys
import tempfile
import types
import unittest
import zipfile
from unittest.mock import patch

if sys.platform == "win32":
    sys.modules.setdefault("fcntl", types.SimpleNamespace())
ROOT = pathlib.Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("plugin_updater", ROOT / "deploy/plugin-auto-update.py")
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)

class UpdateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = pathlib.Path(self.temp.name)
        (root / "plugins").mkdir()
        (root / "state").mkdir()
        self.path = root / "config.yaml"
        self.original = "# keep comment\nplugins:\n  configs:\n    antigravity-acp:\n      enabled: true\n      cpa_config_file: /config.yaml\n      store:\n        version: 0.12.0\nother-provider: unchanged\n"
        self.path.write_text(self.original)
        self.config = {"cpa_config": str(self.path), "plugin_dir": str(root / "plugins"),
                       "state_dir": str(root / "state"), "idle_seconds": 0}
        self.package_name = "antigravity-acp_0.13.0_linux_amd64.zip"
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr("antigravity-acp-v0.13.0.so", b"fixture-plugin")
        package = buffer.getvalue()
        self.data = {
            self.package_name: package,
            "checksums.txt": (hashlib.sha256(package).hexdigest() + "  " + self.package_name + "\n").encode(),
            "auto-update.json": json.dumps({"schema": 1, "kind": "cpa-cloak-plugin", "version": "0.13.0",
                "minimum_bridge_version": "0.12.0", "cpa": {"tag": "v8.0.72", "commit": "fixture"}}).encode(),
        }
        self.release = {"tag_name": "v0.13.0", "draft": False, "prerelease": False,
                        "assets": [{"name": k, "browser_download_url": k} for k in self.data]}
        self.health = {"version": "0.12.0", "active_requests": 0, "concurrency": {"queued": 0}}
        self.reports = []
        for name, value in [("latest_release", lambda: self.release),
                            ("download", lambda url: self.data[url]),
                            ("bridge_health", lambda config: self.health),
                            ("verify", lambda *args: None)]:
            item = patch.object(updater, name, value)
            item.start()
            self.addCleanup(item.stop)

    def run_update(self):
        updater.update(self.config, lambda **values: self.reports.append(values))

    def test_status_preserves_last_upgrade_and_exports_snapshot(self):
        self.config["status_file"] = str(pathlib.Path(self.temp.name) / "shared/status.json")
        with patch.object(updater.subprocess, "run", return_value=types.SimpleNamespace(returncode=0)):
            updater.record_status(self.config, {"status": "updated", "version": "0.13.0", "previous_version": "0.12.0"})
            updater.record_status(self.config, {"status": "current", "version": "0.13.0"})
            status = json.loads(pathlib.Path(self.config["status_file"]).read_text())
            self.assertEqual(status["status"], "current")
            self.assertEqual(status["last_update"]["status"], "updated")
            self.assertTrue(status["enabled"])
            updater.record_status(self.config, {"status": "failed", "error": "fixture network error"})
            status = json.loads(pathlib.Path(self.config["status_file"]).read_text())
            self.assertEqual(status["error"], "fixture network error")
            self.assertEqual(status["last_update"]["version"], "0.13.0")

    def test_idle_update_preserves_other_config(self):
        self.run_update()
        self.assertEqual(self.path.read_text(), self.original.replace("version: 0.12.0", "version: 0.13.0"))
        self.assertEqual(self.reports[-1]["status"], "updated")
        self.assertEqual((pathlib.Path(self.config["plugin_dir"]) / "antigravity-acp-v0.13.0.so").read_bytes(), b"fixture-plugin")

    def test_busy_leaves_production_unchanged(self):
        self.health["active_requests"] = 1
        self.run_update()
        self.assertEqual(self.path.read_text(), self.original)
        self.assertEqual(self.reports[-1]["status"], "busy")
        self.assertEqual(list(pathlib.Path(self.config["plugin_dir"]).iterdir()), [])

    def test_bad_checksum_never_installs(self):
        self.data[self.package_name] += b"corrupted"
        with self.assertRaisesRegex(RuntimeError, "checksum"):
            self.run_update()
        self.assertEqual(self.path.read_text(), self.original)
        self.assertEqual(list(pathlib.Path(self.config["plugin_dir"]).iterdir()), [])

    def test_failed_reload_restores_previous_selection(self):
        with patch.object(updater, "verify", side_effect=RuntimeError("fixture reload failure")):
            with self.assertRaisesRegex(RuntimeError, "fixture reload"):
                self.run_update()
        self.assertEqual(self.path.read_text(), self.original)
        self.assertEqual(self.reports[-1]["status"], "rolled_back")

if __name__ == "__main__":
    unittest.main()
