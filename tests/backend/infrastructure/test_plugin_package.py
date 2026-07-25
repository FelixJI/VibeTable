"""Provider-neutral package safety and reproducibility tests."""

from __future__ import annotations

import hashlib
import json
import stat
import zipfile
from pathlib import Path

import pytest

from backend.infrastructure.plugin_package import (
    PackagePolicy,
    PluginPackageError,
    inspect_plugin_package,
    pack_plugin,
)


def _write_local_plugin(root: Path) -> None:
    (root / "dist" / "workers").mkdir(parents=True)
    (root / "schemas").mkdir()
    manifest = {
        "$schema": "vibetable.plugin-manifest.v1",
        "pluginId": "com.example.reader",
        "version": "1.0.0",
        "displayName": {"en": "Reader"},
        "compatibility": {
            "minHostVersion": "1.0.0",
            "pluginApi": "1.x",
        },
        "permissions": {
            "data": [
                {
                    "collection": "$active",
                    "operations": ["read"],
                    "fields": ["*"],
                }
            ],
            "files": [],
            "privateStorage": False,
        },
        "actions": [
            {
                "actionId": "read-selection",
                "displayName": {"en": "Read selection"},
                "mode": "local",
                "risk": "read",
                "workerEntry": "dist/workers/read.js",
                "inputSchema": "schemas/input.json",
                "outputSchema": "schemas/output.json",
            }
        ],
        "ui": {"customViews": []},
    }
    (root / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    (root / "schemas" / "input.json").write_text('{"type":"object"}', encoding="utf-8")
    (root / "schemas" / "output.json").write_text('{"type":"object"}', encoding="utf-8")
    (root / "dist" / "workers" / "read.js").write_text(
        "export default async function run() {}",
        encoding="utf-8",
    )


def test_inspect_local_package_returns_normalized_files_and_hash(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)

    inspected = inspect_plugin_package(plugin)

    assert inspected.manifest["pluginId"] == "com.example.reader"
    assert [item.path for item in inspected.files] == [
        "dist/workers/read.js",
        "manifest.json",
        "schemas/input.json",
        "schemas/output.json",
    ]
    expected = hashlib.sha256(b"export default async function run() {}").hexdigest()
    assert inspected.files[0].sha256 == expected
    assert inspected.package_hash.startswith("sha256:")


@pytest.mark.parametrize("member", ["/absolute.json", "../escape.json", "C:/drive.json"])
def test_archive_rejects_paths_outside_package(tmp_path: Path, member: str) -> None:
    package = tmp_path / "unsafe.vtplugin"
    with zipfile.ZipFile(package, "w") as archive:
        archive.writestr("manifest.json", "{}")
        archive.writestr(member, "unsafe")

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(package)

    assert error.value.code == "unsafe_path"


def test_package_policy_limits_file_count(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin, policy=PackagePolicy(max_file_count=3))

    assert error.value.code == "file_count_limit"


def test_pack_is_deterministic_and_archive_integrity_is_verified(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    first = tmp_path / "first.vtplugin"
    second = tmp_path / "second.vtplugin"

    first_hash = pack_plugin(plugin, first)
    second_hash = pack_plugin(plugin, second)

    assert first.read_bytes() == second.read_bytes()
    assert first_hash == second_hash == inspect_plugin_package(first).package_hash
    with zipfile.ZipFile(first) as archive:
        integrity = json.loads(archive.read("integrity.json"))
    assert (
        integrity["files"]["manifest.json"]
        == hashlib.sha256((plugin / "manifest.json").read_bytes()).hexdigest()
    )


def test_local_pack_excludes_development_inputs(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    (plugin / "src").mkdir()
    (plugin / "src" / "worker.ts").write_text("export {};", encoding="utf-8")
    (plugin / "node_modules" / "dependency").mkdir(parents=True)
    (plugin / "node_modules" / "dependency" / "index.js").write_text(
        "throw new Error('must not ship');",
        encoding="utf-8",
    )
    (plugin / "package.json").write_text("{}", encoding="utf-8")
    package = tmp_path / "reader.vtplugin"

    inspected = inspect_plugin_package(plugin)
    pack_plugin(plugin, package)

    assert all(not item.path.startswith(("src/", "node_modules/")) for item in inspected.files)
    with zipfile.ZipFile(package) as archive:
        assert "src/worker.ts" not in archive.namelist()
        assert "node_modules/dependency/index.js" not in archive.namelist()
        assert "package.json" not in archive.namelist()


def test_archive_rejects_duplicate_paths_symlinks_and_integrity_drift(
    tmp_path: Path,
) -> None:
    duplicate = tmp_path / "duplicate.vtplugin"
    with zipfile.ZipFile(duplicate, "w") as archive:
        archive.writestr("manifest.json", "{}")
        archive.writestr("schemas/value.json", "{}")
        archive.writestr("schemas/./value.json", "{}")
    with pytest.raises(PluginPackageError) as duplicate_error:
        inspect_plugin_package(duplicate)
    assert duplicate_error.value.code == "duplicate_path"

    symlink = tmp_path / "symlink.vtplugin"
    link = zipfile.ZipInfo("dist/link.js")
    link.create_system = 3
    link.external_attr = (stat.S_IFLNK | 0o777) << 16
    with zipfile.ZipFile(symlink, "w") as archive:
        archive.writestr("manifest.json", "{}")
        archive.writestr(link, "../../outside.js")
    with pytest.raises(PluginPackageError) as symlink_error:
        inspect_plugin_package(symlink)
    assert symlink_error.value.code == "symbolic_link"

    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    original = tmp_path / "original.vtplugin"
    changed = tmp_path / "changed.vtplugin"
    pack_plugin(plugin, original)
    with zipfile.ZipFile(original) as archive:
        members = {name: archive.read(name) for name in archive.namelist()}
    members["manifest.json"] += b" "
    with zipfile.ZipFile(changed, "w") as archive:
        for name, content in members.items():
            archive.writestr(name, content)
    with pytest.raises(PluginPackageError) as integrity_error:
        inspect_plugin_package(changed)
    assert integrity_error.value.code == "integrity_mismatch"
