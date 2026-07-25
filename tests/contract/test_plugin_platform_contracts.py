from __future__ import annotations

import json
from pathlib import Path

from backend.contracts.plugin import (
    InteractionSnapshot,
    PluginEventEnvelope,
    PluginManifest,
    PluginResult,
    PluginSafeError,
    PluginTaskSnapshot,
)


def test_plugin_platform_fixture_is_valid_across_public_contracts() -> None:
    fixture = json.loads(
        (Path(__file__).parent / "fixtures" / "plugin-platform-v1.json").read_text(encoding="utf-8")
    )

    assert PluginManifest.model_validate(fixture["manifest"]).plugin_id == "com.example.summary"
    assert PluginResult.model_validate(fixture["result"]).status == "success"
    assert InteractionSnapshot.model_validate(fixture["interaction"]).progress.current == 2
    assert PluginTaskSnapshot.model_validate(fixture["task"]).state == "succeeded"
    assert PluginSafeError.model_validate(fixture["error"]).recoverability == "reconfigure"
    assert PluginEventEnvelope.model_validate(fixture["event"]).revision == 7
