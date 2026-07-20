from __future__ import annotations

import pytest

from backend.application.flow_binding_manager import FlowBindingError, FlowBindingManager
from backend.contracts.plugin import ExternalFlowAttestation, FlowRequirement
from backend.infrastructure.directus_flow import (
    DirectusFlowDefinition,
    InMemoryDirectusFlowAdapter,
)
from backend.infrastructure.plugin_store import InMemoryPluginStore


@pytest.mark.asyncio
async def test_external_flow_binding_is_read_only_and_resolves_logical_id() -> None:
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid="a12f74d8-45c8-4f5a-8ce5-140502f78839",
                trigger="manual",
                status="active",
                operation_keys=(),
                definition={"name": "User-owned summary"},
            )
        ]
    )
    manager = FlowBindingManager(store=InMemoryPluginStore(), directus=directus)
    requirement = FlowRequirement(
        logical_flow_id="summary",
        ownership="external",
        trigger="manual",
        risk="read",
        contract_version="1.0",
    )

    binding = await manager.bind_external(
        project_key="local:default",
        plugin_id="com.example.summary",
        requirement=requirement,
        directus_uuid="a12f74d8-45c8-4f5a-8ce5-140502f78839",
        attestation=ExternalFlowAttestation(),
    )

    assert binding.logical_flow_id == "summary"
    assert binding.directus_flow_uuid == "a12f74d8-45c8-4f5a-8ce5-140502f78839"
    assert binding.ownership == "external"
    assert binding.health == "healthy"
    assert manager.resolve("local:default", "com.example.summary", "summary") == binding
    assert directus.mutation_log == []


@pytest.mark.asyncio
async def test_external_write_flow_requires_unknown_side_effect_attestation() -> None:
    directus = InMemoryDirectusFlowAdapter(
        flows=[
            DirectusFlowDefinition(
                flow_uuid="external-write",
                trigger="manual",
                status="active",
                operation_keys=("vibetable.confirm@1",),
                definition={"name": "User-owned write Flow"},
            )
        ]
    )
    manager = FlowBindingManager(store=InMemoryPluginStore(), directus=directus)
    requirement = FlowRequirement(
        logical_flow_id="write",
        ownership="external",
        trigger="manual",
        risk="write",
        contract_version="1.0",
        requires_operations=["vibetable.confirm@1"],
    )

    with pytest.raises(FlowBindingError) as error:
        await manager.bind_external(
            project_key="local:default",
            plugin_id="com.example.writer",
            requirement=requirement,
            directus_uuid="external-write",
            attestation=ExternalFlowAttestation(),
        )

    assert error.value.code == "external_flow_attestation_required"


@pytest.mark.asyncio
async def test_managed_flow_is_created_inactive_validated_then_activated() -> None:
    directus = InMemoryDirectusFlowAdapter()
    manager = FlowBindingManager(store=InMemoryPluginStore(), directus=directus)
    requirement = FlowRequirement(
        logical_flow_id="normalize",
        ownership="managed",
        trigger="manual",
        risk="read",
        contract_version="1.0",
        definition={
            "name": "Normalize preview",
            "operations": [{"key": "read-selection", "type": "item-read"}],
        },
    )

    binding = await manager.provision_managed(
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        requirement=requirement,
    )

    assert binding.ownership == "managed"
    assert binding.health == "healthy"
    assert binding.installed_definition_hash == binding.observed_definition_hash
    assert directus.mutation_log == [
        ("create-inactive", binding.directus_flow_uuid),
        ("create-operations", binding.directus_flow_uuid),
        ("activate", binding.directus_flow_uuid),
    ]


