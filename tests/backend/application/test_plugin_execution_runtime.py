"""Local plugin execution runtime tests over closed product ports."""

from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import pytest

from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.contracts.plugin import (
    CommandContext,
    PluginAuditEvent,
    PluginManifest,
    PluginSnapshot,
)
from backend.infrastructure.plugin_worker import (
    InMemoryBulkMutationAdapter,
    InMemoryHostConfirmationAdapter,
)

FIELD_VALUE_CORPUS_PATH = (
    Path(__file__).resolve().parents[3]
    / "contracts"
    / "schema-v2"
    / "fixtures"
    / "field-value-entry-corpus.json"
)


def _field_value_corpus_values() -> dict[str, Any]:
    payload = json.loads(FIELD_VALUE_CORPUS_PATH.read_text(encoding="utf-8"))
    return {case["field"]: case["productValue"] for case in payload["cases"]}


def _snapshot(
    *,
    status: str = "enabled",
    risk: str = "read",
    requires: dict[str, Any] | None = None,
) -> PluginSnapshot:
    manifest = PluginManifest.model_validate(
        {
            "$schema": "vibetable.plugin-manifest.v1",
            "pluginId": "com.example.summary",
            "version": "1.0.0",
            "displayName": {"en": "Summary"},
            "compatibility": {
                "minHostVersion": "1.0.0",
                "pluginApi": "1.x",
            },
            "permissions": {
                "data": [],
                "files": [],
                "privateStorage": False,
            },
            "actions": [
                {
                    "actionId": "summarize",
                    "displayName": {"en": "Summarize"},
                    "mode": "local",
                    "risk": risk,
                    "workerEntry": "dist/worker.js",
                    "requires": requires or {},
                }
            ],
        }
    )
    return PluginSnapshot(
        project_key="local:default",
        plugin_id="com.example.summary",
        version="1.0.0",
        package_hash="sha256:package",
        source_type="package",
        source_location="summary.vtplugin",
        manifest=manifest,
        status=status,
        revision=1,
    )


