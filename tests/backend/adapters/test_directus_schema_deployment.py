"""Tests for Directus schema diff/apply deployment (G0.3).

Validates the G0.3 gate requirements:
1. ``plan_diff`` is a pure function with zero writes.
2. Destructive actions (drop, type narrowing, NOT NULL without default)
   default to ``rejected``.
3. Plan hash prevents drift between preview and apply.
4. Dry-run mode writes nothing.
5. Schema rollback restores schema only (snapshot hash recorded).
"""

from __future__ import annotations

from typing import Any

import pytest

from backend.adapters.directus.schema_deployment import (
    SchemaDeployer,
    plan_diff,
    snapshot_hash,
)
from backend.contracts.schema_deployment import (
    ApplyOptions,
    DestructiveClassification,
    SchemaActionKind,
    SchemaActionType,
    SchemaDeploymentError,
)

# ---------------------------------------------------------------------------
# Snapshot builders
# ---------------------------------------------------------------------------


def _snapshot(collections: dict[str, list[dict[str, Any]]]) -> dict[str, Any]:
    """Build a synthetic Directus snapshot from a {name: [fields]} map."""
    return {
        "collections": [
            {
                "collection": name,
                "fields": fields,
                "meta": {"accountability": "all"},
            }
            for name, fields in collections.items()
        ]
    }


def _field(
    name: str,
    data_type: str = "string",
    nullable: bool = True,
    default: Any = None,
) -> dict[str, Any]:
    """Build a synthetic Directus field definition."""
    schema: dict[str, Any] = {
        "data_type": data_type,
        "is_nullable": nullable,
    }
    if default is not None:
        schema["default_value"] = default
    return {"field": name, "type": data_type, "schema": schema}


# ---------------------------------------------------------------------------
# Pure diff tests
# ---------------------------------------------------------------------------


def test_plan_diff_new_collection_is_safe() -> None:
    current = _snapshot({"vibetable_contracts": [_field("id", "uuid")]})
    desired = _snapshot(
        {"vibetable_contracts": [_field("id", "uuid")], "vibetable_demo": [_field("id", "uuid")]}
    )
    plan = plan_diff(current, desired)
    creates = [a for a in plan.actions if a.action == SchemaActionType.CREATE]
    assert len(creates) == 1
    assert creates[0].target == "vibetable_demo"
    assert creates[0].classification == DestructiveClassification.SAFE
    assert not plan.has_rejected


def test_plan_diff_new_field_is_safe() -> None:
    current = _snapshot({"vibetable_contracts": [_field("id", "uuid")]})
    desired = _snapshot({"vibetable_contracts": [_field("id", "uuid"), _field("title", "string")]})
    plan = plan_diff(current, desired)
    field_creates = [
        a
        for a in plan.actions
        if a.kind == SchemaActionKind.FIELD and a.action == SchemaActionType.CREATE
    ]
    assert len(field_creates) == 1
    assert field_creates[0].target == "vibetable_contracts.title"
    assert field_creates[0].classification == DestructiveClassification.SAFE


def test_plan_diff_drop_collection_is_rejected() -> None:
    current = _snapshot(
        {"vibetable_contracts": [_field("id", "uuid")], "vibetable_old": [_field("id", "uuid")]}
    )
    desired = _snapshot({"vibetable_contracts": [_field("id", "uuid")]})
    plan = plan_diff(current, desired)
    drops = [a for a in plan.actions if a.action == SchemaActionType.DROP]
    assert len(drops) == 1
    assert drops[0].target == "vibetable_old"
    assert drops[0].classification == DestructiveClassification.REJECTED
    assert plan.has_rejected


def test_plan_diff_drop_field_is_rejected() -> None:
    current = _snapshot({"vibetable_contracts": [_field("id", "uuid"), _field("legacy", "string")]})
    desired = _snapshot({"vibetable_contracts": [_field("id", "uuid")]})
    plan = plan_diff(current, desired)
    field_drops = [
        a
        for a in plan.actions
        if a.kind == SchemaActionKind.FIELD and a.action == SchemaActionType.DROP
    ]
    assert len(field_drops) == 1
    assert field_drops[0].target == "vibetable_contracts.legacy"
    assert field_drops[0].classification == DestructiveClassification.REJECTED


