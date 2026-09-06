#!/usr/bin/env python3
"""Append v0.5.0 formal producer inputs without changing historical asset bytes."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
CORPUS = ROOT / "compatibility-corpus.json"
FIXTURES = ROOT / "fixtures" / "formal-v0.5.0"
NEGATIVE = FIXTURES / "truncated.vtsnapshot"
NOTE = (
    "v0.5.0 formal producer inputs are collected; cases describe required consumer outcomes, "
    "not observed compatibility. Runtime verification and an independent remote-main anchor "
    "remain pending; the immutable first-release baseline is preserved."
)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    plain = (FIXTURES / "plain.vtsnapshot").read_bytes()
    # The formal exporter writes a ZIP with no archive comment. Remove only its
    # end-of-central-directory record for a distinct, reproducible rejection input.
    if plain[-22:-18] != b"PK\x05\x06" or plain[-2:] != b"\0\0":
        raise SystemExit("formal plain snapshot has an unexpected ZIP ending")
    truncated = plain[:-22]
    artifacts = []
    for artifact_id, kind, name in (
        ("v050-workspace", "workspace-archive", "workspace.zip"),
        ("v050-plain", "snapshot-package", "plain.vtsnapshot"),
        ("v050-passphrase", "snapshot-package", "passphrase.vtsnapshot"),
        ("v050-truncated", "snapshot-package", "truncated.vtsnapshot"),
    ):
        path = FIXTURES / name
        content = truncated if path == NEGATIVE else path.read_bytes()
        artifacts.append(
            {
                "id": artifact_id,
                "kind": kind,
                "path": path.relative_to(ROOT).as_posix(),
                "sha256": hashlib.sha256(content).hexdigest(),
            }
        )
    entry = {
        "writerVersion": "0.5.0",
        "sourceRelease": {
            "tag": "v0.5.0",
            "sourceCommit": "9a3077b019eb94fba30761f21b6442fc7034fea8",
            "assetName": "VibeTable-v0.5.0-win-x64.zip",
        },
        "artifacts": artifacts,
        "cases": [
            {"operation": operation, "artifactId": artifact_id, "expected": expected}
            for operation, artifact_id, expected in (
                ("workspace.open", "v050-workspace", "migrate"),
                ("snapshot.import", "v050-plain", "migrate"),
                ("snapshot.import", "v050-passphrase", "migrate"),
                ("snapshot.import", "v050-truncated", "reject-zero-write"),
            )
        ],
    }
    corpus = json.loads(CORPUS.read_text(encoding="utf-8"))
    releases = corpus["previousFormalReleases"]
    existing = [item for item in releases if item["writerVersion"] == "0.5.0"]
    if existing and existing != [entry]:
        raise SystemExit("refusing to rewrite an existing v0.5.0 corpus entry")
    if args.check:
        if existing != [entry] or not NEGATIVE.is_file() or NEGATIVE.read_bytes() != truncated:
            raise SystemExit("v0.5.0 corpus entry or rejection fixture is stale")
        return 0
    if NEGATIVE.exists() and NEGATIVE.read_bytes() != truncated:
        raise SystemExit("refusing to overwrite a different rejection fixture")
    if not existing:
        releases.append(entry)
    corpus["previousFormalReleaseNote"] = NOTE
    NEGATIVE.write_bytes(truncated)
    CORPUS.write_text(json.dumps(corpus, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
