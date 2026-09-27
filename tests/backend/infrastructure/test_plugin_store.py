"""Product-neutral async plugin store semantics tests."""

from __future__ import annotations

import pytest

from backend.application.plugin_registry import PluginRegistryError
from backend.contracts.plugin import (
    InstallPlan,
    PluginAuditEvent,
    PluginManifest,
    PluginPackageRevision,
    PluginPrivateSetting,
    PluginSnapshot,
)
from backend.infrastructure.plugin_store import (
    InMemoryPluginStore,
    PluginStoreConflictError,
)


def _manifest(*, version: str = "1.0.0") -> PluginManifest:
    return PluginManifest.model_validate(
        {
            "$schema": "vibetable.plugin-manifest.v1",
            "pluginId": "com.example.summary",
            "version": version,
            "displayName": {"en": "Summary"},
            "compatibility": {"minHostVersion": "1.0.0", "pluginApi": "1.x"},
            "permissions": {
                "data": [],
                "files": [],
                "privateStorage": True,
            },
            "actions": [
                {
                    "actionId": "summarize",
                    "displayName": {"en": "Summarize"},
                    "mode": "local",
                    "risk": "read",
                    "workerEntry": "dist/worker.js",
                }
            ],
        }
    )


def _plan(*, version: str = "1.0.0") -> InstallPlan:
    return InstallPlan(
        plan_id=f"plan-{version}",
        project_key="local:default",
        project_revision="project-1",
        source_type="package",
        source_location=f"summary-{version}.vtplugin",
        package_hash=f"sha256:{version}",
        manifest=_manifest(version=version),
    )


def _snapshot(*, revision: int = 1, status: str = "enabled") -> PluginSnapshot:
    return PluginSnapshot(
        project_key="local:default",
        plugin_id="com.example.summary",
        version="1.0.0",
        package_hash="sha256:package",
        source_type="package",
        source_location="summary.vtplugin",
        manifest=_manifest(),
        status=status,
        revision=revision,
    )


def _package_revision(
    *,
    version: str = "1.0.0",
    state: str = "current",
) -> PluginPackageRevision:
    return PluginPackageRevision(
        project_key="local:default",
        plugin_id="com.example.summary",
        version=version,
        package_hash=f"sha256:{version}",
        local_path=f"packages/{version}.vtplugin",
        manifest=_manifest().model_copy(update={"version": version}),
        state=state,
    )


def _setting(*, revision: int = 1) -> PluginPrivateSetting:
    return PluginPrivateSetting(
        project_key="local:default",
        plugin_id="com.example.summary",
        setting_key="columns",
        value={"visible": ["title"]},
        revision=revision,
    )


def _audit(*, event_id: str = "audit-1") -> PluginAuditEvent:
    return PluginAuditEvent(
        event_id=event_id,
        project_key="local:default",
        plugin_id="com.example.summary",
        plugin_version="1.0.0",
        package_hash="sha256:package",
        event_type="checked",
        outcome="succeeded",
    )


@pytest.mark.asyncio
async def test_installation_guard_and_delete_operations_are_exact() -> None:
    store = InMemoryPluginStore()
    await store.save_installation(_snapshot(), expected_revision=None)
    assert await store.get_installation("local:default", "com.example.summary") == _snapshot()
    assert await store.list_installations("local:default") == [_snapshot()]

    with pytest.raises(PluginStoreConflictError):
        await store.save_installation(
            _snapshot(revision=2, status="disabled"),
            expected_revision=0,
        )
    updated = _snapshot(revision=2, status="disabled")
    assert await store.save_installation(updated, expected_revision=1) == updated

    assert await store.delete_installation("local:default", "com.example.summary")
    assert not await store.delete_installation("local:default", "com.example.summary")
    assert await store.list_installations("local:default") == []


@pytest.mark.asyncio
async def test_revisions_settings_and_audit_round_trip() -> None:
    store = InMemoryPluginStore()
    package = _package_revision()
    setting = _setting()
    event = _audit()

    assert await store.save_package_revision(package) == package
    await store.save_private_setting(setting, expected_revision=None)
    await store.record_audit(event)

    assert await store.list_package_revisions("local:default", "com.example.summary") == [package]
    assert (
        await store.get_private_setting(
            "local:default",
            "com.example.summary",
            "columns",
        )
        == setting
    )
    assert await store.list_audit("local:default", "com.example.summary") == [event]
    assert await store.list_project_audit("local:default") == [event]


@pytest.mark.asyncio
async def test_revision_delete_operations_are_exact_and_idempotent() -> None:
    store = InMemoryPluginStore()
    for revision in (
        _package_revision(),
        _package_revision(version="2.0.0", state="rollback"),
    ):
        await store.save_package_revision(revision)

    assert await store.is_package_path_referenced("packages/1.0.0.vtplugin")
    assert not await store.is_package_path_referenced("packages/missing.vtplugin")
    assert await store.delete_package_revision(
        "local:default",
        "com.example.summary",
        "sha256:1.0.0",
    )
    assert not await store.delete_package_revision(
        "local:default",
        "com.example.summary",
        "sha256:1.0.0",
    )
    assert (
        await store.delete_package_revisions(
            "local:default",
            "com.example.summary",
        )
        == 1
    )
    assert await store.list_package_revisions("local:default", "com.example.summary") == []


@pytest.mark.asyncio
async def test_private_settings_use_optimistic_revision_guard() -> None:
    store = InMemoryPluginStore()
    initial = _setting()
    with pytest.raises(PluginStoreConflictError):
        await store.save_private_setting(initial, expected_revision=1)
    await store.save_private_setting(initial, expected_revision=None)
    updated = initial.model_copy(
        update={
            "value": {"visible": ["number"]},
            "revision": 2,
        }
    )
    assert await store.save_private_setting(updated, expected_revision=1) == updated
    with pytest.raises(PluginStoreConflictError):
        await store.save_private_setting(
            updated.model_copy(update={"revision": 3}),
            expected_revision=1,
        )
    assert (
        await store.delete_private_settings(
            "local:default",
            "com.example.summary",
        )
        == 1
    )


@pytest.mark.asyncio
async def test_commit_install_is_atomic_and_fails_closed_on_duplicate() -> None:
    store = InMemoryPluginStore()
    plan = _plan()
    revision = _package_revision()

    installed = await store.commit_install(plan, package_revision=revision)

    assert installed.status == "disabled"
    assert installed.disabled_reason == "disabled_by_user"
    assert installed.revision == 1
    assert await store.get_installation("local:default", "com.example.summary") == installed
    assert await store.list_package_revisions("local:default", "com.example.summary") == [revision]
    assert [
        event.event_type for event in await store.list_audit("local:default", "com.example.summary")
    ] == ["install"]

    with pytest.raises(PluginRegistryError) as duplicate:
        await store.commit_install(_plan(), package_revision=revision)
    assert duplicate.value.code == "plugin_already_installed"
