#!/usr/bin/env python3
"""Package only release binaries and public deployment files."""
import hashlib
import io
import pathlib
import re
import sys
import tarfile
import zipfile

tag = sys.argv[1]
if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", tag):
    raise SystemExit("Expected tag vX.Y.Z or vX.Y.Z-prerelease")
version = tag[1:]
root = pathlib.Path(__file__).resolve().parent.parent
built = root / "build/release"
dist = root / "dist"
dist.mkdir(exist_ok=True)
plugin = built / f"antigravity-acp-v{version}.so"
bridge = built / "acp-bridge"

# Naming follows CPA's plugin release installer.
plugin_zip = dist / f"antigravity-acp_{version}_linux_amd64.zip"
with zipfile.ZipFile(plugin_zip, "w", zipfile.ZIP_DEFLATED) as archive:
    archive.write(plugin, plugin.name)
    archive.write(root / "LICENSE-CPA", "LICENSE-CPA")
    archive.write(root / "NOTICE", "NOTICE")

bundle = dist / f"cliproxy-antigravity-acp_{version}_linux_amd64.tar.gz"
with tarfile.open(bundle, "w:gz") as archive:
    archive.add(plugin, arcname="bin/antigravity-acp.so")
    archive.add(bridge, arcname="bin/acp-bridge")
    for name in [".dockerignore", "Dockerfile", "README.md", "CHANGELOG.md",
                 "LICENSE-CPA", "NOTICE", "clients", "management-center"]:
        archive.add(root / name, arcname=name)
    for name in ["compose.yaml", "deploy/cpa-plugin.example.yaml"]:
        content = (root / name).read_text(encoding="utf-8")
        if name == "compose.yaml":
            content = re.sub(r"cliproxy-antigravity-acp:[^}\s]+",
                             f"cliproxy-antigravity-acp:{version}", content)
        else:
            content = re.sub(r"version: [^\s]+", f"version: {version}", content)
        data = content.encode()
        entry = tarfile.TarInfo(name)
        entry.size = len(data)
        entry.mode = 0o644
        archive.addfile(entry, io.BytesIO(data))
    data = (version + "\n").encode()
    entry = tarfile.TarInfo("VERSION")
    entry.size = len(data)
    entry.mode = 0o644
    archive.addfile(entry, io.BytesIO(data))

bridge_asset = dist / "acp-bridge-linux-amd64"
bridge_asset.write_bytes(bridge.read_bytes())
bridge_asset.chmod(0o755)
assets = [plugin_zip, bundle, bridge_asset]
(dist / "checksums.txt").write_text(
    "".join(f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n" for p in assets),
    encoding="utf-8",
)
print("\n".join(str(p.relative_to(root)) for p in assets))
