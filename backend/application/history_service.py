"""Permission-safe table, row and cell history with two-phase restore."""

from __future__ import annotations

import asyncio
import base64
import hashlib
import hmac
import json
import secrets
import time
from collections import defaultdict, deque
from collections.abc import AsyncIterator, Awaitable, Callable, Mapping, Sequence
from datetime import UTC, datetime
from typing import Any, cast
from urllib.parse import quote

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusSchemaError, DirectusTransportError
from backend.adapters.directus.profile import CollectionProfile, RelationProfile
from backend.contracts.history import (
    ApplyRestoreParams,
    HistoryActor,
    HistoryChangeSet,
    HistoryPage,
    HistoryRecordChange,
    PreviewRestoreParams,
    ReadChangeSetsParams,
    RelationFieldChange,
    RestoreDiagnostic,
    RestorePreview,
    RestoreResult,
    ScalarFieldChange,
)

RESTORE_TOKEN_TTL_SECONDS: float = 5 * 60.0
_HISTORY_PAGE_SIZE = 500
_HISTORY_WINDOW_LIMIT = 2000
_RELATION_LOOKUP_CONCURRENCY = 8
_RELATION_CACHE_LIMIT = 2048
_RESTORE_TOKEN_LIMIT = 1_024
_SYSTEM_FIELD_NAMES = {"date_created", "user_created", "date_updated", "user_updated"}
_SYSTEM_SPECIALS = {"date-created", "user-created", "date-updated", "user-updated"}


