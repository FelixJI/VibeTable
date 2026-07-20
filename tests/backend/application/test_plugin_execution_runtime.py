from __future__ import annotations

import asyncio

import pytest

from backend.application.flow_binding_manager import FlowBindingManager
from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_registry import PluginRegistry
from backend.application.task_runtime import TaskRuntime
from backend.contracts.plugin import (
    CommandContext,
    ExternalFlowAttestation,
    FlowRequirement,
    InstallPlan,
    PluginAction,
    PluginManifest,
)
from backend.infrastructure.directus_flow import (
    DirectusFlowDefinition,
    InMemoryDirectusFlowAdapter,
)
from backend.infrastructure.plugin_store import InMemoryPluginStore
from backend.infrastructure.plugin_worker import (
    InMemoryBulkMutationAdapter,
    InMemoryHostConfirmationAdapter,
    InMemoryPluginWorkerAdapter,
)


class _InteractionLifecycle:
    def __init__(self) -> None:
        self.events: list[tuple[str, str]] = []

    async def register_run(self, *, run_id: str, plugin_id: str, action_id: str) -> None:
        self.events.append(("register", run_id))

    async def request_cancel(self, run_id: str) -> None:
        self.events.append(("cancel", run_id))

    async def complete_run(self, run_id: str, terminal_hint: str) -> None:
        self.events.append((f"complete:{terminal_hint}", run_id))


@pytest.mark.asyncio
async def test_describe_centralizes_whole_plugin_flow_and_context_availability() -> None:
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid="4b159c64-57e2-4db7-af50-495ad8ddf371",
                trigger="manual",
                status="active",
                operation_keys=(),
                definition={},
            )
        ]
    )
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    requirement = FlowRequirement(
        logical_flow_id="summary",
        ownership="external",
        trigger="manual",
        risk="read",
        contract_version="1.0",
    )
    plan = InstallPlan(
        plan_id="plan-summary",
        project_key="local:default",
        project_revision="r1",
        source_type="package",
        source_location="summary.vtplugin",
        package_hash="sha256:summary",
        manifest=PluginManifest(
            plugin_id="com.example.summary",
            version="1.0.0",
            display_name={"zh-CN": "数据概览"},
            actions=[
                PluginAction(
                    action_id="show-summary",
                    display_name={"zh-CN": "查看概览"},
                    mode="flow",
                    risk="read",
                    invocation="manual",
                    placements=["table.toolbar"],
                    requires={"selection": "one-or-more"},
                    entry_flow="summary",
                )
            ],
        ),
        flow_requirements=[requirement],
    )
    await registry.install(plan)
    runtime = PluginExecutionRuntime(
        registry=registry,
        bindings=bindings,
        tasks=TaskRuntime(),
        flow_adapter=directus,
    )
    context = CommandContext(
        project_key="local:default",
        collection="articles",
        selected_keys=["a-1"],
    )

    before = runtime.describe("com.example.summary", "show-summary", context)
    assert before.available is False
    assert before.reasons == ["flow_unbound:summary"]

    await bindings.bind_external(
        project_key="local:default",
        plugin_id="com.example.summary",
        requirement=requirement,
        directus_uuid="4b159c64-57e2-4db7-af50-495ad8ddf371",
        attestation=ExternalFlowAttestation(),
    )
    await registry.set_enabled(
        project_key="local:default", plugin_id="com.example.summary", enabled=True
    )

    after = runtime.describe("com.example.summary", "show-summary", context)
    assert after.available is True
    assert after.reasons == []


