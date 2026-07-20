"""Versioned contracts for the Flow-first plugin platform."""

from __future__ import annotations

from datetime import UTC, datetime
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field


def _camel(value: str) -> str:
    first, *rest = value.split("_")
    return first + "".join(part[:1].upper() + part[1:] for part in rest)


class PluginContract(BaseModel):
    """Strict camel-case wire model shared by plugin use-case boundaries."""

    model_config = ConfigDict(
        alias_generator=_camel,
        populate_by_name=True,
        extra="forbid",
    )


PluginSourceType = Literal["package", "local-folder"]
PluginStatus = Literal["disabled", "enabled", "error"]
FlowOwnership = Literal["managed", "external"]
FlowTrigger = Literal["manual", "webhook", "schedule", "event"]
PluginRisk = Literal["read", "write", "destructive"]
FlowHealth = Literal["healthy", "missing", "incompatible", "drifted"]
InteractionDecision = Literal["approved", "rejected"]
PluginMode = Literal["flow", "local", "hybrid"]
PluginInvocation = Literal["manual", "webhook"]


class PluginAction(PluginContract):
    action_id: str = Field(min_length=1, max_length=128)
    display_name: dict[str, str] = Field(default_factory=dict)
    description: dict[str, str] = Field(default_factory=dict)
    mode: PluginMode
    risk: PluginRisk
    invocation: PluginInvocation = "manual"
    placements: list[str] = Field(default_factory=list)
    requires: dict[str, Any] = Field(default_factory=dict)
    entry_flow: str | None = None
    worker_entry: str | None = None
    form_schema: str | None = None
    input_schema: str | None = None
    output_schema: str | None = None


class PluginManifest(PluginContract):
    """Installed manifest snapshot.

    Rich action/Flow fields are introduced through subsequent vertical slices;
    the identity fields are already strict because they define the project-level
    single-instance key.
    """

    schema_id: Literal["vibetable.plugin-manifest.v1"] = Field(
        default="vibetable.plugin-manifest.v1", alias="$schema"
    )
    plugin_id: str = Field(min_length=3, max_length=255)
    version: str = Field(min_length=1, max_length=64)
    display_name: dict[str, str] = Field(default_factory=dict)
    description: dict[str, str] = Field(default_factory=dict)
    compatibility: dict[str, Any] = Field(default_factory=dict)
    permissions: dict[str, Any] = Field(default_factory=dict)
    actions: list[PluginAction] = Field(default_factory=list)
    flows: list[dict[str, Any]] = Field(default_factory=list)
    ui: dict[str, Any] = Field(default_factory=dict)


class FlowRequirement(PluginContract):
    """Logical Flow requirement declared by a package manifest."""

    logical_flow_id: str = Field(min_length=1, max_length=128)
    ownership: FlowOwnership
    trigger: FlowTrigger
    risk: PluginRisk
    contract_version: str = Field(min_length=1, max_length=64)
    requires_operations: list[str] = Field(default_factory=list)
    input_schema: dict[str, Any] = Field(default_factory=dict)
    output_schema: dict[str, Any] = Field(default_factory=dict)
    definition: dict[str, Any] | None = None


class InstallPlan(PluginContract):
    """Immutable installation preview approved by the host."""

    plan_id: str = Field(min_length=1, max_length=128)
    project_key: str = Field(min_length=1, max_length=2048)
    project_revision: str = Field(min_length=1, max_length=128)
    source_type: PluginSourceType
    source_location: str = Field(min_length=1, max_length=4096)
    package_hash: str = Field(min_length=1, max_length=128)
    manifest: PluginManifest
    flow_requirements: list[FlowRequirement] = Field(default_factory=list)
    schemas: dict[str, dict[str, Any]] = Field(default_factory=dict)


class PluginSnapshot(PluginContract):
    """Current project-local plugin instance returned by the Registry."""

    project_key: str
    plugin_id: str
    version: str
    package_hash: str
    source_type: PluginSourceType
    source_location: str
    development_source_location: str | None = None
    source_changed: bool = False
    manifest: PluginManifest
    flow_requirements: list[FlowRequirement] = Field(default_factory=list)
    flow_bindings: list[FlowBindingSnapshot] = Field(default_factory=list)
    schemas: dict[str, dict[str, Any]] = Field(default_factory=dict)
    status: PluginStatus
    disabled_reason: str | None = None
    blocking_reasons: list[str] = Field(default_factory=list)
    revision: int = Field(ge=1)


