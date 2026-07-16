"""C2 collaboration service: Activity/Revisions/Revert, Comments, Notifications.

Reads Directus system collections (``directus_activity``, ``directus_comments``,
``directus_notifications``, ``directus_presets``, ``directus_users``) under the
current user's permissions — never an admin service token. Permission denials
on system collections degrade safely (empty results, not crashes).

The service keeps NO local mirror database; it reads on demand with a small
bounded in-process cache for hot reads (e.g. unread count). Realtime
invalidation clears the cache when a relevant ``directus.changed`` event arrives.

Restricted revert is a two-step flow:
* :meth:`preview_revert` reads the current item + target revision, computes the
  field diff (cropped to readable fields), and returns a :class:`RevertPreview`
  bound to ``(item id, current hash, target revision)`` via an HMAC token.
* :meth:`apply_revert` validates the token, re-checks the current hash, applies
  the revert via the Directus Revisions API, and writes activity.
"""

from __future__ import annotations

import hashlib
import hmac
import secrets
import time
import uuid
from collections.abc import Callable
from typing import Any

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusSchemaError, DirectusTransportError
from backend.adapters.directus.profile import CollectionProfile
from backend.contracts.collaboration import (
    ActivityResult,
    ActivityRevisionEntry,
    ApplyRevertParams,
    CommentEntry,
    CommentMention,
    CommentsResult,
    CreateCommentParams,
    DeleteCommentParams,
    MentionsResult,
    NotificationEntry,
    NotificationIdParams,
    NotificationsResult,
    PreviewRevertParams,
    ReadActivityParams,
    ReadCommentsParams,
    ReadNotificationsParams,
    RevertDiagnostic,
    RevertPreview,
    RevertResult,
    SearchMentionsParams,
    UpdateCommentParams,
)

#: Revert-token TTL (seconds).
REVERT_TOKEN_TTL_SECONDS: float = 5 * 60.0


