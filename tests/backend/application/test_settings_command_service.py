"""D2 settings/command service tests."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest

from backend.application.settings_command_service import (
    SettingsCommandError,
    SettingsCommandService,
)
from backend.contracts.settings_commands import (
    DeviceSettings,
    ShortcutEntry,
    ThemeTokens,
)


class FakeMetadataPort:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def list_metadata(
        self,
        namespace: str,
        *,
        scope: str | None = None,
        keys: list[str] | None = None,
    ) -> list[dict[str, Any]]:
        self.requests.append({"namespace": namespace, "scope": scope, "keys": list(keys or [])})
        if not self.responses:
            raise AssertionError("unexpected metadata read")
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


class FakeGrantAuthority:
    def __init__(self, accepted: set[tuple[str, str, str]]) -> None:
        self.accepted = accepted

    def resolve(self, grant_id: str, *, purpose: str, direction: str) -> object:
        if (grant_id, purpose, direction) not in self.accepted:
            raise ValueError("grant scope mismatch")
        return object()


def _service(
    metadata: FakeMetadataPort,
    state_path: Path,
    grant_authority: FakeGrantAuthority | None = None,
    command_executors: dict[str, Any] | None = None,
) -> SettingsCommandService:
    return SettingsCommandService(
        metadata_port=metadata,
        device_state_path=state_path,
        grant_authority=grant_authority,
        command_executors=command_executors,
    )


# ---------------------------------------------------------------------------
# Device settings
# ---------------------------------------------------------------------------


def test_device_settings_round_trip(tmp_path: Any) -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, tmp_path / "device.json")
    settings = DeviceSettings(theme=ThemeTokens(mode="dark", accent="#ff0000"))
    service.save_device(settings)
    loaded = service.read_device()
    assert loaded.theme.mode == "dark"
    assert loaded.theme.accent == "#ff0000"


def test_device_settings_recovers_from_corrupt_file(tmp_path: Any) -> None:
    path = tmp_path / "device.json"
    path.write_text("{not valid json", encoding="utf-8")
    transport = FakeMetadataPort([])
    service = _service(transport, path)
    loaded = service.read_device()
    # Corrupt → recover to defaults (never crash).
    assert loaded.schema_version == 1


def test_device_settings_defaults_when_missing(tmp_path: Any) -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, tmp_path / "nonexistent.json")
    loaded = service.read_device()
    assert loaded.theme.mode == "system"


# ---------------------------------------------------------------------------
# Shared settings
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_read_shared_returns_entries() -> None:
    transport = FakeMetadataPort(
        [
            [
                {
                    "key": "holiday",
                    "value": {"type": "national"},
                    "updatedOn": "2026-07-14",
                }
            ]
        ]
    )
    service = _service(transport, Path("x"))
    result = await service.read_shared("workspace", [])
    assert len(result.settings) == 1
    assert result.settings[0].key == "holiday"
    assert result.fresh is True
    assert transport.requests == [
        {
            "namespace": "shared_settings",
            "scope": "workspace",
            "keys": [],
        }
    ]


@pytest.mark.asyncio
async def test_read_shared_offline_returns_empty_not_fake_defaults() -> None:
    transport = FakeMetadataPort([ConnectionError("offline")])
    service = _service(transport, Path("x"))
    result = await service.read_shared("workspace", [])
    assert result.settings == []
    assert result.fresh is False  # no fake defaults


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------


def test_list_commands_returns_catalog() -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, Path("x"))
    result = service.list_commands()
    ids = {c.command_id for c in result.commands}
    assert "export.query" in ids
    assert ids.isdisjoint(
        {
            "workspace.relink",
            "plugin.upgrade",
            "plugin.rollback",
            "plugin.uninstall",
            "replica.synchronize",
        }
    )


@pytest.mark.asyncio
async def test_run_command_requires_grant_when_needed() -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="requires a path grant"):
        await service.run_command("export.query", {})


@pytest.mark.asyncio
async def test_run_command_executes_export_with_grant() -> None:
    transport = FakeMetadataPort([])

    async def execute(params: dict[str, Any], grant_id: str) -> dict[str, Any]:
        return {"rowsWritten": 2, "grantId": grant_id, "query": params["query"]}

    service = _service(
        transport,
        Path("x"),
        FakeGrantAuthority({("g1", "export_target", "write")}),
        {"export.query": execute},
    )
    result = await service.run_command(
        "export.query",
        {"collection": "orders", "format": "csv", "query": {"limit": 10}},
        grant_id="g1",
    )
    assert result.success is True
    assert result.output["rowsWritten"] == 2
    assert result.output["grantId"] == "g1"


@pytest.mark.asyncio
async def test_run_command_rejects_grant_with_wrong_scope() -> None:
    service = _service(
        FakeMetadataPort([]),
        Path("x"),
        FakeGrantAuthority({("g1", "import_source", "read")}),
    )
    with pytest.raises(SettingsCommandError) as error:
        await service.run_command("export.query", {}, grant_id="g1")
    assert error.value.code == "command_grant_invalid"


@pytest.mark.asyncio
async def test_run_command_rejects_unknown() -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="not in the static catalog"):
        await service.run_command("evil", {})


@pytest.mark.asyncio
async def test_run_command_never_acknowledges_without_an_executor() -> None:
    service = _service(
        FakeMetadataPort([]),
        Path("x"),
        FakeGrantAuthority({("g1", "export_target", "write")}),
    )
    with pytest.raises(SettingsCommandError) as error:
        await service.run_command("export.query", {}, grant_id="g1")
    assert error.value.code == "command_executor_unavailable"


# ---------------------------------------------------------------------------
# Shortcuts + Launch Broker
# ---------------------------------------------------------------------------


def test_save_shortcut_validates_command_reference() -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="unknown command"):
        service.save_shortcut(
            ShortcutEntry(shortcut_id="s1", target="built-in-command", command_id="evil")
        )


def test_save_shortcut_blocks_disallowed_url_scheme() -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="not allowed"):
        service.save_shortcut(
            ShortcutEntry(shortcut_id="s1", target="url", url="javascript:alert(1)")
        )


def test_save_shortcut_accepts_https_url() -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, Path("x"))
    service.save_shortcut(
        ShortcutEntry(shortcut_id="s1", target="url", url="https://example.com", label="Example")
    )
    result = service.list_shortcuts()
    assert len(result.shortcuts) == 1


def test_launch_action_succeeds_for_valid_shortcut() -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, Path("x"))
    service.save_shortcut(ShortcutEntry(shortcut_id="s1", target="url", url="https://example.com"))
    result = service.launch_action("s1")
    assert result.launched is True


def test_launch_action_rejects_unknown_shortcut() -> None:
    transport = FakeMetadataPort([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="not found"):
        service.launch_action("bogus")