class HistoryError(Exception):
    """A history-flow error carrying an RPC-friendly code."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class _RestoreTokenSecret:
    def __init__(self) -> None:
        self._secret = secrets.token_bytes(32)

    def sign(self, payload: str) -> str:
        return hmac.new(self._secret, payload.encode("ascii"), hashlib.sha256).hexdigest()


class _StoredRestore:
    __slots__ = (
        "capability_hash",
        "collection",
        "current_hash",
        "expires_at",
        "item_id",
        "latest_revision",
        "patch",
        "permission_hash",
        "schema_revision",
        "scope",
        "server_authorization_token",
        "target_revision",
        "watched_fields",
    )

    def __init__(
        self,
        *,
        collection: str,
        item_id: str,
        target_revision: str,
        scope: str,
        current_hash: str,
        watched_fields: set[str],
        patch: dict[str, Any],
        permission_hash: str,
        schema_revision: str,
        server_authorization_token: str | None,
        capability_hash: str,
        latest_revision: str | None,
        expires_at: float,
    ) -> None:
        self.collection = collection
        self.item_id = item_id
        self.target_revision = target_revision
        self.scope = scope
        self.current_hash = current_hash
        self.watched_fields = watched_fields
        self.patch = patch
        self.permission_hash = permission_hash
        self.schema_revision = schema_revision
        self.server_authorization_token = server_authorization_token
        self.capability_hash = capability_hash
        self.latest_revision = latest_revision
        self.expires_at = expires_at


class _PermissionPolicy:
    __slots__ = ("hash", "readable", "updatable", "update_access")

    def __init__(self, *, readable: set[str], updatable: set[str], update_access: str) -> None:
        self.readable = readable
        self.updatable = updatable
        self.update_access = update_access
        encoded = json.dumps(
            {
                "readable": sorted(readable),
                "update_access": update_access,
                "updatable": sorted(updatable),
            },
            separators=(",", ":"),
            sort_keys=True,
        ).encode("utf-8")
        self.hash = hashlib.sha256(encoded).hexdigest()


class HistoryService:
    """Read revision Activity groups and safely restore an allowed field patch."""

    def __init__(
        self,
        *,
        client: DirectusClient,
        auth: DirectusAuthBroker,
        profiles: dict[str, CollectionProfile],
        transport: Any,
        schema_revision: str,
        proof_secret: str | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._client = client
        self._auth = auth
        self._profiles = profiles
        self._transport = transport
        self._schema_revision = schema_revision
        self._proof_secret = proof_secret
        self._clock = clock
        self._token_secret = _RestoreTokenSecret()
        self._restore_tokens: dict[str, _StoredRestore] = {}
        self._restore_lock = asyncio.Lock()

    async def read_change_sets(self, params: ReadChangeSetsParams) -> HistoryPage:
        """Read, permission-crop, group, filter and page revisions.

        Paging happens after Activity grouping, so one request/activity can never
        be split across pages. Search is intentionally evaluated only against
        the already-cropped values.
        """
        profile = self._profile(params.collection)
        if not profile.allow_revision_history:
            raise HistoryError(
                f"collection {params.collection!r} does not allow revision history",
                code="history_not_allowed",
            )
        token = await self._auth.access_token()
        await self._require_revision_capability(profile, token)
        permissions = await self._permission_policy(profile, token)
        readable = permissions.readable
        if params.field and params.field not in readable:
            raise HistoryError("history field is not readable", code="history_field_unreadable")

        archived_ids: set[str] | None = None
        archived_labels: dict[str, str] = {}
        if params.scope == "archived":
            archived_ids, archived_labels = await self._read_archived_records(
                profile, token, readable
            )
            if not archived_ids:
                return self._page(params, profile, [], total=0)

        revision_filter: dict[str, Any] = {"collection": {"_eq": params.collection}}
        if params.item_id:
            revision_filter["item"] = {"_eq": params.item_id}
        elif params.record_id:
            revision_filter["item"] = {"_eq": params.record_id}
        selected, total, archived_defaults = await self._stream_change_sets(
            params,
            profile,
            revision_filter,
            readable=readable,
            archived_ids=archived_ids,
            archived_labels=archived_labels,
            token=token,
        )
        return self._page(
            params,
            profile,
            selected,
            total=total,
            archived_default_revision_ids=archived_defaults,
        )

    async def preview_restore(self, params: PreviewRestoreParams) -> RestorePreview:
        """Classify target fields and mint a token containing the exact patch."""
        profile = self._profile(params.collection)
        if not profile.allow_revision_revert:
            raise HistoryError(
                f"collection {params.collection!r} does not allow revision revert",
                code="restore_not_allowed",
            )
        if params.scope == "archived" and profile.archive_field is None:
            raise HistoryError("collection has no archive field", code="archive_not_supported")

        token = await self._auth.access_token()
        await self._require_revision_capability(profile, token)
        permissions = await self._permission_policy(profile, token)
        item_can_update = await self._item_update_allowed(profile, params.item_id, token)
        effective_updatable = permissions.updatable if item_can_update else set()
        permission_hash = _permission_hash(permissions, item_can_update)
        if params.field and params.field not in permissions.readable:
            raise HistoryError("restore field is not readable", code="history_field_unreadable")
        current = await self._client.read_item(
            profile,
            params.item_id,
            fields=sorted(permissions.readable),
        )
        if params.scope == "archived":
            archive_field = profile.archive_field
            if archive_field is None or current.get(archive_field) != profile.archive_value:
                raise HistoryError("record is not archived", code="restore_scope_mismatch")

        revision_payload = await self._transport.request(
            "GET",
            f"/revisions/{_segment(params.target_revision)}",
            access_token=token,
            query={"fields": ["id", "collection", "item", "data", "delta"]},
        )
        revision = _response_object(revision_payload)
        if revision.get("collection") not in {None, params.collection}:
            raise HistoryError(
                "target revision belongs to another collection", code="target_revision_invalid"
            )
        revision_item = _revision_item_id(revision)
        if revision_item and revision_item != params.item_id:
            raise HistoryError(
                "target revision belongs to another item", code="target_revision_invalid"
            )
        target = revision.get("data")
        if not isinstance(target, Mapping):
            raise HistoryError(
                "target revision has no restorable snapshot", code="target_revision_invalid"
            )

        metadata = await self._live_field_metadata(profile)
        relations = {relation.field: relation for relation in profile.relations}
        fields: Sequence[str]
        if params.scope == "cell":
            fields = [params.field] if params.field else []
        else:
            # Existing fields must still be readable. Fields removed from the
            # live schema are included by name only so the preview can explain
            # why they will be skipped without disclosing their old value.
            fields = [
                field
                for field in target
                if field not in profile.fields or field in permissions.readable
            ]

        scalar_changes: list[ScalarFieldChange] = []
        relation_changes: list[RelationFieldChange] = []
        diagnostics: list[RestoreDiagnostic] = []
        patch: dict[str, Any] = {}

        for field in fields:
            if not isinstance(field, str):
                continue
            if field not in profile.fields:
                diagnostics.append(
                    _diagnostic(
                        field,
                        "schema_retired",
                        "field_not_readable",
                        "Field is not present in the current readable schema.",
                    )
                )
                continue
            if field not in target:
                continue
            target_value = target[field]
            current_value = current.get(field)
            reason = _field_restriction(
                field,
                target_value,
                profile=profile,
                metadata=metadata.get(field, {}),
                current_value=current_value,
                updatable=effective_updatable,
            )
            relation = relations.get(field)
            relation_safe = True
            before_display: str | None = None
            after_display: str | None = None
            if relation is not None:
                if relation.kind not in {"m2o", "file"}:
                    relation_safe = False
                    reason = reason or (
                        "relation_unsafe",
                        "complex_relation",
                        "Complex relations cannot be restored atomically.",
                    )
                else:
                    _before_safe, before_display = (
                        await self._resolve_relation_target(relation, current_value, token)
                        if current_value is not None
                        else (True, None)
                    )
                    after_safe, after_display = (
                        await self._resolve_relation_target(relation, target_value, token)
                        if target_value is not None
                        else (True, None)
                    )
                    # The current relation is resolved only for a safe display label.
                    # Restore eligibility depends on the historical target: an unreadable
                    # or deleted current target must not block replacing it.
                    relation_safe = after_safe
                    if not relation_safe:
                        reason = reason or (
                            "relation_unsafe",
                            "relation_target_unavailable",
                            "Related target is unavailable or not readable.",
                        )

            if current_value != target_value:
                if relation is None:
                    scalar_changes.append(
                        ScalarFieldChange(field=field, before=current_value, after=target_value)
                    )
                else:
                    relation_changes.append(
                        RelationFieldChange(
                            field=field,
                            kind=relation.kind,
                            related_collection=relation.related_collection,
                            related_item_id=None,
                            display_value=after_display,
                            before_item_id=None,
                            after_item_id=None,
                            before_display_value=before_display,
                            after_display_value=after_display,
                            target_available=relation_safe,
                        )
                    )
            if reason is not None:
                classification, code, message = reason
                diagnostics.append(_diagnostic(field, classification, code, message))
                continue
            patch[field] = target_value

        if params.scope == "archived" and profile.archive_field is not None:
            archive_field = profile.archive_field
            archive_reason = _field_restriction(
                archive_field,
                profile.restore_value,
                profile=profile,
                metadata=metadata.get(archive_field, {}),
                current_value=current.get(archive_field),
                updatable=effective_updatable,
            )
            if archive_reason is not None:
                classification, code, message = archive_reason
                if not any(diagnostic.field == archive_field for diagnostic in diagnostics):
                    diagnostics.append(_diagnostic(archive_field, classification, code, message))
                patch.clear()
            else:
                patch[archive_field] = profile.restore_value
                if current.get(archive_field) != profile.restore_value and not any(
                    change.field == archive_field for change in scalar_changes
                ):
                    scalar_changes.append(
                        ScalarFieldChange(
                            field=archive_field,
                            before=current.get(archive_field),
                            after=profile.restore_value,
                        )
                    )

        watched_fields = set(patch)
        current_hash = _hash_item(current, watched_fields)
        has_change = any(current.get(field) != value for field, value in patch.items())
        server_authorization_token = None
        if patch and has_change:
            server_authorization_token = await self._authorize_restore(
                collection=params.collection,
                item_id=params.item_id,
                target_revision=params.target_revision,
                scope=params.scope,
                field=params.field,
                values=patch,
                expected_values={field: current.get(field) for field in watched_fields},
                access_token=token,
            )
        latest_revision = (
            await self._latest_revision_id(profile.collection, params.item_id, token)
            if params.scope == "archived"
            else None
        )
        restore_token = self._mint_restore(
            collection=params.collection,
            item_id=params.item_id,
            target_revision=params.target_revision,
            scope=params.scope,
            current_hash=current_hash,
            watched_fields=watched_fields,
            patch=patch if has_change else {},
            permission_hash=permission_hash,
            schema_revision=self._schema_revision,
            server_authorization_token=server_authorization_token,
            capability_hash=profile.capability_hash,
            latest_revision=latest_revision,
        )
        return RestorePreview(
            collection=params.collection,
            item_id=params.item_id,
            target_revision=params.target_revision,
            scope=params.scope,
            field=params.field,
            current_hash=current_hash,
            schema_revision=self._schema_revision,
            scalar_changes=scalar_changes,
            relation_changes=relation_changes,
            diagnostics=diagnostics,
            can_apply=bool(patch) and has_change,
            restorable_fields=sorted(patch),
            token=restore_token,
            expires_at=_iso_timestamp(self._clock() + RESTORE_TOKEN_TTL_SECONDS),
        )

    async def apply_restore(self, params: ApplyRestoreParams) -> RestoreResult:
        """Apply a preview token once, serializing competing restore attempts."""

        async with self._restore_lock:
            return await self._apply_restore_locked(params)

    async def _apply_restore_locked(self, params: ApplyRestoreParams) -> RestoreResult:
        stored = self._restore_tokens.pop(params.token, None)
        if stored is None:
            raise HistoryError("restore token not found", code="restore_token_unknown")
        if self._clock() >= stored.expires_at:
            raise HistoryError("restore token expired", code="restore_token_expired")
        if stored.collection != params.collection or stored.item_id != params.item_id:
            raise HistoryError(
                "restore token scope does not match request", code="restore_scope_mismatch"
            )
        if not stored.patch:
            raise HistoryError("preview contains no restorable fields", code="restore_no_fields")
        if not stored.server_authorization_token:
            raise HistoryError("restore authorization is unavailable", code="restore_token_unknown")

        profile = self._profile(params.collection)
        if not profile.allow_revision_revert:
            raise HistoryError("revision restore is no longer allowed", code="restore_not_allowed")
        token = await self._auth.access_token()
        await self._require_revision_capability(profile, token)
        permissions = await self._permission_policy(profile, token)
        item_can_update = await self._item_update_allowed(profile, params.item_id, token)
        if (
            self._schema_revision != stored.schema_revision
            or profile.capability_hash != stored.capability_hash
            or _permission_hash(permissions, item_can_update) != stored.permission_hash
        ):
            raise HistoryError(
                "schema changed since the restore was previewed", code="schema_drift"
            )

        current = await self._client.read_item(
            profile,
            params.item_id,
            fields=sorted(stored.watched_fields),
        )
        if _hash_item(current, stored.watched_fields) != stored.current_hash:
            raise HistoryError(
                "item changed since the restore was previewed", code="restore_conflict"
            )
        pre_latest = await self._latest_revision_id(params.collection, params.item_id, token)
        if stored.scope == "archived":
            if (
                profile.archive_field is None
                or current.get(profile.archive_field) != profile.archive_value
            ):
                raise HistoryError("archived record state changed", code="restore_conflict")
            if stored.latest_revision != pre_latest:
                raise HistoryError("archived record version changed", code="restore_conflict")

        expected_values = {field: current.get(field) for field in stored.watched_fields}
        request_id = f"history-restore-{hashlib.sha256(params.token.encode()).hexdigest()[:24]}"
        conditional_update = getattr(self._client, "update_item_if_unchanged", None)
        try:
            if callable(conditional_update):
                update_if_unchanged = cast(
                    Callable[..., Awaitable[dict[str, Any]]], conditional_update
                )
                await update_if_unchanged(
                    profile,
                    params.item_id,
                    dict(stored.patch),
                    expected_values=expected_values,
                    read_fields=permissions.readable,
                    request_id=request_id,
                    operation="restore",
                    authorization_token=stored.server_authorization_token,
                )
            else:  # Compatibility for narrow test doubles and older adapters.
                await self._client.update_item(
                    profile,
                    params.item_id,
                    dict(stored.patch),
                    request_id=request_id,
                )
        except DirectusTransportError as exc:
            if exc.status == 409 or exc.code == "EDIT_CONFLICT":
                raise HistoryError(
                    "item changed while restore was being applied", code="restore_conflict"
                ) from exc
            raise
        post_latest = await self._latest_revision_id(params.collection, params.item_id, token)
        if post_latest is None or post_latest == pre_latest:
            raise HistoryError("restore did not create a new revision", code="revision_not_created")
        refreshed = await self._client.read_item(
            profile,
            params.item_id,
            fields=sorted(permissions.readable),
        )
        return RestoreResult(
            collection=params.collection,
            item_id=params.item_id,
            restored_to_revision=stored.target_revision,
            new_revision_id=post_latest,
            item=refreshed,
        )

    async def _authorize_restore(
        self,
        *,
        collection: str,
        item_id: str,
        target_revision: str,
        scope: str,
        field: str | None,
        values: Mapping[str, Any],
        expected_values: Mapping[str, Any],
        access_token: str,
    ) -> str:
        """Bind the BFF preview to a one-time, user-scoped Directus authorization."""

        if not self._proof_secret:
            raise HistoryError(
                "local restore proof secret is unavailable",
                code="restore_not_allowed",
            )
        restore_request = {
            "contract": "vibetable-history-restore.v1",
            "collection": collection,
            "itemId": item_id,
            "targetRevision": target_revision,
            "scope": scope,
            "field": field,
            "schemaRevision": self._schema_revision,
            "values": dict(values),
            "expectedValues": dict(expected_values),
        }
        encoded_payload = base64.urlsafe_b64encode(
            json.dumps(
                restore_request,
                ensure_ascii=False,
                separators=(",", ":"),
                sort_keys=True,
            ).encode("utf-8")
        ).decode("ascii")
        issued_at = int(self._clock() * 1000)
        nonce = secrets.token_urlsafe(24)
        subject = hashlib.sha256(access_token.encode()).hexdigest()
        proof_material = f"{issued_at}\n{nonce}\n{subject}\n{encoded_payload}".encode()
        signature = hmac.new(
            self._proof_secret.encode("utf-8"),
            proof_material,
            hashlib.sha256,
        ).hexdigest()
        try:
            payload = await self._transport.request(
                "POST",
                "/vibetable-bulk-mutation/restore/authorize",
                access_token=access_token,
                json_body={
                    "contract": "vibetable-history-preview-proof.v1",
                    "issuedAt": issued_at,
                    "nonce": nonce,
                    "payload": encoded_payload,
                    "subject": subject,
                    "signature": signature,
                },
            )
        except DirectusTransportError as exc:
            if exc.status == 409 or exc.code == "EDIT_CONFLICT":
                raise HistoryError(
                    "item changed while restore was being previewed", code="restore_conflict"
                ) from exc
            raise
        authorization = _response_object(payload)
        authorization_token = authorization.get("authorizationToken")
        if not isinstance(authorization_token, str) or not authorization_token:
            raise HistoryError(
                "Directus did not issue a restore authorization",
                code="restore_not_allowed",
            )
        return authorization_token

    async def _permission_policy(self, profile: CollectionProfile, token: str) -> _PermissionPolicy:
        """Resolve the current user's live read/update field policy fail-closed."""

        payload = await self._transport.request(
            "GET",
            "/permissions/me",
            access_token=token,
        )
        data = payload.get("data") if isinstance(payload, Mapping) else None
        collection = data.get(profile.collection) if isinstance(data, Mapping) else None
        if not isinstance(collection, Mapping):
            raise HistoryError("collection is not readable", code="history_not_allowed")

        def allowed(action: str, candidates: set[str]) -> set[str]:
            details = collection.get(action)
            if not isinstance(details, Mapping) or details.get("access") not in {
                "full",
                "partial",
                True,
            }:
                return set()
            fields = details.get("fields")
            if not isinstance(fields, list):
                return set()
            if "*" in fields:
                return set(candidates)
            return candidates & {field for field in fields if isinstance(field, str)}

        readable = allowed("read", set(profile.fields))
        if not readable:
            raise HistoryError("collection is not readable", code="history_not_allowed")
        update_details = collection.get("update")
        update_access = (
            str(update_details.get("access")) if isinstance(update_details, Mapping) else "none"
        )
        updatable = allowed("update", set(profile.update_fields))
        return _PermissionPolicy(
            readable=readable,
            updatable=updatable,
            update_access=update_access,
        )

    async def _require_revision_capability(self, profile: CollectionProfile, token: str) -> None:
        """Verify Directus is still configured to persist item revisions."""

        payload = await self._transport.request(
            "GET",
            f"/collections/{_segment(profile.collection)}",
            access_token=token,
            query={"fields": ["collection", "meta.accountability"]},
        )
        data = payload.get("data") if isinstance(payload, Mapping) else None
        meta = data.get("meta") if isinstance(data, Mapping) else None
        if not isinstance(meta, Mapping) or meta.get("accountability") != "all":
            raise HistoryError(
                "collection revision tracking is unavailable",
                code="history_not_allowed",
            )

    async def _item_update_allowed(
        self, profile: CollectionProfile, item_id: str, token: str
    ) -> bool:
        payload = await self._transport.request(
            "GET",
            f"/permissions/me/{_segment(profile.collection)}/{_segment(item_id)}",
            access_token=token,
        )
        data = payload.get("data") if isinstance(payload, Mapping) else None
        update = data.get("update") if isinstance(data, Mapping) else None
        return isinstance(update, Mapping) and update.get("access") is True

    async def _read_archived_records(
        self, profile: CollectionProfile, token: str, readable: set[str]
    ) -> tuple[set[str], dict[str, str]]:
        if profile.archive_field is None:
            raise HistoryError("collection has no archive field", code="archive_not_supported")
        label_fields = _label_fields_from_set(readable, profile.primary_key)
        ids: set[str] = set()
        labels: dict[str, str] = {}
        offset = 0
        while True:
            payload = await self._transport.request(
                "GET",
                f"/items/{_segment(profile.collection)}",
                access_token=token,
                query={
                    "filter": {profile.archive_field: {"_eq": profile.archive_value}},
                    "fields": [profile.primary_key, *label_fields],
                    "limit": _HISTORY_PAGE_SIZE,
                    "offset": offset,
                    "meta": "filter_count",
                },
            )
            page = _response_list(payload)
            if not page:
                break
            for item in page:
                item_id = item.get(profile.primary_key)
                if item_id is None:
                    continue
                key = str(item_id)
                ids.add(key)
                labels[key] = _record_label(item, profile.primary_key, label_fields) or key
            offset += len(page)
            filter_count = _response_filter_count(payload)
            if (filter_count is not None and offset >= filter_count) or (
                filter_count is None and len(page) < _HISTORY_PAGE_SIZE
            ):
                break
        return ids, labels

    async def _iter_revision_pages(
        self, revision_filter: dict[str, Any], token: str
    ) -> AsyncIterator[list[dict[str, Any]]]:
        """Yield the complete history as bounded, chronologically sorted pages."""

        offset = 0
        while True:
            payload = await self._transport.request(
                "GET",
                "/revisions",
                access_token=token,
                query={
                    "filter": revision_filter,
                    "fields": [
                        "id",
                        "collection",
                        "item",
                        "data",
                        "delta",
                        "parent.id",
                        "activity.id",
                        "activity.action",
                        "activity.user.id",
                        "activity.user.first_name",
                        "activity.user.last_name",
                        "activity.user.email",
                        "activity.timestamp",
                    ],
                    "limit": _HISTORY_PAGE_SIZE,
                    "offset": offset,
                    "meta": "filter_count",
                    "sort": "activity.timestamp,activity.id,id",
                },
            )
            page = _response_list(payload)
            if not page:
                return
            page = await self._attach_history_markers(page, token)
            yield page
            offset += len(page)
            filter_count = _response_filter_count(payload)
            if (filter_count is not None and offset >= filter_count) or (
                filter_count is None and len(page) < _HISTORY_PAGE_SIZE
            ):
                return

    async def _attach_history_markers(
        self, page: list[dict[str, Any]], token: str
    ) -> list[dict[str, Any]]:
        activity_ids = sorted(
            {
                str(activity.get("id"))
                for raw in page
                if (activity := _as_mapping(raw.get("activity"))).get("id") is not None
            }
        )
        if not activity_ids:
            return page
        try:
            payload = await self._transport.request(
                "POST",
                "/vibetable-bulk-mutation/history-markers",
                access_token=token,
                json_body={"activityIds": activity_ids},
            )
        except DirectusTransportError as exc:
            if exc.status == 404:
                raise HistoryError(
                    "history marker capability is unavailable",
                    code="history_not_allowed",
                ) from exc
            raise
        markers = _response_object(payload)
        annotated: list[dict[str, Any]] = []
        for raw in page:
            activity = dict(_as_mapping(raw.get("activity")))
            marker = markers.get(str(activity.get("id")))
            if marker in {"restore"}:
                activity["vibetable_action"] = marker
            annotated.append({**raw, "activity": activity})
        return annotated

    async def _stream_change_sets(
        self,
        params: ReadChangeSetsParams,
        profile: CollectionProfile,
        revision_filter: dict[str, Any],
        *,
        readable: set[str],
        archived_ids: set[str] | None,
        archived_labels: dict[str, str],
        token: str,
    ) -> tuple[list[HistoryChangeSet], int, dict[str, str]]:
        """Stream revisions with bounded result memory and exact totals."""

        relations = {relation.field: relation for relation in profile.relations}
        state_fields = readable | (
            {profile.archive_field} if profile.archive_field is not None else set()
        )

        async def scan(
            *,
            retain_newest: int,
            chronological_range: tuple[int, int] | None = None,
            track_archived_defaults: bool,
            scan_filter: dict[str, Any] | None = None,
        ) -> tuple[
            int,
            list[HistoryChangeSet],
            list[HistoryChangeSet],
            dict[str, str],
            str | None,
        ]:
            snapshots: dict[str, dict[str, Any]] = {}
            prior_revision_ids: dict[str, str] = {}
            archived_defaults: dict[str, str] = {}
            newest: deque[HistoryChangeSet] = deque(maxlen=retain_newest)
            chronological: list[HistoryChangeSet] = []
            pending_group_id: str | None = None
            pending_raw: list[dict[str, Any]] = []
            pending_changes: dict[str, HistoryRecordChange] = {}
            # Relation labels are only a presentation aid. Bound the cross-activity cache
            # so memory usage does not grow with the age of the revision log.
            relation_cache: dict[tuple[str, str], tuple[bool, str | None]] = {}
            total = 0
            max_revision_id: int | None = None

            async def finish_pending() -> None:
                nonlocal pending_group_id, pending_raw, pending_changes, total
                if not pending_changes:
                    pending_group_id = None
                    pending_raw = []
                    return
                safe_changes = await self._sanitize_relation_history(
                    pending_changes,
                    relations=relations,
                    token=token,
                    cache=relation_cache,
                )
                groups = _group_record_changes(pending_raw, safe_changes)
                if groups:
                    group = _crop_group(groups[0], params.field)
                    if group.record_changes and _matches_filters(group, params):
                        index = total
                        total += 1
                        if retain_newest:
                            newest.append(group)
                        if (
                            chronological_range is not None
                            and chronological_range[0] <= index < chronological_range[1]
                        ):
                            chronological.append(group)
                pending_group_id = None
                pending_raw = []
                pending_changes = {}

            async for page in self._iter_revision_pages(scan_filter or revision_filter, token):
                # Directus owns the typed ordering (timestamp, activity id, revision id).
                # Preserve it exactly so numeric ids and Activity groups remain contiguous
                # across page boundaries.
                for raw in page:
                    raw_revision_id = raw.get("id")
                    if raw_revision_id is not None:
                        try:
                            numeric_revision_id = int(str(raw_revision_id))
                        except ValueError:
                            numeric_revision_id = None
                        if numeric_revision_id is not None:
                            max_revision_id = max(max_revision_id or 0, numeric_revision_id)
                    item_id = _revision_item_id(raw) or params.item_id or params.record_id
                    if item_id is None:
                        continue
                    before = snapshots.get(item_id, {})
                    before_archive = (
                        before.get(profile.archive_field)
                        if profile.archive_field is not None
                        else None
                    )
                    prior_revision_id = prior_revision_ids.get(item_id)
                    record = _advance_record_change(
                        raw,
                        item_id=item_id,
                        prior=before,
                        state_fields=state_fields,
                        readable=readable,
                        relations=relations,
                        primary_key=profile.primary_key,
                        archive_field=profile.archive_field,
                        archive_value=profile.archive_value,
                        forced_label=archived_labels.get(item_id),
                    )
                    snapshots[item_id] = record[1]
                    raw_id = raw.get("id")
                    if raw_id is not None:
                        prior_revision_ids[item_id] = str(raw_id)
                    if (
                        track_archived_defaults
                        and archived_ids is not None
                        and profile.archive_field is not None
                        and record[1].get(profile.archive_field) == profile.archive_value
                        and before_archive != profile.archive_value
                        and prior_revision_id is not None
                    ):
                        archived_defaults[item_id] = prior_revision_id
                    if archived_ids is not None and item_id not in archived_ids:
                        continue
                    change = record[0]
                    if change is None:
                        continue
                    activity = _as_mapping(raw.get("activity"))
                    group_id = str(activity.get("id") or f"revision:{change.revision_id}")
                    if pending_group_id is not None and group_id != pending_group_id:
                        await finish_pending()
                    pending_group_id = group_id
                    pending_raw.append(raw)
                    pending_changes[change.revision_id] = change
            await finish_pending()
            return (
                total,
                list(newest),
                chronological,
                archived_defaults,
                str(max_revision_id) if max_revision_id is not None else None,
            )

        requested_window = params.offset + params.limit
        if requested_window <= _HISTORY_WINDOW_LIMIT:
            total, newest, _, archived_defaults, _ = await scan(
                retain_newest=requested_window,
                track_archived_defaults=True,
            )
            selected = list(reversed(newest))[params.offset : requested_window]
        else:
            total, _, _, archived_defaults, upper_revision_id = await scan(
                retain_newest=0,
                track_archived_defaults=True,
            )
            end = max(0, total - params.offset)
            start = max(0, end - params.limit)
            chronological: list[HistoryChangeSet] = []
            if start < end and upper_revision_id is not None:
                bounded_filter = {"_and": [revision_filter, {"id": {"_lte": upper_revision_id}}]}
                _, _, chronological, _, _ = await scan(
                    retain_newest=0,
                    chronological_range=(start, end),
                    track_archived_defaults=False,
                    scan_filter=bounded_filter,
                )
            selected = list(reversed(chronological))

        selected_item_ids = {
            record.item_id for group in selected for record in group.record_changes
        }
        selected_defaults = {
            item_id: revision_id
            for item_id, revision_id in archived_defaults.items()
            if item_id in selected_item_ids
        }
        return selected, total, selected_defaults

    async def _live_field_metadata(
        self, profile: CollectionProfile
    ) -> dict[str, Mapping[str, Any]]:
        read_fields = getattr(self._client, "fields", None)
        if not callable(read_fields):
            return {}
        load_fields = cast(
            Callable[[CollectionProfile], Awaitable[list[dict[str, Any]]]],
            read_fields,
        )
        fields = await load_fields(profile)
        return {
            str(field["field"]): field
            for field in fields
            if isinstance(field, Mapping) and isinstance(field.get("field"), str)
        }

    async def _resolve_relation_target(
        self, relation: RelationProfile, target_value: Any, token: str
    ) -> tuple[bool, str | None]:
        fields = relation.display_fields or ["id"]
        try:
            payload = await self._transport.request(
                "GET",
                f"/items/{_segment(relation.related_collection)}/{_segment(str(target_value))}",
                access_token=token,
                query={"fields": fields},
            )
        except DirectusTransportError as exc:
            if exc.status not in {403, 404}:
                raise
            return False, None
        item = _response_object(payload)
        if not item:
            return False, None
        parts = [str(item[field]) for field in fields if item.get(field) not in {None, ""}]
        return True, " · ".join(parts)[:512] or None

    async def _sanitize_relation_history(
        self,
        records: dict[str, HistoryRecordChange],
        *,
        relations: dict[str, RelationProfile],
        token: str,
        cache: dict[tuple[str, str], tuple[bool, str | None]] | None = None,
    ) -> dict[str, HistoryRecordChange]:
        """Resolve readable labels and remove ids for unavailable relation targets."""
        resolved_cache = cache if cache is not None else {}
        lookups: dict[tuple[str, str], RelationProfile] = {}
        for record in records.values():
            for change in record.relation_changes:
                relation = relations[change.field]
                if relation.kind not in {"m2o", "file"}:
                    continue
                for item_id in (change.before_item_id, change.after_item_id):
                    if item_id is not None and (relation.field, item_id) not in resolved_cache:
                        lookups[(relation.field, item_id)] = relation

        semaphore = asyncio.Semaphore(_RELATION_LOOKUP_CONCURRENCY)

        async def load(
            key: tuple[str, str], relation: RelationProfile
        ) -> tuple[tuple[str, str], tuple[bool, str | None]]:
            async with semaphore:
                result = await self._resolve_relation_target(relation, key[1], token)
            return key, result

        resolved = await asyncio.gather(*(load(key, relation) for key, relation in lookups.items()))
        resolved_for_group = dict(resolved)
        resolved_cache.update(resolved_for_group)

        def resolve(relation: RelationProfile, item_id: str | None) -> tuple[bool, str | None]:
            if item_id is None:
                return True, None
            key = (relation.field, item_id)
            return resolved_for_group[key] if key in resolved_for_group else resolved_cache[key]

        safe_records: dict[str, HistoryRecordChange] = {}
        for revision_id, record in records.items():
            safe_relations: list[RelationFieldChange] = []
            for change in record.relation_changes:
                relation = relations[change.field]
                if relation.kind not in {"m2o", "file"}:
                    safe_relations.append(
                        change.model_copy(
                            update={
                                "related_item_id": None,
                                "before_item_id": None,
                                "after_item_id": None,
                                "display_value": None,
                                "target_available": False,
                            }
                        )
                    )
                    continue
                before_ok, before_display = resolve(relation, change.before_item_id)
                after_ok, after_display = resolve(relation, change.after_item_id)
                safe_relations.append(
                    change.model_copy(
                        update={
                            "related_item_id": None,
                            "before_item_id": None,
                            "after_item_id": None,
                            "before_display_value": before_display,
                            "after_display_value": after_display,
                            "display_value": after_display,
                            "target_available": before_ok and after_ok,
                        }
                    )
                )
            safe_records[revision_id] = record.model_copy(
                update={"relation_changes": safe_relations}
            )
        while len(resolved_cache) > _RELATION_CACHE_LIMIT:
            resolved_cache.pop(next(iter(resolved_cache)))
        return safe_records

    async def _latest_revision_id(self, collection: str, item_id: str, token: str) -> str | None:
        payload = await self._transport.request(
            "GET",
            "/revisions",
            access_token=token,
            query={
                "filter": {"collection": {"_eq": collection}, "item": {"_eq": item_id}},
                "fields": ["id"],
                "limit": 1,
                "sort": "-id",
            },
        )
        revisions = _response_list(payload)
        return str(revisions[0]["id"]) if revisions and revisions[0].get("id") is not None else None

    def _page(
        self,
        params: ReadChangeSetsParams,
        profile: CollectionProfile,
        change_sets: list[HistoryChangeSet],
        *,
        total: int,
        archived_default_revision_ids: dict[str, str] | None = None,
    ) -> HistoryPage:
        return HistoryPage(
            collection=params.collection,
            scope=params.scope,
            item_id=params.item_id,
            field=params.field,
            change_sets=change_sets,
            total=total,
            capability_hash=profile.capability_hash,
            schema_revision=self._schema_revision,
            has_more=params.offset + len(change_sets) < total,
            archived_default_revision_ids=archived_default_revision_ids or {},
        )

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise DirectusSchemaError(f"collection {collection!r} is not in capability manifest")
        return profile

    def _mint_restore(self, **values: Any) -> str:
        now = self._clock()
        for token, stored in list(self._restore_tokens.items()):
            if now >= stored.expires_at:
                self._restore_tokens.pop(token, None)
        while len(self._restore_tokens) >= _RESTORE_TOKEN_LIMIT:
            self._restore_tokens.pop(next(iter(self._restore_tokens)))
        raw = secrets.token_urlsafe(18)
        token = f"rst-{raw}.{self._token_secret.sign(raw)}"
        self._restore_tokens[token] = _StoredRestore(
            **values,
            expires_at=now + RESTORE_TOKEN_TTL_SECONDS,
        )
        return token