@pytest.mark.asyncio
async def test_managed_flow_install_compensates_when_activation_fails() -> None:
    store = InMemoryPluginStore()
    directus = InMemoryDirectusFlowAdapter(fail_on={"activate"})
    manager = FlowBindingManager(store=store, directus=directus)
    requirement = FlowRequirement(
        logical_flow_id="normalize",
        ownership="managed",
        trigger="manual",
        risk="read",
        contract_version="1.0",
        definition={"operations": [{"key": "read", "type": "item-read"}]},
    )

    with pytest.raises(FlowBindingError) as error:
        await manager.provision_managed(
            project_key="local:default",
            plugin_id="com.example.normalize-text",
            requirement=requirement,
        )

    assert error.value.code == "managed_flow_install_failed"
    assert directus.mutation_log[-1] == ("delete", "managed-flow-1")
    assert manager.resolve("local:default", "com.example.normalize-text", "normalize") is None


@pytest.mark.asyncio
async def test_managed_flow_user_edit_is_reported_as_drift_without_silent_overwrite() -> None:
    directus = InMemoryDirectusFlowAdapter()
    store = InMemoryPluginStore()
    manager = FlowBindingManager(store=store, directus=directus)
    requirement = FlowRequirement(
        logical_flow_id="normalize",
        ownership="managed",
        trigger="manual",
        risk="read",
        contract_version="1.0",
        definition={"name": "Original", "operations": []},
    )
    installed = await manager.provision_managed(
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        requirement=requirement,
    )
    directus.edit_definition(installed.directus_flow_uuid, {"name": "User edited"})

    report = await manager.detect_drift("local:default", "com.example.normalize-text")

    assert len(report) == 1
    assert report[0].drift_status == "drifted"
    assert report[0].health == "drifted"
    assert report[0].installed_definition_hash == installed.installed_definition_hash
    assert report[0].observed_definition_hash != installed.observed_definition_hash
    assert directus.mutation_log[-1] == ("user-edit", installed.directus_flow_uuid)


@pytest.mark.asyncio
async def test_managed_flow_upgrade_stops_before_creating_revision_when_drifted() -> None:
    directus = InMemoryDirectusFlowAdapter()
    store = InMemoryPluginStore()
    manager = FlowBindingManager(store=store, directus=directus)
    original = FlowRequirement(
        logical_flow_id="normalize",
        ownership="managed",
        trigger="manual",
        risk="read",
        contract_version="1.0",
        definition={"name": "Original", "operations": []},
    )
    installed = await manager.provision_managed(
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        requirement=original,
    )
    directus.edit_definition(installed.directus_flow_uuid, {"name": "User edited"})
    mutations_before_upgrade = list(directus.mutation_log)

    with pytest.raises(FlowBindingError) as error:
        await manager.upgrade_managed(
            project_key="local:default",
            plugin_id="com.example.normalize-text",
            requirement=original.model_copy(
                update={"contract_version": "1.1", "definition": {"name": "Upgrade"}}
            ),
        )

    assert error.value.code == "managed_flow_drifted"
    assert directus.mutation_log == mutations_before_upgrade


@pytest.mark.asyncio
async def test_managed_manual_flow_upgrade_switches_to_new_revision_and_retains_rollback() -> None:
    directus = InMemoryDirectusFlowAdapter()
    manager = FlowBindingManager(store=InMemoryPluginStore(), directus=directus)
    original = FlowRequirement(
        logical_flow_id="normalize",
        ownership="managed",
        trigger="manual",
        risk="read",
        contract_version="1.0",
        definition={"name": "Original", "operations": []},
    )
    before = await manager.provision_managed(
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        requirement=original,
    )

    after = await manager.upgrade_managed(
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        requirement=original.model_copy(
            update={
                "contract_version": "1.1",
                "definition": {"name": "Upgrade", "operations": []},
            }
        ),
    )

    assert after.directus_flow_uuid != before.directus_flow_uuid
    assert after.revision == 2
    assert after.rollback_flow_uuid == before.directus_flow_uuid
    assert (await directus.read_flow(after.directus_flow_uuid)).status == "active"
    assert (await directus.read_flow(before.directus_flow_uuid)).status == "inactive"
    assert directus.mutation_log[-2:] == [
        ("activate", after.directus_flow_uuid),
        ("deactivate", before.directus_flow_uuid),
    ]


