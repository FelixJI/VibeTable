"""Retirement keeps package facts and installation transitions without an executor."""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

import pytest

from backend.application.plugin_registry import PluginRegistryError
from backend.contracts.plugin import PluginManifest, PluginPackageRevision, PluginPrivateSetting
from backend.infrastructure.plugin_store import InMemoryPluginStore
from tests.backend.application.test_plugin_platform_service import _service, _write_plugin


def _make_legacy(source: Path, *, worker_entry: bool) -> PluginManifest:
    path = source / "manifest.json"
    raw = json.loads(path.read_text(encoding="utf-8"))
    raw["$schema"] = "vibetable.plugin-manifest.v1"
    raw["compatibility"]["pluginApi"] = "2.x"
    if worker_entry:
        raw["actions"][0]["workerEntry"] = "old.js"
        (source / "old.js").write_text("throw new Error('must never run');", encoding="utf-8")
    path.write_text(json.dumps(raw), encoding="utf-8")
    return PluginManifest.model_validate(raw)


@pytest.mark.parametrize("worker_entry", [False, True])
async def test_old_inspection_preserves_metadata_and_forged_clear_cannot_install(
    tmp_path: Path, worker_entry: bool
) -> None:
    source = tmp_path / "legacy"
    _write_plugin(source)
    manifest = _make_legacy(source, worker_entry=worker_entry)
    original = (source / "manifest.json").read_bytes()
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(source),
        plan_id="old",
    )
    assert plan.manifest == manifest
    assert plan.blocking_reasons == ["plugin_worker_retired"]
    assert plan.model_dump(by_alias=True)["blockingReasons"] == ["plugin_worker_retired"]
    for candidate in (plan, plan.model_copy(update={"blocking_reasons": []})):
        with pytest.raises(PluginRegistryError) as error:
            await service.commit_install(
                project_key="local:default", plan=candidate, project_revision="r1"
            )
        assert error.value.code == "plugin_worker_retired"
    assert await store.list_installations("local:default") == []
    assert await store.list_audit("local:default", manifest.plugin_id) == []
    assert not (tmp_path / "cache").exists()
    assert (source / "manifest.json").read_bytes() == original


async def test_legacy_rollback_and_enable_refuse_without_losing_package_settings_or_audit(
    tmp_path: Path,
) -> None:
    source = tmp_path / "current"
    _write_plugin(source)
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(source),
        plan_id="new",
    )
    current = await service.commit_install(
        project_key="local:default", plan=plan, project_revision="r1"
    )
    setting = PluginPrivateSetting(
        project_key=current.project_key,
        plugin_id=current.plugin_id,
        setting_key="unknown-old-key",
        value={"keep": True},
        revision=1,
    )
    await store.save_private_setting(setting, expected_revision=None)
    legacy_source = tmp_path / "legacy"
    _write_plugin(legacy_source)
    legacy_manifest = _make_legacy(legacy_source, worker_entry=True)
    legacy = service._package_lifecycle.inspect(str(legacy_source))
    retained = service._package_lifecycle.retain(
        source_location=str(legacy_source), expected_hash=legacy.package_hash
    )
    await store.save_package_revision(
        PluginPackageRevision(
            project_key=current.project_key,
            plugin_id=current.plugin_id,
            version="0.9.0",
            package_hash=legacy.package_hash,
            local_path=retained,
            manifest=legacy_manifest,
            state="rollback",
        )
    )
    revisions = await store.list_package_revisions(current.project_key, current.plugin_id)
    audit = await store.list_audit(current.project_key, current.plugin_id)
    with pytest.raises(PluginRegistryError) as error:
        await service.rollback(project_key=current.project_key, plugin_id=current.plugin_id)
    assert error.value.code == "plugin_worker_retired"
    assert await store.get_installation(current.project_key, current.plugin_id) == current
    assert await store.list_package_revisions(current.project_key, current.plugin_id) == revisions
    assert Path(retained).is_file()
    assert (
        await store.get_private_setting(current.project_key, current.plugin_id, setting.setting_key)
        == setting
    )
    assert await store.list_audit(current.project_key, current.plugin_id) == audit
    old_current = current.model_copy(update={"manifest": legacy_manifest})
    await store.save_installation(old_current, expected_revision=current.revision)
    with pytest.raises(PluginRegistryError) as error:
        await service.set_enabled_execution(
            project_key=current.project_key,
            plugin_id=current.plugin_id,
            enabled=True,
            installation_id=current.installation_id or "",
            expected_revision=current.revision,
        )
    assert error.value.code == "plugin_worker_retired"
    assert await store.get_installation(current.project_key, current.plugin_id) == old_current


async def test_lifecycle_lock_rechecks_original_revision_after_waiting(tmp_path: Path) -> None:
    entered, release = asyncio.Event(), asyncio.Event()

    class Store(InMemoryPluginStore):
        async def set_enabled(
            self, project_key, plugin_id, enabled, *, installation_id, expected_revision
        ):
            entered.set()
            await release.wait()
            return await super().set_enabled(
                project_key,
                plugin_id,
                enabled,
                installation_id=installation_id,
                expected_revision=expected_revision,
            )

    source = tmp_path / "current"
    _write_plugin(source)
    store = Store()
    service = _service(store, package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(source),
        plan_id="new",
    )
    current = await service.commit_install(
        project_key="local:default", plan=plan, project_revision="r1"
    )
    identity = {
        "project_key": current.project_key,
        "plugin_id": current.plugin_id,
        "installation_id": current.installation_id or "",
        "expected_revision": current.revision,
    }
    enabling = asyncio.create_task(service.set_enabled_execution(**identity, enabled=True))
    await entered.wait()
    removing = asyncio.create_task(service.uninstall(**identity, cleanup_private_settings=True))
    await asyncio.sleep(0)
    assert not removing.done()
    release.set()
    enabled = await enabling
    with pytest.raises(PluginRegistryError) as error:
        await removing
    assert error.value.code == "plugin_revision_conflict"
    assert await store.get_installation(current.project_key, current.plugin_id) == enabled
    assert [
        event.event_type for event in await store.list_audit(current.project_key, current.plugin_id)
    ] == ["install", "enable"]
