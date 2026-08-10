"""Plugin package lifecycle adapter tests."""

from __future__ import annotations

import base64
import json
import os
from pathlib import Path

from backend.infrastructure.plugin_package_lifecycle import LocalPluginPackageLifecycle


def _write_plugin(root: Path) -> None:
    worker = root / "dist" / "workers" / "read.js"
    worker.parent.mkdir(parents=True)
    (root / "manifest.json").write_text(
        json.dumps(
            {
                "$schema": "vibetable.plugin-manifest.v1",
                "pluginId": "com.example.reader",
                "version": "1.0.0",
                "displayName": {"en": "Reader"},
                "compatibility": {
                    "minHostVersion": "1.0.0",
                    "pluginApi": "1.x",
                },
                "permissions": {
                    "data": [],
                    "files": [],
                    "privateStorage": False,
                },
                "actions": [
                    {
                        "actionId": "read",
                        "displayName": {"en": "Read"},
                        "mode": "local",
                        "risk": "read",
                        "workerEntry": "dist/workers/read.js",
                    }
                ],
                "ui": {"customViews": []},
            }
        ),
        encoding="utf-8",
    )
    worker.write_text("export async function run() {}", encoding="utf-8")


def test_local_lifecycle_inspects_retains_and_discards_package(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    lifecycle = LocalPluginPackageLifecycle(tmp_path / "cache")

    inspected = lifecycle.inspect(str(source))
    retained_location = lifecycle.retain(
        source_location=inspected.source_location,
        expected_hash=inspected.package_hash,
    )
    retained = Path(retained_location)
    retained_inspection = lifecycle.inspect(retained_location)
    digest = bytes.fromhex(inspected.package_hash.removeprefix("sha256:"))
    compact = base64.b32encode(digest).rstrip(b"=").decode("ascii").lower()

    assert inspected.source_type == "local-folder"
    assert retained_inspection.source_type == "package"
    assert retained_inspection.package_hash == inspected.package_hash
    assert retained.name == f"{compact}.vtplugin"
    assert lifecycle.is_available(retained_location)

    lifecycle.discard(retained_location)

    assert not lifecycle.is_available(retained_location)


def test_local_lifecycle_retains_package_under_deep_windows_cache_path(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    cache = tmp_path
    while len(str(cache.resolve())) + len("\\deep") <= 233:
        cache /= "deep"
    remaining = 233 - len(str(cache.resolve())) - 1
    if remaining > 0:
        cache /= "x" * remaining
    lifecycle = LocalPluginPackageLifecycle(cache)
    inspected = lifecycle.inspect(str(source))

    retained_location = lifecycle.retain(
        source_location=inspected.source_location,
        expected_hash=inspected.package_hash,
    )

    digest = bytes.fromhex(inspected.package_hash.removeprefix("sha256:"))
    compact = base64.b32encode(digest).rstrip(b"=").decode("ascii").lower()
    assert len(compact) == 52
    assert len(str((cache / ".tmp-0000000000000000.vtplugin").resolve())) > 260
    assert len(str((cache / ("f" * 64 + ".vtplugin")).resolve())) > 260
    assert len(str((cache / f"{compact}.vtplugin").resolve())) > 260
    if os.name == "nt":
        assert retained_location.startswith("\\\\?\\")
    assert lifecycle.is_available(retained_location)
    assert lifecycle.inspect(retained_location).package_hash == inspected.package_hash
    assert not any(Path(retained_location).parent.glob(".tmp-*.vtplugin"))