def _advance_record_change(
    raw: dict[str, Any],
    *,
    item_id: str,
    prior: Mapping[str, Any],
    state_fields: set[str],
    readable: set[str],
    relations: dict[str, RelationProfile],
    primary_key: str,
    archive_field: str | None,
    archive_value: Any,
    forced_label: str | None,
) -> tuple[HistoryRecordChange | None, dict[str, Any]]:
    """Advance one item's snapshot and emit its permission-cropped adjacent diff."""

    raw_data = raw.get("data")
    delta = raw.get("delta")
    snapshot = (
        {field: raw_data[field] for field in state_fields if field in raw_data}
        if isinstance(raw_data, Mapping)
        else {
            **prior,
            **(
                {field: delta[field] for field in state_fields if field in delta}
                if isinstance(delta, Mapping)
                else {}
            ),
        }
    )
    changed_fields = (
        set(delta) if isinstance(delta, Mapping) and delta else set(snapshot) | set(prior)
    )
    scalar: list[ScalarFieldChange] = []
    relation_changes: list[RelationFieldChange] = []
    for field in sorted(changed_fields & readable):
        before = prior.get(field)
        after = snapshot.get(field)
        if before == after:
            continue
        relation = relations.get(field)
        if relation is None:
            scalar.append(ScalarFieldChange(field=field, before=before, after=after))
        else:
            relation_changes.append(
                RelationFieldChange(
                    field=field,
                    kind=relation.kind,
                    related_collection=relation.related_collection,
                    related_item_id=str(after) if after is not None else None,
                    before_item_id=str(before) if before is not None else None,
                    after_item_id=str(after) if after is not None else None,
                )
            )
    if not scalar and not relation_changes:
        return None, snapshot
    revision_id = str(raw.get("id", ""))
    activity = _as_mapping(raw.get("activity"))
    action = _activity_action(
        activity,
        before_archive=prior.get(archive_field) if archive_field is not None else None,
        after_archive=snapshot.get(archive_field) if archive_field is not None else None,
        archive_value=archive_value,
    )
    label = forced_label or _record_label(
        snapshot,
        primary_key,
        _label_fields_from_set(readable, primary_key),
    )
    return (
        HistoryRecordChange(
            revision_id=revision_id,
            item_id=item_id,
            record_label=label,
            action=action,
            scalar_changes=scalar,
            relation_changes=relation_changes,
        ),
        snapshot,
    )


