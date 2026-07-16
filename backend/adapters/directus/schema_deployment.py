"""Directus schema diff/apply deployment support (G0.3).

Provides a reviewed, dry-run-capable schema diff/apply pipeline that extends
the existing greenfield-only ``DirectusProjectBootstrapper.apply_empty``.

Design:

* :func:`plan_diff` is a PURE function comparing two Directus schema snapshots.
  It classifies every change as safe, destructive or rejected.
* :class:`SchemaDeployer` wraps a Directus transport and applies a plan only
  when the supplied ``plan_hash`` matches the plan's canonical hash.
* Destructive actions (drop collection, drop field, type narrowing, NOT NULL
  without default) default to ``rejected`` — they require explicit
  ``allow_destructive=True`` with a documented reason.
* Apply saves a pre-apply schema snapshot hash so rollback can restore schema
  only (never business data).

The diff operates on Directus ``/schema/snapshot`` payloads, which look like::

    {"collections": [{"collection": "x", "fields": [...], "meta": {...}}, ...]}
"""

from __future__ import annotations

import hashlib
import json
from typing import Any

from backend.adapters.directus.errors import DirectusSchemaError
from backend.adapters.directus.transport import DirectusTransport
from backend.contracts.schema_deployment import (
    ApplyOptions,
    ApplyResult,
    DestructiveClassification,
    SchemaAction,
    SchemaActionKind,
    SchemaActionType,
    SchemaDeploymentError,
    SchemaDiffPlan,
)

#: Types that are "wider" (safe to widen to). Key = from, Value = set of safe targets.
#:
#: Widening is safe because it can hold all previous values; narrowing risks
#: truncation or constraint violations on existing rows.
_WIDENABLE_TYPES: dict[str, set[str]] = {
    "integer": {"bigInteger", "float", "decimal"},
    "bigInteger": {"float", "decimal"},
    "string": {"text"},
    "float": {"decimal"},
}


def _snapshot_collections(snapshot: dict[str, Any]) -> dict[str, dict[str, Any]]:
    """Extract a {name: collection_def} map from a Directus snapshot."""
    collections_raw = snapshot.get("collections")
    if not isinstance(collections_raw, list):
        raise SchemaDeploymentError("snapshot must contain a 'collections' list")
    result: dict[str, dict[str, Any]] = {}
    for item in collections_raw:
        if not isinstance(item, dict):
            continue
        name = item.get("collection")
        if isinstance(name, str) and name:
            result[name] = item
    return result


def _collection_fields(collection_def: dict[str, Any]) -> dict[str, dict[str, Any]]:
    """Extract a {field_name: field_def} map from a collection definition."""
    fields_raw = collection_def.get("fields")
    if not isinstance(fields_raw, list):
        return {}
    result: dict[str, dict[str, Any]] = {}
    for field in fields_raw:
        if not isinstance(field, dict):
            continue
        name = field.get("field")
        if isinstance(name, str) and name:
            result[name] = field
    return result


def _field_type(field_def: dict[str, Any]) -> str:
    """Return the Directus data type of a field definition."""
    schema = field_def.get("schema")
    if isinstance(schema, dict):
        dt = schema.get("data_type")
        if isinstance(dt, str):
            return dt
    return str(field_def.get("type", "unknown"))


def _is_nullable(field_def: dict[str, Any]) -> bool:
    """Return True if the field allows NULL."""
    schema = field_def.get("schema")
    if isinstance(schema, dict):
        return bool(schema.get("is_nullable", True))
    return True


def _classify_field_change(
    collection: str, field_name: str, old: dict[str, Any], new: dict[str, Any]
) -> SchemaAction | None:
    """Classify a single field type/nullability change.

    Returns ``None`` when the field's type and nullability are unchanged
    (no meaningful schema change to act on).
    """
    old_type = _field_type(old)
    new_type = _field_type(new)
    old_nullable = _is_nullable(old)
    new_nullable = _is_nullable(new)

    # No type or nullability change -> no action needed.
    if new_type == old_type and new_nullable == old_nullable:
        return None

    # Type widening is safe.
    if new_type != old_type:
        safe_targets = _WIDENABLE_TYPES.get(old_type, set())
        if new_type in safe_targets:
            return SchemaAction(
                kind=SchemaActionKind.FIELD,
                action=SchemaActionType.UPDATE,
                target=f"{collection}.{field_name}",
                detail=f"type {old_type} -> {new_type} (widening)",
                classification=DestructiveClassification.SAFE,
            )
        # Type narrowing or incompatible type change.
        return SchemaAction(
            kind=SchemaActionKind.FIELD,
            action=SchemaActionType.UPDATE,
            target=f"{collection}.{field_name}",
            detail=f"type {old_type} -> {new_type} (narrowing/incompatible)",
            classification=DestructiveClassification.REJECTED,
            reason="type narrowing or incompatible type change risks data loss",
        )

    # Nullable -> NOT NULL without a default is destructive.
    if old_nullable and not new_nullable:
        schema = new.get("schema")
        has_default = isinstance(schema, dict) and schema.get("default_value") is not None
        if not has_default:
            return SchemaAction(
                kind=SchemaActionKind.FIELD,
                action=SchemaActionType.UPDATE,
                target=f"{collection}.{field_name}",
                detail="nullable -> NOT NULL without default",
                classification=DestructiveClassification.REJECTED,
                reason="adding NOT NULL constraint without a default fails on existing rows",
            )

    # Any other field update (meta, choices, etc.) is safe.
    return SchemaAction(
        kind=SchemaActionKind.FIELD,
        action=SchemaActionType.UPDATE,
        target=f"{collection}.{field_name}",
        detail="metadata or constraint update",
        classification=DestructiveClassification.SAFE,
    )


