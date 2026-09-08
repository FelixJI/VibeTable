"""Regenerate the declared PocketBase migration release metadata."""

from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path


def main() -> None:
    root = Path(__file__).resolve().parents[1]
    migrations = root / "sidecar" / "migrations"
    manifest_path = migrations / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    for entry in manifest["migrations"]:
        entry["sha256"] = hashlib.sha256((migrations / entry["source"]).read_bytes()).hexdigest()
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8", newline="\n")
    embed_path = migrations / "manifest.go"
    source = embed_path.read_text(encoding="utf-8")
    declaration = "//go:embed manifest.json " + " ".join(
        entry["source"] for entry in manifest["migrations"]
    )
    embed_path.write_text(
        re.sub(r"^//go:embed .*$", declaration, source, flags=re.MULTILINE),
        encoding="utf-8",
        newline="\n",
    )
    info_path = root / "sidecar" / "internal" / "buildinfo" / "info.go"
    info_path.write_text(
        re.sub(
            r'(SchemaVersion\s*=\s*")\d+',
            lambda match: match[1] + str(manifest["schemaVersion"]),
            info_path.read_text(encoding="utf-8"),
        ),
        encoding="utf-8",
        newline="\n",
    )


if __name__ == "__main__":
    main()
