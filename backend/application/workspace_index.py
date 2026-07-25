"""Device-local, provider-neutral workspace metadata index.

Only metadata is stored here. Workspace files, content-addressed objects,
revision manifests, refs, and restore output remain under the native
workspace's ``.backup`` directory.
"""

from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import threading
from pathlib import Path
from typing import Protocol

from backend.contracts.document_workspace import (
    DocumentHistoryResult,
    DocumentListResult,
    DocumentRevisionEntry,
    DocumentSummary,
    FolderResult,
    LinkDocumentParams,
    LinkResult,
    PublishHeadAdvance,
    PublishIndexBatchParams,
    PublishIndexBatchResult,
    PublishResult,
    RegisterDocumentParams,
    RegisterDocumentResult,
)

_SCHEMA_VERSION = 1
_EXPECTED_SCHEMA: dict[str, tuple[tuple[str, str, int, int], ...]] = {
    "workspace_schema_meta": (
        ("key", "TEXT", 0, 1),
        ("value", "TEXT", 1, 0),
    ),
    "workspace_documents": (
        ("document_id", "TEXT", 0, 1),
        ("workspace_id", "TEXT", 1, 0),
        ("workspace_name", "TEXT", 1, 0),
        ("file_name", "TEXT", 1, 0),
        ("mime_type", "TEXT", 1, 0),
        ("main_head", "TEXT", 0, 0),
        ("main_hash", "TEXT", 0, 0),
        ("status", "TEXT", 1, 0),
        ("folder_relative_path", "TEXT", 0, 0),
    ),
    "workspace_revisions": (
        ("revision_id", "TEXT", 0, 1),
        ("document_id", "TEXT", 1, 0),
        ("scheme_id", "TEXT", 1, 0),
        ("parent_revision_id", "TEXT", 0, 0),
        ("sequence", "INTEGER", 1, 0),
        ("version_label", "TEXT", 1, 0),
        ("kind", "TEXT", 1, 0),
        ("hash", "TEXT", 1, 0),
        ("size", "INTEGER", 1, 0),
        ("mime_type", "TEXT", 1, 0),
        ("created_by", "TEXT", 0, 0),
        ("device_id", "TEXT", 0, 0),
        ("comment", "TEXT", 0, 0),
        ("created_at", "TEXT", 1, 0),
    ),
    "workspace_links": (
        ("link_id", "TEXT", 0, 1),
        ("document_id", "TEXT", 1, 0),
        ("item_collection", "TEXT", 1, 0),
        ("item_id", "TEXT", 1, 0),
        ("link_type", "TEXT", 1, 0),
        ("status", "TEXT", 1, 0),
    ),
    "workspace_idempotency": (
        ("idempotency_key", "TEXT", 0, 1),
        ("request_hash", "TEXT", 1, 0),
        ("receipt_json", "TEXT", 1, 0),
    ),
}
_EXPECTED_DEFAULTS: dict[str, dict[str, str]] = {
    table: ({"created_at": "''"} if table == "workspace_revisions" else {})
    for table in _EXPECTED_SCHEMA
}
_EXPECTED_INDEXES: dict[str, tuple[str, int, tuple[tuple[str, int], ...]]] = {
    "ix_workspace_revisions_document_sequence": (
        "workspace_revisions",
        0,
        (("document_id", 0), ("sequence", 1)),
    ),
    "ix_workspace_links_item": (
        "workspace_links",
        0,
        (("item_collection", 0), ("item_id", 0), ("status", 0)),
    ),
}
_EXPECTED_UNIQUE_CONSTRAINTS: dict[str, set[tuple[str, ...]]] = {
    table: (
        {("document_id", "item_collection", "item_id", "link_type")}
        if table == "workspace_links"
        else set()
    )
    for table in _EXPECTED_SCHEMA
}


class WorkspaceIndexError(Exception):
    """Metadata-index failure with a stable product error code."""

    def __init__(self, message: str, *, code: str) -> None:
        super().__init__(message)
        self.code = code


