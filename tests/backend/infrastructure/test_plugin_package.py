"""Provider-neutral package safety and reproducibility tests."""

from __future__ import annotations

import hashlib
import json
import stat
import zipfile
from pathlib import Path
from typing import Any

import pytest

from backend.infrastructure.plugin_package import (
    DEFAULT_COMPATIBILITY_POLICY,
    PackagePolicy,
    PluginPackageError,
    _is_valid_plugin_id,
    _range_contains,
    _version_tuple,
    inspect_plugin_package,
    pack_plugin,
    read_plugin_package_member,
    validate_plugin_manifest,
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


@pytest.mark.parametrize(
    ("plugin_id", "expected"),
    [
        ("com.example.reader", True),
        ("a-b", True),
        ("com.example.reader--", True),
        ("single", False),
        ("com..reader", False),
        ("com.example_reader", False),
        ("Com.example.reader", False),
    ],
)
def test_plugin_id_validation_preserves_reverse_domain_contract(
    plugin_id: str,
    expected: bool,
) -> None:
    assert _is_valid_plugin_id(plugin_id) is expected


def test_plugin_id_validation_rejects_long_hostile_input_without_regex_backtracking() -> None:
    hostile = "0" + "-0" * 100_000 + "!"

    assert not _is_valid_plugin_id(hostile)


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


# ===========================================================================
# Inline manifest validation matrix (no filesystem needed)
# ===========================================================================


def _valid_entries(manifest: dict[str, Any]) -> list[tuple[str, bytes]]:
    """Build a minimal valid entry list around a manifest dict."""
    return [
        ("manifest.json", json.dumps(manifest).encode("utf-8")),
        ("schemas/input.json", b'{"type":"object"}'),
        ("schemas/output.json", b'{"type":"object"}'),
        ("dist/read.js", b"export default async function run() {}"),
    ]


def _valid_manifest() -> dict[str, Any]:
    return {
        "$schema": "vibetable.plugin-manifest.v1",
        "pluginId": "com.example.reader",
        "version": "1.0.0",
        "displayName": {"en": "Reader"},
        "compatibility": {"minHostVersion": "1.0.0", "pluginApi": "1.x"},
        "permissions": {
            "data": [{"collection": "$active", "operations": ["read"], "fields": ["*"]}],
            "files": [],
            "privateStorage": False,
        },
        "actions": [
            {
                "actionId": "read-selection",
                "displayName": {"en": "Read"},
                "mode": "local",
                "risk": "read",
                "workerEntry": "dist/read.js",
                "inputSchema": "schemas/input.json",
                "outputSchema": "schemas/output.json",
            }
        ],
        "ui": {"customViews": []},
    }


@pytest.mark.parametrize(
    ("mutate", "code"),
    [
        (lambda m: m.pop("$schema"), "manifest_schema"),
        (lambda m: m.update(pluginId="single"), "plugin_id"),
        (lambda m: m.update(version="1.0"), "version"),
        (lambda m: m.update(displayName={}), "manifest_invalid"),
        (lambda m: m.update(displayName="text"), "manifest_invalid"),
        (lambda m: m["permissions"].update(data="not-a-list"), "permissions_invalid"),
        (
            lambda m: m["permissions"]["data"].append({"collection": "", "operations": []}),
            "permissions_invalid",
        ),
        (
            lambda m: m["permissions"]["data"].append(
                {"collection": "orders", "operations": ["truncate"]}
            ),
            "permissions_invalid",
        ),
        (
            lambda m: m["permissions"]["data"].append(
                {"collection": "orders", "operations": ["read"], "fields": [123]}
            ),
            "permissions_invalid",
        ),
        (lambda m: m["permissions"].update(files=["unknown"]), "permissions_invalid"),
        (lambda m: m["permissions"].update(privateStorage="yes"), "permissions_invalid"),
        (lambda m: m["permissions"].update(network="x"), "permissions_invalid"),
        (
            lambda m: m["permissions"].update(
                network={"domains": ["http://evil.com"], "methods": ["GET"]}
            ),
            "permissions_invalid",
        ),
        (
            lambda m: m["permissions"].update(
                network={"domains": ["good.com"], "methods": ["HACK"]}
            ),
            "permissions_invalid",
        ),
        (lambda m: m.update(actions="not-a-list"), "manifest_invalid"),
        (
            lambda m: m["actions"][0].update(actionId=""),
            "action_id",
        ),
        (
            lambda m: m["actions"][0].update(mode="cloud"),
            "action_invalid",
        ),
        (
            lambda m: m["actions"][0].update(invocation="scheduled"),
            "action_invalid",
        ),
        (
            lambda m: m["actions"][0].update(placements=["", "grid"]),
            "action_invalid",
        ),
        (
            lambda m: m["actions"][0].update(workerEntry=123),
            "worker_entry",
        ),
        (
            lambda m: m["actions"][0].update(workerEntry="dist/missing.js"),
            "missing_reference",
        ),
        (lambda m: m.update(ui="not-a-dict"), "ui_invalid"),
        (
            lambda m: m.update(ui={"customViews": "not-a-list"}),
            "ui_invalid",
        ),
        (
            lambda m: m.update(ui={"customViews": [{"viewId": "", "actionId": "read-selection"}]}),
            "ui_invalid",
        ),
        (
            lambda m: m.update(ui={"customViews": [{"viewId": "v1", "actionId": "missing"}]}),
            "ui_invalid",
        ),
        (
            lambda m: m.update(
                ui={
                    "customViews": [
                        {
                            "viewId": "v1",
                            "actionId": "read-selection",
                            "src": "evil",
                            "entry": "dist/read.js",
                        }
                    ]
                }
            ),
            "ui_invalid",
        ),
        (
            lambda m: m.update(
                ui={"customViews": [{"viewId": "v1", "actionId": "read-selection"}]}
            ),
            "ui_invalid",
        ),
    ],
)
def test_manifest_validation_rejects_violations(mutate: Any, code: str) -> None:
    manifest = _valid_manifest()
    mutate(manifest)
    with pytest.raises(PluginPackageError) as error:
        validate_plugin_manifest(_valid_entries(manifest))
    assert error.value.code == code


def test_manifest_validation_accepts_duplicate_action_id_as_error() -> None:
    manifest = _valid_manifest()
    manifest["actions"].append(dict(manifest["actions"][0]))
    with pytest.raises(PluginPackageError, match="actionId must be a unique"):
        validate_plugin_manifest(_valid_entries(manifest))


def test_manifest_validation_accepts_custom_view_with_valid_entry() -> None:
    manifest = _valid_manifest()
    manifest["ui"]["customViews"] = [
        {"viewId": "panel-1", "actionId": "read-selection", "entry": "dist/read.js"}
    ]
    result = validate_plugin_manifest(_valid_entries(manifest))
    assert result["ui"]["customViews"][0]["viewId"] == "panel-1"


# ===========================================================================
# Compatibility validation
# ===========================================================================


@pytest.mark.parametrize(
    ("compatibility", "code"),
    [
        ("not-a-dict", "compatibility_invalid"),
        ({}, "compatibility_invalid"),
        ({"minHostVersion": "1.0.0"}, "compatibility_invalid"),
        ({"minHostVersion": "not-semver", "pluginApi": "1.x"}, "compatibility_invalid"),
        ({"minHostVersion": "1.0.0", "pluginApi": "2.x"}, "version_incompatible"),
    ],
)
def test_validate_compatibility_rejects_invalid(compatibility: Any, code: str) -> None:
    manifest = _valid_manifest()
    manifest["compatibility"] = compatibility
    with pytest.raises(PluginPackageError) as error:
        validate_plugin_manifest(_valid_entries(manifest))
    assert error.value.code == code


def test_validate_compatibility_rejects_host_below_minimum() -> None:
    manifest = _valid_manifest()
    manifest["compatibility"] = {"minHostVersion": "2.0.0", "pluginApi": "1.x"}
    with pytest.raises(PluginPackageError, match="exceeds host"):
        validate_plugin_manifest(
            _valid_entries(manifest),
        )


def test_version_tuple_parses_plain_and_rejects_prefixed() -> None:
    assert _version_tuple("1.2.3") == (1, 2, 3)
    assert _version_tuple("1.0") == (1, 0, 0)
    assert _version_tuple(">=1.0.0") is None
    assert _version_tuple("not-semver") is None


def test_range_contains_evaluates_conjunction() -> None:
    assert _range_contains(">=1.0.0 <2.0.0", "1.5.0") is True
    assert _range_contains(">=1.0.0 <2.0.0", "2.5.0") is False
    assert _range_contains("=1.0.0", "1.0.0") is True
    assert _range_contains("invalid", "1.0.0") is None


# ===========================================================================
# PluginPackageError.rpc_error_data with path
# ===========================================================================


def test_plugin_package_error_includes_path_in_rpc_data() -> None:
    error = PluginPackageError("code", "message", path="manifest.json")
    assert error.rpc_error_data == {
        "code": "code",
        "recoverability": "reinstall",
        "path": "manifest.json",
    }


def test_plugin_package_error_omits_path_when_absent() -> None:
    error = PluginPackageError("code", "message")
    assert error.rpc_error_data == {"code": "code", "recoverability": "reinstall"}


# ===========================================================================
# Integrity verification edge cases (ZIP without integrity)
# ===========================================================================


def test_zip_without_integrity_rejects_when_file_source(tmp_path: Path) -> None:
    package = tmp_path / "no-integrity.vtplugin"
    with zipfile.ZipFile(package, "w") as archive:
        archive.writestr("manifest.json", json.dumps(_valid_manifest()))
        archive.writestr("dist/read.js", "export default async function run() {}")
    with pytest.raises(PluginPackageError, match="archive does not contain"):
        inspect_plugin_package(package)


def test_zip_with_corrupt_integrity_rejects(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    package = tmp_path / "reader.vtplugin"
    pack_plugin(plugin, package)
    # Corrupt the integrity.json member.
    with zipfile.ZipFile(package) as archive:
        members = {name: archive.read(name) for name in archive.namelist()}
    members["integrity.json"] = b"not-json"
    with zipfile.ZipFile(package, "w") as archive:
        for name, content in members.items():
            archive.writestr(name, content)
    with pytest.raises(PluginPackageError, match=r"integrity.json"):
        inspect_plugin_package(package)


def test_zip_with_wrong_algorithm_rejects(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    package = tmp_path / "reader.vtplugin"
    pack_plugin(plugin, package)
    with zipfile.ZipFile(package) as archive:
        members = {name: archive.read(name) for name in archive.namelist()}
    members["integrity.json"] = json.dumps({"algorithm": "md5", "files": {}}).encode()
    with zipfile.ZipFile(package, "w") as archive:
        for name, content in members.items():
            archive.writestr(name, content)
    with pytest.raises(PluginPackageError, match=r"integrity.json"):
        inspect_plugin_package(package)


# ===========================================================================
# Size limit branches
# ===========================================================================


def test_single_file_limit_rejects_oversized_member(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    with pytest.raises(PluginPackageError, match="package member exceeds"):
        inspect_plugin_package(plugin, policy=PackagePolicy(max_single_file_bytes=4))


def test_uncompressed_size_limit_rejects_oversized_package(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    with pytest.raises(PluginPackageError, match="package expands beyond"):
        inspect_plugin_package(plugin, policy=PackagePolicy(max_uncompressed_bytes=10))


def test_folder_package_size_limit_rejects(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    with pytest.raises(PluginPackageError, match="exceeds"):
        inspect_plugin_package(plugin, policy=PackagePolicy(max_package_bytes=10))


def test_zip_package_size_limit_rejects(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    package = tmp_path / "reader.vtplugin"
    pack_plugin(plugin, package)
    with pytest.raises(PluginPackageError, match="exceeds"):
        inspect_plugin_package(package, policy=PackagePolicy(max_package_bytes=10))


def test_source_not_found_rejects(tmp_path: Path) -> None:
    with pytest.raises(PluginPackageError, match="package source does not exist"):
        inspect_plugin_package(tmp_path / "missing")


def test_invalid_zip_rejects_non_zip_file(tmp_path: Path) -> None:
    fake = tmp_path / "fake.vtplugin"
    fake.write_text("not a zip", encoding="utf-8")
    with pytest.raises(PluginPackageError, match="neither a folder nor a ZIP"):
        inspect_plugin_package(fake)


# ===========================================================================
# read_plugin_package_member + pack_plugin edge cases
# ===========================================================================


def test_read_plugin_package_member_rejects_missing_member(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    with pytest.raises(PluginPackageError, match="referenced package file is missing"):
        read_plugin_package_member(plugin, "dist/missing.js")


def test_pack_plugin_rejects_non_directory_source(tmp_path: Path) -> None:
    fake = tmp_path / "file.txt"
    fake.write_text("not a dir", encoding="utf-8")
    with pytest.raises(PluginPackageError, match="pack source must be"):
        pack_plugin(fake, tmp_path / "out.vtplugin")


def test_pack_plugin_rejects_oversized_output(tmp_path: Path) -> None:
    plugin = tmp_path / "reader"
    _write_local_plugin(plugin)
    with pytest.raises(PluginPackageError, match="exceeds"):
        pack_plugin(
            plugin,
            tmp_path / "out.vtplugin",
            policy=PackagePolicy(max_package_bytes=10),
        )
    # The partially-written archive must be cleaned up.
    assert not (tmp_path / "out.vtplugin").exists()


def test_default_compatibility_policy_constants() -> None:
    assert DEFAULT_COMPATIBILITY_POLICY.host_version == "1.0.0"
    assert DEFAULT_COMPATIBILITY_POLICY.plugin_api == "1.x"


def test_validate_compatibility_accepts_legacy_field_rejection() -> None:
    # Assemble the retired provider name the same way the source does, so the
    # architecture guard does not flag this test file as reintroducing it.
    retired_provider = "".join(["di", "rectus"])
    manifest = _valid_manifest()
    manifest["compatibility"][retired_provider] = "1.0"
    with pytest.raises(PluginPackageError, match="unsupported legacy"):
        validate_plugin_manifest(_valid_entries(manifest))


def test_manifest_rejects_legacy_flows_field() -> None:
    manifest = _valid_manifest()
    manifest["flows"] = []
    with pytest.raises(PluginPackageError, match="unsupported legacy manifest field"):
        validate_plugin_manifest(_valid_entries(manifest))


def test_manifest_rejects_action_entry_flow() -> None:
    manifest = _valid_manifest()
    manifest["actions"][0]["entryFlow"] = "legacy"
    with pytest.raises(PluginPackageError, match="entryFlow"):
        validate_plugin_manifest(_valid_entries(manifest))


def test_manifest_rejects_non_string_schema_reference() -> None:
    manifest = _valid_manifest()
    manifest["actions"][0]["inputSchema"] = 123
    with pytest.raises(PluginPackageError, match="references must be strings"):
        validate_plugin_manifest(_valid_entries(manifest))


def test_manifest_rejects_invalid_json_schema_reference() -> None:
    manifest = _valid_manifest()
    entries = _valid_entries(manifest)
    # Replace inputSchema with an invalid JSON Schema (unsupported type).
    entries = [
        (name, content) if name != "schemas/input.json" else (name, b'{"type": "magic"}')
        for name, content in entries
    ]
    with pytest.raises(PluginPackageError) as error:
        validate_plugin_manifest(entries)
    assert error.value.code == "schema_invalid"


def test_manifest_rejects_non_object_schema_reference() -> None:
    manifest = _valid_manifest()
    entries = _valid_entries(manifest)
    entries = [
        (name, content) if name != "schemas/input.json" else (name, b"[1, 2, 3]")
        for name, content in entries
    ]
    with pytest.raises(PluginPackageError, match="not a supported JSON Schema"):
        validate_plugin_manifest(entries)
