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
from backend.contracts.plugin_transactions import (
    MAX_RULES,
    PluginTransactionsDeclaration,
    TransactionRule,
)
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


@pytest.mark.parametrize(
    ("public", "private"),
    [
        ("whenPresent", "when_present"),
        ("skipEmptyLeftEnd", "skip_empty_left_end"),
        ("currentWhen", "current_when"),
        ("frozenStates", "frozen_states"),
        ("modelId", "model_id"),
    ],
)
def test_raw_transaction_permissions_accept_only_wire_aliases(public: str, private: str) -> None:
    manifest = transactions_manifest()
    d = declaration(manifest)
    path = [{"field": "header", "target": "headers"}]
    scope = {
        "id": "scope",
        "kind": "sameTarget",
        "entity": "lines",
        "left": path,
        "right": path,
        "whenPresent": "header",
        "skipEmptyLeftEnd": True,
    }
    d["rules"].append(scope)
    balance = d["rules"][2]
    balance.update(kind="balance", currentWhen={"left": path, "right": path})
    assert validate_plugin_manifest(package_entries(manifest)) == manifest
    assert PluginManifest.model_validate(manifest).permissions["transactions"] == d

    owner = next(item for item in [d, *d["rules"]] if public in item)
    owner[private] = owner.pop(public)
    # In-process model construction may use Python attributes. Only raw package/manifest
    # admission promises that these same bytes will be accepted by the native decoder.
    assert PluginTransactionsDeclaration.model_validate(d).model_id == "synthetic"
    with pytest.raises(ValidationError):
        PluginManifest.model_validate(manifest)
    with pytest.raises(PluginPackageError):
        validate_plugin_manifest(package_entries(manifest))


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


def test_package_capacity_and_closed_relation_paths() -> None:
    manifest = transactions_manifest()
    entities = manifest["permissions"]["model"]["entities"]
    for index in range(38):
        entities.append(
            {
                "key": f"e{index}",
                "label": f"T{index}",
                "operations": ["read", "query"],
                "fields": [{"key": "code", "label": "Code", "type": "text"}],
            }
        )
    path = [{"field": "header", "target": "headers"}]
    rules = declaration(manifest)["rules"]
    rules.extend(
        [
            {"id": "scope", "kind": "sameTarget", "entity": "lines", "left": path, "right": path},
            {
                **rules[2],
                "id": "current",
                "kind": "balance",
                "currentWhen": {"left": path, "right": path},
            },
        ]
    )
    rules.extend({**rules[0], "id": f"u{i}"} for i in range(MAX_RULES - len(rules)))
    assert PluginManifest.model_validate(manifest)
    assert validate_plugin_manifest(package_entries(manifest)) == manifest

    for invalid_path in (
        [],
        path * 5,
        [{"field": "amount", "target": "headers"}],
        [{"field": "header", "target": "lines"}],
        [*path, {"field": "lines", "target": "lines"}],
    ):
        invalid = copy.deepcopy(manifest)
        declaration(invalid)["rules"][4]["right"] = invalid_path
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))

    for key, collection in (
        ("entities", entities),
        ("rules", rules),
        ("decimals", declaration(manifest)["decimals"]),
    ):
        invalid = copy.deepcopy(manifest)
        dest = (invalid["permissions"]["model"] if key == "entities" else declaration(invalid))[key]
        if key == "decimals":
            dest.extend({**collection[0], "field": f"n{i}"} for i in range(66 - len(dest)))
        else:
            dest.append({**collection[-1], ("key" if key == "entities" else "id"): "extra"})
            if key == "entities":
                dest[-1]["label"] = "Extra"
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))


def test_optional_single_pointer_cannot_skip_an_unrelated_path() -> None:
    manifest = transactions_manifest()
    entities = manifest["permissions"]["model"]["entities"]
    entities[0]["fields"].append(
        {
            "key": "current",
            "label": "Current",
            "type": "relation",
            "relation": {"entity": "lines", "field": "current_for", "cardinality": "one"},
        }
    )
    entities[1]["fields"].append(
        {
            "key": "current_for",
            "label": "Current for",
            "type": "relation",
            "relation": {"entity": "headers", "field": "current", "cardinality": "many"},
        }
    )
    rule = {
        "id": "owner",
        "kind": "sameTarget",
        "entity": "headers",
        "whenPresent": "current",
        "left": [{"field": "current", "target": "lines"}, {"field": "header", "target": "headers"}],
        "right": [],
    }
    declaration(manifest)["rules"].append(rule)
    assert validate_plugin_manifest(package_entries(manifest)) == manifest
    rule["whenPresent"] = "code"
    with pytest.raises(PluginPackageError):
        validate_plugin_manifest(package_entries(manifest))


