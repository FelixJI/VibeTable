"""Focused PocketBase plugin store adapter serialization and error mapping."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest
from pydantic import JsonValue

from backend.adapters.pocketbase.client import PocketBaseProductError
from backend.adapters.pocketbase.plugin_store import PocketBasePluginStore
from backend.application.plugin_registry import PluginRegistryError
from backend.contracts.plugin import (
    InstallPlan,
    PluginAuditEvent,
    PluginManifest,
    PluginPackageRevision,
    PluginPrivateSetting,
    PluginSnapshot,
)
from backend.infrastructure.plugin_store import PluginStoreConflictError


class FakePluginStoreClient:
    """Records each fixed operation and its frozen request fields."""

    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.calls: list[tuple[str, dict[str, JsonValue]]] = []

    async def plugin_store(
        self,
        operation: str,
        request: dict[str, JsonValue],
    ) -> JsonValue:
        self.calls.append((operation, dict(request)))
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


def _manifest() -> PluginManifest:
    return PluginManifest.model_validate(
        {
            "$schema": "vibetable.plugin-manifest.v1",
            "pluginId": "com.example.summary",
            "version": "1.0.0",
            "displayName": {"en": "Summary"},
            "compatibility": {"minHostVersion": "1.0.0", "pluginApi": "1.x"},
            "permissions": {"data": [], "files": [], "privateStorage": True},
            "actions": [
                {
                    "actionId": "summarize",
                    "displayName": {"en": "Summarize"},
                    "mode": "local",
                    "risk": "read",
                    "workerEntry": "dist/worker.js",
                }
            ],
        }
    )


def _snapshot(*, revision: int = 2) -> PluginSnapshot:
    return PluginSnapshot(
        project_key="local:default",
        plugin_id="com.example.summary",
        version="1.0.0",
        package_hash="sha256:package",
        source_type="package",
        source_location="summary.vtplugin",
        manifest=_manifest(),
        status="enabled",
        revision=revision,
    )


def _snapshot_payload(*, revision: int = 2) -> dict[str, JsonValue]:
    return _snapshot(revision=revision).model_dump(mode="json", by_alias=True)


def _setting() -> PluginPrivateSetting:
    return PluginPrivateSetting(
        project_key="local:default",
        plugin_id="com.example.summary",
        setting_key="columns",
        value={"visible": ["title"]},
        revision=3,
    )


def _package_revision() -> PluginPackageRevision:
    return PluginPackageRevision(
        project_key="local:default",
        plugin_id="com.example.summary",
        version="1.0.0",
        package_hash="sha256:package",
        local_path=str(Path("cache") / "abc.vtplugin"),
        manifest=_manifest(),
        state="current",
    )


def _audit() -> PluginAuditEvent:
    return PluginAuditEvent(
        event_id="audit-1",
        project_key="local:default",
        plugin_id="com.example.summary",
        plugin_version="1.0.0",
        package_hash="sha256:package",
        event_type="install",
        outcome="succeeded",
    )


def _plan() -> InstallPlan:
    return InstallPlan(
        plan_id="plan-1",
        project_key="local:default",
        project_revision="project-r1",
        source_type="package",
        source_location="summary.vtplugin",
        package_hash="sha256:package",
        manifest=_manifest(),
    )


def _product_error(code: str, message: str = "plugin storage failed") -> PocketBaseProductError:
    return PocketBaseProductError(status=409, payload={"code": code, "message": message})


@pytest.mark.asyncio
async def test_save_installation_serializes_camelcase_payload_with_expected_revision() -> None:
    client = FakePluginStoreClient([_snapshot_payload()])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    saved = await store.save_installation(_snapshot(), expected_revision=1)

    assert saved == _snapshot()
    assert client.calls == [
        (
            "save_installation",
            {
                "projectKey": "local:default",
                "pluginId": "com.example.summary",
                "payload": _snapshot_payload(),
                "expectedRevision": 1,
            },
        )
    ]


@pytest.mark.asyncio
async def test_get_installation_maps_null_and_object_results() -> None:
    client = FakePluginStoreClient([None, _snapshot_payload()])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    missing = await store.get_installation("local:default", "com.example.summary")
    current = await store.get_installation("local:default", "com.example.summary")

    assert missing is None
    assert current == _snapshot()
    assert client.calls[0] == (
        "get_installation",
        {"projectKey": "local:default", "pluginId": "com.example.summary"},
    )


@pytest.mark.asyncio
async def test_list_installations_validates_each_object() -> None:
    client = FakePluginStoreClient([[_snapshot_payload(), _snapshot_payload(revision=3)]])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    listed = await store.list_installations("local:default")

    assert listed == [_snapshot(), _snapshot(revision=3)]


@pytest.mark.asyncio
async def test_identity_and_item_key_operations_use_frozen_fields() -> None:
    client = FakePluginStoreClient([True, 2, False])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    deleted = await store.delete_installation("local:default", "com.example.summary")
    removed = await store.delete_package_revisions("local:default", "com.example.summary")
    referenced = await store.is_package_path_referenced("cache/abc.vtplugin")

    assert (deleted, removed, referenced) == (True, 2, False)
    assert client.calls == [
        (
            "delete_installation",
            {"projectKey": "local:default", "pluginId": "com.example.summary"},
        ),
        (
            "delete_package_revisions",
            {"projectKey": "local:default", "pluginId": "com.example.summary"},
        ),
        ("is_package_path_referenced", {"localPath": "cache/abc.vtplugin"}),
    ]


@pytest.mark.asyncio
async def test_package_revision_and_item_key_round_trip() -> None:
    payload = _package_revision().model_dump(mode="json", by_alias=True)
    client = FakePluginStoreClient([[payload], payload, True])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    listed = await store.list_package_revisions("local:default", "com.example.summary")
    saved = await store.save_package_revision(_package_revision())
    deleted = await store.delete_package_revision(
        "local:default",
        "com.example.summary",
        "sha256:package",
    )

    assert listed == [_package_revision()]
    assert saved == _package_revision()
    assert deleted is True
    assert client.calls[1] == (
        "save_package_revision",
        {
            "projectKey": "local:default",
            "pluginId": "com.example.summary",
            "payload": payload,
        },
    )
    assert client.calls[2] == (
        "delete_package_revision",
        {
            "projectKey": "local:default",
            "pluginId": "com.example.summary",
            "itemKey": "sha256:package",
        },
    )


@pytest.mark.asyncio
async def test_private_setting_round_trip_uses_item_key_and_revision_guard() -> None:
    payload = _setting().model_dump(mode="json", by_alias=True)
    client = FakePluginStoreClient([None, payload, 1])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    missing = await store.get_private_setting(
        "local:default",
        "com.example.summary",
        "columns",
    )
    saved = await store.save_private_setting(_setting(), expected_revision=2)
    removed = await store.delete_private_settings("local:default", "com.example.summary")

    assert saved == _setting()
    assert missing is None
    assert removed == 1
    assert client.calls[0] == (
        "get_private_setting",
        {
            "projectKey": "local:default",
            "pluginId": "com.example.summary",
            "itemKey": "columns",
        },
    )
    assert client.calls[1] == (
        "save_private_setting",
        {
            "projectKey": "local:default",
            "pluginId": "com.example.summary",
            "payload": payload,
            "expectedRevision": 2,
        },
    )


@pytest.mark.asyncio
async def test_audit_operations_round_trip_payload_models() -> None:
    payload = _audit().model_dump(mode="json", by_alias=True)
    client = FakePluginStoreClient([payload, [payload], [payload]])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    recorded = await store.record_audit(_audit())
    listed = await store.list_audit("local:default", "com.example.summary")
    project = await store.list_project_audit("local:default")

    assert recorded == _audit()
    assert listed == [_audit()]
    assert project == [_audit()]
    assert client.calls[0] == (
        "record_audit",
        {
            "projectKey": "local:default",
            "pluginId": "com.example.summary",
            "payload": payload,
        },
    )
    assert client.calls[2] == ("list_project_audit", {"projectKey": "local:default"})


@pytest.mark.asyncio
async def test_commit_install_posts_plan_and_package_revision_models() -> None:
    client = FakePluginStoreClient([_snapshot_payload(revision=1)])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    installed = await store.commit_install(
        _plan(),
        package_revision=_package_revision(),
    )

    assert installed == _snapshot(revision=1)
    assert client.calls == [
        (
            "commit_install",
            {
                "plan": _plan().model_dump(mode="json", by_alias=True),
                "packageRevision": _package_revision().model_dump(mode="json", by_alias=True),
            },
        )
    ]


@pytest.mark.asyncio
async def test_revision_conflict_maps_to_frozen_conflict_error() -> None:
    message = "plugin revision mismatch: expected 1, found 2"
    client = FakePluginStoreClient([_product_error("plugin.revision_conflict", message)])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    with pytest.raises(PluginStoreConflictError) as error:
        await store.save_installation(_snapshot(), expected_revision=1)

    assert str(error.value) == message


@pytest.mark.asyncio
async def test_public_registry_errors_map_to_frozen_codes() -> None:
    cases = [
        ("plugin_already_installed", "plugin is already installed"),
        ("plugin_not_found", "plugin is not installed"),
        ("plugin_blocked", "plugin has blocking reasons"),
    ]
    for code, message in cases:
        client = FakePluginStoreClient([_product_error(code, message)])
        store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

        with pytest.raises(PluginRegistryError) as error:
            await store.commit_install(
                _plan(),
                package_revision=_package_revision(),
            )

        assert error.value.code == code
        assert str(error.value) == message


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("internal_code", "public_code"),
    [
        ("plugin.already_installed", "plugin_already_installed"),
        ("plugin.not_found", "plugin_not_found"),
    ],
)
async def test_private_registry_errors_map_to_frozen_codes(
    internal_code: str, public_code: str
) -> None:
    client = FakePluginStoreClient([_product_error(internal_code, "private store failure")])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    with pytest.raises(PluginRegistryError) as error:
        await store.commit_install(_plan(), package_revision=_package_revision())

    assert error.value.code == public_code


@pytest.mark.asyncio
async def test_internal_store_errors_keep_code_and_message() -> None:
    client = FakePluginStoreClient([_product_error("plugin.storage_failed", "boom")])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    with pytest.raises(PocketBaseProductError) as error:
        await store.list_installations("local:default")

    assert error.value.code == "plugin.storage_failed"
    assert str(error.value) == "boom"


@pytest.mark.asyncio
async def test_invalid_result_shapes_fail_closed() -> None:
    client = FakePluginStoreClient(["not-an-object"])
    store = PocketBasePluginStore(client=client, package_cache=Path("cache"))

    with pytest.raises(ValueError, match="invalid plugin installation"):
        await store.get_installation("local:default", "com.example.summary")
