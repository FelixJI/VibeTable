from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.application.flow_binding_manager import FlowBindingManager
from backend.application.plugin_platform_service import PluginPlatformService
from backend.application.plugin_registry import PluginRegistry, PluginRegistryError
from backend.contracts.plugin import (
    CommandContext,
    InteractionResolveResult,
    PluginSafeError,
    PluginTaskSnapshot,
)
from backend.infrastructure.directus_flow import (
    DirectusFlowDefinition,
    InMemoryDirectusFlowAdapter,
)
from backend.infrastructure.plugin_package import pack_plugin
from backend.infrastructure.plugin_store import InMemoryPluginStore


def _write_plugin(root: Path, *, version: str = "1.0.0") -> None:
    (root / "flows").mkdir(parents=True)
    (root / "schemas").mkdir()
    (root / "manifest.json").write_text(
        json.dumps(
            {
                "$schema": "vibetable.plugin-manifest.v1",
                "pluginId": "com.example.reader",
                "version": version,
                "displayName": {"zh-CN": "读取器"},
                "compatibility": {
                    "minHostVersion": "1.0.0",
                    "pluginApi": "1.x",
                    "directus": ">=12.1 <13",
                },
                "permissions": {"data": [], "files": [], "privateStorage": False},
                "actions": [
                    {
                        "actionId": "read",
                        "displayName": {"zh-CN": "读取"},
                        "mode": "flow",
                        "risk": "read",
                        "entryFlow": "read",
                        "inputSchema": "schemas/input.json",
                        "outputSchema": "schemas/output.json",
                    }
                ],
                "flows": [
                    {
                        "logicalFlowId": "read",
                        "ownership": "managed",
                        "trigger": "manual",
                        "risk": "read",
                        "definition": "flows/read.json",
                        "inputSchema": "schemas/input.json",
                        "outputSchema": "schemas/output.json",
                        "requiresOperations": [],
                    }
                ],
                "ui": {"customViews": []},
            }
        ),
        encoding="utf-8",
    )
    (root / "flows" / "read.json").write_text(
        json.dumps({"name": "Read", "operations": []}), encoding="utf-8"
    )
    (root / "schemas" / "input.json").write_text(
        json.dumps({"type": "object", "required": ["limit"]}), encoding="utf-8"
    )
    (root / "schemas" / "output.json").write_text(json.dumps({"type": "object"}), encoding="utf-8")


def _write_plugin_with_flows(root: Path, *, version: str, flow_ids: tuple[str, ...]) -> None:
    _write_plugin(root, version=version)
    manifest_path = root / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["actions"][0]["entryFlow"] = flow_ids[0]
    manifest["flows"] = []
    for logical_id in flow_ids:
        definition_path = root / "flows" / f"{logical_id}.json"
        definition_path.write_text(
            json.dumps({"name": f"Flow {logical_id} {version}", "operations": []}),
            encoding="utf-8",
        )
        manifest["flows"].append(
            {
                "logicalFlowId": logical_id,
                "ownership": "managed",
                "trigger": "manual",
                "risk": "read",
                "definition": f"flows/{logical_id}.json",
                "inputSchema": "schemas/input.json",
                "outputSchema": "schemas/output.json",
                "requiresOperations": [],
            }
        )
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")


def _write_automatic_plugin(root: Path, *, version: str = "1.0.0") -> None:
    _write_plugin(root, version=version)
    manifest_path = root / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["actions"] = []
    manifest["flows"][0]["trigger"] = "schedule"
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")


def _write_automatic_plugin_with_flows(root: Path, *, flow_ids: tuple[str, ...]) -> None:
    _write_plugin_with_flows(root, version="1.0.0", flow_ids=flow_ids)
    manifest_path = root / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["actions"] = []
    for flow in manifest["flows"]:
        flow["trigger"] = "schedule"
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")


def _write_blocked_automatic_plugin(root: Path, *, version: str = "1.0.0") -> None:
    _write_automatic_plugin(root, version=version)
    manifest_path = root / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    manifest["flows"].append(
        {
            "logicalFlowId": "external-gate",
            "ownership": "external",
            "trigger": "manual",
            "risk": "read",
            "inputSchema": "schemas/input.json",
            "outputSchema": "schemas/output.json",
            "requiresOperations": [],
        }
    )
    manifest_path.write_text(json.dumps(manifest), encoding="utf-8")


