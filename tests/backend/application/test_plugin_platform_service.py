"""Code-plugin package lifecycle tests."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.application.plugin_package_lifecycle import PluginPackageInspection
from backend.application.plugin_platform_service import PluginPlatformService
from backend.application.plugin_registry import PluginRegistry, PluginRegistryError
from backend.contracts.plugin import (
    InstallPlan,
    PluginManifest,
    PluginPackageRevision,
    PluginSnapshot,
)
from backend.infrastructure.plugin_package_lifecycle import LocalPluginPackageLifecycle
from backend.infrastructure.plugin_store import InMemoryPluginStore, PluginStoreConflictError


def _write_plugin(root: Path, *, version: str = "1.0.0") -> None:
    (root / "ui").mkdir(parents=True)
    (root / "schemas").mkdir()
    (root / "manifest.json").write_text(
        json.dumps(
            {
                "$schema": "vibetable.plugin-manifest.v2",
                "pluginId": "com.example.reader",
                "version": version,
                "displayName": {"en": "Reader"},
                "compatibility": {
                    "minHostVersion": "0.5.1",
                    "pluginApi": "3.x",
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
                        "inputSchema": "schemas/input.json",
                        "outputSchema": "schemas/output.json",
                    }
                ],
                "ui": {
                    "customViews": [
                        {"viewId": "reader", "actionId": "read", "entry": "ui/index.html"}
                    ]
                },
            }
        ),
        encoding="utf-8",
    )
    (root / "ui" / "index.html").write_text("<!doctype html>", encoding="utf-8")
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
    return PluginPlatformService(
        store=store,
        registry=registry,
        package_lifecycle=LocalPluginPackageLifecycle(package_cache),
    )


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

    def retained_location(self, package_hash: str) -> str:
        return f"memory://{package_hash}"

    def is_available(self, retained_location: str) -> bool:
        return retained_location in self._packages

    def discard(self, retained_location: str) -> None:
        self.discard_calls.append(retained_location)
        self._packages.pop(retained_location, None)


@pytest.mark.asyncio
async def test_install_and_uninstall_use_package_lifecycle_tasks() -> None:
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    packages = InMemoryPluginPackageLifecycle()
    source_location = "memory://reader-source"
    package_hash = f"sha256:{'1' * 64}"
    manifest = PluginManifest.model_validate(
        {
            "$schema": "vibetable.plugin-manifest.v2",
            "pluginId": "com.example.reader",
            "version": "1.0.0",
            "displayName": {"en": "Reader"},
            "compatibility": {"minHostVersion": "0.5.1", "pluginApi": "3.x"},
            "permissions": {"data": [], "files": [], "privateStorage": False},
            "actions": [
                {
                    "actionId": "read",
                    "displayName": {"en": "Read"},
                    "mode": "local",
                    "risk": "read",
                }
            ],
            "ui": {
                "customViews": [{"viewId": "reader", "actionId": "read", "entry": "ui/index.html"}]
            },
        }
    )
    packages.add(source_location, manifest, package_hash)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        package_lifecycle=packages,
    )

    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=source_location,
        plan_id="plan-host-lifecycle",
    )
    await service.commit_install(
        project_key="local:default",
        plan=plan,
        project_revision="project-r1",
    )
    retained_location = f"memory://{package_hash}"
    revisions = await store.list_package_revisions("local:default", "com.example.reader")
    revision = revisions[0]
    current = await registry.get("local:default", "com.example.reader")
    assert current is not None
    assert current.installation_id
    with pytest.raises(PluginRegistryError, match="installation changed"):
        await service.uninstall(
            project_key=current.project_key,
            plugin_id=current.plugin_id,
            installation_id=current.installation_id,
            expected_revision=current.revision + 1,
            cleanup_private_settings=True,
        )
    assert packages.discard_calls == []
    assert await store.list_package_revisions(current.project_key, current.plugin_id) == revisions
    await service.uninstall(
        project_key="local:default",
        plugin_id="com.example.reader",
        cleanup_private_settings=True,
        installation_id=(await registry.get("local:default", "com.example.reader")).installation_id,
        expected_revision=(await registry.get("local:default", "com.example.reader")).revision,
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
        plan_id="plan-host-recheck",
    )
    installed = await service.commit_install(
        project_key="local:default",
        plan=plan,
        project_revision="project-r1",
    )

    assert installed.status == "disabled"
    revisions = await store.list_package_revisions(
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
        plan_id="plan-host-source-changed",
    )
    (source / "schemas" / "input.json").write_text(
        '{"type":"array"}',
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="changed"):
        await service.commit_install(
            project_key="local:default",
            plan=plan,
            project_revision="project-r1",
        )
    # There is no local ledger to consume: the same payload is re-checked
    # and rejected deterministically on every attempt.
    with pytest.raises(ValueError, match="changed"):
        await service.commit_install(
            project_key="local:default",
            plan=plan,
            project_revision="project-r1",
        )

    assert await store.list_installations("local:default") == []
    assert (
        await store.list_package_revisions(
            "local:default",
            "com.example.reader",
        )
        == []
    )


@pytest.mark.asyncio
async def test_duplicate_commit_fails_closed_without_local_compensation(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    first = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-duplicate-first",
    )
    await service.commit_install(
        project_key="local:default",
        plan=first,
        project_revision="project-r1",
    )
    first_revisions = await store.list_package_revisions(
        "local:default",
        "com.example.reader",
    )
    retained = first_revisions[0].local_path
    second = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-duplicate-second",
    )

    with pytest.raises(ValueError, match="upgrade plan"):
        await service.commit_install(
            project_key="local:default",
            plan=second,
            project_revision="project-r1",
        )

    # One atomic commit keeps the first installation and its package.
    installations = await store.list_installations("local:default")
    assert len(installations) == 1
    assert installations[0].plugin_id == "com.example.reader"
    assert Path(retained).is_file()


@pytest.mark.asyncio
async def test_install_lifecycle_records_one_authority_audit_per_transition(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    service = _service(store, package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-no-event",
    )
    await service.commit_install(
        project_key="local:default",
        plan=plan,
        project_revision="project-r1",
    )
    enabled = await registry.set_enabled(
        "local:default",
        "com.example.reader",
        True,
    )
    result = await service.uninstall(
        project_key="local:default",
        plugin_id="com.example.reader",
        cleanup_private_settings=True,
        installation_id=(await registry.get("local:default", "com.example.reader")).installation_id,
        expected_revision=(await registry.get("local:default", "com.example.reader")).revision,
    )

    assert enabled.status == "enabled"
    assert result.uninstalled
    assert [
        event.event_type for event in await store.list_audit("local:default", "com.example.reader")
    ] == ["install", "enable", "uninstall"]
    assert await registry.list("local:default") == []


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
        plan_id="plan-host-v1-rollback",
    )
    await service.commit_install(
        project_key="local:default",
        plan=first,
        project_revision="project-r1",
    )
    second = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r2",
        source_location=str(source_v2),
        plan_id="plan-host-v2-rollback",
    )
    upgraded = await service.upgrade(
        project_key="local:default",
        plugin_id="com.example.reader",
        plan=second,
        project_revision="project-r2",
    )
    rolled_back = await service.rollback(
        project_key="local:default",
        plugin_id="com.example.reader",
    )

    assert upgraded.version == "2.0.0"
    assert rolled_back.version == "1.0.0"
    revisions = await store.list_package_revisions("local:default", "com.example.reader")
    assert {item.version: item.state for item in revisions} == {
        "1.0.0": "current",
        "2.0.0": "rollback",
    }
    audit = await store.list_audit("local:default", "com.example.reader")
    assert audit[-1].event_type == "rollback"


@pytest.mark.asyncio
async def test_upgrade_records_one_authority_audit_per_transition(
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
        plan_id="plan-host-v1-hidden",
    )
    await service.commit_install(
        project_key="local:default",
        plan=first,
        project_revision="project-r1",
    )
    second = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r2",
        source_location=str(source_v2),
        plan_id="plan-host-v2-hidden",
    )
    await service.upgrade(
        project_key="local:default",
        plugin_id="com.example.reader",
        plan=second,
        project_revision="project-r2",
    )

    assert [
        event.event_type for event in await store.list_audit("local:default", "com.example.reader")
    ] == ["install", "upgrade"]


@pytest.mark.parametrize("cleanup_failure", [False, True])
async def test_atomic_upgrade_prunes_only_unreferenced_prior_cache_after_success(
    tmp_path: Path,
    caplog: pytest.LogCaptureFixture,
    cleanup_failure: bool,
) -> None:
    class Packages(InMemoryPluginPackageLifecycle):
        def discard(self, retained_location: str) -> None:
            if cleanup_failure:
                raise OSError("synthetic cleanup failure")
            super().discard(retained_location)

    packages = Packages()
    store = InMemoryPluginStore()
    service = PluginPlatformService(
        store=store,
        registry=PluginRegistry(store=store),
        package_lifecycle=packages,
    )
    first_path = ""
    for index in range(3):
        source = f"memory://source-{index}"
        manifest = _reader_manifest().model_copy(update={"version": f"{index + 1}.0.0"})
        packages.add(source, manifest, f"sha256:{str(index + 1) * 64}")
        plan = await service.inspect_install(
            project_key="local:default",
            project_revision="r1",
            source_location=source,
            plan_id=f"p-{index}",
        )
        if index == 0:
            result = await service.commit_install(
                project_key="local:default", plan=plan, project_revision="r1"
            )
            first_path = packages.retained_location(result.package_hash)
        else:
            result = await service.upgrade(
                project_key="local:default",
                plugin_id=manifest.plugin_id,
                plan=plan,
                project_revision="r1",
            )
    assert result.version == "3.0.0"
    assert result.revision == 3
    assert packages.discard_calls == ([] if cleanup_failure else [first_path])
    assert ("local cache cleanup deferred" in caplog.text) == cleanup_failure


@pytest.mark.parametrize("transition", ["uninstall", "reinstall", "revision"])
async def test_upgrade_plan_cannot_follow_a_changed_installation(
    tmp_path: Path, transition: str
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    service = _service(store, package_cache=tmp_path / "cache")
    args = {
        "project_key": "local:default",
        "project_revision": "r1",
        "source_location": str(source),
    }
    first = await service.inspect_install(**args, plan_id="first")
    installed = await service.commit_install(
        project_key="local:default", plan=first, project_revision="r1"
    )
    replacement_source = tmp_path / "reader-next"
    _write_plugin(replacement_source, version="2.0.0")
    bound = await service.inspect_install(
        **{**args, "source_location": str(replacement_source)}, plan_id="bound"
    )
    assert bound.target_installation_id == installed.installation_id
    assert bound.target_revision == installed.revision
    if transition == "revision":
        await store.save_installation(
            installed.model_copy(update={"revision": 2}), expected_revision=1
        )
    else:
        await store.delete_installation(
            "local:default",
            installed.plugin_id,
            installation_id=installed.installation_id or "",
            expected_revision=1,
            cleanup_private_settings=True,
        )
        if transition == "reinstall":
            await service.commit_install(
                project_key="local:default", plan=first, project_revision="r1"
            )
    before = await store.get_installation("local:default", installed.plugin_id)
    revisions = await store.list_package_revisions("local:default", installed.plugin_id)
    with pytest.raises((PluginStoreConflictError, PluginRegistryError)):
        await service.upgrade(
            project_key="local:default",
            plugin_id=installed.plugin_id,
            plan=bound,
            project_revision="r1",
        )
    assert await store.get_installation("local:default", installed.plugin_id) == before
    assert await store.list_package_revisions("local:default", installed.plugin_id) == revisions
    # Only the unreferenced candidate is removed; authority and retained revisions are untouched.
    assert len(list((tmp_path / "cache").glob("*.vtplugin"))) == 1
    with pytest.raises(ValueError, match="upgrade plan"):
        await service.commit_install(project_key="local:default", plan=bound, project_revision="r1")


@pytest.mark.parametrize("failure", ["rejected", "reference-read", "committed-ack-lost"])
async def test_upgrade_failure_cleans_only_unreferenced_candidate_and_keeps_original_error(
    tmp_path: Path, failure: str
) -> None:
    original = RuntimeError("synthetic original commit error")

    class Store(InMemoryPluginStore):
        async def commit_upgrade(
            self, plan: InstallPlan, *, package_revision: PluginPackageRevision
        ) -> PluginSnapshot:
            if failure == "committed-ack-lost":
                await super().commit_upgrade(plan, package_revision=package_revision)
            raise original

        async def is_package_path_referenced(self, local_path: str) -> bool:
            if failure == "reference-read":
                raise OSError("authority reference read failed")
            return await super().is_package_path_referenced(local_path)

    first, second = tmp_path / "first", tmp_path / "second"
    _write_plugin(first)
    _write_plugin(second, version="2.0.0")
    store = Store()
    service = _service(store, package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(first),
        plan_id="first",
    )
    await service.commit_install(project_key="local:default", project_revision="r1", plan=plan)
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="r1",
        source_location=str(second),
        plan_id="second",
    )
    with pytest.raises(RuntimeError) as error:
        await service.upgrade(
            project_key="local:default",
            plugin_id=plan.manifest.plugin_id,
            project_revision="r1",
            plan=plan,
        )
    assert error.value is original
    assert len(list((tmp_path / "cache").glob("*.vtplugin"))) == (1 if failure == "rejected" else 2)
    installed = await store.get_installation("local:default", plan.manifest.plugin_id)
    assert installed is not None
    assert installed.version == ("2.0.0" if failure == "committed-ack-lost" else "1.0.0")


class _CommitCleanupProbeStore(InMemoryPluginStore):
    """Fails the atomic commit and the reference query to probe cleanup."""

    async def commit_install(
        self, plan: InstallPlan, *, package_revision: PluginPackageRevision
    ) -> PluginSnapshot:
        raise PluginRegistryError("plugin is already installed", code="plugin_already_installed")

    async def is_package_path_referenced(self, local_path: str) -> bool:
        raise RuntimeError("shared catalog is unavailable")


@pytest.mark.asyncio
async def test_commit_failure_keeps_original_error_and_retained_package(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = _CommitCleanupProbeStore()
    registry = PluginRegistry(store=store)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        package_lifecycle=LocalPluginPackageLifecycle(tmp_path / "cache"),
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-commit-failure",
    )

    with pytest.raises(PluginRegistryError) as error:
        await service.commit_install(
            project_key="local:default",
            plan=plan,
            project_revision="project-r1",
        )

    # The original commit error survives; the failing reference query never
    # masks it, and the retained package stays cached as the safest outcome.
    assert error.value.code == "plugin_already_installed"
    cached = list((tmp_path / "cache").glob("*.vtplugin"))
    assert len(cached) == 1


@pytest.mark.asyncio
async def test_upgrade_identity_failure_never_reaches_the_source(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    packages = InMemoryPluginPackageLifecycle()
    packages.add(str(source), _reader_manifest(), f"sha256:{'5' * 64}")
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        package_lifecycle=packages,
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-upgrade-identity",
    )

    with pytest.raises(ValueError, match="identity"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.other",
            plan=plan,
            project_revision="project-r1",
        )
    # The top-level identity check precedes any source access: no ledger was
    # consumed, nothing was retained and the store stayed untouched.
    assert packages.inspect_calls == [str(source)]
    assert packages.retain_calls == []
    assert await store.list_package_revisions("local:default", "com.example.reader") == []


@pytest.mark.asyncio
async def test_commit_executes_from_the_host_full_plan_without_a_local_ledger(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    packages = InMemoryPluginPackageLifecycle()
    packages.add(str(source), _reader_manifest(), f"sha256:{'2' * 64}")
    registry = PluginRegistry(store=store)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        package_lifecycle=packages,
    )

    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-1",
    )

    assert plan.plan_id == "plan-host-1"
    installed = await service.commit_install(
        project_key="local:default",
        plan=plan,
        project_revision="project-r1",
    )

    assert installed.status == "disabled"
    assert packages.retain_calls == [(str(source), plan.package_hash)]
    # Python keeps no independent acceptable-commit ledger and no remote
    # cancel registration: the WPF host owns plan admission and cancel.
    assert not hasattr(service, "cancel_install")
    assert not hasattr(service, "_consume_plan")


@pytest.mark.asyncio
async def test_commit_rejects_source_manifest_or_type_drift_beyond_hash(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    packages = InMemoryPluginPackageLifecycle()
    package_hash = f"sha256:{'3' * 64}"
    packages.add(str(source), _reader_manifest(), package_hash)
    registry = PluginRegistry(store=store)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        package_lifecycle=packages,
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-manifest",
    )

    # A tampered full-plan payload keeps the inspected hash but drifts the
    # manifest: a hash-only recheck would accept this forgery.
    tampered_manifest = plan.model_copy(
        update={"manifest": plan.manifest.model_copy(update={"version": "9.9.9"})},
    )
    with pytest.raises(ValueError, match="changed"):
        await service.commit_install(
            project_key="local:default",
            plan=tampered_manifest,
            project_revision="project-r1",
        )

    # Same payload drift for the source type.
    tampered_type = plan.model_copy(update={"source_type": "package"})
    with pytest.raises(ValueError, match="changed"):
        await service.commit_install(
            project_key="local:default",
            plan=tampered_type,
            project_revision="project-r1",
        )

    assert packages.retain_calls == []
    assert await store.list_installations("local:default") == []


@pytest.mark.asyncio
async def test_commit_rejects_top_level_identity_drift(tmp_path: Path) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    store = InMemoryPluginStore()
    packages = InMemoryPluginPackageLifecycle()
    packages.add(str(source), _reader_manifest(), f"sha256:{'4' * 64}")
    registry = PluginRegistry(store=store)
    service = PluginPlatformService(
        store=store,
        registry=registry,
        package_lifecycle=packages,
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-identity",
    )

    with pytest.raises(ValueError, match="revision"):
        await service.commit_install(
            project_key="local:default",
            plan=plan,
            project_revision="project-r2",
        )
    with pytest.raises(ValueError, match="project does not match"):
        await service.commit_install(
            project_key="local:other",
            plan=plan,
            project_revision="project-r1",
        )

    assert packages.retain_calls == []


@pytest.mark.asyncio
async def test_upgrade_rejects_foreign_plan_identity_at_execution(tmp_path: Path) -> None:
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
        plan_id="plan-host-v1",
    )
    await service.commit_install(
        project_key="local:default",
        plan=first,
        project_revision="project-r1",
    )
    second = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r2",
        source_location=str(source_v2),
        plan_id="plan-host-v2",
    )

    with pytest.raises(ValueError, match="identity"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.other",
            plan=second,
            project_revision="project-r2",
        )
    with pytest.raises(ValueError, match="revision"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.reader",
            plan=second,
            project_revision="project-r1",
        )

    revisions = await store.list_package_revisions("local:default", "com.example.reader")
    assert [item.version for item in revisions] == ["1.0.0"]


def _reader_manifest() -> PluginManifest:
    return PluginManifest.model_validate(
        {
            "$schema": "vibetable.plugin-manifest.v2",
            "pluginId": "com.example.reader",
            "version": "1.0.0",
            "displayName": {"en": "Reader"},
            "compatibility": {"minHostVersion": "0.5.1", "pluginApi": "3.x"},
            "permissions": {"data": [], "files": [], "privateStorage": False},
            "actions": [
                {
                    "actionId": "read",
                    "displayName": {"en": "Read"},
                    "mode": "local",
                    "risk": "read",
                }
            ],
            "ui": {
                "customViews": [{"viewId": "reader", "actionId": "read", "entry": "ui/index.html"}]
            },
        }
    )


@pytest.mark.asyncio
async def test_upgrade_source_failure_is_deterministic_without_a_ledger(
    tmp_path: Path,
) -> None:
    source = tmp_path / "reader"
    _write_plugin(source)
    service = _service(InMemoryPluginStore(), package_cache=tmp_path / "cache")
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-upgrade-source",
    )
    await service.commit_install(
        project_key="local:default", plan=plan, project_revision="project-r1"
    )
    plan = await service.inspect_install(
        project_key="local:default",
        project_revision="project-r1",
        source_location=str(source),
        plan_id="plan-host-upgrade-source-bound",
    )
    (source / "schemas" / "input.json").write_text('{"type":"array"}', encoding="utf-8")

    with pytest.raises(ValueError, match="changed"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.reader",
            plan=plan,
            project_revision="project-r1",
        )
    # No ledger exists to consume: the same payload fails the source recheck
    # deterministically on every attempt.
    with pytest.raises(ValueError, match="changed"):
        await service.upgrade(
            project_key="local:default",
            plugin_id="com.example.reader",
            plan=plan,
            project_revision="project-r1",
        )