class PluginPackageRevision(PluginContract):
    """One locally retained, immutable package revision."""

    project_key: str
    plugin_id: str
    version: str
    package_hash: str
    local_path: str
    manifest: PluginManifest
    flow_bindings: list[FlowBindingSnapshot] = Field(default_factory=list)
    state: Literal["current", "rollback", "retired"]


class PluginPrivateSetting(PluginContract):
    """Plugin-private value with optimistic concurrency metadata."""

    project_key: str
    plugin_id: str
    setting_key: str
    value: Any
    revision: int = Field(ge=1)


class ExternalFlowAttestation(PluginContract):
    """User acknowledgement for facts Directus metadata cannot prove."""

    accepts_unknown_side_effects: bool = False


class FlowBindingSnapshot(PluginContract):
    """Project mapping from package logical Flow id to Directus UUID."""

    project_key: str
    plugin_id: str
    logical_flow_id: str
    ownership: FlowOwnership
    directus_flow_uuid: str
    rollback_flow_uuid: str | None = None
    rollback_contract_version: str | None = None
    rollback_definition_hash: str | None = None
    trigger_type: FlowTrigger
    contract_version: str
    installed_definition_hash: str | None = None
    observed_definition_hash: str
    revision: int = Field(ge=1)
    health: FlowHealth
    drift_status: Literal["clean", "drifted", "not-applicable"]
    last_error: str | None = None


class ExternalFlowCandidate(PluginContract):
    directus_flow_uuid: str
    name: str
    trigger_type: FlowTrigger
    status: Literal["active", "inactive"]
    operation_keys: list[str] = Field(default_factory=list)
    compatible: bool
    reasons: list[str] = Field(default_factory=list)


class ConfirmationPreview(PluginContract):
    summary: list[dict[str, Any]] = Field(default_factory=list)
    sample_rows: list[dict[str, Any]] = Field(default_factory=list)
    affected_count: int = Field(default=0, ge=0)
    warnings: list[str] = Field(default_factory=list)


class PendingConfirmation(PluginContract):
    interaction_id: str
    risk: PluginRisk
    title: str
    preview: ConfirmationPreview
    expires_at: float


class PluginProgress(PluginContract):
    current: int = Field(ge=0)
    total: int = Field(ge=0)
    message: str = ""
    cancellable: bool = False


class CancelFlag(PluginContract):
    cancel_requested: bool


class InteractionSnapshot(PluginContract):
    run_id: str
    project_key: str
    plugin_id: str
    action_id: str
    caller: str
    progress: PluginProgress | None = None
    pending_confirmation: PendingConfirmation | None = None
    cancel_requested: bool = False


class InteractionResolveResult(PluginContract):
    status: Literal["resolved", "already-resolved", "expired"]
    decision: InteractionDecision | None = None


class PluginFileRequest(PluginContract):
    request_id: str
    run_id: str
    project_key: str
    plugin_id: str
    action_id: str
    direction: Literal["read", "write"]
    media_types: list[str] = Field(default_factory=list)
    suggested_name: str | None = None
    media_type: str | None = None
    expires_at: float


class CommandContext(PluginContract):
    contract: Literal["vibetable.command-context.v1"] = "vibetable.command-context.v1"
    project_key: str
    collection: str | None = None
    selected_keys: list[str | int] = Field(default_factory=list)
    query_snapshot: dict[str, Any] | None = None
    locale: str = "zh-CN"
    theme: Literal["light", "dark"] = "light"
    density: str = "comfortable"
    user: dict[str, Any] = Field(default_factory=dict)
    host_version: str = "1.0.0"


class ActionAvailability(PluginContract):
    available: bool
    reasons: list[str] = Field(default_factory=list)


class PluginMetric(PluginContract):
    label: str
    value: str | int | float