@pytest.mark.asyncio
async def test_inspect_and_commit_rechecks_package_and_provisions_managed_flow(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )

    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
    )
    installed = await service.commit_install(plan_id=plan.plan_id, project_revision="project-r1")

    assert installed.status == "enabled"
    assert installed.schemas["schemas/input.json"]["required"] == ["limit"]
    binding = bindings.resolve("local:default", "com.example.reader", "read")
    assert binding is not None
    assert binding.ownership == "managed"
    revisions = store.list_package_revisions("local:default", "com.example.reader")
    assert len(revisions) == 1
    assert revisions[0].state == "current"
    assert Path(revisions[0].local_path).is_file()
    assert [
        event.event_type for event in store.list_audit("local:default", "com.example.reader")
    ] == [
        "install.inspect",
        "install.commit",
    ]


@pytest.mark.asyncio
async def test_commit_rejects_source_changed_after_inspection(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(source),
    )
    (source / "schemas" / "input.json").write_text('{"type":"array"}', encoding="utf-8")

    with pytest.raises(ValueError, match="changed"):
        await service.commit_install(plan_id=plan.plan_id, project_revision="r1")


@pytest.mark.asyncio
async def test_whole_plugin_enable_disable_controls_managed_automatic_flows(
    tmp_path: Path,
) -> None:
    source = tmp_path / "automatic"
    _write_automatic_plugin(source)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(source)
    )
    installed = await service.commit_install(plan_id=plan.plan_id, project_revision="r1")
    flow_uuid = installed.flow_bindings[0].directus_flow_uuid

    disabled = await service.set_enabled(
        project_key="local:default", plugin_id="com.example.reader", enabled=False
    )
    assert disabled.status == "disabled"
    assert (await directus.read_flow(flow_uuid)).status == "inactive"  # type: ignore[union-attr]

    enabled = await service.set_enabled(
        project_key="local:default", plugin_id="com.example.reader", enabled=True
    )
    assert enabled.status == "enabled"
    assert (await directus.read_flow(flow_uuid)).status == "active"  # type: ignore[union-attr]


@pytest.mark.asyncio
async def test_disable_compensates_registry_when_automatic_flow_change_fails(
    tmp_path: Path,
) -> None:
    source = tmp_path / "automatic"
    _write_automatic_plugin(source)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(source)
    )
    installed = await service.commit_install(plan_id=plan.plan_id, project_revision="r1")
    directus.fail_on.add("deactivate")

    with pytest.raises(RuntimeError, match="deactivate"):
        await service.set_enabled(
            project_key="local:default", plugin_id="com.example.reader", enabled=False
        )

    current = registry.get("local:default", "com.example.reader")
    assert current is not None
    assert current.status == "enabled"
    assert (await directus.read_flow(installed.flow_bindings[0].directus_flow_uuid)).status == (
        "active"
    )


@pytest.mark.asyncio
async def test_blocked_install_never_leaves_managed_automatic_flow_running(
    tmp_path: Path,
) -> None:
    source = tmp_path / "blocked-automatic"
    _write_blocked_automatic_plugin(source)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(source)
    )

    installed = await service.commit_install(plan_id=plan.plan_id, project_revision="r1")

    automatic = next(item for item in installed.flow_bindings if item.logical_flow_id == "read")
    assert installed.status == "disabled"
    assert ("activate", automatic.directus_flow_uuid) not in directus.mutation_log
    assert (await directus.read_flow(automatic.directus_flow_uuid)).status == "inactive"  # type: ignore[union-attr]

    directus.mutation_log.clear()
    with pytest.raises(PluginRegistryError, match="cannot be enabled"):
        await service.set_enabled(
            project_key="local:default", plugin_id="com.example.reader", enabled=True
        )
    assert ("activate", automatic.directus_flow_uuid) not in directus.mutation_log


@pytest.mark.asyncio
async def test_upgrade_keeps_user_disabled_automatic_flow_inactive(tmp_path: Path) -> None:
    first = tmp_path / "automatic-v1"
    second = tmp_path / "automatic-v2"
    _write_automatic_plugin(first, version="1.0.0")
    _write_automatic_plugin(second, version="2.0.0")
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    first_plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(first)
    )
    await service.commit_install(plan_id=first_plan.plan_id, project_revision="r1")
    await service.set_enabled(
        project_key="local:default", plugin_id="com.example.reader", enabled=False
    )
    second_plan = await service.inspect_install(
        project_key="local:default", project_revision="r2", source_location=str(second)
    )

    upgraded = await service.upgrade(
        project_key="local:default",
        plugin_id="com.example.reader",
        plan_id=second_plan.plan_id,
        project_revision="r2",
    )

    flow = await directus.read_flow(upgraded.flow_bindings[0].directus_flow_uuid)
    assert upgraded.status == "disabled"
    assert upgraded.disabled_reason == "user_disabled"
    assert flow is not None
    assert flow.status == "inactive"
    assert ("activate", upgraded.flow_bindings[0].directus_flow_uuid) not in (directus.mutation_log)


