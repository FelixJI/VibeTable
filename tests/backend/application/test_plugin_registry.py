from __future__ import annotations

import pytest

from backend.application.flow_binding_manager import FlowBindingManager
from backend.application.plugin_registry import PluginRegistry, PluginRegistryError
from backend.contracts.plugin import (
    ExternalFlowAttestation,
    FlowRequirement,
    InstallPlan,
    PluginManifest,
    PluginPrivateSetting,
)
from backend.infrastructure.directus_flow import (
    DirectusFlowDefinition,
    InMemoryDirectusFlowAdapter,
)
from backend.infrastructure.plugin_store import InMemoryPluginStore


def _plan(*, version: str = "1.0.0") -> InstallPlan:
    return InstallPlan(
        plan_id=f"plan-{version}",
        project_key="local:default",
        project_revision="project-r1",
        source_type="package",
        source_location="normalize.vtplugin",
        package_hash=f"sha256:{version}",
        manifest=PluginManifest(
            plugin_id="com.example.normalize-text",
            version=version,
            display_name={"zh-CN": "批量文本规范化"},
        ),
    )


@pytest.mark.asyncio
async def test_install_keeps_one_current_plugin_instance_per_project() -> None:
    registry = PluginRegistry(store=InMemoryPluginStore())

    installed = await registry.install(_plan())

    assert installed.project_key == "local:default"
    assert installed.plugin_id == "com.example.normalize-text"
    assert installed.version == "1.0.0"
    assert installed.status == "enabled"

    with pytest.raises(PluginRegistryError) as error:
        await registry.install(_plan(version="2.0.0"))

    assert error.value.code == "plugin_already_installed"


@pytest.mark.asyncio
async def test_plugin_is_disabled_as_a_whole_until_every_required_flow_is_bound() -> None:
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid="02b83af8-d220-4719-9b56-5f01490866a7",
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
    plan = _plan().model_copy(update={"flow_requirements": [requirement]})

    installed = await registry.install(plan)

    assert installed.status == "disabled"
    assert installed.disabled_reason == "flow_unbound:summary"

    await bindings.bind_external(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        requirement=requirement,
        directus_uuid="02b83af8-d220-4719-9b56-5f01490866a7",
        attestation=ExternalFlowAttestation(),
    )
    enabled = await registry.set_enabled(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        enabled=True,
    )

    assert enabled.status == "enabled"
    assert enabled.disabled_reason is None


@pytest.mark.asyncio
async def test_plugin_snapshot_reports_every_blocking_flow_reason() -> None:
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    requirements = [
        FlowRequirement(
            logical_flow_id=logical_id,
            ownership="external",
            trigger="manual",
            risk="read",
            contract_version="1.0",
        )
        for logical_id in ("summary", "export")
    ]

    installed = await registry.install(
        _plan().model_copy(update={"flow_requirements": requirements})
    )

    assert installed.disabled_reason == "flow_unbound:summary"
    assert installed.blocking_reasons == [
        "flow_unbound:summary",
        "flow_unbound:export",
    ]


@pytest.mark.asyncio
async def test_uninstall_only_unbinds_external_flow_and_retains_audit() -> None:
    store = InMemoryPluginStore()
    flow_uuid = "8e630ae0-ea59-4921-aafd-a7b5b5488787"
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid=flow_uuid,
                trigger="manual",
                status="active",
                operation_keys=(),
                definition={"name": "User-owned"},
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
    plan = _plan().model_copy(update={"flow_requirements": [requirement]})
    await registry.install(plan)
    await bindings.bind_external(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        requirement=requirement,
        directus_uuid=flow_uuid,
        attestation=ExternalFlowAttestation(),
    )
    mutations_before = list(directus.mutation_log)

    result = await registry.uninstall(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        cleanup_private_settings=False,
    )

    assert result.uninstalled is True
    assert result.external_flows_unbound == 1
    assert result.managed_flows_removed == 0
    assert registry.get(plan.project_key, plan.manifest.plugin_id) is None
    assert directus.mutation_log == mutations_before
    assert await directus.read_flow(flow_uuid) is not None
    audit = store.list_audit(plan.project_key, plan.manifest.plugin_id)
    assert audit[-1].event_type == "uninstall"


@pytest.mark.asyncio
async def test_uninstall_can_explicitly_delete_private_settings() -> None:
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    plan = _plan()
    await registry.install(plan)
    store.save_private_setting(
        PluginPrivateSetting(
            project_key=plan.project_key,
            plugin_id=plan.manifest.plugin_id,
            setting_key="mode",
            value="compact",
            revision=1,
        )
    )

    result = await registry.uninstall(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        cleanup_private_settings=True,
    )

    assert result.private_settings_retained is False
    assert store.get_private_setting(plan.project_key, plan.manifest.plugin_id, "mode") is None


@pytest.mark.asyncio
async def test_uninstall_is_locally_final_and_retains_cleanup_record_when_directus_is_offline() -> (
    None
):
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter()
    bindings = FlowBindingManager(store=store, directus=directus)
    registry = PluginRegistry(store=store, bindings=bindings)
    requirement = FlowRequirement(
        logical_flow_id="managed",
        ownership="managed",
        trigger="manual",
        risk="read",
        contract_version="1.0",
        definition={"operations": []},
    )
    plan = _plan().model_copy(update={"flow_requirements": [requirement]})
    await bindings.provision_managed(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        requirement=requirement,
    )
    await registry.install(plan)
    directus.fail_on.add("delete")

    result = await registry.uninstall(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        cleanup_private_settings=False,
    )

    assert result.uninstalled is True
    assert result.cleanup_pending is True
    assert registry.get(plan.project_key, plan.manifest.plugin_id) is None
    assert bindings.resolve(plan.project_key, plan.manifest.plugin_id, "managed") is not None
    assert store.list_audit(plan.project_key, plan.manifest.plugin_id)[-1].outcome == (
        "pending-cleanup"
    )

    directus.fail_on.remove("delete")
    retry = await registry.uninstall(
        project_key=plan.project_key,
        plugin_id=plan.manifest.plugin_id,
        cleanup_private_settings=False,
    )

    assert retry.uninstalled is False
    assert retry.cleanup_pending is False
    assert retry.managed_flows_removed == 1
    assert bindings.resolve(plan.project_key, plan.manifest.plugin_id, "managed") is None
    retry_audit = store.list_audit(plan.project_key, plan.manifest.plugin_id)[-1]
    assert retry_audit.event_type == "uninstall-cleanup-retry"
    assert retry_audit.outcome == "succeeded"
