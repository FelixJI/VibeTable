"""Logical Flow binding, ownership and contract health module."""

from __future__ import annotations

import contextlib
import hashlib
import json
from typing import Any, Protocol

from backend.contracts.plugin import (
    ExternalFlowAttestation,
    FlowBindingSnapshot,
    FlowRemovalReport,
    FlowRequirement,
    FlowTrigger,
)
from backend.infrastructure.directus_flow import DirectusFlowDefinition


class FlowBindingStore(Protocol):
    def save_binding(
        self, binding: FlowBindingSnapshot, *, expected_revision: int | None = None
    ) -> FlowBindingSnapshot: ...

    def get_binding(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> FlowBindingSnapshot | None: ...

    def list_bindings(self, project_key: str, plugin_id: str) -> list[FlowBindingSnapshot]: ...

    def delete_bindings(self, project_key: str, plugin_id: str) -> int: ...

    def delete_binding(self, project_key: str, plugin_id: str, logical_flow_id: str) -> bool: ...


class DirectusFlowReader(Protocol):
    async def read_flow(self, flow_uuid: str) -> DirectusFlowDefinition | None: ...

    async def create_inactive_flow(
        self, *, trigger: FlowTrigger, definition: dict[str, Any]
    ) -> str: ...

    async def create_operations(self, flow_uuid: str, operations: list[dict[str, Any]]) -> None: ...

    async def activate_flow(self, flow_uuid: str) -> None: ...

    async def deactivate_flow(self, flow_uuid: str) -> None: ...

    async def delete_flow(self, flow_uuid: str) -> None: ...


class FlowBindingError(Exception):
    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, str]:
        recovery = "rebind" if self.code.startswith("flow_") else "retry"
        if "drift" in self.code:
            recovery = "reconfigure"
        return {"code": self.code, "recoverability": recovery}