@pytest.mark.asyncio
async def test_flow_action_runs_once_through_task_runtime_with_versioned_payload() -> None:
    store = InMemoryPluginStore()
    flow_uuid = "b41e185f-9c5b-43e4-89a9-86e67881d291"
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid=flow_uuid,
                trigger="manual",
                status="active",
                operation_keys=(),
                definition={},
            )
        ],
        flow_results={
            flow_uuid: {
                "contract": "vibetable.plugin-result.v1",
                "status": "success",
                "summary": "读取了 2 条记录",
                "metrics": [{"label": "记录", "value": 2}],
            }
        },
    )
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    requirement = FlowRequirement(
        logical_flow_id="summary",
        ownership="external",
        trigger="manual",
        risk="read",
        contract_version="1.0",
    )
    action = PluginAction(
        action_id="show-summary",
        display_name={"zh-CN": "查看概览"},
        mode="flow",
        risk="read",
        invocation="manual",
        entry_flow="summary",
    )
    plan = InstallPlan(
        plan_id="plan-summary",
        project_key="local:default",
        project_revision="r1",
        source_type="package",
        source_location="summary.vtplugin",
        package_hash="sha256:summary",
        manifest=PluginManifest(
            plugin_id="com.example.summary",
            version="1.0.0",
            display_name={"zh-CN": "数据概览"},
            actions=[action],
        ),
        flow_requirements=[requirement],
    )
    await registry.install(plan)
    await bindings.bind_external(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        requirement=requirement,
        directus_uuid=flow_uuid,
        attestation=ExternalFlowAttestation(),
    )
    await registry.set_enabled(
        project_key=plan.project_key, plugin_id=plan.manifest.plugin_id, enabled=True
    )
    runtime = PluginExecutionRuntime(
        registry=registry,
        bindings=bindings,
        tasks=TaskRuntime(),
        flow_adapter=directus,
    )
    context = CommandContext(
        project_key="local:default",
        collection="articles",
        selected_keys=["a-1", "a-2"],
    )

    handle = await runtime.start(
        "com.example.summary", "show-summary", context, {"range": "selection"}
    )
    for _ in range(20):
        task = runtime.get_task(handle.task_id)
        if task.state in {"succeeded", "failed", "cancelled", "aborted"}:
            break
        await asyncio.sleep(0)

    assert task.state == "succeeded"
    assert task.result is not None
    assert task.result.summary == "读取了 2 条记录"
    assert len(directus.invocation_log) == 1
    invocation = directus.invocation_log[0]
    assert invocation["flowUuid"] == flow_uuid
    assert invocation["body"]["collection"] == "articles"
    assert invocation["body"]["keys"] == ["a-1", "a-2"]
    payload = invocation["body"]["payload"]
    assert payload["runId"] == handle.run_id
    assert payload["pluginId"] == "com.example.summary"
    assert payload["actionId"] == "show-summary"


@pytest.mark.asyncio
async def test_hybrid_action_uses_fixed_prepare_flow_present_pipeline() -> None:
    trace: list[str] = []
    store = InMemoryPluginStore()
    flow_uuid = "5279c226-254e-439e-895c-40773ad21242"
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid=flow_uuid,
                trigger="manual",
                status="active",
                operation_keys=(),
                definition={},
            )
        ],
        flow_results={flow_uuid: {"rows": 2}},
        trace=trace,
    )
    worker = InMemoryPluginWorkerAdapter(
        prepare_results={"dist/workers/hybrid.js": {"prepared": True}},
        present_results={
            "dist/workers/hybrid.js": {
                "contract": "vibetable.plugin-result.v1",
                "status": "success",
                "summary": "已生成本地报告",
            }
        },
        trace=trace,
    )
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    requirement = FlowRequirement(
        logical_flow_id="aggregate",
        ownership="external",
        trigger="manual",
        risk="read",
        contract_version="1.0",
    )
    action = PluginAction(
        action_id="build-report",
        display_name={"zh-CN": "生成报告"},
        mode="hybrid",
        risk="read",
        entry_flow="aggregate",
        worker_entry="dist/workers/hybrid.js",
    )
    plan = InstallPlan(
        plan_id="plan-hybrid",
        project_key="local:default",
        project_revision="r1",
        source_type="package",
        source_location="hybrid.vtplugin",
        package_hash="sha256:hybrid",
        manifest=PluginManifest(
            plugin_id="com.example.hybrid",
            version="1.0.0",
            display_name={"zh-CN": "混合报告"},
            actions=[action],
        ),
        flow_requirements=[requirement],
    )
    await registry.install(plan)
    await bindings.bind_external(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        requirement=requirement,
        directus_uuid=flow_uuid,
        attestation=ExternalFlowAttestation(),
    )
    await registry.set_enabled(
        project_key=plan.project_key, plugin_id=plan.manifest.plugin_id, enabled=True
    )
    runtime = PluginExecutionRuntime(
        registry=registry,
        bindings=bindings,
        tasks=TaskRuntime(),
        flow_adapter=directus,
        worker_adapter=worker,
    )

    handle = await runtime.start(
        plan.manifest.plugin_id,
        action.action_id,
        CommandContext(project_key=plan.project_key, collection="articles"),
        {"format": "csv"},
    )
    for _ in range(20):
        task = runtime.get_task(handle.task_id)
        if task.state in {"succeeded", "failed", "cancelled", "aborted"}:
            break
        await asyncio.sleep(0)

    assert task.state == "succeeded"
    assert task.result is not None
    assert task.result.summary == "已生成本地报告"
    assert trace == ["worker.prepare", "flow.trigger", "worker.present"]
    assert len(directus.invocation_log) == 1