def test_transaction_source_choice_and_decimal_sign_match_package_admission() -> None:
    manifest = transactions_manifest()
    entities = manifest["permissions"]["model"]["entities"]
    entities[1]["fields"].append(
        {
            "key": "other",
            "label": "Other",
            "type": "relation",
            "relation": {"entity": "headers", "field": "others", "cardinality": "one"},
        }
    )
    entities[0]["fields"].append(
        {
            "key": "others",
            "label": "Others",
            "type": "relation",
            "relation": {"entity": "lines", "field": "other", "cardinality": "many"},
        }
    )
    d = declaration(manifest)
    d["decimals"][0]["sign"] = "nonnegative"
    d["decimals"][1]["sign"] = "positive"
    path = [{"field": "header", "target": "headers"}]
    d["rules"].extend(
        [
            {
                "id": "sources",
                "kind": "atMostOne",
                "entity": "lines",
                "fields": ["header", "other"],
            },
            {
                "id": "owner",
                "kind": "sameTarget",
                "entity": "lines",
                "skipEmptyLeftEnd": True,
                "left": path,
                "right": path,
            },
        ]
    )
    assert validate_plugin_manifest(package_entries(manifest)) == manifest
    for section, index, patch in [
        ("decimals", 0, {"sign": "signed"}),
        ("decimals", 0, {"sign": None}),
        ("rules", -2, {"fields": ["header", "header"]}),
        ("rules", -2, {"fields": ["header", "amount"]}),
        ("rules", -2, {"entity": "headers", "fields": ["lines", "others"]}),
        ("rules", -2, {"fields": ["header"]}),
        ("rules", -2, {"skipEmptyLeftEnd": True}),
        ("rules", -1, {"skipEmptyLeftEnd": False}),
    ]:
        invalid = copy.deepcopy(manifest)
        declaration(invalid)[section][index].update(patch)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))


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


def delivery_manifest() -> dict:
    manifest = transactions_manifest()
    manifest["permissions"]["model"] = {
        "contract": "vibetable.plugin-model.v1",
        "modelId": "synthetic",
        "version": 1,
        "entities": [
            {
                "key": "obligations",
                "label": "交付义务",
                "operations": ["read", "query", "create", "update", "delete"],
                "fields": [
                    {"key": "quantity", "label": "义务数量", "type": "text"},
                    {
                        "key": "deliveries",
                        "label": "交付来源",
                        "type": "relation",
                        "relation": {"entity": "sources", "field": "scope", "cardinality": "many"},
                    },
                ],
            },
            {
                "key": "documents",
                "label": "交付单据",
                "operations": ["read", "query", "create", "update", "delete"],
                "fields": [
                    {"key": "phase", "label": "阶段", "type": "text"},
                    {
                        "key": "lines",
                        "label": "业务行",
                        "type": "relation",
                        "relation": {"entity": "lines", "field": "document", "cardinality": "many"},
                    },
                ],
            },
            {
                "key": "lines",
                "label": "交付业务行",
                "operations": ["read", "query", "create", "update", "delete"],
                "fields": [
                    {"key": "purpose", "label": "用途", "type": "text"},
                    {
                        "key": "real_qty",
                        "label": "真实数量",
                        "type": "number",
                        "precision": "float64",
                    },
                    {
                        "key": "document",
                        "label": "单据",
                        "type": "relation",
                        "relation": {"entity": "documents", "field": "lines", "cardinality": "one"},
                    },
                    {
                        "key": "sources",
                        "label": "来源",
                        "type": "relation",
                        "relation": {
                            "entity": "sources",
                            "field": "owner_line",
                            "cardinality": "many",
                        },
                    },
                ],
            },
            {
                "key": "sources",
                "label": "交付来源",
                "operations": ["read", "query", "create", "update", "delete"],
                "fields": [
                    {"key": "quantity", "label": "数量", "type": "text"},
                    {"key": "note", "label": "备注", "type": "text"},
                    {
                        "key": "scope",
                        "label": "义务",
                        "type": "relation",
                        "relation": {
                            "entity": "obligations",
                            "field": "deliveries",
                            "cardinality": "one",
                        },
                    },
                    {
                        "key": "owner_line",
                        "label": "所属行",
                        "type": "relation",
                        "relation": {"entity": "lines", "field": "sources", "cardinality": "one"},
                    },
                    {
                        "key": "instruction_source",
                        "label": "指令来源",
                        "type": "relation",
                        "relation": {
                            "entity": "sources",
                            "field": "instruction_uses",
                            "cardinality": "one",
                        },
                    },
                    {
                        "key": "instruction_uses",
                        "label": "指令占用",
                        "type": "relation",
                        "relation": {
                            "entity": "sources",
                            "field": "instruction_source",
                            "cardinality": "many",
                        },
                    },
                    {
                        "key": "transferred_from",
                        "label": "转出指令",
                        "type": "relation",
                        "relation": {
                            "entity": "sources",
                            "field": "transfer_targets",
                            "cardinality": "one",
                        },
                    },
                    {
                        "key": "transfer_targets",
                        "label": "转入指令",
                        "type": "relation",
                        "relation": {
                            "entity": "sources",
                            "field": "transferred_from",
                            "cardinality": "many",
                        },
                    },
                ],
            },
        ],
    }
    manifest["permissions"]["transactions"] = {
        "contract": "vibetable.plugin-transactions.v1",
        "modelId": "synthetic",
        "version": 1,
        "decimals": [
            {
                "entity": "sources",
                "field": "quantity",
                "storage": "decimalText",
                "scale": 0,
                "digits": 38,
                "sign": "nonnegative",
            },
            {
                "entity": "obligations",
                "field": "quantity",
                "storage": "decimalText",
                "scale": 0,
                "digits": 38,
                "sign": "positive",
            },
        ],
        "rules": [
            {
                "id": "freeze_sources",
                "kind": "parentFreeze",
                "entity": "sources",
                "parentPath": [
                    {"field": "owner_line", "target": "lines"},
                    {"field": "document", "target": "documents"},
                ],
                "parentStateField": "phase",
                "frozenStates": ["delivery_confirmed"],
                "fields": ["quantity", "scope", "instruction_source"],
            },
            {
                "id": "freeze_lines",
                "kind": "parentFreeze",
                "entity": "lines",
                "parentPath": [{"field": "document", "target": "documents"}],
                "parentStateField": "phase",
                "frozenStates": ["delivery_confirmed"],
                "fields": ["purpose", "real_qty"],
            },
            {
                "id": "capacity",
                "kind": "reservationBalance",
                "entity": "sources",
                "reservation": {
                    "obligation": {"entity": "obligations", "quantity": "quantity"},
                    "scope": "scope",
                    "quantity": "quantity",
                    "purpose": {
                        "path": [{"field": "owner_line", "target": "lines"}],
                        "field": "purpose",
                    },
                    "normalPurpose": "normal",
                    "phase": {
                        "path": [
                            {"field": "owner_line", "target": "lines"},
                            {"field": "document", "target": "documents"},
                        ],
                        "field": "phase",
                        "instructionDraft": "instruction_draft",
                        "instructionOpen": "instruction_open",
                        "instructionClosed": "instruction_closed",
                        "instructionTransferred": "instruction_transferred",
                        "deliveryDraft": "delivery_draft",
                        "deliveryConfirmed": "delivery_confirmed",
                    },
                    "instructionSource": "instruction_source",
                    "transferredFrom": "transferred_from",
                },
            },
        ],
    }
    return manifest