@pytest.mark.asyncio
async def test_upgrade_and_rollback_keep_blocked_automatic_flows_inactive(
    tmp_path: Path,
) -> None:
    first = tmp_path / "blocked-v1"
    second = tmp_path / "blocked-v2"
    _write_blocked_automatic_plugin(first, version="1.0.0")
    _write_blocked_automatic_plugin(second, version="2.0.0")
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    first_plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(first)
    )
    installed = await service.commit_install(plan_id=first_plan.plan_id, project_revision="r1")
    assert installed.status == "disabled"
    second_plan = await service.inspect_install(
        project_key="local:default", project_revision="r2", source_location=str(second)
    )

    upgraded = await service.upgrade(
        project_key="local:default",
        plugin_id="com.example.reader",
        plan_id=second_plan.plan_id,
        project_revision="r2",
    )
    upgraded_auto = next(item for item in upgraded.flow_bindings if item.logical_flow_id == "read")
    assert upgraded.status == "disabled"
    assert (await directus.read_flow(upgraded_auto.directus_flow_uuid)).status == "inactive"  # type: ignore[union-attr]

    rolled_back = await service.rollback(
        project_key="local:default", plugin_id="com.example.reader"
    )
    rollback_auto = next(
        item for item in rolled_back.flow_bindings if item.logical_flow_id == "read"
    )
    assert rolled_back.status == "disabled"
    assert (await directus.read_flow(rollback_auto.directus_flow_uuid)).status == "inactive"  # type: ignore[union-attr]


@pytest.mark.asyncio
async def test_upgrade_to_blocked_stops_automatic_flow_and_healthy_rollback_restarts_it(
    tmp_path: Path,
) -> None:
    first = tmp_path / "healthy-v1"
    second = tmp_path / "blocked-v2"
    _write_automatic_plugin(first, version="1.0.0")
    _write_blocked_automatic_plugin(second, version="2.0.0")
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    first_plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(first)
    )
    installed = await service.commit_install(plan_id=first_plan.plan_id, project_revision="r1")
    assert installed.status == "enabled"
    second_plan = await service.inspect_install(
        project_key="local:default", project_revision="r2", source_location=str(second)
    )

    upgraded = await service.upgrade(
        project_key="local:default",
        plugin_id="com.example.reader",
        plan_id=second_plan.plan_id,
        project_revision="r2",
    )
    upgraded_auto = next(item for item in upgraded.flow_bindings if item.logical_flow_id == "read")
    assert upgraded.status == "disabled"
    assert (await directus.read_flow(upgraded_auto.directus_flow_uuid)).status == "inactive"  # type: ignore[union-attr]

    rolled_back = await service.rollback(
        project_key="local:default", plugin_id="com.example.reader"
    )
    rollback_auto = next(
        item for item in rolled_back.flow_bindings if item.logical_flow_id == "read"
    )
    assert rolled_back.status == "enabled"
    assert (await directus.read_flow(rollback_auto.directus_flow_uuid)).status == "active"  # type: ignore[union-attr]


@pytest.mark.asyncio
async def test_plan_is_rejected_after_plugin_project_state_changes(tmp_path: Path) -> None:
    first = tmp_path / "reader-v1"
    second = tmp_path / "reader-v2"
    _write_plugin(first, version="1.0.0")
    _write_plugin(second, version="2.0.0")
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    first_plan = await service.inspect_install(
        project_key="local:default", project_revision="workspace-r1", source_location=str(first)
    )
    await service.commit_install(
        plan_id=first_plan.plan_id, project_revision=first_plan.project_revision
    )
    upgrade_plan = await service.inspect_install(
        project_key="local:default", project_revision="workspace-r1", source_location=str(second)
    )
    await service.set_enabled(
        project_key="local:default", plugin_id="com.example.reader", enabled=False
    )

    with pytest.raises(ValueError, match="project changed"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.reader",
            plan_id=upgrade_plan.plan_id,
            project_revision=upgrade_plan.project_revision,
        )


