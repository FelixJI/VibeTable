"""Closed local-worker plugin platform tests."""

from __future__ import annotations

import asyncio
import json
import shutil
from pathlib import Path
from typing import Any

import pytest

from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_package_lifecycle import PluginPackageInspection
from backend.application.plugin_platform_service import PluginPlatformService
from backend.application.plugin_registry import PluginRegistry
from backend.contracts.plugin import (
    CommandContext,
    InteractionDecision,
    InteractionResolveResult,
    PluginManifest,
)
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
        self.calls: list[tuple[str, str | None]] = []

    def set_notification_sink(self, sink: Any) -> None:
        self.sink = sink

    async def resolve(self, request_id: str, selected_path: str | None) -> bool:
        self.calls.append((request_id, selected_path))
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
    await service.resolve_file(
        request_id="file-1",
        selected_path="C:/safe/selected.csv",
    )

    assert interaction.status == "resolved"
    assert confirmation.calls == [("run-1", "interaction-1", "approved")]
    assert files.calls == [("file-1", "C:/safe/selected.csv")]
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
    revision = store.list_package_revisions("local:default", "com.example.reader")[0]
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
    revisions = store.list_package_revisions(
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

    assert store.list_installations("local:default") == []
    assert (
        store.list_package_revisions(
            "local:default",
            "com.example.reader",
        )
        == []
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
    worker = NodePluginWorkerAdapter(
        store=store,
        profiles={},
        client=object(),
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
        package_lifecycle=LocalPluginPackageLifecycle(tmp_path / "cache"),
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
    await service.set_enabled(
        project_key="local:default",
        plugin_id="com.example.reader",
        enabled=True,
    )

    started = await service.start_action(
        project_key="local:default",
        plugin_id="com.example.reader",
        action_id="read",
        context=CommandContext(project_key="local:default"),
        input_payload={},
    )
    for _ in range(100):
        completed = service.get_task(task_id=started.task_id)
        if completed.state in {"succeeded", "failed", "cancelled"}:
            break
        await asyncio.sleep(0.01)
    else:
        raise AssertionError("installed plugin did not finish")

    assert completed.state == "succeeded"
    assert completed.result is not None
    assert completed.result.summary == "installed package executed"


@pytest.mark.asyncio
async def test_catalog_lifecycle_emits_product_events_and_uninstall_cleans_state(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    events: list[Any] = []

    async def record(event: Any) -> None:
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
    enabled = await service.set_enabled(
        project_key="local:default",
        plugin_id="com.example.reader",
        enabled=True,
    )
    result = await service.uninstall(
        project_key="local:default",
        plugin_id="com.example.reader",
        cleanup_private_settings=True,
    )

    assert enabled.status == "enabled"
    assert result.uninstalled
    assert await service.list_catalog(project_key="local:default") == []
    assert [event.event_type for event in events] == [
        "plugin.catalog.changed",
        "plugin.catalog.changed",
    ]


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
    revisions = store.list_package_revisions("local:default", "com.example.reader")
    assert {item.version: item.state for item in revisions} == {
        "1.0.0": "current",
        "2.0.0": "rollback",
    }
    assert store.list_audit("local:default", "com.example.reader")[-1].event_type == "rollback"
