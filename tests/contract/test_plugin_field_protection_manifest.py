"""Regressions for the closed ``permissions.fieldProtection`` manifest contract.

The declaration is only a *suggestion*: protection stays OFF until the host
records an explicit enable event. Go stores the declaration with the
installation and decodes it strictly on every read, so both admission seams
below must refuse anything Go could not decode or bind to the sibling model,
exactly like ``pluginstore.validateManifest`` does on its side.
"""

from __future__ import annotations

import argparse
import copy
import importlib.util
import json
from pathlib import Path
from types import ModuleType

import pydantic
import pytest

from backend.contracts.plugin import SURFACE_LIFECYCLE_CONTRACT, PluginManifest
from backend.contracts.plugin_field_protection import (
    CONTRACT,
    MAX_PROTECTED_FIELDS,
    PluginFieldProtectionDeclaration,
)
from backend.contracts.plugin_model import PluginModelDeclaration
from backend.infrastructure.plugin_package import PluginPackageError, validate_plugin_manifest

ROOT = Path(__file__).resolve().parents[2]


def protection_manifest(operations: list[str] | None = None) -> dict:
    return {
        "$schema": "vibetable.plugin-manifest.v2",
        "pluginId": "com.vibetable.examples.field-protection",
        "version": "1.0.0",
        "displayName": {"zh-CN": "字段保护"},
        "description": {"zh-CN": "宿主持久字段保护清单校验合成示例"},
        "compatibility": {"minHostVersion": "0.5.3", "pluginApi": "3.x"},
        "permissions": {
            "model": {
                "contract": "vibetable.plugin-model.v1",
                "modelId": "synthetic",
                "version": 1,
                "entities": [
                    {
                        "key": "tasks",
                        "label": "任务",
                        "operations": operations or ["read", "query", "create", "update", "delete"],
                        "fields": [
                            {"key": "title", "label": "标题", "type": "text"},
                            {"key": "status", "label": "状态", "type": "text"},
                        ],
                    },
                    {
                        "key": "notes",
                        "label": "备注",
                        "operations": ["read", "query"],
                        "fields": [{"key": "body", "label": "正文", "type": "text"}],
                    },
                ],
            },
            "fieldProtection": {
                "contract": CONTRACT,
                "fields": [{"entity": "tasks", "field": "status"}],
            },
        },
        "actions": [
            {
                "actionId": "run",
                "displayName": {"zh-CN": "运行"},
                "description": {},
                "mode": "local",
                "risk": "write",
                "invocation": "manual",
                "placements": [],
                "presentation": "application",
                "entry": "ui/index.html",
            }
        ],
        "ui": {"customViews": []},
    }


def package_entries(manifest: dict) -> list[tuple[str, bytes]]:
    return [
        ("manifest.json", json.dumps(manifest, ensure_ascii=False).encode("utf-8")),
        ("ui/index.html", b"<!doctype html><html></html>"),
    ]


def both_seams_refuse(manifest: dict, match: str) -> None:
    with pytest.raises(pydantic.ValidationError, match=match):
        PluginManifest.model_validate(manifest)
    with pytest.raises(PluginPackageError) as error:
        validate_plugin_manifest(package_entries(manifest))
    assert error.value.code == "permissions_invalid"
    assert match in str(error.value)


def test_native_manifest_accepts_bound_field_protection_suggestion() -> None:
    manifest = protection_manifest()
    manifest["compatibility"]["surfaceLifecycle"] = SURFACE_LIFECYCLE_CONTRACT
    parsed = PluginManifest.model_validate(manifest)
    assert parsed.compatibility["surfaceLifecycle"] == SURFACE_LIFECYCLE_CONTRACT
    assert parsed.permissions["fieldProtection"] == manifest["permissions"]["fieldProtection"]
    declaration = PluginFieldProtectionDeclaration.model_validate(
        manifest["permissions"]["fieldProtection"], by_alias=True, by_name=False
    )
    assert [item.entity for item in declaration.fields] == ["tasks"]
    assert validate_plugin_manifest(package_entries(manifest)) == manifest
    # Every valid model subset can be protected in full (pluginmodel: 45 x 64).
    assert MAX_PROTECTED_FIELDS == 45 * 64


def test_field_protection_accepts_any_write_operation_on_the_entity() -> None:
    for operations in (["read", "query", "delete"], ["read", "query", "create"]):
        manifest = protection_manifest(operations)
        assert validate_plugin_manifest(package_entries(manifest)) == manifest


def test_field_protection_requires_declared_model_fields_on_written_entities() -> None:
    # Computed projections and user-added extension fields are never part of the
    # model, so "missing" below also stands for them: they can never be declared.
    for entity, field in [("tasks", "missing"), ("missing", "status"), ("notes", "body"), ("", "")]:
        manifest = protection_manifest()
        manifest["permissions"]["fieldProtection"]["fields"] = [{"entity": entity, "field": field}]
        with pytest.raises(PluginPackageError) as error:
            validate_plugin_manifest(package_entries(manifest))
        assert error.value.code == "permissions_invalid"
        with pytest.raises(pydantic.ValidationError):
            PluginManifest.model_validate(manifest)