def _group_record_changes(
    revisions: list[dict[str, Any]], changes: dict[str, HistoryRecordChange]
) -> list[HistoryChangeSet]:
    grouped: dict[str, list[tuple[dict[str, Any], HistoryRecordChange]]] = defaultdict(list)
    for raw in revisions:
        revision_id = str(raw.get("id", ""))
        record = changes.get(revision_id)
        if record is None:
            continue
        activity = _as_mapping(raw.get("activity"))
        group_id = str(activity.get("id") or f"revision:{revision_id}")
        grouped[group_id].append((raw, record))

    groups: list[HistoryChangeSet] = []
    for group_id, entries in grouped.items():
        raw = entries[0][0]
        activity = _as_mapping(raw.get("activity"))
        user = _as_mapping(activity.get("user"))
        records = [entry[1] for entry in entries]
        item_ids = {record.item_id for record in records}
        actions = {record.action for record in records}
        scalar = [change for record in records for change in record.scalar_changes]
        relations = [change for record in records for change in record.relation_changes]
        groups.append(
            HistoryChangeSet(
                root_revision_id=records[0].revision_id,
                activity_id=None if group_id.startswith("revision:") else group_id,
                action=next(iter(actions)) if len(actions) == 1 else "batch",
                timestamp=str(activity.get("timestamp") or "1970-01-01T00:00:00Z"),
                actor=HistoryActor(
                    user_id=str(user.get("id")) if user.get("id") is not None else None,
                    display_name=_display_name(dict(user)),
                ),
                item_id=next(iter(item_ids)) if len(item_ids) == 1 else None,
                record_label=records[0].record_label if len(records) == 1 else None,
                revision_ids=[record.revision_id for record in records],
                affected_records=len(item_ids),
                record_changes=records,
                scalar_changes=scalar,
                relation_changes=relations,
            )
        )
    return sorted(groups, key=lambda group: (group.timestamp, group.root_revision_id), reverse=True)


