"""E3 production deployment, acceptance and operations contracts.

E3 deploys a blank production Directus instance (greenfield — no legacy data),
applies the version-controlled schema/policies/Flows/extensions, and runs the
first-line acceptance. These contracts capture the bootstrap plan, acceptance
checklist, operations runbook and release approval.

All production secrets, admin tokens and DB credentials stay OUT of the client
package and repository; these contracts carry only the non-secret plan/checklist.
"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


def _camel_config() -> ConfigDict:
    return ConfigDict(
        extra="forbid",
        populate_by_name=True,
        alias_generator=to_camel,
    )


class CamelModel(BaseModel):
    model_config = _camel_config()


# ---------------------------------------------------------------------------
# Production bootstrap (E3 Task 1)
# ---------------------------------------------------------------------------


BootstrapState = Literal["planned", "applying", "applied", "failed"]


class ProductionBootstrapPlan(CamelModel):
    """The version-controlled production bootstrap plan.

    Carries the schema/policy/Flow/extension inventory to apply to a blank
    Directus instance. NO secrets or admin tokens.
    """

    directus_version: str = Field(min_length=1, max_length=64)
    schema_version: str = Field(min_length=1, max_length=64)
    capability_manifest_path: str = Field(default="", max_length=256)
    blueprint_path: str = Field(default="", max_length=256)
    extensions: list[str] = Field(default_factory=list, max_length=32)
    flows: list[str] = Field(default_factory=list, max_length=32)
    state: BootstrapState = "planned"


class BootstrapResult(CamelModel):
    """Result of applying the bootstrap plan."""

    state: BootstrapState
    collections_created: int = Field(default=0, ge=0)
    policies_created: int = Field(default=0, ge=0)
    roles_created: int = Field(default=0, ge=0)
    extensions_deployed: int = Field(default=0, ge=0)
    error: str | None = Field(default=None, max_length=1024)


# ---------------------------------------------------------------------------
# Acceptance checklist (E3 Tasks 2-3)
# ---------------------------------------------------------------------------


AcceptanceCategory = Literal[
    "auth",
    "permissions",
    "core",
    "batch",
    "collaboration",
    "content-versions",
    "insights",
    "asset-presets",
    "revision-revert",
    "disabled-features",
    "automation",
    "backup-restore",
]


class AcceptanceCheck(CamelModel):
    """One acceptance check item."""

    category: AcceptanceCategory
    name: str = Field(min_length=1, max_length=256)
    passed: bool = False
    evidence: str = Field(default="", max_length=1024)


class AcceptanceReport(CamelModel):
    """The full acceptance report for first-line approval."""

    checks: list[AcceptanceCheck] = Field(default_factory=list, max_length=256)
    p0_passed: bool = False
    p1_passed: bool = False
    disabled_features_confirmed: bool = False
    summary: str = Field(default="", max_length=2048)


# ---------------------------------------------------------------------------
# Operations runbook (E3 Task 5)
# ---------------------------------------------------------------------------


class OperationsRunbook(CamelModel):
    """The operations runbook for production.

    Documents deployment, schema apply, extension/Flow updates, backup/restore,
    monitoring and incident response. Carries NO credentials.
    """

    deployment_steps: list[str] = Field(default_factory=list, max_length=64)
    schema_apply_command: str = Field(default="", max_length=512)
    backup_strategy: str = Field(default="", max_length=512)
    rpo_rto: str = Field(default="", max_length=256)
    monitoring_endpoints: list[str] = Field(default_factory=list, max_length=32)
    incident_response: str = Field(default="", max_length=2048)


# ---------------------------------------------------------------------------
# Release approval (E3 Task 5)
# ---------------------------------------------------------------------------


class ReleaseApproval(CamelModel):
    """The first-line release approval.

    All P0/P1 closed, core features passed, optional feature status documented.
    """

    release_version: str = Field(min_length=1, max_length=32)
    approved: bool = False
    approved_at: str = Field(default="", max_length=64)
    conditions: list[str] = Field(default_factory=list, max_length=32)
    acceptance_report: AcceptanceReport = Field(default_factory=AcceptanceReport)


__all__ = [
    "AcceptanceCategory",
    "AcceptanceCheck",
    "AcceptanceReport",
    "BootstrapResult",
    "BootstrapState",
    "CamelModel",
    "OperationsRunbook",
    "ProductionBootstrapPlan",
    "ReleaseApproval",
]
