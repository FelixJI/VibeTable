"""Dependency-free checks for the language-neutral v1 product contracts."""

from __future__ import annotations

import ast
import json
import math
import re
from pathlib import Path
from typing import Any

ROOT = Path(__file__).parents[2]
CONTRACT_ROOT = ROOT / "contracts" / "v1"
SCHEMA_PATH = CONTRACT_ROOT / "contracts.schema.json"
FIXTURES = CONTRACT_ROOT / "fixtures"

FIXTURE_DEFINITIONS = {
    "table-definition.json": "TableDefinition",
    "product-error.json": "ProductError",
    "formula-error.json": "FormulaError",
    "mutation-request.json": "MutationRequest",
    "mutation-receipt.json": "MutationReceipt",
    "managed-attachment-ref.json": "ManagedAttachmentRef",
    "data-changed-event.json": "DataChangedEvent",
    "task-changed-event.json": "TaskChangedEvent",
    "rpc-catalog.json": "RpcContractCatalog",
}

FIELD_KINDS = {"scalar", "relation", "lookup", "formula", "attachment", "system"}
CONSTRAINT_KINDS = {
    "required",
    "default",
    "unique",
    "index",
    "range",
    "length",
    "pattern",
    "precisionScale",
    "enum",
    "jsonSchema",
    "relation",
    "attachment",
}


class SchemaMismatchError(AssertionError):
    """Raised when a fixture violates the supported v1 schema subset."""


def _load(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def _json_type_matches(value: Any, expected: str) -> bool:
    if expected == "null":
        return value is None
    if expected == "boolean":
        return isinstance(value, bool)
    if expected == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if expected == "number":
        return (
            isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)
        )
    if expected == "string":
        return isinstance(value, str)
    if expected == "array":
        return isinstance(value, list)
    if expected == "object":
        return isinstance(value, dict)
    raise AssertionError(f"unsupported JSON Schema type in contract test: {expected}")


