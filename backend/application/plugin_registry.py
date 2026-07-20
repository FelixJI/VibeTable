"""Deep module for project-level plugin installation lifecycle."""

from __future__ import annotations

import builtins
import uuid
from collections.abc import Sequence
from typing import Protocol

from backend.contracts.plugin import (
    FlowBindingSnapshot,
    FlowRemovalReport,
    FlowRequirement,
    InstallPlan,
    PluginAuditEvent,
    PluginSnapshot,
    UninstallResult,
)


class PluginStore(Protocol):
    def get_installation(self, project_key: str, plugin_id: str) -> PluginSnapshot | None: ...

    def save_installation(
        self, snapshot: PluginSnapshot, *, expected_revision: int | None = None
    ) -> PluginSnapshot: ...

    def list_installations(self, project_key: str) -> list[PluginSnapshot]: ...

    def delete_installation(self, project_key: str, plugin_id: str) -> bool: ...

    def delete_private_settings(self, project_key: str, plugin_id: str) -> int: ...

    def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent: ...

    def list_audit(self, project_key: str, plugin_id: str) -> list[PluginAuditEvent]: ...


class BindingResolver(Protocol):
    def resolve(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> FlowBindingSnapshot | None: ...

    async def remove_owned(self, project_key: str, plugin_id: str) -> FlowRemovalReport: ...


class PluginRegistryError(Exception):
    """Safe Registry failure with a stable recovery-oriented code."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, str]:
        recovery = "reinstall" if "install" in self.code else "reconfigure"
        return {"code": self.code, "recoverability": recovery}


class PluginRegistry:
    """Owns current plugin instances for each normalized Directus project."""

    def __init__(self, *, store: PluginStore, bindings: BindingResolver | None = None) -> None:
        self._store = store
        self._bindings = bindings

    async def install(self, plan: InstallPlan) -> PluginSnapshot:
        """Commit one previously approved installation plan.

        Installing another current version is never an implicit upgrade: callers
        must use the upgrade use case so its Flow and rollback plan is visible.
        """
        plugin_id = plan.manifest.plugin_id
        if self._store.get_installation(plan.project_key, plugin_id) is not None:
            raise PluginRegistryError(
                f"plugin {plugin_id!r} is already installed in this project",
                code="plugin_already_installed",
            )
        blocking_reasons = self._blocking_reasons(
            plan.project_key, plugin_id, plan.flow_requirements
        )
        disabled_reason = blocking_reasons[0] if blocking_reasons else None
        return self._store.save_installation(
            PluginSnapshot(
                project_key=plan.project_key,
                plugin_id=plugin_id,
                version=plan.manifest.version,
                package_hash=plan.package_hash,
                source_type=plan.source_type,
                source_location=plan.source_location,
                manifest=plan.manifest,
                flow_requirements=plan.flow_requirements,
                schemas=plan.schemas,
                status="disabled" if disabled_reason else "enabled",
                disabled_reason=disabled_reason,
                blocking_reasons=blocking_reasons,
                revision=1,
            )
        )

    def get(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        return self._store.get_installation(project_key, plugin_id)

    def list(self, project_key: str) -> builtins.list[PluginSnapshot]:
        return self._store.list_installations(project_key)

    def blocking_reasons(self, project_key: str, plugin_id: str) -> builtins.list[str]:
        current = self._store.get_installation(project_key, plugin_id)
        if current is None:
            raise PluginRegistryError("plugin is not installed", code="plugin_not_installed")
        return self._blocking_reasons(project_key, plugin_id, current.flow_requirements)

    def blocking_reasons_for(
        self,
        project_key: str,
        plugin_id: str,
        requirements: Sequence[FlowRequirement],
    ) -> builtins.list[str]:
        """Evaluate a proposed topology before committing its manifest."""

        return self._blocking_reasons(project_key, plugin_id, requirements)

    def touch(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        """Advance the canonical snapshot after binding/source metadata changes."""

        current = self._store.get_installation(project_key, plugin_id)
        if current is None:
            raise PluginRegistryError("plugin is not installed", code="plugin_not_installed")
        updated = current.model_copy(update={"revision": current.revision + 1})
        return self._store.save_installation(updated, expected_revision=current.revision)

    async def set_enabled(
        self, *, project_key: str, plugin_id: str, enabled: bool
    ) -> PluginSnapshot:
        current = self._store.get_installation(project_key, plugin_id)
        if current is None:
            raise PluginRegistryError("plugin is not installed", code="plugin_not_installed")
        blocking_reasons: builtins.list[str] = []
        if enabled:
            blocking_reasons = self._blocking_reasons(
                project_key, plugin_id, current.flow_requirements
            )
            if blocking_reasons:
                raise PluginRegistryError(
                    f"plugin cannot be enabled: {'; '.join(blocking_reasons)}",
                    code="plugin_flow_invalid",
                )
        updated = current.model_copy(
            update={
                "status": "enabled" if enabled else "disabled",
                "disabled_reason": None if enabled else "user_disabled",
                "blocking_reasons": blocking_reasons if enabled else ["user_disabled"],
                "revision": current.revision + 1,
            }
        )
        return self._store.save_installation(updated, expected_revision=current.revision)

    def refresh_status(self, project_key: str, plugin_id: str) -> PluginSnapshot:
        """Reconcile whole-plugin availability after binding health refresh."""

        current = self._store.get_installation(project_key, plugin_id)
        if current is None:
            raise PluginRegistryError("plugin is not installed", code="plugin_not_installed")
        if current.disabled_reason == "user_disabled":
            return current
        reasons = self._blocking_reasons(project_key, plugin_id, current.flow_requirements)
        reason = reasons[0] if reasons else None
        target_status = "disabled" if reasons else "enabled"
        if (
            current.status == target_status
            and current.disabled_reason == reason
            and current.blocking_reasons == reasons
        ):
            return current
        updated = current.model_copy(
            update={
                "status": target_status,
                "disabled_reason": reason,
                "blocking_reasons": reasons,
                "revision": current.revision + 1,
            }
        )
        return self._store.save_installation(updated, expected_revision=current.revision)

    def adopt_external_flow(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> PluginSnapshot:
        """Persist the user's decision to stop managing one installed Flow."""

        current = self._store.get_installation(project_key, plugin_id)
        if current is None:
            raise PluginRegistryError("plugin is not installed", code="plugin_not_installed")
        requirements = [
            item.model_copy(update={"ownership": "external"})
            if item.logical_flow_id == logical_flow_id
            else item
            for item in current.flow_requirements
        ]
        updated = current.model_copy(
            update={"flow_requirements": requirements, "revision": current.revision + 1}
        )
        return self._store.save_installation(updated, expected_revision=current.revision)

    async def commit_upgrade(self, plan: InstallPlan) -> PluginSnapshot:
        current = self._store.get_installation(plan.project_key, plan.manifest.plugin_id)
        if current is None:
            raise PluginRegistryError("plugin is not installed", code="plugin_not_installed")
        blocking_reasons = self._blocking_reasons(
            plan.project_key, plan.manifest.plugin_id, plan.flow_requirements
        )
        disabled_reason = blocking_reasons[0] if blocking_reasons else None
        user_disabled = current.status == "disabled" and current.disabled_reason == "user_disabled"
        updated = current.model_copy(
            update={
                "version": plan.manifest.version,
                "package_hash": plan.package_hash,
                "source_type": plan.source_type,
                "source_location": plan.source_location,
                "manifest": plan.manifest,
                "flow_requirements": plan.flow_requirements,
                "schemas": plan.schemas,
                "status": "disabled" if user_disabled or disabled_reason else "enabled",
                "disabled_reason": "user_disabled" if user_disabled else disabled_reason,
                "blocking_reasons": ["user_disabled"] if user_disabled else blocking_reasons,
                "revision": current.revision + 1,
            }
        )
        return self._store.save_installation(updated, expected_revision=current.revision)

    async def uninstall(
        self,
        *,
        project_key: str,
        plugin_id: str,
        cleanup_private_settings: bool,
    ) -> UninstallResult:
        current = self._store.get_installation(project_key, plugin_id)
        if current is None:
            return await self._retry_uninstall_cleanup(
                project_key=project_key,
                plugin_id=plugin_id,
                cleanup_private_settings=cleanup_private_settings,
            )
        if current.status != "disabled":
            current = self._store.save_installation(
                current.model_copy(
                    update={
                        "status": "disabled",
                        "disabled_reason": "uninstalling",
                        "revision": current.revision + 1,
                    }
                ),
                expected_revision=current.revision,
            )
        cleanup_error: str | None = None
        try:
            removal = (
                await self._bindings.remove_owned(project_key, plugin_id)
                if self._bindings is not None
                else FlowRemovalReport()
            )
        except Exception as exc:
            # Local uninstall is authoritative. Retaining the binding rows is
            # the durable retry record for managed Directus resources.
            removal = FlowRemovalReport()
            cleanup_error = str(exc) or exc.__class__.__name__
        self._store.delete_installation(project_key, plugin_id)
        if cleanup_private_settings:
            self._store.delete_private_settings(project_key, plugin_id)
        self._store.record_audit(
            PluginAuditEvent(
                event_id=f"audit-{uuid.uuid4().hex}",
                project_key=project_key,
                plugin_id=plugin_id,
                plugin_version=current.version,
                package_hash=current.package_hash,
                event_type="uninstall",
                outcome="pending-cleanup" if cleanup_error else "succeeded",
                details={
                    "managedFlowsRemoved": removal.managed_flows_removed,
                    "externalFlowsUnbound": removal.external_flows_unbound,
                    "cleanupError": cleanup_error,
                },
            )
        )
        return UninstallResult(
            uninstalled=True,
            managed_flows_removed=removal.managed_flows_removed,
            external_flows_unbound=removal.external_flows_unbound,
            private_settings_retained=not cleanup_private_settings,
            cleanup_pending=cleanup_error is not None,
        )

    async def _retry_uninstall_cleanup(
        self,
        *,
        project_key: str,
        plugin_id: str,
        cleanup_private_settings: bool,
    ) -> UninstallResult:
        removal = FlowRemovalReport()
        cleanup_error: str | None = None
        try:
            if self._bindings is not None:
                removal = await self._bindings.remove_owned(project_key, plugin_id)
        except Exception as exc:
            cleanup_error = str(exc) or exc.__class__.__name__
        if cleanup_private_settings:
            self._store.delete_private_settings(project_key, plugin_id)
        previous = self._store.list_audit(project_key, plugin_id)
        latest = previous[-1] if previous else None
        pending = latest if latest is not None and latest.outcome == "pending-cleanup" else None
        if pending is not None:
            self._store.record_audit(
                PluginAuditEvent(
                    event_id=f"audit-{uuid.uuid4().hex}",
                    project_key=project_key,
                    plugin_id=plugin_id,
                    plugin_version=pending.plugin_version,
                    package_hash=pending.package_hash,
                    event_type="uninstall-cleanup-retry",
                    outcome="pending-cleanup" if cleanup_error else "succeeded",
                    details={
                        "managedFlowsRemoved": removal.managed_flows_removed,
                        "externalFlowsUnbound": removal.external_flows_unbound,
                        "cleanupError": cleanup_error,
                    },
                )
            )
        return UninstallResult(
            uninstalled=False,
            managed_flows_removed=removal.managed_flows_removed,
            external_flows_unbound=removal.external_flows_unbound,
            private_settings_retained=not cleanup_private_settings,
            cleanup_pending=cleanup_error is not None,
        )

    def _blocking_reasons(
        self, project_key: str, plugin_id: str, requirements: Sequence[FlowRequirement]
    ) -> builtins.list[str]:
        if not requirements:
            return []
        if self._bindings is None:
            return [f"flow_unbound:{item.logical_flow_id}" for item in requirements]
        reasons: builtins.list[str] = []
        for requirement in requirements:
            logical_id = requirement.logical_flow_id
            binding = self._bindings.resolve(project_key, plugin_id, logical_id)
            if binding is None:
                reasons.append(f"flow_unbound:{logical_id}")
                continue
            if binding.ownership != requirement.ownership:
                reasons.append(f"flow_ownership_mismatch:{logical_id}")
                continue
            if (
                binding.trigger_type != requirement.trigger
                or binding.contract_version != requirement.contract_version
            ):
                reasons.append(f"flow_contract_mismatch:{logical_id}")
                continue
            if binding.health not in ("healthy", "drifted"):
                reasons.append(f"flow_invalid:{logical_id}")
        return list(dict.fromkeys(reasons))


__all__ = ["PluginRegistry", "PluginRegistryError", "PluginStore"]