def test_delivery_guard_rules_form_a_closed_shape() -> None:
    manifest = delivery_manifest()
    assert PluginManifest.model_validate(manifest)
    assert validate_plugin_manifest(package_entries(manifest)) == manifest

    for index, patch in (
        (0, {"amount": "quantity"}),
        (0, {"parentStateField": ""}),
        (0, {"frozenStates": []}),
        (2, {"reservation": None}),
        (2, {"parentPath": [{"field": "owner_line", "target": "lines"}]}),
    ):
        invalid = copy.deepcopy(manifest)
        declaration(invalid)["rules"][index].update(patch)
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))


def test_delivery_guard_kind_fields_shared_with_go() -> None:
    corpus = json.loads(
        (Path(__file__).parents[2] / "contracts/plugin-transaction-kind-fields.json").read_text()
    )
    rules = copy.deepcopy(declaration(transactions_manifest())["rules"])
    rules.extend(
        [
            {"id": "one", "kind": "atMostOne", "entity": "sources", "fields": ["a", "b"]},
            {
                "id": "balance",
                "kind": "balance",
                "entity": "sources",
                "relation": "scope",
                "amount": "quantity",
                "target": "obligations",
                "limit": "quantity",
            },
            {
                "id": "same",
                "kind": "sameTarget",
                "entity": "sources",
                "left": [{"field": "scope", "target": "obligations"}],
                "right": [{"field": "scope", "target": "obligations"}],
            },
            declaration(delivery_manifest())["rules"][0],
            declaration(delivery_manifest())["rules"][2],
        ]
    )
    for rule in rules:
        assert TransactionRule.model_validate(rule)
        patches = corpus["parentFreeze" if rule["kind"] == "parentFreeze" else "legacy"]
        if rule["kind"] != "sameTarget":
            patches = [*patches, *corpus["sameTargetOnly"]]
        for patch in patches:
            with pytest.raises(ValidationError):
                TransactionRule.model_validate(rule | patch)

    for factory, index, patches in (
        (transactions_manifest, 0, corpus["legacy"]),
        (delivery_manifest, 0, corpus["parentFreeze"]),
    ):
        for patch in patches:
            manifest = factory()
            declaration(manifest)["rules"][index].update(patch)
            with pytest.raises(ValidationError):
                PluginManifest.model_validate(manifest)
            with pytest.raises(PluginPackageError):
                validate_plugin_manifest(package_entries(manifest))


