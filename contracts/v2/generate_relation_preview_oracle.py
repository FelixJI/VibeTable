"""Capture original Python relation preview through its real dispatcher and adapter."""

from __future__ import annotations

import argparse
import asyncio
import inspect
import json
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from functools import partial
from pathlib import Path

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
from backend.adapters.pocketbase.product_relation_lookup_file_rpc import (
    ProductRelationLookupFileRpc,
)
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, JsonValue
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.product_errors import register_product_rpc_errors

PRODUCER_COMMIT = "6ed36810f3753caed5e2e8ca27a4d4ad2117d41d"
OUTPUT = Path(__file__).with_name("relation-preview-python-oracle.json")
METHODS = ("relation.previewDelta",)
CAPTURE_ROOT = Path(__file__).resolve().parents[2]

# These describe authority wire shapes, not additional domain success cases.
TYPED_GO_BOUNDARIES: JsonObject = {
    "non-object-result": "DeltaPreview is a struct, not an array.",
    "non-object-current-members-filtered": "[]TargetRef cannot contain non-object members.",
    "non-array-current": "Current is []TargetRef, not an object.",
    "current-nontext-label": "TargetRef.Label is string, not bool.",
    "secondary-label-falsy-becomes-null": "TargetRef.SecondaryLabel is string, not arbitrary JSON.",
    "secondary-label-truthy-nontext-preserved": "TargetRef.SecondaryLabel cannot carry truthy non-text JSON.",
    "can-apply-missing-is-false": "DeltaPreview always emits CanApply.",
    "can-apply-one-is-false": "CanApply is bool, not integer.",
    "can-apply-string-is-false": "CanApply is bool, not string.",
    "can-apply-null-is-false": "CanApply is bool, not null.",
    "transport-error": "An in-process PreviewDelta call has no Python HTTP transport boundary.",
}


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    response: JsonValue = None
    failure: str | None = None


def base_params() -> JsonObject:
    return {
        "relationId": "orders.related",
        "sourceItemId": "source-1",
        "expectedSchemaRevision": "schema-1",
        "adds": [],
        "removes": [],
        "idempotencyKey": "preview-1",
    }


