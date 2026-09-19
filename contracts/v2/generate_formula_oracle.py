"""Capture Formula Python wire before migration; the HTTP authority is scripted.

This freezes adapter validation and error projection, not formula-domain truth.
The three original handlers must still be present when capture/check runs.
"""

from __future__ import annotations

import argparse
import asyncio
import json
from copy import deepcopy
from dataclasses import dataclass
from pathlib import Path

import httpx

from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, JsonValue, ProductParams
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.product_errors import register_product_rpc_errors

PRODUCER_COMMIT = "2c8eb476078eafe06a31484814e17b1bc999e340"
OUTPUT = Path(__file__).with_name("formula-python-oracle.json")
BOUNDARY = "Real Python dispatcher/DTO/adapter/HTTP error projection; scripted HTTP authority, not Go domain execution"
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


async def capture_case(case: Case) -> JsonObject:
    attempts: list[JsonValue] = []

    def respond(request: httpx.Request) -> httpx.Response:
        attempts.append(
            {
                "method": request.method,
                "path": request.url.path,
                "body": json.loads(request.content),
            }
        )
        if case.transport_failure:
            raise httpx.ConnectError("scripted offline", request=request)
        return httpx.Response(case.status, json=case.body)

    secret = "a" * 64
    transport = StdlibPocketBaseTransport(
        PocketBaseConfig("http://127.0.0.1:8090", secret),
        http_transport=httpx.MockTransport(respond),
    )
    service = PocketBaseProductRpc(
        client=PocketBaseClient(transport=transport, session_secret=secret),
        transport=transport,
        session_secret=secret,
    )
    dispatcher = RpcDispatcher()
    register_product_rpc_errors()

    async def invoke(params: ProductParams) -> JsonObject:
        return await service.invoke(case.method, params)

    dispatcher.register(case.method, invoke, PRODUCT_RPC_REGISTRY[case.method])
    request: JsonObject = {
        "jsonrpc": "2.0",
        "id": case.name,
        "method": case.method,
        "params": case.params,
    }
    response = await dispatcher.dispatch(request)
    assert response is not None
    return {
        "name": case.name,
        "request": request,
        "authorityFixture": {
            "status": case.status,
            "body": case.body,
            "transportFailure": case.transport_failure,
        },
        "authorityRequests": attempts,
        "response": response,
    }


async def capture() -> JsonObject:
    return {
        "producerCommit": PRODUCER_COMMIT,
        "boundary": BOUNDARY,
        "cases": [await capture_case(case) for case in cases()],
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()
    captured = asyncio.run(capture())
    if args.write:
        OUTPUT.write_text(
            json.dumps(captured, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
    elif json.loads(OUTPUT.read_text(encoding="utf-8")) != captured:
        raise SystemExit(
            "Formula Python oracle differs; investigate, do not update expectations to pass"
        )
    print(f"Formula Python oracle: {len(cases())} cases")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