def test_delivery_guard_quantity_and_purpose_validation() -> None:
    manifest = delivery_manifest()
    for section, index, patch in (
        ("decimals", 0, {"nullable": True}),
        ("decimals", 1, {"sign": None}),
        ("decimals", 1, {"scale": 2}),
        (
            "rules",
            2,
            {
                "reservation": {
                    "obligation": {"entity": "obligations", "quantity": "quantity"},
                    "scope": "scope",
                    "quantity": "quantity",
                    "purpose": {
                        "path": [{"field": "owner_line", "target": "lines"}],
                        "field": "missing",
                    },
                    "normalPurpose": "normal",
                    "phase": {
                        "path": [
                            {"field": "owner_line", "target": "lines"},
                            {"field": "document", "target": "documents"},
                        ],
                        "field": "phase",
                        "instructionDraft": "instruction_draft",
                        "instructionOpen": "instruction_open",
                        "instructionClosed": "instruction_closed",
                        "instructionTransferred": "instruction_transferred",
                        "deliveryDraft": "delivery_draft",
                        "deliveryConfirmed": "delivery_confirmed",
                    },
                    "instructionSource": "instruction_source",
                    "transferredFrom": "transferred_from",
                }
            },
        ),
    ):
        invalid = copy.deepcopy(manifest)
        declaration(invalid)[section][index].update(patch)
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))


def test_model_capacity_allows_forty_entities() -> None:
    manifest = transactions_manifest()
    model = manifest["permissions"]["model"]
    for index in range(40 - len(model["entities"])):
        model["entities"].append(
            {
                "key": f"extra_{index}",
                "label": f"扩展{index}",
                "operations": ["read", "query"],
                "fields": [{"key": "code", "label": "代码", "type": "text"}],
            }
        )
    assert PluginManifest.model_validate(manifest)
    model["entities"].append(
        {
            "key": "over",
            "label": "超额",
            "operations": ["read", "query"],
            "fields": [{"key": "code", "label": "代码", "type": "text"}],
        }
    )
    with pytest.raises(ValidationError):
        PluginManifest.model_validate(manifest)


def reversal_manifest() -> dict:
    manifest = delivery_manifest()
    entities = manifest["permissions"]["model"]["entities"]
    entities[1]["fields"].append({"key": "agreement", "label": "agreement", "type": "text"})
    entities[2]["fields"].extend(
        {"key": field, "label": field, "type": "text"}
        for field in ("quantity", "material", "project", "package", "unit", "currency")
    )
    entities[2]["fields"].extend(
        [
            {
                "key": "original_line",
                "label": "original",
                "type": "relation",
                "relation": {"entity": "lines", "field": "reversals", "cardinality": "one"},
            },
            {
                "key": "reversals",
                "label": "reversals",
                "type": "relation",
                "relation": {"entity": "lines", "field": "original_line", "cardinality": "many"},
            },
        ]
    )
    d = declaration(manifest)
    d["decimals"].append(
        {
            "entity": "lines",
            "field": "quantity",
            "storage": "decimalText",
            "scale": 0,
            "digits": 38,
            "sign": "positive",
        }
    )
    d["rules"].append(
        json.loads((Path(__file__).parents[2] / "contracts/plugin-reversal-rule.json").read_text())
    )
    return manifest


def test_new_rule_null_shapes_match_go() -> None:
    corpus = json.loads(
        (Path(__file__).parents[2] / "contracts/plugin-transaction-null-cases.json").read_text(
            encoding="utf-8"
        )
    )

    def validate(base: str, path: list[str], value: object = None, omit: bool = False) -> None:
        rule = copy.deepcopy(corpus["rules"][base])
        node = rule
        for index, key in enumerate(path):
            key = int(key) if isinstance(node, list) else key
            if index == len(path) - 1:
                if omit:
                    del node[key]
                else:
                    node[key] = value
            else:
                node = node[key]
        PluginTransactionsDeclaration.model_validate(
            {
                "contract": "vibetable.plugin-transactions.v1",
                "modelId": "m",
                "version": 1,
                "rules": [rule],
                "decimals": [
                    {
                        "entity": "sources",
                        "field": "quantity",
                        "storage": "decimalText",
                        "scale": 0,
                        "digits": 38,
                        "sign": "nonnegative",
                    },
                    {
                        "entity": "obligations",
                        "field": "quantity",
                        "storage": "decimalText",
                        "scale": 0,
                        "digits": 38,
                        "sign": "nonnegative",
                    },
                    *invoice_guard_decimals(),
                ],
            }
        )

    for base in corpus["rules"]:
        validate(base, [])
    for case in corpus["invalid"]:
        with pytest.raises(ValidationError):
            validate(case["base"], case["path"], case.get("value"))
    for case in corpus["valid"]:
        validate(case["base"], case["path"], case.get("value"), case.get("omit", False))


