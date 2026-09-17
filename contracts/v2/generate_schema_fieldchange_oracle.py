"""Check retained schema/field-change wire after closing the Python capture.

The frozen original captured the real RpcDispatcher, Product parameter DTOs,
PocketBaseProductRpc and the registered product errors with only the loopback
HTTP end scripted. The seven forwarding handlers are retired, so the capture
stays closed and only the retained inputs and public wire are validated against
the frozen original; nothing is replayed through Python or regenerated from Go.
"""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

from backend.contracts.product_rpc import JsonObject, JsonValue

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
CAPTURE_CLOSED = "Python schema/field-change capture is retired; preserve the frozen producer"
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


CAPTURED_ROUTES = {
    "schema.table.create": ("POST", "/api/vibetable/v2/schema/tables"),
    "schema.delete": ("POST", "/api/vibetable/v1/schema/delete"),
    "field.change.plan": ("POST", "/api/vibetable/v2/field-change/plan"),
    "field.change.apply": ("POST", "/api/vibetable/v2/field-change/apply"),
    "field.change.status": ("GET", "/api/vibetable/v2/field-change/status/{id}"),
    "field.change.cancel": ("POST", "/api/vibetable/v2/field-change/cancel/{id}"),
    "field.recycleBin.list": ("GET", "/api/vibetable/v2/field-recycle-bin/{id}"),
}
PATH_KEYS = {
    "field.change.status": "jobId",
    "field.change.cancel": "jobId",
    "field.recycleBin.list": "tableId",
}
PARAM_REJECTIONS = frozenset(
    {
        "schema.table.create:unknown-param",
        "schema.table.create:missing-required-actor",
        "schema.table.create:actor-type",
        "schema.table.create:nested-credential",
        "schema.delete:missing-required-revision",
        "field.change.plan:unknown-param",
        "field.change.plan:missing-required-revision",
        "field.change.plan:draft-type",
        "field.change.plan:depth-budget",
        "field.change.apply:nested-actor-empty-id",
        "field.change.apply:nested-confirmation-type",
        "field.change.apply:unknown-param",
        "field.change.status:missing-job-id",
        "field.change.status:numeric-job-id",
        "field.change.cancel:empty-job-id",
        "field.recycleBin.list:null-table-id",
    }
)
ENVELOPE_REJECTIONS = frozenset({"field.change.status:envelope-nonobject-params"})
HANDLER_REJECTIONS = frozenset(
    {
        "field.change.status:invalid-job-path",
        "field.change.cancel:invalid-job-path",
        "field.change.cancel:oversized-job-path",
        "field.recycleBin.list:invalid-table-path",
    }
)
INVALID_RESPONSES = frozenset(
    {
        "schema.table.create:scalar-response",
        "field.change.status:array-response",
        "field.change.status:empty-body-response",
    }
)
# Fixed assertions over the original capture, never regenerated output.
PUBLIC_ERROR_DATA = {
    "schema.delete:revision-conflict": {
        "kind": "product_data_error",
        "message": "schema revision is stale",
        "code": "schema.revision_conflict",
        "path": "expectedRevision",
        "details": {},
        "retryable": False,
    },
    "schema.delete:transport-failure": {
        "kind": "product_data_unavailable",
        "message": "PocketBase sidecar is unavailable",
        "code": "sidecar.unavailable",
    },
    "field.change.plan:schema-conflict": {
        "kind": "product_data_error",
        "message": "schema revision changed",
        "code": "field.change.schema_conflict",
        "path": "expectedSchemaRevision",
        "details": {},
        "retryable": False,
    },
    "field.change.apply:plan-expired": {
        "kind": "product_data_error",
        "message": "field change plan expired",
        "code": "field.change.plan_expired",
        "path": "planId",
        "details": {},
        "retryable": False,
    },
    "field.change.status:migration-not-found": {
        "kind": "product_data_error",
        "message": "field migration job was not found",
        "code": "field.migration.not_found",
        "path": "jobId",
        "details": {},
        "retryable": False,
    },
}


async def capture_case(case: Case) -> JsonObject:
    raise RuntimeError(CAPTURE_CLOSED)


