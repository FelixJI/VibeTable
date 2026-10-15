"""Task #482 presentation/entry contract boundaries.

Every accepting case here fails on the pre-#482 implementation (which
required a custom view for every v2 action), and every rejecting case is a
new closed-set rule that the old validator could not express.
"""

import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend.contracts.plugin import PluginAction, PluginManifest
from backend.infrastructure.plugin_package import (
    PluginPackageError,
    inspect_plugin_package,
    pack_plugin,
)


def _write_package(root: Path, actions: list[dict], views: list[dict] | None = None) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    (root / "ui").mkdir(exist_ok=True)
    (root / "ui" / "index.html").write_text("<!doctype html>", encoding="utf-8")
    (root / "ui" / "app.js").write_text("export {};", encoding="utf-8")
    manifest = {
        "$schema": "vibetable.plugin-manifest.v2",
        "pluginId": "com.example.presentation",
        "version": "1.0.0",
        "displayName": {"zh-CN": "presentation 示例"},
        "compatibility": {"pluginApi": "3.x", "minHostVersion": "0.5.3"},
        "permissions": {"privateStorage": True},
        "actions": actions,
        "ui": {"customViews": views or []},
    }
    (root / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False), encoding="utf-8")
    return root


def test_application_action_installs_without_custom_view(tmp_path: Path) -> None:
    plugin = _write_package(
        tmp_path / "app",
        [
            {
                "actionId": "open",
                "mode": "local",
                "risk": "read",
                "presentation": "application",
                "entry": "ui/index.html",
            }
        ],
    )
    inspected = inspect_plugin_package(plugin)
    manifest = PluginManifest.model_validate(inspected.manifest)
    assert manifest.actions[0].presentation == "application"
    assert manifest.actions[0].entry == "ui/index.html"


def test_headless_action_installs_with_js_entry(tmp_path: Path) -> None:
    plugin = _write_package(
        tmp_path / "headless",
        [
            {
                "actionId": "run",
                "mode": "local",
                "risk": "read",
                "presentation": "headless",
                "entry": "ui/app.js",
            }
        ],
    )
    inspect_plugin_package(plugin)


def test_headless_and_application_entries_must_exist_in_package(tmp_path: Path) -> None:
    plugin = _write_package(
        tmp_path / "missing",
        [
            {
                "actionId": "open",
                "mode": "local",
                "risk": "read",
                "presentation": "application",
                "entry": "ui/gone.html",
            }
        ],
    )
    with pytest.raises(PluginPackageError) as raised:
        inspect_plugin_package(plugin)
    assert raised.value.code == "missing_reference"


def test_surface_action_rejects_declared_entry(tmp_path: Path) -> None:
    plugin = _write_package(
        tmp_path / "surface-entry",
        [
            {
                "actionId": "open",
                "mode": "local",
                "risk": "read",
                "presentation": "surface",
                "entry": "ui/index.html",
            }
        ],
        views=[{"viewId": "v", "actionId": "open", "entry": "ui/index.html"}],
    )
    with pytest.raises(PluginPackageError) as raised:
        inspect_plugin_package(plugin)
    assert raised.value.code == "action_invalid"


def test_new_shapes_reject_legacy_schema_and_view_channels(tmp_path: Path) -> None:
    (tmp_path / "schemas").mkdir(parents=True)
    (tmp_path / "schemas" / "input.json").write_text('{"type":"object"}', encoding="utf-8")
    plugin = _write_package(
        tmp_path / "mixed",
        [
            {
                "actionId": "open",
                "mode": "local",
                "risk": "read",
                "presentation": "headless",
                "entry": "ui/app.js",
                "inputSchema": "schemas/input.json",
            }
        ],
    )
    with pytest.raises(PluginPackageError) as raised:
        inspect_plugin_package(plugin)
    assert raised.value.code == "action_invalid"

    with pytest.raises(PluginPackageError) as raised:
        inspect_plugin_package(
            _write_package(
                tmp_path / "mixed-view",
                [
                    {
                        "actionId": "open",
                        "mode": "local",
                        "risk": "read",
                        "presentation": "application",
                        "entry": "ui/index.html",
                    }
                ],
                views=[{"viewId": "v", "actionId": "open", "entry": "ui/index.html"}],
            )
        )
    assert raised.value.code == "ui_invalid"


def test_non_canonical_presentation_entry_is_rejected(tmp_path: Path) -> None:
    plugin = _write_package(
        tmp_path / "canonical",
        [
            {
                "actionId": "open",
                "mode": "local",
                "risk": "read",
                "presentation": "application",
                "entry": "./ui/index.html",
            }
        ],
    )
    with pytest.raises(PluginPackageError) as raised:
        inspect_plugin_package(plugin)
    assert raised.value.code == "reference_non_canonical"


