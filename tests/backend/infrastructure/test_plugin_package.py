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


def _write_minimal_plugin(root: Path) -> None:
    (root / "schemas").mkdir(parents=True)
    (root / "flows").mkdir()
    manifest = {
        "$schema": "vibetable.plugin-manifest.v1",
        "pluginId": "com.example.reader",
        "version": "1.0.0",
        "displayName": {"zh-CN": "读取器"},
        "compatibility": {
            "minHostVersion": "1.0.0",
            "pluginApi": "1.x",
            "directus": ">=12.1 <13",
        },
        "permissions": {
            "data": [{"collection": "$active", "operations": ["read"], "fields": ["*"]}],
            "files": [],
            "privateStorage": False,
        },
        "actions": [
            {
                "actionId": "read-selection",
                "displayName": {"zh-CN": "读取选中项"},
                "mode": "flow",
                "risk": "read",
                "entryFlow": "read-selection",
                "inputSchema": "schemas/input.json",
                "outputSchema": "schemas/output.json",
            }
        ],
        "flows": [
            {
                "logicalFlowId": "read-selection",
                "ownership": "managed",
                "risk": "read",
                "definition": "flows/read.flow.json",
                "inputSchema": "schemas/input.json",
                "outputSchema": "schemas/output.json",
                "requiresOperations": [],
            }
        ],
    }
    (root / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    (root / "schemas" / "input.json").write_text('{"type":"object"}', encoding="utf-8")
    (root / "schemas" / "output.json").write_text('{"type":"object"}', encoding="utf-8")
    (root / "flows" / "read.flow.json").write_text('{"operations":[]}', encoding="utf-8")


def test_inspect_local_folder_returns_manifest_files_and_normalized_hash(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_minimal_plugin(plugin)

    inspected = inspect_plugin_package(plugin)

    assert inspected.manifest["pluginId"] == "com.example.reader"
    assert [item.path for item in inspected.files] == [
        "flows/read.flow.json",
        "manifest.json",
        "schemas/input.json",
        "schemas/output.json",
    ]
    assert inspected.files[0].sha256 == hashlib.sha256(b'{"operations":[]}').hexdigest()
    assert inspected.package_hash.startswith("sha256:")


@pytest.mark.parametrize("member", ["/absolute.json", "../escape.json", "C:/drive.json"])
def test_inspect_archive_rejects_paths_outside_package(tmp_path: Path, member: str) -> None:
    package = tmp_path / "unsafe.vtplugin"
    with zipfile.ZipFile(package, "w") as archive:
        archive.writestr("manifest.json", "{}")
        archive.writestr(member, "unsafe")

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(package)

    assert error.value.code == "unsafe_path"


def test_inspect_enforces_central_package_limits(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_minimal_plugin(plugin)
    policy = PackagePolicy(max_file_count=3)

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin, policy=policy)

    assert error.value.code == "file_count_limit"


def test_pack_is_deterministic_and_archive_integrity_is_verified(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_minimal_plugin(plugin)
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


def test_local_folder_ignores_development_inputs_when_inspecting_and_packing(
    tmp_path: Path,
) -> None:
    plugin = tmp_path / "reader"
    _write_minimal_plugin(plugin)
    (plugin / "src").mkdir()
    (plugin / "src" / "worker.ts").write_text("export {};", encoding="utf-8")
    (plugin / "node_modules" / "dependency").mkdir(parents=True)
    (plugin / "node_modules" / "dependency" / "index.js").write_text(
        "throw new Error('must not ship');", encoding="utf-8"
    )
    (plugin / "package.json").write_text("{}", encoding="utf-8")
    (plugin / "package-lock.json").write_text("{}", encoding="utf-8")
    package = tmp_path / "reader.vtplugin"

    inspected = inspect_plugin_package(plugin)
    pack_plugin(plugin, package)

    assert all(not item.path.startswith(("src/", "node_modules/")) for item in inspected.files)
    assert all(item.path not in {"package.json", "package-lock.json"} for item in inspected.files)
    with zipfile.ZipFile(package) as archive:
        assert "src/worker.ts" not in archive.namelist()
        assert "node_modules/dependency/index.js" not in archive.namelist()


def test_validate_rejects_manual_write_flow_without_confirmation_point(tmp_path: Path) -> None:
    plugin = tmp_path / "writer"
    _write_minimal_plugin(plugin)
    manifest_path = plugin / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["actions"][0]["risk"] = "write"
    manifest["flows"][0]["risk"] = "write"
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin)

    assert error.value.code == "confirmation_required"


def test_inspect_rejects_duplicate_normalized_archive_paths(tmp_path: Path) -> None:
    package = tmp_path / "duplicate.vtplugin"
    with zipfile.ZipFile(package, "w") as archive:
        archive.writestr("manifest.json", "{}")
        archive.writestr("schemas/value.json", "{}")
        archive.writestr("schemas/./value.json", "{}")

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(package)

    assert error.value.code == "duplicate_path"


def test_inspect_rejects_archive_symbolic_links(tmp_path: Path) -> None:
    package = tmp_path / "symlink.vtplugin"
    link = zipfile.ZipInfo("dist/link.js")
    link.create_system = 3
    link.external_attr = (stat.S_IFLNK | 0o777) << 16
    with zipfile.ZipFile(package, "w") as archive:
        archive.writestr("manifest.json", "{}")
        archive.writestr(link, "../../outside.js")

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(package)

    assert error.value.code == "symbolic_link"


def test_inspect_detects_content_changed_after_integrity_was_generated(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_minimal_plugin(plugin)
    original = tmp_path / "original.vtplugin"
    changed = tmp_path / "changed.vtplugin"
    pack_plugin(plugin, original)
    with zipfile.ZipFile(original) as archive:
        members = {name: archive.read(name) for name in archive.namelist()}
    members["manifest.json"] += b" "
    with zipfile.ZipFile(changed, "w") as archive:
        for name, content in members.items():
            archive.writestr(name, content)

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(changed)

    assert error.value.code == "integrity_mismatch"


def test_validate_rejects_write_before_confirmation(tmp_path: Path) -> None:
    plugin = tmp_path / "writer"
    _write_minimal_plugin(plugin)
    manifest_path = plugin / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["actions"][0]["risk"] = "write"
    manifest["flows"][0]["risk"] = "write"
    manifest["flows"][0]["requiresOperations"] = ["vibetable.confirm@1"]
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
    (plugin / "flows" / "read.flow.json").write_text(
        json.dumps(
            {
                "operations": [
                    {"type": "items.update"},
                    {"type": "vibetable.confirm@1"},
                ]
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin)

    assert error.value.code == "confirmation_order"


def test_validate_rejects_branch_that_bypasses_confirmation(tmp_path: Path) -> None:
    plugin = tmp_path / "writer"
    _write_minimal_plugin(plugin)
    manifest_path = plugin / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["actions"][0]["risk"] = "write"
    manifest["flows"][0]["risk"] = "write"
    manifest["flows"][0]["requiresOperations"] = ["vibetable.confirm@1"]
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
    (plugin / "flows" / "read.flow.json").write_text(
        json.dumps(
            {
                "operations": [
                    {"key": "branch", "type": "condition", "resolve": "confirm", "reject": "write"},
                    {"key": "confirm", "type": "vibetable.confirm@1", "resolve": "write"},
                    {"key": "write", "type": "items.update"},
                ]
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin)

    assert error.value.code == "confirmation_order"


def test_validate_rejects_unknown_declared_write_side_effect(tmp_path: Path) -> None:
    plugin = tmp_path / "writer"
    _write_minimal_plugin(plugin)
    manifest_path = plugin / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["actions"][0]["risk"] = "write"
    manifest["flows"][0]["risk"] = "write"
    manifest["flows"][0]["requiresOperations"] = ["vibetable.confirm@1"]
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
    (plugin / "flows" / "read.flow.json").write_text(
        json.dumps(
            {
                "operations": [
                    {"key": "custom", "type": "vendor.magic", "sideEffect": "unknown"},
                    {"key": "confirm", "type": "vibetable.confirm@1"},
                ]
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin)

    assert error.value.code == "unknown_write_operation"


def test_validate_rejects_schema_keywords_outside_closed_dialect(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_minimal_plugin(plugin)
    (plugin / "schemas" / "input.json").write_text(
        json.dumps({"type": "object", "$ref": "https://example.invalid/schema.json"}),
        encoding="utf-8",
    )

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin)

    assert error.value.code == "schema_invalid"
    assert "unsupported keyword" in str(error.value)


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("minHostVersion", "2.0.0"),
        ("pluginApi", "2.x"),
        ("directus", ">=13 <14"),
    ],
)
def test_validate_rejects_incompatible_platform_versions(
    tmp_path: Path, field: str, value: str
) -> None:
    plugin = tmp_path / "reader"
    _write_minimal_plugin(plugin)
    manifest_path = plugin / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["compatibility"][field] = value
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin)

    assert error.value.code == "version_incompatible"
    assert field in str(error.value)


def test_validate_rejects_unsupported_public_operation_version(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_minimal_plugin(plugin)
    manifest_path = plugin / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["flows"][0]["requiresOperations"] = ["vibetable.progress@2"]
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

    with pytest.raises(PluginPackageError) as error:
        inspect_plugin_package(plugin)

    assert error.value.code == "version_incompatible"
    assert "vibetable.progress@2" in str(error.value)
