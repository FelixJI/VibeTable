"""Closed local-worker plugin platform tests."""

from __future__ import annotations

import asyncio
import json
import shutil
import uuid
from pathlib import Path
from typing import Any

import pytest

from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_package_lifecycle import PluginPackageInspection
from backend.application.plugin_platform_service import PluginPlatformService
from backend.application.plugin_registry import PluginRegistry, PluginRegistryError
from backend.contracts.plugin import (
    CommandContext,
    InstallPlan,
    InteractionDecision,
    InteractionResolveResult,
    PluginEventEnvelope,
    PluginManifest,
    PluginPackageRevision,
    PluginSnapshot,
)
from backend.contracts.task import SessionPathGrant
from backend.infrastructure.plugin_package_lifecycle import LocalPluginPackageLifecycle
from backend.infrastructure.plugin_store import InMemoryPluginStore
from backend.infrastructure.plugin_worker import (
    InMemoryPluginWorkerAdapter,
    NodePluginWorkerAdapter,
)


def _write_plugin(root: Path, *, version: str = "1.0.0") -> None:
    (root / "dist" / "workers").mkdir(parents=True)
    (root / "schemas").mkdir()
    (root / "manifest.json").write_text(
        json.dumps(
            {
                "$schema": "vibetable.plugin-manifest.v1",
                "pluginId": "com.example.reader",
                "version": version,
                "displayName": {"en": "Reader"},
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
                        "actionId": "read",
                        "displayName": {"en": "Read"},
                        "mode": "local",
                        "risk": "read",
                        "workerEntry": "dist/workers/read.js",
                        "inputSchema": "schemas/input.json",
                        "outputSchema": "schemas/output.json",
                    }
                ],
                "ui": {"customViews": []},
            }
        ),
        encoding="utf-8",
    )
    (root / "dist" / "workers" / "read.js").write_text(
        """
        export async function run() {
          return {
            contract: "vibetable.plugin-result.v1",
            status: "success",
            summary: "installed package executed",
            warnings: [],
          };
        }
        """,
        encoding="utf-8",
    )
    (root / "schemas" / "input.json").write_text(
        '{"type":"object"}',
        encoding="utf-8",
    )
    (root / "schemas" / "output.json").write_text(
        '{"type":"object"}',
        encoding="utf-8",
    )


def _service(
    store: InMemoryPluginStore,
    *,
    package_cache: Path,
) -> PluginPlatformService:
    registry = PluginRegistry(store=store)
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=InMemoryPluginWorkerAdapter(),
    )
    return PluginPlatformService(
        store=store,
        registry=registry,
        runtime=runtime,
        package_lifecycle=LocalPluginPackageLifecycle(package_cache),
    )


class _Confirmation:
    def __init__(self) -> None:
        self.sink: Any = None
        self.calls: list[tuple[str, str, str]] = []

    def set_notification_sink(self, sink: Any) -> None:
        self.sink = sink

    async def try_resolve(
        self, run_id: str, interaction_id: str, decision: InteractionDecision
    ) -> InteractionResolveResult:
        self.calls.append((run_id, interaction_id, decision))
        return InteractionResolveResult(status="resolved", decision=decision)


class _Files:
    def __init__(self) -> None:
        self.sink: Any = None
        self.calls: list[tuple[str, SessionPathGrant | None]] = []

    def set_notification_sink(self, sink: Any) -> None:
        self.sink = sink

    async def resolve(self, request_id: str, grant: SessionPathGrant | None) -> bool:
        self.calls.append((request_id, grant))
        return True


class InMemoryPluginPackageLifecycle:
    """Test adapter for application-level package lifecycle orchestration."""

    def __init__(self) -> None:
        self._packages: dict[str, PluginPackageInspection] = {}
        self.inspect_calls: list[str] = []
        self.retain_calls: list[tuple[str, str]] = []
        self.discard_calls: list[str] = []

    def add(self, source_location: str, manifest: PluginManifest, package_hash: str) -> None:
        self._packages[source_location] = PluginPackageInspection(
            source_type="local-folder",
            source_location=source_location,
            package_hash=package_hash,
            manifest=manifest,
        )

    def inspect(self, source_location: str) -> PluginPackageInspection:
        self.inspect_calls.append(source_location)
        try:
            return self._packages[source_location]
        except KeyError as exc:
            raise ValueError("package source does not exist") from exc

    def retain(self, *, source_location: str, expected_hash: str) -> str:
        self.retain_calls.append((source_location, expected_hash))
        inspected = self._packages[source_location]
        if inspected.package_hash != expected_hash:
            raise ValueError("retained plugin package hash does not match the plan")
        retained_location = f"memory://{expected_hash}"
        self._packages[retained_location] = PluginPackageInspection(
            source_type="package",
            source_location=retained_location,
            package_hash=expected_hash,
            manifest=inspected.manifest,
        )
        return retained_location

    def retained_location(self, package_hash: str) -> str:
        return f"memory://{package_hash}"

    def is_available(self, retained_location: str) -> bool:
        return retained_location in self._packages

    def discard(self, retained_location: str) -> None:
        self.discard_calls.append(retained_location)
        self._packages.pop(retained_location, None)