def cases() -> tuple[Case, ...]:
    method = METHODS[0]
    params = base_params()
    target: JsonObject = {
        "tableId": "authors",
        "recordId": "author-1",
        "label": "中文 Cafe\u0301 👩🏽‍💻 \u200fعربي",
        "secondaryLabel": "副标题",
    }
    result: JsonObject = {
        "current": [target],
        "result": [],
        "adds": 99,
        "removes": 99,
        "diagnostics": ["authority-only"],
        "canApply": True,
    }
    falsy_labels: tuple[JsonValue, ...] = (None, "", False, 0, [], {})
    truthy_labels: tuple[JsonValue, ...] = (True, 1, ["detail"], {"detail": False})
    return (
        Case("empty-delta-hydrates-current", method, params, result),
        Case("empty-current", method, params, {"current": [], "canApply": True}),
        Case(
            "nested-target-translation-and-original-echo",
            method,
            {
                **params,
                "adds": [
                    {
                        "target": {"collection": "authors", "itemId": "a2", "label": "新增 👩🏽‍💻"},
                        "note": [0, False],
                    }
                ],
                "removes": [{"collection": "authors", "itemId": "author-1"}],
            },
            result,
        ),
        Case(
            "target-alias-priority-and-fallback",
            method,
            {
                **params,
                "adds": [
                    {
                        "tableId": "preferred",
                        "collection": "ignored",
                        "recordId": "preferred-id",
                        "itemId": "ignored",
                        "label": "",
                    },
                    {
                        "tableId": False,
                        "collection": "authors",
                        "recordId": "",
                        "itemId": "fallback-id",
                    },
                ],
            },
            result,
        ),
        Case(
            "non-object-nested-target-uses-outer",
            method,
            {**params, "adds": [{"target": False, "collection": "authors", "itemId": "a2"}]},
            result,
        ),
        Case(
            "empty-nested-object-does-not-fall-back",
            method,
            {**params, "adds": [{"target": {}, "collection": "authors", "itemId": "a2"}]},
        ),
        Case(
            "whitespace-identifiers-and-label-preserved",
            method,
            {
                **params,
                "relationId": " ",
                "sourceItemId": "\t",
                "adds": [{"collection": " ", "itemId": " ", "label": "\t"}],
            },
            result,
        ),
        Case(
            "expected-date-null-echoed-not-forwarded",
            method,
            {**params, "expectedDateUpdated": None},
            result,
        ),
        Case(
            "expected-date-object-echoed-not-forwarded",
            method,
            {**params, "expectedDateUpdated": {"nested": [False, 0, "中文"]}},
            result,
        ),
        Case(
            "secondary-label-falsy-becomes-null",
            method,
            params,
            {
                "current": [{**target, "secondaryLabel": value} for value in falsy_labels],
                "canApply": True,
            },
        ),
        Case(
            "secondary-label-truthy-nontext-preserved",
            method,
            params,
            {
                "current": [{**target, "secondaryLabel": value} for value in truthy_labels],
                "canApply": True,
            },
        ),
        Case(
            "non-object-current-members-filtered",
            method,
            params,
            {"current": [None, False, 0, "", [], target], "canApply": True},
        ),
        Case("can-apply-missing-is-false", method, params, {"current": []}),
        Case("can-apply-false", method, params, {"current": [], "canApply": False}),
        Case("can-apply-one-is-false", method, params, {"current": [], "canApply": 1}),
        Case("can-apply-string-is-false", method, params, {"current": [], "canApply": "true"}),
        Case("can-apply-null-is-false", method, params, {"current": [], "canApply": None}),
        Case("non-object-result", method, params, []),
        Case("missing-current", method, params, {"canApply": True}),
        Case("null-current", method, params, {"current": None, "canApply": True}),
        Case("non-array-current", method, params, {"current": {}, "canApply": True}),
        Case(
            "current-missing-label",
            method,
            params,
            {"current": [{"tableId": "authors", "recordId": "a1"}], "canApply": True},
        ),
        Case(
            "current-empty-record-id",
            method,
            params,
            {"current": [{**target, "recordId": ""}], "canApply": True},
        ),
        Case(
            "current-nontext-label",
            method,
            params,
            {"current": [{**target, "label": False}], "canApply": True},
        ),
        Case(
            "missing-required-adds",
            method,
            {key: value for key, value in params.items() if key != "adds"},
        ),
        Case("unknown-top-level-field", method, {**params, "extra": True}),
        Case("empty-relation-is-handler-error", method, {**params, "relationId": ""}),
        Case("wrong-relation-type-is-handler-error", method, {**params, "relationId": False}),
        Case("null-adds-is-handler-error", method, {**params, "adds": None}),
        Case("non-object-add-is-handler-error", method, {**params, "adds": [False]}),
        Case(
            "null-target-label-is-handler-error",
            method,
            {**params, "adds": [{"collection": "authors", "itemId": "a2", "label": None}]},
        ),
        Case("top-level-authority-alias-rejected", method, {**params, "sourceRecordId": "other"}),
        Case(
            "nested-credential-rejected-before-handler",
            method,
            {**params, "expectedDateUpdated": {"password": "oracle-only"}},
        ),
        Case("public-error", method, params, failure="product"),
        Case("transport-error", method, params, failure="transport"),
        Case(
            "duplicate-adds-forwarded-to-authority",
            method,
            {
                **params,
                "adds": [
                    {"collection": "authors", "itemId": "a2"},
                    {"collection": "authors", "itemId": "a2"},
                ],
            },
            result,
        ),
        Case("non-object-params", method, []),
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
        assert method == "POST"
        assert path == "/api/vibetable/v1/relations/preview-delta"
        assert query is None
        assert tuple(expected_status) == (200,)
        assert not self.requests, "Preview must make at most one authority request"
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
                    "code": "relation.target_not_linked",
                    "message": "remove target is not linked",
                    "path": "removes[0]",
                    "details": {"recordId": "author-1"},
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
    for symbol in (
        PocketBaseProductRpc,
        ProductRelationLookupFileRpc,
        RpcDispatcher,
        PRODUCT_RPC_REGISTRY[METHODS[0]],
    ):
        if not Path(inspect.getfile(symbol)).resolve().is_relative_to(CAPTURE_ROOT / "backend"):
            raise RuntimeError(
                "Capture must use this checkout's backend; run from its root with "
                "python -m contracts.v2.generate_relation_preview_oracle"
            )
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
        "typedGoBoundaries": TYPED_GO_BOUNDARIES,
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
            "Python relation preview contract differs from the frozen producer; inspect the change, do not regenerate"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
