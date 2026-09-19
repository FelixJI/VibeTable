"""Check retained Formula wire after closing the Python capture.

The frozen original captured the real RpcDispatcher, Product parameter DTOs,
PocketBaseProductRpc and the registered product errors with only the loopback
HTTP end scripted. The three forwarding handlers are retired, so the capture
stays closed and only the retained inputs and public wire are validated
against the frozen original; nothing is replayed through Python or regenerated
from Go.
"""

from __future__ import annotations

import argparse
import json
from copy import deepcopy
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

PRODUCER_COMMIT = "2c8eb476078eafe06a31484814e17b1bc999e340"
OUTPUT = Path(__file__).with_name("formula-python-oracle.json")
BOUNDARY = "Real Python dispatcher/DTO/adapter/HTTP error projection; scripted HTTP authority, not Go domain execution"
CAPTURE_CLOSED = "Formula Python capture is retired; preserve the frozen producer"
METHODS = ("formula.validate", "formula.draft.validate", "formula.preview")
ROUTES = ("validate", "draft/validate", "preview")


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    body: JsonValue = None
    status: int = 200
    transport_failure: bool = False


def formula_field() -> JsonObject:
    path = Path(__file__).parents[1] / "schema-v2/fixtures/field-definition.json"
    field: JsonObject = json.loads(path.read_text(encoding="utf-8"))
    field["logicalType"] = "formula"
    storage = field["storage"]
    assert isinstance(storage, dict)
    storage["kind"] = "computed"
    value = field["value"]
    assert isinstance(value, dict)
    value["presence"] = {"mode": "computed"}
    display = field["display"]
    assert isinstance(display, dict)
    display["kind"] = "readonly"
    field["formula"] = {"language": "cel-v1", "source": "1 + 1", "resultType": "number"}
    return field


def cases() -> list[Case]:
    field = formula_field()
    requests: list[JsonObject] = [
        {"tableId": "orders", "field": field},
        {"tableId": "orders", "displaySource": "SUM({明细}.{金额})"},
        {"tableId": "orders", "field": field, "row": {}, "changedFieldIds": []},
    ]
    responses: list[JsonObject] = [
        {
            "formulas": [
                {
                    "fieldId": "fld_01JABCDE",
                    "canonicalSource": "1 + 1",
                    "astHash": "authority-fixture",
                    "dependencies": [],
                }
            ]
        },
        {
            "canonicalSource": 'relationSum(f_lines, "f_amount")',
            "resultType": "number",
            "dependencies": [],
            "relationAggregatePaths": ["f_lines.f_amount"],
        },
        {"values": {"f_01jabcde": 2, "empty": None, "large": 9007199254740993}},
    ]
    result: list[Case] = []
    for method, request, response in zip(METHODS, requests, responses, strict=True):
        variants: list[tuple[str, JsonValue]] = [
            ("success", request),
            ("unknown-param", {**request, "extra": True}),
            ("revision-is-not-a-public-param", {**request, "expectedRevision": "stale"}),
            ("missing-table", {key: value for key, value in request.items() if key != "tableId"}),
            ("null-table", {**request, "tableId": None}),
            ("empty-table", {**request, "tableId": ""}),
            ("wrong-table-type", {**request, "tableId": False}),
            ("whitespace-forwarded", {**request, "tableId": " "}),
            ("nonobject-params", []),
        ]
        if method == "formula.draft.validate":
            variants += [
                ("empty-source", {**request, "displaySource": ""}),
                ("null-source", {**request, "displaySource": None}),
                ("missing-source", {"tableId": "orders"}),
                ("syntax-forwarded", {**request, "displaySource": "1 + ("}),
            ]
        else:
            nested_unknown = deepcopy(field)
            nested_unknown["unexpected"] = True
            nested_alias = deepcopy(field)
            nested_alias["display_name"] = nested_alias.pop("displayName")
            invalid_language = deepcopy(field)
            invalid_language["formula"] = {
                "language": "other",
                "source": "1",
                "resultType": "number",
            }
            invalid_source = deepcopy(field)
            invalid_source["formula"] = {"language": "cel-v1", "source": "", "resultType": "number"}
            variants += [
                ("missing-field", {key: value for key, value in request.items() if key != "field"}),
                ("null-field", {**request, "field": None}),
                ("incomplete-field-handler-error", {**request, "field": {}}),
                ("nested-unknown-handler-error", {**request, "field": nested_unknown}),
                ("nested-language-handler-error", {**request, "field": invalid_language}),
                ("nested-empty-source-handler-error", {**request, "field": invalid_source}),
                ("nested-python-alias-forwarded", {**request, "field": nested_alias}),
                ("credential-rejected", {**request, "field": {**field, "password": "oracle-only"}}),
            ]
        if method == "formula.preview":
            variants += [
                ("missing-row", {key: value for key, value in request.items() if key != "row"}),
                ("null-row", {**request, "row": None}),
                (
                    "missing-changed",
                    {key: value for key, value in request.items() if key != "changedFieldIds"},
                ),
                ("null-changed", {**request, "changedFieldIds": None}),
                ("empty-changed-id-handler-error", {**request, "changedFieldIds": [""]}),
                ("numeric-changed-id-handler-error", {**request, "changedFieldIds": [1]}),
                (
                    "row-values-forwarded",
                    {
                        **request,
                        "row": {
                            "large": 9007199254740993,
                            "negative": -2.5,
                            "empty": None,
                            "unicode": "金额😀",
                            "date": "2026-09-19T00:00:00Z",
                        },
                    },
                ),
            ]
        result.extend(
            Case(f"{method}:{name}", method, params, response) for name, params in variants
        )
        result.extend(
            [
                Case(f"{method}:scalar-response", method, request, 7),
                Case(f"{method}:transport-failure", method, request, transport_failure=True),
                Case(
                    f"{method}:domain-error-projection",
                    method,
                    request,
                    {
                        "contractVersion": "2.0",
                        "code": "formula.dependency",
                        "path": "field.formula.source",
                        "message": "unknown field",
                        "details": {"fieldId": "missing"},
                    },
                    422,
                ),
            ]
        )
    return result