@pytest.mark.asyncio
async def test_catalog_refresh_revalidates_external_flow_and_stops_automatic_flow(
    tmp_path: Path,
) -> None:
    source = tmp_path / "blocked-automatic"
    _write_blocked_automatic_plugin(source)
    external_uuid = "external-gate-flow"
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid=external_uuid,
                trigger="manual",
                status="active",
                operation_keys=(),
                definition={"name": "External gate"},
            )
        ]
    )
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(source)
    )
    await service.commit_install(plan_id=plan.plan_id, project_revision=plan.project_revision)
    await service.bind_external_flow(
        project_key="local:default",
        plugin_id="com.example.reader",
        logical_flow_id="external-gate",
        directus_flow_uuid=external_uuid,
        accepts_unknown_side_effects=False,
    )
    automatic = bindings.resolve("local:default", "com.example.reader", "read")
    assert automatic is not None
    assert (await directus.read_flow(automatic.directus_flow_uuid)).status == "active"  # type: ignore[union-attr]
    await directus.delete_flow(external_uuid)

    refreshed = (await service.list_catalog(project_key="local:default"))[0]

    assert refreshed.status == "disabled"
    assert "flow_invalid:external-gate" in refreshed.blocking_reasons
    assert (await directus.read_flow(automatic.directus_flow_uuid)).status == "inactive"  # type: ignore[union-attr]


@pytest.mark.asyncio
async def test_missing_managed_flow_stops_every_other_automatic_flow(tmp_path: Path) -> None:
    source = tmp_path / "two-automatic-flows"
    _write_automatic_plugin_with_flows(source, flow_ids=("missing", "survivor"))
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(source)
    )
    await service.commit_install(plan_id=plan.plan_id, project_revision=plan.project_revision)
    missing = bindings.resolve("local:default", "com.example.reader", "missing")
    survivor = bindings.resolve("local:default", "com.example.reader", "survivor")
    assert missing is not None
    assert survivor is not None
    await directus.delete_flow(missing.directus_flow_uuid)

    refreshed = (await service.list_catalog(project_key="local:default"))[0]

    assert refreshed.status == "disabled"
    assert "flow_invalid:missing" in refreshed.blocking_reasons
    assert (await directus.read_flow(survivor.directus_flow_uuid)).status == "inactive"  # type: ignore[union-attr]

    restored = await service.resolve_drift(
        project_key="local:default",
        plugin_id="com.example.reader",
        logical_flow_id="missing",
        strategy="restore",
    )
    restored_missing = bindings.resolve("local:default", "com.example.reader", "missing")
    assert restored_missing is not None
    assert restored.status == "enabled"
    assert (await directus.read_flow(restored_missing.directus_flow_uuid)).status == "active"  # type: ignore[union-attr]
    assert (await directus.read_flow(survivor.directus_flow_uuid)).status == "active"  # type: ignore[union-attr]


@pytest.mark.asyncio
async def test_local_folder_change_is_persisted_and_emits_catalog_event(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    events: list[object] = []
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )

    async def collect_event(event: object) -> None:
        events.append(event)

    service.set_notification_sink(collect_event)
    try:
        plan = await service.inspect_install(
            project_key="local:default", project_revision="r1", source_location=str(source)
        )
        installed = await service.commit_install(
            plan_id=plan.plan_id, project_revision=plan.project_revision
        )
        assert installed.source_changed is False
        (source / "schemas" / "input.json").write_text(
            '{"type":"object","properties":{"changed":{"type":"boolean"}}}',
            encoding="utf-8",
        )

        refreshed = (await service.list_catalog(project_key="local:default"))[0]

        assert refreshed.source_changed is True
        catalog_events = [
            event
            for event in events
            if getattr(event, "event_type", None) == "plugin.catalog.changed"
        ]
        assert len(catalog_events) >= 2
    finally:
        await service.close()


@pytest.mark.asyncio
async def test_package_installation_uses_retained_revision_after_commit(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    package = tmp_path / "reader.vtplugin"
    _write_plugin(source)
    pack_plugin(source, package)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )

    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(package),
    )
    installed = await service.commit_install(plan_id=plan.plan_id, project_revision="r1")

    assert installed.source_type == "package"
    assert installed.source_location != str(package)
    assert Path(installed.source_location).is_file()
    revision = store.list_package_revisions("local:default", "com.example.reader")[0]
    assert installed.source_location == revision.local_path


