"""Permission-safe table, row and cell history with two-phase restore."""

from __future__ import annotations

import hashlib
import hmac
import json
import secrets
import time
from collections import defaultdict
from collections.abc import Awaitable, Callable, Mapping, Sequence
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
        "schema_revision",
        "scope",
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
        schema_revision: str,
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
        self.schema_revision = schema_revision
        self.capability_hash = capability_hash
        self.latest_revision = latest_revision
        self.expires_at = expires_at


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
        readable = set(profile.fields)
        if params.field and params.field not in readable:
            raise HistoryError("history field is not readable", code="history_field_unreadable")

        token = await self._auth.access_token()
        archived_ids: set[str] | None = None
        archived_labels: dict[str, str] = {}
        if params.scope == "archived":
            archived_ids, archived_labels = await self._read_archived_records(profile, token)
            if not archived_ids:
                return self._page(params, profile, [], total=0)

        revision_filter: dict[str, Any] = {"collection": {"_eq": params.collection}}
        if params.item_id:
            revision_filter["item"] = {"_eq": params.item_id}
        elif params.record_id:
            revision_filter["item"] = {"_eq": params.record_id}
        elif archived_ids is not None:
            revision_filter["item"] = {"_in": sorted(archived_ids)}

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
                "limit": -1,
                "sort": "-activity.timestamp,-id",
            },
        )
        raw = _response_list(payload)
        if archived_ids is not None:
            raw = [revision for revision in raw if _revision_item_id(revision) in archived_ids]

        record_changes = _adjacent_record_changes(
            raw,
            readable=readable,
            relations={relation.field: relation for relation in profile.relations},
            primary_key=profile.primary_key,
            forced_labels=archived_labels,
            default_item_id=params.item_id or params.record_id,
        )
        record_changes = await self._sanitize_relation_history(
            record_changes,
            relations={relation.field: relation for relation in profile.relations},
            token=token,
        )
        groups = _group_record_changes(raw, record_changes)
        groups = [_crop_group(group, params.field) for group in groups]
        groups = [group for group in groups if group.record_changes]
        groups = [group for group in groups if _matches_filters(group, params)]
        total = len(groups)
        selected = groups[params.offset : params.offset + params.limit]
        return self._page(params, profile, selected, total=total)

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
        if params.field and params.field not in profile.fields:
            raise HistoryError("restore field is not readable", code="history_field_unreadable")

        token = await self._auth.access_token()
        current = await self._client.read_item(profile, params.item_id)
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
            fields = list(target)

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
            )
            relation = relations.get(field)
            relation_safe = True
            display_value: str | None = None
            if relation is not None:
                if relation.kind not in {"m2o", "file"}:
                    relation_safe = False
                    reason = reason or (
                        "relation_unsafe",
                        "complex_relation",
                        "Complex relations cannot be restored atomically.",
                    )
                elif target_value is not None and reason is None:
                    relation_safe, display_value = await self._resolve_relation_target(
                        relation, target_value, token
                    )
                    if not relation_safe:
                        reason = (
                            "relation_unsafe",
                            "relation_target_unavailable",
                            "Related target is unavailable or not readable.",
                        )

            if current_value != target_value:
                if relation is None:
                    scalar_changes.append(
                        ScalarFieldChange(field=field, before=current_value, after=target_value)
                    )
                elif relation_safe:
                    relation_changes.append(
                        RelationFieldChange(
                            field=field,
                            kind=relation.kind,
                            related_collection=relation.related_collection,
                            related_item_id=str(target_value) if target_value is not None else None,
                            display_value=display_value,
                            before_item_id=str(current_value)
                            if current_value is not None
                            else None,
                            after_item_id=str(target_value) if target_value is not None else None,
                            after_display_value=display_value,
                            target_available=True,
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
            schema_revision=self._schema_revision,
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
        """Apply only the patch captured by preview and verify a new revision."""
        stored = self._restore_tokens.get(params.token)
        if stored is None:
            raise HistoryError("restore token not found", code="restore_token_unknown")
        if self._clock() >= stored.expires_at:
            self._restore_tokens.pop(params.token, None)
            raise HistoryError("restore token expired", code="restore_token_expired")
        if stored.collection != params.collection or stored.item_id != params.item_id:
            raise HistoryError(
                "restore token scope does not match request", code="restore_scope_mismatch"
            )
        if not stored.patch:
            raise HistoryError("preview contains no restorable fields", code="restore_no_fields")

        profile = self._profile(params.collection)
        if not profile.allow_revision_revert:
            raise HistoryError("revision restore is no longer allowed", code="restore_not_allowed")
        if (
            self._schema_revision != stored.schema_revision
            or profile.capability_hash != stored.capability_hash
        ):
            raise HistoryError(
                "schema changed since the restore was previewed", code="schema_drift"
            )

        current = await self._client.read_item(profile, params.item_id)
        if _hash_item(current, stored.watched_fields) != stored.current_hash:
            raise HistoryError(
                "item changed since the restore was previewed", code="restore_conflict"
            )
        token = await self._auth.access_token()
        pre_latest = await self._latest_revision_id(params.collection, params.item_id, token)
        if stored.scope == "archived":
            if (
                profile.archive_field is None
                or current.get(profile.archive_field) != profile.archive_value
            ):
                raise HistoryError("archived record state changed", code="restore_conflict")
            if stored.latest_revision != pre_latest:
                raise HistoryError("archived record version changed", code="restore_conflict")

        await self._client.update_item(
            profile,
            params.item_id,
            dict(stored.patch),
            request_id=f"history-restore-{hashlib.sha256(params.token.encode()).hexdigest()[:24]}",
        )
        post_latest = await self._latest_revision_id(params.collection, params.item_id, token)
        if post_latest is None or post_latest == pre_latest:
            raise HistoryError("restore did not create a new revision", code="revision_not_created")
        self._restore_tokens.pop(params.token, None)
        refreshed = await self._client.read_item(profile, params.item_id)
        return RestoreResult(
            collection=params.collection,
            item_id=params.item_id,
            restored_to_revision=stored.target_revision,
            new_revision_id=post_latest,
            item=refreshed,
        )

    async def _read_archived_records(
        self, profile: CollectionProfile, token: str
    ) -> tuple[set[str], dict[str, str]]:
        if profile.archive_field is None:
            raise HistoryError("collection has no archive field", code="archive_not_supported")
        label_fields = _label_fields(profile)
        payload = await self._transport.request(
            "GET",
            f"/items/{_segment(profile.collection)}",
            access_token=token,
            query={
                "filter": {profile.archive_field: {"_eq": profile.archive_value}},
                "fields": [profile.primary_key, *label_fields],
                "limit": -1,
            },
        )
        ids: set[str] = set()
        labels: dict[str, str] = {}
        for item in _response_list(payload):
            item_id = item.get(profile.primary_key)
            if item_id is None:
                continue
            key = str(item_id)
            ids.add(key)
            labels[key] = _record_label(item, profile.primary_key, label_fields) or key
        return ids, labels

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
    ) -> dict[str, HistoryRecordChange]:
        """Resolve readable labels and remove ids for unavailable relation targets."""
        cache: dict[tuple[str, str], tuple[bool, str | None]] = {}

        async def resolve(
            relation: RelationProfile, item_id: str | None
        ) -> tuple[bool, str | None]:
            if item_id is None:
                return True, None
            key = (relation.field, item_id)
            if key not in cache:
                cache[key] = await self._resolve_relation_target(relation, item_id, token)
            return cache[key]

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
                before_ok, before_display = await resolve(relation, change.before_item_id)
                after_ok, after_display = await resolve(relation, change.after_item_id)
                safe_relations.append(
                    change.model_copy(
                        update={
                            "related_item_id": change.after_item_id if after_ok else None,
                            "before_item_id": change.before_item_id if before_ok else None,
                            "after_item_id": change.after_item_id if after_ok else None,
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
        )

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise DirectusSchemaError(f"collection {collection!r} is not in capability manifest")
        return profile

    def _mint_restore(self, **values: Any) -> str:
        raw = secrets.token_urlsafe(18)
        token = f"rst-{raw}.{self._token_secret.sign(raw)}"
        self._restore_tokens[token] = _StoredRestore(
            **values,
            expires_at=self._clock() + RESTORE_TOKEN_TTL_SECONDS,
        )
        return token


def _adjacent_record_changes(
    revisions: list[dict[str, Any]],
    *,
    readable: set[str],
    relations: dict[str, RelationProfile],
    primary_key: str,
    forced_labels: dict[str, str],
    default_item_id: str | None,
) -> dict[str, HistoryRecordChange]:
    by_item: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for raw in revisions:
        item_id = _revision_item_id(raw) or default_item_id
        if item_id:
            by_item[item_id].append(raw)
    result: dict[str, HistoryRecordChange] = {}
    for item_id, item_revisions in by_item.items():
        prior: dict[str, Any] = {}
        ordered = sorted(item_revisions, key=_revision_order_key)
        for raw in ordered:
            raw_data = raw.get("data")
            delta = raw.get("delta")
            snapshot = (
                dict(raw_data)
                if isinstance(raw_data, Mapping)
                else {**prior, **(dict(delta) if isinstance(delta, Mapping) else {})}
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
                prior = snapshot
                continue
            revision_id = str(raw.get("id", ""))
            activity = _as_mapping(raw.get("activity"))
            label = forced_labels.get(item_id) or _record_label(
                snapshot,
                primary_key,
                _label_fields_from_set(readable, primary_key),
            )
            result[revision_id] = HistoryRecordChange(
                revision_id=revision_id,
                item_id=item_id,
                record_label=label,
                action=str(activity.get("action") or "update"),
                scalar_changes=scalar,
                relation_changes=relation_changes,
            )
            prior = snapshot
    return result


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
    if params.date_from and group.timestamp < params.date_from:
        return False
    if params.date_to and group.timestamp > params.date_to:
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
                    relation_change.display_value,
                ]
            )
    return any(needle in _search_text(value) for value in values if value is not None)


def _field_restriction(
    field: str,
    value: Any,
    *,
    profile: CollectionProfile,
    metadata: Mapping[str, Any],
    current_value: Any,
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
    if field not in profile.update_fields:
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


def _revision_order_key(raw: Mapping[str, Any]) -> tuple[str, str]:
    activity = _as_mapping(raw.get("activity"))
    return str(activity.get("timestamp") or ""), str(raw.get("id") or "")


def _as_mapping(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def _label_fields(profile: CollectionProfile) -> list[str]:
    return _label_fields_from_set(set(profile.fields), profile.primary_key)


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
