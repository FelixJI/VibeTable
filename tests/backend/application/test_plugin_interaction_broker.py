from __future__ import annotations

import asyncio

import pytest

from backend.application.plugin_interaction_broker import PluginInteractionBroker
from backend.contracts.plugin import ConfirmationPreview
from backend.infrastructure.plugin_interaction import (
    HostConfirmationAdapter,
    InMemoryInteractionAdapter,
)


@pytest.mark.asyncio
async def test_confirmation_is_observed_resolved_once_and_repeated_idempotently() -> None:
    adapter = InMemoryInteractionAdapter()
    broker = PluginInteractionBroker(adapter=adapter)
    adapter.open_run(
        run_id="run-1",
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        action_id="normalize-selection",
        caller="user-1",
    )
    flow_waiter = asyncio.create_task(
        adapter.request_confirmation(
            run_id="run-1",
            risk="write",
            title="即将更新 2 条记录",
            preview=ConfirmationPreview(affected_count=2),
            timeout_ms=300_000,
        )
    )

    snapshot = await broker.watch("run-1")
    assert snapshot.pending_confirmation is not None
    interaction_id = snapshot.pending_confirmation.interaction_id

    first = await broker.resolve("run-1", interaction_id, "approved")
    second = await broker.resolve("run-1", interaction_id, "approved")

    assert first.status == "resolved"
    assert second.status == "already-resolved"
    assert await flow_waiter is True


@pytest.mark.asyncio
async def test_progress_is_monotonic_and_returns_cooperative_cancel_flag() -> None:
    adapter = InMemoryInteractionAdapter()
    broker = PluginInteractionBroker(adapter=adapter)
    adapter.open_run(
        run_id="run-2",
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        action_id="normalize-selection",
        caller="user-1",
    )

    first = await adapter.report_progress(
        "run-2", current=40, total=100, message="正在处理", cancellable=True
    )
    requested = await broker.request_cancel("run-2")
    second = await adapter.report_progress(
        "run-2", current=60, total=100, message="正在完成当前批次", cancellable=True
    )

    assert first.cancel_requested is False
    assert requested.cancel_requested is True
    assert second.cancel_requested is True
    snapshot = await broker.get("run-2")
    assert snapshot.progress is not None
    assert snapshot.progress.current == 60
    assert snapshot.progress.total == 100

    with pytest.raises(ValueError, match="monotonic"):
        await adapter.report_progress(
            "run-2", current=59, total=100, message="倒退", cancellable=True
        )


@pytest.mark.asyncio
async def test_local_confirmation_is_published_and_resolved_by_host() -> None:
    published = asyncio.Event()
    envelopes = []
    adapter = HostConfirmationAdapter(timeout_seconds=1)

    async def capture(envelope):
        envelopes.append(envelope)
        published.set()

    adapter.set_notification_sink(capture)
    confirmation = asyncio.create_task(
        adapter.confirm(
            ConfirmationPreview(affected_count=3),
            "write",
            execution={
                "runId": "run-local",
                "projectKey": "local:default",
                "pluginId": "com.example.local",
                "actionId": "mutate",
            },
        )
    )
    await asyncio.wait_for(published.wait(), timeout=1)
    snapshot = envelopes[0].snapshot

    result = await adapter.try_resolve(
        "run-local",
        snapshot["pendingConfirmation"]["interactionId"],
        "approved",
    )

    assert result is not None
    assert result.status == "resolved"
    assert await confirmation is True
    assert envelopes[0].event_type == "plugin.interaction.requested"
    assert envelopes[0].revision == 2