class WorkspaceIndexPort(Protocol):
    """Persistence seam consumed by :class:`DocumentWorkspaceService`."""

    def read_folder(self, collection: str, item_id: str) -> FolderResult: ...

    def read_documents(self, *, limit: int, offset: int) -> DocumentListResult: ...

    def register_document(self, params: RegisterDocumentParams) -> RegisterDocumentResult: ...

    def publish_revisions(self, params: PublishIndexBatchParams) -> PublishIndexBatchResult: ...

    def link_document(self, params: LinkDocumentParams) -> LinkResult: ...

    def unlink_document(self, link_id: str) -> None: ...

    def read_history(
        self, document_id: str, *, limit: int, offset: int
    ) -> DocumentHistoryResult: ...

    def close(self) -> None: ...


def default_workspace_index_path() -> Path:
    """Return a device-state path separate from workspace and backend data."""

    base = Path(os.environ.get("LOCALAPPDATA") or (Path.home() / ".vibetable"))
    state_dir = base / "VibeTable" / "state"
    state_dir.mkdir(parents=True, exist_ok=True)
    return state_dir / "workspace-index.db"


class SqliteWorkspaceIndex:
    """Durable metadata-only implementation of :class:`WorkspaceIndexPort`."""

    def __init__(self, db_path: Path | None = None) -> None:
        self._db_path = (db_path or default_workspace_index_path()).resolve()
        if any(part.casefold() == "pb_data" for part in self._db_path.parts):
            raise ValueError("workspace index must not be stored in pb_data")
        self._db_path.parent.mkdir(parents=True, exist_ok=True)
        self._lock = threading.RLock()
        self._conn = sqlite3.connect(str(self._db_path), check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute("PRAGMA busy_timeout=5000")
        schema_exists = self._reject_incompatible_schema()
        if not schema_exists:
            self._ensure_schema()
        self._validate_schema()

    @property
    def db_path(self) -> Path:
        return self._db_path

    def _ensure_schema(self) -> None:
        with self._lock:
            self._conn.executescript(
                """
                CREATE TABLE IF NOT EXISTS workspace_schema_meta (
                    key TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS workspace_documents (
                    document_id TEXT PRIMARY KEY,
                    workspace_id TEXT NOT NULL,
                    workspace_name TEXT NOT NULL,
                    file_name TEXT NOT NULL,
                    mime_type TEXT NOT NULL,
                    main_head TEXT,
                    main_hash TEXT,
                    status TEXT NOT NULL,
                    folder_relative_path TEXT
                );
                CREATE TABLE IF NOT EXISTS workspace_revisions (
                    revision_id TEXT PRIMARY KEY,
                    document_id TEXT NOT NULL,
                    scheme_id TEXT NOT NULL,
                    parent_revision_id TEXT,
                    sequence INTEGER NOT NULL,
                    version_label TEXT NOT NULL,
                    kind TEXT NOT NULL,
                    hash TEXT NOT NULL,
                    size INTEGER NOT NULL,
                    mime_type TEXT NOT NULL,
                    created_by TEXT,
                    device_id TEXT,
                    comment TEXT,
                    created_at TEXT NOT NULL DEFAULT ''
                );
                CREATE INDEX IF NOT EXISTS ix_workspace_revisions_document_sequence
                    ON workspace_revisions(document_id, sequence DESC);
                CREATE TABLE IF NOT EXISTS workspace_links (
                    link_id TEXT PRIMARY KEY,
                    document_id TEXT NOT NULL,
                    item_collection TEXT NOT NULL,
                    item_id TEXT NOT NULL,
                    link_type TEXT NOT NULL,
                    status TEXT NOT NULL,
                    UNIQUE(document_id, item_collection, item_id, link_type)
                );
                CREATE INDEX IF NOT EXISTS ix_workspace_links_item
                    ON workspace_links(item_collection, item_id, status);
                CREATE TABLE IF NOT EXISTS workspace_idempotency (
                    idempotency_key TEXT PRIMARY KEY,
                    request_hash TEXT NOT NULL,
                    receipt_json TEXT NOT NULL
                );
                """
            )
            self._conn.execute(
                "INSERT OR IGNORE INTO workspace_schema_meta(key, value) VALUES (?, ?)",
                ("schema_version", str(_SCHEMA_VERSION)),
            )
            self._conn.commit()

    def _reject_incompatible_schema(self) -> bool:
        existing = self._conn.execute(
            """
            SELECT 1 FROM sqlite_master
            WHERE type = 'table' AND name = 'workspace_schema_meta'
            """
        ).fetchone()
        if existing is None:
            partial = self._conn.execute(
                """
                SELECT name FROM sqlite_master
                WHERE type = 'table' AND name LIKE 'workspace_%'
                LIMIT 1
                """
            ).fetchone()
            if partial is not None:
                self._conn.close()
                raise WorkspaceIndexError(
                    "workspace index contains unversioned or partially initialized tables",
                    code="workspace.schema_incomplete",
                )
            return False
        version = self._conn.execute(
            """
            SELECT value FROM workspace_schema_meta
            WHERE key = 'schema_version'
            """
        ).fetchone()
        if version is None or version["value"] != str(_SCHEMA_VERSION):
            actual = None if version is None else version["value"]
            self._conn.close()
            raise WorkspaceIndexError(
                f"workspace index schema version {actual!r} is not supported",
                code="workspace.schema_version_unsupported",
            )
        return True

    def _validate_schema(self) -> None:
        try:
            for table, expected_columns in _EXPECTED_SCHEMA.items():
                rows = self._conn.execute(f'PRAGMA table_info("{table}")').fetchall()
                actual_columns = tuple(
                    (
                        str(row["name"]),
                        str(row["type"]).upper(),
                        int(row["notnull"]),
                        int(row["pk"]),
                    )
                    for row in rows
                )
                if actual_columns != expected_columns:
                    raise WorkspaceIndexError(
                        f"workspace index table {table!r} has an invalid structure",
                        code="workspace.schema_invalid",
                    )
                actual_defaults = {
                    str(row["name"]): str(row["dflt_value"])
                    for row in rows
                    if row["dflt_value"] is not None
                }
                if actual_defaults != _EXPECTED_DEFAULTS[table]:
                    raise WorkspaceIndexError(
                        f"workspace index table {table!r} has invalid defaults",
                        code="workspace.schema_invalid",
                    )

            indexes = {
                str(row["name"]): str(row["tbl_name"])
                for row in self._conn.execute(
                    """
                    SELECT name, tbl_name FROM sqlite_master
                    WHERE type = 'index' AND name LIKE 'ix_workspace_%'
                    """
                ).fetchall()
            }
            if set(indexes) != set(_EXPECTED_INDEXES):
                raise WorkspaceIndexError(
                    "workspace index has an invalid index structure",
                    code="workspace.schema_invalid",
                )
            for index_name, (
                index_table,
                expected_unique,
                expected_index_columns,
            ) in _EXPECTED_INDEXES.items():
                index_rows = self._conn.execute(f'PRAGMA index_list("{index_table}")').fetchall()
                index = next(
                    (row for row in index_rows if row["name"] == index_name),
                    None,
                )
                index_columns = tuple(
                    (str(row["name"]), int(row["desc"]))
                    for row in self._conn.execute(f'PRAGMA index_xinfo("{index_name}")').fetchall()
                    if int(row["key"]) == 1
                )
                if (
                    indexes[index_name] != index_table
                    or index is None
                    or int(index["unique"]) != expected_unique
                    or str(index["origin"]) != "c"
                    or index_columns != expected_index_columns
                ):
                    raise WorkspaceIndexError(
                        f"workspace index {index_name!r} has an invalid structure",
                        code="workspace.schema_invalid",
                    )

            for table, expected_constraints in _EXPECTED_UNIQUE_CONSTRAINTS.items():
                actual_constraints = {
                    tuple(
                        str(column["name"])
                        for column in self._conn.execute(
                            f'PRAGMA index_info("{row["name"]}")'
                        ).fetchall()
                    )
                    for row in self._conn.execute(f'PRAGMA index_list("{table}")').fetchall()
                    if str(row["origin"]) == "u" and int(row["unique"]) == 1
                }
                if actual_constraints != expected_constraints:
                    raise WorkspaceIndexError(
                        f"workspace index table {table!r} has invalid constraints",
                        code="workspace.schema_invalid",
                    )
        except WorkspaceIndexError:
            self._conn.close()
            raise

    def read_folder(self, collection: str, item_id: str) -> FolderResult:
        with self._lock:
            rows = self._conn.execute(
                """
                SELECT l.link_id, l.link_type, d.*
                FROM workspace_links l
                JOIN workspace_documents d ON d.document_id = l.document_id
                WHERE l.item_collection = ? AND l.item_id = ?
                  AND l.status = 'active' AND d.status = 'active'
                ORDER BY d.file_name, d.document_id
                """,
                (collection, item_id),
            ).fetchall()
        return FolderResult(
            collection=collection,
            item_id=item_id,
            folder_id=None,
            documents=[
                self._document_summary(
                    row,
                    link_id=row["link_id"],
                    link_type=row["link_type"],
                )
                for row in rows
            ],
        )

    def read_documents(self, *, limit: int, offset: int) -> DocumentListResult:
        with self._lock:
            total = int(
                self._conn.execute(
                    "SELECT COUNT(*) FROM workspace_documents WHERE status = 'active'"
                ).fetchone()[0]
            )
            rows = self._conn.execute(
                """
                SELECT * FROM workspace_documents
                WHERE status = 'active'
                ORDER BY file_name, document_id
                LIMIT ? OFFSET ?
                """,
                (limit, offset),
            ).fetchall()
        return DocumentListResult(
            documents=[self._document_summary(row) for row in rows],
            total=total,
        )

    def register_document(self, params: RegisterDocumentParams) -> RegisterDocumentResult:
        with self._lock, self._conn:
            existing = self._conn.execute(
                "SELECT * FROM workspace_documents WHERE document_id = ?",
                (params.document_id,),
            ).fetchone()
            status = "unchanged"
            if existing is None:
                self._conn.execute(
                    """
                    INSERT INTO workspace_documents(
                        document_id, workspace_id, workspace_name, file_name,
                        mime_type, main_head, main_hash, status, folder_relative_path
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, 'active', NULL)
                    """,
                    (
                        params.document_id,
                        params.workspace_id,
                        params.workspace_name,
                        params.file_name,
                        params.mime_type,
                        params.revision_id,
                        params.hash,
                    ),
                )
                status = "created"
            elif (
                existing["workspace_id"] != params.workspace_id
                or existing["file_name"] != params.file_name
            ):
                raise WorkspaceIndexError(
                    f"document {params.document_id!r} has conflicting metadata",
                    code="document_identity_conflict",
                )

            self._insert_initial_revision(params)
            link_id = None
            if params.item_collection is not None and params.item_id is not None:
                link = self.link_document(
                    LinkDocumentParams(
                        document_id=params.document_id,
                        item_collection=params.item_collection,
                        item_id=params.item_id,
                        link_type=params.link_type,
                    )
                )
                link_id = link.link_id
            return RegisterDocumentResult(
                document_id=params.document_id,
                status=status,
                link_id=link_id,
            )

    def _insert_initial_revision(self, params: RegisterDocumentParams) -> None:
        existing = self._conn.execute(
            "SELECT * FROM workspace_revisions WHERE revision_id = ?",
            (params.revision_id,),
        ).fetchone()
        if existing is not None:
            if not self._revision_matches(
                existing,
                document_id=params.document_id,
                scheme_id=params.scheme_id,
                parent_revision_id=None,
                sequence=1,
                version_label="",
                kind="formal",
                content_hash=params.hash,
                size=params.size,
                mime_type=params.mime_type,
                created_by=None,
                device_id=None,
                comment=None,
                created_at=params.created_at,
            ):
                raise WorkspaceIndexError(
                    f"revision {params.revision_id!r} has conflicting identity or content",
                    code="revision_immutable_conflict",
                )
            return
        self._conn.execute(
            """
            INSERT INTO workspace_revisions(
                revision_id, document_id, scheme_id, parent_revision_id,
                sequence, version_label, kind, hash, size, mime_type,
                created_by, device_id, comment, created_at
            ) VALUES (?, ?, ?, NULL, 1, '', 'formal', ?, ?, ?, NULL, NULL, NULL, ?)
            """,
            (
                params.revision_id,
                params.document_id,
                params.scheme_id,
                params.hash,
                params.size,
                params.mime_type,
                params.created_at,
            ),
        )

    def publish_revisions(self, params: PublishIndexBatchParams) -> PublishIndexBatchResult:
        canonical = json.dumps(
            params.model_dump(mode="json", by_alias=True),
            ensure_ascii=False,
            separators=(",", ":"),
            sort_keys=True,
        )
        request_hash = hashlib.sha256(canonical.encode("utf-8")).hexdigest()
        with self._lock, self._conn:
            prior = self._conn.execute(
                """
                SELECT request_hash, receipt_json FROM workspace_idempotency
                WHERE idempotency_key = ?
                """,
                (params.idempotency_key,),
            ).fetchone()
            if prior is not None:
                if prior["request_hash"] != request_hash:
                    raise WorkspaceIndexError(
                        "idempotency key was reused with a different revision batch",
                        code="workspace.idempotency_conflict",
                    )
                return PublishIndexBatchResult.model_validate_json(prior["receipt_json"])

            results: list[PublishResult] = []
            conflicts: list[str] = []
            for revision in params.revisions:
                document = self._conn.execute(
                    "SELECT document_id FROM workspace_documents WHERE document_id = ?",
                    (revision.document_id,),
                ).fetchone()
                if document is None:
                    raise WorkspaceIndexError(
                        f"document {revision.document_id!r} is not registered",
                        code="document_not_found",
                    )
                existing = self._conn.execute(
                    "SELECT * FROM workspace_revisions WHERE revision_id = ?",
                    (revision.revision_id,),
                ).fetchone()
                if existing is not None:
                    result_status = (
                        "unchanged"
                        if self._revision_matches(
                            existing,
                            document_id=revision.document_id,
                            scheme_id=revision.scheme_id,
                            parent_revision_id=revision.parent_revision_id,
                            sequence=revision.sequence,
                            version_label=revision.version_label,
                            kind=revision.kind,
                            content_hash=revision.hash,
                            size=revision.size,
                            mime_type=revision.mime_type,
                            created_by=revision.created_by,
                            device_id=revision.device_id,
                            comment=revision.comment,
                            created_at=revision.created_at,
                        )
                        else "conflict"
                    )
                else:
                    self._conn.execute(
                        """
                        INSERT INTO workspace_revisions(
                            revision_id, document_id, scheme_id, parent_revision_id,
                            sequence, version_label, kind, hash, size, mime_type,
                            created_by, device_id, comment, created_at
                        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                        """,
                        (
                            revision.revision_id,
                            revision.document_id,
                            revision.scheme_id,
                            revision.parent_revision_id,
                            revision.sequence,
                            revision.version_label,
                            revision.kind,
                            revision.hash,
                            revision.size,
                            revision.mime_type,
                            revision.created_by,
                            revision.device_id,
                            revision.comment,
                            revision.created_at,
                        ),
                    )
                    result_status = "created"
                results.append(
                    PublishResult(
                        revision_id=revision.revision_id,
                        status=result_status,
                    )
                )
                if result_status == "conflict":
                    conflicts.append(revision.revision_id)

            if params.head_advance is not None and not conflicts:
                self._advance_head(params.head_advance)

            receipt = PublishIndexBatchResult(results=results, conflicts=conflicts)
            self._conn.execute(
                """
                INSERT INTO workspace_idempotency(
                    idempotency_key, request_hash, receipt_json
                ) VALUES (?, ?, ?)
                """,
                (
                    params.idempotency_key,
                    request_hash,
                    receipt.model_dump_json(by_alias=True),
                ),
            )
            return receipt

    def _advance_head(self, advance: PublishHeadAdvance) -> None:
        document = self._conn.execute(
            """
            SELECT main_head FROM workspace_documents
            WHERE document_id = ?
            """,
            (advance.document_id,),
        ).fetchone()
        if document is None:
            raise WorkspaceIndexError(
                f"document {advance.document_id!r} is not registered",
                code="document_not_found",
            )
        if document["main_head"] != advance.expected_head_revision_id:
            raise WorkspaceIndexError(
                "workspace document head changed before publication",
                code="workspace.head_conflict",
            )

        current_id: str | None = advance.new_head_revision_id
        if current_id is None:
            raise WorkspaceIndexError(
                "new workspace head is required",
                code="workspace.head_not_descendant",
            )
        current = self._read_revision_for_head(
            current_id,
            advance.document_id,
            advance.scheme_id,
        )
        target = current
        visited: set[str] = set()
        while current_id != advance.expected_head_revision_id:
            if current_id is None or current_id in visited:
                raise WorkspaceIndexError(
                    "new workspace head is not a continuous descendant of the expected head",
                    code="workspace.head_not_descendant",
                )
            visited.add(current_id)
            parent_id = current["parent_revision_id"]
            if parent_id is None:
                if advance.expected_head_revision_id is not None:
                    raise WorkspaceIndexError(
                        "new workspace head is not a continuous descendant of the expected head",
                        code="workspace.head_not_descendant",
                    )
                if current["sequence"] != 1:
                    raise WorkspaceIndexError(
                        "workspace revision ancestry has a non-contiguous sequence",
                        code="workspace.head_not_descendant",
                    )
                current_id = None
                continue

            parent = self._read_revision_for_head(
                parent_id,
                advance.document_id,
                advance.scheme_id,
            )
            if parent["sequence"] + 1 != current["sequence"]:
                raise WorkspaceIndexError(
                    "workspace revision ancestry has a non-contiguous sequence",
                    code="workspace.head_not_descendant",
                )
            current_id = parent_id
            current = parent

        cursor = self._conn.execute(
            """
            UPDATE workspace_documents
            SET main_head = ?, main_hash = ?
            WHERE document_id = ? AND main_head IS ?
            """,
            (
                advance.new_head_revision_id,
                target["hash"],
                advance.document_id,
                advance.expected_head_revision_id,
            ),
        )
        if cursor.rowcount != 1:
            raise WorkspaceIndexError(
                "workspace document head changed before publication",
                code="workspace.head_conflict",
            )

    def _read_revision_for_head(
        self,
        revision_id: str,
        document_id: str,
        scheme_id: str,
    ) -> sqlite3.Row:
        revision = self._conn.execute(
            "SELECT * FROM workspace_revisions WHERE revision_id = ?",
            (revision_id,),
        ).fetchone()
        if (
            revision is None
            or revision["document_id"] != document_id
            or revision["scheme_id"] != scheme_id
        ):
            raise WorkspaceIndexError(
                "workspace head references a missing or unrelated revision",
                code="workspace.head_not_descendant",
            )
        return revision

    def link_document(self, params: LinkDocumentParams) -> LinkResult:
        link_id = self._link_id(params)
        with self._lock, self._conn:
            document = self._conn.execute(
                "SELECT document_id FROM workspace_documents WHERE document_id = ?",
                (params.document_id,),
            ).fetchone()
            if document is None:
                raise WorkspaceIndexError(
                    f"document {params.document_id!r} is not registered",
                    code="document_not_found",
                )
            existing = self._conn.execute(
                """
                SELECT link_id, status FROM workspace_links
                WHERE document_id = ? AND item_collection = ?
                  AND item_id = ? AND link_type = ?
                """,
                (
                    params.document_id,
                    params.item_collection,
                    params.item_id,
                    params.link_type,
                ),
            ).fetchone()
            if existing is not None:
                if existing["status"] != "active":
                    self._conn.execute(
                        "UPDATE workspace_links SET status = 'active' WHERE link_id = ?",
                        (existing["link_id"],),
                    )
                    return LinkResult(link_id=existing["link_id"], status="restored")
                return LinkResult(link_id=existing["link_id"], status="unchanged")
            self._conn.execute(
                """
                INSERT INTO workspace_links(
                    link_id, document_id, item_collection, item_id, link_type, status
                ) VALUES (?, ?, ?, ?, ?, 'active')
                """,
                (
                    link_id,
                    params.document_id,
                    params.item_collection,
                    params.item_id,
                    params.link_type,
                ),
            )
        return LinkResult(link_id=link_id, status="created")

    def unlink_document(self, link_id: str) -> None:
        with self._lock, self._conn:
            cursor = self._conn.execute(
                "UPDATE workspace_links SET status = 'deleted' WHERE link_id = ?",
                (link_id,),
            )
            if cursor.rowcount == 0:
                raise WorkspaceIndexError(
                    f"link {link_id!r} does not exist",
                    code="document_link_not_found",
                )

    def read_history(self, document_id: str, *, limit: int, offset: int) -> DocumentHistoryResult:
        with self._lock:
            total = int(
                self._conn.execute(
                    "SELECT COUNT(*) FROM workspace_revisions WHERE document_id = ?",
                    (document_id,),
                ).fetchone()[0]
            )
            rows = self._conn.execute(
                """
                SELECT * FROM workspace_revisions
                WHERE document_id = ?
                ORDER BY sequence DESC, revision_id DESC
                LIMIT ? OFFSET ?
                """,
                (document_id, limit, offset),
            ).fetchall()
        return DocumentHistoryResult(
            document_id=document_id,
            revisions=[
                DocumentRevisionEntry(
                    revision_id=row["revision_id"],
                    scheme_name=row["scheme_id"],
                    sequence=row["sequence"],
                    version_label=row["version_label"],
                    kind=row["kind"],
                    hash=row["hash"],
                    size=row["size"],
                    created_at=row["created_at"],
                    created_by=row["created_by"],
                )
                for row in rows
            ],
            total=total,
        )

    def close(self) -> None:
        with self._lock:
            self._conn.close()

    @staticmethod
    def _revision_matches(
        row: sqlite3.Row,
        *,
        document_id: str,
        scheme_id: str,
        parent_revision_id: str | None,
        sequence: int,
        version_label: str,
        kind: str,
        content_hash: str,
        size: int,
        mime_type: str,
        created_by: str | None,
        device_id: str | None,
        comment: str | None,
        created_at: str,
    ) -> bool:
        return (
            row["document_id"] == document_id
            and row["scheme_id"] == scheme_id
            and row["parent_revision_id"] == parent_revision_id
            and row["sequence"] == sequence
            and row["version_label"] == version_label
            and row["kind"] == kind
            and row["hash"] == content_hash
            and row["size"] == size
            and row["mime_type"] == mime_type
            and row["created_by"] == created_by
            and row["device_id"] == device_id
            and row["comment"] == comment
            and row["created_at"] == created_at
        )

    @staticmethod
    def _link_id(params: LinkDocumentParams) -> str:
        raw = "\0".join(
            [
                params.document_id,
                params.item_collection,
                params.item_id,
                params.link_type,
            ]
        )
        return "link_" + hashlib.sha256(raw.encode("utf-8")).hexdigest()[:24]

    @staticmethod
    def _document_summary(
        row: sqlite3.Row,
        *,
        link_id: str | None = None,
        link_type: str | None = None,
    ) -> DocumentSummary:
        return DocumentSummary(
            link_id=link_id,
            document_id=row["document_id"],
            workspace_id=row["workspace_id"],
            file_name=row["file_name"],
            mime_type=row["mime_type"] or None,
            main_head=row["main_head"],
            main_hash=row["main_hash"],
            status=row["status"],
            link_type=link_type,
            folder_relative_path=row["folder_relative_path"],
        )


__all__ = [
    "SqliteWorkspaceIndex",
    "WorkspaceIndexError",
    "WorkspaceIndexPort",
    "default_workspace_index_path",
]
