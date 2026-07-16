"""C2 collaboration contracts: Activity/Revisions/Revert, Comments, Notifications.

These contracts describe the Directus-native collaboration features VibeTable
surfaces in the business workspace. All read system-collection data under the
current user's permissions (never an admin service token), with bounded caching
and Realtime invalidation — no local audit/notification mirror database.

Design notes
------------
* Activity/Revisions are paged per-item; the service crops fields/relations the
  user cannot read so historical values are never leaked.
* Restricted revert is a two-step flow: a preview that binds (item id, current
  revision hash, target revision), then an apply that rejects if the item
  changed since preview.
* Comments support list/create/edit-delete-own with @mention input (the server
  resolves mentions; VibeTable does not parse/send email).
* Notifications are an inbox/archive with unread count; collection/item refs
  navigate to the VibeTable workspace, admin-only links open Data Studio.
"""

from __future__ import annotations

from typing import Any, Literal

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
# Activity / Revisions
# ---------------------------------------------------------------------------


class ActivityRevisionEntry(CamelModel):
    """One revision in an item's activity timeline.

    ``delta`` is the permission-cropped field diff (only fields the user can
    read). ``activity`` carries the action/user/timestamp metadata.
    """

    revision_id: str = Field(min_length=1, max_length=128)
    activity_id: str | None = Field(default=None, max_length=128)
    action: str = Field(min_length=1, max_length=32)
    user_id: str | None = Field(default=None, max_length=128)
    user_name: str | None = Field(default=None, max_length=256)
    timestamp: str = Field(min_length=1, max_length=64)
    delta: dict[str, Any] = Field(default_factory=dict)


class ReadActivityParams(CamelModel):
    """Parameters for ``directus.readActivity``.

    Wire form:: ``{"collection": "vibetable_demo", "itemId": "1", "limit": 50, "offset": 0}``
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    limit: int = Field(default=50, ge=1, le=100)
    offset: int = Field(default=0, ge=0)


class ActivityResult(CamelModel):
    """Result of ``directus.readActivity``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    revisions: list[ActivityRevisionEntry] = Field(default_factory=list)
    total: int = Field(default=0, ge=0)
    capability_hash: str = Field(min_length=1)


