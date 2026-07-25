"""Durable project-local storage for local-worker plugins."""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

from backend.contracts.plugin import (
    PluginAuditEvent,
    PluginPackageRevision,
    PluginPrivateSetting,
    PluginSnapshot,
)


class PluginStoreConflictError(Exception):
    pass


class InMemoryPluginStore:
    def __init__(self) -> None:
        self._installations: dict[tuple[str, str], PluginSnapshot] = {}
        self._revisions: dict[tuple[str, str, str], PluginPackageRevision] = {}
        self._settings: dict[tuple[str, str, str], PluginPrivateSetting] = {}
        self._audit: list[PluginAuditEvent] = []

    def get_installation(self, project_key: str, plugin_id: str) -> PluginSnapshot | None:
        return self._installations.get((project_key, plugin_id))

    def save_installation(
        self,
        snapshot: PluginSnapshot,
        *,
        expected_revision: int | None,
    ) -> PluginSnapshot:
        key = (snapshot.project_key, snapshot.plugin_id)
        current = self._installations.get(key)
        actual = None if current is None else current.revision
        if actual != expected_revision:
            raise PluginStoreConflictError(
                f"plugin revision mismatch: expected {expected_revision}, found {actual}"
            )
        self._installations[key] = snapshot
        return snapshot

    def list_installations(self, project_key: str) -> list[PluginSnapshot]:
        return sorted(
            (
                snapshot
                for (candidate, _), snapshot in self._installations.items()
                if candidate == project_key
            ),
            key=lambda snapshot: snapshot.plugin_id,
        )

    def delete_installation(self, project_key: str, plugin_id: str) -> bool:
        return self._installations.pop((project_key, plugin_id), None) is not None

    def save_package_revision(
        self,
        revision: PluginPackageRevision,
    ) -> PluginPackageRevision:
        self._revisions[
            (revision.project_key, revision.plugin_id, revision.package_hash)
        ] = revision
        return revision

    def list_package_revisions(
        self,
        project_key: str,
        plugin_id: str,
    ) -> list[PluginPackageRevision]:
        return [
            revision
            for (project, plugin, _), revision in self._revisions.items()
            if project == project_key and plugin == plugin_id
        ]

    def delete_package_revision(
        self,
        project_key: str,
        plugin_id: str,
        package_hash: str,
    ) -> bool:
        return self._revisions.pop((project_key, plugin_id, package_hash), None) is not None

    def delete_package_revisions(self, project_key: str, plugin_id: str) -> int:
        keys = [
            key for key in self._revisions if key[:2] == (project_key, plugin_id)
        ]
        for key in keys:
            del self._revisions[key]
        return len(keys)

    def is_package_path_referenced(self, local_path: str) -> bool:
        return any(item.local_path == local_path for item in self._revisions.values())

    def save_private_setting(
        self,
        setting: PluginPrivateSetting,
        *,
        expected_revision: int | None,
    ) -> PluginPrivateSetting:
        key = (setting.project_key, setting.plugin_id, setting.setting_key)
        current = self._settings.get(key)
        actual = None if current is None else current.revision
        if actual != expected_revision:
            raise PluginStoreConflictError(
                f"setting revision mismatch: expected {expected_revision}, found {actual}"
            )
        self._settings[key] = setting
        return setting

    def get_private_setting(
        self,
        project_key: str,
        plugin_id: str,
        setting_key: str,
    ) -> PluginPrivateSetting | None:
        return self._settings.get((project_key, plugin_id, setting_key))

    def delete_private_settings(self, project_key: str, plugin_id: str) -> int:
        keys = [
            key for key in self._settings if key[:2] == (project_key, plugin_id)
        ]
        for key in keys:
            del self._settings[key]
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


