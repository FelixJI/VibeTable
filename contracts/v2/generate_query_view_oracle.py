"""Capture the current Python query view contract without running a Sidecar."""

from __future__ import annotations

import argparse
import asyncio
import json
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from functools import partial
from pathlib import Path

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.contracts.product_rpc import PYTHON_PRODUCT_RPC_REGISTRY, JsonObject, JsonValue
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.product_errors import register_product_rpc_errors

PRODUCER_COMMIT = "a6840f3ad983a0e6722d623737d4de668c905660"
OUTPUT = Path(__file__).with_name("query-view-python-oracle.json")
METHODS = ("query.view",)


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def cases() -> tuple[Case, ...]:
    method = METHODS[0]
    query: JsonObject = {"offset": 0, "limit": 100}
    snapshot: JsonObject = {
        "snapshotId": "view-oracle-snapshot",
        "digest": "opaque-authority-digest",
        "databaseId": "workspace",
        "table": "orders",
        "schemaRevision": "schema_1",
        "dataRevision": 7,
        "normalizedQuery": query,
    }
    row: JsonObject = {
        "id": "r1",
        "text": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي",
        "blank": "",
        "zero": 0,
        "negativeZero": -0.0,
        "false": False,
        "null": None,
        "array": [],
        "object": {},
    }
    page: JsonObject = {
        "rows": [row],
        "offset": 0,
        "limit": 100,
        "filteredRows": 3,
        "totalRows": 4,
        "querySnapshot": snapshot,
    }
    view: JsonObject = {
        "query": query,
        "groups": [],
        "summaries": [],
        "groupOffset": 0,
        "groupLimit": 2,
    }
    params: JsonObject = {"tableId": "orders", "view": view}
    response: JsonObject = {
        "page": page,
        "groupRows": [],
        "groupOffset": 0,
        "groupLimit": 2,
        "hasMoreGroups": False,
    }
    group: JsonObject = {"key": ["中文"], "count": 2, "summaries": [0, None]}
    grouped: JsonObject = {
        **view,
        "groups": [{"field": "region", "direction": "asc", "bucket": "value"}],
        "summaries": [
            {"field": "amount", "function": "sum"},
            {"field": "amount", "function": "avg"},
        ],
    }
    parent: JsonObject = {
        "key": ["中文", "Cafe\u0301"],
        "count": 1,
        "summaries": [-0.0, None],
        "parentCount": 2,
        "parentSummaries": [0, None],
    }
    nested: JsonObject = {
        **grouped,
        "groups": [{"field": "region"}, {"field": "city"}],
    }
    return (
        Case("ungrouped-unicode-falsy", method, params, response),
        Case(
            "grouped-first-page",
            method,
            {**params, "view": grouped},
            {**response, "groupRows": [group], "hasMoreGroups": True},
        ),
        Case(
            "grouped-terminal-page",
            method,
            {**params, "view": {**grouped, "groupOffset": 2}},
            {**response, "groupRows": [{**group, "key": [None], "count": 1}], "groupOffset": 2},
        ),
        Case(
            "two-level-parent-summary",
            method,
            {**params, "view": nested},
            {**response, "groupRows": [parent]},
        ),
        Case(
            "empty-page-and-groups",
            method,
            params,
            {**response, "page": {**page, "rows": [], "filteredRows": 0, "totalRows": 0}},
        ),
        Case("empty-view-forwarded", method, {**params, "view": {}}, response),
        Case("table-non-path-forwarded", method, {**params, "tableId": "中文/表"}, response),
        Case(
            "unknown-view-members-forwarded",
            method,
            {**params, "view": {"extra": False, "query": {"limit": 0}}},
            response,
        ),
        Case(
            "paired-null-parent-preserved",
            method,
            {**params, "view": nested},
            {**response, "groupRows": [{**parent, "parentCount": None, "parentSummaries": None}]},
        ),
        Case(
            "extra-group-member-preserved",
            method,
            params,
            {**response, "groupRows": [{**group, "extra": {"enabled": False}}]},
        ),
        Case(
            "empty-snapshot-preserved",
            method,
            params,
            {**response, "page": {**page, "querySnapshot": {}}},
        ),
        Case(
            "negative-counters-preserved",
            method,
            params,
            {
                **response,
                "groupOffset": -1,
                "groupRows": [{**group, "count": -1}],
                "page": {**page, "offset": -1, "limit": 0, "filteredRows": -1},
            },
        ),
        Case("missing-view", method, {"tableId": "orders"}),
        Case("null-view", method, {**params, "view": None}),
        Case("array-view", method, {**params, "view": []}),
        Case("unknown-param", method, {**params, "extra": True}),
        Case("empty-table", method, {**params, "tableId": ""}),
        Case("null-table", method, {**params, "tableId": None}),
        Case("credential-in-view", method, {**params, "view": {"password": "test-only"}}),
        Case("missing-page", method, params, {"groupRows": [], "hasMoreGroups": False}),
        Case("malformed-row", method, params, {**response, "page": {**page, "rows": [None]}}),
        Case(
            "wrong-snapshot-type",
            method,
            params,
            {**response, "page": {**page, "querySnapshot": []}},
        ),
        Case(
            "boolean-page-count", method, params, {**response, "page": {**page, "totalRows": False}}
        ),
        Case("malformed-group-row", method, params, {**response, "groupRows": [None]}),
        Case(
            "boolean-group-count",
            method,
            params,
            {**response, "groupRows": [{**group, "count": False}]},
        ),
        Case(
            "non-array-group-summaries",
            method,
            params,
            {**response, "groupRows": [{**group, "summaries": None}]},
        ),
        Case(
            "unpaired-parent",
            method,
            params,
            {**response, "groupRows": [{**group, "parentCount": 2}]},
        ),
        Case(
            "parent-on-single-level",
            method,
            params,
            {**response, "groupRows": [{**parent, "key": ["中文"]}]},
        ),
        Case(
            "boolean-parent-count",
            method,
            params,
            {**response, "groupRows": [{**parent, "parentCount": False}]},
        ),
        Case("boolean-group-offset", method, params, {**response, "groupOffset": False}),
        Case("non-boolean-has-more", method, params, {**response, "hasMoreGroups": 1}),
        Case("product-error", method, params, failure="product"),
        Case("transport-error", method, params, failure="transport"),
    )


