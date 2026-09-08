"""Capture the fixed Python mutation Product wire using scripted authority HTTP."""

from __future__ import annotations

import argparse
import asyncio
import inspect
import json
import subprocess
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from functools import partial
from pathlib import Path

from backend.adapters.pocketbase.client import PocketBaseClient, PocketBaseProductError
from backend.adapters.pocketbase.product_query_schema_rpc import ProductQuerySchemaRpc
from backend.adapters.pocketbase.product_relation_lookup_file_rpc import (
    ProductRelationLookupFileRpc,
)
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.adapters.pocketbase.product_rpc_support import _object, _result_object
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.contracts.generated_product_rpc_capabilities import current_owner_methods
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, JsonValue
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.error_registry import RpcErrorRegistry
from backend.rpc.messages import RpcRequest
from backend.rpc.product_errors import register_product_rpc_errors

PRODUCER_COMMIT = "38098da214a0fb33bb6df1fd0707b1ba0b4ac754"
METHODS = ("mutation.preview", "mutation.apply")
OUTPUT = Path(__file__).with_name("mutation-product-python-oracle.json")
CAPTURE_ROOT = Path(__file__).resolve().parents[2]


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


class RecordingTransport:
    """Record every attempt and keep protocol failures outside the public RPC envelope."""

    def __init__(self, case: Case) -> None:
        self.case = case
        self.requests: list[JsonObject] = []
        self.violations: list[str] = []

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
        self.requests.append(
            {
                "method": method,
                "path": path,
                "query": dict(query) if query is not None else None,
                "body": json_body,
                "expectedStatus": list(expected_status),
            }
        )
        params = self.case.params
        try:
            assert isinstance(params, dict)
            assert len(self.requests) == 1
            assert method == "POST"
            assert path == "/api/vibetable/v1/mutations/" + self.case.method.split(".")[1]
            assert query is None
            assert headers == {"X-VibeTable-Session": "oracle-only"}
            assert tuple(expected_status) == (200,)
            assert json_body == params
        except AssertionError:
            self.violations.append(f"attempt {len(self.requests)}: {method} {path}")
            raise
        if self.case.failure == "domain":
            raise PocketBaseProductError(
                status=409,
                payload={
                    "contract": "vibetable.schema.v2",
                    "code": "mutation.revision_conflict",
                    "path": "expectedRevision",
                    "message": "mutation revision is stale",
                    "details": {"reason": "stale_revision"},
                    "retryable": False,
                    "occurredAt": "2026-09-08T00:00:00Z",
                },
            )
        if self.case.failure == "transport":
            raise PocketBaseTransportError("mutation unavailable")
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
        self.requests.append({"method": "MULTIPART", "path": path})
        self.violations.append("mutation must not upload")
        raise AssertionError(self.violations[-1])

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
        self.requests.append({"method": "DOWNLOAD", "path": path})
        self.violations.append("mutation must not download")
        raise AssertionError(self.violations[-1])


def require_producer_source() -> None:
    for symbol in (
        PocketBaseProductRpc,
        ProductQuerySchemaRpc,
        ProductRelationLookupFileRpc,
        PocketBaseClient,
        PocketBaseProductError,
        PocketBaseTransportError,
        *(PRODUCT_RPC_REGISTRY[method] for method in METHODS),
        RpcDispatcher,
        RpcRequest,
        RpcErrorRegistry,
        register_product_rpc_errors,
        _object,
        _result_object,
        current_owner_methods,
    ):
        module = inspect.getmodule(symbol)
        if module is None:
            raise RuntimeError("Cannot locate fixed producer module")
        source = Path(inspect.getfile(module)).resolve()
        if not source.is_relative_to(CAPTURE_ROOT / "backend"):
            raise RuntimeError(
                "Capture requires this checkout's backend; set PYTHONPATH to its root"
            )
    try:
        difference = subprocess.run(
            [
                "git",
                "-C",
                str(CAPTURE_ROOT),
                "diff",
                "--quiet",
                "--no-ext-diff",
                "--no-textconv",
                PRODUCER_COMMIT,
                "--",
                "backend",
            ],
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise RuntimeError("Cannot verify fixed producer source") from error
    if difference.returncode != 0:
        raise RuntimeError("Capture source differs from fixed producer or Git verification failed")


async def capture_case(case: Case) -> JsonObject:
    require_producer_source()
    transport = RecordingTransport(case)
    service = PocketBaseProductRpc(
        client=PocketBaseClient(transport=transport, session_secret="oracle-only"),
        transport=transport,
        session_secret="oracle-only",
    )
    dispatcher = RpcDispatcher()
    register_product_rpc_errors()
    dispatcher.register(
        case.method, partial(service.invoke, case.method), PRODUCT_RPC_REGISTRY[case.method]
    )
    request: JsonObject = {
        "jsonrpc": "2.0",
        "id": case.name,
        "method": case.method,
        "params": case.params,
    }
    response = await dispatcher.dispatch(request)
    if transport.violations:
        raise RuntimeError(
            "Capture authority protocol violation: " + "; ".join(transport.violations)
        )
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
        "boundary": "Fixed Python dispatcher and adapter; scripted authority HTTP, not mutation domain or product qualification",
        "cases": [await capture_case(case) for case in cases()],
    }


def render(value: JsonObject) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--write", action="store_true", help="Create once; never replace original")
    modes.add_argument("--check", action="store_true", help="Compare full capture (default)")
    args = parser.parse_args()
    if args.write:
        result = render(asyncio.run(capture()))
        with OUTPUT.open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(result)
    elif OUTPUT.read_text(encoding="utf-8") != render(asyncio.run(capture())):
        parser.error("Original mutation differs; inspect, do not regenerate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
