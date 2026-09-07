"""Capture the current Python query window contract without running a Sidecar."""

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

PRODUCER_COMMIT = "c97c83336e4aa1bdf993fc46a7de57040219fb03"
OUTPUT = Path(__file__).with_name("query-window-python-oracle.json")
METHODS = ("query.page", "query.cursorOpen", "query.cursorFetch")


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def cases() -> tuple[Case, ...]:
    page, open_cursor, fetch = METHODS
    params: JsonObject = {"tableId": "中文/表", "query": {"offset": 0, "limit": 0}}
    snapshot: JsonObject = {"schemaRevision": "schema_1", "dataRevision": 0}
    rows: list[JsonValue] = [
        {
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
    ]
    page_result: JsonObject = {
        "rows": rows,
        "offset": 3,
        "limit": 5,
        "filteredRows": 1,
        "totalRows": 7,
        "querySnapshot": snapshot,
        "ignored": "not projected",
    }
    window: JsonObject = {
        "rows": rows,
        "nextCursor": "游标 /+",
        "hasMore": True,
        "filteredRows": 1,
        "totalRows": 7,
        "querySnapshot": snapshot,
        "ignored": "not projected",
    }
    terminal: JsonObject = {**window, "rows": [], "nextCursor": None, "hasMore": False}
    cursor: JsonObject = {"cursor": "游标 /+"}
    return (
        Case("page-unicode-falsy-offset-limit", page, params, page_result),
        Case(
            "page-empty-query",
            page,
            {**params, "query": {}},
            {**page_result, "rows": [], "offset": 0, "limit": 0},
        ),
        Case(
            "page-nested-query-forwarded",
            page,
            {
                **params,
                "query": {"filters": [{"value": False}], "sorts": [], "offset": -1, "limit": 0},
            },
            page_result,
        ),
        Case("page-missing-query", page, {"tableId": "orders"}),
        Case("page-null-query", page, {**params, "query": None}),
        Case("page-unknown-field", page, {**params, "extra": 0}),
        Case("page-malformed-rows", page, params, {**page_result, "rows": [None]}),
        Case("page-malformed-offset", page, params, {**page_result, "offset": False}),
        Case("page-product-error", page, params, failure="product"),
        Case("page-transport-error", page, params, failure="transport"),
        Case("open-unicode-falsy", open_cursor, params, window),
        Case("open-terminal-window", open_cursor, params, terminal),
        Case("open-empty-table", open_cursor, {**params, "tableId": ""}),
        Case("open-unknown-field", open_cursor, {**params, "cursor": "x"}),
        Case("open-malformed-snapshot", open_cursor, params, {**window, "querySnapshot": None}),
        Case("open-inconsistent-null-cursor", open_cursor, params, {**window, "nextCursor": None}),
        Case("open-product-error", open_cursor, params, failure="product"),
        Case("open-transport-error", open_cursor, params, failure="transport"),
        Case("fetch-unicode-cursor", fetch, cursor, window),
        Case("fetch-terminal-window", fetch, cursor, terminal),
        Case("fetch-empty-next-cursor", fetch, cursor, {**window, "nextCursor": ""}),
        Case("fetch-empty-cursor", fetch, {"cursor": ""}),
        Case("fetch-null-cursor", fetch, {"cursor": None}),
        Case("fetch-unknown-field", fetch, {**cursor, "limit": 1}),
        Case("fetch-malformed-next-cursor", fetch, cursor, {**window, "nextCursor": 0}),
        Case("fetch-product-error", fetch, cursor, failure="product"),
        Case("fetch-transport-error", fetch, cursor, failure="transport"),
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
            "Python query window contract differs from the frozen producer; inspect the change, do not regenerate"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
