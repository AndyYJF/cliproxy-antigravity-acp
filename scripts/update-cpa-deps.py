#!/usr/bin/env python3
"""Set direct dependency versions from the pinned CPA manifest; go mod tidy follows."""
import json
import pathlib
import re
root = pathlib.Path(__file__).resolve().parent.parent
data = json.loads((root / "internal/cpacloak/upstream.json").read_text(encoding="utf-8"))
path = root / "go.mod"
text = path.read_text(encoding="utf-8")
for name, version in data["dependencies"].items():
    pattern = r"(?m)^(\s*(?:require\s+)?" + re.escape(name) + r")\s+\S+"
    if re.search(pattern, text):
        text = re.sub(pattern, lambda m: m.group(1) + " " + version, text)
    else:
        text += "\nrequire " + name + " " + version + "\n"
path.write_text(text, encoding="utf-8")