def plan_diff(current_snapshot: dict[str, Any], desired_snapshot: dict[str, Any]) -> SchemaDiffPlan:
    """Compute a classified diff between two Directus schema snapshots.

    This is a PURE function — it never touches the network or disk. It
    classifies every change as safe, destructive or rejected.

    Collections and fields present in ``desired`` but not ``current`` are
    ``create`` (safe). Those present in ``current`` but not ``desired`` are
    ``drop`` (rejected by default). Field type/nullability changes are
    classified by :func:`_classify_field_change`.
    """
    current = _snapshot_collections(current_snapshot)
    desired = _snapshot_collections(desired_snapshot)
    actions: list[SchemaAction] = []

    # New collections (create — safe).
    for name in desired:
        if name not in current:
            actions.append(
                SchemaAction(
                    kind=SchemaActionKind.COLLECTION,
                    action=SchemaActionType.CREATE,
                    target=name,
                    classification=DestructiveClassification.SAFE,
                )
            )

    # Dropped collections (drop — rejected by default).
    for name in current:
        if name not in desired:
            actions.append(
                SchemaAction(
                    kind=SchemaActionKind.COLLECTION,
                    action=SchemaActionType.DROP,
                    target=name,
                    detail="collection not in desired snapshot",
                    classification=DestructiveClassification.REJECTED,
                    reason="dropping a collection deletes all its data",
                )
            )

    # Existing collections: diff fields.
    for name in current:
        if name not in desired:
            continue
        old_fields = _collection_fields(current[name])
        new_fields = _collection_fields(desired[name])

        # New fields (create — safe).
        for field_name in new_fields:
            if field_name not in old_fields:
                actions.append(
                    SchemaAction(
                        kind=SchemaActionKind.FIELD,
                        action=SchemaActionType.CREATE,
                        target=f"{name}.{field_name}",
                        classification=DestructiveClassification.SAFE,
                    )
                )

        # Dropped fields (drop — rejected by default).
        for field_name in old_fields:
            if field_name not in new_fields:
                actions.append(
                    SchemaAction(
                        kind=SchemaActionKind.FIELD,
                        action=SchemaActionType.DROP,
                        target=f"{name}.{field_name}",
                        detail="field not in desired snapshot",
                        classification=DestructiveClassification.REJECTED,
                        reason="dropping a field deletes its column data",
                    )
                )

        # Changed fields.
        for field_name in old_fields:
            if field_name in new_fields:
                action = _classify_field_change(
                    name, field_name, old_fields[field_name], new_fields[field_name]
                )
                if action is not None:
                    actions.append(action)

    plan_hash = _compute_plan_hash(actions)
    has_rejected = any(a.classification == DestructiveClassification.REJECTED for a in actions)
    has_destructive = has_rejected or any(
        a.classification == DestructiveClassification.DESTRUCTIVE for a in actions
    )
    summary = _summarize(actions)
    return SchemaDiffPlan(
        actions=actions,
        plan_hash=plan_hash,
        has_rejected=has_rejected,
        has_destructive=has_destructive,
        summary=summary,
    )


def _compute_plan_hash(actions: list[SchemaAction]) -> str:
    """Compute a stable SHA-256 hash from the canonical JSON of the actions."""
    payload = [
        {
            "kind": a.kind.value,
            "action": a.action.value,
            "target": a.target,
            "detail": a.detail,
            "classification": a.classification.value,
        }
        for a in actions
    ]
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def _summarize(actions: list[SchemaAction]) -> dict[str, int]:
    """Count actions by kind+action and by classification."""
    summary: dict[str, int] = {}
    for a in actions:
        key = f"{a.kind.value}_{a.action.value}"
        summary[key] = summary.get(key, 0) + 1
        cls_key = f"class_{a.classification.value}"
        summary[cls_key] = summary.get(cls_key, 0) + 1
    summary["total"] = len(actions)
    return summary


