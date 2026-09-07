"""Capture the current Python query read contract without running a Sidecar."""

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

PRODUCER_COMMIT = "b55f878641bf74c0b49b04222a217c48abf544a7"
OUTPUT = Path(__file__).with_name("query-read-python-oracle.json")
METHODS = ("query.readRows", "query.validateSnapshot")


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def cases() -> tuple[Case, ...]:
    read, validate = METHODS
    ids: JsonObject = {"tableId": "orders", "rowIds": ["r1"]}
    snapshot: JsonObject = {"snapshot": {"schemaRevision": "schema_1", "dataRevision": "data_0"}}
    return (
        Case(
            "read-unicode-falsy",
            read,
            ids,
            {
                "rows": [
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
            },
        ),
        Case("read-empty-ids", read, {**ids, "rowIds": []}, {"rows": []}),
        Case(
            "read-duplicate-whitespace-ids",
            read,
            {**ids, "rowIds": ["r1", "r1", " "]},
            {"rows": []},
        ),
        Case("read-table-not-path-segment", read, {**ids, "tableId": "中文/表"}, {"rows": []}),
        Case("read-missing-ids", read, {"tableId": "orders"}),
        Case("read-ids-wrong-type", read, {**ids, "rowIds": "r1"}),
        Case("read-empty-id", read, {**ids, "rowIds": [""]}),
        Case("read-zero-id", read, {**ids, "rowIds": [0]}),
        Case("read-empty-table", read, {**ids, "tableId": ""}),
        Case("read-unknown-field", read, {**ids, "limit": 0}),
        Case("read-malformed-rows", read, ids, {"rows": [None]}),
        Case("read-product-error", read, ids, failure="product"),
        Case("read-transport-error", read, ids, failure="transport"),
        Case(
            "validate-query-unicode-falsy",
            validate,
            {
                **snapshot,
                "currentQuery": {
                    "filters": [{"value": "中文\u200fعربي"}],
                    "sorts": [],
                    "offset": 0,
                    "limit": 0,
                    "blank": "",
                    "enabled": False,
                },
            },
            {"valid": True, "reason": "", "revision": 0},
        ),
        Case(
            "validate-empty-snapshot",
            validate,
            {"snapshot": {}},
            {"valid": False, "reason": "snapshot_stale"},
        ),
        Case(
            "validate-empty-current-query",
            validate,
            {**snapshot, "currentQuery": {}},
            {"valid": True},
        ),
        Case("validate-missing-snapshot", validate, {}),
        Case("validate-null-snapshot", validate, {"snapshot": None}),
        Case("validate-null-current-query", validate, {**snapshot, "currentQuery": None}),
        Case("validate-unknown-field", validate, {**snapshot, "tableId": "orders"}),
        Case("validate-malformed-response", validate, snapshot, []),
        Case("validate-product-error", validate, snapshot, failure="product"),
        Case("validate-transport-error", validate, snapshot, failure="transport"),
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
            "Python query read contract differs from the frozen producer; inspect the change, do not regenerate"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
