"""C2 collaboration + insights contract fixture tests."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from backend.contracts.collaboration import (
    ActivityResult,
    CommentEntry,
    NotificationEntry,
    RevertPreview,
    RevertResult,
)
from backend.contracts.presets_versions_dashboards import (
    ContentVersionEntry,
    DashboardEntry,
    PanelManifestResult,
    PresetEntry,
    VersionCompareResult,
)

FIXTURES = Path(__file__).parent / "fixtures"


def _load(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


def test_fixture_contract_header() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    assert fixture["contract"] == "table.c2.collaboration.fixtures.v1"


def test_activity_result_round_trip() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    result = ActivityResult.model_validate(fixture["activity"]["result"])
    assert result.revisions[0].user_name == "Ada Lovelace"
    assert result.revisions[0].delta.get("number") == "A-1"


def test_revert_preview_and_result_round_trip() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    preview = RevertPreview.model_validate(fixture["revert"]["preview"])
    assert preview.target_revision == "rev-5"
    assert preview.changes["number"]["after"] == "A-OLD"
    result = RevertResult.model_validate(fixture["revert"]["result"])
    assert result.reverted_to_revision == "rev-5"


def test_comment_entry_round_trip() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    entry = CommentEntry.model_validate(fixture["comment"]["entry"])
    assert entry.comment == "hello"
    assert entry.edited_on is None


def test_notification_entry_round_trip() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    entry = NotificationEntry.model_validate(fixture["notification"]["entry"])
    assert entry.read is False
    assert entry.collection == "vibetable_demo"


def test_preset_entry_round_trip() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    entry = PresetEntry.model_validate(fixture["preset"]["entry"])
    assert entry.scope == "personal"
    assert "number" in entry.view.visible_fields


def test_version_entry_and_compare_round_trip() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    entry = ContentVersionEntry.model_validate(fixture["version"]["entry"])
    assert entry.name == "Draft"
    compare = VersionCompareResult.model_validate(fixture["version"]["compare"])
    assert compare.outdated is True
    assert compare.differences["number"]["version"] == "A-DRAFT"


def test_dashboard_entry_round_trip() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    entry = DashboardEntry.model_validate(fixture["dashboard"]["entry"])
    assert entry.panels[0].type == "metric"
    assert entry.panels[0].position.width == 4


def test_panel_manifest_covers_required_built_in_types() -> None:
    fixture = _load("table-c2-collaboration-contracts.json")
    manifest = PanelManifestResult.model_validate(fixture["dashboard"]["panelManifest"])
    types = {p.type for p in manifest.panels}
    for required in ("label", "metric", "metric-list", "list", "time-series"):
        assert required in types


def test_disabled_features_list_excludes_shares_translations() -> None:
    """Shares/Translations/Share Email/external SSO are explicitly disabled."""
    fixture = _load("table-c2-collaboration-contracts.json")
    disabled = fixture["dashboardFilter"]["disabledFeatures"]
    for feature in ("shares", "translations", "share_email_flow", "external_sso"):
        assert feature in disabled


if __name__ == "__main__":
    pytest.main([__file__, "-q"])