def _validate(instance: Any, schema: Any, root: dict[str, Any], path: str = "$") -> None:
    """Validate the JSON Schema subset used by ``contracts.schema.json``."""

    if schema is True or schema == {}:
        return
    if schema is False:
        raise SchemaMismatchError(f"{path}: schema rejects every value")
    if "$ref" in schema:
        prefix = "#/$defs/"
        ref = schema["$ref"]
        if not ref.startswith(prefix):
            raise AssertionError(f"unsupported external reference: {ref}")
        _validate(instance, root["$defs"][ref.removeprefix(prefix)], root, path)
        return
    if "oneOf" in schema:
        matches = 0
        reasons: list[str] = []
        for candidate in schema["oneOf"]:
            try:
                _validate(instance, candidate, root, path)
            except SchemaMismatchError as exc:
                reasons.append(str(exc))
            else:
                matches += 1
        if matches != 1:
            raise SchemaMismatchError(
                f"{path}: expected exactly one oneOf match, got {matches}; "
                + " | ".join(reasons[:3])
            )
        return
    if "anyOf" in schema:
        reasons: list[str] = []
        for candidate in schema["anyOf"]:
            try:
                _validate(instance, candidate, root, path)
            except SchemaMismatchError as exc:
                reasons.append(str(exc))
            else:
                return
        raise SchemaMismatchError(
            f"{path}: expected at least one anyOf match; " + " | ".join(reasons[:3])
        )
    if "allOf" in schema:
        for candidate in schema["allOf"]:
            _validate(instance, candidate, root, path)
    if "if" in schema:
        try:
            _validate(instance, schema["if"], root, path)
        except SchemaMismatchError:
            if "else" in schema:
                _validate(instance, schema["else"], root, path)
        else:
            if "then" in schema:
                _validate(instance, schema["then"], root, path)
    if "not" in schema:
        try:
            _validate(instance, schema["not"], root, path)
        except SchemaMismatchError:
            pass
        else:
            raise SchemaMismatchError(f"{path}: value matched a prohibited schema")

    expected_type = schema.get("type")
    if expected_type is not None:
        allowed = [expected_type] if isinstance(expected_type, str) else expected_type
        if not any(_json_type_matches(instance, item) for item in allowed):
            raise SchemaMismatchError(
                f"{path}: expected type {allowed}, got {type(instance).__name__}"
            )
    if "const" in schema and instance != schema["const"]:
        raise SchemaMismatchError(f"{path}: expected const {schema['const']!r}")
    if "enum" in schema and instance not in schema["enum"]:
        raise SchemaMismatchError(f"{path}: {instance!r} is not in {schema['enum']!r}")

    if isinstance(instance, str):
        if len(instance) < schema.get("minLength", 0):
            raise SchemaMismatchError(f"{path}: string is shorter than minLength")
        if "pattern" in schema and re.search(schema["pattern"], instance) is None:
            raise SchemaMismatchError(f"{path}: string does not match {schema['pattern']!r}")

    if (
        isinstance(instance, (int, float))
        and not isinstance(instance, bool)
        and math.isfinite(instance)
    ):
        if "minimum" in schema and instance < schema["minimum"]:
            raise SchemaMismatchError(f"{path}: number is below minimum")
        if "maximum" in schema and instance > schema["maximum"]:
            raise SchemaMismatchError(f"{path}: number is above maximum")

    if isinstance(instance, list):
        if len(instance) < schema.get("minItems", 0):
            raise SchemaMismatchError(f"{path}: array is shorter than minItems")
        if schema.get("uniqueItems"):
            canonical = [json.dumps(item, sort_keys=True) for item in instance]
            if len(canonical) != len(set(canonical)):
                raise SchemaMismatchError(f"{path}: array items are not unique")
        if "items" in schema:
            for index, item in enumerate(instance):
                _validate(item, schema["items"], root, f"{path}[{index}]")

    if isinstance(instance, dict):
        required = set(schema.get("required", []))
        missing = required - instance.keys()
        if missing:
            raise SchemaMismatchError(f"{path}: missing required properties {sorted(missing)}")
        properties = schema.get("properties", {})
        for key, value in instance.items():
            if key in properties:
                _validate(value, properties[key], root, f"{path}.{key}")
                continue
            additional = schema.get("additionalProperties", True)
            if additional is False:
                raise SchemaMismatchError(f"{path}: unexpected property {key!r}")
            if isinstance(additional, dict):
                _validate(value, additional, root, f"{path}.{key}")


def _const_kinds(schema: dict[str, Any], definition_name: str) -> set[str]:
    result: set[str] = set()
    for branch in schema["$defs"][definition_name]["oneOf"]:
        target = schema["$defs"][branch["$ref"].removeprefix("#/$defs/")]
        kind_schema = target["properties"]["kind"]
        if "const" in kind_schema:
            result.add(kind_schema["const"])
        else:
            result.update(kind_schema["enum"])
    return result


def test_schema_bundle_has_required_public_definitions_and_local_refs() -> None:
    schema = _load(SCHEMA_PATH)
    assert schema["$schema"] == "https://json-schema.org/draft/2020-12/schema"
    required = set(FIXTURE_DEFINITIONS.values()) | {"FieldDefinition", "FieldConstraint"}
    assert required <= schema["$defs"].keys()

    serialized = json.dumps(schema)
    refs = re.findall(r'"\\$ref":\\s*"(#/[^\"]+)"', serialized)
    for ref in refs:
        prefix = "#/$defs/"
        assert ref.startswith(prefix)
        assert ref.removeprefix(prefix) in schema["$defs"], ref


def test_fixtures_validate_and_round_trip_without_shape_changes() -> None:
    schema = _load(SCHEMA_PATH)
    assert {path.name for path in FIXTURES.glob("*.json")} == set(FIXTURE_DEFINITIONS)

    for filename, definition in FIXTURE_DEFINITIONS.items():
        payload = _load(FIXTURES / filename)
        _validate(payload, schema["$defs"][definition], schema)
        _validate(payload, schema, schema)
        assert payload["contractVersion"] == "1.0"
        wire = json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
        assert json.loads(wire) == payload


