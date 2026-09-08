"""Freeze the original Workspace field.settings.describe execution and public wire."""

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
from backend.adapters.pocketbase.product_rpc_support import _path_segment, _result_object
from backend.adapters.pocketbase.transport import PocketBaseTransportError
from backend.contracts.generated_product_rpc_capabilities import current_owner_methods
from backend.contracts.product_rpc import PRODUCT_RPC_REGISTRY, JsonObject, JsonValue
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.error_registry import RpcErrorRegistry
from backend.rpc.messages import RpcRequest
from backend.rpc.product_errors import register_product_rpc_errors

PRODUCER_COMMIT = "2c211088a682163bfdd126eda4528b69cd8419f8"
METHOD = "field.settings.describe"
OUTPUT = Path(__file__).with_name("field-settings-describe-python-oracle.json")
CAPTURE_ROOT = Path(__file__).resolve().parents[2]


@dataclass(frozen=True)
class Case:
    name: str
    params: JsonValue
    response: JsonValue
    failure: str | None = None
    typed_boundary: str | None = None


def settings_result(field_id: str = "") -> JsonObject:
    return {
        "contract": "vibetable.schema.v2",
        "tableId": "orders",
        "fieldId": field_id,
        "schemaRevision": "schema-1",
        "dataRevision": 7,
        "definition": None,
        "capabilities": [],
        "recommendedDefaultsVersion": 1,
    }


def cases() -> tuple[Case, ...]:
    result = settings_result()
    return (
        Case("table-defaults", {"tableId": "orders"}, result),
        Case("selected-field", {"tableId": "orders", "fieldId": "title"}, settings_result("title")),
        Case(
            "field-whitespace-preserved",
            {"tableId": "orders", "fieldId": " \t "},
            settings_result(" \t "),
        ),
        Case(
            "field-unicode-path-preserved",
            {"tableId": "orders", "fieldId": "../Cafe\u0301/中文 👩🏽‍💻?x=1"},
            settings_result("../Cafe\u0301/中文 👩🏽‍💻?x=1"),
        ),
        Case("table-128-characters", {"tableId": "a" * 128}, {**result, "tableId": "a" * 128}),
        Case("table-ascii-hyphen-underscore", {"tableId": "A_1-z"}, {**result, "tableId": "A_1-z"}),
        Case("missing-table", {}, result),
        Case("empty-table", {"tableId": ""}, result),
        Case("null-table", {"tableId": None}, result),
        Case("nonobject-params", [], result),
        Case("empty-field", {"tableId": "orders", "fieldId": ""}, result),
        Case("null-field", {"tableId": "orders", "fieldId": None}, result),
        Case("numeric-field", {"tableId": "orders", "fieldId": 7}, result),
        Case("unknown-param", {"tableId": "orders", "extra": False}, result),
        Case("table-whitespace", {"tableId": " orders "}, result),
        Case("table-unicode", {"tableId": "订单"}, result),
        Case("table-path-separator", {"tableId": "../orders"}, result),
        Case("table-percent-escape", {"tableId": "orders%2Ftitle"}, result),
        Case("table-leading-underscore", {"tableId": "_orders"}, result),
        Case("table-129-characters", {"tableId": "a" * 129}, result),
        Case(
            "field-type-before-table-path",
            {"tableId": "../orders", "fieldId": None},
            result,
            "domain",
        ),
        Case(
            "table-path-before-authority-error",
            {"tableId": "../orders", "fieldId": "title"},
            result,
            "domain",
        ),
        Case(
            "empty-response-object",
            {"tableId": "orders"},
            {},
            typed_boundary="FieldSettingsDescribeResult always emits its eight fields; an empty object is not representable",
        ),
        Case(
            "dynamic-response-pass-through",
            {"tableId": "orders", "fieldId": "title"},
            {
                **settings_result("title"),
                "definition": {"custom": [None, False, 0, "", "Cafe\u0301 👩🏽‍💻"]},
                "extension": {"nested": [1, "1"]},
            },
            typed_boundary="Typed FieldDefinition and fixed result cannot preserve arbitrary definition/extra root members; dynamic JSON itself is not a blanket exemption",
        ),
        Case(
            "wrong-shaped-response-object",
            {"tableId": "orders"},
            {"contract": False, "dataRevision": "seven", "capabilities": {"custom": True}},
            typed_boundary="Typed result cannot express wrong scalar/container types and omitted required wire fields; original Python passes the object through",
        ),
        Case(
            "null-response",
            {"tableId": "orders"},
            None,
            typed_boundary="Typed response struct cannot be a null root",
        ),
        Case(
            "array-response",
            {"tableId": "orders"},
            [],
            typed_boundary="Typed response struct cannot be an array root",
        ),
        Case("public-domain-error", {"tableId": "orders", "fieldId": "missing"}, result, "domain"),
        Case(
            "transport-error",
            {"tableId": "orders"},
            result,
            "transport",
            "Direct in-process service has no Python HTTP transport outage; public domain errors remain replayable",
        ),
        populated_settings_case(),
    )


