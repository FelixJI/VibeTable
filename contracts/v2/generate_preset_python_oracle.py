"""Capture the fixed Python Preset public seam with scripted metadata transport replies."""

from __future__ import annotations

import argparse
import asyncio
import copy
import json
import logging
import subprocess
import uuid
from collections.abc import Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Never

import backend
from backend.adapters.pocketbase.internal_metadata import PocketBaseInternalMetadataPort
from backend.application.insights_service import InsightsService
from backend.application.revisioned_metadata_port import JsonObject, JsonValue
from backend.contracts.presets_versions_dashboards import (
    DeletePresetParams,
    ListPresetsParams,
    SavePresetParams,
)
from backend.rpc.dispatcher import RpcDispatcher
from backend.rpc.error_registry import ErrorDomain, register_application_errors

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "146a9c2cac5998ee013daebc78eedff0bd4a7ca5"
OUTPUT = Path(__file__).with_name("preset-python-oracle.json")


@dataclass(frozen=True)
class Reply:
    operation: str
    value: JsonObject = field(default_factory=dict)
    error_code: str | None = None


@dataclass(frozen=True)
class Case:
    name: str
    method: str
    params: JsonObject
    replies: tuple[Reply, ...] = ()
    repeats: int = 1


class ScriptedError(RuntimeError):
    def __init__(self, code: str) -> None:
        super().__init__("scripted metadata failure")
        self.code = code


class ScriptedClient:
    """No storage/CAS/replay implementation: consume explicit external replies in order."""

    def __init__(self, replies: tuple[Reply, ...]) -> None:
        self.replies = replies
        self.calls: list[JsonObject] = []
        self.mismatch: str | None = None

    def consume(self, operation: str, namespace: str, request: JsonObject) -> JsonObject:
        index = len(self.calls)
        self.calls.append({"operation": operation, "namespace": namespace, "request": request})
        if index >= len(self.replies) or self.replies[index].operation != operation:
            self.mismatch = f"Unexpected transport call {operation} at {index}"
            raise AssertionError(self.mismatch)
        reply = self.replies[index]
        if reply.error_code is not None:
            raise ScriptedError(reply.error_code)
        return copy.deepcopy(reply.value)

    async def list_internal_metadata(self, namespace: str) -> JsonObject:
        return self.consume("list", namespace, {})

    async def upsert_internal_metadata(
        self, namespace: str, request: Mapping[str, JsonValue]
    ) -> JsonObject:
        return self.consume("upsert", namespace, dict(request))

    async def delete_internal_metadata(
        self, namespace: str, request: Mapping[str, JsonValue]
    ) -> JsonObject:
        return self.consume("delete", namespace, dict(request))

    async def commit_dashboard_metadata(self, request: JsonObject) -> JsonObject:
        raise AssertionError("Preset capture must not call dashboard metadata")


class UnusedQueryPort:
    async def query_page(self, *, table_id: str, query: JsonObject) -> Never:
        raise AssertionError("Preset must not execute a table query")

    async def aggregate(self, *, table_id: str, query: JsonObject) -> list[JsonObject]:
        raise AssertionError("Preset must not aggregate")

    async def read_history(self, *, collection: str, item_id: str, limit: int = 50) -> JsonObject:
        raise AssertionError("Preset must not read history")

    async def preview_history_restore(
        self, *, collection: str, item_id: str, target_revision: str
    ) -> JsonObject:
        raise AssertionError("Preset must not preview history")

    async def apply_history_restore(
        self, *, collection: str, item_id: str, token: str
    ) -> JsonObject:
        raise AssertionError("Preset must not restore history")


def item(logical_id: str = "preset-existing", **payload: JsonValue) -> JsonObject:
    return {
        "logicalId": logical_id,
        "revision": "revision-scripted-before",
        "payload": {"scope": "orders", "name": "Existing", **payload},
    }


def listing(*items: JsonObject) -> Reply:
    return Reply("list", {"items": list(items)})


def applied(operation: str = "upsert") -> Reply:
    # These opaque values are supplied transport facts, not generated PB identities.
    return Reply(
        operation,
        {
            "status": "applied",
            "changeSetId": "change-scripted",
            "emittedEvents": ["event-scripted"],
            "item": {
                "logicalId": "receipt-item-scripted",
                "revision": "revision-scripted-after",
                "payload": {},
            },
        },
    )


def save(view: JsonObject | None = None, **changes: JsonValue) -> JsonObject:
    return {
        "collection": "orders",
        "name": "Saved 中文",
        "view": view or {},
        "presetId": None,
        "expectedRevision": None,
        "operationId": "operation-fixed",
        **changes,
    }


def repeated(value: JsonValue, count: int) -> list[JsonValue]:
    return [copy.deepcopy(value) for _ in range(count)]