def test_plan_diff_type_widening_is_safe() -> None:
    current = _snapshot({"vibetable_items": [_field("count", "integer")]})
    desired = _snapshot({"vibetable_items": [_field("count", "bigInteger")]})
    plan = plan_diff(current, desired)
    updates = [a for a in plan.actions if a.action == SchemaActionType.UPDATE]
    assert len(updates) == 1
    assert updates[0].classification == DestructiveClassification.SAFE


def test_plan_diff_type_narrowing_is_rejected() -> None:
    current = _snapshot({"vibetable_items": [_field("count", "bigInteger")]})
    desired = _snapshot({"vibetable_items": [_field("count", "integer")]})
    plan = plan_diff(current, desired)
    updates = [a for a in plan.actions if a.action == SchemaActionType.UPDATE]
    assert len(updates) == 1
    assert updates[0].classification == DestructiveClassification.REJECTED
    assert "narrowing" in updates[0].reason


def test_plan_diff_incompatible_type_is_rejected() -> None:
    current = _snapshot({"vibetable_items": [_field("data", "string")]})
    desired = _snapshot({"vibetable_items": [_field("data", "boolean")]})
    plan = plan_diff(current, desired)
    updates = [a for a in plan.actions if a.action == SchemaActionType.UPDATE]
    assert len(updates) == 1
    assert updates[0].classification == DestructiveClassification.REJECTED


def test_plan_diff_not_null_without_default_is_rejected() -> None:
    current = _snapshot({"vibetable_items": [_field("label", "string", nullable=True)]})
    desired = _snapshot({"vibetable_items": [_field("label", "string", nullable=False)]})
    plan = plan_diff(current, desired)
    updates = [a for a in plan.actions if a.action == SchemaActionType.UPDATE]
    assert len(updates) == 1
    assert updates[0].classification == DestructiveClassification.REJECTED
    assert "NOT NULL" in updates[0].reason


def test_plan_diff_not_null_with_default_is_safe() -> None:
    current = _snapshot({"vibetable_items": [_field("label", "string", nullable=True)]})
    desired = _snapshot(
        {"vibetable_items": [_field("label", "string", nullable=False, default="untitled")]}
    )
    plan = plan_diff(current, desired)
    updates = [a for a in plan.actions if a.action == SchemaActionType.UPDATE]
    assert len(updates) == 1
    assert updates[0].classification == DestructiveClassification.SAFE


def test_plan_diff_identical_snapshots_produce_empty_plan() -> None:
    snap = _snapshot({"vibetable_contracts": [_field("id", "uuid")]})
    plan = plan_diff(snap, snap)
    assert plan.actions == []
    assert plan.summary["total"] == 0
    assert not plan.has_rejected


# ---------------------------------------------------------------------------
# Plan hash stability
# ---------------------------------------------------------------------------


def test_plan_hash_is_stable() -> None:
    """The same diff always produces the same hash."""
    current = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    desired = _snapshot({"vibetable_a": [_field("id", "uuid")], "vibetable_b": [_field("id", "uuid")]})
    plan1 = plan_diff(current, desired)
    plan2 = plan_diff(current, desired)
    assert plan1.plan_hash == plan2.plan_hash
    assert len(plan1.plan_hash) == 64  # SHA-256 hex


def test_different_diffs_have_different_hashes() -> None:
    base = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    plan1 = plan_diff(base, _snapshot({"vibetable_a": [_field("id", "uuid")], "vibetable_b": []}))
    plan2 = plan_diff(base, _snapshot({"vibetable_a": [_field("id", "uuid")], "vibetable_c": []}))
    assert plan1.plan_hash != plan2.plan_hash


# ---------------------------------------------------------------------------
# Snapshot hash
# ---------------------------------------------------------------------------


def test_snapshot_hash_is_stable() -> None:
    snap = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    assert snapshot_hash(snap) == snapshot_hash(snap)
    assert len(snapshot_hash(snap)) == 64


# ---------------------------------------------------------------------------
# Apply tests (with FakeTransport)
# ---------------------------------------------------------------------------


class FakeTransport:
    def __init__(self, responses: list[Any] | None = None) -> None:
        self.responses = list(responses or [])
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if self.responses:
            return self.responses.pop(0)
        return {"data": {}}