def invoice_guard_decimals() -> list[dict]:
    """Mirrors the Go null-shape harness decimals for the new rule corpus."""

    def decimal(entity: str, field: str, scale: int, sign: str | None = None) -> dict:
        entry = {
            "entity": entity,
            "field": field,
            "storage": "decimalText",
            "scale": scale,
            "digits": 38,
            "nullable": True,
        }
        if sign:
            entry["sign"] = sign
        return entry

    decimals = []
    for entity in ("allocations", "actual_lines", "request_sources"):
        decimals.append(
            decimal(entity, "quantity", 0, "nonnegative" if entity == "request_sources" else None)
        )
        decimals += [decimal(entity, field, 2) for field in ("net", "tax", "gross")]
    for field, scale in (
        ("quantity", 0),
        ("net", 2),
        ("tax", 2),
        ("gross", 2),
    ):
        decimals.append(decimal("adjustments", field, scale, "nonnegative"))
    return decimals


def test_invoice_guard_corpus_rules_form_closed_manifest_declarations() -> None:
    """The natural seven-entity corpus registers through the manifest seam."""
    for name in (
        "plugin-allocation-rule.json",
        "plugin-settlement-rule.json",
        "plugin-evidence-rule.json",
    ):
        rule = json.loads(
            (Path(__file__).parents[2] / "contracts" / name).read_text(encoding="utf-8")
        )
        parsed = PluginTransactionsDeclaration.model_validate(
            {
                "contract": "vibetable.plugin-transactions.v1",
                "modelId": "m",
                "version": 1,
                "rules": [rule],
                "decimals": invoice_guard_decimals(),
            }
        )
        assert parsed.rules[0].kind in (
            "allocationBalance",
            "settlementBalance",
            "changeEvidence",
        )
    # The 65th decimal registers and the 66th is rejected with data intact.
    manifest = transactions_manifest()
    decimals = declaration(manifest)["decimals"]
    decimals.extend({**decimals[0], "field": f"n{index}"} for index in range(65 - len(decimals)))
    assert len(decimals) == 65
    assert PluginManifest.model_validate(manifest)
    decimals.append({**decimals[0], "field": "over"})
    with pytest.raises(ValidationError):
        PluginManifest.model_validate(manifest)


def invoice_guard_manifest() -> dict:
    manifest = transactions_manifest()
    model = manifest["permissions"]["model"]
    entities = {}
    for entity, names in {
        "requests": ["status", "direction", "purpose", "basis", "currency"],
        "request_lines": ["unit", "material"],
        "request_sources": ["quantity", "net", "tax", "gross"],
        "invoices": ["status", "direction", "amount_convention", "currency"],
        "actual_lines": ["purpose", "unit", "material", "quantity", "net", "tax", "gross"],
        "allocations": ["quantity", "net", "tax", "gross"],
        "adjustments": [
            "kind",
            "note",
            "date",
            "close_remaining",
            "quantity",
            "net",
            "tax",
            "gross",
        ],
    }.items():
        entities[entity] = {
            "key": entity,
            "label": entity,
            "operations": ["read", "query", "create", "update", "delete"],
            "fields": [{"key": name, "label": name, "type": "text"} for name in names],
        }
    for source, field, target, reverse in (
        ("request_lines", "request", "requests", "lines"),
        ("request_sources", "line", "request_lines", "sources"),
        ("actual_lines", "invoice", "invoices", "lines"),
        ("allocations", "line", "actual_lines", "allocations"),
        ("request_sources", "original", "allocations", "requests"),
        ("allocations", "original", "allocations", "reversals"),
        ("allocations", "application_source", "request_sources", "actuals"),
        ("adjustments", "source", "request_sources", "adjustments"),
        ("adjustments", "allocation", "allocations", "adjustments"),
        ("adjustments", "invoice", "invoices", "evidence"),
        ("invoices", "latest_evidence", "adjustments", "latest_for"),
    ):
        for entity, key, other, back, cardinality in (
            (source, field, target, reverse, "one"),
            (target, reverse, source, field, "many"),
        ):
            entities[entity]["fields"].append(
                {
                    "key": key,
                    "label": key,
                    "type": "relation",
                    "relation": {"entity": other, "field": back, "cardinality": cardinality},
                }
            )
    model["entities"] = list(entities.values())
    rules = [
        json.loads(
            (Path(__file__).parents[2] / "contracts" / f"plugin-{name}-rule.json").read_text(
                encoding="utf-8"
            )
        )
        for name in ("allocation", "settlement", "evidence")
    ]
    scope = rules[1]["settlement"]
    for entry in ("capacity", "request", "actual"):
        path = scope[entry]["direction"]["path"]
        scope[entry]["currency"] = {"path": path, "field": "currency"}
        for field in ("unit", "material"):
            scope[entry][field] = {"path": path[:1], "field": field}
    manifest["permissions"]["transactions"] = {
        "contract": "vibetable.plugin-transactions.v1",
        "modelId": "synthetic",
        "version": 1,
        "decimals": invoice_guard_decimals(),
        "rules": rules,
    }
    return manifest