@pytest.mark.asyncio
async def test_host_interaction_and_file_resolutions_reach_live_adapters(
    tmp_path: Path,
) -> None:
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=InMemoryPluginWorkerAdapter(),
    )
    confirmation = _Confirmation()
    files = _Files()
    service = PluginPlatformService(
        store=store,
        registry=registry,
        runtime=runtime,
        package_lifecycle=LocalPluginPackageLifecycle(tmp_path / "cache"),
        confirmation_adapter=confirmation,
        file_adapter=files,
    )

    async def sink(_event: Any) -> None:
        return None

    service.set_notification_sink(sink)
    interaction = await service.resolve_interaction(
        run_id="run-1",
        interaction_id="interaction-1",
        decision="approved",
    )
    grant = SessionPathGrant(
        grant_id="native-grant",
        purpose="import_source",
        direction="read",
        display_name="selected.csv",
        expires_at=9999999999,
    )
    await service.resolve_file(request_id="file-1", grant=grant)

    assert interaction.status == "resolved"
    assert confirmation.calls == [("run-1", "interaction-1", "approved")]
    assert files.calls == [("file-1", grant)]
    assert confirmation.sink is not None
    assert files.sink is not None


@pytest.mark.asyncio
async def test_install_and_uninstall_use_package_lifecycle_tasks() -> None:
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=InMemoryPluginWorkerAdapter(),
    )
    packages = InMemoryPluginPackageLifecycle()
    source_location = "memory://reader-source"
    package_hash = f"sha256:{'1' * 64}"
    manifest = PluginManifest.model_validate(
        {
            "$schema": "vibetable.plugin-manifest.v1",
            "pluginId": "com.example.reader",
            "version": "1.0.0",
            "displayName": {"en": "Reader"},
            "compatibility": {"minHostVersion": "1.0.0", "pluginApi": "1.x"},
            "permissions": {"data": [], "files": [], "privateStorage": False},
            "actions": [
                {
                    "actionId": "read",
                    "displayName": {"en": "Read"},
                    "mode": "local",
                    "risk": "read",
                    "workerEntry": "dist/workers/read.js",
                }
            ],
            "ui": {"customViews": []},
        }
    )
    packages.add(source_location, manifest, package_hash)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        runtime=runtime,
        package_lifecycle=packages,
    )

    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=source_location,
    )
    await service.commit_install(
        plan_id=plan.plan_id,
        project_revision="project-r1",
    )
    retained_location = f"memory://{package_hash}"
    revisions = await store.list_package_revisions("local:default", "com.example.reader")
    revision = revisions[0]
    await service.uninstall(
        project_key="local:default",
        plugin_id="com.example.reader",
        cleanup_private_settings=True,
    )

    assert packages.inspect_calls == [source_location, source_location]
    assert packages.retain_calls == [(source_location, package_hash)]
    assert revision.local_path == retained_location
    assert packages.discard_calls == [retained_location]


@pytest.mark.asyncio
async def test_inspect_and_commit_recheck_and_retain_immutable_package(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )
    installed = await service.commit_install(
        plan_id=plan.plan_id,
        project_revision="project-r1",
    )

    assert installed.status == "disabled"
    revisions = await store.list_package_revisions(
        "local:default",
        "com.example.reader",
    )
    assert len(revisions) == 1
    assert revisions[0].state == "current"
    assert revisions[0].package_hash == plan.package_hash
    assert Path(revisions[0].local_path).is_file()
    assert Path(revisions[0].local_path).is_relative_to(tmp_path / "cache")
    assert Path(revisions[0].local_path).parent == tmp_path / "cache"