def cases() -> list[Case]:
    result = [Case("list-empty", "preset.list", {"collection": "orders"}, (listing(),))]
    views: dict[str, JsonObject] = {
        "table": {},
        "calendar": {"kind": "calendar", "dateField": "due", "titleField": "title"},
        "timeline": {"kind": "timeline", "dateField": "start", "endDateField": "end"},
        "kanban": {"kind": "kanban", "groupField": "status", "columns": [{"key": "open"}]},
        "gallery": {"kind": "gallery", "coverField": "photo", "density": "cozy"},
    }
    for kind, view in views.items():
        result.append(
            Case(f"save-{kind}-defaults", "preset.save", save(view), (listing(), applied()))
        )
    rich: JsonObject = {
        "filters": [
            {"groupLogic": "OR", "filters": [{"field": "amount", "operator": "gt", "value": 3}]}
        ],
        "sorts": [{"field": "title"}],
        "groups": [{"field": "amount", "bucket": "number", "numberInterval": 10}],
        "summaries": [{"field": "amount", "function": "sum"}],
        "collapsedGroupKeys": ["bucket:0"],
        "visibleFields": ["title", "amount"],
        "search": "中文",
        "isDefault": True,
        "columns": [{"custom": {"retained": [None, False, 3]}}],
    }
    result.append(Case("save-complete-view", "preset.save", save(rich), (listing(), applied())))
    rows = (
        item("z", key="a", presetScope="role", view={"isDefault": False}),
        item("a", name="Zulu", presetScope="system", view={"isDefault": True}),
        item("b", name="Alpha", presetScope="unknown", userId="reader"),
        item("foreign", scope="other"),
        item("absent-scope", scope=None),
    )
    result.append(
        Case(
            "list-key-id-order-scope-defaults",
            "preset.list",
            {"collection": "orders"},
            (listing(*rows),),
        )
    )
    update = save(presetId="preset-existing", expectedRevision="revision-scripted-before")
    result.append(
        Case(
            "save-update-preserves-payload",
            "preset.save",
            update,
            (listing(item(extra="retained", presetScope="personal", userId="reader")), applied()),
        )
    )
    # Same logical identity is derived by the real service; the script merely supplies
    # the current item that a real authority may return on the second public call.
    generated = str(uuid.uuid5(uuid.NAMESPACE_URL, "vibetable:preset:operation-fixed"))
    result.append(
        Case(
            "save-create-repeat-observes-current",
            "preset.save",
            save(),
            (listing(), applied(), listing(item(generated)), applied()),
            2,
        )
    )
    result.append(
        Case(
            "save-update-repeat-public-request",
            "preset.save",
            update,
            (listing(item()), applied(), listing(item()), applied()),
            2,
        )
    )
    result.append(
        Case(
            "save-repeat-current-invalid-blocks-receipt",
            "preset.save",
            update,
            (listing(item()), applied(), Reply("list", {"items": [{}]})),
            2,
        )
    )
    result.append(
        Case(
            "save-create-repeat-changed-wire-conflict",
            "preset.save",
            save(),
            (
                listing(),
                applied(),
                listing(item(generated)),
                Reply("upsert", error_code="metadata.idempotency_conflict"),
            ),
            2,
        )
    )
    for name, reply in [
        ("cas-conflict", Reply("upsert", error_code="metadata.revision_conflict")),
        ("idempotency-conflict", Reply("upsert", error_code="metadata.idempotency_conflict")),
        ("storage-failure", Reply("upsert", error_code="metadata.storage_failed")),
        ("invalid-receipt", Reply("upsert", {"item": None})),
    ]:
        result.append(Case(f"save-{name}", "preset.save", update, (listing(item()), reply)))
    result.append(
        Case(
            "save-current-invalid-blocks-write",
            "preset.save",
            update,
            (Reply("list", {"items": [{"logicalId": "unrelated", "payload": {}}]}),),
        )
    )
    result.append(
        Case(
            "save-current-failure-blocks-write",
            "preset.save",
            update,
            (Reply("list", error_code="metadata.storage_failed"),),
        )
    )
    delete: JsonObject = {
        "presetId": "preset-existing",
        "expectedRevision": "revision-scripted-before",
        "operationId": "delete-fixed",
    }
    result.append(
        Case(
            "delete-repeated-public-request",
            "preset.delete",
            delete,
            (applied("delete"), applied("delete")),
            2,
        )
    )
    for code in [
        "metadata.revision_conflict",
        "metadata.idempotency_conflict",
        "metadata.not_found",
        "metadata.storage_failed",
    ]:
        result.append(
            Case(f"delete-{code}", "preset.delete", delete, (Reply("delete", error_code=code),))
        )
    for name, body in [
        ("invalid-items", {"items": {}}),
        ("invalid-item", {"items": [{}]}),
        ("invalid-view", {"items": [item(view={"kind": "unknown"})]}),
    ]:
        result.append(
            Case(f"list-{name}", "preset.list", {"collection": "orders"}, (Reply("list", body),))
        )
    invalid: list[tuple[str, JsonObject]] = [
        (
            "missing-required-nullables",
            {"collection": "orders", "name": "View", "view": {}, "operationId": "op"},
        ),
        ("target-without-revision", save(presetId="existing")),
        ("revision-without-target", save(expectedRevision="r1")),
        ("operation-empty", save(operationId="")),
        ("operation-null", save(operationId=None)),
        ("name-empty", save(name="")),
        ("unknown-param", save(extra=True)),
        ("kind-invalid", save({"kind": "board"})),
        ("density-invalid", save({"density": "dense"})),
        ("view-extra", save({"extra": 1})),
        ("view-null", {**save(), "view": None}),
        ("filters-51", save({"filters": repeated({"field": "x", "operator": "eq"}, 51)})),
        ("sorts-17", save({"sorts": repeated({"field": "x"}, 17)})),
        ("groups-3", save({"groups": repeated({"field": "x"}, 3)})),
        ("summaries-4", save({"summaries": repeated({"field": "x", "function": "sum"}, 4)})),
        ("search-257", save({"search": "x" * 257})),
    ]
    for name, params in invalid:
        result.append(Case(f"dto-{name}", "preset.save", params))
    for name, method, params in [
        ("list-missing-collection", "preset.list", {}),
        ("delete-empty-revision", "preset.delete", {**delete, "expectedRevision": ""}),
        ("delete-null-operation", "preset.delete", {**delete, "operationId": None}),
    ]:
        result.append(Case(f"dto-{name}", method, params))
    alias = {
        "collection": "orders",
        "name": "Alias",
        "view": {"is_default": "yes", "visible_fields": ["x"]},
        "preset_id": None,
        "expected_revision": None,
        "operation_id": "alias",
    }
    result.append(Case("save-snake-alias-coercion", "preset.save", alias, (listing(), applied())))
    for depth in (3, 4):
        expression: JsonObject = {"field": "x", "operator": "eq"}
        for _ in range(depth):
            expression = {"groupLogic": "AND", "filters": [expression]}
        result.append(
            Case(
                f"filter-depth-{depth}",
                "preset.save",
                save({"filters": [expression]}),
                (listing(), applied()) if depth == 3 else (),
            )
        )
    limits: JsonObject = {
        "filters": repeated({"field": "x", "operator": "eq"}, 50),
        "sorts": repeated({"field": "x"}, 16),
        "groups": repeated({"field": "x"}, 2),
        "summaries": repeated({"field": "x", "function": "sum"}, 3),
        "search": "x" * 256,
        "visibleFields": repeated("x", 128),
        "columns": repeated({}, 128),
        "collapsedGroupKeys": repeated("x", 512),
    }
    result.append(
        Case("view-valid-size-boundaries", "preset.save", save(limits), (listing(), applied()))
    )
    for name, value in [
        ("visibleFields", ["x"] * 129),
        ("columns", [{}] * 129),
        ("collapsedGroupKeys", ["x"] * 513),
    ]:
        result.append(Case(f"dto-{name}-over-limit", "preset.save", save({name: value})))
    return result