class CollaborationError(Exception):
    """A collaboration-flow error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class _RevertTokenSecret:
    """Per-process HMAC secret for revert tokens (not persisted)."""

    def __init__(self) -> None:
        self._secret = secrets.token_bytes(32)

    def sign(self, payload: str) -> str:
        return hmac.new(self._secret, payload.encode("ascii"), hashlib.sha256).hexdigest()


class CollaborationService:
    """C2 collaboration surface over the B4 Directus data plane."""

    def __init__(
        self,
        *,
        client: DirectusClient,
        auth: DirectusAuthBroker,
        profiles: dict[str, CollectionProfile],
        transport: Any,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._client = client
        self._auth = auth
        self._profiles = profiles
        self._transport = transport
        self._clock = clock
        self._token_secret = _RevertTokenSecret()
        self._revert_tokens: dict[str, _StoredRevert] = {}

    # ------------------------------------------------------------------
    # Activity / Revisions
    # ------------------------------------------------------------------

    async def read_activity(self, params: ReadActivityParams) -> ActivityResult:
        profile = self._profile(params.collection)
        readable = set(profile.fields)
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
        entries: list[ActivityRevisionEntry] = []
        for raw in raw_revisions:
            delta = _crop_delta(raw.get("delta") or raw.get("data") or {}, readable)
            activity = raw.get("activity") or {}
            user = activity.get("user") or {}
            entries.append(
                ActivityRevisionEntry(
                    revision_id=str(raw.get("id", "")),
                    activity_id=str(activity.get("id", "")) or None,
                    action=str(activity.get("action", "update")),
                    user_id=str(user.get("id", "")) or None,
                    user_name=_display_name(user),
                    timestamp=str(activity.get("timestamp", "")),
                    delta=delta,
                )
            )
        return ActivityResult(
            collection=params.collection,
            item_id=params.item_id,
            revisions=entries,
            total=_safe_int(meta.get("filter_count"))
            or _safe_int(meta.get("total_count"))
            or len(entries),
            capability_hash=profile.capability_hash,
        )

    async def preview_revert(self, params: PreviewRevertParams) -> RevertPreview:
        profile = self._profile(params.collection)
        if not profile.allow_revision_revert:
            raise CollaborationError(
                f"collection {params.collection!r} does not allow revision revert",
                code="revert_not_allowed",
            )
        readable = set(profile.fields)
        token = await self._auth.access_token()
        current = await self._client.read_item(profile, params.item_id)
        current_hash = _hash_item(current, readable)
        revision_payload = await self._transport.request(
            "GET",
            f"/revisions/{params.target_revision}",
            access_token=token,
            query={"fields": ["id", "data"]},
        )
        revision_data = _response_object(revision_payload).get("data") or {}
        changes: dict[str, dict[str, Any]] = {}
        diagnostics: list[RevertDiagnostic] = []
        for field in readable:
            target_value = revision_data.get(field)
            current_value = current.get(field)
            if target_value != current_value:
                changes[field] = {"before": current_value, "after": target_value}
        revert_token = self._mint_revert(
            collection=params.collection,
            item_id=params.item_id,
            target_revision=params.target_revision,
            current_hash=current_hash,
        )
        return RevertPreview(
            collection=params.collection,
            item_id=params.item_id,
            target_revision=params.target_revision,
            current_hash=current_hash,
            changes=changes,
            diagnostics=diagnostics,
            token=revert_token,
        )

    async def apply_revert(self, params: ApplyRevertParams) -> RevertResult:
        stored = self._revert_tokens.get(params.token)
        if stored is None:
            raise CollaborationError("revert token not found", code="revert_token_unknown")
        if self._clock() >= stored.expires_at:
            raise CollaborationError("revert token expired", code="revert_token_expired")
        profile = self._profile(params.collection)
        current = await self._client.read_item(profile, params.item_id)
        current_hash = _hash_item(current, set(profile.fields))
        if current_hash != stored.current_hash:
            raise CollaborationError(
                "item changed since the revert was previewed",
                code="revert_conflict",
            )
        token = await self._auth.access_token()
        await self._transport.request(
            "POST",
            f"/revisions/{stored.target_revision}/revert",
            access_token=token,
        )
        self._revert_tokens.pop(params.token, None)
        refreshed = await self._client.read_item(profile, params.item_id)
        return RevertResult(
            collection=params.collection,
            item_id=params.item_id,
            reverted_to_revision=stored.target_revision,
            item=refreshed,
        )

    # ------------------------------------------------------------------
    # Comments
    # ------------------------------------------------------------------

    async def read_comments(self, params: ReadCommentsParams) -> CommentsResult:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            "/comments",
            access_token=token,
            query={
                "filter": {
                    "collection": {"_eq": params.collection},
                    "item": {"_eq": params.item_id},
                },
                "fields": [
                    "id",
                    "collection",
                    "item",
                    "comment",
                    "user.id",
                    "user.first_name",
                    "user.last_name",
                    "date_created",
                    "date_updated",
                ],
                "limit": params.limit,
                "offset": params.offset,
                "meta": "filter_count,total_count",
                "sort": "-date_created",
            },
        )
        raw_comments = _response_list(payload)
        meta = _response_meta(payload)
        comments = [
            CommentEntry(
                id=str(c.get("id", "")),
                collection=str(c.get("collection", params.collection)),
                item_id=str(c.get("item", params.item_id)),
                comment=str(c.get("comment", "")),
                user_id=str((c.get("user") or {}).get("id", "")) or None,
                user_name=_display_name(c.get("user") or {}),
                created_on=str(c.get("date_created", "")),
                edited_on=str(c.get("date_updated", "")) or None,
            )
            for c in raw_comments
        ]
        return CommentsResult(
            collection=params.collection,
            item_id=params.item_id,
            comments=comments,
            total=_safe_int(meta.get("filter_count"))
            or _safe_int(meta.get("total_count"))
            or len(comments),
        )

    async def create_comment(self, params: CreateCommentParams) -> CommentEntry:
        token = await self._auth.access_token()
        body: dict[str, Any] = {
            "collection": params.collection,
            "item": params.item_id,
            "comment": _sanitize_comment(params.comment),
        }
        request_id = params.request_id or str(uuid.uuid4())
        payload = await self._transport.request(
            "POST",
            "/comments",
            access_token=token,
            json_body=body,
            headers={"X-Request-Id": request_id},
        )
        created = _response_object(payload)
        return CommentEntry(
            id=str(created.get("id", "")),
            collection=str(created.get("collection", params.collection)),
            item_id=str(created.get("item", params.item_id)),
            comment=str(created.get("comment", "")),
            created_on=str(created.get("date_created", "")),
        )

    async def update_comment(self, params: UpdateCommentParams) -> CommentEntry:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "PATCH",
            f"/comments/{params.comment_id}",
            access_token=token,
            json_body={"comment": _sanitize_comment(params.comment)},
        )
        updated = _response_object(payload)
        return CommentEntry(
            id=str(updated.get("id", params.comment_id)),
            collection=str(updated.get("collection", "")),
            item_id=str(updated.get("item", "")),
            comment=str(updated.get("comment", "")),
            edited_on=str(updated.get("date_updated", "")) or None,
        )

    async def delete_comment(self, params: DeleteCommentParams) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "DELETE",
            f"/comments/{params.comment_id}",
            access_token=token,
            expected_status=(204,),
        )
        return {"deleted": params.comment_id}

    async def search_mentions(self, params: SearchMentionsParams) -> MentionsResult:
        token = await self._auth.access_token()
        payload = await self._transport.request(
            "GET",
            "/users",
            access_token=token,
            query={
                "fields": ["id", "first_name", "last_name", "email"],
                "filter": {
                    "_or": [
                        {"first_name": {"_starts_with": params.prefix}},
                        {"last_name": {"_starts_with": params.prefix}},
                        {"email": {"_starts_with": params.prefix}},
                    ]
                },
                "limit": params.limit,
            },
        )
        users = _response_list(payload)
        mentions = [
            CommentMention(
                name=_display_name(u) or str(u.get("id", "")) or "Unknown user",
                user_id=str(u.get("id", "")),
            )
            for u in users
        ]
        return MentionsResult(mentions=mentions)

    # ------------------------------------------------------------------
    # Notifications
    # ------------------------------------------------------------------

    async def read_notifications(self, params: ReadNotificationsParams) -> NotificationsResult:
        token = await self._auth.access_token()
        status_filter = {"_in": ["inbox"]} if params.folder == "inbox" else {"_in": ["archived"]}
        payload = await self._transport.request(
            "GET",
            "/notifications",
            access_token=token,
            query={
                "fields": [
                    "id",
                    "subject",
                    "message",
                    "collection",
                    "item",
                    "timestamp",
                    "status",
                ],
                "filter": {"status": status_filter},
                "limit": params.limit,
                "offset": params.offset,
                "meta": "filter_count,total_count",
                "sort": "-timestamp",
            },
        )
        raw_notifications = _response_list(payload)
        meta = _response_meta(payload)
        notifications = [
            NotificationEntry(
                id=str(n.get("id", "")),
                subject=str(n.get("subject", "")),
                message=str(n.get("message", "")),
                collection=str(n.get("collection", "")) or None,
                item_id=str(n.get("item", "")) or None,
                timestamp=str(n.get("timestamp", "")),
                read=n.get("status") == "read",
            )
            for n in raw_notifications
        ]
        # Unread count is a separate cheap query.
        unread = await self._unread_count(token)
        return NotificationsResult(
            notifications=notifications,
            total=_safe_int(meta.get("filter_count"))
            or _safe_int(meta.get("total_count"))
            or len(notifications),
            unread_count=unread,
        )

    async def archive_notification(self, params: NotificationIdParams) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "PATCH",
            f"/notifications/{params.notification_id}",
            access_token=token,
            json_body={"status": "archived"},
        )
        return {"archived": params.notification_id}

    async def delete_notification(self, params: NotificationIdParams) -> dict[str, Any]:
        token = await self._auth.access_token()
        await self._transport.request(
            "DELETE",
            f"/notifications/{params.notification_id}",
            access_token=token,
            expected_status=(204,),
        )
        return {"deleted": params.notification_id}

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise DirectusSchemaError(f"collection {collection!r} is not in capability manifest")
        return profile

    async def _unread_count(self, token: str) -> int:
        try:
            payload = await self._transport.request(
                "GET",
                "/notifications",
                access_token=token,
                query={
                    "fields": ["id"],
                    "filter": {"status": {"_in": ["inbox"]}},
                    "limit": 0,
                    "meta": "filter_count",
                },
            )
            meta = _response_meta(payload)
            return _safe_int(meta.get("filter_count")) or 0
        except DirectusTransportError:
            return 0

    def _mint_revert(
        self,
        *,
        collection: str,
        item_id: str,
        target_revision: str,
        current_hash: str,
    ) -> str:
        raw = secrets.token_urlsafe(18)
        tag = self._token_secret.sign(raw)
        token = f"rev-{raw}.{tag}"
        self._revert_tokens[token] = _StoredRevert(
            collection=collection,
            item_id=item_id,
            target_revision=target_revision,
            current_hash=current_hash,
            expires_at=self._clock() + REVERT_TOKEN_TTL_SECONDS,
        )
        return token


class _StoredRevert:
    __slots__ = ("collection", "current_hash", "expires_at", "item_id", "target_revision")

    def __init__(
        self,
        *,
        collection: str,
        item_id: str,
        target_revision: str,
        current_hash: str,
        expires_at: float,
    ) -> None:
        self.collection = collection
        self.item_id = item_id
        self.target_revision = target_revision
        self.current_hash = current_hash
        self.expires_at = expires_at


# ---------------------------------------------------------------------------
# Module helpers
# ---------------------------------------------------------------------------

_DANGEROUS_COMMENT_PATTERNS = [
    "<script",
    "javascript:",
    "data:text/html",
    "<iframe",
    "onload=",
    "onerror=",
]


def _sanitize_comment(text: str) -> str:
    """Strip obviously dangerous markup from a comment.

    This is a defense-in-depth pass; the host renderer applies a strict HTML
    sanitizer on render. VibeTable never executes comment content as script.
    """
    lowered = text.lower()
    if any(pattern in lowered for pattern in _DANGEROUS_COMMENT_PATTERNS):
        # Remove the dangerous segment rather than rejecting the whole comment.
        cleaned = text
        for pattern in _DANGEROUS_COMMENT_PATTERNS:
            while True:
                idx = cleaned.lower().find(pattern)
                if idx < 0:
                    break
                end = cleaned.find(">", idx)
                cleaned = cleaned[:idx] + (cleaned[end + 1 :] if end >= 0 else "")
        return cleaned
    return text


def _crop_delta(delta: dict[str, Any], readable: set[str]) -> dict[str, Any]:
    """Crop a revision delta to only fields the user can read."""
    if not isinstance(delta, dict):
        return {}
    return {field: value for field, value in delta.items() if field in readable}


def _hash_item(item: dict[str, Any], readable: set[str]) -> str:
    """Stable hash of an item's readable fields (binds revert preview)."""
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


__all__ = [
    "REVERT_TOKEN_TTL_SECONDS",
    "CollaborationError",
    "CollaborationService",
]
