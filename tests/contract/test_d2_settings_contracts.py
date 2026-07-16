"""D2 settings/command contract fixture tests."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.settings_commands import (
    ApprovedFlowsResult,
    CommandsResult,
    DeviceSettings,
    FlowInvocationResult,
    LaunchActionResult,
    SharedSettingsResult,
    ShortcutEntry,
)

FIXTURES = Path(__file__).parent / "fixtures"


def _load(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def test_fixture_header() -> None:
    assert _load("table-d2-settings-contracts.json")["contract"] == "table.d2.settings.fixtures.v1"


def test_device_settings_round_trip() -> None:
    fixture = _load("table-d2-settings-contracts.json")
    settings = DeviceSettings.model_validate(fixture["deviceSettings"])
    assert settings.theme.mode == "dark"
    assert settings.window_position["x"] == 100


def test_shared_settings_round_trip() -> None:
    fixture = _load("table-d2-settings-contracts.json")
    result = SharedSettingsResult.model_validate(fixture["sharedSettings"]["result"])
    assert len(result.settings) == 1
    assert result.fresh is True


def test_approved_flows_round_trip() -> None:
    fixture = _load("table-d2-settings-contracts.json")
    result = ApprovedFlowsResult.model_validate(fixture["flows"]["approved"])
    assert result.flows[0].flow_id == "vibetable-notify-owner"


def test_flow_invocation_round_trip() -> None:
    fixture = _load("table-d2-settings-contracts.json")
    result = FlowInvocationResult.model_validate(fixture["flows"]["invocation"]["result"])
    assert result.async_acknowledged is False
    assert result.response.get("ok") is True


def test_command_catalog_round_trip() -> None:
    fixture = _load("table-d2-settings-contracts.json")
    result = CommandsResult.model_validate(fixture["commands"]["catalog"])
    assert result.commands[0].command_id == "export.query"
    assert result.commands[0].requires_grant is True


def test_shortcut_and_launch_round_trip() -> None:
    fixture = _load("table-d2-settings-contracts.json")
    entry = ShortcutEntry.model_validate(fixture["shortcuts"]["entry"])
    assert entry.target == "url"
    launch = LaunchActionResult.model_validate(fixture["shortcuts"]["launchResult"])
    assert launch.launched is True


def test_blocked_schemes_excludes_dangerous() -> None:
    """The OS Launch Broker blocks dangerous URL schemes."""
    fixture = _load("table-d2-settings-contracts.json")
    blocked = fixture["shortcuts"]["blockedSchemes"]
    for scheme in ("javascript", "vbscript", "data"):
        assert scheme in blocked


def test_no_desktop_db_backup_and_no_arbitrary_script() -> None:
    """D2 explicitly removes desktop DB backup and arbitrary script execution."""
    fixture = _load("table-d2-settings-contracts.json")
    assert fixture["noDesktopDbBackup"] is True
    assert fixture["noArbitraryScript"] is True


if __name__ == "__main__":
    pytest.main([__file__, "-q"])