@pytest.mark.asyncio
async def test_commit_rejects_source_changed_after_inspection(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )
    (source / "schemas" / "input.json").write_text(
        '{"type":"array"}',
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="changed"):
        await service.commit_install(
            plan_id=plan.plan_id,
            project_revision="project-r1",
        )

    assert service.cancel_install(plan_id=plan.plan_id) is False
    with pytest.raises(ValueError, match="not found"):
        await service.commit_install(
            plan_id=plan.plan_id,
            project_revision="project-r1",
        )

    assert await store.list_installations("local:default") == []
    assert (
        await store.list_package_revisions(
            "local:default",
            "com.example.reader",
        )
        == []
    )


@pytest.mark.asyncio
async def test_duplicate_commit_fails_closed_without_local_compensation(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    first = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )
    await service.commit_install(
        plan_id=first.plan_id,
        project_revision="project-r1",
    )
    first_revisions = await store.list_package_revisions(
        "local:default",
        "com.example.reader",
    )
    retained = first_revisions[0].local_path
    second = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )

    with pytest.raises(PluginRegistryError) as error:
        await service.commit_install(
            plan_id=second.plan_id,
            project_revision="project-r1",
        )

    assert error.value.code == "plugin_already_installed"
    # One atomic commit keeps the first installation and its package.
    installations = await store.list_installations("local:default")
    assert len(installations) == 1
    assert installations[0].plugin_id == "com.example.reader"
    assert Path(retained).is_file()


@pytest.mark.asyncio
async def test_cancel_install_discards_the_pending_plan(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )

    assert service.cancel_install(plan_id=plan.plan_id) is True
    assert service.cancel_install(plan_id=plan.plan_id) is False
    with pytest.raises(ValueError, match="not found"):
        await service.commit_install(
            plan_id=plan.plan_id,
            project_revision="project-r1",
        )


@pytest.mark.asyncio
async def test_committed_installation_executes_from_retained_current_revision(
    tmp_path: Path,
) -> None:
    if shutil.which("node") is None:
        pytest.skip("Node.js is not installed")
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    package_lifecycle = LocalPluginPackageLifecycle(tmp_path / "cache")
    worker = NodePluginWorkerAdapter(
        store=store,
        profiles={},
        client=object(),
        package_lifecycle=package_lifecycle,
        timeout_seconds=3,
    )
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=worker,
    )
    service = PluginPlatformService(
        store=store,
        registry=registry,
        runtime=runtime,
        package_lifecycle=package_lifecycle,
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )
    await service.commit_install(
        plan_id=plan.plan_id,
        project_revision="project-r1",
    )
    await registry.set_enabled(
        "local:default",
        "com.example.reader",
        True,
    )

    task_states: list[str] = []

    async def record(event: PluginEventEnvelope) -> None:
        if event.event_type == "plugin.task.changed":
            task_states.append(str(event.snapshot["state"]))

    service.set_notification_sink(record)
    started = await service.start_action(
        project_key="local:default",
        plugin_id="com.example.reader",
        action_id="read",
        context=CommandContext(project_key="local:default"),
        input_payload={},
        task_id=f"plugin-task-{uuid.uuid4().hex[:12]}",
        run_id=f"plugin-run-{uuid.uuid4().hex[:12]}",
    )
    for _ in range(100):
        if task_states and task_states[-1] in {"succeeded", "failed", "cancelled"}:
            break
        await asyncio.sleep(0.01)
    else:
        raise AssertionError("installed plugin did not finish")

    assert task_states[-1] == "succeeded"
    assert started.task_id.startswith("plugin-task-")


@pytest.mark.asyncio
async def test_install_commit_does_not_emit_a_second_catalog_event(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    service = _service(store, package_cache=tmp_path / "cache")
    events: list[PluginEventEnvelope] = []

    async def record(event: PluginEventEnvelope) -> None:
        events.append(event)

    service.set_notification_sink(record)
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )
    await service.commit_install(
        plan_id=plan.plan_id,
        project_revision="project-r1",
    )
    enabled = await registry.set_enabled(
        "local:default",
        "com.example.reader",
        True,
    )
    result = await service.uninstall(
        project_key="local:default",
        plugin_id="com.example.reader",
        cleanup_private_settings=True,
    )

    assert enabled.status == "enabled"
    assert result.uninstalled
    # The durable catalog change for install/enable is emitted by the Go
    # catalog; Python must not send a second event for the same commit.
    assert events == []
    assert await registry.list("local:default") == []


