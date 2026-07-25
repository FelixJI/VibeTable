"""Product-neutral plugin store persistence and concurrency tests."""

from __future__ import annotations

from pathlib import Path

import pytest

from backend.contracts.plugin import (
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


def _manifest() -> PluginManifest:
    return PluginManifest(
        plugin_id="com.example.summary",
        version="1.0.0",
        display_name={"en": "Summary"},
        compatibility={"minHostVersion": "1.0.0", "pluginApi": "1.x"},
        permissions={
            "data": [],
            "files": [],
            "privateStorage": True,
        },
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


def test_project_store_persists_installation_and_revision_guard(tmp_path: Path) -> None:
    path = tmp_path / "plugin-state.db"
    store = PluginProjectStore(path)
    store.save_installation(_snapshot(), expected_revision=None)
    store.close()

    reopened = PluginProjectStore(path)
    assert (
        reopened.get_installation(
            "local:default",
            "com.example.summary",
        )
        == _snapshot()
    )
    with pytest.raises(PluginStoreConflictError):
        reopened.save_installation(
            _snapshot(revision=2, status="disabled"),
            expected_revision=0,
        )
    updated = _snapshot(revision=2, status="disabled")
    assert reopened.save_installation(updated, expected_revision=1) == updated
    reopened.close()


def test_project_store_persists_revisions_settings_and_audit(tmp_path: Path) -> None:
    path = tmp_path / "plugin-state.db"
    store = PluginProjectStore(path)
    package = _package_revision()
    setting = _setting()
    event = _audit()
    store.save_package_revision(package)
    store.save_private_setting(setting, expected_revision=None)
    store.record_audit(event)
    store.close()

    reopened = PluginProjectStore(path)
    assert reopened.list_package_revisions(
        "local:default",
        "com.example.summary",
    ) == [package]
    assert (
        reopened.get_private_setting(
            "local:default",
            "com.example.summary",
            "columns",
        )
        == setting
    )
    assert reopened.list_project_audit("local:default") == [event]
    reopened.close()


def test_project_store_delete_operations_are_exact_and_idempotent(
    tmp_path: Path,
) -> None:
    store = PluginProjectStore(tmp_path / "plugin-state.db")
    store.save_installation(_snapshot(), expected_revision=None)
    for revision in (
        _package_revision(),
        _package_revision(version="2.0.0", state="rollback"),
    ):
        store.save_package_revision(revision)

    assert store.is_package_path_referenced("packages/1.0.0.vtplugin")
    assert not store.is_package_path_referenced("packages/missing.vtplugin")
    assert store.delete_package_revision(
        "local:default",
        "com.example.summary",
        "sha256:1.0.0",
    )
    assert not store.delete_package_revision(
        "local:default",
        "com.example.summary",
        "sha256:1.0.0",
    )
    assert (
        store.delete_package_revisions(
            "local:default",
            "com.example.summary",
        )
        == 1
    )
    assert store.delete_installation("local:default", "com.example.summary")
    assert not store.delete_installation("local:default", "com.example.summary")
    store.close()


def test_private_settings_use_optimistic_revision_guard(tmp_path: Path) -> None:
    store = PluginProjectStore(tmp_path / "plugin-state.db")
    initial = _setting()
    with pytest.raises(PluginStoreConflictError):
        store.save_private_setting(initial, expected_revision=1)
    store.save_private_setting(initial, expected_revision=None)
    updated = initial.model_copy(
        update={
            "value": {"visible": ["number"]},
            "revision": 2,
        }
    )
    assert store.save_private_setting(updated, expected_revision=1) == updated
    with pytest.raises(PluginStoreConflictError):
        store.save_private_setting(
            updated.model_copy(update={"revision": 3}),
            expected_revision=1,
        )
    assert (
        store.delete_private_settings(
            "local:default",
            "com.example.summary",
        )
        == 1
    )
    store.close()


def test_in_memory_store_matches_durable_conflict_and_audit_semantics() -> None:
    store = InMemoryPluginStore()
    store.save_installation(_snapshot(), expected_revision=None)
    with pytest.raises(PluginStoreConflictError):
        store.save_installation(_snapshot(revision=2), expected_revision=0)

    store.save_private_setting(_setting(), expected_revision=None)
    with pytest.raises(PluginStoreConflictError):
        store.save_private_setting(_setting(revision=2), expected_revision=0)

    event = _audit(event_id="audit-memory")
    store.record_audit(event)
    assert store.list_audit(
        "local:default",
        "com.example.summary",
    ) == [event]