def test_v1_manifest_rejects_presentation_and_entry() -> None:
    manifest = {
        "$schema": "vibetable.plugin-manifest.v1",
        "pluginId": "com.example.v1",
        "version": "1.0.0",
        "compatibility": {"pluginApi": "1.x"},
        "permissions": {},
        "actions": [
            {
                "actionId": "run",
                "risk": "read",
                "workerEntry": "dist/workers/run.js",
                "presentation": "application",
                "entry": "ui/index.html",
            }
        ],
        "ui": {},
    }
    with pytest.raises(ValueError, match="only available on v2"):
        PluginManifest.model_validate(manifest)


def test_serialization_omits_presentation_defaults_for_legacy_actions() -> None:
    legacy = PluginAction.model_validate({"actionId": "open", "risk": "read"})
    legacy_payload = json.loads(legacy.model_dump_json(by_alias=True))
    assert "presentation" not in legacy_payload
    assert "entry" not in legacy_payload

    application = PluginAction.model_validate(
        {
            "actionId": "open",
            "risk": "read",
            "presentation": "application",
            "entry": "ui/index.html",
        }
    )
    application_payload = json.loads(application.model_dump_json(by_alias=True))
    assert application_payload["presentation"] == "application"
    assert application_payload["entry"] == "ui/index.html"


@pytest.mark.parametrize(
    ("presentation", "entry", "valid"),
    [
        ("application", "ui/ENTRY.html", True),
        ("application", "ui/ENTRY.HTML", True),
        ("headless", "ui/ENTRY.js", True),
        ("headless", "ui/ENTRY.JS", True),
        ("headless", "ui/ENTRY.mjs", True),
        ("headless", "ui/ENTRY.MJS", True),
        ("application", "ui/ENTRY.js", False),
        ("application", "ui/ENTRY.htm", False),
        ("headless", "ui/ENTRY.html", False),
        ("headless", "ui/ENTRY", False),
        ("application", "__vibetable_host__/headless.html", False),
        ("application", "__vibetable_host__/nested/index.HTML", False),
        ("headless", "__vibetable_host__/headless.js", False),
        ("headless", "__vibetable_host__/nested/entry.MJS", False),
    ],
)
def test_presentation_entry_matches_served_mime_on_all_python_paths(
    tmp_path: Path,
    presentation: str,
    entry: str,
    valid: bool,
) -> None:
    action = {
        "actionId": "open",
        "mode": "local",
        "risk": "read",
        "presentation": presentation,
        "entry": entry,
    }
    source = _write_package(tmp_path / "source", [action])
    (source / entry).parent.mkdir(parents=True, exist_ok=True)
    (source / entry).write_text(
        "<!doctype html>" if presentation == "application" else "export {};", encoding="utf-8"
    )
    output = tmp_path / "package.vtplugin"
    if valid:
        PluginAction.model_validate(action)
        PluginManifest.model_validate(
            json.loads((source / "manifest.json").read_text(encoding="utf-8"))
        )
        inspect_plugin_package(source)
        pack_plugin(source, output)
        inspect_plugin_package(output)
    else:
        with pytest.raises(ValidationError, match="entry"):
            PluginAction.model_validate(action)
        with pytest.raises(PluginPackageError, match="entry"):
            inspect_plugin_package(source)
        with pytest.raises(PluginPackageError, match="entry"):
            pack_plugin(source, output)


@pytest.mark.parametrize("resource", ["entry", "html", "script", "style"])
def test_custom_views_reject_declared_host_reserved_resources(
    tmp_path: Path, resource: str
) -> None:
    action = {"actionId": "open", "mode": "local", "risk": "read"}
    view = {
        "viewId": "custom",
        "actionId": "open",
        "entry": "ui/index.html",
        resource: "__vibetable_host__/nested/resource.html",
    }
    source = _write_package(tmp_path / "source", [action], [view])
    member = source / view[resource]
    member.parent.mkdir(parents=True, exist_ok=True)
    member.write_text("reserved resource", encoding="utf-8")
    with pytest.raises(ValidationError, match="Host-reserved"):
        PluginManifest.model_validate(json.loads((source / "manifest.json").read_text()))
    with pytest.raises(PluginPackageError, match="Host-reserved"):
        inspect_plugin_package(source)
    with pytest.raises(PluginPackageError, match="Host-reserved"):
        pack_plugin(source, tmp_path / "reserved.vtplugin")
