"""Freeze the Python wire of the seven schema/field-change product methods.

The capture dispatches through the real RpcDispatcher, Product parameter DTOs,
PocketBaseProductRpc and the registered product errors; only the loopback HTTP
end is scripted. The recorded responses are therefore Python forwarding and
error-boundary evidence, never Go domain execution or product qualification.
"""

from __future__ import annotations

import argparse
import asyncio
import inspect
import json
import subprocess
from dataclasses import dataclass
from pathlib import Path

import httpx

from backend.__main__ import _register_pocketbase_product_methods
from backend.adapters.pocketbase.client import PocketBaseClient
from backend.adapters.pocketbase.product_query_schema_rpc import ProductQuerySchemaRpc
from backend.adapters.pocketbase.product_rpc import PocketBaseProductRpc
from backend.adapters.pocketbase.transport import PocketBaseConfig, StdlibPocketBaseTransport
from backend.contracts.product_rpc import JsonObject, JsonValue
from backend.rpc.dispatcher import RpcDispatcher

PRODUCER_COMMIT = "98829eb9d7fdddf78afce396fb274f8468210ca8"
REPOSITORY = Path(__file__).resolve().parents[2]
FIXTURES = REPOSITORY / "contracts" / "schema-v2" / "fixtures"
OUTPUT = Path(__file__).with_name("schema-fieldchange-python-oracle.json")
BASE_URL = "http://127.0.0.1:8090"
SESSION_SECRET = "1f2e3d4c5b6a798877665544332211ff" * 2
BOUNDARY = (
    "Fixed Python dispatcher, Product DTO, PocketBaseProductRpc and registered "
    "errors; only the loopback HTTP end is scripted, so responses are Python "
    "forwarding and error-boundary evidence, not Go domain execution"
)
METHODS = (
    "schema.table.create",
    "schema.delete",
    "field.change.plan",
    "field.change.apply",
    "field.change.status",
    "field.change.cancel",
    "field.recycleBin.list",
)


@dataclass(frozen=True)
class Authority:
    status: int = 200
    body: JsonValue | None = None
    failure: str | None = None


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonValue
    authority: Authority


def _fixture(name: str) -> JsonValue:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def _object_fixture(name: str) -> JsonObject:
    value = _fixture(name)
    if not isinstance(value, dict):
        raise ValueError(f"fixture {name} is not a JSON object")
    return value


def _field_error(code: str, path: str, message: str) -> JsonObject:
    return {
        "contract": "vibetable.schema.v2",
        "code": code,
        "path": path,
        "message": message,
        "details": {},
        "retryable": False,
        "occurredAt": "2026-09-17T00:00:00Z",
    }


def _retired_field() -> JsonObject:
    field = _object_fixture("field-definition.json")
    field["lifecycle"] = {"state": "retired", "retiredAt": "2026-09-17T00:00:00Z"}
    return field


def _cancelled_status() -> JsonObject:
    status = _object_fixture("migration-status.json")
    status["phase"] = "cancelled"
    status["processed"] = status["total"]
    status["canCancel"] = False
    return status