@pytest.mark.asyncio
async def test_local_folder_installation_serves_immutable_retained_archive(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(source),
    )
    installed = await service.commit_install(plan_id=plan.plan_id, project_revision="r1")

    assert installed.source_type == "local-folder"
    assert Path(installed.source_location).is_file()
    assert Path(installed.source_location).suffix == ".vtplugin"
    (source / "manifest.json").write_text("{}", encoding="utf-8")
    assert Path(installed.source_location).read_bytes().startswith(b"PK")


@pytest.mark.asyncio
async def test_upgrade_keeps_only_current_and_previous_retained_packages(tmp_path: Path) -> None:
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    retained_paths: list[Path] = []
    for index, version in enumerate(("1.0.0", "2.0.0", "3.0.0"), start=1):
        source = tmp_path / f"reader-{index}"
        package = tmp_path / f"reader-{index}.vtplugin"
        _write_plugin(source, version=version)
        pack_plugin(source, package)
        plan = await service.inspect_install(
            project_key="local:default",
            project_revision=f"r{index}",
            source_location=str(package),
        )
        snapshot = (
            await service.commit_install(plan_id=plan.plan_id, project_revision=f"r{index}")
            if index == 1
            else await service.upgrade(
                project_key="local:default",
                plugin_id="com.example.reader",
                plan_id=plan.plan_id,
                project_revision=f"r{index}",
            )
        )
        retained_paths.append(Path(snapshot.source_location))

    revisions = store.list_package_revisions("local:default", "com.example.reader")
    assert [(item.version, item.state) for item in revisions] == [
        ("2.0.0", "rollback"),
        ("3.0.0", "current"),
    ]
    assert retained_paths[0].exists() is False
    assert all(path.exists() for path in retained_paths[1:])


@pytest.mark.asyncio
async def test_upgrade_rollback_restores_removed_and_removes_added_managed_flows(
    tmp_path: Path,
) -> None:
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    first = tmp_path / "reader-v1"
    second = tmp_path / "reader-v2"
    _write_plugin_with_flows(first, version="1.0.0", flow_ids=("kept", "removed"))
    _write_plugin_with_flows(second, version="2.0.0", flow_ids=("kept", "added"))

    install_plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(first)
    )
    await service.commit_install(plan_id=install_plan.plan_id, project_revision="r1")
    original_kept = bindings.resolve("local:default", "com.example.reader", "kept")
    assert original_kept is not None
    upgrade_plan = await service.inspect_install(
        project_key="local:default", project_revision="r2", source_location=str(second)
    )
    upgraded = await service.upgrade(
        project_key="local:default",
        plugin_id="com.example.reader",
        plan_id=upgrade_plan.plan_id,
        project_revision="r2",
    )
    assert upgraded.version == "2.0.0"
    assert {item.logical_flow_id for item in upgraded.flow_bindings} == {"kept", "added"}

    rolled_back = await service.rollback(
        project_key="local:default", plugin_id="com.example.reader"
    )

    assert rolled_back.version == "1.0.0"
    assert {item.logical_flow_id for item in rolled_back.flow_bindings} == {"kept", "removed"}
    restored_kept = bindings.resolve("local:default", "com.example.reader", "kept")
    assert restored_kept is not None
    assert restored_kept.directus_flow_uuid == original_kept.directus_flow_uuid
    active_flow_ids = {flow.flow_uuid for flow in directus.flows if flow.status == "active"}
    assert active_flow_ids == {binding.directus_flow_uuid for binding in rolled_back.flow_bindings}


@pytest.mark.asyncio
async def test_upgrade_cleanup_failure_keeps_registry_on_previous_version(tmp_path: Path) -> None:
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    first = tmp_path / "reader-v1"
    second = tmp_path / "reader-v2"
    _write_plugin_with_flows(first, version="1.0.0", flow_ids=("kept", "removed"))
    _write_plugin_with_flows(second, version="2.0.0", flow_ids=("kept",))
    install_plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(first)
    )
    await service.commit_install(plan_id=install_plan.plan_id, project_revision="r1")
    upgrade_plan = await service.inspect_install(
        project_key="local:default", project_revision="r2", source_location=str(second)
    )
    directus.fail_on.add("delete")

    with pytest.raises(RuntimeError, match="injected Directus delete failure"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.reader",
            plan_id=upgrade_plan.plan_id,
            project_revision="r2",
        )

    current = registry.get("local:default", "com.example.reader")
    assert current is not None
    assert current.version == "1.0.0"
    assert {
        item.logical_flow_id for item in store.list_bindings("local:default", "com.example.reader")
    } == {"kept", "removed"}


