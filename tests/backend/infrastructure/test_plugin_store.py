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
from backend.infrastructure.plugin_store import PluginProjectStore, PluginStoreConflictError


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
    store.save_binding(
        FlowBindingSnapshot(
            project_key="local:default",
            plugin_id="com.example.summary",
            logical_flow_id="summary",
            ownership="external",
            directus_flow_uuid="34e2d18e-bf0e-4e0d-901b-ea779cb692c1",
            trigger_type="manual",
            contract_version="1.0",
            observed_definition_hash="sha256:observed",
            revision=1,
            health="healthy",
            drift_status="not-applicable",
        )
    )
    store.record_audit(
        PluginAuditEvent(
            event_id="audit-1",
            project_key="local:default",
            plugin_id="com.example.summary",
            plugin_version="1.0.0",
            package_hash="sha256:package",
            event_type="uninstall",
            outcome="succeeded",
        )
    )
    assert store.delete_bindings("local:default", "com.example.summary") == 1
    assert store.delete_installation("local:default", "com.example.summary") is True
    store.close()

    reopened = PluginProjectStore(path)
    assert reopened.get_installation("local:default", "com.example.summary") is None
    assert reopened.list_bindings("local:default", "com.example.summary") == []
    assert reopened.list_audit("local:default", "com.example.summary") == [
        PluginAuditEvent(
            event_id="audit-1",
            project_key="local:default",
            plugin_id="com.example.summary",
            plugin_version="1.0.0",
            package_hash="sha256:package",
            event_type="uninstall",
            outcome="succeeded",
        )
    ]
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