def cases() -> tuple[Case, ...]:
    create: JsonObject = {
        "displayName": "订单",
        "operationId": "operation_01JABCDEFGH",
        "actor": {"id": "user_local", "kind": "user"},
    }
    receipt: JsonObject = {
        "contract": "vibetable.schema.v2",
        "operationId": "operation_01JABCDEFGH",
        "tableId": "tbl_orders",
        "displayName": "订单",
        "schemaRevision": "schema_1",
    }
    delete: JsonObject = {"tableId": "tbl_orders", "expectedRevision": "schema_7"}
    intent: JsonObject = _object_fixture("field-change-intent.json")
    apply_request: JsonObject = _object_fixture("apply-request.json")
    patch: JsonObject = {
        "reciprocalDisplayName": "客户",
        "reciprocalCardinality": "many",
        "sourceDisplayFieldId": "fld_01JABCDE",
    }
    deep_draft: JsonValue = "budget"
    for _ in range(40):
        deep_draft = {"nested": deep_draft}
    success = Authority()
    return (
        Case("schema.table.create:success-receipt", METHODS[0], create, Authority(body=receipt)),
        Case(
            "schema.table.create:unknown-param",
            METHODS[0],
            {**create, "extra": False},
            success,
        ),
        Case(
            "schema.table.create:missing-required-actor",
            METHODS[0],
            {key: value for key, value in create.items() if key != "actor"},
            success,
        ),
        Case(
            "schema.table.create:actor-type", METHODS[0], {**create, "actor": "user_local"}, success
        ),
        Case("schema.table.create:scalar-response", METHODS[0], create, Authority(body=7)),
        Case(
            "schema.table.create:nested-credential",
            METHODS[0],
            {
                **create,
                "actor": {"id": "user_local", "kind": "user", "password": "oracle-only"},
            },
            success,
        ),
        Case(
            "schema.delete:success-deleted",
            METHODS[1],
            delete,
            Authority(body={"deleted": True, "tableId": "tbl_orders"}),
        ),
        Case(
            "schema.delete:revision-conflict",
            METHODS[1],
            delete,
            Authority(
                status=409,
                body={
                    "code": "schema.revision_conflict",
                    "path": "expectedRevision",
                    "message": "schema revision is stale",
                },
                failure="domain",
            ),
        ),
        Case(
            "schema.delete:missing-required-revision",
            METHODS[1],
            {"tableId": "tbl_orders"},
            success,
        ),
        Case("schema.delete:transport-failure", METHODS[1], delete, Authority(failure="transport")),
        Case(
            "field.change.plan:success-null-draft",
            METHODS[2],
            intent,
            Authority(body=_fixture("field-change-plan.json")),
        ),
        Case(
            "field.change.plan:optional-pair-patch-forwarded",
            METHODS[2],
            {**intent, "relationPair": None, "relationPairPatch": patch},
            Authority(body=_fixture("field-change-plan.json")),
        ),
        Case("field.change.plan:unknown-param", METHODS[2], {**intent, "extra": False}, success),
        Case(
            "field.change.plan:missing-required-revision",
            METHODS[2],
            {key: value for key, value in intent.items() if key != "expectedSchemaRevision"},
            success,
        ),
        Case("field.change.plan:draft-type", METHODS[2], {**intent, "draft": []}, success),
        Case(
            "field.change.plan:schema-conflict",
            METHODS[2],
            intent,
            Authority(
                status=409,
                body=_field_error(
                    "field.change.schema_conflict",
                    "expectedSchemaRevision",
                    "schema revision changed",
                ),
                failure="domain",
            ),
        ),
        Case(
            "field.change.plan:depth-budget", METHODS[2], {**intent, "draft": deep_draft}, success
        ),
        Case(
            "field.change.apply:success-receipt",
            METHODS[3],
            apply_request,
            Authority(body=_fixture("apply-receipt.json")),
        ),
        Case(
            "field.change.apply:plan-expired",
            METHODS[3],
            apply_request,
            Authority(
                status=410,
                body=_field_error(
                    "field.change.plan_expired",
                    "planId",
                    "field change plan expired",
                ),
                failure="domain",
            ),
        ),
        Case(
            "field.change.apply:nested-actor-empty-id",
            METHODS[3],
            {**apply_request, "actor": {"id": "", "kind": "user"}},
            success,
        ),
        Case(
            "field.change.apply:nested-confirmation-type",
            METHODS[3],
            {**apply_request, "confirmations": [42]},
            success,
        ),
        Case(
            "field.change.apply:unknown-param", METHODS[3], {**apply_request, "extra": 1}, success
        ),
        Case(
            "field.change.status:success-copying",
            METHODS[4],
            {"jobId": "job_01JMIGRATE"},
            Authority(body=_fixture("migration-status.json")),
        ),
        Case(
            "field.change.status:migration-not-found",
            METHODS[4],
            {"jobId": "job_01JMIGRATE"},
            Authority(
                status=404,
                body=_field_error(
                    "field.migration.not_found",
                    "jobId",
                    "field migration job was not found",
                ),
                failure="domain",
            ),
        ),
        Case("field.change.status:invalid-job-path", METHODS[4], {"jobId": "../orders"}, success),
        Case("field.change.status:missing-job-id", METHODS[4], {}, success),
        Case("field.change.status:numeric-job-id", METHODS[4], {"jobId": 7}, success),
        Case(
            "field.change.status:array-response",
            METHODS[4],
            {"jobId": "job_01JMIGRATE"},
            Authority(body=[]),
        ),
        Case(
            "field.change.status:empty-body-response",
            METHODS[4],
            {"jobId": "job_01JMIGRATE"},
            Authority(body=None),
        ),
        Case("field.change.status:envelope-nonobject-params", METHODS[4], [], success),
        Case(
            "field.change.cancel:success-cancelled-job",
            METHODS[5],
            {"jobId": "job_01JMIGRATE"},
            Authority(body=_cancelled_status()),
        ),
        Case("field.change.cancel:invalid-job-path", METHODS[5], {"jobId": "订单"}, success),
        Case("field.change.cancel:empty-job-id", METHODS[5], {"jobId": ""}, success),
        Case(
            "field.change.cancel:oversized-job-path",
            METHODS[5],
            {"jobId": "j" + "_" * 128},
            success,
        ),
        Case(
            "field.recycleBin.list:success-empty-list",
            METHODS[6],
            {"tableId": "tbl_orders"},
            Authority(body=_fixture("field-recycle-bin.json")),
        ),
        Case(
            "field.recycleBin.list:success-retired-field",
            METHODS[6],
            {"tableId": "tbl_orders"},
            Authority(
                body={"contract": "vibetable.schema.v2", "fields": [_retired_field()]},
            ),
        ),
        Case(
            "field.recycleBin.list:invalid-table-path",
            METHODS[6],
            {"tableId": "../orders"},
            success,
        ),
        Case("field.recycleBin.list:null-table-id", METHODS[6], {"tableId": None}, success),
    )