def _crop_group(group: HistoryChangeSet, field: str | None) -> HistoryChangeSet:
    if not field:
        return group
    records: list[HistoryRecordChange] = []
    for record in group.record_changes:
        scalar = [change for change in record.scalar_changes if change.field == field]
        relations = [change for change in record.relation_changes if change.field == field]
        if scalar or relations:
            records.append(
                record.model_copy(update={"scalar_changes": scalar, "relation_changes": relations})
            )
    return group.model_copy(
        update={
            "record_changes": records,
            "affected_records": len({record.item_id for record in records}) or 1,
            "scalar_changes": [change for record in records for change in record.scalar_changes],
            "relation_changes": [
                change for record in records for change in record.relation_changes
            ],
        }
    )


def _matches_filters(group: HistoryChangeSet, params: ReadChangeSetsParams) -> bool:
    if params.date_from or params.date_to:
        timestamp = _parse_timestamp(group.timestamp)
        if timestamp is None:
            return False
        if params.date_from and timestamp < _as_utc(params.date_from):
            return False
        if params.date_to and timestamp > _as_utc(params.date_to):
            return False
    if params.actor_id and group.actor.user_id != params.actor_id:
        return False
    if params.actions and not any(
        record.action in params.actions for record in group.record_changes
    ):
        return False
    wanted_record = params.record_id or (
        params.item_id if params.scope in {"row", "cell"} else None
    )
    if wanted_record and all(record.item_id != wanted_record for record in group.record_changes):
        return False
    if not params.search:
        return True
    needle = params.search.casefold()
    values: list[Any] = [
        group.root_revision_id,
        group.activity_id,
        group.actor.user_id,
        group.actor.display_name,
    ]
    for record in group.record_changes:
        values.extend([record.revision_id, record.item_id, record.record_label])
        for scalar_change in record.scalar_changes:
            values.extend([scalar_change.field, scalar_change.before, scalar_change.after])
        for relation_change in record.relation_changes:
            values.extend(
                [
                    relation_change.field,
                    relation_change.before_item_id,
                    relation_change.after_item_id,
                    relation_change.before_display_value,
                    relation_change.after_display_value,
                    relation_change.display_value,
                ]
            )
    return any(needle in _search_text(value) for value in values if value is not None)


