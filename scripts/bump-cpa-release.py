#!/usr/bin/env python3
"""Bump the plugin patch version after a verified CPA source synchronization."""
import json
import pathlib
import re

root = pathlib.Path(__file__).resolve().parent.parent
management = root / "cmd/plugin/management.go"
text = management.read_text()
old = re.search(r'var pluginVersion = "(\d+\.\d+\.\d+)"', text).group(1)
major, minor, patch = map(int, old.split("."))
version = f"{major}.{minor}.{patch + 1}"
management.write_text(text.replace(f'var pluginVersion = "{old}"', f'var pluginVersion = "{version}"'))
for name in ("README.md", "deploy/cpa-plugin.example.yaml", "cmd/plugin/web/index.html"):
    path = root / name
    path.write_text(path.read_text().replace(old, version), encoding="utf-8")
upstream = json.loads((root / "internal/cpacloak/upstream.json").read_text())
path = root / "CHANGELOG.md"
text = path.read_text()
index = text.index("\n## ")
text = text[:index] + f"\n## {version}\n\n- 自动同步 CPA {upstream['tag']} 的 AGV 提示词处理实现与原始测试。\n" + text[index:]
path.write_text(text, encoding="utf-8")
print("v" + version)
