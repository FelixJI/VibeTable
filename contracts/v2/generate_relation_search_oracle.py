"""Capture the current Python relation target search contract without running a Sidecar."""

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
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, JsonValue
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.product_errors import register_product_rpc_errors

PRODUCER_COMMIT = "8cf989c5873830d28d61067a9afb82ddb06db124"
OUTPUT = Path(__file__).with_name("relation-search-python-oracle.json")
METHODS = ("relation.searchTargets",)


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def cases() -> tuple[Case, ...]:
    method = METHODS[0]
    params: JsonObject = {"relationId": "rel-orders"}
    item: JsonObject = {
        "tableId": "orders",
        "recordId": "r1",
        "label": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي",
        "secondaryLabel": "private authority detail",
        "extra": {"enabled": False},
    }
    result: JsonObject = {
        "items": [item],
        "total": 1,
        "snapshot": {"opaque": "authority-only"},
        "extra": False,
    }
    return (
        Case("omitted-query-and-paging-defaults", method, params, result),
        Case(
            "explicit-query-and-paging",
            method,
            {**params, "query": "中文 Cafe\u0301", "offset": 2, "limit": 3},
            result,
        ),
        Case("whitespace-query-preserved", method, {**params, "query": " \t "}, result),
        Case("whitespace-relation-forwarded", method, {"relationId": " "}, result),
        Case("empty-result", method, params, {"items": [], "total": 0}),
        Case(
            "non-object-items-filtered",
            method,
            params,
            {"items": [None, False, 0, "", [], item], "total": 6},
        ),
        Case(
            "all-non-object-items-filtered",
            method,
            params,
            {"items": [None, False, 0, "", []], "total": 5},
        ),
        Case("negative-total-preserved", method, params, {**result, "total": -1}),
        Case("limit-101-forwarded", method, {**params, "limit": 101}, result),
        Case(
            "negative-offset-zero-limit-forwarded",
            method,
            {**params, "offset": -1, "limit": 0},
            result,
        ),
        Case("long-query-forwarded", method, {**params, "query": "x" * 257}, result),
        Case("missing-relation", method, {}),
        Case("empty-relation", method, {"relationId": ""}),
        Case("empty-query", method, {**params, "query": ""}),
        Case("null-query", method, {**params, "query": None}),
        Case("boolean-offset", method, {**params, "offset": False}),
        Case("string-limit", method, {**params, "limit": "50"}),
        Case("unknown-parameter", method, {**params, "collection": "orders"}),
        Case("nested-credential", method, {**params, "query": {"password": "test-only"}}),
        Case("non-object-result", method, params, []),
        Case("missing-items", method, params, {"total": 1}),
        Case("null-items", method, params, {"items": None, "total": 1}),
        Case(
            "item-missing-label",
            method,
            params,
            {"items": [{"tableId": "orders", "recordId": "r1"}], "total": 1},
        ),
        Case(
            "item-empty-record-id",
            method,
            params,
            {"items": [{**item, "recordId": ""}], "total": 1},
        ),
        Case(
            "item-non-text-label", method, params, {"items": [{**item, "label": False}], "total": 1}
        ),
        Case("missing-total", method, params, {"items": []}),
        Case("boolean-total", method, params, {"items": [], "total": False}),
        Case("string-total", method, params, {"items": [], "total": "0"}),
        Case("public-error", method, params, failure="product"),
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
        assert headers == {"X-VibeTable-Session": "oracle-only"}
        assert not self.requests, "Search must make at most one authority request"
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
                    "code": "relation.not_found",
                    "message": "Relation was not found",
                    "path": "relationId",
                    "details": {"relationId": "rel-orders"},
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
        dispatcher.register(method, partial(service.invoke, method), PRODUCT_RPC_REGISTRY[method])
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
            "Python relation search contract differs from the frozen producer; inspect the change, do not regenerate"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
