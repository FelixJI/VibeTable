"""Project-scoped persistence ports and in-memory adapter for plugins."""

from __future__ import annotations

import json
import sqlite3
import threading
from pathlib import Path

from backend.contracts.plugin import (
    FlowBindingSnapshot,
    PluginAuditEvent,
    PluginManifest,
    PluginPackageRevision,
    PluginPrivateSetting,
    PluginSnapshot,
)


class PluginStoreConflictError(Exception):
    """Raised when a project-local optimistic revision is stale."""


class InMemoryPluginStore:
    """Deterministic store adapter used by public module tests."""

    def __init__(self) -> None:
        self._installations: dict[tuple[str, str], PluginSnapshot] = {}
        self._bindings: dict[tuple[str, str, str], FlowBindingSnapshot] = {}
        self._package_revisions: dict[tuple[str, str, str, str], PluginPackageRevision] = {}
        self._private_settings: dict[tuple[str, str, str], PluginPrivateSetting] = {}
        self._audit: list[PluginAuditEvent] = []

    def get_installation(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        return self._installations.get((project_key, plugin_id))

    def save_installation(
        self, snapshot: PluginSnapshot, *, expected_revision: int | None = None
    ) -> PluginSnapshot:
        key = (snapshot.project_key, snapshot.plugin_id)
        existing = self._installations.get(key)
        if existing is None:
            if expected_revision is not None:
                raise PluginStoreConflictError("installation revision is stale")
        elif expected_revision != existing.revision:
            raise PluginStoreConflictError("installation revision is stale")
        self._installations[key] = snapshot
        return snapshot

    def list_installations(self, project_key: str) -> list[PluginSnapshot]:
        return [
            snapshot
            for (stored_project, _), snapshot in self._installations.items()
            if stored_project == project_key
        ]

    def save_binding(
        self, binding: FlowBindingSnapshot, *, expected_revision: int | None = None
    ) -> FlowBindingSnapshot:
        key = (binding.project_key, binding.plugin_id, binding.logical_flow_id)
        existing = self._bindings.get(key)
        if existing is None:
            if expected_revision is not None:
                raise PluginStoreConflictError("Flow binding revision is stale")
        elif expected_revision != existing.revision:
            raise PluginStoreConflictError("Flow binding revision is stale")
        self._bindings[key] = binding
        return binding

    def get_binding(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> FlowBindingSnapshot | None:
        return self._bindings.get((project_key, plugin_id, logical_flow_id))

    def list_bindings(self, project_key: str, plugin_id: str) -> list[FlowBindingSnapshot]:
        return [
            binding
            for (stored_project, stored_plugin, _), binding in self._bindings.items()
            if stored_project == project_key and stored_plugin == plugin_id
        ]

    def delete_bindings(self, project_key: str, plugin_id: str) -> int:
        keys = [key for key in self._bindings if key[0] == project_key and key[1] == plugin_id]
        for key in keys:
            del self._bindings[key]
        return len(keys)

    def delete_binding(self, project_key: str, plugin_id: str, logical_flow_id: str) -> bool:
        return self._bindings.pop((project_key, plugin_id, logical_flow_id), None) is not None

    def delete_installation(self, project_key: str, plugin_id: str) -> bool:
        return self._installations.pop((project_key, plugin_id), None) is not None

    def save_package_revision(self, revision: PluginPackageRevision) -> PluginPackageRevision:
        key = (
            revision.project_key,
            revision.plugin_id,
            revision.version,
            revision.package_hash,
        )
        self._package_revisions[key] = revision
        return revision

    def list_package_revisions(
        self, project_key: str, plugin_id: str
    ) -> list[PluginPackageRevision]:
        return [
            item
            for (stored_project, stored_plugin, _, _), item in self._package_revisions.items()
            if stored_project == project_key and stored_plugin == plugin_id
        ]

    def delete_package_revision(
        self, project_key: str, plugin_id: str, version: str, package_hash: str
    ) -> bool:
        return (
            self._package_revisions.pop((project_key, plugin_id, version, package_hash), None)
            is not None
        )

    def delete_package_revisions(self, project_key: str, plugin_id: str) -> int:
        keys = [
            key for key in self._package_revisions if key[0] == project_key and key[1] == plugin_id
        ]
        for key in keys:
            del self._package_revisions[key]
        return len(keys)

    def is_package_path_referenced(self, local_path: str) -> bool:
        return any(item.local_path == local_path for item in self._package_revisions.values())

    def save_private_setting(
        self,
        setting: PluginPrivateSetting,
        *,
        expected_revision: int | None = None,
    ) -> PluginPrivateSetting:
        key = (setting.project_key, setting.plugin_id, setting.setting_key)
        current = self._private_settings.get(key)
        if current is None:
            if expected_revision is not None:
                raise PluginStoreConflictError("private setting revision is stale")
        elif expected_revision != current.revision:
            raise PluginStoreConflictError("private setting revision is stale")
        self._private_settings[key] = setting
        return setting

    def get_private_setting(
        self, project_key: str, plugin_id: str, setting_key: str
    ) -> PluginPrivateSetting | None:
        return self._private_settings.get((project_key, plugin_id, setting_key))

    def delete_private_settings(self, project_key: str, plugin_id: str) -> int:
        keys = [
            key for key in self._private_settings if key[0] == project_key and key[1] == plugin_id
        ]
        for key in keys:
            del self._private_settings[key]
        return len(keys)

    def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
        self._audit.append(event)
        return event

    def list_audit(self, project_key: str, plugin_id: str) -> list[PluginAuditEvent]:
        return [
            event
            for event in self._audit
            if event.project_key == project_key and event.plugin_id == plugin_id
        ]

    def list_project_audit(self, project_key: str) -> list[PluginAuditEvent]:
        return [event for event in self._audit if event.project_key == project_key]


class PluginProjectStore:
    """SQLite-backed project plugin store with explicit optimistic revisions."""

    def __init__(self, db_path: Path) -> None:
        self._lock = threading.Lock()
        self._conn = sqlite3.connect(str(db_path), check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute("PRAGMA busy_timeout=5000")
        self._ensure_schema()

    def _ensure_schema(self) -> None:
        with self._lock:
            self._conn.executescript(
                """
                CREATE TABLE IF NOT EXISTS plugin_installations (
                    project_key TEXT NOT NULL,
                    plugin_id TEXT NOT NULL,
                    version TEXT NOT NULL,
                    status TEXT NOT NULL,
                    disabled_reason TEXT,
                    package_hash TEXT NOT NULL,
                    source_type TEXT NOT NULL,
                    source_location TEXT NOT NULL,
                    manifest_json TEXT NOT NULL,
                    permission_grant_json TEXT NOT NULL DEFAULT '{}',
                    config_revision INTEGER NOT NULL DEFAULT 1,
                    revision INTEGER NOT NULL,
                    installed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    last_error TEXT,
                    snapshot_json TEXT NOT NULL,
                    PRIMARY KEY (project_key, plugin_id)
                );
                CREATE TABLE IF NOT EXISTS plugin_package_revisions (
                    project_key TEXT NOT NULL,
                    plugin_id TEXT NOT NULL,
                    version TEXT NOT NULL,
                    package_hash TEXT NOT NULL,
                    local_path TEXT NOT NULL,
                    manifest_json TEXT NOT NULL,
                    flow_bindings_json TEXT NOT NULL DEFAULT '[]',
                    state TEXT NOT NULL,
                    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    PRIMARY KEY (project_key, plugin_id, version, package_hash)
                );
                CREATE TABLE IF NOT EXISTS plugin_flow_bindings (
                    project_key TEXT NOT NULL,
                    plugin_id TEXT NOT NULL,
                    logical_flow_id TEXT NOT NULL,
                    ownership TEXT NOT NULL,
                    directus_flow_uuid TEXT NOT NULL,
                    trigger_type TEXT NOT NULL,
                    contract_version TEXT NOT NULL,
                    installed_definition_hash TEXT,
                    observed_definition_hash TEXT NOT NULL,
                    revision INTEGER NOT NULL,
                    health TEXT NOT NULL,
                    drift_status TEXT NOT NULL,
                    last_error TEXT,
                    snapshot_json TEXT NOT NULL,
                    PRIMARY KEY (project_key, plugin_id, logical_flow_id)
                );
                CREATE TABLE IF NOT EXISTS plugin_private_settings (
                    project_key TEXT NOT NULL,
                    plugin_id TEXT NOT NULL,
                    setting_key TEXT NOT NULL,
                    value_json TEXT NOT NULL,
                    revision INTEGER NOT NULL,
                    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    PRIMARY KEY (project_key, plugin_id, setting_key)
                );
                CREATE TABLE IF NOT EXISTS plugin_audit_events (
                    event_id TEXT PRIMARY KEY,
                    project_key TEXT NOT NULL,
                    plugin_id TEXT NOT NULL,
                    plugin_version TEXT NOT NULL,
                    package_hash TEXT NOT NULL,
                    event_type TEXT NOT NULL,
                    outcome TEXT NOT NULL,
                    event_json TEXT NOT NULL
                );
                """
            )
            revision_columns = {
                str(row[1])
                for row in self._conn.execute(
                    "PRAGMA table_info(plugin_package_revisions)"
                ).fetchall()
            }
            if "flow_bindings_json" not in revision_columns:
                self._conn.execute(
                    "ALTER TABLE plugin_package_revisions "
                    "ADD COLUMN flow_bindings_json TEXT NOT NULL DEFAULT '[]'"
                )
            self._conn.commit()

    def get_installation(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        with self._lock:
            row = self._conn.execute(
                "SELECT snapshot_json FROM plugin_installations "
                "WHERE project_key = ? AND plugin_id = ?",
                (project_key, plugin_id),
            ).fetchone()
        if row is None:
            return None
        return PluginSnapshot.model_validate_json(row["snapshot_json"])

    def save_installation(
        self, snapshot: PluginSnapshot, *, expected_revision: int | None = None
    ) -> PluginSnapshot:
        snapshot_json = snapshot.model_dump_json(by_alias=True)
        manifest_json = snapshot.manifest.model_dump_json(by_alias=True)
        with self._lock:
            row = self._conn.execute(
                "SELECT revision FROM plugin_installations WHERE project_key = ? AND plugin_id = ?",
                (snapshot.project_key, snapshot.plugin_id),
            ).fetchone()
            if row is None:
                if expected_revision is not None:
                    raise PluginStoreConflictError("installation revision is stale")
                self._conn.execute(
                    """
                    INSERT INTO plugin_installations (
                        project_key, plugin_id, version, status, disabled_reason,
                        package_hash, source_type, source_location, manifest_json,
                        revision, snapshot_json
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        snapshot.project_key,
                        snapshot.plugin_id,
                        snapshot.version,
                        snapshot.status,
                        snapshot.disabled_reason,
                        snapshot.package_hash,
                        snapshot.source_type,
                        snapshot.source_location,
                        manifest_json,
                        snapshot.revision,
                        snapshot_json,
                    ),
                )
            else:
                current_revision = int(row["revision"])
                if expected_revision != current_revision:
                    raise PluginStoreConflictError("installation revision is stale")
                self._conn.execute(
                    """
                    UPDATE plugin_installations SET
                        version = ?, status = ?, disabled_reason = ?,
                        package_hash = ?, source_type = ?, source_location = ?,
                        manifest_json = ?, revision = ?, snapshot_json = ?,
                        updated_at = CURRENT_TIMESTAMP
                    WHERE project_key = ? AND plugin_id = ? AND revision = ?
                    """,
                    (
                        snapshot.version,
                        snapshot.status,
                        snapshot.disabled_reason,
                        snapshot.package_hash,
                        snapshot.source_type,
                        snapshot.source_location,
                        manifest_json,
                        snapshot.revision,
                        snapshot_json,
                        snapshot.project_key,
                        snapshot.plugin_id,
                        current_revision,
                    ),
                )
            self._conn.commit()
        return snapshot

    def list_installations(self, project_key: str) -> list[PluginSnapshot]:
        with self._lock:
            rows = self._conn.execute(
                "SELECT snapshot_json FROM plugin_installations WHERE project_key = ? "
                "ORDER BY plugin_id",
                (project_key,),
            ).fetchall()
        return [PluginSnapshot.model_validate_json(row["snapshot_json"]) for row in rows]

    def save_binding(
        self, binding: FlowBindingSnapshot, *, expected_revision: int | None = None
    ) -> FlowBindingSnapshot:
        snapshot_json = binding.model_dump_json(by_alias=True)
        with self._lock:
            row = self._conn.execute(
                "SELECT revision FROM plugin_flow_bindings WHERE project_key = ? "
                "AND plugin_id = ? AND logical_flow_id = ?",
                (binding.project_key, binding.plugin_id, binding.logical_flow_id),
            ).fetchone()
            values = (
                binding.project_key,
                binding.plugin_id,
                binding.logical_flow_id,
                binding.ownership,
                binding.directus_flow_uuid,
                binding.trigger_type,
                binding.contract_version,
                binding.installed_definition_hash,
                binding.observed_definition_hash,
                binding.revision,
                binding.health,
                binding.drift_status,
                binding.last_error,
                snapshot_json,
            )
            if row is None:
                if expected_revision is not None:
                    raise PluginStoreConflictError("Flow binding revision is stale")
                self._conn.execute(
                    """
                    INSERT INTO plugin_flow_bindings (
                    project_key, plugin_id, logical_flow_id, ownership,
                    directus_flow_uuid, trigger_type, contract_version,
                    installed_definition_hash, observed_definition_hash,
                    revision, health, drift_status, last_error, snapshot_json
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    values,
                )
            else:
                current_revision = int(row["revision"])
                if expected_revision != current_revision:
                    raise PluginStoreConflictError("Flow binding revision is stale")
                cursor = self._conn.execute(
                    """
                    UPDATE plugin_flow_bindings SET ownership = ?, directus_flow_uuid = ?,
                        trigger_type = ?, contract_version = ?, installed_definition_hash = ?,
                        observed_definition_hash = ?, revision = ?, health = ?, drift_status = ?,
                        last_error = ?, snapshot_json = ?
                    WHERE project_key = ? AND plugin_id = ? AND logical_flow_id = ?
                      AND revision = ?
                    """,
                    (
                        binding.ownership,
                        binding.directus_flow_uuid,
                        binding.trigger_type,
                        binding.contract_version,
                        binding.installed_definition_hash,
                        binding.observed_definition_hash,
                        binding.revision,
                        binding.health,
                        binding.drift_status,
                        binding.last_error,
                        snapshot_json,
                        binding.project_key,
                        binding.plugin_id,
                        binding.logical_flow_id,
                        current_revision,
                    ),
                )
                if cursor.rowcount != 1:
                    raise PluginStoreConflictError("Flow binding revision is stale")
            self._conn.commit()
        return binding

    def get_binding(
        self, project_key: str, plugin_id: str, logical_flow_id: str
    ) -> FlowBindingSnapshot | None:
        with self._lock:
            row = self._conn.execute(
                "SELECT snapshot_json FROM plugin_flow_bindings "
                "WHERE project_key = ? AND plugin_id = ? AND logical_flow_id = ?",
                (project_key, plugin_id, logical_flow_id),
            ).fetchone()
        return (
            None if row is None else FlowBindingSnapshot.model_validate_json(row["snapshot_json"])
        )

    def list_bindings(self, project_key: str, plugin_id: str) -> list[FlowBindingSnapshot]:
        with self._lock:
            rows = self._conn.execute(
                "SELECT snapshot_json FROM plugin_flow_bindings "
                "WHERE project_key = ? AND plugin_id = ? ORDER BY logical_flow_id",
                (project_key, plugin_id),
            ).fetchall()
        return [FlowBindingSnapshot.model_validate_json(row["snapshot_json"]) for row in rows]

    def delete_bindings(self, project_key: str, plugin_id: str) -> int:
        with self._lock:
            cursor = self._conn.execute(
                "DELETE FROM plugin_flow_bindings WHERE project_key = ? AND plugin_id = ?",
                (project_key, plugin_id),
            )
            self._conn.commit()
            return cursor.rowcount

    def delete_binding(self, project_key: str, plugin_id: str, logical_flow_id: str) -> bool:
        with self._lock:
            cursor = self._conn.execute(
                """
                DELETE FROM plugin_flow_bindings
                WHERE project_key = ? AND plugin_id = ? AND logical_flow_id = ?
                """,
                (project_key, plugin_id, logical_flow_id),
            )
            self._conn.commit()
            return cursor.rowcount > 0

    def delete_installation(self, project_key: str, plugin_id: str) -> bool:
        with self._lock:
            cursor = self._conn.execute(
                "DELETE FROM plugin_installations WHERE project_key = ? AND plugin_id = ?",
                (project_key, plugin_id),
            )
            self._conn.commit()
            return cursor.rowcount > 0

    def save_package_revision(self, revision: PluginPackageRevision) -> PluginPackageRevision:
        with self._lock:
            self._conn.execute(
                """
                INSERT INTO plugin_package_revisions (
                    project_key, plugin_id, version, package_hash, local_path,
                    manifest_json, flow_bindings_json, state
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(project_key, plugin_id, version, package_hash) DO UPDATE SET
                    local_path=excluded.local_path,
                    manifest_json=excluded.manifest_json,
                    flow_bindings_json=excluded.flow_bindings_json,
                    state=excluded.state
                """,
                (
                    revision.project_key,
                    revision.plugin_id,
                    revision.version,
                    revision.package_hash,
                    revision.local_path,
                    revision.manifest.model_dump_json(by_alias=True),
                    json.dumps(
                        [
                            binding.model_dump(mode="json", by_alias=True)
                            for binding in revision.flow_bindings
                        ],
                        ensure_ascii=False,
                        separators=(",", ":"),
                    ),
                    revision.state,
                ),
            )
            self._conn.commit()
        return revision

    def list_package_revisions(
        self, project_key: str, plugin_id: str
    ) -> list[PluginPackageRevision]:
        with self._lock:
            rows = self._conn.execute(
                """
                SELECT version, package_hash, local_path, manifest_json,
                       flow_bindings_json, state
                FROM plugin_package_revisions
                WHERE project_key = ? AND plugin_id = ?
                ORDER BY created_at, version, package_hash
                """,
                (project_key, plugin_id),
            ).fetchall()
        return [
            PluginPackageRevision(
                project_key=project_key,
                plugin_id=plugin_id,
                version=row["version"],
                package_hash=row["package_hash"],
                local_path=row["local_path"],
                manifest=PluginManifest.model_validate_json(row["manifest_json"]),
                flow_bindings=[
                    FlowBindingSnapshot.model_validate(item)
                    for item in json.loads(row["flow_bindings_json"])
                ],
                state=row["state"],
            )
            for row in rows
        ]

    def delete_package_revision(
        self, project_key: str, plugin_id: str, version: str, package_hash: str
    ) -> bool:
        with self._lock:
            cursor = self._conn.execute(
                """
                DELETE FROM plugin_package_revisions
                WHERE project_key = ? AND plugin_id = ? AND version = ? AND package_hash = ?
                """,
                (project_key, plugin_id, version, package_hash),
            )
            self._conn.commit()
            return cursor.rowcount > 0

    def delete_package_revisions(self, project_key: str, plugin_id: str) -> int:
        with self._lock:
            cursor = self._conn.execute(
                "DELETE FROM plugin_package_revisions WHERE project_key = ? AND plugin_id = ?",
                (project_key, plugin_id),
            )
            self._conn.commit()
            return cursor.rowcount

    def is_package_path_referenced(self, local_path: str) -> bool:
        with self._lock:
            row = self._conn.execute(
                "SELECT 1 FROM plugin_package_revisions WHERE local_path = ? LIMIT 1",
                (local_path,),
            ).fetchone()
        return row is not None

    def save_private_setting(
        self,
        setting: PluginPrivateSetting,
        *,
        expected_revision: int | None = None,
    ) -> PluginPrivateSetting:
        import json

        with self._lock:
            row = self._conn.execute(
                """
                SELECT revision FROM plugin_private_settings
                WHERE project_key = ? AND plugin_id = ? AND setting_key = ?
                """,
                (setting.project_key, setting.plugin_id, setting.setting_key),
            ).fetchone()
            if row is None:
                if expected_revision is not None:
                    raise PluginStoreConflictError("private setting revision is stale")
                self._conn.execute(
                    """
                    INSERT INTO plugin_private_settings (
                        project_key, plugin_id, setting_key, value_json, revision
                    ) VALUES (?, ?, ?, ?, ?)
                    """,
                    (
                        setting.project_key,
                        setting.plugin_id,
                        setting.setting_key,
                        json.dumps(setting.value, ensure_ascii=False, separators=(",", ":")),
                        setting.revision,
                    ),
                )
            else:
                current = int(row["revision"])
                if expected_revision != current:
                    raise PluginStoreConflictError("private setting revision is stale")
                self._conn.execute(
                    """
                    UPDATE plugin_private_settings
                    SET value_json = ?, revision = ?, updated_at = CURRENT_TIMESTAMP
                    WHERE project_key = ? AND plugin_id = ? AND setting_key = ?
                      AND revision = ?
                    """,
                    (
                        json.dumps(setting.value, ensure_ascii=False, separators=(",", ":")),
                        setting.revision,
                        setting.project_key,
                        setting.plugin_id,
                        setting.setting_key,
                        current,
                    ),
                )
            self._conn.commit()
        return setting

    def get_private_setting(
        self, project_key: str, plugin_id: str, setting_key: str
    ) -> PluginPrivateSetting | None:
        import json

        with self._lock:
            row = self._conn.execute(
                """
                SELECT value_json, revision FROM plugin_private_settings
                WHERE project_key = ? AND plugin_id = ? AND setting_key = ?
                """,
                (project_key, plugin_id, setting_key),
            ).fetchone()
        if row is None:
            return None
        return PluginPrivateSetting(
            project_key=project_key,
            plugin_id=plugin_id,
            setting_key=setting_key,
            value=json.loads(row["value_json"]),
            revision=int(row["revision"]),
        )

    def delete_private_settings(self, project_key: str, plugin_id: str) -> int:
        with self._lock:
            cursor = self._conn.execute(
                "DELETE FROM plugin_private_settings WHERE project_key = ? AND plugin_id = ?",
                (project_key, plugin_id),
            )
            self._conn.commit()
            return cursor.rowcount

    def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
        with self._lock:
            self._conn.execute(
                """
                INSERT INTO plugin_audit_events (
                    event_id, project_key, plugin_id, plugin_version,
                    package_hash, event_type, outcome, event_json
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    event.event_id,
                    event.project_key,
                    event.plugin_id,
                    event.plugin_version,
                    event.package_hash,
                    event.event_type,
                    event.outcome,
                    event.model_dump_json(by_alias=True),
                ),
            )
            self._conn.commit()
        return event

    def list_audit(self, project_key: str, plugin_id: str) -> list[PluginAuditEvent]:
        with self._lock:
            rows = self._conn.execute(
                "SELECT event_json FROM plugin_audit_events "
                "WHERE project_key = ? AND plugin_id = ? ORDER BY rowid",
                (project_key, plugin_id),
            ).fetchall()
        return [PluginAuditEvent.model_validate_json(row["event_json"]) for row in rows]

    def list_project_audit(self, project_key: str) -> list[PluginAuditEvent]:
        with self._lock:
            rows = self._conn.execute(
                "SELECT event_json FROM plugin_audit_events WHERE project_key = ? ORDER BY rowid",
                (project_key,),
            ).fetchall()
        return [PluginAuditEvent.model_validate_json(row["event_json"]) for row in rows]

    def close(self) -> None:
        with self._lock:
            self._conn.close()


__all__ = ["InMemoryPluginStore", "PluginProjectStore", "PluginStoreConflictError"]
