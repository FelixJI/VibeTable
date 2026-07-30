#!/usr/bin/env python3
"""Generate the deterministic first-release SnapshotPackage corpus fixture."""

from __future__ import annotations

import argparse
import hashlib
import json
import tempfile
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent
OUTPUT = ROOT / "fixtures" / "snapshot-package-v2.vtsnapshot"
SOURCE_NAMES = (
    "workspace-manifest.json",
    "snapshot-manifest.json",
    "retention-policy.json",
)


def _zip_info(name: str) -> zipfile.ZipInfo:
    info = zipfile.ZipInfo(name, date_time=(2026, 7, 28, 0, 0, 0))
    # This corpus is hash-pinned and must remain byte-identical across the
    # system and release Python runtimes. DEFLATE output is allowed to vary
    # with the linked zlib version, while these tiny JSON fixtures do not
    # benefit materially from compression.
    info.compress_type = zipfile.ZIP_STORED
    info.create_system = 3
    info.external_attr = 0o600 << 16
    return info


def render(path: Path) -> None:
    entries = {
        f"contracts/{name}": (ROOT / "fixtures" / name).read_bytes() for name in SOURCE_NAMES
    }
    workspace = json.loads(entries["contracts/workspace-manifest.json"])
    snapshot = json.loads(entries["contracts/snapshot-manifest.json"])
    manifest = {
        "metadata": {
            "formatVersion": 2,
            "workspaceId": workspace["workspaceId"],
            "snapshotId": snapshot["snapshotId"],
            "writerVersion": "0.1.0",
            "minimumAppVersion": "0.1.0",
        },
        "entries": {
            name: hashlib.sha256(content).hexdigest() for name, content in sorted(entries.items())
        },
    }
    manifest_raw = (
        json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    ).encode()
    with zipfile.ZipFile(path, "w", allowZip64=True, compression=zipfile.ZIP_STORED) as archive:
        archive.writestr(_zip_info("manifest.json"), manifest_raw)
        for name, content in sorted(entries.items()):
            archive.writestr(_zip_info(name), content)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if args.check:
        with tempfile.TemporaryDirectory() as directory:
            candidate = Path(directory) / OUTPUT.name
            render(candidate)
            if not OUTPUT.is_file() or OUTPUT.read_bytes() != candidate.read_bytes():
                raise SystemExit("compatibility SnapshotPackage fixture is stale")
        return 0
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    render(OUTPUT)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