async def capture() -> JsonObject:
    raise RuntimeError(CAPTURE_CLOSED)


def _authority_attempts(case: Case) -> list[JsonValue]:
    if (
        case.name in PARAM_REJECTIONS
        or case.name in ENVELOPE_REJECTIONS
        or case.name in HANDLER_REJECTIONS
    ):
        return []
    verb, route = CAPTURED_ROUTES[case.method]
    key = PATH_KEYS.get(case.method)
    if key is None:
        path, body = route, case.params
    else:
        identifier = case.params[key] if isinstance(case.params, dict) else None
        path, body = route.format(id=identifier), None if verb == "GET" else {}
    return [{"method": verb, "path": path, "body": body}]


def validate_frozen_inputs() -> None:
    """Check historical inputs and wire invariants, not current Python or Go parity."""
    frozen: JsonObject = json.loads(OUTPUT.read_text(encoding="utf-8"))
    if not isinstance(frozen, dict) or frozen.get("producerCommit") != PRODUCER_COMMIT:
        raise ValueError("Frozen schema/field-change producer changed")
    if frozen.get("boundary") != BOUNDARY:
        raise ValueError("Frozen schema/field-change boundary changed")
    if set(frozen) != {"producerCommit", "boundary", "cases"}:
        raise ValueError("Frozen schema/field-change metadata changed")
    entries = frozen.get("cases")
    inputs = cases()
    if not isinstance(entries, list) or len(entries) != len(inputs) or len(inputs) != 38:
        raise ValueError("Frozen schema/field-change case inventory changed")
    for entry, case in zip(entries, inputs, strict=True):
        if not isinstance(entry, dict) or set(entry) != {
            "name",
            "request",
            "authorityFixture",
            "authorityRequests",
            "response",
        }:
            raise ValueError("Invalid frozen schema/field-change entry")
        expected: JsonObject = {
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
            "authorityRequests": _authority_attempts(case),
        }
        actual = {key: entry[key] for key in expected}
        if render(actual) != render(expected):
            raise ValueError(f"Frozen schema/field-change inputs or attempts changed: {case.name}")
        response = entry["response"]
        if (
            not isinstance(response, dict)
            or response.get("jsonrpc") != "2.0"
            or response.get("id") != case.name
        ):
            raise ValueError(f"Frozen schema/field-change response envelope changed: {case.name}")
        if case.name in PUBLIC_ERROR_DATA:
            error = response.get("error")
            if (
                set(response) != {"jsonrpc", "id", "error"}
                or not isinstance(error, dict)
                or set(error) != {"code", "message", "data"}
                or error.get("code") != -32150
                or error.get("message") not in {"Product data error", "Product data unavailable"}
                or error.get("data") != PUBLIC_ERROR_DATA[case.name]
            ):
                raise ValueError(f"Frozen schema/field-change public error changed: {case.name}")
            continue
        code, message = (
            (-32602, "Invalid params")
            if case.name in PARAM_REJECTIONS
            else (-32600, "Invalid Request")
            if case.name in ENVELOPE_REJECTIONS
            else (-32603, "Internal error")
            if case.name in HANDLER_REJECTIONS or case.name in INVALID_RESPONSES
            else (None, None)
        )
        if code is None:
            if set(response) != {"jsonrpc", "id", "result"} or render(response["result"]) != render(
                case.authority.body
            ):
                raise ValueError(f"Frozen schema/field-change result changed: {case.name}")
            continue
        if set(response) != {"jsonrpc", "id", "error"} or response.get("error") != {
            "code": code,
            "message": message,
        }:
            raise ValueError(f"Frozen schema/field-change error envelope changed: {case.name}")


def render(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--write", action="store_true", help="Retired; always rejected")
    modes.add_argument(
        "--check", action="store_true", help="Validate retained inputs and wire (default)"
    )
    args = parser.parse_args()
    if args.write:
        parser.error(CAPTURE_CLOSED)
    try:
        validate_frozen_inputs()
    except (ValueError, OSError) as error:
        parser.error(str(error))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
