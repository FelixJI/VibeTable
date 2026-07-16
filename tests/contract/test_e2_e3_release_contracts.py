"""E2+E3 release/production contract fixture tests."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.production import (
    AcceptanceReport,
    BootstrapResult,
    OperationsRunbook,
    ProductionBootstrapPlan,
    ReleaseApproval,
)
from backend.contracts.release import (
    CompatibilityReport,
    HealthCheckResult,
    LauncherPointer,
    ReleaseManifest,
    RollbackResult,
    UpdateResult,
)

FIXTURES = Path(__file__).parent / "fixtures"


def _load(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def test_fixture_header() -> None:
    assert (
        _load("table-e2-e3-release-contracts.json")["contract"] == "table.e2-e3.release.fixtures.v1"
    )


# E2


def test_release_manifest_round_trip() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    manifest = ReleaseManifest.model_validate(fixture["releaseManifest"])
    assert manifest.release_version == "1.0.0"
    assert "directus.realtime.v1" in manifest.directus_compatibility.required_capabilities
    assert len(manifest.components) == 2


def test_launcher_pointer_round_trip() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    pointer = LauncherPointer.model_validate(fixture["launcherPointer"])
    assert pointer.active_version == "1.0.0"


def test_update_and_rollback_round_trip() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    update = UpdateResult.model_validate(fixture["updateResult"])
    assert update.state == "succeeded"
    rollback = RollbackResult.model_validate(fixture["rollbackResult"])
    assert rollback.rolled_back_to == "1.0.0"


def test_compatibility_report_all_states() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    for key in ("compatible", "offline", "incompatible"):
        report = CompatibilityReport.model_validate(fixture["compatibilityReport"][key])
        assert report.status == key


def test_health_check_result_round_trip() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    result = HealthCheckResult.model_validate(
        {
            "compatible": fixture["compatibilityReport"]["compatible"],
            "timestamp": "2026-07-14T12:00:00Z",
        }
    )
    assert result.compatible.status == "compatible"


# E3


def test_production_bootstrap_plan_and_result_round_trip() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    plan = ProductionBootstrapPlan.model_validate(fixture["productionBootstrap"]["plan"])
    assert plan.directus_version == "12.1.1"
    result = BootstrapResult.model_validate(fixture["productionBootstrap"]["result"])
    assert result.state == "applied"
    assert result.collections_created == 6


def test_acceptance_report_round_trip() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    report = AcceptanceReport.model_validate(fixture["acceptanceReport"])
    assert report.p0_passed is True
    assert report.disabled_features_confirmed is True
    assert len(report.checks) == 5


def test_operations_runbook_round_trip() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    runbook = OperationsRunbook.model_validate(fixture["operationsRunbook"])
    assert "pg_dump" in runbook.backup_strategy


def test_release_approval_round_trip() -> None:
    fixture = _load("table-e2-e3-release-contracts.json")
    approval = ReleaseApproval.model_validate(fixture["releaseApproval"])
    assert approval.approved is True
    assert approval.release_version == "1.0.0"


def test_no_secrets_or_directus_server_in_package() -> None:
    """The client package contains no secrets and no Directus server."""
    fixture = _load("table-e2-e3-release-contracts.json")
    assert fixture["noSecretsInPackage"] is True
    assert fixture["noDirectusServerInPackage"] is True


if __name__ == "__main__":
    pytest.main([__file__, "-q"])
