"""Closed JSON-RPC parameter contracts for plugin use cases."""

from __future__ import annotations

from typing import Any

from pydantic import Field

from backend.contracts.plugin import CommandContext, InteractionDecision, PluginContract


class PluginProjectParams(PluginContract):
    project_key: str


class InspectInstallParams(PluginContract):
    project_key: str
    project_revision: str
    source_location: str


class CommitInstallParams(PluginContract):
    plan_id: str
    project_revision: str


class CancelInstallParams(PluginContract):
    plan_id: str


class PluginIdentityParams(PluginContract):
    project_key: str
    plugin_id: str


class SetPluginEnabledParams(PluginIdentityParams):
    enabled: bool


class UpgradePluginParams(PluginIdentityParams):
    plan_id: str
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


class ResolvePluginInteractionParams(PluginContract):
    run_id: str
    interaction_id: str
    decision: InteractionDecision


class ResolvePluginFileParams(PluginContract):
    request_id: str
    selected_path: str | None = None


class PluginTaskParams(PluginContract):
    task_id: str


__all__ = [
    "CancelInstallParams",
    "CommitInstallParams",
    "DescribePluginActionParams",
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
    "UpgradePluginParams",
]
