"""Regressions for the closed ``permissions.transactions`` manifest contract.

The previous implementation ignored ``permissions.transactions`` entirely, so a
manifest with script/SQL-ish or cross-kind rule fields, a foreign modelId, a
non-native schema or an oversized declaration passed both seams below. These
tests mirror the Go closed set in ``sidecar/internal/transactionrules`` and
``exactdecimal`` and fail on that older permissive behavior.
"""

from __future__ import annotations

import copy
import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from backend._version import __version__
from backend.contracts.plugin import PluginManifest
from backend.contracts.plugin_transactions import MAX_RULES, PluginTransactionsDeclaration
from backend.infrastructure.plugin_package import (
    InspectedPluginPackage,
    PluginPackageError,
    inspect_plugin_package,
    validate_plugin_manifest,
)


def transactions_manifest() -> dict:
    manifest = {
        "$schema": "vibetable.plugin-manifest.v2",
        "pluginId": "com.vibetable.examples.transactions-conformance",
        "version": "1.0.0",
        "displayName": {"zh-CN": "合成事务"},
        "description": {"zh-CN": "事务规则清单校验合成示例"},
        "compatibility": {"minHostVersion": "0.5.3", "pluginApi": "3.x"},
        "permissions": {
            "model": {
                "contract": "vibetable.plugin-model.v1",
                "modelId": "synthetic",
                "version": 1,
                "entities": [
                    {
                        "key": "headers",
                        "label": "合成单头",
                        "operations": ["read", "query", "create", "update", "delete"],
                        "fields": [
                            {"key": "code", "label": "单号", "type": "text"},
                            {"key": "status", "label": "状态", "type": "text"},
                            {"key": "amount", "label": "金额", "type": "text"},
                            {
                                "key": "lines",
                                "label": "明细",
                                "type": "relation",
                                "relation": {
                                    "entity": "lines",
                                    "field": "header",
                                    "cardinality": "many",
                                },
                            },
                        ],
                    },
                    {
                        "key": "lines",
                        "label": "合成明细",
                        "operations": ["read", "query", "create", "update", "delete"],
                        "fields": [
                            {"key": "amount", "label": "金额", "type": "text"},
                            {
                                "key": "header",
                                "label": "单头",
                                "type": "relation",
                                "relation": {
                                    "entity": "headers",
                                    "field": "lines",
                                    "cardinality": "one",
                                },
                            },
                        ],
                    },
                ],
            },
            "transactions": {
                "contract": "vibetable.plugin-transactions.v1",
                "modelId": "synthetic",
                "version": 1,
                "decimals": [
                    {
                        "entity": "headers",
                        "field": "amount",
                        "storage": "decimalText",
                        "scale": 2,
                        "digits": 38,
                    },
                    {
                        "entity": "lines",
                        "field": "amount",
                        "storage": "decimalText",
                        "scale": 2,
                        "digits": 38,
                    },
                ],
                "rules": [
                    {"id": "code", "kind": "unique", "entity": "headers", "fields": ["code"]},
                    {
                        "id": "header",
                        "kind": "reference",
                        "entity": "lines",
                        "field": "header",
                        "target": "headers",
                    },
                    {
                        "id": "total",
                        "kind": "conservation",
                        "entity": "lines",
                        "relation": "header",
                        "amount": "amount",
                        "target": "headers",
                        "limit": "amount",
                    },
                    {
                        "id": "status",
                        "kind": "state",
                        "entity": "headers",
                        "field": "status",
                        "fields": ["amount"],
                        "transitions": {"draft": ["posted"], "posted": []},
                        "frozenStates": ["posted"],
                    },
                ],
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
    return manifest


def conformance_manifest() -> dict:
    root = Path(__file__).resolve().parents[2]
    return json.loads(
        (root / "examples/plugins/transaction-conformance/manifest.json").read_text(
            encoding="utf-8"
        )
    )


def declaration(manifest: dict) -> dict:
    return manifest["permissions"]["transactions"]


def package_entries(manifest: dict) -> list[tuple[str, bytes]]:
    return [
        ("manifest.json", json.dumps(manifest, ensure_ascii=False).encode("utf-8")),
        ("ui/index.html", b"<!doctype html><html></html>"),
    ]


def test_native_manifest_accepts_closed_transaction_rules() -> None:
    manifest = transactions_manifest()
    parsed = PluginManifest.model_validate(manifest)
    assert parsed.permissions["transactions"] == declaration(manifest)
    assert (
        PluginTransactionsDeclaration.model_validate(declaration(manifest)).model_id == "synthetic"
    )
    assert validate_plugin_manifest(package_entries(manifest)) == manifest


def test_conformance_fixture_passes_closed_manifest_and_package_validation() -> None:
    manifest = conformance_manifest()
    parsed = PluginManifest.model_validate(manifest)
    assert parsed.permissions["transactions"] == declaration(manifest)
    declaration_model = PluginTransactionsDeclaration.model_validate(declaration(manifest))
    assert declaration_model.model_id == manifest["permissions"]["model"]["modelId"]
    assert [r.kind for r in declaration_model.rules] == ["unique", "balance", "conservation"]
    # The shipped fixture is a real local development folder: local build inputs
    # (src/, tsconfig.json) are excluded and the remaining package must inspect.
    inspected = inspect_plugin_package(
        Path(__file__).resolve().parents[2] / "examples/plugins/transaction-conformance"
    )
    assert isinstance(inspected, InspectedPluginPackage)
    assert inspected.manifest["permissions"]["transactions"] == declaration(manifest)
    assert inspected.package_hash.startswith("sha256:")
    assert __version__ >= manifest["compatibility"]["minHostVersion"]


def test_transactions_require_native_manifest_and_declared_model() -> None:
    legacy = transactions_manifest()
    legacy["$schema"] = "vibetable.plugin-manifest.v1"
    legacy["compatibility"]["pluginApi"] = "1.x"
    with pytest.raises(ValidationError, match=r"native plugin API 3\.x"):
        PluginManifest.model_validate(legacy)

    mismatch = transactions_manifest()
    declaration(mismatch)["modelId"] = "foreign"
    with pytest.raises(ValidationError, match=r"reference the declared model"):
        PluginManifest.model_validate(mismatch)

    orphan = transactions_manifest()
    del orphan["permissions"]["model"]
    with pytest.raises(ValidationError):
        PluginManifest.model_validate(orphan)

    with pytest.raises(PluginPackageError, match="reference the declared model"):
        validate_plugin_manifest(package_entries(mismatch))


@pytest.mark.parametrize(
    "case",
    [
        "contract",
        "unknown_kind",
        "unknown_key",
        "unique_extra_field",
        "reference_extra_relation",
        "conservation_missing_decimal",
        "balance_undeclared_limit",
        "state_empty_transitions",
        "state_extra_target",
        "duplicate_ids",
        "empty_rules",
        "version_zero",
        "version_string",
        "storage_unknown",
        "scale_over_digits",
        "scale_overflow",
        "duplicate_decimal",
        "empty_unique_fields",
        "duplicate_unique_fields",
        "empty_transition_state",
        "blank_entity",
        "empty_rule_id",
    ],
)
def test_transactions_reject_unknown_or_cross_kind_declarations(case: str) -> None:
    manifest = transactions_manifest()
    rules = declaration(manifest)["rules"]
    decimals = declaration(manifest)["decimals"]
    if case == "contract":
        declaration(manifest)["contract"] = "vibetable.plugin-transactions.v2"
    elif case == "unknown_kind":
        rules[0]["kind"] = "script"
    elif case == "unknown_key":
        rules[0]["omitOnImport"] = True
    elif case == "unique_extra_field":
        rules[0]["field"] = "code"
    elif case == "reference_extra_relation":
        rules[1]["relation"] = "headers"
    elif case == "conservation_missing_decimal":
        declaration(manifest)["decimals"] = decimals[:1]
    elif case == "balance_undeclared_limit":
        rules[2]["kind"] = "balance"
        rules[2]["limit"] = "code"
    elif case == "state_empty_transitions":
        rules[3]["transitions"] = {}
    elif case == "state_extra_target":
        rules[3]["target"] = "lines"
    elif case == "duplicate_ids":
        rules[1]["id"] = rules[0]["id"]
    elif case == "empty_rules":
        declaration(manifest)["rules"] = []
    elif case == "version_zero":
        declaration(manifest)["version"] = 0
    elif case == "version_string":
        declaration(manifest)["version"] = "1"
    elif case == "storage_unknown":
        decimals[0]["storage"] = "float"
    elif case == "scale_over_digits":
        decimals[0]["scale"] = 3
        decimals[0]["digits"] = 2
    elif case == "scale_overflow":
        decimals[0]["scale"] = 19
    elif case == "duplicate_decimal":
        decimals[1] = copy.deepcopy(decimals[0])
    elif case == "empty_unique_fields":
        rules[0]["fields"] = []
    elif case == "duplicate_unique_fields":
        rules[0]["fields"] = ["code", "code"]
    elif case == "empty_transition_state":
        rules[3]["transitions"][""] = ["posted"]
    elif case == "blank_entity":
        rules[0]["entity"] = ""
    elif case == "empty_rule_id":
        rules[0]["id"] = ""
    with pytest.raises(ValidationError):
        PluginManifest.model_validate(manifest)
    with pytest.raises(PluginPackageError):
        validate_plugin_manifest(package_entries(manifest))


def test_transactions_enforce_declared_budgets() -> None:
    manifest = transactions_manifest()
    rule = {"id": "bulk", "kind": "unique", "entity": "lines", "fields": ["amount"]}
    declaration(manifest)["rules"] = [
        {"id": f"r{index}", **{k: v for k, v in rule.items() if k != "id"}}
        for index in range(MAX_RULES + 1)
    ]
    with pytest.raises(ValidationError):
        PluginManifest.model_validate(manifest)

    oversized = transactions_manifest()
    # Same closed shape, but the serialized declaration exceeds the 1 MiB budget
    # that Go Decode applies at the write boundary.
    oversized["permissions"]["transactions"]["rules"][0]["fields"] = ["code" * 300_000]
    with pytest.raises(ValidationError, match="1 MiB"):
        PluginManifest.model_validate(oversized)
    with pytest.raises(PluginPackageError, match="1 MiB"):
        validate_plugin_manifest(package_entries(oversized))