@pytest.mark.asyncio
async def test_local_write_requires_host_confirmation_before_bulk_mutation() -> None:
    trace: list[str] = []
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    action = PluginAction(
        action_id="normalize-local",
        display_name={"zh-CN": "本地规范化"},
        mode="local",
        risk="write",
        worker_entry="dist/workers/local.js",
    )
    plan = InstallPlan(
        plan_id="plan-local",
        project_key="local:default",
        project_revision="r1",
        source_type="package",
        source_location="local.vtplugin",
        package_hash="sha256:local",
        manifest=PluginManifest(
            plugin_id="com.example.local",
            version="1.0.0",
            display_name={"zh-CN": "本地规范化"},
            actions=[action],
        ),
    )
    await registry.install(plan)
    worker = InMemoryPluginWorkerAdapter(
        run_results={
            action.worker_entry: {
                "contract": "vibetable.mutation-plan.v1",
                "collection": "articles",
                "operations": [
                    {
                        "kind": "update",
                        "primaryKey": "a-1",
                        "values": {"title": "Normalized"},
                    }
                ],
                "preview": {"affectedCount": 1, "warnings": []},
            }
        },
        trace=trace,
    )
    confirmation = InMemoryHostConfirmationAdapter(decisions=[True], trace=trace)
    bulk = InMemoryBulkMutationAdapter(
        result={
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "已更新 1 条记录",
        },
        trace=trace,
    )
    runtime = PluginExecutionRuntime(
        registry=registry,
        bindings=FlowBindingManager(store=store, directus=InMemoryDirectusFlowAdapter()),
        tasks=TaskRuntime(),
        flow_adapter=InMemoryDirectusFlowAdapter(),
        worker_adapter=worker,
        confirmation_adapter=confirmation,
        bulk_mutation_adapter=bulk,
    )

    handle = await runtime.start(
        plan.manifest.plugin_id,
        action.action_id,
        CommandContext(
            project_key=plan.project_key,
            collection="articles",
            selected_keys=["a-1"],
        ),
        {},
    )
    for _ in range(20):
        task = runtime.get_task(handle.task_id)
        if task.state in {"succeeded", "failed", "cancelled", "aborted"}:
            break
        await asyncio.sleep(0)

    assert task.state == "succeeded"
    assert task.result is not None
    assert task.result.summary == "已更新 1 条记录"
    assert trace == ["worker.run", "host.confirm", "bulk.apply"]
    assert worker.executions[0]["projectKey"] == plan.project_key
    assert worker.executions[0]["pluginId"] == plan.manifest.plugin_id
    assert worker.executions[0]["packageHash"] == plan.package_hash
    assert worker.executions[0]["actionId"] == action.action_id
    assert confirmation.previews[0].affected_count == 1
    assert len(bulk.plans) == 1