def test_settlement_plan_shape_rejects_money_measures() -> None:
    rule = declaration(invoice_guard_manifest())["rules"][1]
    rule["settlement"]["request"]["planItem"] = "plan_item"
    rule["settlement"]["plans"] = {
        "entity": "plans",
        "quantity": "quantity",
        "scope": "scope",
        "share": "share",
        "logicalItem": {"field": "logical", "target": "logical_items"},
        "currentWhen": {
            "left": [{"field": "version", "target": "versions"}],
            "right": [{"field": "current", "target": "versions"}],
        },
    }
    with pytest.raises(ValidationError, match="quantity"):
        TransactionRule.model_validate(rule)


def external_optional_settlement_rule() -> dict:
    rule = json.loads(
        (Path(__file__).parents[2] / "contracts" / "plugin-settlement-rule.json").read_text(
            encoding="utf-8"
        )
    )
    spec = rule["settlement"]
    spec["capacity"] = {"entity": "obligations", "quantity": "quantity", "optional": True}
    spec["request"]["directions"] = ["blue"]
    spec["request"].pop("purpose", None)
    spec["request"].pop("purposes", None)
    spec["request"]["capacity"] = "obligation"
    spec["actual"]["directions"] = ["blue"]
    spec["actual"].pop("purpose", None)
    spec["actual"].pop("purposes", None)
    spec["actual"].pop("zeroQuantityPurposes", None)
    spec["actual"]["capacity"] = "obligation"
    # The real normal blue path pairs request/actual scope refs and omits the
    # capacity refs (the candidate shape has no capacity.currency).
    spec["request"]["currency"] = {
        "path": [
            {"field": "line", "target": "request_lines"},
            {"field": "request", "target": "requests"},
        ],
        "field": "currency",
    }
    spec["actual"]["currency"] = {
        "path": [
            {"field": "line", "target": "actual_lines"},
            {"field": "invoice", "target": "invoices"},
        ],
        "field": "currency",
    }
    for role, line in (("request", "request_lines"), ("actual", "actual_lines")):
        spec[role]["unit"] = {"path": [{"field": "line", "target": line}], "field": "unit"}
        spec[role]["material"] = {
            "path": [{"field": "line", "target": line}],
            "field": "material",
        }
    return rule


def external_optional_declaration(rule: dict) -> dict:
    return {
        "contract": "vibetable.plugin-transactions.v1",
        "modelId": "m",
        "version": 1,
        "rules": [rule],
        "decimals": [
            {
                "entity": "obligations",
                "field": "quantity",
                "storage": "decimalText",
                "scale": 0,
                "digits": 38,
                "sign": "nonnegative",
            },
            *invoice_guard_decimals(),
        ],
    }


def test_settlement_optional_external_capacity_subset() -> None:
    """optional:true separates the source's full measures from the capacity subset."""
    parsed = PluginTransactionsDeclaration.model_validate(
        external_optional_declaration(external_optional_settlement_rule())
    )
    capacity = parsed.rules[0].settlement.capacity
    assert capacity.optional is True
    assert capacity.quantity
    assert not capacity.net
    # Only literal true registers; false, null and wrong types stay closed.
    for value in (False, None, "true", 1):
        rule = external_optional_settlement_rule()
        rule["settlement"]["capacity"]["optional"] = value
        with pytest.raises(ValidationError):
            PluginTransactionsDeclaration.model_validate(external_optional_declaration(rule))
    # The shared original-blue bucket never enables optional.
    red = json.loads(
        (Path(__file__).parents[2] / "contracts" / "plugin-settlement-rule.json").read_text(
            encoding="utf-8"
        )
    )
    red["settlement"]["capacity"]["optional"] = True
    with pytest.raises(ValidationError):
        TransactionRule.model_validate(red)
    # A capacity measure outside the identical request/actual set is rejected.
    drift = external_optional_settlement_rule()
    drift["settlement"]["capacity"]["net"] = "net"
    drift["settlement"]["request"].pop("net")
    drift["settlement"]["actual"].pop("net")
    drift["settlement"]["adjustment"]["closeNet"] = ""
    with pytest.raises(ValidationError):
        PluginTransactionsDeclaration.model_validate(external_optional_declaration(drift))
    mismatch = external_optional_settlement_rule()
    mismatch["settlement"]["actual"].pop("gross")
    with pytest.raises(ValidationError):
        PluginTransactionsDeclaration.model_validate(external_optional_declaration(mismatch))
    # Close facts follow the source measure set, not the capacity subset.
    partial = external_optional_settlement_rule()
    partial["settlement"]["adjustment"].pop("closeQuantity")
    with pytest.raises(ValidationError):
        PluginTransactionsDeclaration.model_validate(external_optional_declaration(partial))
    # A capacity scope ref without the paired request/actual ref is rejected.
    orphan = external_optional_settlement_rule()
    orphan["settlement"]["capacity"]["currency"] = {"field": "currency"}
    del orphan["settlement"]["request"]["currency"]
    del orphan["settlement"]["actual"]["currency"]
    with pytest.raises(ValidationError):
        PluginTransactionsDeclaration.model_validate(external_optional_declaration(orphan))
    # Request/actual scope must stay paired.
    unpaired = external_optional_settlement_rule()
    del unpaired["settlement"]["actual"]["unit"]
    with pytest.raises(ValidationError):
        PluginTransactionsDeclaration.model_validate(external_optional_declaration(unpaired))