class PluginResult(PluginContract):
    contract: Literal["vibetable.plugin-result.v1"] = "vibetable.plugin-result.v1"
    status: Literal["success", "warning", "error"]
    summary: str
    metrics: list[PluginMetric] = Field(default_factory=list)
    table: dict[str, Any] | None = None
    artifacts: list[dict[str, Any]] = Field(default_factory=list)
    refresh: dict[str, Any] | None = None
    warnings: list[str] = Field(default_factory=list)


class MutationOperation(PluginContract):
    kind: Literal["create", "update"]
    primary_key: str | int | None = None
    expected_date_updated: str | None = None
    values: dict[str, Any]


class MutationPlan(PluginContract):
    contract: Literal["vibetable.mutation-plan.v1"] = "vibetable.mutation-plan.v1"
    collection: str
    operations: list[MutationOperation] = Field(max_length=10_000)
    preview: ConfirmationPreview
    idempotency_key: str | None = None


class PluginSafeError(PluginContract):
    contract: Literal["vibetable.plugin-error.v1"] = "vibetable.plugin-error.v1"
    code: str
    message: str
    recoverability: Literal["retry", "rebind", "reconfigure", "reinstall", "none"]
    plugin_id: str | None = None
    action_id: str | None = None
    run_id: str | None = None
    details: dict[str, Any] = Field(default_factory=dict)
    cause_id: str | None = None


class PluginTaskSnapshot(PluginContract):
    task_id: str
    run_id: str
    plugin_id: str
    plugin_version: str
    action_id: str
    project_key: str
    collection: str | None = None
    target_count: int = Field(ge=0)
    risk: PluginRisk
    state: Literal["queued", "running", "succeeded", "failed", "cancelled", "aborted"]
    cancel_requested: bool = False
    progress: PluginProgress | None = None
    result: PluginResult | None = None
    error: PluginSafeError | None = None


class PluginEventEnvelope(PluginContract):
    contract: Literal["vibetable.plugin-event.v1"] = "vibetable.plugin-event.v1"
    event_type: Literal[
        "plugin.catalog.changed",
        "plugin.task.changed",
        "plugin.interaction.requested",
        "plugin.file.requested",
        "plugin.surface.message",
    ]
    project_key: str
    entity_id: str
    revision: int = Field(ge=1)
    snapshot: dict[str, Any]


class PluginAuditEvent(PluginContract):
    event_id: str
    project_key: str
    plugin_id: str
    plugin_version: str
    package_hash: str
    event_type: str
    outcome: str
    action_id: str | None = None
    run_id: str | None = None
    actor: str = "local-user"
    risk: PluginRisk | None = None
    target_collection: str | None = None
    target_count: int | None = None
    started_at: datetime = Field(default_factory=lambda: datetime.now(UTC).replace(microsecond=0))
    finished_at: datetime | None = None
    duration_ms: int | None = Field(default=None, ge=0)
    error_code: str | None = None
    details: dict[str, Any] = Field(default_factory=dict)


class FlowRemovalReport(PluginContract):
    managed_flows_removed: int = 0
    external_flows_unbound: int = 0


class UninstallResult(FlowRemovalReport):
    uninstalled: bool
    private_settings_retained: bool
    cleanup_pending: bool = False


__all__ = [
    "ActionAvailability",
    "CancelFlag",
    "CommandContext",
    "ConfirmationPreview",
    "ExternalFlowAttestation",
    "ExternalFlowCandidate",
    "FlowBindingSnapshot",
    "FlowHealth",
    "FlowOwnership",
    "FlowRemovalReport",
    "FlowRequirement",
    "FlowTrigger",
    "InstallPlan",
    "InteractionDecision",
    "InteractionResolveResult",
    "InteractionSnapshot",
    "MutationOperation",
    "MutationPlan",
    "PendingConfirmation",
    "PluginAction",
    "PluginAuditEvent",
    "PluginContract",
    "PluginEventEnvelope",
    "PluginFileRequest",
    "PluginInvocation",
    "PluginManifest",
    "PluginMetric",
    "PluginMode",
    "PluginPackageRevision",
    "PluginPrivateSetting",
    "PluginProgress",
    "PluginResult",
    "PluginRisk",
    "PluginSafeError",
    "PluginSnapshot",
    "PluginSourceType",
    "PluginStatus",
    "PluginTaskSnapshot",
    "UninstallResult",
]