class PreviewRevertParams(CamelModel):
    """Parameters for ``directus.previewRevert``.

    Wire form::

        {"collection": "vibetable_demo", "itemId": "1",
         "targetRevision": "rev-42"}

    The service reads the current item + target revision + intermediate
    revisions, computes the field diff, and returns a :class:`RevertPreview`
    bound to (item id, current hash, target revision).
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    target_revision: str = Field(min_length=1, max_length=128)


class RevertDiagnostic(CamelModel):
    """A localized diagnostic for a revert (e.g. an irrecoverable field)."""

    field: str = Field(min_length=1, max_length=128)
    severity: Literal["warning", "error"] = "warning"
    code: str = Field(min_length=1, max_length=64)
    message: str = Field(min_length=1, max_length=512)


class RevertPreview(CamelModel):
    """Result of ``directus.previewRevert``.

    ``current_hash`` binds the preview to the item's current state; the apply
    rejects if it changed. ``changes`` maps field → {before, after} (after =
    the target revision's value). ``diagnostics`` flag irrecoverable fields.
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    target_revision: str = Field(min_length=1, max_length=128)
    current_hash: str = Field(min_length=1)
    changes: dict[str, dict[str, Any]] = Field(default_factory=dict)
    diagnostics: list[RevertDiagnostic] = Field(default_factory=list)
    token: str = Field(min_length=1, max_length=2048)


class ApplyRevertParams(CamelModel):
    """Parameters for ``directus.applyRevert``.

    Wire form:: ``{"collection": "...", "itemId": "...", "token": "..."}``
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    token: str = Field(min_length=1, max_length=2048)


class RevertResult(CamelModel):
    """Result of ``directus.applyRevert``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    reverted_to_revision: str = Field(min_length=1, max_length=128)
    item: dict[str, Any]


# ---------------------------------------------------------------------------
# Comments
# ---------------------------------------------------------------------------


class CommentEntry(CamelModel):
    """One comment on an item."""

    id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    comment: str = Field(min_length=0, max_length=8192)
    user_id: str | None = Field(default=None, max_length=128)
    user_name: str | None = Field(default=None, max_length=256)
    created_on: str = Field(default="", max_length=64)
    edited_on: str | None = Field(default=None, max_length=64)


class ReadCommentsParams(CamelModel):
    """Parameters for ``directus.readComments``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    limit: int = Field(default=50, ge=1, le=100)
    offset: int = Field(default=0, ge=0)


class CommentsResult(CamelModel):
    """Result of ``directus.readComments``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    comments: list[CommentEntry] = Field(default_factory=list)
    total: int = Field(default=0, ge=0)


class CreateCommentParams(CamelModel):
    """Parameters for ``directus.createComment``.

    ``request_id`` dedupes retries (a repeated id returns the original comment
    instead of creating a duplicate).
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    comment: str = Field(min_length=1, max_length=8192)
    request_id: str = Field(default="", max_length=128)


class UpdateCommentParams(CamelModel):
    """Parameters for ``directus.updateComment`` (own comments only)."""

    comment_id: str = Field(min_length=1, max_length=128)
    comment: str = Field(min_length=1, max_length=8192)


class DeleteCommentParams(CamelModel):
    """Parameters for ``directus.deleteComment`` (own comments only)."""

    comment_id: str = Field(min_length=1, max_length=128)


class CommentMention(CamelModel):
    """A resolved @mention (name + user id, returned by the server)."""

    name: str = Field(min_length=1, max_length=256)
    user_id: str = Field(min_length=1, max_length=128)


class SearchMentionsParams(CamelModel):
    """Parameters for ``directus.searchMentions``.

    Wire form:: ``{"prefix": "ad"}``

    Returns users whose name matches the prefix (for @mention autocomplete).
    """

    prefix: str = Field(default="", max_length=128)
    limit: int = Field(default=10, ge=1, le=50)


class MentionsResult(CamelModel):
    """Result of ``directus.searchMentions``."""

    mentions: list[CommentMention] = Field(default_factory=list)


# ---------------------------------------------------------------------------
# Notifications
# ---------------------------------------------------------------------------


NotificationFolder = Literal["inbox", "archive"]


class NotificationEntry(CamelModel):
    """One notification."""

    id: str = Field(min_length=1, max_length=128)
    subject: str = Field(default="", max_length=512)
    message: str = Field(default="", max_length=4096)
    collection: str | None = Field(default=None, max_length=128)
    item_id: str | None = Field(default=None, max_length=128)
    timestamp: str = Field(default="", max_length=64)
    read: bool = False


class ReadNotificationsParams(CamelModel):
    """Parameters for ``directus.readNotifications``."""

    folder: NotificationFolder = "inbox"
    limit: int = Field(default=50, ge=1, le=100)
    offset: int = Field(default=0, ge=0)


class NotificationsResult(CamelModel):
    """Result of ``directus.readNotifications``."""

    notifications: list[NotificationEntry] = Field(default_factory=list)
    total: int = Field(default=0, ge=0)
    unread_count: int = Field(default=0, ge=0)


class NotificationIdParams(CamelModel):
    """Parameters for ``directus.archiveNotification`` / ``.deleteNotification``."""

    notification_id: str = Field(min_length=1, max_length=128)


__all__ = [
    "ActivityResult",
    "ActivityRevisionEntry",
    "ApplyRevertParams",
    "CamelModel",
    "CommentEntry",
    "CommentMention",
    "CommentsResult",
    "CreateCommentParams",
    "DeleteCommentParams",
    "MentionsResult",
    "NotificationEntry",
    "NotificationFolder",
    "NotificationIdParams",
    "NotificationsResult",
    "PreviewRevertParams",
    "ReadActivityParams",
    "ReadCommentsParams",
    "ReadNotificationsParams",
    "RevertDiagnostic",
    "RevertPreview",
    "RevertResult",
    "SearchMentionsParams",
    "UpdateCommentParams",
]