def _parse_timestamp(value: str) -> datetime | None:
    try:
        return _as_utc(datetime.fromisoformat(value.replace("Z", "+00:00")))
    except ValueError:
        return None


def _as_utc(value: datetime) -> datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=UTC)
    return value.astimezone(UTC)


def _permission_hash(policy: _PermissionPolicy, item_can_update: bool) -> str:
    encoded = f"{policy.hash}:{int(item_can_update)}".encode("ascii")
    return hashlib.sha256(encoded).hexdigest()


def _field_restriction(
    field: str,
    value: Any,
    *,
    profile: CollectionProfile,
    metadata: Mapping[str, Any],
    current_value: Any,
    updatable: set[str],
) -> tuple[str, str, str] | None:
    if field == profile.primary_key:
        return "readonly_system", "primary_key", "Primary keys cannot be restored."
    schema = _as_mapping(metadata.get("schema"))
    meta = _as_mapping(metadata.get("meta"))
    if schema.get("is_generated") is True:
        return "derived", "field_generated", "Generated fields are maintained by the database."
    special = meta.get("special")
    specials = (
        {special}
        if isinstance(special, str)
        else set(special or [])
        if isinstance(special, list)
        else set()
    )
    if field in _SYSTEM_FIELD_NAMES or specials & _SYSTEM_SPECIALS:
        return (
            "readonly_system",
            "system_field",
            "System audit fields are maintained automatically.",
        )
    if meta.get("readonly") is True:
        return "readonly_system", "field_readonly", "Field is marked read-only in Directus."
    if field not in profile.update_fields or field not in updatable:
        return (
            "permission_denied",
            "field_not_updatable",
            "Current update capabilities do not allow this field.",
        )
    directus_type = metadata.get("type") or schema.get("data_type")
    if not _compatible_value(value, directus_type, current_value):
        return (
            "incompatible",
            "type_incompatible",
            "Historical value is incompatible with the current field type.",
        )
    return None


