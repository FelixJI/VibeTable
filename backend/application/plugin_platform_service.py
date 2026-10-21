"""Closed package lifecycle for code plugins; business execution belongs to the Host."""

from __future__ import annotations

import asyncio
import contextlib
import logging
import uuid
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Protocol

from backend.application.plugin_package_lifecycle import PluginPackageLifecycle
from backend.application.plugin_registry import (
    PluginRegistry,
    PluginRegistryError,
    validate_install_plan,
)
from backend.contracts.plugin import (
    InstallPlan,
    PluginPackageRevision,
    PluginSnapshot,
    UninstallResult,
)


class PluginStorePort(Protocol):
    """The shared-state operations the platform orchestration needs."""

    async def commit_install(
        self,
        plan: InstallPlan,
        *,
        package_revision: PluginPackageRevision,
    ) -> PluginSnapshot: ...

    async def commit_upgrade(
        self, plan: InstallPlan, *, package_revision: PluginPackageRevision
    ) -> PluginSnapshot: ...

    async def commit_rollback(
        self, plan: InstallPlan, *, package_revision: PluginPackageRevision
    ) -> PluginSnapshot: ...

    async def set_enabled(
        self,
        project_key: str,
        plugin_id: str,
        enabled: bool,
        *,
        installation_id: str,
        expected_revision: int,
    ) -> PluginSnapshot: ...

    async def save_installation(
        self,
        snapshot: PluginSnapshot,
        *,
        expected_revision: int | None,
    ) -> PluginSnapshot: ...

    async def list_package_revisions(
        self,
        project_key: str,
        plugin_id: str,
    ) -> list[PluginPackageRevision]: ...

    async def save_package_revision(
        self,
        revision: PluginPackageRevision,
    ) -> PluginPackageRevision: ...

    async def delete_package_revision(
        self,
        project_key: str,
        plugin_id: str,
        package_hash: str,
    ) -> bool: ...

    async def delete_package_revisions(self, project_key: str, plugin_id: str) -> int: ...

    async def is_package_path_referenced(self, local_path: str) -> bool: ...