class ScriptedLoopback(httpx.AsyncBaseTransport):
    """Records production requests; replays one frozen authority script."""

    def __init__(self, authority: Authority) -> None:
        self._authority = authority
        self.requests: list[JsonValue] = []

    async def handle_async_request(self, request: httpx.Request) -> httpx.Response:
        raw = request.content
        # Credentials ride in headers and are deliberately not recorded.
        self.requests.append(
            {
                "method": request.method,
                "path": request.url.path,
                "body": json.loads(raw) if raw else None,
            }
        )
        if self._authority.failure == "transport":
            raise httpx.ConnectError("scripted sidecar outage", request=request)
        content = (
            b""
            if self._authority.body is None
            else json.dumps(self._authority.body, ensure_ascii=False).encode("utf-8")
        )
        return httpx.Response(
            self._authority.status,
            content=content,
            headers={"Content-Type": "application/json"},
            request=request,
        )


def check_producer() -> None:
    sources = {
        _register_pocketbase_product_methods: "backend/__main__.py",
        PocketBaseProductRpc: "backend/adapters/pocketbase/product_rpc.py",
        ProductQuerySchemaRpc: "backend/adapters/pocketbase/product_query_schema_rpc.py",
        RpcDispatcher: "backend/rpc/dispatcher.py",
    }
    for symbol, relative in sources.items():
        if Path(inspect.getfile(symbol)).resolve() != REPOSITORY / relative:
            raise RuntimeError("Schema/field-change capture imported a different Python producer")


def check_producer_commit() -> None:
    head = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=REPOSITORY,
        capture_output=True,
        text=True,
        check=True,
    ).stdout.strip()
    if head != PRODUCER_COMMIT:
        raise RuntimeError(f"capture requires producer {PRODUCER_COMMIT}, found {head}")


async def capture_case(case: Case) -> JsonObject:
    loopback = ScriptedLoopback(case.authority)
    transport = StdlibPocketBaseTransport(
        PocketBaseConfig(base_url=BASE_URL, session_secret=SESSION_SECRET),
        http_transport=loopback,
    )
    client = PocketBaseClient(transport=transport, session_secret=SESSION_SECRET)
    service = PocketBaseProductRpc(
        client=client, transport=transport, session_secret=SESSION_SECRET
    )
    dispatcher = RpcDispatcher()
    _register_pocketbase_product_methods(dispatcher, service)
    assert set(dispatcher.registered_methods) >= set(METHODS)
    response = await dispatcher.dispatch(
        {
            "jsonrpc": "2.0",
            "id": case.name,
            "method": case.method,
            "params": case.params,
        }
    )
    return {
        "name": case.name,
        "request": {
            "jsonrpc": "2.0",
            "id": case.name,
            "method": case.method,
            "params": case.params,
        },
        "authorityFixture": {
            "status": case.authority.status,
            "body": case.authority.body,
            "failure": case.authority.failure,
        },
        "authorityRequests": loopback.requests,
        "response": response,
    }


async def capture() -> JsonObject:
    check_producer()
    return {
        "producerCommit": PRODUCER_COMMIT,
        "boundary": BOUNDARY,
        "cases": [await capture_case(case) for case in cases()],
    }


def render(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--write", action="store_true", help="Create the original once")
    modes.add_argument("--check", action="store_true", help="Recompute and compare (default)")
    args = parser.parse_args()
    try:
        if args.write:
            if OUTPUT.exists():
                parser.error("oracle original already exists; --write never overwrites")
            check_producer_commit()
            rendered = render(asyncio.run(capture()))
            with OUTPUT.open("x", encoding="utf-8", newline="\n") as output:
                output.write(rendered)
        else:
            frozen = json.loads(OUTPUT.read_text(encoding="utf-8"))
            if render(frozen) != render(asyncio.run(capture())):
                raise ValueError("frozen oracle does not match the current capture")
    except (OSError, RuntimeError, ValueError, subprocess.CalledProcessError) as error:
        parser.error(str(error))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