def _compatible_value(value: Any, directus_type: Any, current_value: Any) -> bool:
    if value is None:
        return True
    if isinstance(directus_type, str):
        if directus_type in {"integer", "bigInteger"}:
            return isinstance(value, int) and not isinstance(value, bool)
        if directus_type in {"float", "decimal"}:
            return isinstance(value, (int, float)) and not isinstance(value, bool)
        if directus_type == "boolean":
            return isinstance(value, bool)
        if directus_type in {"json"}:
            return True
        if directus_type in {
            "string",
            "text",
            "uuid",
            "hash",
            "csv",
            "date",
            "dateTime",
            "timestamp",
            "time",
        }:
            return isinstance(value, str)
    if current_value is None:
        return True
    if isinstance(current_value, bool):
        return isinstance(value, bool)
    if isinstance(current_value, (int, float)) and not isinstance(current_value, bool):
        return isinstance(value, (int, float)) and not isinstance(value, bool)
    return isinstance(value, type(current_value))


def _diagnostic(field: str, classification: str, code: str, message: str) -> RestoreDiagnostic:
    return RestoreDiagnostic(
        field=field,
        classification=classification,  # type: ignore[arg-type]
        severity="warning",
        code=code,
        message=message,
    )


def _hash_item(item: Mapping[str, Any], fields: set[str]) -> str:
    payload = {field: item.get(field) for field in sorted(fields) if field in item}
    encoded = json.dumps(
        payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"), default=str
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _revision_item_id(raw: Mapping[str, Any]) -> str | None:
    item = raw.get("item")
    if isinstance(item, Mapping):
        item = item.get("id")
    return str(item) if item is not None and str(item) else None


def _as_mapping(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def _activity_action(
    activity: Mapping[str, Any],
    *,
    before_archive: Any,
    after_archive: Any,
    archive_value: Any,
) -> str:
    if activity.get("vibetable_action") == "restore":
        return "restore"
    if before_archive != archive_value and after_archive == archive_value:
        return "delete"
    if before_archive == archive_value and after_archive != archive_value:
        return "restore"
    return str(activity.get("action") or "update")


def _label_fields_from_set(fields: set[str], primary_key: str) -> list[str]:
    candidates = [
        field
        for field in sorted(fields)
        if field not in {primary_key, "status", "sort", *_SYSTEM_FIELD_NAMES}
    ]
    return candidates[:3]


def _record_label(item: Mapping[str, Any], primary_key: str, fields: Sequence[str]) -> str | None:
    for field in fields:
        value = item.get(field)
        if isinstance(value, (str, int, float)) and str(value).strip():
            return str(value)[:512]
    value = item.get(primary_key)
    return str(value)[:512] if value is not None else None


def _display_name(user: dict[str, Any]) -> str | None:
    first = user.get("first_name")
    last = user.get("last_name")
    if first or last:
        return " ".join(part for part in [first, last] if part).strip() or None
    return str(user["email"]) if user.get("email") else None


def _response_list(payload: Any) -> list[dict[str, Any]]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), list):
        return [item for item in payload["data"] if isinstance(item, dict)]
    return []


def _response_filter_count(payload: Any) -> int | None:
    meta = payload.get("meta") if isinstance(payload, Mapping) else None
    value = meta.get("filter_count") if isinstance(meta, Mapping) else None
    if isinstance(value, int) and not isinstance(value, bool) and value >= 0:
        return value
    if isinstance(value, str) and value.isdigit():
        return int(value)
    return None


def _response_object(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        return payload["data"]
    return {}


def _search_text(value: Any) -> str:
    if isinstance(value, (dict, list)):
        return json.dumps(value, ensure_ascii=False, sort_keys=True, default=str).casefold()
    return str(value).casefold()


def _segment(value: str) -> str:
    return quote(value, safe="")


def _iso_timestamp(epoch: float) -> str:
    return datetime.fromtimestamp(epoch, tz=UTC).isoformat()


__all__ = ["RESTORE_TOKEN_TTL_SECONDS", "HistoryError", "HistoryService"]
