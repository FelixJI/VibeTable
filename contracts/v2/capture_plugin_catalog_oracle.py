"""Freeze the Python plugin catalog authority; replay only the fixed producer."""

from __future__ import annotations

import asyncio
import json
import sqlite3
import tempfile
import uuid as uuid_module
from datetime import UTC, datetime, tzinfo
from pathlib import Path
from types import SimpleNamespace
from typing import cast
from unittest.mock import patch

from pydantic import BaseModel

import backend.application.plugin_registry as plugin_registry_module
from backend.application.plugin_registry import PluginRegistry, PluginRegistryError
from backend.application.revisioned_metadata_port import JsonObject, JsonValue
from backend.contracts.plugin import (
    InstallPlan,
    PluginAction,
    PluginManifest,
    PluginPackageRevision,
    PluginPrivateSetting,
    PluginSnapshot,
)
from backend.infrastructure.plugin_store import PluginProjectStore, PluginStoreConflictError

PRODUCER = "aa564213d9526d79a93182cd1db2a4dcbc08f1ef"
PRODUCER_SOURCES = (
    "backend/application/plugin_registry.py",
    "backend/infrastructure/plugin_store.py",
    "backend/contracts/plugin.py",
)
# Host PluginProjectContext identity format: workspace UUID "N" (32 lowercase
# hex) and "{identity}:{sessionEpoch}" for the revision.
WORKSPACE_IDENTITY = "0f8f4a3b2c1d4e5f8091a2b3c4d5e6f7"
PROJECT_KEY = f"local:{WORKSPACE_IDENTITY}"
PROJECT_REVISION = f"{WORKSPACE_IDENTITY}:7"
# Real package byte identity format: "sha256:" + 64 lowercase hex.
READER_PACKAGE_HASH = "sha256:" + "0123456789abcdef" * 4
BLOCKED_PACKAGE_HASH = "sha256:" + "fedcba9876543210" * 4
FIXED_CLOCK = datetime(2026, 1, 1, 8, 0, 0, tzinfo=UTC)
FIXED_EVENT_IDS = [
    uuid_module.UUID(f"00000000-0000-4000-8000-{index:012d}") for index in range(1, 17)
]


class _FixedDatetime:
    @staticmethod
    def now(tz: tzinfo | None = None) -> datetime:
        return FIXED_CLOCK.replace(tzinfo=tz)


def _dump(model: BaseModel) -> JsonValue:
    return cast(JsonValue, model.model_dump(mode="json", by_alias=True))


def _manifest(plugin_id: str = "com.example.reader", version: str = "1.2.0") -> PluginManifest:
    return PluginManifest(
        plugin_id=plugin_id,
        version=version,
        display_name={"en": "Reader", "zh-CN": "阅读器"},
        permissions={"data": [], "privateStorage": True},
        actions=[
            PluginAction(
                action_id="summarize",
                display_name={"en": "Summarize"},
                risk="read",
                worker_entry="dist/worker.js",
                placements=["toolbar"],
            )
        ],
        ui={"customViews": []},
    )


def _plan(plugin_id: str = "com.example.reader") -> InstallPlan:
    return InstallPlan(
        plan_id="plugin-plan-fixed",
        project_key=PROJECT_KEY,
        project_revision=PROJECT_REVISION,
        source_type="package",
        source_location="C:/Users/demo/Downloads/reader.vtplugin",
        package_hash=READER_PACKAGE_HASH,
        manifest=_manifest(plugin_id),
        schemas={"form": {"type": "object"}},
    )


def _error(exc: BaseException) -> JsonObject:
    payload: JsonObject = {"type": type(exc).__name__, "message": str(exc)}
    rpc_data: dict[str, str] | None = getattr(exc, "rpc_error_data", None)
    if rpc_data is not None:
        payload["rpcErrorData"] = cast(JsonValue, dict(rpc_data))
    return payload