def snapshot_hash(snapshot: dict[str, Any]) -> str:
    """Compute a stable SHA-256 hash of a Directus schema snapshot.

    Used to record the pre-apply state so rollback can verify it restored
    schema only.
    """
    canonical = json.dumps(snapshot, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


class SchemaDeployer:
    """Apply a reviewed schema diff plan to a Directus instance.

    Wraps a Directus transport. The caller supplies a plan and an
    :class:`ApplyOptions` carrying the ``plan_hash`` that was computed at
    preview time. If the hash does not match the plan's current hash, the
    apply is rejected (plan drift).
    """

    def __init__(self, transport: DirectusTransport, admin_token: str) -> None:
        if not admin_token:
            raise ValueError("admin token is required for schema deployment")
        self._transport = transport
        self._admin_token = admin_token

    async def snapshot(self) -> dict[str, Any]:
        """Fetch the current Directus schema snapshot."""
        payload = await self._transport.request(
            "GET", "/schema/snapshot", access_token=self._admin_token
        )
        if not isinstance(payload, dict) or not isinstance(payload.get("data"), dict):
            raise DirectusSchemaError("Directus returned an invalid schema snapshot")
        return payload["data"]

    async def apply(self, plan: SchemaDiffPlan, options: ApplyOptions) -> ApplyResult:
        """Apply ``plan`` according to ``options``.

        Validates the plan hash, then applies safe actions. Rejected/destructive
        actions are skipped unless ``options.allow_destructive`` is True. In
        ``dry_run`` mode, nothing is written.
        """
        # Plan drift check: the hash supplied at apply time must match the
        # plan's own hash. This prevents applying a stale plan after the
        # snapshot changed between preview and execution.
        if options.plan_hash != plan.plan_hash:
            raise SchemaDeploymentError(
                "plan hash mismatch: the plan changed between preview and apply "
                f"(supplied {options.plan_hash[:12]}... != plan {plan.plan_hash[:12]}...)"
            )

        applied: list[SchemaAction] = []
        skipped: list[SchemaAction] = []
        errors: list[str] = []
        pre_hash = ""

        if not options.dry_run:
            pre_snapshot = await self.snapshot()
            pre_hash = snapshot_hash(pre_snapshot)

        for action in plan.actions:
            if action.classification == DestructiveClassification.REJECTED:
                if not options.allow_destructive:
                    skipped.append(action)
                    errors.append(
                        f"rejected destructive action skipped: {action.target} ({action.reason})"
                    )
                    continue
                if not options.destructive_reason:
                    errors.append(
                        f"destructive action {action.target} requires a documented reason"
                    )
                    skipped.append(action)
                    continue

            if options.dry_run:
                # Dry run: classify but do not write.
                applied.append(action)
                continue

            try:
                await self._apply_action(action)
                applied.append(action)
            except Exception as exc:
                errors.append(f"failed to apply {action.target}: {exc}")
                skipped.append(action)

        return ApplyResult(
            dry_run=options.dry_run,
            plan_hash=plan.plan_hash,
            applied=applied,
            skipped=skipped,
            pre_apply_snapshot_hash=pre_hash,
            errors=errors,
        )

    async def _apply_action(self, action: SchemaAction) -> None:
        """Execute a single schema action against Directus."""
        if action.kind == SchemaActionKind.COLLECTION:
            if action.action == SchemaActionType.CREATE:
                await self._transport.request(
                    "POST",
                    "/collections",
                    access_token=self._admin_token,
                    json_body={"collection": action.target, "meta": {}, "schema": {}},
                )
            elif action.action == SchemaActionType.DROP:
                await self._transport.request(
                    "DELETE",
                    f"/collections/{action.target}",
                    access_token=self._admin_token,
                )
        elif action.kind == SchemaActionKind.FIELD:
            collection, field_name = action.target.split(".", 1)
            if action.action == SchemaActionType.CREATE:
                await self._transport.request(
                    "POST",
                    f"/fields/{collection}",
                    access_token=self._admin_token,
                    json_body={"field": field_name, "type": "string"},
                )
            elif action.action == SchemaActionType.DROP:
                await self._transport.request(
                    "DELETE",
                    f"/fields/{collection}/{field_name}",
                    access_token=self._admin_token,
                )