def test_state_cases_and_group_contracts_bind_all_declared_relations() -> None:
    manifest = invoice_guard_manifest()
    model = manifest["permissions"]["model"]
    entities = {entity["key"]: entity for entity in model["entities"]}

    def ensure_entity(key: str) -> None:
        if key not in entities:
            entities[key] = {
                "key": key,
                "label": key,
                "operations": ["read", "query", "create", "update", "delete"],
                "fields": [{"key": "label", "label": "label", "type": "text"}],
            }

    def edge(source: str, field: str, target: str) -> None:
        ensure_entity(source)
        ensure_entity(target)
        if any(existing["key"] == field for existing in entities[source]["fields"]):
            return
        reverse = f"back_{source}_{field}"
        entities[source]["fields"].append(
            {
                "key": field,
                "label": field,
                "type": "relation",
                "relation": {"entity": target, "field": reverse, "cardinality": "one"},
            }
        )
        entities[target]["fields"].append(
            {
                "key": reverse,
                "label": reverse,
                "type": "relation",
                "relation": {"entity": source, "field": field, "cardinality": "many"},
            }
        )

    def path(entity: str, steps: list[dict]) -> None:
        for step in steps:
            edge(entity, step["field"], step["target"])
            entity = step["target"]

    root = Path(__file__).parents[2] / "contracts"
    state = json.loads((root / "plugin-state-relations-rule.json").read_text(encoding="utf-8"))
    group = json.loads((root / "plugin-group-same-target-rule.json").read_text(encoding="utf-8"))
    entities[state["entity"]]["fields"].append(
        {"key": state["field"], "label": state["field"], "type": "text"}
    )
    for case in state["relationCases"].values():
        for step in [*case["required"], *case["optional"]]:
            edge(state["entity"], step["field"], step["target"])
    path(group["entity"], group["group"])
    for selector in group["targets"]:
        path(group["entity"], selector["path"])
    edge(group["entity"], group["related"]["field"], group["related"]["target"])
    reference = group["reference"]
    path(reference["entity"], reference["group"])
    for selector in reference["targets"]:
        path(reference["entity"], selector["path"])
    model["entities"] = list(entities.values())
    declaration(manifest)["rules"].extend([state, group])
    assert PluginManifest.model_validate(manifest)
    assert validate_plugin_manifest(package_entries(manifest)) == manifest
    for index, edit in ((-2, "case"), (-1, "group")):
        invalid = copy.deepcopy(manifest)
        rule = declaration(invalid)["rules"][index]
        if edit == "case":
            rule["relationCases"]["delivery"]["required"][0]["field"] = "missing"
        else:
            rule["targets"][0]["path"][0]["field"] = "missing"
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))


def test_invoice_natural_rules_validate_model_at_both_package_seams() -> None:
    manifest = invoice_guard_manifest()
    assert PluginManifest.model_validate(manifest)
    assert validate_plugin_manifest(package_entries(manifest)) == manifest
    for patch in ("owner", "currency", "unit", "material", "evidence-fact", "relation"):
        invalid = copy.deepcopy(manifest)
        rules = declaration(invalid)["rules"]
        if patch == "owner":
            rules[0]["allocation"]["owner"] = "missing"
        elif patch in ("currency", "unit", "material"):
            rules[1]["settlement"]["capacity"][patch]["field"] = "missing"
        elif patch == "evidence-fact":
            rules[2]["evidence"]["evidence"]["fields"].append("missing")
        else:
            rules[1]["settlement"]["actual"]["request"] = "line"
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))
    # A scope dimension cannot break the request/actual pair; the capacity
    # ref is optional and may be omitted while the pair stays declared.
    for field in ("currency", "unit", "material"):
        invalid = copy.deepcopy(manifest)
        rules = declaration(invalid)["rules"]
        del rules[1]["settlement"]["request"][field]
        del rules[1]["settlement"]["actual"][field]
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        paired = copy.deepcopy(manifest)
        del declaration(paired)["rules"][1]["settlement"]["capacity"][field]
        assert PluginManifest.model_validate(paired)