def test_field_protection_declaration_is_closed_and_requires_native_model() -> None:
    def mutated(mutator) -> dict:
        manifest = protection_manifest()
        mutator(manifest)
        return manifest

    def drop_model(manifest: dict) -> None:
        del manifest["permissions"]["model"]

    def legacy(manifest: dict) -> None:
        manifest["$schema"] = "vibetable.plugin-manifest.v1"
        manifest["compatibility"]["pluginApi"] = "1.x"

    def unknown_key(manifest: dict) -> None:
        manifest["permissions"]["fieldProtection"]["on"] = True

    def unknown_entry_key(manifest: dict) -> None:
        manifest["permissions"]["fieldProtection"]["fields"][0]["label"] = "状态"

    def missing_fields(manifest: dict) -> None:
        del manifest["permissions"]["fieldProtection"]["fields"]

    def empty_fields(manifest: dict) -> None:
        manifest["permissions"]["fieldProtection"]["fields"] = []

    def duplicate(manifest: dict) -> None:
        manifest["permissions"]["fieldProtection"]["fields"] *= 2

    def null_declaration(manifest: dict) -> None:
        manifest["permissions"]["fieldProtection"] = None

    def wrong_contract(manifest: dict) -> None:
        manifest["permissions"]["fieldProtection"]["contract"] = CONTRACT + "2"

    def oversized(manifest: dict) -> None:
        # The 2880-entry cap binds long before the 1 MiB byte budget.
        manifest["permissions"]["fieldProtection"]["fields"] = [
            {"entity": "tasks", "field": f"f{i}"} for i in range(MAX_PROTECTED_FIELDS + 1)
        ]

    both_seams_refuse(mutated(drop_model), "requires a model declaration")
    # A legacy package cannot carry a native model either; both refusals are legitimate.
    legacy_manifest = mutated(legacy)
    with pytest.raises(PluginPackageError):
        validate_plugin_manifest(package_entries(legacy_manifest))
    with pytest.raises(pydantic.ValidationError):
        PluginManifest.model_validate(legacy_manifest)
    both_seams_refuse(mutated(unknown_key), "extra_forbidden")
    both_seams_refuse(mutated(unknown_entry_key), "extra_forbidden")
    both_seams_refuse(mutated(missing_fields), "fields")
    both_seams_refuse(mutated(empty_fields), "fields")
    both_seams_refuse(mutated(duplicate), "declared twice")
    both_seams_refuse(mutated(null_declaration), "model_type")
    both_seams_refuse(mutated(wrong_contract), "contract")
    both_seams_refuse(mutated(oversized), "fields")


def test_validate_model_mirrors_go_declaration_validate() -> None:
    declaration = PluginFieldProtectionDeclaration.model_validate(
        {"contract": CONTRACT, "fields": [{"entity": "tasks", "field": "status"}]}
    )
    with pytest.raises(ValueError, match="requires a model declaration"):
        declaration.validate_model(None)
    model = PluginModelDeclaration.model_validate(protection_manifest()["permissions"]["model"])
    declaration.validate_model(model)
    read_only = PluginFieldProtectionDeclaration.model_validate(
        {"contract": CONTRACT, "fields": [{"entity": "notes", "field": "body"}]}
    )
    with pytest.raises(ValueError, match="entity the plugin writes"):
        read_only.validate_model(model)


def _cli() -> ModuleType:
    spec = importlib.util.spec_from_file_location(
        "vibetable_plugin_cli", ROOT / "scripts/vibetable_plugin.py"
    )
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_permission_report_and_cli_print_field_protection_suggestion(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    cli = _cli()
    manifest = protection_manifest()
    report = cli._permission_report(manifest)
    assert report["fieldProtection"] == manifest["permissions"]["fieldProtection"]
    without = copy.deepcopy(manifest)
    del without["permissions"]["fieldProtection"]
    assert cli._permission_report(without)["fieldProtection"] is None

    monkeypatch.setattr(
        cli,
        "inspect_plugin_package",
        lambda source: type("Inspected", (), {"manifest": manifest})(),
    )
    args = argparse.Namespace(source=Path("."), json=False)
    assert cli._command_inspect_permissions(args) == 0
    out = capsys.readouterr().out
    assert "宿主持久字段保护建议: 1 个字段" in out
    assert (
        json.dumps(manifest["permissions"]["fieldProtection"], ensure_ascii=False, sort_keys=True)
        in out
    )
    monkeypatch.setattr(
        cli, "inspect_plugin_package", lambda source: type("Inspected", (), {"manifest": without})()
    )
    assert cli._command_inspect_permissions(args) == 0
    assert "宿主持久字段保护建议" not in capsys.readouterr().out