def populated_settings_case() -> Case:
    """Independent valid input shaped after the schema-v2 fixtures, never an expected response."""
    definition: JsonObject = {
        "contract": "vibetable.schema.v2",
        "identity": {
            "fieldId": "fld_01JABCDE",
            "physicalName": "f_01jabcde",
            "providerFieldId": "pb_01JABCDE",
        },
        "displayName": "金额 Café 👩🏽\u200d💻",
        "help": "保留空值、默认值与精度；不改写 Unicode。",
        "logicalType": "number",
        "lifecycle": {"state": "active", "retiredAt": None},
        "value": {
            "required": False,
            "default": {
                "enabled": False,
                "value": None,
                "source": "recommended",
                "defaultsVersion": 1,
            },
            "presence": {
                "mode": "companion",
                "providerFieldId": "pb_01JPRESEN",
                "physicalName": "__vt_has_f_01jabcde",
            },
        },
        "constraints": {
            "unique": {"enabled": False, "blankPolicy": "ignoreMissing"},
            "range": {"min": None, "max": None},
            "length": {"min": None, "max": None},
            "pattern": {"enabled": False, "value": ""},
            "domains": {"only": [], "except": []},
            "selection": {"min": 0, "max": None},
        },
        "storage": {
            "kind": "pocketbase-number",
            "options": {"onlyInt": False, "maxSize": 0, "convertURLs": False, "presentable": False},
        },
        "display": {
            "kind": "number",
            "preset": "number",
            "displayScale": 2,
            "scaleMode": "max",
            "trimTrailingZeros": True,
            "useGrouping": True,
            "currency": "CNY",
            "percentStorage": "ratio",
            "unit": None,
            "precision": "minute",
            "timezone": "system",
            "mode": "default",
            "indent": 0,
            "trueLabel": "是",
            "falseLabel": "否",
        },
    }
    capability: JsonObject = {
        "logicalType": "number",
        "generalSettings": ["displayName", "help", "required", "default", "unique"],
        "advancedSettings": ["range", "onlyInt", "displayScale"],
        "dangerSettings": ["retire", "purge"],
        "recommended": {
            "defaultsVersion": 1,
            "value": {
                "required": False,
                "default": {
                    "enabled": False,
                    "value": None,
                    "source": "recommended",
                    "defaultsVersion": 1,
                },
                "presence": {"mode": "companion"},
            },
            "constraints": {
                "unique": {"enabled": False, "blankPolicy": "ignoreMissing"},
                "range": {"min": None, "max": None},
                "length": {"min": None, "max": None},
                "pattern": {"enabled": False, "value": ""},
                "domains": {"only": [], "except": []},
                "selection": {"min": 0, "max": None},
            },
            "storage": {
                "kind": "pocketbase-number",
                "options": {
                    "onlyInt": False,
                    "maxSize": 0,
                    "convertURLs": False,
                    "presentable": False,
                },
            },
            "display": {
                "kind": "number",
                "preset": "number",
                "displayScale": 2,
                "scaleMode": "max",
                "trimTrailingZeros": True,
                "useGrouping": True,
                "currency": "CNY",
                "percentStorage": "ratio",
                "unit": None,
                "precision": "minute",
                "timezone": "system",
                "mode": "default",
                "indent": 0,
                "trueLabel": "是",
                "falseLabel": "否",
            },
        },
        "supportsRequired": True,
        "supportsDefault": True,
        "supportsUnique": True,
        "needsPresence": True,
        "displayPresets": ["number", "integer", "currency", "percent", "unit"],
        "conversionTargets": ["text"],
        "conversionRules": ["round", "floor", "ceil", "truncate", "block"],
        "compileStrategy": "pocketbase-number",
        "userCreatable": True,
        "filterOperators": ["eq", "ne", "isEmpty", "isNotEmpty", "gt", "gte", "lt", "lte"],
        "groupable": True,
        "summaryOperations": ["count", "countDistinct", "sum", "avg", "min", "max"],
        "relationCardinalities": [],
        "relationDeletePolicies": [],
        "lookupMaxDepth": 0,
        "formulaResultTypeInferred": False,
        "formulaRelationAggregates": [],
    }
    return Case(
        "populated-valid-definition-and-capability",
        {"tableId": "orders", "fieldId": "fld_01JABCDE"},
        {**settings_result("fld_01JABCDE"), "definition": definition, "capabilities": [capability]},
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
            assert method == "GET"
            assert path == "/api/vibetable/v2/field-settings/" + str(params["tableId"])
            expected_query = {"fieldId": params["fieldId"]} if "fieldId" in params else {}
            assert query == expected_query
            assert headers == {"X-VibeTable-Session": "oracle-only"}
            assert tuple(expected_status) == (200,)
            assert json_body is None
        except AssertionError:
            self.violations.append(f"attempt {len(self.requests)}: {method} {path}")
            raise
        if self.case.failure == "domain":
            raise PocketBaseProductError(
                status=404,
                payload={
                    "contract": "vibetable.schema.v2",
                    "code": "field.not_found",
                    "path": "fieldId",
                    "message": "field was not found",
                    "details": {"fieldId": "missing"},
                    "retryable": False,
                    "occurredAt": "2026-09-08T00:00:00Z",
                },
            )
        if self.case.failure == "transport":
            raise PocketBaseTransportError("field settings unavailable")
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
        self.violations.append("field settings must not upload")
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
        self.violations.append("field settings must not download")
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
        _path_segment,
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
        "boundary": "Original Workspace catalog method through Python Product DTO/adapter; scripted authority HTTP",
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
        parser.error("Original field settings differs; inspect, do not regenerate")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
