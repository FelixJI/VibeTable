"""D2 settings/command service tests."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest

from backend.adapters.directus.auth import CurrentUser, DirectusAuthBroker
from backend.adapters.directus.profile import CapabilityManifest
from backend.application.settings_command_service import (
    APPROVED_FLOWS,
    SettingsCommandError,
    SettingsCommandService,
)
from backend.contracts.settings_commands import (
    DeviceSettings,
    ShortcutEntry,
    ThemeTokens,
)


class FakeDirectusAuth(DirectusAuthBroker):
    def __init__(self) -> None:
        self._user = CurrentUser(id="u1", display_name="T", role_id="r1")

    async def access_token(self) -> str:
        return "tok"


class FakeTransport:
    def __init__(self, responses: list[Any]) -> None:
        self.responses = list(responses)
        self.requests: list[dict[str, Any]] = []

    async def request(self, method: str, path: str, **kwargs: Any) -> Any:
        self.requests.append({"method": method, "path": path, **kwargs})
        if not self.responses:
            raise AssertionError(f"unexpected {method} {path}")
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


def _manifest() -> CapabilityManifest:
    return CapabilityManifest.model_validate(
        {
            "contract": "directus.project.v1",
            "schema_version": "vibetable-1.0",
            "directus_compatibility": ">=12 <13",
            "collections": [
                {
                    "collection": "vibetable_settings",
                    "primary_key": "id",
                    "fields": ["id", "key", "value", "date_updated"],
                    "create_fields": ["key", "value"],
                    "update_fields": ["key", "value"],
                    "archive_field": None,
                    "date_updated_field": "date_updated",
                }
            ],
        }
    )


def _service(
    transport: FakeTransport,
    state_path: Path,
) -> SettingsCommandService:
    return SettingsCommandService(
        auth=FakeDirectusAuth(),  # type: ignore[arg-type]
        profiles=_manifest().by_collection,
        transport=transport,
        device_state_path=state_path,
    )


# ---------------------------------------------------------------------------
# Device settings
# ---------------------------------------------------------------------------


def test_device_settings_round_trip(tmp_path: Any) -> None:
    transport = FakeTransport([])
    service = _service(transport, tmp_path / "device.json")
    settings = DeviceSettings(theme=ThemeTokens(mode="dark", accent="#ff0000"))
    service.save_device(settings)
    loaded = service.read_device()
    assert loaded.theme.mode == "dark"
    assert loaded.theme.accent == "#ff0000"


def test_device_settings_recovers_from_corrupt_file(tmp_path: Any) -> None:
    path = tmp_path / "device.json"
    path.write_text("{not valid json", encoding="utf-8")
    transport = FakeTransport([])
    service = _service(transport, path)
    loaded = service.read_device()
    # Corrupt → recover to defaults (never crash).
    assert loaded.schema_version == 1


def test_device_settings_defaults_when_missing(tmp_path: Any) -> None:
    transport = FakeTransport([])
    service = _service(transport, tmp_path / "nonexistent.json")
    loaded = service.read_device()
    assert loaded.theme.mode == "system"


# ---------------------------------------------------------------------------
# Shared settings
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_read_shared_returns_entries() -> None:
    transport = FakeTransport(
        [
            {
                "data": [
                    {"key": "holiday", "value": {"type": "national"}, "date_updated": "2026-07-14"}
                ]
            }
        ]
    )
    service = _service(transport, Path("x"))
    result = await service.read_shared("vibetable_settings", [])
    assert len(result.settings) == 1
    assert result.settings[0].key == "holiday"
    assert result.fresh is True


@pytest.mark.asyncio
async def test_read_shared_offline_returns_empty_not_fake_defaults() -> None:
    from backend.adapters.directus.errors import DirectusTransportError

    transport = FakeTransport([DirectusTransportError("offline", status=503, code="UNAVAILABLE")])
    service = _service(transport, Path("x"))
    result = await service.read_shared("vibetable_settings", [])
    assert result.settings == []
    assert result.fresh is False  # no fake defaults


# ---------------------------------------------------------------------------
# Flows
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_invoke_approved_flow() -> None:
    transport = FakeTransport([{"data": {"ok": True}}])
    service = _service(transport, Path("x"))
    flow_id = APPROVED_FLOWS[0].flow_id
    result = await service.invoke_flow(flow_id, "corr-1", {"collection": "vibetable_demo"})
    assert result.flow_id == flow_id
    assert result.error is None


@pytest.mark.asyncio
async def test_invoke_rejects_unapproved_flow() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="not in the approved manifest"):
        await service.invoke_flow("evil-flow", "corr-1", {})


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------


def test_list_commands_returns_catalog() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    result = service.list_commands()
    ids = {c.command_id for c in result.commands}
    assert "export.query" in ids


def test_run_command_requires_grant_when_needed() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="requires a path grant"):
        service.run_command("export.query", {})


def test_run_command_succeeds_with_grant() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    result = service.run_command("export.query", {}, grant_id="g1")
    assert result.success is True


def test_run_command_rejects_unknown() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="not in the static catalog"):
        service.run_command("evil", {})


# ---------------------------------------------------------------------------
# Shortcuts + Launch Broker
# ---------------------------------------------------------------------------


def test_save_shortcut_validates_command_reference() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="unknown command"):
        service.save_shortcut(
            ShortcutEntry(shortcut_id="s1", target="built-in-command", command_id="evil")
        )


def test_save_shortcut_blocks_disallowed_url_scheme() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="not allowed"):
        service.save_shortcut(
            ShortcutEntry(shortcut_id="s1", target="url", url="javascript:alert(1)")
        )


def test_save_shortcut_accepts_https_url() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    service.save_shortcut(
        ShortcutEntry(shortcut_id="s1", target="url", url="https://example.com", label="Example")
    )
    result = service.list_shortcuts()
    assert len(result.shortcuts) == 1


def test_launch_action_succeeds_for_valid_shortcut() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    service.save_shortcut(ShortcutEntry(shortcut_id="s1", target="url", url="https://example.com"))
    result = service.launch_action("s1")
    assert result.launched is True


def test_launch_action_rejects_unknown_shortcut() -> None:
    transport = FakeTransport([])
    service = _service(transport, Path("x"))
    with pytest.raises(SettingsCommandError, match="not found"):
        service.launch_action("bogus")