PARAM_REJECTIONS = frozenset(
    {
        "unknown-param",
        "revision-is-not-a-public-param",
        "missing-table",
        "null-table",
        "empty-table",
        "wrong-table-type",
        "empty-source",
        "null-source",
        "missing-source",
        "missing-field",
        "null-field",
        "credential-rejected",
        "missing-row",
        "null-row",
        "missing-changed",
        "null-changed",
    }
)
ENVELOPE_REJECTIONS = frozenset({"nonobject-params"})
HANDLER_REJECTIONS = frozenset(
    {
        "incomplete-field-handler-error",
        "nested-unknown-handler-error",
        "nested-language-handler-error",
        "nested-empty-source-handler-error",
        "empty-changed-id-handler-error",
        "numeric-changed-id-handler-error",
    }
)
INVALID_RESPONSES = frozenset({"scalar-response"})
# Fixed assertions over the original capture, never regenerated output.
PUBLIC_ERROR_DATA = {
    "domain-error-projection": {
        "kind": "product_data_error",
        "message": "unknown field",
        "code": "formula.dependency",
        "path": "field.formula.source",
        "details": {"fieldId": "missing"},
        "retryable": False,
    },
    "transport-failure": {
        "kind": "product_data_unavailable",
        "message": "PocketBase sidecar is unavailable",
        "code": "sidecar.unavailable",
    },
}


async def capture_case(case: Case) -> JsonObject:
    raise RuntimeError(CAPTURE_CLOSED)


async def capture() -> JsonObject:
    raise RuntimeError(CAPTURE_CLOSED)


def _authority_attempts(case: Case) -> list[JsonValue]:
    kind = case.name.split(":", 1)[1]
    if kind in PARAM_REJECTIONS or kind in ENVELOPE_REJECTIONS or kind in HANDLER_REJECTIONS:
        return []
    route = ROUTES[METHODS.index(case.method)]
    return [
        {
            "method": "POST",
            "path": f"/api/vibetable/v1/formulas/{route}",
            "body": case.params,
        }
    ]


def validate_frozen_inputs() -> None:
    """Check historical inputs and wire invariants, not current Python or Go parity."""
    frozen: JsonObject = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if not isinstance(frozen, dict) or frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen Formula producer changed")
    if frozen.get("boundary") != BOUNDARY:
        raise ValueError("Frozen Formula boundary changed")
    if set(frozen) != {"producerCommit", "boundary", "cases"}:
        raise ValueError("Frozen Formula metadata changed")
    entries = frozen.get("cases")
    inputs = cases()
    if not isinstance(entries, list) or len(entries) != len(inputs) or len(inputs) != 63:
        raise ValueError("Frozen Formula case inventory changed")
    for entry, case in zip(entries, inputs, strict=True):
        if not isinstance(entry, dict) or set(entry) != {
            "name",
            "request",
            "authorityFixture",
            "authorityRequests",
            "response",
        }:
            raise ValueError("Invalid frozen Formula entry")
        expected: JsonObject = {
            "name": case.name,
            "request": {
                "jsonrpc": "2.0",
                "id": case.name,
                "method": case.method,
                "params": case.params,
            },
            "authorityFixture": {
                "status": case.status,
                "body": case.body,
                "transportFailure": case.transport_failure,
            },
            "authorityRequests": _authority_attempts(case),
        }
        actual = {key: entry[key] for key in expected}
        if render(actual) != render(expected):
            raise ValueError(f"Frozen Formula inputs or attempts changed: {case.name}")
        response = entry["response"]
        if (
            not isinstance(response, dict)
            or response.get("jsonrpc") != "2.0"
            or response.get("id") != case.name
        ):
            raise ValueError(f"Frozen Formula response envelope changed: {case.name}")
        kind = case.name.split(":", 1)[1]
        if kind in PUBLIC_ERROR_DATA:
            error = response.get("error")
            if (
                set(response) != {"jsonrpc", "id", "error"}
                or not isinstance(error, dict)
                or set(error) != {"code", "message", "data"}
                or error.get("code") != -32150
                or error.get("message") not in {"Product data error", "Product data unavailable"}
                or error.get("data") != PUBLIC_ERROR_DATA[kind]
            ):
                raise ValueError(f"Frozen Formula public error changed: {case.name}")
            continue
        code, message = (
            (-32602, "Invalid params")
            if kind in PARAM_REJECTIONS
            else (-32600, "Invalid Request")
            if kind in ENVELOPE_REJECTIONS
            else (-32603, "Internal error")
            if kind in HANDLER_REJECTIONS or kind in INVALID_RESPONSES
            else (None, None)
        )
        if code is None:
            if set(response) != {"jsonrpc", "id", "result"} or render(response["result"]) != render(
                case.body
            ):
                raise ValueError(f"Frozen Formula result changed: {case.name}")
            continue
        if set(response) != {"jsonrpc", "id", "error"} or response.get("error") != {
            "code": code,
            "message": message,
        }:
            raise ValueError(f"Frozen Formula error envelope changed: {case.name}")


def render(value: object) -> str:
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
    print(f"Formula Python oracle: {len(cases())} retained cases")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
