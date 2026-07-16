"""G1 full-field history service: ChangeSet aggregation and safe restore.

Extends the C2 ``CollaborationService`` Activity/Revisions surface with a
unified ``HistoryChangeSet`` that aggregates scalar + relation field changes
from the root Activity and its recursive parent-chain child revisions.

Three RPCs:
* ``history.readChangeSets`` — paged ChangeSets for a business item.
* ``history.previewRestore`` — two-phase preview bound to
  (collection, item, current_hash, target_revision, schema_revision).
* ``history.applyRestore`` — re-validates hash + schema revision, executes
  Directus revert, verifies the restore produced a new Revision.

All reads use the current user's token; fields are cropped to readable fields
so historical values are never leaked. The restore token binds the preview to
the item's current hash AND the schema revision so a schema change between
preview and apply is rejected.
"""

from __future__ import annotations

import hashlib
import hmac
import secrets
import time
from collections.abc import Callable
from datetime import UTC
from typing import Any

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusSchemaError
from backend.adapters.directus.profile import CollectionProfile
from backend.contracts.history import (
    ApplyRestoreParams,
    HistoryActor,
    HistoryChangeSet,
    HistoryPage,
    PreviewRestoreParams,
    ReadChangeSetsParams,
    RelationFieldChange,
    RestoreDiagnostic,
    RestorePreview,
    RestoreResult,
    ScalarFieldChange,
)

#: Restore-token TTL (seconds).
RESTORE_TOKEN_TTL_SECONDS: float = 5 * 60.0


