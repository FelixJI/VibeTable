"""Local plugin execution runtime tests over closed product ports."""

from __future__ import annotations

import asyncio
import json
import uuid
from collections.abc import Callable
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
                "minHostVersion": "0.5.1",
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

    async def get(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        if (
            self.snapshot is not None
            and project_key == self.snapshot.project_key
            and plugin_id == self.snapshot.plugin_id
        ):
            return self.snapshot
        return None

    async def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
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


def _ids() -> dict[str, str]:
    return {
        "task_id": f"plugin-task-{uuid.uuid4().hex[:12]}",
        "run_id": f"plugin-run-{uuid.uuid4().hex[:12]}",
    }


async def _wait_terminal(events: list[Any]) -> str:
    for _ in range(100):
        if events and events[-1].snapshot["state"] in {
            "succeeded",
            "failed",
            "cancelled",
            "aborted",
        }:
            return str(events[-1].snapshot["state"])
        await asyncio.sleep(0)
    raise AssertionError("plugin task did not reach a terminal state")


def test_constructor_rejects_unknown_adapters() -> None:
    with pytest.raises(TypeError, match="unexpected keyword argument"):
        PluginExecutionRuntime(
            registry=FakeRegistry(None),
            unknown_adapter=object(),
        )


@pytest.mark.asyncio
async def test_describe_centralizes_plugin_and_context_availability() -> None:
    runtime = PluginExecutionRuntime(
        registry=FakeRegistry(
            _snapshot(
                status="disabled",
                requires={"selection": "one-or-more"},
            )
        ),
        worker_adapter=RecordingWorker({}),
    )

    availability = await runtime.describe(
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
        **_ids(),
    )
    terminal = await _wait_terminal(events)

    assert terminal == "succeeded"
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
    assert audit.run_id == started.run_id
    assert audit.details["taskId"] == started.task_id


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
    events: list[Any] = []

    async def record(event: Any) -> None:
        events.append(event)

    runtime.set_notification_sink(record)
    await runtime.start(
        "com.example.summary",
        "summarize",
        _context(),
        {},
        **_ids(),
    )

    assert worker_started.is_set()
    assert [event.snapshot["state"] for event in events] == ["running"]
    worker_release.set()
    assert await _wait_terminal(events) == "succeeded"


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
                    "expectedDigest": "sha256:" + "a" * 64,
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

    events: list[Any] = []

    async def record(event: Any) -> None:
        events.append(event)

    runtime.set_notification_sink(record)
    await runtime.start(
        "com.example.summary",
        "summarize",
        _context(selected=["1"]),
        {},
        **_ids(),
    )

    assert await _wait_terminal(events) == "succeeded"
    assert events[-1].snapshot["result"]["summary"] == "updated"
    assert trace == ["host.confirm", "bulk.apply"]
    assert mutation.plans[0].operations[0].expected_digest == "sha256:" + "a" * 64


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

    events: list[Any] = []

    async def record(event: Any) -> None:
        events.append(event)

    runtime.set_notification_sink(record)
    await runtime.start(
        "com.example.summary",
        "summarize",
        _context(),
        {},
        **_ids(),
    )

    assert await _wait_terminal(events) == "succeeded"
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

    events: list[Any] = []

    async def record(event: Any) -> None:
        events.append(event)

    runtime.set_notification_sink(record)
    await runtime.start(
        "com.example.summary",
        "summarize",
        _context(selected=["1"]),
        {},
        **_ids(),
    )

    assert await _wait_terminal(events) == "failed"
    assert events[-1].snapshot["error"]["code"] == "plugin_mutation_rejected"
    assert mutation.plans == []
    assert len(registry.audit) == 1
    assert registry.audit[0].outcome == "failed"
    assert registry.audit[0].error_code == "plugin_mutation_rejected"


@pytest.mark.asyncio
async def test_progress_is_an_execution_report_and_retains_cancellability() -> None:
    class ProgressWorker(RecordingWorker):
        async def run(self, *args: Any, execution: dict[str, Any] | None = None) -> dict[str, Any]:
            assert execution is not None
            assert execution["_hostCancel"].cancelled is False
            execution["_hostCancellable"] = False
            await execution["_hostReporter"].report(done=1, total=2, message="one")
            execution["_hostCancellable"] = True
            await execution["_hostReporter"].report(done=2, total=2, message="two")
            return self.result

    runtime = PluginExecutionRuntime(
        registry=FakeRegistry(_snapshot()),
        worker_adapter=ProgressWorker(
            {"contract": "vibetable.plugin-result.v1", "status": "success", "summary": "done"}
        ),
    )
    events: list[Any] = []

    async def record(event: Any) -> None:
        events.append(event)

    runtime.set_notification_sink(record)
    await runtime.start("com.example.summary", "summarize", _context(), {}, **_ids())
    assert await _wait_terminal(events) == "succeeded"
    reports = [
        event.snapshot["progress"]
        for event in events
        if event.snapshot["state"] == "running" and event.snapshot["progress"]
    ]
    assert [report["current"] for report in reports] == [1, 2]
    assert [report["cancellable"] for report in reports] == [False, True]


@pytest.mark.asyncio
@pytest.mark.parametrize("commit_started", [False, True])
async def test_cancel_at_submission_boundary_settles_without_replay(commit_started: bool) -> None:
    submitted = asyncio.Event()
    calls = 0

    class WaitingMutation:
        async def apply(
            self, plan: Any, *, on_submit: Callable[[], None] | None = None
        ) -> dict[str, Any]:
            nonlocal calls
            calls += 1
            if commit_started:
                assert on_submit is not None
                on_submit()
            submitted.set()
            await asyncio.Future()
            raise AssertionError("unreachable")

    runtime = PluginExecutionRuntime(
        registry=FakeRegistry(_snapshot(risk="write")),
        worker_adapter=RecordingWorker(
            {
                "contract": "vibetable.mutation-plan.v1",
                "collection": "articles",
                "operations": [],
                "preview": {"affectedCount": 0},
            }
        ),
        confirmation_adapter=InMemoryHostConfirmationAdapter(decisions=[True]),
        mutation_adapter=WaitingMutation(),
    )
    events: list[Any] = []

    async def record(event: Any) -> None:
        events.append(event)

    runtime.set_notification_sink(record)
    ids = _ids()
    await runtime.start("com.example.summary", "summarize", _context(), {}, **ids)
    await asyncio.wait_for(submitted.wait(), 1)
    assert await runtime.request_cancel(ids["task_id"])
    assert await _wait_terminal(events) == ("aborted" if commit_started else "cancelled")
    if commit_started:
        assert events[-1].snapshot["error"]["code"] == "plugin_commit_unknown"
    else:
        assert events[-1].snapshot["error"] is None
    assert calls == 1
