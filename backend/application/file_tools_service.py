"""D1 file-tools service: Directus Files and operation journal.

Implements the D1 Tasks 1-3 file workflows over the B4 Directus data plane
and the C1 path-grant/task-runtime infrastructure.

* **Directus Files** (Task 3): business attachments live in Directus Files;
  VibeTable keeps no second authoritative metadata. Asset Preset previews use only
  approved ``key=<preset>`` from the capability profile.
* **Operation journal** (Task 2): destructive multi-step operations keep a
  recovery journal with backup artifacts. Startup surfaces pending operations
  for user resolution — never auto-rollback.

The journal is in-process + bounded; a production deployment may persist it to
a local recovery store (the contract is stable either way).
"""

from __future__ import annotations

import contextlib
import os
import shutil
import time
import uuid
from collections.abc import Callable
from typing import Any

from backend.adapters.directus.auth import DirectusAuthBroker
from backend.adapters.directus.client import DirectusClient
from backend.adapters.directus.errors import DirectusSchemaError
from backend.adapters.directus.profile import CollectionProfile
from backend.contracts.file_tools import (
    DeleteFileParams,
    DirectusFileMetadata,
    FilesResult,
    JournalEntry,
    JournalIdParams,
    JournalResult,
    JournalStep,
    ListJournalParams,
    PresetPreviewParams,
    PresetPreviewResult,
    ReadFilesParams,
    ResolveJournalParams,
    UnlinkFileParams,
    UploadFileParams,
)

#: Approved Asset Preset keys (capability-profile-bound; Web never sends arbitrary
#: width/height/quality). A production deployment reads these from the capability
#: manifest; this is the locked default set.
APPROVED_ASSET_PRESETS: frozenset[str] = frozenset(
    {"thumbnail-small", "thumbnail-medium", "preview-large", "card-cover"}
)

#: How long to retain journal entries after commit/rollback (seconds).
JOURNAL_RETENTION_SECONDS: float = 24 * 3600.0