def test_allocation_coverage_requires_text_on_a_parent_path() -> None:
    manifest = invoice_guard_manifest()
    allocation = declaration(manifest)["rules"][0]["allocation"]
    allocation["requireCompleteWhenAllocated"] = True
    allocation["requireCompleteWhen"] = [
        {
            "ref": {"path": [{"field": "invoice", "target": "invoices"}], "field": "status"},
            "equals": "issued",
        }
    ]
    assert PluginManifest.model_validate(manifest)
    assert validate_plugin_manifest(package_entries(manifest)) == manifest

    ref = allocation["requireCompleteWhen"][0]["ref"]
    for invalid_ref in (
        {**ref, "field": "missing"},
        {**ref, "field": "latest_evidence"},
        {**ref, "path": [{"field": "invoice", "target": "requests"}]},
    ):
        allocation["requireCompleteWhen"][0]["ref"] = invalid_ref
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(manifest)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(manifest))

    allocation["requireCompleteWhen"][0]["ref"] = ref
    del allocation["requireCompleteWhenAllocated"]
    with pytest.raises(ValidationError):
        PluginManifest.model_validate(manifest)


def test_reversal_shape_quantity_and_model_binding_match_native_contract() -> None:
    manifest = reversal_manifest()
    assert PluginManifest.model_validate(manifest)
    assert validate_plugin_manifest(package_entries(manifest)) == manifest
    patches = [
        {"original": "missing"},
        {"quantity": "missing"},
        {"purpose": "missing"},
        {"ownerPath": []},
        {"ownerPath": [{"field": "document", "target": "lines"}]},
        {"ownerStateField": "missing"},
        {"requiredStates": []},
        {"requiredStates": ["delivery_confirmed", "delivery_confirmed"]},
        {"confirmedState": "missing"},
        {"originalPurpose": "salesReturn"},
        {"sameFields": ["missing"]},
        {"sameFields": ["unit", "unit"]},
        {"sameOwnerFields": ["missing"]},
        {"script": "ignore"},
    ]
    for patch in patches:
        invalid = copy.deepcopy(manifest)
        declaration(invalid)["rules"][-1]["reversal"].update(patch)
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))
    for patch in ({"nullable": True}, {"sign": None}):
        invalid = copy.deepcopy(manifest)
        declaration(invalid)["decimals"][-1].update(patch)
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))
    for patch in ({"reservation": None}, {"fields": []}, {"parentPath": None}, {"reversal": None}):
        invalid = copy.deepcopy(manifest)
        declaration(invalid)["rules"][-1].update(patch)
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))


def test_settlement_plan_model_fields_rejected_at_package_seams() -> None:
    manifest = invoice_guard_manifest()
    model = manifest["permissions"]["model"]
    entities = {entity["key"]: entity for entity in model["entities"]}
    for key, fields in (
        ("plans", ("quantity", "share")),
        ("logical_items", ("label",)),
        ("versions", ("label",)),
    ):
        entities[key] = {
            "key": key,
            "label": key,
            "operations": ["read", "query", "create", "update", "delete"],
            "fields": [{"key": field, "label": field, "type": "text"} for field in fields],
        }

    def edge(source: str, field: str, target: str) -> None:
        reverse = f"back_{source}_{field}"
        for owner, key, other, inverse, cardinality in (
            (source, field, target, reverse, "one"),
            (target, reverse, source, field, "many"),
        ):
            entities[owner]["fields"].append(
                {
                    "key": key,
                    "label": key,
                    "type": "relation",
                    "relation": {"entity": other, "field": inverse, "cardinality": cardinality},
                }
            )

    edge("request_sources", "plan_item", "plans")
    edge("plans", "scope", "allocations")
    edge("plans", "logical", "logical_items")
    edge("plans", "version", "versions")
    edge("plans", "current", "versions")
    model["entities"] = list(entities.values())
    transactions = declaration(manifest)
    rule = transactions["rules"][1]
    transactions["rules"] = [rule]
    spec = rule["settlement"]
    spec["request"]["planItem"] = "plan_item"
    for role in ("capacity", "request", "actual"):
        for field in ("net", "tax", "gross"):
            spec[role].pop(field)
    for field in ("closeNet", "closeTax", "closeGross"):
        spec["adjustment"].pop(field)
    spec["plans"] = {
        "entity": "plans",
        "quantity": "quantity",
        "scope": "scope",
        "share": "share",
        "logicalItem": {"field": "logical", "target": "logical_items"},
        "currentWhen": {
            "left": [{"field": "version", "target": "versions"}],
            "right": [{"field": "current", "target": "versions"}],
        },
    }
    transactions["decimals"].append(
        {
            "entity": "plans",
            "field": "quantity",
            "storage": "decimalText",
            "scale": 0,
            "digits": 38,
            "sign": "nonnegative",
        }
    )
    assert PluginManifest.model_validate(manifest)
    assert validate_plugin_manifest(package_entries(manifest)) == manifest
    for field in ("quantity", "share"):
        invalid = copy.deepcopy(manifest)
        plans = next(
            entity
            for entity in invalid["permissions"]["model"]["entities"]
            if entity["key"] == "plans"
        )
        plans["fields"] = [entry for entry in plans["fields"] if entry["key"] != field]
        with pytest.raises(ValidationError):
            PluginManifest.model_validate(invalid)
        with pytest.raises(PluginPackageError):
            validate_plugin_manifest(package_entries(invalid))