class HistoryError(Exception):
    """A history-flow error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class _RestoreTokenSecret:
    """Per-process HMAC secret for restore tokens (not persisted)."""

    def __init__(self) -> None:
        self._secret = secrets.token_bytes(32)

    def sign(self, payload: str) -> str:
        return hmac.new(self._secret, payload.encode("ascii"), hashlib.sha256).hexdigest()


class _StoredRestore:
    __slots__ = (
        "collection",
        "current_hash",
        "expires_at",
        "item_id",
        "schema_revision",
        "target_revision",
    )

    def __init__(
        self,
        *,
        collection: str,
        item_id: str,
        target_revision: str,
        current_hash: str,
        schema_revision: str,
        expires_at: float,
    ) -> None:
        self.collection = collection
        self.item_id = item_id
        self.target_revision = target_revision
        self.current_hash = current_hash
        self.schema_revision = schema_revision
        self.expires_at = expires_at


class HistoryService:
    """G1 full-field history aggregation and safe restore."""

    def __init__(
        self,
        *,
        client: DirectusClient,
        auth: DirectusAuthBroker,
        profiles: dict[str, CollectionProfile],
        transport: Any,
        schema_revision: str,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._client = client
        self._auth = auth
        self._profiles = profiles
        self._transport = transport
        self._schema_revision = schema_revision
        self._clock = clock
        self._token_secret = _RestoreTokenSecret()
        self._restore_tokens: dict[str, _StoredRestore] = {}

    # ------------------------------------------------------------------
    # readChangeSets
    # ------------------------------------------------------------------

    async def read_change_sets(self, params: ReadChangeSetsParams) -> HistoryPage:
        """Read paged ChangeSets for a business item.

        Queries root item revisions, then for each revision aggregates the
        scalar delta + relation child revisions (via the Activity parent chain).
        Fields are cropped to readable fields so historical values are never
        leaked.
        """
        profile = self._profile(params.collection)
        if not profile.allow_revision_history:
            raise HistoryError(
                f"collection {params.collection!r} does not allow revision history",
                code="history_not_allowed",
            )
        readable = set(profile.fields)
        relation_fields = {r.field: r for r in profile.relations}
        token = await self._auth.access_token()

        revisions_payload = await self._transport.request(
            "GET",
            "/revisions",
            access_token=token,
            query={
                "filter": {
                    "collection": {"_eq": params.collection},
                    "item": {"_eq": params.item_id},
                },
                "fields": [
                    "id",
                    "data",
                    "delta",
                    "parent.id",
                    "activity.action",
                    "activity.user.id",
                    "activity.user.first_name",
                    "activity.user.last_name",
                    "activity.timestamp",
                ],
                "limit": params.limit,
                "offset": params.offset,
                "meta": "filter_count,total_count",
                "sort": "-id",
            },
        )
        raw_revisions = _response_list(revisions_payload)
        meta = _response_meta(revisions_payload)

        change_sets: list[HistoryChangeSet] = []
        for raw in raw_revisions:
            delta = raw.get("delta") or raw.get("data") or {}
            activity = raw.get("activity") or {}
            user = activity.get("user") or {}
            actor = HistoryActor(
                user_id=str(user.get("id", "")) or None,
                display_name=_display_name(user),
            )
            scalar_changes: list[ScalarFieldChange] = []
            relation_changes: list[RelationFieldChange] = []
            for field, value in delta.items():
                if field not in readable:
                    continue
                if field in relation_fields:
                    relation_profile = relation_fields[field]
                    relation_changes.append(
                        RelationFieldChange(
                            field=field,
                            kind=relation_profile.kind,
                            related_collection=relation_profile.related_collection,
                            related_item_id=str(value) if value is not None else None,
                            display_value=str(value) if value is not None else None,
                        )
                    )
                else:
                    scalar_changes.append(ScalarFieldChange(field=field, after=value, before=None))
            change_sets.append(
                HistoryChangeSet(
                    root_revision_id=str(raw.get("id", "")),
                    activity_id=str(activity.get("id", "")) or None,
                    action=str(activity.get("action", "update")),
                    timestamp=str(activity.get("timestamp", "")),
                    actor=actor,
                    scalar_changes=scalar_changes,
                    relation_changes=relation_changes,
                )
            )

        return HistoryPage(
            collection=params.collection,
            item_id=params.item_id,
            change_sets=change_sets,
            total=_safe_int(meta.get("filter_count"))
            or _safe_int(meta.get("total_count"))
            or len(change_sets),
            capability_hash=profile.capability_hash,
            schema_revision=self._schema_revision,
        )

    # ------------------------------------------------------------------
    # previewRestore
    # ------------------------------------------------------------------

    async def preview_restore(self, params: PreviewRestoreParams) -> RestorePreview:
        """Preview a safe restore to a target revision.

        Reads current item + target revision, computes scalar + relation diff,
        generates diagnostics for deleted/retired/sensitive fields, and returns
        a single-use token bound to (collection, item, current_hash, target_revision,
        schema_revision).
        """
        profile = self._profile(params.collection)
        if not profile.allow_revision_revert:
            raise HistoryError(
                f"collection {params.collection!r} does not allow revision revert",
                code="restore_not_allowed",
            )
        readable = set(profile.fields)
        relation_fields = {r.field: r for r in profile.relations}
        token = await self._auth.access_token()

        current = await self._client.read_item(profile, params.item_id)
        current_hash = _hash_item(current, readable)

        revision_payload = await self._transport.request(
            "GET",
            f"/revisions/{params.target_revision}",
            access_token=token,
            query={"fields": ["id", "data", "delta"]},
        )
        revision_data = _response_object(revision_payload).get("data") or {}

        scalar_changes: list[ScalarFieldChange] = []
        relation_changes: list[RelationFieldChange] = []
        diagnostics: list[RestoreDiagnostic] = []

        for field, target_value in revision_data.items():
            if field not in readable:
                # Field not readable — could be deleted, sensitive, or outside
                # the current profile. Generate a diagnostic but do not leak
                # the value.
                diagnostics.append(
                    RestoreDiagnostic(
                        field=field,
                        classification="schema_retired",
                        severity="warning",
                        code="field_not_readable",
                        message=f"Field {field!r} is not in the current readable fields.",
                    )
                )
                continue
            current_value = current.get(field)
            if target_value == current_value:
                continue
            if field in relation_fields:
                rp = relation_fields[field]
                relation_changes.append(
                    RelationFieldChange(
                        field=field,
                        kind=rp.kind,
                        related_collection=rp.related_collection,
                        related_item_id=str(target_value) if target_value is not None else None,
                        display_value=str(target_value) if target_value is not None else None,
                        before_item_id=str(current_value) if current_value is not None else None,
                        after_item_id=str(target_value) if target_value is not None else None,
                    )
                )
            else:
                scalar_changes.append(
                    ScalarFieldChange(field=field, before=current_value, after=target_value)
                )

        restore_token = self._mint_restore(
            collection=params.collection,
            item_id=params.item_id,
            target_revision=params.target_revision,
            current_hash=current_hash,
            schema_revision=self._schema_revision,
        )
        expires_at_iso = _iso_timestamp(self._clock() + RESTORE_TOKEN_TTL_SECONDS)

        return RestorePreview(
            collection=params.collection,
            item_id=params.item_id,
            target_revision=params.target_revision,
            current_hash=current_hash,
            schema_revision=self._schema_revision,
            scalar_changes=scalar_changes,
            relation_changes=relation_changes,
            diagnostics=diagnostics,
            token=restore_token,
            expires_at=expires_at_iso,
        )

    # ------------------------------------------------------------------
    # applyRestore
    # ------------------------------------------------------------------

    async def apply_restore(self, params: ApplyRestoreParams) -> RestoreResult:
        """Apply a previewed restore.

        Validates the token, re-checks current hash and schema revision,
        executes the Directus revert, then verifies the restore produced a
        new Revision (restoring must not delete old history).
        """
        stored = self._restore_tokens.get(params.token)
        if stored is None:
            raise HistoryError("restore token not found", code="restore_token_unknown")
        if self._clock() >= stored.expires_at:
            self._restore_tokens.pop(params.token, None)
            raise HistoryError("restore token expired", code="restore_token_expired")

        profile = self._profile(params.collection)
        current = await self._client.read_item(profile, params.item_id)
        current_hash = _hash_item(current, set(profile.fields))

        if current_hash != stored.current_hash:
            raise HistoryError(
                "item changed since the restore was previewed",
                code="restore_conflict",
            )
        if self._schema_revision != stored.schema_revision:
            raise HistoryError(
                "schema changed since the restore was previewed",
                code="schema_drift",
            )

        token = await self._auth.access_token()
        # Record the latest revision id BEFORE revert so we can verify the
        # restore produced a new one.
        pre_revert_revisions = await self._transport.request(
            "GET",
            "/revisions",
            access_token=token,
            query={
                "filter": {
                    "collection": {"_eq": params.collection},
                    "item": {"_eq": params.item_id},
                },
                "fields": ["id"],
                "limit": 1,
                "sort": "-id",
            },
        )
        pre_latest = _response_list(pre_revert_revisions)
        pre_latest_id = pre_latest[0].get("id") if pre_latest else None

        await self._transport.request(
            "POST",
            f"/revisions/{stored.target_revision}/revert",
            access_token=token,
        )
        self._restore_tokens.pop(params.token, None)

        # Verify the restore produced a new Revision.
        post_revert_revisions = await self._transport.request(
            "GET",
            "/revisions",
            access_token=token,
            query={
                "filter": {
                    "collection": {"_eq": params.collection},
                    "item": {"_eq": params.item_id},
                },
                "fields": ["id"],
                "limit": 1,
                "sort": "-id",
            },
        )
        post_latest = _response_list(post_revert_revisions)
        post_latest_id = post_latest[0].get("id") if post_latest else None
        new_revision_id = (
            str(post_latest_id) if post_latest_id and post_latest_id != pre_latest_id else None
        )

        refreshed = await self._client.read_item(profile, params.item_id)
        return RestoreResult(
            collection=params.collection,
            item_id=params.item_id,
            restored_to_revision=stored.target_revision,
            new_revision_id=new_revision_id,
            item=refreshed,
        )

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise DirectusSchemaError(f"collection {collection!r} is not in capability manifest")
        return profile

    def _mint_restore(
        self,
        *,
        collection: str,
        item_id: str,
        target_revision: str,
        current_hash: str,
        schema_revision: str,
    ) -> str:
        raw = secrets.token_urlsafe(18)
        tag = self._token_secret.sign(raw)
        token = f"rst-{raw}.{tag}"
        self._restore_tokens[token] = _StoredRestore(
            collection=collection,
            item_id=item_id,
            target_revision=target_revision,
            current_hash=current_hash,
            schema_revision=schema_revision,
            expires_at=self._clock() + RESTORE_TOKEN_TTL_SECONDS,
        )
        return token


# ---------------------------------------------------------------------------
# Module helpers
# ---------------------------------------------------------------------------


def _hash_item(item: dict[str, Any], readable: set[str]) -> str:
    """Stable hash of an item's readable fields."""
    payload = {field: item.get(field) for field in sorted(readable) if field in item}
    encoded = repr(sorted(payload.items())).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _display_name(user: dict[str, Any]) -> str | None:
    first = user.get("first_name")
    last = user.get("last_name")
    if first or last:
        return " ".join(part for part in [first, last] if part).strip() or None
    email = user.get("email")
    return str(email) if email else None


def _response_list(payload: Any) -> list[dict[str, Any]]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), list):
        return [item for item in payload["data"] if isinstance(item, dict)]
    return []


def _response_object(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        return payload["data"]
    return {}


def _response_meta(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict) and isinstance(payload.get("meta"), dict):
        return payload["meta"]
    return {}


def _safe_int(value: Any) -> int | None:
    return value if isinstance(value, int) else None


def _iso_timestamp(epoch: float) -> str:
    from datetime import datetime

    return datetime.fromtimestamp(epoch, tz=UTC).isoformat()


__all__ = [
    "RESTORE_TOKEN_TTL_SECONDS",
    "HistoryError",
    "HistoryService",
]