@pytest.mark.asyncio
async def test_local_cancel_terminates_worker_before_confirmation_or_mutation() -> None:
    started = asyncio.Event()
    release = asyncio.Event()

    class _CancellableWorker:
        available = True

        async def run(self, *_args: object, execution: dict[str, object], **_kwargs: object):
            started.set()
            await release.wait()
            return {
                "contract": "vibetable.mutation-plan.v1",
                "collection": "articles",
                "operations": [{"kind": "update", "primaryKey": "a-1", "values": {}}],
                "preview": {"affectedCount": 1},
            }

        async def cancel(self, _run_id: str) -> bool:
            release.set()
            return True

    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    action = PluginAction(
        action_id="write",
        display_name={"en-US": "Write"},
        mode="local",
        risk="write",
        worker_entry="dist/write.js",
    )
    plan = InstallPlan(
        plan_id="cancel-local",
        project_key="local:default",
        project_revision="r1",
        source_type="package",
        source_location="write.vtplugin",
        package_hash="sha256:write",
        manifest=PluginManifest(
            plugin_id="com.example.write",
            version="1.0.0",
            display_name={"en-US": "Write"},
            actions=[action],
        ),
    )
    await registry.install(plan)
    confirmation = InMemoryHostConfirmationAdapter(decisions=[True])
    bulk = InMemoryBulkMutationAdapter(
        result={
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "should not run",
        }
    )
    runtime = PluginExecutionRuntime(
        registry=registry,
        bindings=FlowBindingManager(store=store, directus=InMemoryDirectusFlowAdapter()),
        tasks=TaskRuntime(),
        flow_adapter=InMemoryDirectusFlowAdapter(),
        worker_adapter=_CancellableWorker(),
        confirmation_adapter=confirmation,
        bulk_mutation_adapter=bulk,
    )
    handle = await runtime.start(
        plan.manifest.plugin_id,
        action.action_id,
        CommandContext(project_key=plan.project_key, collection="articles"),
        {},
    )
    await started.wait()

    cancelled = await runtime.request_cancel(handle.task_id)

    assert cancelled.state == "cancelled"
    assert cancelled.cancel_requested is True
    assert confirmation.previews == []
    assert bulk.plans == []


@pytest.mark.asyncio
async def test_hybrid_cancel_terminates_prepare_before_registering_directus_run() -> None:
    started = asyncio.Event()
    release = asyncio.Event()

    class _SlowHybridWorker:
        available = True

        def __init__(self) -> None:
            self.cancelled: list[str] = []

        async def prepare(self, *_args: object, execution: dict[str, object], **_kwargs: object):
            started.set()
            await release.wait()
            return {"prepared": True}

        async def present(self, *_args: object, **_kwargs: object):
            raise AssertionError("present must not run after prepare cancellation")

        async def cancel(self, run_id: str) -> bool:
            self.cancelled.append(run_id)
            release.set()
            return True

    class _StrictInteractions(_InteractionLifecycle):
        async def request_cancel(self, run_id: str) -> None:
            assert ("register", run_id) in self.events
            await super().request_cancel(run_id)

    store = InMemoryPluginStore()
    flow_uuid = "9ba88ba4-3538-4774-831f-a4aa2ba0fc68"
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid=flow_uuid,
                trigger="manual",
                status="active",
                operation_keys=(),
                definition={},
            )
        ]
    )
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    requirement = FlowRequirement(
        logical_flow_id="aggregate",
        ownership="external",
        trigger="manual",
        risk="read",
        contract_version="1.0",
    )
    action = PluginAction(
        action_id="hybrid",
        display_name={"en-US": "Hybrid"},
        mode="hybrid",
        risk="read",
        entry_flow="aggregate",
        worker_entry="dist/hybrid.js",
    )
    plan = InstallPlan(
        plan_id="cancel-hybrid",
        project_key="local:default",
        project_revision="r1",
        source_type="package",
        source_location="hybrid.vtplugin",
        package_hash="sha256:hybrid-cancel",
        manifest=PluginManifest(
            plugin_id="com.example.hybrid-cancel",
            version="1.0.0",
            display_name={"en-US": "Hybrid"},
            actions=[action],
        ),
        flow_requirements=[requirement],
    )
    await registry.install(plan)
    await bindings.bind_external(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        requirement=requirement,
        directus_uuid=flow_uuid,
        attestation=ExternalFlowAttestation(),
    )
    await registry.set_enabled(
        project_key=plan.project_key, plugin_id=plan.manifest.plugin_id, enabled=True
    )
    worker = _SlowHybridWorker()
    interactions = _StrictInteractions()
    runtime = PluginExecutionRuntime(
        registry=registry,
        bindings=bindings,
        tasks=TaskRuntime(),
        flow_adapter=directus,
        worker_adapter=worker,
        interaction_adapter=interactions,
    )
    handle = await runtime.start(
        plan.manifest.plugin_id,
        action.action_id,
        CommandContext(project_key=plan.project_key, collection="articles"),
        {},
    )
    await started.wait()

    cancelled = await runtime.request_cancel(handle.task_id)

    assert cancelled.state == "cancelled"
    assert worker.cancelled == [handle.run_id]
    assert interactions.events == []
    assert directus.invocation_log == []