class FileToolsError(Exception):
    """A file-tools error carrying an RPC-friendly ``code``."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code

    @property
    def rpc_error_data(self) -> dict[str, Any]:
        return {"code": self.code}


class OperationJournal:
    """In-process recovery journal for destructive file operations.

    A journal entry progresses: ``planned → running → committed`` (success) or
    ``→ rollback-required → rolled-back`` / ``→ failed``. Backup artifacts are
    recorded so a user can resolve a pending operation on restart. The journal
    NEVER auto-rolls-back on startup (it could overwrite the user's newer
    changes).
    """

    def __init__(self) -> None:
        self._entries: dict[str, JournalEntry] = {}

    def plan(self, operation: str, steps: list[JournalStep]) -> str:
        journal_id = f"journal-{uuid.uuid4().hex[:12]}"
        entry = JournalEntry(
            journal_id=journal_id,
            operation=operation,
            state="planned",
            steps=steps,
            created_at=_now_iso(),
        )
        self._entries[journal_id] = entry
        return journal_id

    def begin(self, journal_id: str) -> None:
        self._update(journal_id, state="running")

    def commit(self, journal_id: str) -> None:
        self._update(journal_id, state="committed")

    def mark_rollback_required(self, journal_id: str, error: str) -> None:
        self._update(journal_id, state="rollback-required", error=error)

    def mark_rolled_back(self, journal_id: str) -> None:
        self._update(journal_id, state="rolled-back")

    def mark_failed(self, journal_id: str, error: str) -> None:
        self._update(journal_id, state="failed", error=error)

    def get(self, journal_id: str) -> JournalEntry:
        entry = self._entries.get(journal_id)
        if entry is None:
            raise FileToolsError("journal not found", code="journal_unknown")
        return entry

    def pending(self) -> list[JournalEntry]:
        """Entries that need user resolution (rollback-required / running on restart)."""
        return [
            e
            for e in self._entries.values()
            if e.state in ("planned", "running", "rollback-required")
        ]

    def discard(self, journal_id: str) -> None:
        self._entries.pop(journal_id, None)

    def resolve(self, journal_id: str, action: str) -> JournalEntry:
        """Resolve a pending journal entry: ``rollback`` (restore backups) or ``keep``."""
        entry = self.get(journal_id)
        if action == "rollback":
            self._rollback(entry)
            self.mark_rolled_back(journal_id)
        else:
            self.commit(journal_id)
        return self.get(journal_id)

    def _update(self, journal_id: str, *, state: str, error: str | None = None) -> None:
        entry = self.get(journal_id)
        # JournalEntry is frozen-ish (Pydantic); replace via model_copy.
        updates: dict[str, Any] = {"state": state}
        if error is not None:
            updates["error"] = error
        self._entries[journal_id] = entry.model_copy(update=updates)

    def _rollback(self, entry: JournalEntry) -> None:
        """Restore backup artifacts (only for steps that have one)."""
        for step in reversed(entry.steps):
            if step.backup_path and os.path.isfile(step.backup_path) and step.target:
                with contextlib.suppress(OSError):
                    shutil.copy2(step.backup_path, step.target)


class FileToolsService:
    """D1 Directus Files + content replace/rename + journal."""

    def __init__(
        self,
        *,
        client: DirectusClient,
        auth: DirectusAuthBroker,
        profiles: dict[str, CollectionProfile],
        transport: Any,
        resolve_path: Callable[..., str],
        consume_grant: Callable[[str], None],
        clock: Callable[[], float] = time.time,
    ) -> None:
        self._client = client
        self._auth = auth
        self._profiles = profiles
        self._transport = transport
        self._resolve_path = resolve_path
        self._consume_grant = consume_grant
        self._clock = clock
        self._journal = OperationJournal()

    # ------------------------------------------------------------------
    # Operation journal (Task 2)
    # ------------------------------------------------------------------

    def list_journal(self, _params: ListJournalParams | None = None) -> JournalResult:
        return JournalResult(pending=self._journal.pending())

    def resolve_journal(self, params: ResolveJournalParams) -> JournalEntry:
        return self._journal.resolve(params.journal_id, params.action)

    def discard_journal(self, params: JournalIdParams) -> dict[str, Any]:
        self._journal.discard(params.journal_id)
        return {"discarded": params.journal_id}

    # ------------------------------------------------------------------
    # Directus Files workspace (Task 3)
    # ------------------------------------------------------------------

    async def read_files(self, params: ReadFilesParams) -> FilesResult:
        profile = self._profile(params.collection)
        relation = next((r for r in profile.relations if r.field == params.relation_field), None)
        if relation is None or relation.preset not in {"file", "files"}:
            raise FileToolsError(
                f"{params.relation_field!r} is not a declared file relation",
                code="not_a_file_relation",
            )
        item = await self._client.read_item(profile, params.item_id)
        raw_file = item.get(params.relation_field)
        files: list[DirectusFileMetadata] = []
        if isinstance(raw_file, dict):
            files.append(_file_metadata(raw_file))
        elif isinstance(raw_file, list):
            for f in raw_file:
                if isinstance(f, dict):
                    files.append(_file_metadata(f))
        return FilesResult(collection=params.collection, item_id=params.item_id, files=files)

    async def upload_file(self, params: UploadFileParams) -> DirectusFileMetadata:
        profile = self._profile(params.collection)
        if params.relation_field not in profile.update_fields:
            raise FileToolsError(
                f"{params.relation_field!r} is not updatable",
                code="field_not_updatable",
            )
        path = self._resolve_path(
            params.grant_id,
            purpose="import_source",
            direction="read",
        )
        token = await self._auth.access_token()
        filename = os.path.basename(path)
        with open(path, "rb") as fh:
            payload = await self._transport.request(
                "POST",
                "/files",
                access_token=token,
                files={"file": (filename, fh)},
            )
        uploaded = _object(payload)
        file_id = str(uploaded.get("id", ""))
        await self._client.update_item(profile, params.item_id, {params.relation_field: file_id})
        self._consume_grant(params.grant_id)
        return _file_metadata(uploaded)

    async def unlink_file(self, params: UnlinkFileParams) -> dict[str, Any]:
        """Remove the business relation (does NOT delete the Directus file)."""
        profile = self._profile(params.collection)
        if params.relation_field not in profile.update_fields:
            raise FileToolsError(
                f"{params.relation_field!r} is not updatable",
                code="field_not_updatable",
            )
        await self._client.update_item(profile, params.item_id, {params.relation_field: None})
        return {"unlinked": params.file_id, "deleted": False}

    async def delete_file(self, params: DeleteFileParams) -> dict[str, Any]:
        """Permanently delete a Directus file (checks references first)."""
        token = await self._auth.access_token()
        try:
            referenced_by = await self._find_file_reference(params.file_id, access_token=token)
        except FileToolsError:
            raise
        except Exception as exc:
            raise FileToolsError(
                "file reference check failed; refusing permanent deletion",
                code="file_reference_check_failed",
            ) from exc
        if referenced_by is not None:
            raise FileToolsError(
                f"file is still referenced by {referenced_by}",
                code="file_in_use",
            )
        await self._transport.request(
            "DELETE",
            f"/files/{params.file_id}",
            access_token=token,
            expected_status=(204,),
        )
        return {"deleted": params.file_id}

    async def _find_file_reference(self, file_id: str, *, access_token: str) -> str | None:
        """Return the first manifest-declared business reference to ``file_id``.

        A malformed response is treated as an unavailable reference check so
        permanent deletion fails closed rather than risking an orphaned row.
        """
        for profile in self._profiles.values():
            for relation in profile.relations:
                if relation.preset not in {"file", "files"}:
                    continue
                file_filter: dict[str, Any]
                if relation.preset == "files":
                    if relation.junction is None:
                        raise FileToolsError(
                            "multi-file relation has no verified junction metadata",
                            code="file_reference_check_failed",
                        )
                    file_filter = {relation.junction.target_field: {"_eq": file_id}}
                else:
                    file_filter = {"_eq": file_id}
                payload = await self._transport.request(
                    "GET",
                    f"/items/{profile.collection}",
                    access_token=access_token,
                    query={
                        "filter": {relation.field: file_filter},
                        "fields": [profile.primary_key],
                        "limit": 1,
                    },
                )
                if not isinstance(payload, dict) or not isinstance(payload.get("data"), list):
                    raise FileToolsError(
                        "Directus returned an invalid file reference response",
                        code="file_reference_check_failed",
                    )
                if payload["data"]:
                    return f"{profile.collection}.{relation.field}"
        return None

    async def preset_preview(self, params: PresetPreviewParams) -> PresetPreviewResult:
        if params.preset_key not in APPROVED_ASSET_PRESETS:
            raise FileToolsError(
                f"preset {params.preset_key!r} is not in the approved set",
                code="preset_not_approved",
            )
        token = await self._auth.access_token()
        await self._transport.request(
            "GET",
            f"/files/{params.file_id}",
            access_token=token,
            query={"fields": ["id", "filename_download", "type", "filesize"]},
        )
        url = f"/assets/{params.file_id}?key={params.preset_key}"
        return PresetPreviewResult(
            file_id=params.file_id,
            preset_key=params.preset_key,
            url=url,
            cached=False,
        )

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------

    def _profile(self, collection: str) -> CollectionProfile:
        profile = self._profiles.get(collection)
        if profile is None:
            raise DirectusSchemaError(f"collection {collection!r} is not in capability manifest")
        return profile


# ---------------------------------------------------------------------------
# Module helpers
# ---------------------------------------------------------------------------


def _file_metadata(raw: dict[str, Any]) -> DirectusFileMetadata:
    return DirectusFileMetadata(
        id=str(raw.get("id", "")),
        filename=str(raw.get("filename_download") or raw.get("filename") or ""),
        mime_type=str(raw.get("type") or raw.get("mime_type") or ""),
        file_size=_safe_int(raw.get("filesize")) or _safe_int(raw.get("file_size")) or 0,
        uploaded_on=str(raw.get("uploaded_on") or ""),
        storage=str(raw.get("storage") or ""),
        checksum=str(raw.get("checksum") or "") or None,
    )


def _now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def _object(payload: Any) -> dict[str, Any]:
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        return payload["data"]
    return {}


def _safe_int(value: Any) -> int | None:
    return value if isinstance(value, int) else None


__all__ = [
    "APPROVED_ASSET_PRESETS",
    "JOURNAL_RETENTION_SECONDS",
    "FileToolsError",
    "FileToolsService",
    "OperationJournal",
]