class PluginProjectStore(InMemoryPluginStore):
    """SQLite-backed store using product-neutral JSON snapshots."""

    def __init__(self, db_path: Path) -> None:
        super().__init__()
        db_path.parent.mkdir(parents=True, exist_ok=True)
        self.package_cache = db_path.parent / "plugin-packages"
        self._connection = sqlite3.connect(db_path)
        self._connection.execute(
            """
            CREATE TABLE IF NOT EXISTS plugin_records (
                kind TEXT NOT NULL,
                project_key TEXT NOT NULL,
                plugin_id TEXT NOT NULL,
                item_key TEXT NOT NULL,
                payload TEXT NOT NULL,
                PRIMARY KEY (kind, project_key, plugin_id, item_key)
            )
            """
        )
        self._connection.commit()
        self._load()

    def _load(self) -> None:
        for kind, project_key, plugin_id, item_key, payload in self._connection.execute(
            "SELECT kind, project_key, plugin_id, item_key, payload FROM plugin_records"
        ):
            value = json.loads(payload)
            if kind == "installation":
                self._installations[(project_key, plugin_id)] = PluginSnapshot.model_validate(value)
            elif kind == "revision":
                self._revisions[(project_key, plugin_id, item_key)] = (
                    PluginPackageRevision.model_validate(value)
                )
            elif kind == "setting":
                self._settings[(project_key, plugin_id, item_key)] = (
                    PluginPrivateSetting.model_validate(value)
                )
            elif kind == "audit":
                self._audit.append(PluginAuditEvent.model_validate(value))

    def _upsert(
        self,
        kind: str,
        project_key: str,
        plugin_id: str,
        item_key: str,
        value: object,
    ) -> None:
        payload = value.model_dump(mode="json", by_alias=True)  # type: ignore[attr-defined]
        self._connection.execute(
            """
            INSERT INTO plugin_records(kind, project_key, plugin_id, item_key, payload)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(kind, project_key, plugin_id, item_key)
            DO UPDATE SET payload = excluded.payload
            """,
            (
                kind,
                project_key,
                plugin_id,
                item_key,
                json.dumps(payload, ensure_ascii=False, separators=(",", ":")),
            ),
        )
        self._connection.commit()

    def save_installation(
        self,
        snapshot: PluginSnapshot,
        *,
        expected_revision: int | None,
    ) -> PluginSnapshot:
        saved = super().save_installation(snapshot, expected_revision=expected_revision)
        self._upsert(
            "installation",
            saved.project_key,
            saved.plugin_id,
            "current",
            saved,
        )
        return saved

    def delete_installation(self, project_key: str, plugin_id: str) -> bool:
        removed = super().delete_installation(project_key, plugin_id)
        self._connection.execute(
            "DELETE FROM plugin_records WHERE kind='installation' AND project_key=? AND plugin_id=?",
            (project_key, plugin_id),
        )
        self._connection.commit()
        return removed

    def save_package_revision(
        self,
        revision: PluginPackageRevision,
    ) -> PluginPackageRevision:
        saved = super().save_package_revision(revision)
        self._upsert(
            "revision",
            saved.project_key,
            saved.plugin_id,
            saved.package_hash,
            saved,
        )
        return saved

    def delete_package_revision(
        self,
        project_key: str,
        plugin_id: str,
        package_hash: str,
    ) -> bool:
        removed = super().delete_package_revision(project_key, plugin_id, package_hash)
        self._connection.execute(
            """
            DELETE FROM plugin_records
            WHERE kind='revision' AND project_key=? AND plugin_id=? AND item_key=?
            """,
            (project_key, plugin_id, package_hash),
        )
        self._connection.commit()
        return removed

    def delete_package_revisions(self, project_key: str, plugin_id: str) -> int:
        removed = super().delete_package_revisions(project_key, plugin_id)
        self._connection.execute(
            "DELETE FROM plugin_records WHERE kind='revision' AND project_key=? AND plugin_id=?",
            (project_key, plugin_id),
        )
        self._connection.commit()
        return removed

    def save_private_setting(
        self,
        setting: PluginPrivateSetting,
        *,
        expected_revision: int | None,
    ) -> PluginPrivateSetting:
        saved = super().save_private_setting(setting, expected_revision=expected_revision)
        self._upsert(
            "setting",
            saved.project_key,
            saved.plugin_id,
            saved.setting_key,
            saved,
        )
        return saved

    def delete_private_settings(self, project_key: str, plugin_id: str) -> int:
        removed = super().delete_private_settings(project_key, plugin_id)
        self._connection.execute(
            "DELETE FROM plugin_records WHERE kind='setting' AND project_key=? AND plugin_id=?",
            (project_key, plugin_id),
        )
        self._connection.commit()
        return removed

    def record_audit(self, event: PluginAuditEvent) -> PluginAuditEvent:
        recorded = super().record_audit(event)
        self._upsert(
            "audit",
            event.project_key,
            event.plugin_id,
            event.event_id,
            event,
        )
        return recorded

    def close(self) -> None:
        self._connection.close()


__all__ = [
    "InMemoryPluginStore",
    "PluginProjectStore",
    "PluginStoreConflictError",
]
