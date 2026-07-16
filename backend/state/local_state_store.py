"""B3 Task 3: durable local user-state store for grid state.

The store is a small SQLite database at ``%LOCALAPPDATA%/VibeTable/state/vibetable-state.db``
(overridable for tests). It holds one state row per (database identity, table,
user profile) with a JSON payload, a revision token and an updated timestamp.

Design invariants (per the B3 plan):

* It never opens Directus business data. Grid state is device-local only.
* Migration is idempotent: running ``ensure_schema()`` twice is a no-op.
* Revision tokens are opaque monotonic strings so the host can detect
  conflicts (stale revision on save).
"""

from __future__ import annotations

import json
import os
import sqlite3
import threading
import uuid
from pathlib import Path
from typing import Any, Final

#: The state database directory name under LOCALAPPDATA.
STATE_DIR_NAME: Final[str] = "VibeTable"
STATE_SUBDIR: Final[str] = "state"
STATE_DB_NAME: Final[str] = "vibetable-state.db"

#: Schema version of the local state database.
_STATE_SCHEMA_VERSION: Final[int] = 1


def default_state_db_path() -> Path:
    """Return the default state DB path under ``%LOCALAPPDATA%``.

    Falls back to a ``.vibetable`` directory in the user home when LOCALAPPDATA is
    unset (non-Windows or headless test environments).
    """
    local_app_data = os.environ.get("LOCALAPPDATA")
    if local_app_data:
        base = Path(local_app_data) / STATE_DIR_NAME / STATE_SUBDIR
    else:
        base = Path.home() / ".vibetable" / STATE_SUBDIR
    base.mkdir(parents=True, exist_ok=True)
    return base / STATE_DB_NAME


class LocalStateStore:
    """SQLite-backed device-local grid-state store.

    Thread-safe via a single connection guarded by a lock (the store is
    low-throughput: a few saves per table switch, debounced on the host).
    """

    def __init__(self, db_path: Path | None = None) -> None:
        self._db_path = db_path or default_state_db_path()
        self._lock = threading.Lock()
        self._conn = self._open(self._db_path)
        self.ensure_schema()

    @staticmethod
    def _open(path: Path) -> sqlite3.Connection:
        conn = sqlite3.connect(str(path), check_same_thread=False)
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute("PRAGMA busy_timeout=5000")
        return conn

    def ensure_schema(self) -> None:
        """Create the state tables if absent. Idempotent."""
        with self._lock:
            self._conn.executescript(
                """
                CREATE TABLE IF NOT EXISTS schema_meta (
                    key TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS grid_state (
                    database_id TEXT NOT NULL,
                    table_name TEXT NOT NULL,
                    profile TEXT NOT NULL DEFAULT 'default',
                    payload TEXT NOT NULL,
                    revision TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    PRIMARY KEY (database_id, table_name, profile)
                );
                """
            )
            # Record the schema version once.
            self._conn.execute(
                "INSERT OR IGNORE INTO schema_meta(key, value) VALUES (?, ?)",
                ("schema_version", str(_STATE_SCHEMA_VERSION)),
            )
            self._conn.commit()

    def get_grid_state(
        self, *, database_id: str, table: str, profile: str = "default"
    ) -> tuple[dict[str, Any] | None, str | None]:
        """Return ``(payload_dict, revision)`` or ``(None, None)`` if absent."""
        with self._lock:
            row = self._conn.execute(
                "SELECT payload, revision FROM grid_state "
                "WHERE database_id = ? AND table_name = ? AND profile = ?",
                (database_id, table, profile),
            ).fetchone()
        if row is None:
            return None, None
        return json.loads(row["payload"]), row["revision"]

    def save_grid_state(
        self,
        *,
        database_id: str,
        table: str,
        payload: dict[str, Any],
        revision: str | None,
        profile: str = "default",
    ) -> tuple[str, bool]:
        """Upsert grid state with optimistic-concurrency revision check.

        Returns ``(new_revision, conflict)``. When ``revision`` does not match
        the stored value (or a row exists and ``revision`` is None), the save
        is rejected as a conflict: ``conflict=True`` and the caller must re-read.
        """
        new_revision = uuid.uuid4().hex[:12]
        payload_json = json.dumps(payload, ensure_ascii=False, sort_keys=False)
        from datetime import datetime

        now = datetime.now().isoformat()
        with self._lock:
            existing = self._conn.execute(
                "SELECT revision FROM grid_state "
                "WHERE database_id = ? AND table_name = ? AND profile = ?",
                (database_id, table, profile),
            ).fetchone()
            if existing is not None:
                stored_rev = existing["revision"]
                if revision != stored_rev:
                    # Stale revision -> conflict; caller must re-read.
                    return stored_rev, True
                # Matching revision -> update in place.
                self._conn.execute(
                    "UPDATE grid_state SET payload = ?, revision = ?, updated_at = ? "
                    "WHERE database_id = ? AND table_name = ? AND profile = ?",
                    (payload_json, new_revision, now, database_id, table, profile),
                )
            else:
                # First save for this key. ``revision`` must be None (no prior).
                if revision is not None:
                    # Caller claims a revision that doesn't exist -> conflict.
                    return new_revision, True
                self._conn.execute(
                    "INSERT INTO grid_state "
                    "(database_id, table_name, profile, payload, revision, updated_at) "
                    "VALUES (?, ?, ?, ?, ?, ?)",
                    (database_id, table, profile, payload_json, new_revision, now),
                )
            self._conn.commit()
        return new_revision, False

    def rename_database_identity(self, *, old_id: str, new_id: str) -> int:
        """Re-key all grid-state rows from ``old_id`` to ``new_id``.

        Used when a database is renamed/moved so its saved state follows it.
        Returns the number of rows re-keyed.
        """
        with self._lock:
            cur = self._conn.execute(
                "UPDATE grid_state SET database_id = ? WHERE database_id = ?",
                (new_id, old_id),
            )
            self._conn.commit()
            return cur.rowcount

    def close(self) -> None:
        with self._lock:
            self._conn.close()


# ---------------------------------------------------------------------------
# Process-wide singleton
# ---------------------------------------------------------------------------

_SINGLETON: LocalStateStore | None = None
_SINGLETON_LOCK = threading.Lock()


def get_local_state_store() -> LocalStateStore:
    """Return the process-wide :class:`LocalStateStore` singleton."""
    global _SINGLETON
    if _SINGLETON is None:
        with _SINGLETON_LOCK:
            if _SINGLETON is None:
                _SINGLETON = LocalStateStore()
    return _SINGLETON


def reset_local_state_store_for_tests(db_path: Path | None = None) -> LocalStateStore:
    """Replace the singleton with a fresh store at ``db_path`` (tests only)."""
    global _SINGLETON
    with _SINGLETON_LOCK:
        if _SINGLETON is not None:
            _SINGLETON.close()
        _SINGLETON = LocalStateStore(db_path=db_path)
    return _SINGLETON