@pytest.mark.asyncio
async def test_cancel_request_does_not_falsify_a_flow_that_later_succeeds() -> None:
    gate = asyncio.Event()
    store = InMemoryPluginStore()
    flow_uuid = "db414671-f12e-443a-8df2-296f882a3e55"
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid=flow_uuid,
                trigger="manual",
                status="active",
                operation_keys=(),
                definition={},
            )
        ],
        flow_results={
            flow_uuid: {
                "contract": "vibetable.plugin-result.v1",
                "status": "success",
                "summary": "服务端已完成",
            }
        },
        trigger_gate=gate,
    )
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    requirement = FlowRequirement(
        logical_flow_id="write",
        ownership="external",
        trigger="manual",
        risk="write",
        contract_version="1.0",
    )
    action = PluginAction(
        action_id="write",
        display_name={"zh-CN": "写入"},
        mode="flow",
        risk="write",
        entry_flow="write",
    )
    plan = InstallPlan(
        plan_id="plan-write",
        project_key="local:default",
        project_revision="r1",
        source_type="package",
        source_location="write.vtplugin",
        package_hash="sha256:write",
        manifest=PluginManifest(
            plugin_id="com.example.write",
            version="1.0.0",
            display_name={"zh-CN": "写入"},
            actions=[action],
        ),
        flow_requirements=[requirement],
    )
    await registry.install(plan)
    await bindings.bind_external(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        requirement=requirement,
        directus_uuid=flow_uuid,
        attestation=ExternalFlowAttestation(accepts_unknown_side_effects=True),
    )
    await registry.set_enabled(
        project_key=plan.project_key, plugin_id=plan.manifest.plugin_id, enabled=True
    )
    interaction = _InteractionLifecycle()
    runtime = PluginExecutionRuntime(
        registry=registry,
        bindings=bindings,
        tasks=TaskRuntime(),
        flow_adapter=directus,
        interaction_adapter=interaction,
    )
    handle = await runtime.start(
        plan.manifest.plugin_id,
        action.action_id,
        CommandContext(project_key=plan.project_key, collection="articles"),
        {},
    )
    for _ in range(20):
        if directus.invocation_log:
            break
        await asyncio.sleep(0)

    requested = await runtime.request_cancel(handle.task_id)
    assert requested.cancel_requested is True
    assert requested.state == "running"
    assert interaction.events[:2] == [
        ("register", handle.run_id),
        ("cancel", handle.run_id),
    ]

    gate.set()
    for _ in range(20):
        finished = runtime.get_task(handle.task_id)
        if finished.state in {"succeeded", "failed", "cancelled", "aborted"}:
            break
        await asyncio.sleep(0)

    assert finished.state == "succeeded"
    assert finished.cancel_requested is True
    assert finished.result is not None
    assert finished.result.summary == "服务端已完成"
    assert interaction.events[-1] == ("complete:succeeded", handle.run_id)


@pytest.mark.asyncio
async def test_runtime_validates_action_input_before_task_creation() -> None:
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    action = PluginAction(
        action_id="local-read",
        display_name={"zh-CN": "本地读取"},
        mode="local",
        risk="read",
        worker_entry="dist/read.js",
        input_schema="schemas/input.json",
    )
    await registry.install(
        InstallPlan(
            plan_id="plan-schema",
            project_key="local:default",
            project_revision="r1",
            source_type="package",
            source_location="schema.vtplugin",
            package_hash="sha256:schema",
            manifest=PluginManifest(
                plugin_id="com.example.schema",
                version="1.0.0",
                display_name={"zh-CN": "Schema"},
                actions=[action],
            ),
            schemas={
                "schemas/input.json": {
                    "type": "object",
                    "required": ["limit"],
                    "properties": {"limit": {"type": "integer", "minimum": 1}},
                    "additionalProperties": False,
                }
            },
        )
    )
    runtime = PluginExecutionRuntime(
        registry=registry,
        bindings=FlowBindingManager(store=store, directus=InMemoryDirectusFlowAdapter()),
        tasks=TaskRuntime(),
        flow_adapter=InMemoryDirectusFlowAdapter(),
        worker_adapter=InMemoryPluginWorkerAdapter(),
    )

    with pytest.raises(ValueError, match="input schema"):
        await runtime.start(
            "com.example.schema",
            "local-read",
            CommandContext(project_key="local:default"),
            {"limit": 0, "unexpected": True},
        )