class RecordingTransport:
    """Only the authority HTTP boundary is scripted; Python code executes unchanged."""

    def __init__(self, case: Case) -> None:
        self.case = case
        self.requests: list[JsonObject] = []

    async def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, JsonValue] | None = None,
        json_body: JsonValue = None,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> JsonValue:
        assert not self.requests, "View must use one authority request"
        assert headers == {"X-VibeTable-Session": "oracle-only"}
        self.requests.append(
            {
                "method": method,
                "path": path,
                "query": dict(query) if query is not None else None,
                "body": json_body,
                "expectedStatus": list(expected_status),
            }
        )
        if self.case.failure == "product":
            raise PocketBaseProductError(
                status=409,
                payload={
                    "code": "query.snapshot_stale",
                    "message": "Snapshot is stale",
                    "path": "snapshot",
                    "details": {"expected": "data_0", "actual": "data_1"},
                    "retryable": False,
                },
            )
        if self.case.failure == "transport":
            raise PocketBaseTransportError("Sidecar unavailable")
        return self.case.response

    async def request_multipart(
        self,
        path: str,
        *,
        json_body: Mapping[str, JsonValue],
        uploads: Sequence[tuple[str, str]],
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
    ) -> JsonValue:
        raise AssertionError("Read oracle must not upload files")

    async def download_to_file(
        self,
        path: str,
        *,
        query: Mapping[str, JsonValue],
        target_path: str,
        headers: Mapping[str, str] | None = None,
        expected_status: Sequence[int] = (200,),
        maximum_bytes: int = 2 * 1024 * 1024 * 1024,
    ) -> int:
        raise AssertionError("Read oracle must not download files")


async def capture_case(case: Case) -> JsonObject:
    transport = RecordingTransport(case)
    service = PocketBaseProductRpc(
        client=PocketBaseClient(transport=transport, session_secret="oracle-only"),
        transport=transport,
        session_secret="oracle-only",
    )
    dispatcher = RpcDispatcher()
    register_product_rpc_errors()
    for method in METHODS:
        dispatcher.register(
            method, partial(service.invoke, method), PYTHON_PRODUCT_RPC_REGISTRY[method]
        )
    request: JsonObject = {
        "jsonrpc": "2.0",
        "id": case.name,
        "method": case.method,
        "params": case.params,
    }
    response = await dispatcher.dispatch(request)
    return {
        "name": case.name,
        "request": request,
        "authorityFixture": {"response": case.response, "failure": case.failure},
        "authorityRequests": list(transport.requests),
        "response": response,
    }


async def capture() -> JsonObject:
    return {
        "producerCommit": PRODUCER_COMMIT,
        "boundary": "Python Product dispatcher + adapter; scripted authority transport",
        "cases": [await capture_case(case) for case in cases()],
    }


def render(value: JsonObject) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--write", action="store_true", help="Create the oracle once; never overwrite"
    )
    parser.add_argument(
        "--check", action="store_true", help="Compare without changing the frozen oracle (default)"
    )
    args = parser.parse_args()
    if args.write and args.check:
        parser.error("Choose either --write or --check")
    generated = render(asyncio.run(capture()))
    if args.write:
        with OUTPUT.open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(generated)
        return 0
    if OUTPUT.read_text(encoding="utf-8") != generated:
        parser.error(
            "Python query view contract differs from the frozen producer; inspect the change, do not regenerate"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