@pytest.mark.asyncio
async def test_upgrade_retains_previous_package_and_rollback_restores_it(
    tmp_path: Path,
) -> None:
    source_v1 = tmp_path / "reader-v1"
    source_v2 = tmp_path / "reader-v2"
    _write_plugin(source_v1, version="1.0.0")
    _write_plugin(source_v2, version="2.0.0")
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")

    first = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source_v1),
    )
    await service.commit_install(
        plan_id=first.plan_id,
        project_revision="project-r1",
    )
    second = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r2",
        source_location=str(source_v2),
    )
    upgraded = await service.upgrade(
        project_key="local:default",
        plugin_id="com.example.reader",
        plan_id=second.plan_id,
        project_revision="project-r2",
    )
    rolled_back = await service.rollback(
        project_key="local:default",
        plugin_id="com.example.reader",
    )

    assert upgraded.version == "2.0.0"
    assert rolled_back.version == "1.0.0"
    revisions = await store.list_package_revisions("local:default", "com.example.reader")
    assert {item.version: item.state for item in revisions} == {
        "1.0.0": "current",
        "2.0.0": "rollback",
    }
    audit = await store.list_audit("local:default", "com.example.reader")
    assert audit[-1].event_type == "rollback"


@pytest.mark.asyncio
async def test_hidden_upgrade_does_not_duplicate_the_go_catalog_event(
    tmp_path: Path,
) -> None:
    source_v1 = tmp_path / "reader-v1"
    source_v2 = tmp_path / "reader-v2"
    _write_plugin(source_v1, version="1.0.0")
    _write_plugin(source_v2, version="2.0.0")
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    events: list[PluginEventEnvelope] = []

    async def record(event: PluginEventEnvelope) -> None:
        events.append(event)

    service.set_notification_sink(record)
    first = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source_v1),
    )
    await service.commit_install(
        plan_id=first.plan_id,
        project_revision="project-r1",
    )
    second = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r2",
        source_location=str(source_v2),
    )
    await service.upgrade(
        project_key="local:default",
        plugin_id="com.example.reader",
        plan_id=second.plan_id,
        project_revision="project-r2",
    )

    assert events == []


class _CommitCleanupProbeStore(InMemoryPluginStore):
    """Fails the atomic commit and the reference query to probe cleanup."""

    async def commit_install(
        self, plan: InstallPlan, *, package_revision: PluginPackageRevision
    ) -> PluginSnapshot:
        raise PluginRegistryError("plugin is already installed", code="plugin_already_installed")

    async def is_package_path_referenced(self, local_path: str) -> bool:
        raise RuntimeError("shared catalog is unavailable")


@pytest.mark.asyncio
async def test_commit_failure_keeps_original_error_and_retained_package(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = _CommitCleanupProbeStore()
    registry = PluginRegistry(store=store)
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=InMemoryPluginWorkerAdapter(),
    )
    service = PluginPlatformService(
        store=store,
        registry=registry,
        runtime=runtime,
        package_lifecycle=LocalPluginPackageLifecycle(tmp_path / "cache"),
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )

    with pytest.raises(PluginRegistryError) as error:
        await service.commit_install(
            plan_id=plan.plan_id,
            project_revision="project-r1",
        )

    # The original commit error survives; the failing reference query never
    # masks it, and the retained package stays cached as the safest outcome.
    assert error.value.code == "plugin_already_installed"
    cached = list((tmp_path / "cache").glob("*.vtplugin"))
    assert len(cached) == 1


@pytest.mark.asyncio
async def test_upgrade_identity_failure_consumes_plan_once(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    service = _service(InMemoryPluginStore(), package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )

    with pytest.raises(ValueError, match="identity"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.other",
            plan_id=plan.plan_id,
            project_revision="project-r1",
        )
    with pytest.raises(ValueError, match="not found"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.reader",
            plan_id=plan.plan_id,
            project_revision="project-r1",
        )


@pytest.mark.asyncio
async def test_upgrade_source_failure_consumes_plan_once(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    service = _service(InMemoryPluginStore(), package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )
    (source / "schemas" / "input.json").write_text('{"type":"array"}', encoding="utf-8")

    with pytest.raises(ValueError, match="changed"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.reader",
            plan_id=plan.plan_id,
            project_revision="project-r1",
        )
    with pytest.raises(ValueError, match="not found"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.reader",
            plan_id=plan.plan_id,
            project_revision="project-r1",
        )
