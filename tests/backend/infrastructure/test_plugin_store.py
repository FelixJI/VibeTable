from __future__ import annotations

from pathlib import Path

import pytest

from backend.contracts.plugin import (
    FlowBindingSnapshot,
    PluginAuditEvent,
    PluginManifest,
    PluginPackageRevision,
    PluginPrivateSetting,
    PluginSnapshot,
)
from backend.infrastructure.plugin_store import (
    InMemoryPluginStore,
    PluginProjectStore,
    PluginStoreConflictError,
)


def _snapshot(*, revision: int = 1, status: str = "enabled") -> PluginSnapshot:
    return PluginSnapshot(
        project_key="local:default",
        plugin_id="com.example.summary",
        version="1.0.0",
        package_hash="sha256:package",
        source_type="package",
        source_location="summary.vtplugin",
        manifest=PluginManifest(
            plugin_id="com.example.summary",
            version="1.0.0",
            display_name={"zh-CN": "数据概览"},
        ),
        status=status,
        revision=revision,
    )


def _binding(*, revision: int = 1) -> FlowBindingSnapshot:
    return FlowBindingSnapshot(
        project_key="local:default",
        plugin_id="com.example.summary",
        logical_flow_id="summary",
        ownership="external",
        directus_flow_uuid="34e2d18e-bf0e-4e0d-901b-ea779cb692c1",
        trigger_type="manual",
        contract_version="1.0",
        observed_definition_hash="sha256:observed",
        revision=revision,
        health="healthy",
        drift_status="not-applicable",
    )


def _audit_event(*, event_id: str = "audit-1", event_type: str = "uninstall") -> PluginAuditEvent:
    return PluginAuditEvent(
        event_id=event_id,
        project_key="local:default",
        plugin_id="com.example.summary",
        plugin_version="1.0.0",
        package_hash="sha256:package",
        event_type=event_type,
        outcome="succeeded",
    )


def test_project_store_persists_installation_and_rejects_stale_revision(tmp_path: Path) -> None:
    path = tmp_path / "plugin-state.db"
    first = PluginProjectStore(path)
    first.save_installation(_snapshot())
    first.close()

    reopened = PluginProjectStore(path)
    assert reopened.get_installation("local:default", "com.example.summary") == _snapshot()

    with pytest.raises(PluginStoreConflictError):
        reopened.save_installation(
            _snapshot(revision=2, status="disabled"),
            expected_revision=0,
        )

    assert reopened.get_installation("local:default", "com.example.summary") == _snapshot()
    reopened.close()


def test_project_store_removes_bindings_but_retains_audit_across_reopen(
    tmp_path: Path,
) -> None:
    path = tmp_path / "plugin-state.db"
    store = PluginProjectStore(path)
    store.save_installation(_snapshot())
    store.save_binding(_binding())
    store.record_audit(_audit_event())
    assert store.delete_bindings("local:default", "com.example.summary") == 1
    assert store.delete_installation("local:default", "com.example.summary") is True
    store.close()

    reopened = PluginProjectStore(path)
    assert reopened.get_installation("local:default", "com.example.summary") is None
    assert reopened.list_bindings("local:default", "com.example.summary") == []
    assert reopened.list_audit("local:default", "com.example.summary") == [_audit_event()]
    reopened.close()


def test_project_store_retains_package_rollback_and_private_settings(tmp_path: Path) -> None:
    path = tmp_path / "plugin-state.db"
    store = PluginProjectStore(path)
    revision = PluginPackageRevision(
        project_key="local:default",
        plugin_id="com.example.summary",
        version="1.0.0",
        package_hash="sha256:package",
        local_path="packages/sha256-package.vtplugin",
        manifest=_snapshot().manifest,
        state="current",
    )
    store.save_package_revision(revision)
    setting = store.save_private_setting(
        PluginPrivateSetting(
            project_key="local:default",
            plugin_id="com.example.summary",
            setting_key="columns",
            value={"visible": ["title"]},
            revision=1,
        )
    )

    assert store.list_package_revisions("local:default", "com.example.summary") == [revision]
    assert store.get_private_setting("local:default", "com.example.summary", "columns") == setting
    with pytest.raises(PluginStoreConflictError):
        store.save_private_setting(setting.model_copy(update={"revision": 2}), expected_revision=0)
    assert store.delete_private_settings("local:default", "com.example.summary") == 1
    assert store.get_private_setting("local:default", "com.example.summary", "columns") is None
    store.close()


