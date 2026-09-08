"""Check retained mutation inputs and wire after closing historical Python capture."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "38098da214a0fb33bb6df1fd0707b1ba0b4ac754"
METHODS = ("mutation.preview", "mutation.apply")
OUTPUT = Path(__file__).with_name("mutation-product-python-oracle.json")
CAPTURE_ROOT = Path(__file__).resolve().parents[2]
BOUNDARY = "Fixed Python dispatcher and adapter; scripted authority HTTP, not mutation domain or product qualification"
CAPTURE_CLOSED = "Python mutation capture is retired; preserve the frozen producer"
PARAM_REJECTIONS = frozenset(
    {
        "missing-required",
        "null-operations",
        "numeric-revision",
        "empty-digest",
        "unknown-param",
        "nested-credential",
    }
)


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue
    failure: str | None = None


def mutation() -> JsonObject:
    return {
        "contractVersion": "2.0",
        "requestId": "request-oracle",
        "idempotencyKey": "operation-oracle",
        "tableId": "orders",
        "schemaRevision": "schema_1",
        "actor": {"id": "local-user", "kind": "user"},
        "operations": [
            {
                "kind": "update",
                "recordId": "abcdefghijklmno",
                "values": {
                    "title": "Cafe\u0301 中文 👩🏽‍💻",
                    "amount": 1.25,
                    "count": 9007199254740991,
                    "empty": "",
                    "absent": None,
                    "enabled": False,
                    "tags": [],
                },
            }
        ],
    }


def cases() -> tuple[Case, ...]:
    entries: list[Case] = []
    for method in METHODS:
        params = mutation()
        # Scripted HTTP fixtures establish Python wire, never Go domain acceptance.
        response: JsonObject = {
            "contractVersion": "2.0",
            "status": "scripted",
            "extension": [None, False, 0, 1.25, "", "中文"],
        }
        variants: list[tuple[str, JsonValue, JsonValue, str | None]] = [
            ("omitted-optionals", params, response, None),
            (
                "null-optionals",
                {**params, "expectedRevision": None, "expectedDigest": None},
                response,
                None,
            ),
            (
                "explicit-optionals",
                {**params, "expectedRevision": "revision-1", "expectedDigest": "scripted-digest"},
                response,
                None,
            ),
            ("empty-operations-actor", {**params, "operations": [], "actor": {}}, {}, None),
            (
                "missing-required",
                {key: value for key, value in params.items() if key != "operations"},
                response,
                "domain",
            ),
            ("null-operations", {**params, "operations": None}, response, "domain"),
            ("numeric-revision", {**params, "expectedRevision": 1}, response, "domain"),
            ("empty-digest", {**params, "expectedDigest": ""}, response, "domain"),
            ("unknown-param", {**params, "extra": False}, response, "domain"),
            (
                "nested-credential",
                {**params, "actor": {"password": "oracle-only"}},
                response,
                "domain",
            ),
            ("nonobject-params", [], response, "domain"),
            ("public-domain-error", params, response, "domain"),
            ("transport-error", params, response, "transport"),
            ("null-response", params, None, None),
            ("array-response", params, [], None),
        ]
        entries.extend(
            Case(f"{method}:{name}", method, payload, reply, failure)
            for name, payload, reply, failure in variants
        )
    # Fully typed shapes follow mutation/types.go and kernel.go. They remain
    # scripted HTTP responses: neither schema lookup nor a domain write ran.
    field: JsonValue = json.loads(
        (CAPTURE_ROOT / "contracts/schema-v2/fixtures/field-definition.json").read_text(
            encoding="utf-8"
        )
    )
    values: JsonObject = {"f_01jabcde": 12.5}
    typed_request: JsonObject = {
        "contractVersion": "2.0",
        "requestId": "request-typed",
        "idempotencyKey": "operation-typed",
        "tableId": "orders",
        "schemaRevision": "schema_1",
        "expectedRevision": None,
        "expectedDigest": None,
        "actor": {"type": "user", "id": "local-user", "displayName": "本地用户"},
        "operations": [
            {
                "kind": "update",
                "recordId": "abcdefghijklmno",
                "values": values,
                "expectedRevision": "row_0003",
                "expectedDigest": None,
            }
        ],
    }
    archive: JsonObject = {"mode": "none", "fieldId": None, "archivedValue": None}
    preview: JsonObject = {
        "Definition": {
            "Snapshot": {
                "contract": "vibetable.schema.v2",
                "tableId": "orders",
                "displayName": "订单",
                "kind": "base",
                "schemaRevision": "schema_1",
                "dataRevision": 7,
                "archivePolicy": archive,
                "fields": [field],
                "capabilities": [],
            },
            "PhysicalName": "orders",
            "Kind": "base",
            "ViewSourceTableID": "",
            "PrimaryDisplayFieldID": "fld_01JABCDE",
            "ArchivePolicy": archive,
            "FormulaRuntime": {},
        },
        "Operations": [
            {
                "Kind": "update",
                "RecordID": "abcdefghijklmno",
                "Values": values,
                "ExpectedRevision": "row_0003",
                "ExpectedDigest": None,
                "Attachment": None,
            }
        ],
    }
    receipt: JsonObject = {
        "contractVersion": "2.0",
        "status": "applied",
        "changeSetId": "chg_01HZX",
        "affectedRows": [
            {
                "recordId": "abcdefghijklmno",
                "operation": "update",
                "revision": "row_0004",
                "digest": "sha256:78dbae",
            }
        ],
        "computedFields": {},
        "newRevision": "data_0008",
        "emittedEvents": ["evt_data_0008"],
        "warnings": [],
    }
    entries.extend(
        (
            Case("mutation.preview:typed-complete", "mutation.preview", typed_request, preview),
            Case("mutation.apply:typed-complete", "mutation.apply", typed_request, receipt),
        )
    )
    return tuple(entries)


async def capture_case(case: Case) -> JsonObject:
    raise RuntimeError(CAPTURE_CLOSED)


async def capture() -> JsonObject:
    raise RuntimeError(CAPTURE_CLOSED)


def validate_frozen_inputs() -> None:
    """Check historical inputs and wire invariants, not current Python or Go parity."""
    frozen: JsonObject = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if not isinstance(frozen, dict) or frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen mutation producer changed")
    if frozen.get("boundary") != BOUNDARY:
        raise ValueError("Frozen mutation boundary changed")
    if set(frozen) != {"producerCommit", "boundary", "cases"}:
        raise ValueError("Frozen mutation metadata changed")
    entries = frozen.get("cases")
    inputs = cases()
    if not isinstance(entries, list) or len(entries) != len(inputs) or len(inputs) != 32:
        raise ValueError("Frozen mutation case inventory changed")
    for entry, case in zip(entries, inputs, strict=True):
        if not isinstance(entry, dict) or set(entry) != {
            "name",
            "request",
            "authorityFixture",
            "authorityRequests",
            "response",
        }:
            raise ValueError("Invalid frozen mutation entry")
        variant = case.name.split(":", 1)[1]
        attempts: list[JsonValue] = []
        if variant not in PARAM_REJECTIONS and variant != "nonobject-params":
            attempts.append(
                {
                    "method": "POST",
                    "path": "/api/vibetable/v1/mutations/" + case.method.split(".")[1],
                    "query": None,
                    "body": case.params,
                    "expectedStatus": [200],
                }
            )
        expected: JsonObject = {
            "name": case.name,
            "request": {
                "jsonrpc": "2.0",
                "id": case.name,
                "method": case.method,
                "params": case.params,
            },
            "authorityFixture": {"response": case.response, "failure": case.failure},
            "authorityRequests": attempts,
        }
        actual = {key: entry[key] for key in expected}
        if render(actual) != render(expected):
            raise ValueError(f"Frozen mutation inputs or attempts changed: {case.name}")
        response = entry["response"]
        if (
            not isinstance(response, dict)
            or response.get("jsonrpc") != "2.0"
            or response.get("id") != case.name
        ):
            raise ValueError(f"Frozen mutation response envelope changed: {case.name}")
        # Assert retained outcomes without generating replacement expectations.
        error_case = variant in PARAM_REJECTIONS or variant in {
            "nonobject-params",
            "null-response",
            "array-response",
            "public-domain-error",
            "transport-error",
        }
        if not error_case:
            if set(response) != {"jsonrpc", "id", "result"} or render(response["result"]) != render(
                case.response
            ):
                raise ValueError(f"Frozen mutation result changed: {case.name}")
            continue
        error = response.get("error")
        if set(response) != {"jsonrpc", "id", "error"} or not isinstance(error, dict):
            raise ValueError(f"Frozen mutation error envelope changed: {case.name}")
        code, message = (
            (-32602, "Invalid params")
            if variant in PARAM_REJECTIONS
            else (-32600, "Invalid Request")
            if variant == "nonobject-params"
            else (-32603, "Internal error")
            if variant in {"null-response", "array-response"}
            else (-32150, "Product data error")
            if variant == "public-domain-error"
            else (-32150, "Product data unavailable")
        )
        keys = {"code", "message", "data"} if code == -32150 else {"code", "message"}
        if set(error) != keys or error.get("code") != code or error.get("message") != message:
            raise ValueError(f"Frozen mutation public error changed: {case.name}")
        if code == -32150:
            # Fixed assertions over the original capture, never regenerated output.
            expected_data: JsonObject = (
                {
                    "kind": "product_data_error",
                    "message": "mutation revision is stale",
                    "code": "mutation.revision_conflict",
                    "path": "expectedRevision",
                    "details": {"reason": "stale_revision"},
                    "retryable": False,
                }
                if variant == "public-domain-error"
                else {
                    "kind": "product_data_unavailable",
                    "message": "mutation unavailable",
                    "code": "sidecar.unavailable",
                }
            )
            if render(error.get("data")) != render(expected_data):
                raise ValueError(f"Frozen mutation public error data changed: {case.name}")


def render(value: JsonValue) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--write", action="store_true", help="Retired; always rejected")
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
