"""Freeze the original query.validateSnapshot execution and public wire."""

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

PRODUCER_COMMIT = "2f02bfcb8afdda46fa003d6c546d2ff2a8910aae"
METHOD = "query.validateSnapshot"
OUTPUT = Path(__file__).with_name("query-validate-snapshot-python-oracle.json")
CAPTURE_ROOT = Path(__file__).resolve().parents[2]


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
            assert path == "/api/vibetable/v1/query/validate-snapshot"
            assert query is None
            assert headers == {"X-VibeTable-Session": "oracle-only"}
            assert tuple(expected_status) == (200,)
            assert json_body == params
        except AssertionError:
            self.violations.append(f"attempt {len(self.requests)}: {method} {path}")
            raise
        if self.case.failure == "invalid-snapshot-id":
            # query_routes.writeQueryError returns 422; ProductError.MarshalJSON
            # supplies contractVersion, empty details and retryable=false.
            assert isinstance(self.case.response, dict)
            raise PocketBaseProductError(status=422, payload=self.case.response)
        if self.case.failure == "domain":
            raise PocketBaseProductError(
                status=409,
                payload={
                    "contract": "vibetable.schema.v2",
                    "code": "query.snapshot_invalid",
                    "path": "snapshot",
                    "message": "snapshot signature is invalid",
                    "details": {"reason": "invalid_signature"},
                    "retryable": False,
                    "occurredAt": "2026-09-08T00:00:00Z",
                },
            )
        if self.case.failure == "transport":
            raise PocketBaseTransportError("snapshot validation unavailable")
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
        self.violations.append("snapshot validation must not upload")
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
        self.violations.append("snapshot validation must not download")
        raise AssertionError(self.violations[-1])


def require_producer_source() -> None:
    paths: set[str] = set()
    for symbol in (
        PocketBaseProductRpc,
        ProductQuerySchemaRpc,
        ProductRelationLookupFileRpc,
        PocketBaseClient,
        PocketBaseProductError,
        PocketBaseTransportError,
        PRODUCT_RPC_REGISTRY[METHOD],
        RpcDispatcher,
        RpcRequest,
        RpcErrorRegistry,
        register_product_rpc_errors,
        _object,
        _result_object,
        current_owner_methods,
    ):
        source = Path(inspect.getfile(symbol)).resolve()
        if not source.is_relative_to(CAPTURE_ROOT / "backend"):
            raise RuntimeError(
                "Capture requires this checkout's backend; set PYTHONPATH to its root"
            )
        paths.add(source.relative_to(CAPTURE_ROOT).as_posix())
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
                *sorted(paths),
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
    dispatcher.register(METHOD, partial(service.invoke, METHOD), PRODUCT_RPC_REGISTRY[METHOD])
    request: JsonObject = {
        "jsonrpc": "2.0",
        "id": case.name,
        "method": METHOD,
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
        "typedGoBoundary": case.typed_boundary,
    }


async def capture() -> JsonObject:
    return {
        "producerCommit": PRODUCER_COMMIT,
        "boundary": "Original Python Product DTO/adapter; scripted authority HTTP, not a domain-issued snapshot or product qualification",
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
        parser.error("Original snapshot validation differs; inspect, do not regenerate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
