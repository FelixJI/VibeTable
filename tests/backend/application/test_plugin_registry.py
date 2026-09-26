"""Local-worker plugin registry tests without provider coupling."""

from __future__ import annotations

import pytest

from backend.application.plugin_registry import (
    PluginRegistry,
    PluginRegistryError,
    validate_install_plan,
)
from backend.contracts.plugin import (
    InstallPlan,
    PluginManifest,
    PluginPackageRevision,
    PluginPrivateSetting,
)
from backend.infrastructure.plugin_store import InMemoryPluginStore


def _manifest(*, version: str = "1.0.0") -> PluginManifest:
    return PluginManifest.model_validate(
        {
            "$schema": "vibetable.plugin-manifest.v1",
            "pluginId": "com.example.summary",
            "version": version,
            "displayName": {"en": "Summary"},
            "compatibility": {
                "minHostVersion": "1.0.0",
                "pluginApi": "1.x",
            },
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


async def _install(store: InMemoryPluginStore, plan: InstallPlan) -> None:
    await store.commit_install(
        plan,
        package_revision=PluginPackageRevision(
            project_key=plan.project_key,
            plugin_id=plan.manifest.plugin_id,
            version=plan.manifest.version,
            package_hash=plan.package_hash,
            local_path=f"packages/{plan.manifest.version}.vtplugin",
            manifest=plan.manifest,
            state="current",
        ),
    )


@pytest.mark.asyncio
async def test_enable_disable_and_upgrade_preserve_local_identity() -> None:
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    await _install(store, _plan())
    installed = await store.get_installation("local:default", "com.example.summary")
    assert installed is not None
    assert installed.status == "disabled"
    assert installed.disabled_reason == "disabled_by_user"
    assert installed.revision == 1
    assert await registry.list("local:default") == [installed]
    with pytest.raises(PluginRegistryError) as duplicate:
        await _install(store, _plan())
    assert duplicate.value.code == "plugin_already_installed"

    enabled = await registry.set_enabled(
        "local:default",
        "com.example.summary",
        True,
    )
    assert enabled.status == "enabled"
    assert enabled.revision == 2

    upgraded = await registry.commit_upgrade(_plan(version="2.0.0"))
    assert upgraded.version == "2.0.0"
    assert upgraded.package_hash == "sha256:2.0.0"
    assert upgraded.status == "enabled"
    assert upgraded.revision == 3

    disabled = await registry.set_enabled(
        "local:default",
        "com.example.summary",
        False,
    )
    assert disabled.status == "disabled"
    assert disabled.disabled_reason == "disabled_by_user"


@pytest.mark.asyncio
async def test_uninstall_retains_or_removes_private_settings_explicitly() -> None:
    for cleanup in (False, True):
        store = InMemoryPluginStore()
        registry = PluginRegistry(store=store)
        await _install(store, _plan())
        setting = PluginPrivateSetting(
            project_key="local:default",
            plugin_id="com.example.summary",
            setting_key="columns",
            value=["title"],
            revision=1,
        )
        await store.save_private_setting(setting, expected_revision=None)

        result = await registry.uninstall(
            "local:default",
            "com.example.summary",
            cleanup_private_settings=cleanup,
        )

        assert result.uninstalled
        assert result.private_settings_retained is (not cleanup)
        assert (
            await store.get_installation(
                "local:default",
                "com.example.summary",
            )
            is None
        )
        expected = None if cleanup else setting
        assert (
            await store.get_private_setting(
                "local:default",
                "com.example.summary",
                "columns",
            )
            == expected
        )
        assert [
            event.event_type
            for event in await store.list_audit(
                "local:default",
                "com.example.summary",
            )
        ] == ["install", "uninstall"]


@pytest.mark.asyncio
async def test_missing_plugin_operations_fail_with_stable_product_code() -> None:
    registry = PluginRegistry(store=InMemoryPluginStore())

    with pytest.raises(PluginRegistryError) as error:
        await registry.set_enabled(
            "local:default",
            "com.example.missing",
            True,
        )

    assert error.value.code == "plugin_not_found"
    assert error.value.rpc_error_data == {"code": "plugin_not_found"}


def test_validate_install_plan_rejects_legacy_remote_actions() -> None:
    manifest = _manifest().model_copy(deep=True)
    manifest.actions[0].worker_entry = ""
    plan = _plan().model_copy(update={"manifest": manifest})

    with pytest.raises(PluginRegistryError) as error:
        validate_install_plan(plan)

    assert error.value.code == "plugin_manifest_legacy"
