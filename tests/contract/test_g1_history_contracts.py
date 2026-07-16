"""G1 history contract fixture tests.

Validates that the Python DTOs in ``backend.contracts.history`` round-trip
the wire fixtures in ``table-g1-history-contracts.json`` using camelCase
aliases. These tests pin the cross-stack wire contract so C# and TS can be
generated to match.
"""

from __future__ import annotations

import json
from pathlib import Path

from backend.contracts.history import (
    ApplyRestoreParams,
    HistoryActor,
    HistoryChangeSet,
    HistoryPage,
    PreviewRestoreParams,
    ReadChangeSetsParams,
    RelationFieldChange,
    RestoreDiagnostic,
    RestorePreview,
    RestoreResult,
    ScalarFieldChange,
)

FIXTURE = Path(__file__).resolve().parent / "fixtures" / "table-g1-history-contracts.json"
DATA = json.loads(FIXTURE.read_text(encoding="utf-8"))


def test_fixture_schema_version_is_g1() -> None:
    assert DATA["schemaVersion"] == "vibetable-1.0"
    assert "content_versions" in DATA["disabledFeatures"]


# ---------------------------------------------------------------------------
# readChangeSets
# ---------------------------------------------------------------------------


def test_read_change_sets_params_round_trip() -> None:
    params = ReadChangeSetsParams.model_validate(DATA["readChangeSets"]["params"])
    assert params.collection == "vibetable_demo"
    assert params.item_id == "c-001"
    assert params.limit == 50
    assert params.offset == 0


def test_read_change_sets_result_round_trip() -> None:
    page = HistoryPage.model_validate(DATA["readChangeSets"]["result"])
    assert page.collection == "vibetable_demo"
    assert page.item_id == "c-001"
    assert len(page.change_sets) == 2
    assert page.total == 2
    assert page.capability_hash == "g1-fixture-capability-hash"
    assert page.schema_revision == "vibetable-1.0"


def test_change_set_has_scalar_and_relation_changes() -> None:
    page = HistoryPage.model_validate(DATA["readChangeSets"]["result"])
    cs = page.change_sets[0]
    assert cs.action == "update"
    assert cs.actor.display_name == "Ada Lovelace"
    assert len(cs.scalar_changes) == 2
    assert len(cs.relation_changes) == 1
    sc = cs.scalar_changes[0]
    assert sc.field == "title"
    assert sc.before == "Old Title"
    assert sc.after == "New Title"
    rc = cs.relation_changes[0]
    assert rc.field == "project"
    assert rc.kind == "m2o"
    assert rc.related_collection == "vibetable_demo"
    assert rc.display_value == "P-002 Alpha Project"


def test_create_action_has_null_before_values() -> None:
    page = HistoryPage.model_validate(DATA["readChangeSets"]["result"])
    cs = page.change_sets[1]
    assert cs.action == "create"
    sc = cs.scalar_changes[0]
    assert sc.before is None
    assert sc.after == "Old Title"


# ---------------------------------------------------------------------------
# previewRestore
# ---------------------------------------------------------------------------


def test_preview_restore_params_round_trip() -> None:
    params = PreviewRestoreParams.model_validate(DATA["previewRestore"]["params"])
    assert params.collection == "vibetable_demo"
    assert params.item_id == "c-001"
    assert params.target_revision == "rev-9"


def test_preview_restore_result_round_trip() -> None:
    preview = RestorePreview.model_validate(DATA["previewRestore"]["result"])
    assert preview.current_hash == "current-hash-abc"
    assert preview.schema_revision == "vibetable-1.0"
    assert len(preview.scalar_changes) == 2
    assert len(preview.relation_changes) == 1
    assert preview.diagnostics == []
    assert preview.token == "restore-token-fixture"


def test_preview_restore_with_diagnostics_round_trip() -> None:
    preview = RestorePreview.model_validate(DATA["previewRestoreWithDiagnostics"]["result"])
    assert len(preview.diagnostics) == 2
    diag = preview.diagnostics[0]
    assert diag.field == "legacy_field"
    assert diag.classification == "schema_retired"
    assert diag.severity == "error"
    assert diag.code == "field_deleted"


# ---------------------------------------------------------------------------
# applyRestore
# ---------------------------------------------------------------------------


def test_apply_restore_params_round_trip() -> None:
    params = ApplyRestoreParams.model_validate(DATA["applyRestore"]["params"])
    assert params.token == "restore-token-fixture"


def test_apply_restore_result_round_trip() -> None:
    result = RestoreResult.model_validate(DATA["applyRestore"]["result"])
    assert result.restored_to_revision == "rev-9"
    assert result.new_revision_id == "rev-11"
    assert result.item["title"] == "Old Title"


# ---------------------------------------------------------------------------
# Serialization round-trip (camelCase wire)
# ---------------------------------------------------------------------------


def test_change_set_serializes_to_camel_case() -> None:
    cs = HistoryChangeSet(
        root_revision_id="rev-1",
        activity_id="act-1",
        action="update",
        timestamp="2026-07-15T10:00:00Z",
        actor=HistoryActor(user_id="u1", display_name="Test"),
        scalar_changes=[ScalarFieldChange(field="title", before="A", after="B")],
        relation_changes=[
            RelationFieldChange(field="project", kind="m2o", related_collection="vibetable_demo")
        ],
    )
    dumped = cs.model_dump(by_alias=True, mode="json")
    assert dumped["rootRevisionId"] == "rev-1"
    assert dumped["activityId"] == "act-1"
    assert dumped["scalarChanges"][0]["field"] == "title"
    assert dumped["relationChanges"][0]["relatedCollection"] == "vibetable_demo"


def test_restore_diagnostic_classifications() -> None:
    for cls in ("recoverable", "readonly_system", "derived", "sensitive", "schema_retired"):
        diag = RestoreDiagnostic(
            field="f", classification=cls, severity="warning", code="c", message="m"
        )
        assert diag.classification == cls