async def capture(case: Case) -> JsonObject:
    client = ScriptedClient(case.replies)
    service = InsightsService(
        metadata_port=PocketBaseInternalMetadataPort(client=client), query_port=UnusedQueryPort()
    )
    dispatcher = RpcDispatcher()
    dispatcher.register("preset.list", service.list_presets, ListPresetsParams)
    dispatcher.register("preset.save", service.save_preset, SavePresetParams)
    dispatcher.register("preset.delete", service.delete_preset, DeletePresetParams)
    responses: list[JsonValue] = []
    for _ in range(case.repeats):
        response = await dispatcher.dispatch(
            {
                "jsonrpc": "2.0",
                "id": "preset-oracle",
                "method": case.method,
                "params": copy.deepcopy(case.params),
            }
        )
        responses.append(response)
    if client.mismatch or len(client.calls) != len(case.replies):
        raise AssertionError(f"{case.name}: {client.mismatch or 'unconsumed transport replies'}")
    return {
        "name": case.name,
        "method": case.method,
        "params": case.params,
        "repeats": case.repeats,
        "transportReplies": [
            {"operation": r.operation, "value": r.value, "errorCode": r.error_code}
            for r in case.replies
        ],
        "transportCalls": list(client.calls),
        "responses": responses,
    }


async def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    subprocess.run(
        ["git", "diff", "--quiet", PRODUCER, "--", "backend", "pyproject.toml", "uv.lock"],
        cwd=ROOT,
        check=True,
    )
    if not Path(backend.__file__).resolve().is_relative_to(ROOT):
        raise SystemExit("Capture must import the producer worktree's backend")
    register_application_errors(ErrorDomain.INSIGHTS)
    logging.getLogger("backend.rpc.dispatcher").disabled = True
    results = [await capture(case) for case in cases()]
    corpus = {
        "producer": PRODUCER,
        "boundary": "Original Python dispatcher, InsightsService and metadata adapter; scripted transport replies are inputs, never PocketBase execution or generated storage identities.",
        "cases": results,
    }
    if args.check:
        if json.loads(OUTPUT.read_text(encoding="utf-8")) != corpus:
            raise SystemExit("Frozen Preset corpus differs from its original Python producer")
    else:
        OUTPUT.write_text(json.dumps(corpus, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"Captured {len(results)} Preset cases from {PRODUCER}")


if __name__ == "__main__":
    asyncio.run(main())
