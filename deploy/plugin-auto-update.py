#!/usr/bin/env python3
"""Install published CPA prompt plugin updates when Bridge is idle (Linux)."""
import argparse
import fcntl
import hashlib
import io
import json
import os
import pathlib
import re
import subprocess
import tempfile
import time
import urllib.request
import zipfile
from datetime import datetime, timezone

import yaml

REPOSITORY = "AndyYJF/cliproxy-antigravity-acp"

def version(value):
    match = re.fullmatch(r"v?(\d+)\.(\d+)\.(\d+)", str(value))
    if not match:
        raise ValueError("Not a stable semantic version")
    return tuple(map(int, match.groups()))

def download(url):
    request = urllib.request.Request(url, headers={"User-Agent": "cpa-acp-plugin-updater"})
    with urllib.request.urlopen(request, timeout=90) as response:
        return response.read()

def latest_release():
    return json.loads(download("https://api.github.com/repos/" + REPOSITORY + "/releases/latest"))

def plugin_config(config_path):
    data = yaml.safe_load(pathlib.Path(config_path).read_text())
    return data["plugins"]["configs"]["antigravity-acp"]

def replace_version(config_path, expected, replacement):
    path = pathlib.Path(config_path)
    text = path.read_text()
    node = yaml.compose(text)
    for key in ("plugins", "configs", "antigravity-acp", "store", "version"):
        node = next(value for field, value in node.value if field.value == key)
    if node.value != expected:
        raise RuntimeError("Plugin selection changed during update")
    # Keep the original inode: the CPA container bind-mounts this config file.
    with path.open("r+", encoding="utf-8") as output:
        output.write(text[:node.start_mark.index] + replacement + text[node.end_mark.index:])
        output.truncate()
        output.flush()
        os.fsync(output.fileno())

def bridge_health(config):
    info = json.loads(subprocess.check_output(["docker", "inspect", config["bridge_container"]]))[0]
    address = info["NetworkSettings"]["Networks"][config.get("bridge_network", "cpanet")]["IPAddress"]
    token = pathlib.Path(config["bridge_token_file"]).read_text().strip()
    request = urllib.request.Request("http://" + address + ":8328/healthz",
                                    headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(request, timeout=8) as response:
        return json.load(response)

def loaded_version(config):
    url = config["cpa_base_url"].rstrip("/") + "/v0/resource/plugins/antigravity-acp/version"
    return json.loads(download(url))

def wait_idle(config, minimum):
    deadline = time.monotonic() + config.get("idle_seconds", 30)
    while True:
        health = bridge_health(config)
        if version(health["version"]) < version(minimum):
            raise RuntimeError("Bridge is older than this plugin update requires")
        if health["active_requests"] or health["concurrency"]["queued"]:
            return False
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return True
        time.sleep(min(5, remaining))

def verify(config, target, upstream):
    for _ in range(20):
        try:
            data = loaded_version(config)
            if data["plugin_version"] == target and data["cpa_cloak_commit"] == upstream:
                bridge_health(config)
                return
        except Exception:
            pass
        time.sleep(1)
    raise RuntimeError("New plugin did not pass loaded-version verification")

def update(config, report):
    selected = plugin_config(config["cpa_config"])
    if not selected.get("enabled", True):
        report(status="disabled")
        return
    old = str(selected["store"]["version"])
    release = latest_release()
    target = release["tag_name"].removeprefix("v")
    if release["draft"] or release["prerelease"] or version(target) <= version(old):
        report(status="current", version=old)
        return
    assets = {asset["name"]: asset["browser_download_url"] for asset in release["assets"]}
    if "auto-update.json" not in assets:
        report(status="manual_release", version=old, available=target)
        return
    manifest = json.loads(download(assets["auto-update.json"]))
    if manifest["schema"] != 1 or manifest["kind"] != "cpa-cloak-plugin" or manifest["version"] != target:
        raise RuntimeError("Unsupported automatic update manifest")
    name = "antigravity-acp_" + target + "_linux_amd64.zip"
    checksums = {}
    for line in download(assets["checksums.txt"]).decode().splitlines():
        digest, filename = line.split(None, 1)
        checksums[filename.strip()] = digest
    package = download(assets[name])
    if hashlib.sha256(package).hexdigest() != checksums[name]:
        raise RuntimeError("Plugin package checksum mismatch")
    with zipfile.ZipFile(io.BytesIO(package)) as archive:
        binary = archive.read("antigravity-acp-v" + target + ".so")
    if not wait_idle(config, manifest["minimum_bridge_version"]):
        report(status="busy", version=old, available=target)
        return
    directory = pathlib.Path(config["plugin_dir"])
    destination = directory / ("antigravity-acp-v" + target + ".so")
    backup = pathlib.Path(config["state_dir"]) / ("before-" + target + ".yaml")
    backup.write_bytes(pathlib.Path(config["cpa_config"]).read_bytes())
    backup.chmod(0o600)
    fd, pending = tempfile.mkstemp(prefix=".acp-update-", suffix=".tmp", dir=directory)
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(binary)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(pending, 0o755)
        os.replace(pending, destination)
    finally:
        if os.path.exists(pending):
            os.unlink(pending)
    replace_version(config["cpa_config"], old, target)
    try:
        verify(config, target, manifest["cpa"]["commit"])
    except Exception:
        replace_version(config["cpa_config"], target, old)
        report(status="rolled_back", version=old, failed_version=target)
        raise
    report(status="updated", version=target, previous_version=old, cpa=manifest["cpa"]["tag"])

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    args = parser.parse_args()
    config = json.loads(pathlib.Path(args.config).read_text())
    state = pathlib.Path(config["state_dir"])
    state.mkdir(parents=True, exist_ok=True, mode=0o700)
    def report(**values):
        values["checked_at"] = datetime.now(timezone.utc).isoformat()
        temp = state / "status.json.next"
        temp.write_text(json.dumps(values, ensure_ascii=False, indent=2) + "\n")
        temp.replace(state / "status.json")
        print(json.dumps(values, ensure_ascii=False), flush=True)
    with (state / "update.lock").open("w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("Another update check is already running")
            return
        try:
            update(config, report)
        except Exception as error:
            print("Update failed: " + str(error), flush=True)
            raise SystemExit(1)

if __name__ == "__main__":
    main()