@pytest.mark.asyncio
async def test_managed_schedule_upgrade_never_activates_two_revisions() -> None:
    directus = InMemoryDirectusFlowAdapter()
    manager = FlowBindingManager(store=InMemoryPluginStore(), directus=directus)
    original = FlowRequirement(
        logical_flow_id="nightly",
        ownership="managed",
        trigger="schedule",
        risk="read",
        contract_version="1.0",
        definition={"name": "Nightly", "operations": []},
    )
    before = await manager.provision_managed(
        project_key="local:default",
        plugin_id="com.example.nightly",
        requirement=original,
    )

    after = await manager.upgrade_managed(
        project_key="local:default",
        plugin_id="com.example.nightly",
        requirement=original.model_copy(
            update={
                "contract_version": "2.0",
                "definition": {"name": "Nightly v2", "operations": []},
            }
        ),
    )

    assert directus.mutation_log[-2:] == [
        ("deactivate", before.directus_flow_uuid),
        ("activate", after.directus_flow_uuid),
    ]


@pytest.mark.asyncio
async def test_managed_flow_rollback_restores_previous_binding_and_contract() -> None:
    directus = InMemoryDirectusFlowAdapter()
    manager = FlowBindingManager(store=InMemoryPluginStore(), directus=directus)
    original = FlowRequirement(
        logical_flow_id="normalize",
        ownership="managed",
        trigger="manual",
        risk="read",
        contract_version="1.0",
        definition={"name": "Original", "operations": []},
    )
    before = await manager.provision_managed(
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        requirement=original,
    )
    after = await manager.upgrade_managed(
        project_key="local:default",
        plugin_id="com.example.normalize-text",
        requirement=original.model_copy(
            update={
                "contract_version": "2.0",
                "definition": {"name": "Upgrade", "operations": []},
            }
        ),
    )

    rolled_back = await manager.rollback_managed(
        "local:default", "com.example.normalize-text", "normalize"
    )

    assert rolled_back.directus_flow_uuid == before.directus_flow_uuid
    assert rolled_back.contract_version == "1.0"
    assert rolled_back.revision == 3
    assert (await directus.read_flow(before.directus_flow_uuid)).status == "active"
    assert (await directus.read_flow(after.directus_flow_uuid)).status == "inactive"


@pytest.mark.asyncio
async def test_drift_resolution_restores_package_revision_or_detaches_without_deleting_user_flow() -> (
    None
):
    directus = InMemoryDirectusFlowAdapter()
    store = InMemoryPluginStore()
    manager = FlowBindingManager(store=store, directus=directus)
    requirement = FlowRequirement(
        logical_flow_id="normalize",
        ownership="managed",
        trigger="manual",
        risk="write",
        contract_version="1.0",
        definition={"name": "Package", "operations": []},
    )
    installed = await manager.provision_managed(
        project_key="local:default",
        plugin_id="com.example.normalize",
        requirement=requirement,
    )
    directus.edit_definition(installed.directus_flow_uuid, {"name": "User edit"})
    [drifted] = await manager.detect_drift("local:default", "com.example.normalize")
    assert drifted.drift_status == "drifted"

    restored = await manager.restore_managed(
        project_key="local:default",
        plugin_id="com.example.normalize",
        requirement=requirement,
    )
    assert restored.directus_flow_uuid != installed.directus_flow_uuid
    assert restored.drift_status == "clean"

    user_uuid = restored.directus_flow_uuid
    detached = manager.detach_managed("local:default", "com.example.normalize", "normalize")
    assert detached.ownership == "external"
    report = await manager.remove_requirement("local:default", "com.example.normalize", "normalize")
    assert report.external_flows_unbound == 1
    assert await directus.read_flow(user_uuid) is not None
