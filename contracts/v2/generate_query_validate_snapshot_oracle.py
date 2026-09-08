"""Check historical snapshot inputs and wire after migration to the Go owner."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from pydantic import JsonValue

type JsonObject = dict[str, JsonValue]

PRODUCER_COMMIT = "2f02bfcb8afdda46fa003d6c546d2ff2a8910aae"
METHOD = "query.validateSnapshot"
OUTPUT = Path(__file__).with_name("query-validate-snapshot-python-oracle.json")
CAPTURE_CLOSED = "Historical capture is closed; replay commit 6e0dab18 at its fixed producer; query owner is now Go"


@dataclass(frozen=True)
class Case:
    name: str
    params: JsonValue
    response: JsonValue
    failure: str | None = None
    typed_boundary: str | None = None


def snapshot() -> JsonObject:
    return {
        "snapshotId": "snapshot-scripted",
        "digest": "signature-scripted-not-a-domain-issued-signature",
        "databaseId": "workspace-1",
        "table": "orders",
        "schemaRevision": "schema-1",
        "dataRevision": 7,
        "normalizedQuery": {"offset": 0, "limit": 50},
    }


def validation(reason: str | None = None) -> JsonObject:
    result: JsonObject = {
        "valid": reason is None,
        "currentDataRevision": 7,
        "currentSchemaRevision": "schema-1",
    }
    if reason is not None:
        result["reason"] = reason
    return result


def cases() -> tuple[Case, ...]:
    params: JsonObject = {"snapshot": snapshot()}
    current: JsonObject = {
        "keyword": "Cafe\u0301 中文 👩🏽‍💻",
        "filters": [{"field": "title", "operator": "contains", "value": "عربي\u200f"}],
        "sorts": [{"field": "title", "direction": "desc", "nullsLast": False}],
        "offset": 0,
        "limit": 50,
    }
    return (
        Case("valid-without-current-query", params, validation()),
        Case("valid-complete-current-query", {**params, "currentQuery": current}, validation()),
        Case(
            "query-changed",
            {**params, "currentQuery": {"offset": 0, "limit": 1}},
            validation("query_changed"),
        ),
        Case(
            "schema-changed",
            params,
            {**validation("schema_changed"), "currentSchemaRevision": "schema-2"},
        ),
        Case(
            "application-write",
            params,
            {**validation("application_write"), "currentDataRevision": 8},
        ),
        Case("empty-current-query", {**params, "currentQuery": {}}, validation()),
        Case("empty-snapshot", {"snapshot": {}}, validation("query_changed")),
        Case("empty-both-objects", {"snapshot": {}, "currentQuery": {}}, validation()),
        Case("missing-snapshot", {}, validation()),
        Case("null-snapshot", {"snapshot": None}, validation()),
        Case("array-snapshot", {"snapshot": []}, validation()),
        Case("string-snapshot", {"snapshot": "snapshot"}, validation()),
        Case("null-current-query", {**params, "currentQuery": None}, validation()),
        Case("array-current-query", {**params, "currentQuery": []}, validation()),
        Case("nonobject-params", [], validation()),
        Case("unknown-before-missing", {"extra": False}, validation(), "domain"),
        Case(
            "wrong-type-before-domain",
            {"snapshot": False, "currentQuery": []},
            validation(),
            "domain",
        ),
        Case(
            "nested-credential-before-domain",
            {"snapshot": {"nested": {"password": "test-only"}}},
            validation(),
            "domain",
        ),
        Case("public-domain-error", params, validation(), "domain"),
        Case(
            "transport-error",
            params,
            validation(),
            "transport",
            "Direct Go service has no Python HTTP transport failure; public domain errors remain expressible",
        ),
        Case(
            "empty-response",
            params,
            {},
            typed_boundary="SnapshotValidation always emits valid/currentDataRevision/currentSchemaRevision; empty response object is not representable",
        ),
        Case(
            "explicit-empty-reason",
            params,
            {**validation(), "reason": ""},
            typed_boundary="SnapshotValidation.reason omitempty removes an explicit empty string",
        ),
        Case(
            "dynamic-response",
            params,
            {**validation(), "extension": [None, False, 0, "", "中文"]},
            typed_boundary="SnapshotValidation has no extension field; dynamic JSON is not a blanket exemption",
        ),
        Case(
            "wrong-response-types",
            params,
            {"valid": "yes", "currentDataRevision": None, "currentSchemaRevision": []},
            typed_boundary="SnapshotValidation bool/int64/string members cannot encode these types",
        ),
        Case(
            "null-response",
            params,
            None,
            typed_boundary="SnapshotValidation cannot represent a null root returned by scripted HTTP",
        ),
        Case(
            "array-response",
            params,
            [],
            typed_boundary="SnapshotValidation cannot represent an array root returned by scripted HTTP",
        ),
        Case(
            "opaque-nested-snapshot",
            {"snapshot": {**snapshot(), "extension": [None, False, 0, "", "中文"]}},
            validation(),
            typed_boundary="QuerySnapshot has seven fixed fields and cannot retain arbitrary extension members in its typed value",
        ),
        Case(
            "wrong-nested-snapshot-type",
            {"snapshot": {**snapshot(), "dataRevision": "seven"}},
            validation(),
            typed_boundary="QuerySnapshot.dataRevision int64 cannot encode the string forwarded by Python",
        ),
        Case(
            "unknown-current-query-member",
            {**params, "currentQuery": {"custom": "中文"}},
            validation(),
            typed_boundary="TableQuery has a closed UnmarshalJSON and cannot express unknown currentQuery members",
        ),
        Case(
            "domain-invalid-snapshot-id",
            {"snapshot": {**snapshot(), "snapshotId": "invalid"}},
            {
                "contractVersion": "2.0",
                "code": "query.snapshot.invalid",
                "path": "snapshotId",
                "message": "query snapshot id is invalid",
                "details": {},
                "retryable": False,
            },
            "invalid-snapshot-id",
        ),
    )


async def capture_case(case: Case) -> JsonObject:
    raise RuntimeError(CAPTURE_CLOSED)


async def capture() -> JsonObject:
    raise RuntimeError(CAPTURE_CLOSED)


# These are assertions over captured historical outcomes, not a replacement
# dispatcher or a way to derive new oracle expectations from current production.
PARAM_REJECTIONS = frozenset(
    {
        "missing-snapshot",
        "null-snapshot",
        "array-snapshot",
        "string-snapshot",
        "null-current-query",
        "array-current-query",
        "unknown-before-missing",
        "wrong-type-before-domain",
        "nested-credential-before-domain",
    }
)


def historical_response(case: Case) -> JsonObject:
    response: JsonObject = {"jsonrpc": "2.0", "id": case.name}
    error: JsonObject
    if case.name in PARAM_REJECTIONS:
        error = {"code": -32602, "message": "Invalid params"}
    elif case.name == "nonobject-params":
        error = {"code": -32600, "message": "Invalid Request"}
    elif case.name in {"null-response", "array-response"}:
        error = {"code": -32603, "message": "Internal error"}
    elif case.name == "transport-error":
        error = {
            "code": -32150,
            "message": "Product data unavailable",
            "data": {
                "kind": "product_data_unavailable",
                "message": "snapshot validation unavailable",
                "code": "sidecar.unavailable",
            },
        }
    elif case.name in {"public-domain-error", "domain-invalid-snapshot-id"}:
        actual_domain = case.name == "domain-invalid-snapshot-id"
        error = {
            "code": -32150,
            "message": "Product data error",
            "data": {
                "kind": "product_data_error",
                "message": "query snapshot id is invalid"
                if actual_domain
                else "snapshot signature is invalid",
                "code": "query.snapshot.invalid" if actual_domain else "query.snapshot_invalid",
                "path": "snapshotId" if actual_domain else "snapshot",
                "details": {} if actual_domain else {"reason": "invalid_signature"},
                "retryable": False,
            },
        }
    else:
        response["result"] = case.response
        return response
    response["error"] = error
    return response


def validate_frozen_inputs() -> None:
    """Check all retained inputs, ordered attempts and public outcomes without capture."""
    frozen: JsonObject = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if not isinstance(frozen, dict) or frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen snapshot producer changed")
    if (
        frozen.get("boundary")
        != "Original Python Product DTO/adapter; scripted authority HTTP, not a domain-issued snapshot or product qualification"
    ):
        raise ValueError("Frozen snapshot capture boundary changed")
    entries = frozen.get("cases")
    inputs = cases()
    if not isinstance(entries, list) or len(entries) != len(inputs) or len(inputs) != 30:
        raise ValueError("Frozen snapshot case inventory changed")
    if set(frozen) != {"producerCommit", "boundary", "cases"}:
        raise ValueError("Frozen snapshot metadata changed")
    for entry, case in zip(entries, inputs, strict=True):
        attempts: list[JsonValue] = []
        if case.name not in PARAM_REJECTIONS and case.name != "nonobject-params":
            attempts.append(
                {
                    "method": "POST",
                    "path": "/api/vibetable/v1/query/validate-snapshot",
                    "query": None,
                    "body": case.params,
                    "expectedStatus": [200],
                }
            )
        expected: JsonObject = {
            "name": case.name,
            "request": {"jsonrpc": "2.0", "id": case.name, "method": METHOD, "params": case.params},
            "authorityFixture": {"response": case.response, "failure": case.failure},
            "authorityRequests": attempts,
            "response": historical_response(case),
            "typedGoBoundary": case.typed_boundary,
        }
        if not isinstance(entry, dict) or render(entry) != render(expected):
            raise ValueError(f"Frozen snapshot input or wire changed: {case.name}")


def render(value: JsonObject) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument(
        "--write", action="store_true", help="Historical capture closed; always rejected"
    )
    modes.add_argument(
        "--check", action="store_true", help="Validate retained inputs and wire (default)"
    )
    args = parser.parse_args()
    if args.write:
        parser.error(CAPTURE_CLOSED)
    try:
        validate_frozen_inputs()
    except (ValueError, OSError) as error:
        parser.error(str(error))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
