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


def test_host_install_plans_validate_at_the_real_python_commit_boundary() -> None:
    from backend.contracts.plugin_rpc import (
        CommitInstallExecutionParams,
        UpgradePluginExecutionParams,
    )

    fixtures = (
        Path(__file__).resolve().parents[2] / "contracts/v2/fixtures/plugin-install-host-plans.json"
    )
    cases = json.loads(fixtures.read_text(encoding="utf-8"))
    assert {case["name"] for case in cases} == {"legacy", "native"}
    for case in cases:
        params = CommitInstallExecutionParams.model_validate(case["params"])
        assert params.model_dump(mode="json", by_alias=True) == case["params"]
        upgrade = {**case["params"], "pluginId": params.plan.manifest.plugin_id}
        UpgradePluginExecutionParams.model_validate(upgrade)