def replay() -> JsonObject:
    """Drive the producer registry + SQLite store deterministically."""
    cases: list[JsonObject] = []
    with tempfile.TemporaryDirectory() as tmp:
        db_path = Path(tmp) / "plugins.db"
        store = PluginProjectStore(db_path)
        registry = PluginRegistry(store=store)
        event_ids = iter(FIXED_EVENT_IDS)
        with (
            patch.object(
                plugin_registry_module,
                "uuid",
                SimpleNamespace(uuid4=lambda: str(next(event_ids))),
            ),
            patch.object(plugin_registry_module, "datetime", _FixedDatetime),
        ):
            try:
                installed = asyncio.run(registry.install(_plan()))
                cases.append({"name": "install-snapshot", "result": _dump(installed)})

                try:
                    asyncio.run(registry.install(_plan()))
                except PluginRegistryError as exc:
                    cases.append({"name": "install-duplicate-error", "error": _error(exc)})

                cases.append(
                    {
                        "name": "list-catalog-single",
                        "result": [_dump(item) for item in registry.list(PROJECT_KEY)],
                    }
                )

                enabled = asyncio.run(registry.set_enabled(PROJECT_KEY, "com.example.reader", True))
                disabled = asyncio.run(
                    registry.set_enabled(PROJECT_KEY, "com.example.reader", False)
                )
                cases.append(
                    {
                        "name": "set-enabled-lifecycle",
                        "enabled": _dump(enabled),
                        "disabled": _dump(disabled),
                    }
                )

                try:
                    asyncio.run(registry.set_enabled(PROJECT_KEY, "com.example.missing", True))
                except PluginRegistryError as exc:
                    cases.append({"name": "set-enabled-missing-error", "error": _error(exc)})

                blocked_snapshot = PluginSnapshot(
                    project_key=PROJECT_KEY,
                    plugin_id="com.example.blocked",
                    version="1.0.0",
                    package_hash=BLOCKED_PACKAGE_HASH,
                    source_type="local-folder",
                    source_location="C:/Users/demo/dev/blocked-plugin",
                    development_source_location="C:/Users/demo/dev/blocked-plugin",
                    manifest=_manifest("com.example.blocked", "1.0.0"),
                    status="disabled",
                    disabled_reason="disabled_by_user",
                    blocking_reasons=["requires_reinstall"],
                    revision=1,
                )
                store.save_installation(blocked_snapshot, expected_revision=None)
                try:
                    asyncio.run(registry.set_enabled(PROJECT_KEY, "com.example.blocked", True))
                except PluginRegistryError as exc:
                    cases.append({"name": "set-enabled-blocked-error", "error": _error(exc)})

                try:
                    store.save_installation(
                        blocked_snapshot.model_copy(update={"revision": 9}),
                        expected_revision=0,
                    )
                except PluginStoreConflictError as exc:
                    cases.append({"name": "store-cas-conflict-error", "error": _error(exc)})

                revision = store.save_package_revision(
                    PluginPackageRevision(
                        project_key=PROJECT_KEY,
                        plugin_id="com.example.reader",
                        version="1.2.0",
                        package_hash=READER_PACKAGE_HASH,
                        local_path="C:/Users/demo/.vibetable/plugin-packages/reader-1.2.0",
                        manifest=_manifest(),
                        state="current",
                    )
                )
                cases.append(
                    {
                        "name": "package-revision-persisted",
                        "saved": _dump(revision),
                        "listed": [
                            _dump(item)
                            for item in store.list_package_revisions(
                                PROJECT_KEY, "com.example.reader"
                            )
                        ],
                    }
                )

                setting = store.save_private_setting(
                    PluginPrivateSetting(
                        project_key=PROJECT_KEY,
                        plugin_id="com.example.reader",
                        setting_key="columns",
                        value={"selected": ["name", "amount"]},
                        revision=1,
                    ),
                    expected_revision=None,
                )
                fetched = store.get_private_setting(PROJECT_KEY, "com.example.reader", "columns")
                assert fetched is not None
                cases.append(
                    {
                        "name": "private-setting-persisted",
                        "saved": _dump(setting),
                        "fetched": _dump(fetched),
                    }
                )

                cases.append(
                    {
                        "name": "list-audit",
                        "reader": [
                            _dump(event)
                            for event in store.list_audit(PROJECT_KEY, "com.example.reader")
                        ],
                        "blocked": [
                            _dump(event)
                            for event in store.list_audit(PROJECT_KEY, "com.example.blocked")
                        ],
                    }
                )

                connection = sqlite3.connect(db_path)
                try:
                    rows: list[tuple[str, str, str, str, str]] = connection.execute(
                        "SELECT kind, project_key, plugin_id, item_key, payload"
                        " FROM plugin_records"
                        " ORDER BY kind, project_key, plugin_id, item_key"
                    ).fetchall()
                finally:
                    connection.close()
                persisted_rows: list[JsonValue] = []
                for kind, project_key, plugin_id, item_key, payload in rows:
                    row: JsonValue = {
                        "kind": kind,
                        "projectKey": project_key,
                        "pluginId": plugin_id,
                        "itemKey": item_key,
                        "payload": cast(JsonValue, json.loads(payload)),
                    }
                    persisted_rows.append(row)
                cases.append({"name": "persisted-four-kinds", "rows": persisted_rows})
            finally:
                store.close()
    return {
        "producer": PRODUCER,
        "producerSources": cast(JsonValue, list(PRODUCER_SOURCES)),
        "adapter": (
            "PluginRegistry + PluginProjectStore (SQLite, temporary directory); not "
            "Go/PocketBase output"
        ),
        "normalization": (
            "audit event_id values come from a fixed uuid4 sequence and "
            "started_at/finished_at from a fixed UTC clock pinned inside "
            "backend.application.plugin_registry by the harness; no other producer "
            "behavior is replaced and no output is edited after capture"
        ),
        "cases": cast(JsonValue, cases),
    }