class PluginPlatformService:
    """Coordinates package validation and registry state.

    The Go-owned shared catalog answers the public catalog, audit and
    enable/disable surface; install commits are fixed atomic store
    operations, so this service never compensates with unconditional
    save/delete sequences.
    """

    def __init__(
        self,
        *,
        registry: PluginRegistry,
        store: PluginStorePort,
        package_lifecycle: PluginPackageLifecycle,
    ) -> None:
        self._registry = registry
        self._store = store
        self._package_lifecycle = package_lifecycle
        self._lifecycle_gates: dict[tuple[str, str], asyncio.Lock] = {}

    async def close(self) -> None:
        """No local resources: the Go-owned shared store owns its lifecycle."""

        return None

    async def inspect_install(
        self,
        *,
        project_key: str,
        project_revision: str,
        source_location: str,
        plan_id: str,
    ) -> InstallPlan:
        """Builds the plan under the host-generated identity.

        Python keeps no acceptable-plan ledger: admission, consumption,
        cancellation and invalidation are owned by the WPF host lease
        registry, so nothing is stored here and a plan that never reached the
        host cannot be committed later.
        """
        inspected = self._package_lifecycle.inspect(source_location)
        current = await self._registry.get(project_key, inspected.manifest.plugin_id)
        return InstallPlan(
            target_installation_id=current.installation_id if current else None,
            target_revision=current.revision if current else None,
            plan_id=plan_id,
            project_key=project_key,
            project_revision=project_revision,
            source_type=inspected.source_type,
            source_location=inspected.source_location,
            package_hash=inspected.package_hash,
            manifest=inspected.manifest,
            blocking_reasons=(
                []
                if inspected.manifest.schema_id == "vibetable.plugin-manifest.v2"
                and inspected.manifest.compatibility.get("pluginApi") == "3.x"
                else ["plugin_worker_retired"]
            ),
        )

    async def commit_install(
        self,
        *,
        project_key: str,
        plan: InstallPlan,
        project_revision: str,
    ) -> PluginSnapshot:
        self._verify_execution_identity(
            plan,
            project_key=project_key,
            plugin_id=None,
            project_revision=project_revision,
        )
        validate_install_plan(plan)
        if plan.target_installation_id is not None or plan.target_revision is not None:
            raise ValueError("upgrade plan cannot be committed as a new installation")
        self._recheck_plan_source(plan)
        retained_path = self._package_lifecycle.retain(
            source_location=plan.source_location,
            expected_hash=plan.package_hash,
        )
        try:
            # One fixed atomic store operation: installation identity,
            # the current package revision and the install audit event are
            # committed together and the durable catalog change is emitted by
            # the Go catalog, never duplicated here.
            installed = await self._store.commit_install(
                plan,
                package_revision=PluginPackageRevision(
                    project_key=plan.project_key,
                    plugin_id=plan.manifest.plugin_id,
                    version=plan.manifest.version,
                    package_hash=plan.package_hash,
                    local_path=retained_path,
                    manifest=plan.manifest,
                    state="current",
                ),
            )
        except Exception as commit_error:
            # Cleanup must never mask the original commit failure: if the
            # shared catalog cannot answer the reference query, the cached
            # package is kept (the safest outcome) and the original error is
            # re-raised unchanged.
            with contextlib.suppress(Exception):
                await self._delete_package_if_unreferenced(retained_path)
            raise commit_error
        return installed

    async def upgrade(
        self,
        *,
        project_key: str,
        plugin_id: str,
        plan: InstallPlan,
        project_revision: str,
    ) -> PluginSnapshot:
        self._verify_execution_identity(
            plan, project_key=project_key, plugin_id=plugin_id, project_revision=project_revision
        )
        async with self._lifecycle(
            project_key,
            plugin_id,
            installation_id=plan.target_installation_id or "",
            expected_revision=plan.target_revision or 0,
        ):
            return await self._upgrade(
                project_key=project_key,
                plugin_id=plugin_id,
                plan=plan,
                project_revision=project_revision,
            )

    async def _upgrade(
        self, *, project_key: str, plugin_id: str, plan: InstallPlan, project_revision: str
    ) -> PluginSnapshot:
        self._verify_execution_identity(
            plan,
            project_key=project_key,
            plugin_id=plugin_id,
            project_revision=project_revision,
        )
        validate_install_plan(plan)
        self._recheck_plan_source(plan)
        previous_paths = [
            item.local_path
            for item in await self._store.list_package_revisions(project_key, plugin_id)
        ]
        retained_path = self._package_lifecycle.retain(
            source_location=plan.source_location,
            expected_hash=plan.package_hash,
        )
        try:
            snapshot = await self._store.commit_upgrade(
                plan,
                package_revision=PluginPackageRevision(
                    project_key=project_key,
                    plugin_id=plugin_id,
                    version=plan.manifest.version,
                    package_hash=plan.package_hash,
                    local_path=retained_path,
                    manifest=plan.manifest,
                    state="current",
                ),
            )
        except Exception:
            # Only this retained candidate may be reclaimed, and only after a
            # fresh authority reference read. Cleanup cannot mask the original error.
            await self._cleanup_unreferenced_packages([retained_path])
            raise
        await self._cleanup_unreferenced_packages(previous_paths)
        return snapshot

    async def rollback(self, *, project_key: str, plugin_id: str) -> PluginSnapshot:
        target = await self._registry.get(project_key, plugin_id)
        if target is None:
            raise ValueError("plugin is not installed")
        async with self._lifecycle(
            project_key,
            plugin_id,
            installation_id=target.installation_id or "",
            expected_revision=target.revision,
        ):
            return await self._rollback(project_key=project_key, plugin_id=plugin_id)

    async def _rollback(self, *, project_key: str, plugin_id: str) -> PluginSnapshot:
        revisions = list(await self._store.list_package_revisions(project_key, plugin_id))
        current_revision = next(
            (item for item in revisions if item.state == "current"),
            None,
        )
        rollback_revision = next(
            (item for item in reversed(revisions) if item.state == "rollback"),
            None,
        )
        if current_revision is None or rollback_revision is None:
            raise ValueError("plugin rollback package is unavailable")
        rollback_path = rollback_revision.local_path
        if not self._package_lifecycle.is_available(rollback_path):
            raise ValueError("plugin rollback package is missing")
        inspected = self._package_lifecycle.inspect(rollback_path)
        if (
            inspected.package_hash != rollback_revision.package_hash
            or inspected.manifest != rollback_revision.manifest
        ):
            raise ValueError("plugin rollback package failed integrity verification")
        previous_installation = await self._registry.get(project_key, plugin_id)
        if previous_installation is None:
            raise ValueError("plugin is not installed")
        plan = InstallPlan(
            plan_id=f"plugin-rollback-{uuid.uuid4().hex[:12]}",
            target_installation_id=previous_installation.installation_id,
            target_revision=previous_installation.revision,
            project_key=project_key,
            project_revision=str(previous_installation.revision),
            source_type="package",
            source_location=rollback_path,
            package_hash=rollback_revision.package_hash,
            manifest=rollback_revision.manifest,
            schemas=previous_installation.schemas,
        )
        validate_install_plan(plan)
        snapshot = await self._store.commit_rollback(
            plan, package_revision=rollback_revision.model_copy(update={"state": "current"})
        )
        await self._cleanup_unreferenced_packages([item.local_path for item in revisions])
        return snapshot

    async def uninstall(
        self,
        *,
        project_key: str,
        plugin_id: str,
        cleanup_private_settings: bool,
        installation_id: str,
        expected_revision: int,
    ) -> UninstallResult:
        async with self._lifecycle(
            project_key,
            plugin_id,
            installation_id=installation_id,
            expected_revision=expected_revision,
        ):
            return await self._uninstall(
                project_key=project_key,
                plugin_id=plugin_id,
                cleanup_private_settings=cleanup_private_settings,
                installation_id=installation_id,
                expected_revision=expected_revision,
            )

    async def set_enabled_execution(
        self,
        *,
        project_key: str,
        plugin_id: str,
        enabled: bool,
        installation_id: str,
        expected_revision: int,
    ) -> PluginSnapshot:
        async with self._lifecycle(
            project_key,
            plugin_id,
            installation_id=installation_id,
            expected_revision=expected_revision,
        ) as current:
            if enabled and (
                current.manifest.schema_id != "vibetable.plugin-manifest.v2"
                or current.manifest.compatibility.get("pluginApi") != "3.x"
            ):
                raise PluginRegistryError(
                    "旧 Worker 插件需要迁移到 API 3.x。", code="plugin_worker_retired"
                )
            return await self._store.set_enabled(
                project_key,
                plugin_id,
                enabled,
                installation_id=installation_id,
                expected_revision=expected_revision,
            )

    async def _uninstall(
        self,
        *,
        project_key: str,
        plugin_id: str,
        cleanup_private_settings: bool,
        installation_id: str,
        expected_revision: int,
    ) -> UninstallResult:
        retained_paths = [
            revision.local_path
            for revision in await self._store.list_package_revisions(project_key, plugin_id)
        ]
        result = await self._registry.uninstall(
            project_key,
            plugin_id,
            cleanup_private_settings=cleanup_private_settings,
            installation_id=installation_id,
            expected_revision=expected_revision,
        )
        await self._cleanup_unreferenced_packages(retained_paths)
        return result

    @asynccontextmanager
    async def _lifecycle(
        self,
        project_key: str,
        plugin_id: str,
        *,
        installation_id: str,
        expected_revision: int,
    ) -> AsyncIterator[PluginSnapshot]:
        # Host drains surface tasks before this package transition; Python owns no business task.
        async with self._lifecycle_gates.setdefault((project_key, plugin_id), asyncio.Lock()):
            current = await self._registry.get(project_key, plugin_id)
            if (
                not installation_id
                or current is None
                or current.installation_id != installation_id
                or current.revision != expected_revision
            ):
                raise PluginRegistryError(
                    "plugin installation changed", code="plugin_revision_conflict"
                )
            yield current

    def _verify_execution_identity(
        self,
        plan: InstallPlan,
        *,
        project_key: str,
        plugin_id: str | None,
        project_revision: str,
    ) -> None:
        """Cross-process payload consistency for the host-consumed plan.

        This is not a second admission authority: it only proves that the
        full plan received over the wire is the one the host lease held for
        this exact project, plugin and revision.
        """
        if plan.project_key != project_key:
            raise ValueError("plugin plan project does not match the request")
        if plan.project_revision != project_revision:
            raise ValueError("plugin project revision changed")
        if plugin_id is not None and plan.manifest.plugin_id != plugin_id:
            raise ValueError("upgrade plan identity does not match the installation")

    def _recheck_plan_source(self, plan: InstallPlan) -> None:
        checked = self._package_lifecycle.inspect(plan.source_location)
        if (
            checked.package_hash != plan.package_hash
            or checked.source_type != plan.source_type
            or checked.manifest != plan.manifest
        ):
            raise ValueError("plugin source changed after inspection")

    async def _cleanup_unreferenced_packages(self, previous_paths: list[str]) -> None:
        # Metadata pruning is atomic in Go. Local deletion is limited to exact
        # previous revision paths and a fresh authority reference check.
        for path in set(previous_paths):
            try:
                await self._delete_package_if_unreferenced(path)
            except Exception:
                # A committed package switch must never look retryable because
                # local disk cleanup failed; preserve the cache and evidence.
                logging.getLogger(__name__).warning(
                    "Plugin local cache cleanup deferred", exc_info=True
                )

    async def _delete_package_if_unreferenced(self, path: str) -> None:
        if await self._store.is_package_path_referenced(path):
            return
        self._package_lifecycle.discard(path)


__all__ = ["PluginPlatformService"]