def test_autodate_roles_are_closed_and_fixture_covers_both_roles() -> None:
    schema = _load(SCHEMA_PATH)
    table = _load(FIXTURES / "table-definition.json")
    auto_dates = [field for field in table["fields"] if field["dataType"] == "autoDate"]
    assert {field["autoDate"]["role"] for field in auto_dates} == {
        "createdAt",
        "updatedAt",
    }

    valid = auto_dates[0]
    _validate(valid, schema["$defs"]["FieldDefinition"], schema)
    for invalid_auto_date in (
        {},
        {"role": "later"},
        {"role": "createdAt", "unexpected": True},
    ):
        candidate = dict(valid)
        candidate["autoDate"] = invalid_auto_date
        try:
            _validate(candidate, schema["$defs"]["FieldDefinition"], schema)
        except SchemaMismatchError:
            pass
        else:
            raise AssertionError(f"invalid autoDate config was accepted: {invalid_auto_date!r}")


def test_view_definition_is_a_typed_source_projection_without_raw_sql() -> None:
    schema = _load(SCHEMA_PATH)
    table = _load(FIXTURES / "table-definition.json")
    table["kind"] = "view"
    table["archivePolicy"] = {
        "mode": "none",
        "fieldId": None,
        "archivedValue": None,
    }
    table["view"] = {"sourceTableId": "tbl_orders"}
    table["indexes"] = []
    for field in table["fields"]:
        field["readOnly"] = True
    _validate(table, schema["$defs"]["TableDefinition"], schema)

    table["view"]["query"] = "select * from users"
    try:
        _validate(table, schema["$defs"]["TableDefinition"], schema)
    except SchemaMismatchError as exc:
        assert "unexpected property 'query'" in str(exc)  # noqa: PT017
    else:
        raise AssertionError("view contract accepted raw SQL")


def test_field_and_constraint_discriminators_are_frozen_and_covered() -> None:
    schema = _load(SCHEMA_PATH)
    declared_field_kinds = set(schema["$defs"]["FieldDefinition"]["properties"]["kind"]["enum"])
    declared_constraint_kinds = _const_kinds(schema, "FieldConstraint")
    table = _load(FIXTURES / "table-definition.json")
    covered_field_kinds = {field["kind"] for field in table["fields"]}
    covered_constraint_kinds = {
        constraint["kind"] for field in table["fields"] for constraint in field["constraints"]
    }

    assert declared_field_kinds == FIELD_KINDS == covered_field_kinds
    assert declared_constraint_kinds == CONSTRAINT_KINDS == covered_constraint_kinds


def test_mutation_and_event_required_wire_fields_are_frozen() -> None:
    schema = _load(SCHEMA_PATH)
    mutation_request = set(schema["$defs"]["MutationRequest"]["required"])
    mutation_receipt = set(schema["$defs"]["MutationReceipt"]["required"])

    assert {
        "requestId",
        "idempotencyKey",
        "tableId",
        "schemaRevision",
        "operations",
        "actor",
        "expectedRevision",
        "expectedDigest",
    } <= mutation_request
    assert {
        "status",
        "changeSetId",
        "affectedRows",
        "computedFields",
        "newRevision",
        "emittedEvents",
        "warnings",
    } <= mutation_receipt
    assert schema["$defs"]["DataChangedEvent"]["properties"]["topic"]["const"] == ("data.changed")
    assert schema["$defs"]["TaskChangedEvent"]["properties"]["topic"]["const"] == ("task.changed")


def test_wire_schema_does_not_leak_storage_provider_names() -> None:
    schema_text = SCHEMA_PATH.read_text(encoding="utf-8").lower()
    fixture_text = "\n".join(
        path.read_text(encoding="utf-8").lower() for path in FIXTURES.glob("*.json")
    )
    retired_provider = "".join(["di", "rectus"])
    forbidden = (retired_provider, "pocketbase")
    assert all(name not in schema_text for name in forbidden)
    assert all(name not in fixture_text for name in forbidden)