class FakeRegistry:
    def __init__(self, snapshot: PluginSnapshot | None) -> None:
        self.snapshot = snapshot
        self.audit: list[PluginAuditEvent] = []

    def get(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        if (
            self.snapshot is not None
            and project_key == self.snapshot.project_key
            and plugin_id == self.snapshot.plugin_id
        ):
            return self.snapshot
        return None

    def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
        self.audit.append(event)
        return event


@dataclass
class RecordingWorker:
    result: dict[str, Any]
    available: bool = True
    executions: list[dict[str, Any]] = field(default_factory=list)

    async def run(
        self,
        worker_entry: str,
        context: dict[str, Any],
        input_payload: dict[str, Any],
        *,
        execution: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        del worker_entry, context, input_payload
        assert execution is not None
        self.executions.append(execution)
        return self.result


def _context(*, selected: list[str] | None = None) -> CommandContext:
    return CommandContext(
        project_key="local:default",
        collection="articles",
        selected_keys=selected or [],
    )


async def _terminal(
    runtime: PluginExecutionRuntime,
    task_id: str,
) -> Any:
    for _ in range(100):
        snapshot = runtime.get_task(task_id)
        if snapshot.state in {
            "succeeded",
            "failed",
            "cancelled",
            "aborted",
        }:
            return snapshot
        await asyncio.sleep(0)
    raise AssertionError("plugin task did not reach a terminal state")


def test_describe_centralizes_plugin_and_context_availability() -> None:
    runtime = PluginExecutionRuntime(
        registry=FakeRegistry(
            _snapshot(
                status="disabled",
                requires={"selection": "one-or-more"},
            )
        ),
        worker_adapter=RecordingWorker({}),
    )

    availability = runtime.describe(
        "com.example.summary",
        "summarize",
        _context(),
    )

    assert not availability.available
    assert availability.reasons == ["plugin_disabled", "selection_required"]


@pytest.mark.asyncio
async def test_read_action_runs_once_with_immutable_package_identity() -> None:
    worker = RecordingWorker(
        {
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "2 rows",
            "metrics": [{"label": "rows", "value": 2}],
        }
    )
    registry = FakeRegistry(_snapshot())
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=worker,
    )
    events: list[Any] = []
    audit_counts: list[int] = []

    async def record(event: Any) -> None:
        events.append(event)
        audit_counts.append(len(registry.audit))

    runtime.set_notification_sink(record)
    started = await runtime.start(
        "com.example.summary",
        "summarize",
        _context(selected=["1", "2"]),
        {},
    )
    completed = await _terminal(runtime, started.task_id)

    assert completed.state == "succeeded"
    assert completed.result.summary == "2 rows"
    assert len(worker.executions) == 1
    assert worker.executions[0]["pluginId"] == "com.example.summary"
    assert worker.executions[0]["pluginVersion"] == "1.0.0"
    assert worker.executions[0]["packageHash"] == "sha256:package"
    assert [event.snapshot["state"] for event in events] == [
        "running",
        "succeeded",
    ]
    assert audit_counts == [0, 1]
    assert len(registry.audit) == 1
    audit = registry.audit[0]
    assert audit.event_type == "action"
    assert audit.outcome == "succeeded"
    assert audit.action_id == "summarize"
    assert audit.run_id == completed.run_id
    assert audit.target_collection == "articles"
    assert audit.target_count == 2
    assert audit.error_code is None
    assert audit.finished_at is not None
    assert audit.duration_ms is not None


@pytest.mark.asyncio
async def test_start_yields_until_background_execution_has_left_queued_state() -> None:
    worker_started = asyncio.Event()
    worker_release = asyncio.Event()

    class GatedWorker:
        available = True

        async def run(
            self,
            worker_entry: str,
            context: dict[str, Any],
            input_payload: dict[str, Any],
            *,
            execution: dict[str, Any] | None = None,
        ) -> dict[str, Any]:
            del worker_entry, context, input_payload, execution
            worker_started.set()
            await worker_release.wait()
            return {
                "contract": "vibetable.plugin-result.v1",
                "status": "success",
                "summary": "done",
            }

    runtime = PluginExecutionRuntime(
        registry=FakeRegistry(_snapshot()),
        worker_adapter=GatedWorker(),
    )

    started = await runtime.start(
        "com.example.summary",
        "summarize",
        _context(),
        {},
    )

    assert worker_started.is_set()
    assert runtime.get_task(started.task_id).state == "running"
    worker_release.set()
    assert (await _terminal(runtime, started.task_id)).state == "succeeded"


@pytest.mark.asyncio
async def test_write_action_requires_confirmation_before_product_mutation() -> None:
    trace: list[str] = []
    worker = RecordingWorker(
        {
            "contract": "vibetable.mutation-plan.v1",
            "collection": "articles",
            "operations": [
                {
                    "kind": "update",
                    "primaryKey": "1",
                    "expectedDateUpdated": "sha256:old",
                    "values": {"title": "updated"},
                }
            ],
            "preview": {
                "summary": [{"label": "rows", "value": 1}],
                "affectedCount": 1,
            },
            "idempotencyKey": "plugin-run-1",
        }
    )
    confirmation = InMemoryHostConfirmationAdapter(
        decisions=[True],
        trace=trace,
    )
    mutation = InMemoryBulkMutationAdapter(
        result={
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "updated",
            "refresh": {"collections": ["articles"]},
        },
        trace=trace,
    )
    runtime = PluginExecutionRuntime(
        registry=FakeRegistry(_snapshot(risk="write")),
        worker_adapter=worker,
        confirmation_adapter=confirmation,
        mutation_adapter=mutation,
    )

    started = await runtime.start(
        "com.example.summary",
        "summarize",
        _context(selected=["1"]),
        {},
    )
    completed = await _terminal(runtime, started.task_id)

    assert completed.state == "succeeded"
    assert completed.result.summary == "updated"
    assert trace == ["host.confirm", "bulk.apply"]
    assert mutation.plans[0].operations[0].expected_date_updated == "sha256:old"


@pytest.mark.asyncio
async def test_plugin_forwards_shared_corpus_product_values_unchanged() -> None:
    values = _field_value_corpus_values()
    worker = RecordingWorker(
        {
            "contract": "vibetable.mutation-plan.v1",
            "collection": "articles",
            "operations": [{"kind": "create", "values": values}],
            "preview": {"affectedCount": 1},
            "idempotencyKey": "plugin-corpus-1",
        }
    )
    mutation = InMemoryBulkMutationAdapter(
        result={
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "created",
        }
    )
    runtime = PluginExecutionRuntime(
        registry=FakeRegistry(_snapshot(risk="write")),
        worker_adapter=worker,
        confirmation_adapter=InMemoryHostConfirmationAdapter(decisions=[True]),
        mutation_adapter=mutation,
    )

    started = await runtime.start(
        "com.example.summary",
        "summarize",
        _context(),
        {},
    )
    completed = await _terminal(runtime, started.task_id)

    assert completed.state == "succeeded"
    assert mutation.plans[0].operations[0].values == values


@pytest.mark.asyncio
async def test_rejected_write_never_reaches_product_mutation() -> None:
    worker = RecordingWorker(
        {
            "contract": "vibetable.mutation-plan.v1",
            "collection": "articles",
            "operations": [],
            "preview": {"affectedCount": 0},
        }
    )
    confirmation = InMemoryHostConfirmationAdapter(decisions=[False])
    mutation = InMemoryBulkMutationAdapter(
        result={
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "unexpected",
        }
    )
    registry = FakeRegistry(_snapshot(risk="write"))
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=worker,
        confirmation_adapter=confirmation,
        mutation_adapter=mutation,
    )

    started = await runtime.start(
        "com.example.summary",
        "summarize",
        _context(selected=["1"]),
        {},
    )
    completed = await _terminal(runtime, started.task_id)

    assert completed.state == "failed"
    assert completed.error.code == "plugin_action_failed"
    assert mutation.plans == []
    assert len(registry.audit) == 1
    assert registry.audit[0].outcome == "failed"
    assert registry.audit[0].error_code == "plugin_action_failed"