@pytest.mark.asyncio
async def test_dry_run_writes_nothing() -> None:
    """Dry-run mode must not issue any write requests."""
    current = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    desired = _snapshot({"vibetable_a": [_field("id", "uuid")], "vibetable_b": [_field("id", "uuid")]})
    plan = plan_diff(current, desired)
    transport = FakeTransport()
    deployer = SchemaDeployer(transport, admin_token="admin-token")
    result = await deployer.apply(
        plan,
        ApplyOptions(plan_hash=plan.plan_hash, dry_run=True),
    )
    assert result.dry_run is True
    assert len(result.applied) == 1  # the safe create
    # No write requests (GET /schema/snapshot is skipped in dry_run).
    write_requests = [r for r in transport.requests if r["method"] != "GET"]
    assert write_requests == []


@pytest.mark.asyncio
async def test_apply_rejects_plan_drift() -> None:
    """Applying with a wrong plan_hash must raise SchemaDeploymentError."""
    current = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    desired = _snapshot({"vibetable_a": [_field("id", "uuid")], "vibetable_b": [_field("id", "uuid")]})
    plan = plan_diff(current, desired)
    transport = FakeTransport()
    deployer = SchemaDeployer(transport, admin_token="admin-token")
    with pytest.raises(SchemaDeploymentError, match="plan hash mismatch"):
        await deployer.apply(
            plan,
            ApplyOptions(plan_hash="wrong-hash", dry_run=True),
        )


@pytest.mark.asyncio
async def test_apply_skips_rejected_destructive_actions() -> None:
    """Without allow_destructive, rejected actions are skipped, not applied."""
    current = _snapshot({"vibetable_a": [_field("id", "uuid")], "vibetable_old": [_field("id", "uuid")]})
    desired = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    plan = plan_diff(current, desired)
    transport = FakeTransport(responses=[{"data": _snapshot({})}])  # snapshot response
    deployer = SchemaDeployer(transport, admin_token="admin-token")
    result = await deployer.apply(
        plan,
        ApplyOptions(plan_hash=plan.plan_hash, dry_run=False),
    )
    # The drop is rejected -> skipped, not applied.
    assert len(result.skipped) == 1
    assert result.skipped[0].target == "vibetable_old"
    assert len(result.applied) == 0
    assert any("rejected" in e for e in result.errors)


@pytest.mark.asyncio
async def test_apply_creates_safe_collection() -> None:
    """A safe collection create is applied."""
    current = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    desired = _snapshot({"vibetable_a": [_field("id", "uuid")], "vibetable_b": [_field("id", "uuid")]})
    plan = plan_diff(current, desired)
    transport = FakeTransport(responses=[{"data": _snapshot({})}])
    deployer = SchemaDeployer(transport, admin_token="admin-token")
    result = await deployer.apply(
        plan,
        ApplyOptions(plan_hash=plan.plan_hash, dry_run=False),
    )
    assert len(result.applied) == 1
    assert result.applied[0].target == "vibetable_b"
    # A POST /collections should have been issued.
    posts = [r for r in transport.requests if r["method"] == "POST"]
    assert any("/collections" in r["path"] for r in posts)


@pytest.mark.asyncio
async def test_apply_records_pre_apply_snapshot_hash() -> None:
    """Non-dry-run apply records the pre-apply snapshot hash for rollback."""
    current = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    desired = _snapshot({"vibetable_a": [_field("id", "uuid")], "vibetable_b": [_field("id", "uuid")]})
    plan = plan_diff(current, desired)
    snap = _snapshot({"vibetable_a": [_field("id", "uuid")]})
    transport = FakeTransport(responses=[{"data": snap}])
    deployer = SchemaDeployer(transport, admin_token="admin-token")
    result = await deployer.apply(
        plan,
        ApplyOptions(plan_hash=plan.plan_hash, dry_run=False),
    )
    assert result.pre_apply_snapshot_hash == snapshot_hash(snap)
    assert len(result.pre_apply_snapshot_hash) == 64


def test_deployer_requires_admin_token() -> None:
    with pytest.raises(ValueError, match="admin token"):
        SchemaDeployer(FakeTransport(), admin_token="")