def test_rpc_catalog_covers_every_registered_product_method_and_event() -> None:
    from backend.application.product_data_service import PRODUCT_PARAM_MODELS
    from backend.contracts.plugin import PluginEventEnvelope
    from contracts.v1.generate_rpc_catalog import (
        _registered_models,
        _result_payload,
        _result_specs,
    )

    schema = _load(SCHEMA_PATH)
    tree = ast.parse((ROOT / "backend" / "__main__.py").read_text(encoding="utf-8"))
    registered = {
        call.args[0].value
        for call in ast.walk(tree)
        if isinstance(call, ast.Call)
        and isinstance(call.func, ast.Attribute)
        and call.func.attr == "register"
        and call.args
        and isinstance(call.args[0], ast.Constant)
        and isinstance(call.args[0].value, str)
    }
    registered.update(PRODUCT_PARAM_MODELS)

    catalog = _load(FIXTURES / "rpc-catalog.json")
    assert catalog["rpcMethods"] == sorted(registered)
    assert catalog["eventTopics"] == [
        "data.changed",
        "plugin.catalog.changed",
        "plugin.file.requested",
        "plugin.interaction.requested",
        "plugin.task.changed",
        "task.changed",
    ]
    assert [case["method"] for case in catalog["rpcCases"]] == catalog["rpcMethods"]
    models = _registered_models()
    result_specs = _result_specs(FIXTURES)
    for case in catalog["rpcCases"]:
        assert case["request"]["method"] == case["method"]
        assert case["paramsModel"] == models[case["method"]].__name__
        models[case["method"]].model_validate(case["request"]["params"])
        result_spec = result_specs[case["method"]]
        expected_result, expected_schema = _result_payload(result_spec)
        assert case["resultModel"] == result_spec.model_name
        assert case["resultSchema"] == expected_schema
        assert case["success"]["result"] == expected_result
        schema_root = case["resultSchema"] if "$defs" in case["resultSchema"] else schema
        _validate(
            case["success"]["result"],
            case["resultSchema"],
            schema_root,
            f"$.rpcCases[{case['method']}].success.result",
        )
        assert case["success"]["result"] != {
            "contractVersion": "1.0",
            "method": case["method"],
            "status": "ok",
        }
        request_id = case["request"]["id"]
        assert case["success"]["id"] == request_id
        assert case["error"]["id"] == request_id
        assert case["error"]["error"]["data"]["path"] == "params"
        assert case["error"]["error"]["data"]["method"] == case["method"]
    assert [case["topic"] for case in catalog["eventCases"]] == catalog["eventTopics"]
    for case in catalog["eventCases"]:
        event = case["event"]
        if case["topic"].startswith("plugin."):
            assert event["eventType"] == case["topic"]
            PluginEventEnvelope.model_validate(event)
        else:
            assert event["topic"] == case["topic"]
    assert catalog["eventCases"][0]["event"] == _load(FIXTURES / "data-changed-event.json")
    assert catalog["eventCases"][-1]["event"] == _load(FIXTURES / "task-changed-event.json")


def test_rpc_response_goldens_pin_high_risk_method_specific_shapes() -> None:
    """Guard the response DTOs most likely to cause cross-language breakage."""

    catalog = _load(FIXTURES / "rpc-catalog.json")
    cases = {case["method"]: case for case in catalog["rpcCases"]}

    apply_import = cases["data.applyImport"]
    assert apply_import["resultModel"] == "ApplyImportResult"
    assert set(apply_import["success"]["result"]) == {
        "collection",
        "createdCount",
        "updatedCount",
        "failedRows",
        "chunks",
        "requestIds",
    }

    for method in ("mutation.apply", "file.applyHostChange"):
        mutation = cases[method]
        assert mutation["resultModel"] == "MutationReceipt"
        assert set(mutation["success"]["result"]) == {
            "contractVersion",
            "status",
            "changeSetId",
            "affectedRows",
            "computedFields",
            "newRevision",
            "emittedEvents",
            "warnings",
        }

    table = cases["schema.getTable"]
    assert table["resultModel"] == "TableDefinition"
    assert table["resultSchema"] == {"$ref": "#/$defs/TableDefinition"}
    assert {"tableId", "schemaRevision", "fields"} <= table["success"]["result"].keys()

    plugin = cases["plugin.listCatalog"]
    assert plugin["resultModel"] == "PluginSnapshotList"
    assert isinstance(plugin["success"]["result"], list)
    assert {
        "projectKey",
        "pluginId",
        "version",
        "packageHash",
        "manifest",
    } <= plugin["success"]["result"][0].keys()