def test_project_store_delete_operations_are_exact_and_idempotent(tmp_path: Path) -> None:
    store = PluginProjectStore(tmp_path / "plugin-state.db")
    store.save_installation(_snapshot())
    binding = _binding()
    store.save_binding(binding)
    assert store.get_binding("local:default", "com.example.summary", "summary") == binding
    assert store.delete_binding("local:default", "com.example.summary", "missing") is False
    assert store.delete_binding("local:default", "com.example.summary", "summary") is True
    assert store.delete_binding("local:default", "com.example.summary", "summary") is False
    assert store.delete_installation("local:default", "missing") is False
    assert store.delete_installation("local:default", "com.example.summary") is True
    assert store.list_installations("local:default") == []
    store.close()


def test_project_store_package_revision_reference_and_cleanup(tmp_path: Path) -> None:
    store = PluginProjectStore(tmp_path / "plugin-state.db")
    revisions = [
        PluginPackageRevision(
            project_key="local:default",
            plugin_id="com.example.summary",
            version=version,
            package_hash=f"sha256:{version}",
            local_path=f"packages/{version}.vtplugin",
            manifest=_snapshot().manifest,
            state="rollback",
        )
        for version in ("1.0.0", "2.0.0")
    ]
    for revision in revisions:
        store.save_package_revision(revision)
    assert store.is_package_path_referenced("packages/1.0.0.vtplugin") is True
    assert store.is_package_path_referenced("packages/missing.vtplugin") is False
    assert (
        store.delete_package_revision(
            "local:default", "com.example.summary", "1.0.0", "sha256:1.0.0"
        )
        is True
    )
    assert (
        store.delete_package_revision(
            "local:default", "com.example.summary", "1.0.0", "sha256:1.0.0"
        )
        is False
    )
    assert store.delete_package_revisions("local:default", "com.example.summary") == 1
    assert store.list_package_revisions("local:default", "com.example.summary") == []
    store.close()


def test_project_store_private_setting_update_and_project_audit(tmp_path: Path) -> None:
    store = PluginProjectStore(tmp_path / "plugin-state.db")
    initial = PluginPrivateSetting(
        project_key="local:default",
        plugin_id="com.example.summary",
        setting_key="columns",
        value={"visible": ["title"]},
        revision=1,
    )
    with pytest.raises(PluginStoreConflictError):
        store.save_private_setting(initial, expected_revision=1)
    store.save_private_setting(initial)
    updated = initial.model_copy(update={"value": {"visible": ["number"]}, "revision": 2})
    assert store.save_private_setting(updated, expected_revision=1) == updated
    assert store.get_private_setting("local:default", "com.example.summary", "columns") == updated

    event = _audit_event(event_id="audit-project", event_type="settings-updated")
    store.record_audit(event)
    assert store.list_project_audit("local:default") == [event]
    store.close()


def test_project_store_binding_update_is_revision_guarded(tmp_path: Path) -> None:
    store = PluginProjectStore(tmp_path / "plugin-state.db")
    initial = _binding()
    store.save_binding(initial)
    updated = initial.model_copy(
        update={
            "revision": 2,
            "observed_definition_hash": "sha256:changed",
            "health": "drifted",
            "last_error": "drift detected",
        }
    )
    assert store.save_binding(updated, expected_revision=1) == updated
    assert store.get_binding("local:default", "com.example.summary", "summary") == updated
    with pytest.raises(PluginStoreConflictError):
        store.save_binding(updated.model_copy(update={"revision": 3}), expected_revision=1)
    assert store.get_binding("local:default", "com.example.summary", "summary") == updated
    store.close()


def test_in_memory_store_conflicts_and_project_audit() -> None:
    store = InMemoryPluginStore()
    installation = _snapshot()
    store.save_installation(installation)
    with pytest.raises(PluginStoreConflictError):
        store.save_installation(
            installation.model_copy(update={"revision": 2}), expected_revision=0
        )

    binding = _binding()
    store.save_binding(binding)
    with pytest.raises(PluginStoreConflictError):
        store.save_binding(binding.model_copy(update={"revision": 2}), expected_revision=0)

    setting = PluginPrivateSetting(
        project_key="local:default",
        plugin_id="com.example.summary",
        setting_key="columns",
        value={"visible": ["title"]},
        revision=1,
    )
    store.save_private_setting(setting)
    with pytest.raises(PluginStoreConflictError):
        store.save_private_setting(setting.model_copy(update={"revision": 2}), expected_revision=0)

    event = _audit_event(event_id="audit-memory", event_type="checked")
    store.record_audit(event)
    assert store.list_project_audit("local:default") == [event]
