"""Language-neutral checks for the isolated Schema v2 domain contract."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from tests.contract.test_product_contracts import SchemaMismatchError, _validate

ROOT = Path(__file__).parents[2]
CONTRACT_ROOT = ROOT / "contracts" / "schema-v2"
SCHEMA_PATH = CONTRACT_ROOT / "schema.schema.json"
FIXTURES = CONTRACT_ROOT / "fixtures"

FIXTURE_DEFINITIONS = {
    "field-definition.json": "FieldDefinition",
    "capability.json": "Capability",
    "field-change-intent.json": "FieldChangeIntent",
    "field-change-plan.json": "FieldChangePlan",
    "apply-request.json": "ApplyRequest",
    "apply-receipt.json": "ApplyReceipt",
    "migration-status.json": "MigrationStatus",
    "field-settings-describe.json": "FieldSettingsDescribeResult",
    "field-recycle-bin.json": "FieldRecycleBinResult",
    "field-value-entry-corpus.json": "FieldValueEntryCorpus",
}


def _load(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def test_schema_v2_bundle_has_only_local_refs_and_required_public_types() -> None:
    schema = _load(SCHEMA_PATH)
    assert schema["$schema"] == "https://json-schema.org/draft/2020-12/schema"
    required = {
        "FieldDefinition",
        "Capability",
        "FieldChangeIntent",
        "FieldChangePlan",
        "ApplyRequest",
        "ApplyReceipt",
        "MigrationStatus",
        "FieldSettingsDescribeResult",
        "FieldRecycleBinResult",
        "FieldValueEntryCorpus",
    }
    assert required <= schema["$defs"].keys()
    serialized = json.dumps(schema)
    assert "vibetable.schema.v2" in serialized
    assert "http://" not in serialized.replace(schema["$schema"], "").replace(schema["$id"], "")


def test_schema_v2_fixtures_validate_and_round_trip() -> None:
    schema = _load(SCHEMA_PATH)
    assert {path.name for path in FIXTURES.glob("*.json")} == set(FIXTURE_DEFINITIONS)
    for filename, definition in FIXTURE_DEFINITIONS.items():
        payload = _load(FIXTURES / filename)
        _validate(payload, schema["$defs"][definition], schema)
        _validate(payload, schema, schema)
        assert json.loads(json.dumps(payload, ensure_ascii=False, sort_keys=True)) == payload


def test_schema_v2_rejects_unknown_properties_at_nested_seams() -> None:
    schema = _load(SCHEMA_PATH)
    field = _load(FIXTURES / "field-definition.json")
    candidates = []
    for path, key in (
        (field, "providerSecret"),
        (field["identity"], "legacyId"),
        (field["value"]["default"], "raw"),
        (field["storage"]["options"], "ddl"),
        (field["display"], "sql"),
    ):
        candidate = json.loads(json.dumps(field))
        target = candidate
        if path is field["identity"]:
            target = candidate["identity"]
        elif path is field["value"]["default"]:
            target = candidate["value"]["default"]
        elif path is field["storage"]["options"]:
            target = candidate["storage"]["options"]
        elif path is field["display"]:
            target = candidate["display"]
        target[key] = True
        candidates.append(candidate)

    for candidate in candidates:
        try:
            _validate(candidate, schema["$defs"]["FieldDefinition"], schema)
        except SchemaMismatchError:
            pass
        else:
            raise AssertionError("unknown nested Schema v2 property was accepted")


def test_schema_v2_field_menu_excludes_unsupported_provider_types() -> None:
    schema = _load(SCHEMA_PATH)
    logical_types = set(schema["$defs"]["LogicalType"]["enum"])
    assert {"hash", "secret", "decimal", "password"}.isdisjoint(logical_types)


def test_schema_v2_rejects_mismatched_logical_type_settings() -> None:
    schema = _load(SCHEMA_PATH)
    field = _load(FIXTURES / "field-definition.json")
    field["relation"] = {
        "targetTableId": "tbl_customers",
        "cardinality": "one",
        "deletePolicy": "setNull",
        "displayFieldId": "fld_name",
    }
    try:
        _validate(field, schema["$defs"]["FieldDefinition"], schema)
    except SchemaMismatchError:
        pass
    else:
        raise AssertionError("number field accepted relation-only settings")


def test_schema_v2_shared_negative_field_cases_are_all_rejected() -> None:
    schema = _load(SCHEMA_PATH)
    base = _load(FIXTURES / "field-definition.json")
    cases = _load(FIXTURES / "invalid" / "field-definition-cases.json")
    assert len(cases) >= 4
    for case in cases:
        candidate = json.loads(json.dumps(base))
        target = candidate
        for segment in case["path"][:-1]:
            target = target[segment]
        key = case["path"][-1]
        if case.get("remove"):
            target.pop(key)
        else:
            target[key] = case["value"]
        try:
            _validate(candidate, schema["$defs"]["FieldDefinition"], schema)
        except SchemaMismatchError:
            pass
        else:
            raise AssertionError(f"shared invalid case was accepted: {case['name']}")
