"""D1 file-tools contracts: storage boundary, operation journal, Directus Files.

* **Storage boundary** (Task 1): every workflow tags its input/output as
  ``local-input``, ``local-output``, ``directus-file`` or ``temporary`` so the
  authority (who owns the canonical copy) is explicit.
* **Operation journal** (Task 2): destructive multi-step operations keep a
  recovery journal.
* **Directus Files** (Task 3): business attachments live in Directus Files;
  VibeTable keeps no second authoritative metadata. Asset Preset previews use only
  approved ``key=<preset>`` (no arbitrary width/height/quality).
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
# Storage boundary (Task 1)
# ---------------------------------------------------------------------------


StorageBoundary = Literal["local-input", "local-output", "directus-file", "temporary"]


class DirectusFileMetadata(CamelModel):
    """Metadata for a Directus file (the authoritative copy lives server-side)."""

    id: str = Field(min_length=1, max_length=128)
    filename: str = Field(default="", max_length=256)
    mime_type: str = Field(default="", max_length=128)
    file_size: int = Field(default=0, ge=0)
    uploaded_on: str = Field(default="", max_length=64)
    storage: str = Field(default="", max_length=64)
    checksum: str | None = Field(default=None, max_length=128)


# ---------------------------------------------------------------------------
# Operation journal (Task 2)
# ---------------------------------------------------------------------------


JournalState = Literal[
    "planned",
    "running",
    "committed",
    "rollback-required",
    "rolled-back",
    "failed",
]


class JournalStep(CamelModel):
    """One step in a destructive operation journal."""

    kind: str = Field(min_length=1, max_length=64)
    source: str = Field(default="", max_length=512)
    target: str = Field(default="", max_length=512)
    backup_path: str | None = Field(default=None, max_length=512)
    backup_hash: str | None = Field(default=None, max_length=128)


class JournalEntry(CamelModel):
    """A recovery journal entry for a destructive file operation."""

    journal_id: str = Field(min_length=1, max_length=128)
    operation: str = Field(min_length=1, max_length=64)
    state: JournalState
    steps: list[JournalStep] = Field(default_factory=list)
    created_at: str = Field(default="", max_length=64)
    error: str | None = Field(default=None, max_length=1024)


class ListJournalParams(CamelModel):
    """Parameters for ``file.listJournal`` (recovery prompt on startup)."""


class JournalResult(CamelModel):
    """Result of ``file.listJournal``."""

    pending: list[JournalEntry] = Field(default_factory=list)


class JournalIdParams(CamelModel):
    """Parameters for ``file.resolveJournal`` / ``file.discardJournal``."""

    journal_id: str = Field(min_length=1, max_length=128)


class ResolveJournalParams(JournalIdParams):
    """Parameters for ``file.resolveJournal``.

    ``action`` is ``rollback`` (restore backups) or ``keep`` (accept current
    state). The service never auto-rolls-back on startup.
    """

    action: Literal["rollback", "keep"]


# ---------------------------------------------------------------------------
# Directus Files workspace (Task 3)
# ---------------------------------------------------------------------------


class ReadFilesParams(CamelModel):
    """Parameters for ``directus.readFiles`` (attachment list for an item)."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    relation_field: str = Field(min_length=1, max_length=128)


class FilesResult(CamelModel):
    """Result of ``directus.readFiles``."""

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    files: list[DirectusFileMetadata] = Field(default_factory=list)


class UploadFileParams(CamelModel):
    """Parameters for ``directus.uploadFile``.

    Wire form::

        {"grantId": "g1", "collection": "vibetable_demo", "itemId": "1",
         "relationField": "document"}

    The grant is an import-source grant (read direction) for the local file the
    WPF picker chose. The service uploads to Directus Files and links the
    relation.
    """

    grant_id: str = Field(min_length=1, max_length=128)
    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    relation_field: str = Field(min_length=1, max_length=128)


class UnlinkFileParams(CamelModel):
    """Parameters for ``directus.unlinkFile`` (remove the business relation).

    Unlinking does NOT delete the Directus file; that requires
    :class:`DeleteFileParams` plus an extra permission check.
    """

    collection: str = Field(min_length=1, max_length=128)
    item_id: str = Field(min_length=1, max_length=128)
    relation_field: str = Field(min_length=1, max_length=128)
    file_id: str = Field(min_length=1, max_length=128)


class DeleteFileParams(CamelModel):
    """Parameters for ``directus.deleteFile`` (permanent, needs permission).

    The service checks references before deleting.
    """

    file_id: str = Field(min_length=1, max_length=128)


class PresetPreviewParams(CamelModel):
    """Parameters for ``directus.presetPreview`` (Asset Preset thumbnail).

    ``preset_key`` MUST be an approved key from the capability profile; the Web
    layer never sends arbitrary width/height/quality/format.
    """

    file_id: str = Field(min_length=1, max_length=128)
    preset_key: str = Field(min_length=1, max_length=64)


class PresetPreviewResult(CamelModel):
    """Result of ``directus.presetPreview``."""

    file_id: str = Field(min_length=1, max_length=128)
    preset_key: str = Field(min_length=1, max_length=64)
    url: str = Field(default="", max_length=2048)
    cached: bool = False


__all__ = [
    "CamelModel",
    "DeleteFileParams",
    "DirectusFileMetadata",
    "FilesResult",
    "JournalEntry",
    "JournalIdParams",
    "JournalResult",
    "JournalState",
    "JournalStep",
    "ListJournalParams",
    "PresetPreviewParams",
    "PresetPreviewResult",
    "ReadFilesParams",
    "ResolveJournalParams",
    "StorageBoundary",
    "UnlinkFileParams",
    "UploadFileParams",
]