@pytest.mark.asyncio
async def test_uninstall_deletes_retained_packages_but_not_user_source(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    package = tmp_path / "reader.vtplugin"
    _write_plugin(source)
    pack_plugin(source, package)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(package),
    )
    installed = await service.commit_install(plan_id=plan.plan_id, project_revision="r1")
    retained = Path(installed.source_location)

    result = await service.uninstall(
        project_key="local:default",
        plugin_id="com.example.reader",
        cleanup_private_settings=False,
    )

    assert result.uninstalled is True
    assert retained.exists() is False
    assert package.exists() is True
    assert store.list_package_revisions("local:default", "com.example.reader") == []


@pytest.mark.asyncio
async def test_shared_package_cache_is_deleted_only_after_last_project_uninstalls(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    package = tmp_path / "reader.vtplugin"
    _write_plugin(source)
    pack_plugin(source, package)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store, bindings=bindings),
        bindings=bindings,
        directus=directus,
        runtime=None,
        interactions=None,
        package_cache=tmp_path / "cache",
    )
    retained: Path | None = None
    for project in ("project:a", "project:b"):
        plan = await service.inspect_install(
            project_key=project,
            project_revision="r1",
            source_location=str(package),
        )
        installed = await service.commit_install(plan_id=plan.plan_id, project_revision="r1")
        retained = Path(installed.source_location)
    assert retained is not None
    assert retained.is_file()

    await service.uninstall(
        project_key="project:a",
        plugin_id="com.example.reader",
        cleanup_private_settings=False,
    )
    assert retained.is_file()
    await service.uninstall(
        project_key="project:b",
        plugin_id="com.example.reader",
        cleanup_private_settings=False,
    )
    assert retained.exists() is False


@pytest.mark.asyncio
async def test_confirmation_decision_and_safe_terminal_error_are_audited(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)

    class FakeRuntime:
        def __init__(self) -> None:
            self.task = PluginTaskSnapshot(
                task_id="task-audit",
                run_id="run-audit",
                plugin_id="com.example.reader",
                plugin_version="1.0.0",
                action_id="read",
                project_key="local:default",
                collection="articles",
                target_count=2,
                risk="write",
                state="running",
            )

        async def start(self, *_args: object, **_kwargs: object) -> PluginTaskSnapshot:
            return self.task

        def get_task(self, _task_id: str) -> PluginTaskSnapshot:
            return self.task.model_copy(
                update={
                    "state": "failed",
                    "error": PluginSafeError(
                        code="plugin_directus_unavailable",
                        message="Directus unavailable",
                        recoverability="retry",
                        plugin_id="com.example.reader",
                        action_id="read",
                        run_id="run-audit",
                    ),
                }
            )

    class FakeInteractions:
        async def resolve(
            self, _run_id: str, _interaction_id: str, decision: str
        ) -> InteractionResolveResult:
            return InteractionResolveResult(status="resolved", decision=decision)

    runtime = FakeRuntime()
    service = PluginPlatformService(
        store=store,
        registry=registry,
        bindings=bindings,
        directus=directus,
        runtime=runtime,  # type: ignore[arg-type]
        interactions=FakeInteractions(),
        package_cache=tmp_path / "cache",
    )
    plan = await service.inspect_install(
        project_key="local:default", project_revision="r1", source_location=str(source)
    )
    await service.commit_install(plan_id=plan.plan_id, project_revision="r1")
    await service.start_action(
        project_key="local:default",
        plugin_id="com.example.reader",
        action_id="read",
        context=CommandContext(project_key="local:default", collection="articles"),
        input_payload={},
    )

    await service.resolve_interaction(
        run_id="run-audit", interaction_id="confirm-audit", decision="rejected"
    )
    service.get_task(task_id="task-audit")

    audit = store.list_audit("local:default", "com.example.reader")
    confirmation = next(event for event in audit if event.event_type == "interaction.resolve")
    terminal = next(event for event in audit if event.event_type == "action.end")
    assert confirmation.outcome == "rejected"
    assert confirmation.details["interactionId"] == "confirm-audit"
    assert terminal.error_code == "plugin_directus_unavailable"