class FlowBindingManager:
    """Owns mapping and health without leaking Directus CRUD order to callers."""

    def __init__(self, *, store: FlowBindingStore, directus: DirectusFlowReader) -> None:
        self._store = store
        self._directus = directus

    async def bind_external(
        self,
        *,
        project_key: str,
        plugin_id: str,
        requirement: FlowRequirement,
        directus_uuid: str,
        attestation: ExternalFlowAttestation,
    ) -> FlowBindingSnapshot:
        if requirement.ownership != "external":
            raise FlowBindingError(
                "a managed Flow cannot be bound through the external lifecycle",
                code="flow_ownership_mismatch",
            )
        flow = await self._directus.read_flow(directus_uuid)
        if flow is None:
            raise FlowBindingError("external Flow does not exist", code="flow_missing")
        if flow.trigger != requirement.trigger:
            raise FlowBindingError(
                "external Flow trigger does not match the plugin contract",
                code="flow_contract_mismatch",
            )
        if flow.status != "active":
            raise FlowBindingError(
                "external Flow is not active",
                code="flow_contract_mismatch",
            )
        missing_operations = set(requirement.requires_operations) - set(flow.operation_keys)
        if missing_operations:
            raise FlowBindingError(
                "external Flow is missing required public Operations",
                code="flow_contract_mismatch",
            )
        if (
            requirement.risk in {"write", "destructive"}
            and not attestation.accepts_unknown_side_effects
        ):
            raise FlowBindingError(
                "external write Flow side effects require explicit user acknowledgement",
                code="external_flow_attestation_required",
            )
        observed_hash = _definition_hash(flow)
        current = self.resolve(project_key, plugin_id, requirement.logical_flow_id)
        return self._store.save_binding(
            FlowBindingSnapshot(
                project_key=project_key,
                plugin_id=plugin_id,
                logical_flow_id=requirement.logical_flow_id,
                ownership="external",
                directus_flow_uuid=directus_uuid,
                trigger_type=flow.trigger,
                contract_version=requirement.contract_version,
                observed_definition_hash=observed_hash,
                revision=1 if current is None else current.revision + 1,
                health="healthy",
                drift_status="not-applicable",
            ),
            expected_revision=None if current is None else current.revision,
        )

    def resolve(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> FlowBindingSnapshot | None:
        return self._store.get_binding(project_key, plugin_id, logical_flow_id)

    def _save_changes(
        self, binding: FlowBindingSnapshot, changes: dict[str, Any]
    ) -> FlowBindingSnapshot:
        if all(getattr(binding, key) == value for key, value in changes.items()):
            return binding
        updated = binding.model_copy(update={**changes, "revision": binding.revision + 1})
        return self._store.save_binding(updated, expected_revision=binding.revision)

    async def provision_managed(
        self,
        *,
        project_key: str,
        plugin_id: str,
        requirement: FlowRequirement,
        activate: bool = True,
    ) -> FlowBindingSnapshot:
        if requirement.ownership != "managed":
            raise FlowBindingError(
                "an external Flow cannot be provisioned as managed",
                code="flow_ownership_mismatch",
            )
        if requirement.definition is None:
            raise FlowBindingError(
                "managed Flow definition is missing",
                code="flow_definition_missing",
            )
        flow_uuid: str | None = None
        try:
            flow_uuid = await self._create_managed_draft(requirement)
            if activate:
                await self._directus.activate_flow(flow_uuid)
            installed = await self._directus.read_flow(flow_uuid)
            expected_status = "active" if activate else "inactive"
            if installed is None or installed.status != expected_status:
                raise FlowBindingError("managed Flow disappeared", code="flow_missing")
        except Exception as exc:
            if flow_uuid is not None:
                with contextlib.suppress(Exception):
                    await self._directus.delete_flow(flow_uuid)
            raise FlowBindingError(
                "managed Flow installation failed and was compensated",
                code="managed_flow_install_failed",
            ) from exc
        definition_hash = _definition_hash(installed)
        return self._store.save_binding(
            FlowBindingSnapshot(
                project_key=project_key,
                plugin_id=plugin_id,
                logical_flow_id=requirement.logical_flow_id,
                ownership="managed",
                directus_flow_uuid=flow_uuid,
                trigger_type=requirement.trigger,
                contract_version=requirement.contract_version,
                installed_definition_hash=definition_hash,
                observed_definition_hash=definition_hash,
                revision=1,
                health="healthy",
                drift_status="clean",
            )
        )

    async def _create_managed_draft(self, requirement: FlowRequirement) -> str:
        if requirement.definition is None:
            raise FlowBindingError(
                "managed Flow definition is missing",
                code="flow_definition_missing",
            )
        operations = requirement.definition.get("operations", [])
        if not isinstance(operations, list) or not all(
            isinstance(operation, dict) for operation in operations
        ):
            raise FlowBindingError(
                "managed Flow operations are invalid",
                code="flow_definition_invalid",
            )
        flow_uuid = await self._directus.create_inactive_flow(
            trigger=requirement.trigger,
            definition=requirement.definition,
        )
        try:
            await self._directus.create_operations(flow_uuid, operations)
            created = await self._directus.read_flow(flow_uuid)
            if (
                created is None
                or created.trigger != requirement.trigger
                or created.status != "inactive"
            ):
                raise FlowBindingError(
                    "managed Flow draft failed validation",
                    code="flow_contract_mismatch",
                )
        except Exception:
            with contextlib.suppress(Exception):
                await self._directus.delete_flow(flow_uuid)
            raise
        return flow_uuid

    async def detect_drift(self, project_key: str, plugin_id: str) -> list[FlowBindingSnapshot]:
        report: list[FlowBindingSnapshot] = []
        for binding in self._store.list_bindings(project_key, plugin_id):
            if binding.ownership != "managed":
                report.append(binding)
                continue
            try:
                flow = await self._directus.read_flow(binding.directus_flow_uuid)
            except Exception as exc:
                report.append(
                    self._save_changes(
                        binding,
                        {
                            "health": "incompatible",
                            "last_error": str(exc) or "managed Flow validation failed",
                        },
                    )
                )
                continue
            if flow is None:
                changes: dict[str, Any] = {
                    "health": "missing",
                    "last_error": "managed Flow is missing",
                }
            else:
                observed = _definition_hash(flow)
                drifted = observed != binding.installed_definition_hash
                changes = {
                    "observed_definition_hash": observed,
                    "drift_status": "drifted" if drifted else "clean",
                    "health": "drifted" if drifted else "healthy",
                    "last_error": None,
                }
            report.append(self._save_changes(binding, changes))
        return report

    async def set_automatic_enabled(self, project_key: str, plugin_id: str, enabled: bool) -> int:
        """Atomically toggle plugin-owned schedule/event Flows.

        Manual managed Flows and every external Flow remain untouched. If one
        Directus mutation fails, already changed automatic Flows are restored
        before the original error is propagated to the lifecycle transaction.
        """

        changed: list[str] = []
        try:
            for binding in self._store.list_bindings(project_key, plugin_id):
                if binding.ownership != "managed" or binding.trigger_type not in {
                    "schedule",
                    "event",
                }:
                    continue
                flow = await self._directus.read_flow(binding.directus_flow_uuid)
                if flow is None:
                    if not enabled:
                        # A missing automatic Flow is already stopped. Continue so
                        # every remaining automatic Flow is also deactivated.
                        continue
                    raise FlowBindingError(
                        f"managed automatic Flow {binding.logical_flow_id!r} is missing",
                        code="flow_missing",
                    )
                desired = "active" if enabled else "inactive"
                if flow.status == desired:
                    continue
                if enabled:
                    await self._directus.activate_flow(binding.directus_flow_uuid)
                else:
                    await self._directus.deactivate_flow(binding.directus_flow_uuid)
                changed.append(binding.directus_flow_uuid)
        except Exception:
            for flow_uuid in reversed(changed):
                with contextlib.suppress(Exception):
                    if enabled:
                        await self._directus.deactivate_flow(flow_uuid)
                    else:
                        await self._directus.activate_flow(flow_uuid)
            raise
        return len(changed)

    async def validate_external(
        self,
        *,
        project_key: str,
        plugin_id: str,
        requirement: FlowRequirement,
    ) -> FlowBindingSnapshot | None:
        """Revalidate an existing external binding without mutating its Flow."""

        current = self.resolve(project_key, plugin_id, requirement.logical_flow_id)
        if current is None:
            return None
        if current.ownership != "external" or requirement.ownership != "external":
            raise FlowBindingError(
                "Flow ownership changed; resolve ownership explicitly before upgrade",
                code="flow_ownership_mismatch",
            )
        try:
            flow = await self._directus.read_flow(current.directus_flow_uuid)
        except Exception as exc:
            return self._save_changes(
                current,
                {
                    "contract_version": requirement.contract_version,
                    "health": "incompatible",
                    "drift_status": "not-applicable",
                    "last_error": str(exc) or "external Flow validation failed",
                },
            )
        health = "healthy"
        error: str | None = None
        observed_hash = current.observed_definition_hash
        if flow is None:
            health = "missing"
            error = "external Flow is missing"
        else:
            observed_hash = _definition_hash(flow)
            if (
                flow.status != "active"
                or flow.trigger != requirement.trigger
                or set(requirement.requires_operations) - set(flow.operation_keys)
            ):
                health = "incompatible"
                error = "external Flow no longer satisfies the plugin contract"
        return self._save_changes(
            current,
            {
                "trigger_type": requirement.trigger,
                "contract_version": requirement.contract_version,
                "observed_definition_hash": observed_hash,
                "health": health,
                "drift_status": "not-applicable",
                "last_error": error,
            },
        )

    async def restore_managed(
        self,
        *,
        project_key: str,
        plugin_id: str,
        requirement: FlowRequirement,
        activate: bool = True,
    ) -> FlowBindingSnapshot:
        """Replace a drifted managed Flow with a fresh package-defined revision."""

        current = self.resolve(project_key, plugin_id, requirement.logical_flow_id)
        if current is None or current.ownership != "managed":
            raise FlowBindingError("managed Flow binding is missing", code="flow_unbound")
        new_uuid: str | None = None
        try:
            new_uuid = await self._create_managed_draft(requirement)
            if not activate:
                observed_current = await self._directus.read_flow(current.directus_flow_uuid)
                if observed_current is not None and observed_current.status == "active":
                    await self._directus.deactivate_flow(current.directus_flow_uuid)
            elif requirement.trigger in {"schedule", "event"}:
                await self._directus.deactivate_flow(current.directus_flow_uuid)
                await self._directus.activate_flow(new_uuid)
            else:
                await self._directus.activate_flow(new_uuid)
                await self._directus.deactivate_flow(current.directus_flow_uuid)
            active = await self._directus.read_flow(new_uuid)
            expected_status = "active" if activate else "inactive"
            if active is None or active.status != expected_status:
                raise FlowBindingError(
                    "restored managed Flow did not activate",
                    code="flow_contract_mismatch",
                )
        except Exception as exc:
            if new_uuid is not None:
                with contextlib.suppress(Exception):
                    await self._directus.delete_flow(new_uuid)
            if activate:
                with contextlib.suppress(Exception):
                    await self._directus.activate_flow(current.directus_flow_uuid)
            raise FlowBindingError(
                "managed Flow restore failed and the user revision was preserved",
                code="managed_flow_restore_failed",
            ) from exc
        definition_hash = _definition_hash(active)
        return self._store.save_binding(
            current.model_copy(
                update={
                    "directus_flow_uuid": new_uuid,
                    "rollback_flow_uuid": current.directus_flow_uuid,
                    "rollback_contract_version": current.contract_version,
                    "rollback_definition_hash": current.observed_definition_hash,
                    "trigger_type": requirement.trigger,
                    "contract_version": requirement.contract_version,
                    "installed_definition_hash": definition_hash,
                    "observed_definition_hash": definition_hash,
                    "revision": current.revision + 1,
                    "health": "healthy",
                    "drift_status": "clean",
                    "last_error": None,
                }
            ),
            expected_revision=current.revision,
        )

    def detach_managed(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> FlowBindingSnapshot:
        """Convert the current managed UUID to user-owned external lifecycle."""

        current = self.resolve(project_key, plugin_id, logical_flow_id)
        if current is None or current.ownership != "managed":
            raise FlowBindingError("managed Flow binding is missing", code="flow_unbound")
        detached = current.model_copy(
            update={
                "ownership": "external",
                "rollback_flow_uuid": None,
                "rollback_contract_version": None,
                "rollback_definition_hash": None,
                "installed_definition_hash": None,
                "revision": current.revision + 1,
                "health": "healthy",
                "drift_status": "not-applicable",
                "last_error": None,
            }
        )
        return self._store.save_binding(detached, expected_revision=current.revision)

    async def remove_requirement(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> FlowRemovalReport:
        """Remove one obsolete binding while never mutating an external Flow."""

        binding = self.resolve(project_key, plugin_id, logical_flow_id)
        if binding is None:
            return FlowRemovalReport()
        managed_removed = 0
        if binding.ownership == "managed":
            for flow_uuid in {binding.directus_flow_uuid, binding.rollback_flow_uuid}:
                if flow_uuid is None:
                    continue
                flow = await self._directus.read_flow(flow_uuid)
                if flow is not None:
                    if flow.status == "active":
                        await self._directus.deactivate_flow(flow_uuid)
                    await self._directus.delete_flow(flow_uuid)
                    managed_removed += 1
        self._store.delete_binding(project_key, plugin_id, logical_flow_id)
        return FlowRemovalReport(
            managed_flows_removed=managed_removed,
            external_flows_unbound=1 if binding.ownership == "external" else 0,
        )

    async def restore_requirement(
        self,
        *,
        project_key: str,
        plugin_id: str,
        requirement: FlowRequirement,
        previous_binding: FlowBindingSnapshot,
        activate: bool = True,
    ) -> FlowBindingSnapshot:
        """Compensate a removed requirement without assuming deletion was atomic."""

        current = self.resolve(project_key, plugin_id, requirement.logical_flow_id)
        if requirement.ownership == "external":
            if current is not None:
                return current
            return self._store.save_binding(previous_binding)
        if current is not None:
            flow = await self._directus.read_flow(current.directus_flow_uuid)
            if flow is not None:
                if activate and flow.status != "active":
                    await self._directus.activate_flow(current.directus_flow_uuid)
                elif not activate and flow.status == "active":
                    await self._directus.deactivate_flow(current.directus_flow_uuid)
                return current
            self._store.delete_binding(project_key, plugin_id, requirement.logical_flow_id)
        return await self.provision_managed(
            project_key=project_key,
            plugin_id=plugin_id,
            requirement=requirement,
            activate=activate,
        )

    async def upgrade_managed(
        self,
        *,
        project_key: str,
        plugin_id: str,
        requirement: FlowRequirement,
        activate_new: bool = True,
    ) -> FlowBindingSnapshot:
        current = self.resolve(project_key, plugin_id, requirement.logical_flow_id)
        if current is None:
            raise FlowBindingError("managed Flow binding is missing", code="flow_unbound")
        if current.ownership != "managed" or requirement.ownership != "managed":
            raise FlowBindingError(
                "external Flow lifecycle is user-owned",
                code="flow_ownership_mismatch",
            )
        observed = await self._directus.read_flow(current.directus_flow_uuid)
        if observed is None:
            raise FlowBindingError("managed Flow is missing", code="flow_missing")
        if _definition_hash(observed) != current.installed_definition_hash:
            raise FlowBindingError(
                "managed Flow has user changes; resolve drift before upgrade",
                code="managed_flow_drifted",
            )
        new_uuid: str | None = None
        old_deactivated = False
        try:
            new_uuid = await self._create_managed_draft(requirement)
            if not activate_new:
                if observed.status == "active":
                    await self._directus.deactivate_flow(current.directus_flow_uuid)
                    old_deactivated = True
            elif requirement.trigger in {"schedule", "event"}:
                await self._directus.deactivate_flow(current.directus_flow_uuid)
                old_deactivated = True
                await self._directus.activate_flow(new_uuid)
            else:
                await self._directus.activate_flow(new_uuid)
                await self._directus.deactivate_flow(current.directus_flow_uuid)
                old_deactivated = True
            active = await self._directus.read_flow(new_uuid)
            expected_status = "active" if activate_new else "inactive"
            if active is None or active.status != expected_status:
                raise FlowBindingError(
                    "managed Flow upgrade did not activate",
                    code="flow_contract_mismatch",
                )
        except Exception as exc:
            if new_uuid is not None:
                with contextlib.suppress(Exception):
                    candidate = await self._directus.read_flow(new_uuid)
                    if candidate is not None and candidate.status == "active":
                        await self._directus.deactivate_flow(new_uuid)
                    await self._directus.delete_flow(new_uuid)
            if old_deactivated:
                with contextlib.suppress(Exception):
                    await self._directus.activate_flow(current.directus_flow_uuid)
            raise FlowBindingError(
                "managed Flow upgrade failed and the previous revision was restored",
                code="managed_flow_upgrade_failed",
            ) from exc
        definition_hash = _definition_hash(active)
        return self._store.save_binding(
            FlowBindingSnapshot(
                project_key=project_key,
                plugin_id=plugin_id,
                logical_flow_id=requirement.logical_flow_id,
                ownership="managed",
                directus_flow_uuid=new_uuid,
                rollback_flow_uuid=current.directus_flow_uuid,
                rollback_contract_version=current.contract_version,
                rollback_definition_hash=current.installed_definition_hash,
                trigger_type=requirement.trigger,
                contract_version=requirement.contract_version,
                installed_definition_hash=definition_hash,
                observed_definition_hash=definition_hash,
                revision=current.revision + 1,
                health="healthy",
                drift_status="clean",
            ),
            expected_revision=current.revision,
        )

    async def rollback_managed(
        self,
        project_key: str,
        plugin_id: str,
        logical_flow_id: str,
        *,
        activate_restored: bool = True,
    ) -> FlowBindingSnapshot:
        current = self.resolve(project_key, plugin_id, logical_flow_id)
        if current is None or current.ownership != "managed":
            raise FlowBindingError("managed Flow binding is missing", code="flow_unbound")
        rollback_uuid = current.rollback_flow_uuid
        if rollback_uuid is None:
            raise FlowBindingError(
                "no managed Flow rollback revision is retained",
                code="flow_rollback_unavailable",
            )
        previous = await self._directus.read_flow(rollback_uuid)
        if previous is None:
            raise FlowBindingError(
                "retained managed Flow revision is missing",
                code="flow_rollback_missing",
            )
        current_flow = await self._directus.read_flow(current.directus_flow_uuid)
        if current_flow is not None and current_flow.status == "active":
            await self._directus.deactivate_flow(current.directus_flow_uuid)
        if activate_restored and previous.status != "active":
            await self._directus.activate_flow(rollback_uuid)
        elif not activate_restored and previous.status == "active":
            await self._directus.deactivate_flow(rollback_uuid)
        restored = await self._directus.read_flow(rollback_uuid)
        expected_status = "active" if activate_restored else "inactive"
        if restored is None or restored.status != expected_status:
            raise FlowBindingError(
                "retained managed Flow revision disappeared",
                code="flow_rollback_missing",
            )
        observed_hash = _definition_hash(restored)
        rolled_back = current.model_copy(
            update={
                "directus_flow_uuid": rollback_uuid,
                "rollback_flow_uuid": current.directus_flow_uuid,
                "contract_version": current.rollback_contract_version or current.contract_version,
                "installed_definition_hash": current.rollback_definition_hash or observed_hash,
                "observed_definition_hash": observed_hash,
                "rollback_contract_version": current.contract_version,
                "rollback_definition_hash": current.installed_definition_hash,
                "revision": current.revision + 1,
                "health": "healthy",
                "drift_status": "clean",
                "last_error": None,
            }
        )
        return self._store.save_binding(rolled_back, expected_revision=current.revision)

    async def remove_owned(self, project_key: str, plugin_id: str) -> FlowRemovalReport:
        managed_removed = 0
        external_unbound = 0
        removed_uuids: set[str] = set()
        for binding in self._store.list_bindings(project_key, plugin_id):
            if binding.ownership == "external":
                external_unbound += 1
                continue
            for flow_uuid in (binding.directus_flow_uuid, binding.rollback_flow_uuid):
                if flow_uuid is None or flow_uuid in removed_uuids:
                    continue
                flow = await self._directus.read_flow(flow_uuid)
                if flow is not None:
                    if flow.status == "active":
                        await self._directus.deactivate_flow(flow_uuid)
                    await self._directus.delete_flow(flow_uuid)
                    managed_removed += 1
                removed_uuids.add(flow_uuid)
        self._store.delete_bindings(project_key, plugin_id)
        return FlowRemovalReport(
            managed_flows_removed=managed_removed,
            external_flows_unbound=external_unbound,
        )


def _definition_hash(flow: DirectusFlowDefinition) -> str:
    canonical = json.dumps(
        {
            "trigger": flow.trigger,
            "definition": flow.definition,
        },
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(canonical).hexdigest()


__all__ = ["FlowBindingError", "FlowBindingManager"]
