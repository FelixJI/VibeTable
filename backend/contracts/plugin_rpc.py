"""Closed JSON-RPC parameter contracts for plugin use cases."""

from __future__ import annotations

from typing import Any

from pydantic import Field

from backend.contracts.plugin import (
    CommandContext,
    InstallPlan,
    InteractionDecision,
    PluginContract,
)
from backend.contracts.task import SessionPathGrant


class PluginProjectParams(PluginContract):
    project_key: str


class InspectInstallParams(PluginContract):
    """Public inspection DTO: the renderer never fills the native source."""

    project_key: str
    project_revision: str
    source_location: str


class InspectInstallExecutionParams(PluginContract):
    """Host-gateway inspection payload with the host-generated plan identity."""

    project_key: str
    project_revision: str
    source_location: str
    plan_id: str


class CommitInstallParams(PluginContract):
    """Public frozen renderer commit DTO: plan id plus revision only."""

    plan_id: str
    project_revision: str


class CommitInstallExecutionParams(PluginContract):
    """Host-gateway commit payload: the full plan from the consumed lease."""

    project_key: str
    plan: InstallPlan
    project_revision: str


class CancelInstallParams(PluginContract):
    """Public frozen renderer cancel DTO; the worker registration is retired."""

    plan_id: str


class PluginIdentityParams(PluginContract):
    project_key: str
    plugin_id: str


class SetPluginEnabledParams(PluginIdentityParams):
    enabled: bool


class UpgradePluginParams(PluginIdentityParams):
    """Public frozen renderer upgrade DTO: identity plus plan id and revision."""

    plan_id: str
    project_revision: str


class UpgradePluginExecutionParams(PluginContract):
    """Host-gateway upgrade payload: the full plan from the consumed lease."""

    project_key: str
    plugin_id: str
    plan: InstallPlan
    project_revision: str


class RollbackPluginParams(PluginIdentityParams):
    pass


class UninstallPluginParams(PluginIdentityParams):
    cleanup_private_settings: bool = False


class DescribePluginActionParams(PluginIdentityParams):
    action_id: str
    context: CommandContext


class StartPluginActionParams(DescribePluginActionParams):
    input_payload: dict[str, Any] = Field(default_factory=dict, alias="input")
    # Closed host-only execution identity: generated and registered by the
    # WPF host registry before the executor is invoked.
    task_id: str
    run_id: str


class ResolvePluginInteractionParams(PluginContract):
    run_id: str
    interaction_id: str
    decision: InteractionDecision


class ResolvePluginFileParams(PluginContract):
    request_id: str
    grant: SessionPathGrant | None = None


class PluginTaskParams(PluginContract):
    task_id: str


__all__ = [
    "CancelInstallParams",
    "CommitInstallExecutionParams",
    "CommitInstallParams",
    "DescribePluginActionParams",
    "InspectInstallExecutionParams",
    "InspectInstallParams",
    "PluginIdentityParams",
    "PluginProjectParams",
    "PluginTaskParams",
    "ResolvePluginFileParams",
    "ResolvePluginInteractionParams",
    "RollbackPluginParams",
    "SetPluginEnabledParams",
    "StartPluginActionParams",
    "UninstallPluginParams",
    "UpgradePluginExecutionParams",
    "UpgradePluginParams",
]
