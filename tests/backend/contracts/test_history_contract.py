"""G1 full-field history contracts edge-case coverage.

Validates the camelCase wire shape, ``model_post_init`` scope invariants,
closed ``Literal`` vocabularies, and the length/range boundaries declared on
the history restore + change-set contracts. Mirrors the inline-payload style
used by the other contract tests (no JSON fixtures exist for history yet).
"""

from __future__ import annotations

import pytest
from pydantic import ValidationError

from backend.contracts.history import (
    ApplyRestoreParams,
    HistoryActor,
    HistoryChangeSet,
    HistoryPage,
    HistoryRecordChange,
    PreviewRestoreParams,
    ReadChangeSetsParams,
    RelationFieldChange,
    RestoreClassification,
    RestoreDiagnostic,
    RestorePreview,
    RestoreResult,
    ScalarFieldChange,
)

# ---------------------------------------------------------------------------
# Happy-path construction + camelCase round-trip
# ---------------------------------------------------------------------------


def test_history_actor_round_trips_with_defaults() -> None:
    actor = HistoryActor(user_id="u1", display_name="Ada")
    assert actor.model_dump(mode="json", by_alias=True) == {
        "userId": "u1",
        "displayName": "Ada",
    }
    # Both fields are optional; a bare construct is valid and serializes.
    bare = HistoryActor()
    assert bare.user_id is None
    assert bare.display_name is None


def test_scalar_field_change_round_trips_camel_case() -> None:
    change = ScalarFieldChange(field="amount", before=1, after=2)
    assert change.model_dump(mode="json", by_alias=True) == {
        "field": "amount",
        "before": 1,
        "after": 2,
    }


def test_relation_field_change_round_trips_with_kind_and_targets() -> None:
    change = RelationFieldChange(
        field="owner",
        kind="m2o",
        related_collection="users",
        related_item_id="u1",
        display_value="Ada",
        before_item_id="u0",
        after_item_id="u1",
        before_display_value="Bo",
        after_display_value="Ada",
        target_available=False,
    )
    dumped = change.model_dump(mode="json", by_alias=True)
    assert dumped["kind"] == "m2o"
    assert dumped["relatedCollection"] == "users"
    assert dumped["targetAvailable"] is False


def test_history_change_set_round_trips_and_counts_changes() -> None:
    change_set = HistoryChangeSet(
        root_revision_id="rev-1",
        change_set_id="cs-1",
        activity_id="act-1",
        action="update",
        timestamp="2026-01-01T00:00:00Z",
        actor=HistoryActor(user_id="u1", display_name="Ada"),
        item_id="row-1",
        record_label="Row 1",
        revision_ids=["rev-1", "rev-2"],
        affected_records=2,
        record_changes=[
            HistoryRecordChange(
                revision_id="rev-1",
                item_id="row-1",
                record_label="Row 1",
                action="update",
                scalar_changes=[ScalarFieldChange(field="name", before="a", after="b")],
                relation_changes=[RelationFieldChange(field="owner", after_item_id="u1")],
            ),
        ],
    )
    dumped = change_set.model_dump(mode="json", by_alias=True)
    assert dumped["rootRevisionId"] == "rev-1"
    assert dumped["recordChanges"][0]["scalarChanges"][0]["field"] == "name"
    # total_changes aggregates scalar + relation changes on the change set itself.
    assert (
        HistoryChangeSet(
            root_revision_id="rev-1",
            action="update",
            timestamp="t",
            scalar_changes=[ScalarFieldChange(field="a")],
            relation_changes=[RelationFieldChange(field="b"), RelationFieldChange(field="c")],
        ).total_changes
        == 3
    )


def test_history_page_round_trips_with_archived_defaults() -> None:
    page = HistoryPage(
        collection="orders",
        scope="row",
        item_id="row-1",
        change_sets=[
            HistoryChangeSet(root_revision_id="rev-1", action="update", timestamp="t"),
        ],
        total=1,
        capability_hash="hash-1",
        schema_revision="schema-1",
        has_more=True,
        archived_default_revision_ids={"drafts": "rev-draft"},
    )
    dumped = page.model_dump(mode="json", by_alias=True)
    assert dumped["archivedDefaultRevisionIds"] == {"drafts": "rev-draft"}
    assert dumped["changeSets"][0]["rootRevisionId"] == "rev-1"


def test_restore_diagnostic_round_trips_all_severities() -> None:
    diag = RestoreDiagnostic(
        field="status",
        classification="readonly_system",
        severity="error",
        code="readonly_system",
        message="field is read-only",
    )
    assert diag.model_dump(mode="json", by_alias=True)["classification"] == "readonly_system"


def test_restore_preview_round_trips_with_diagnostics() -> None:
    preview = RestorePreview(
        collection="orders",
        item_id="row-1",
        target_revision="rev-1",
        scope="row",
        current_hash="hash-1",
        schema_revision="schema-1",
        scalar_changes=[ScalarFieldChange(field="name", before="a", after="b")],
        relation_changes=[RelationFieldChange(field="owner")],
        diagnostics=[
            RestoreDiagnostic(
                field="status",
                code="derived",
                message="derived field",
            )
        ],
        can_apply=True,
        restorable_fields=["name"],
        token="restore-token",
        expires_at="2026-01-02T00:00:00Z",
    )
    dumped = preview.model_dump(mode="json", by_alias=True)
    assert dumped["canApply"] is True
    assert dumped["restorableFields"] == ["name"]


def test_restore_result_round_trips_with_item() -> None:
    result = RestoreResult(
        collection="orders",
        item_id="row-1",
        restored_to_revision="rev-1",
        new_revision_id="rev-2",
        item={"id": "row-1"},
    )
    assert result.model_dump(mode="json", by_alias=True)["newRevisionId"] == "rev-2"


# ---------------------------------------------------------------------------
# ReadChangeSetsParams model_post_init invariants
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("payload", "match"),
    [
        (
            {"collection": "orders", "scope": "row"},
            "itemId is required for row history",
        ),
        (
            {"collection": "orders", "scope": "cell", "itemId": "row-1"},
            "field is required for cell history",
        ),
        (
            {"collection": "orders", "scope": "cell"},
            "itemId is required for cell history",
        ),
    ],
)
def test_read_change_sets_rejects_scope_without_required_fields(
    payload: dict[str, object], match: str
) -> None:
    with pytest.raises(ValidationError, match=match):
        ReadChangeSetsParams.model_validate(payload)


def test_read_change_sets_accepts_table_and_archived_scopes_without_item() -> None:
    for scope in ("table", "archived"):
        params = ReadChangeSetsParams.model_validate({"collection": "orders", "scope": scope})
        assert params.scope == scope
        assert params.item_id is None


def test_read_change_sets_accepts_full_cell_scope() -> None:
    params = ReadChangeSetsParams.model_validate(
        {"collection": "orders", "scope": "cell", "itemId": "row-1", "field": "name"}
    )
    assert params.scope == "cell"
    assert params.field == "name"


def test_read_change_sets_defaults_and_limit_bounds() -> None:
    params = ReadChangeSetsParams(collection="orders", item_id="row-1")
    assert params.limit == 50
    assert params.offset == 0
    assert params.actions == []
    assert params.date_from is None
    assert params.date_to is None


# ---------------------------------------------------------------------------
# PreviewRestoreParams / ApplyRestoreParams model_post_init invariants
# ---------------------------------------------------------------------------


def test_preview_restore_params_requires_field_for_cell_scope() -> None:
    with pytest.raises(ValidationError, match="field is required for cell restore"):
        PreviewRestoreParams.model_validate(
            {
                "collection": "orders",
                "itemId": "row-1",
                "targetRevision": "rev-1",
                "scope": "cell",
            }
        )


def test_preview_restore_params_accepts_cell_scope_with_field() -> None:
    params = PreviewRestoreParams.model_validate(
        {
            "collection": "orders",
            "itemId": "row-1",
            "targetRevision": "rev-1",
            "scope": "cell",
            "field": "name",
        }
    )
    assert params.scope == "cell"
    assert params.field == "name"


def test_preview_restore_params_accepts_row_scope_default() -> None:
    params = PreviewRestoreParams(collection="orders", item_id="row-1", target_revision="rev-1")
    assert params.scope == "row"


def test_apply_restore_params_round_trips() -> None:
    params = ApplyRestoreParams(collection="orders", item_id="row-1", token="restore-token")
    dumped = params.model_dump(mode="json", by_alias=True)
    assert dumped["token"] == "restore-token"


# ---------------------------------------------------------------------------
# extra="forbid" rejection
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "model",
    [
        HistoryActor,
        ScalarFieldChange,
        RelationFieldChange,
        HistoryChangeSet,
        ReadChangeSetsParams,
        PreviewRestoreParams,
        ApplyRestoreParams,
        RestoreResult,
    ],
)
def test_history_models_reject_unknown_keys(model: type) -> None:
    # Build a minimal valid payload per model, then inject an unknown key.
    if model is HistoryActor:
        payload: dict[str, object] = {"userId": "u1"}
    elif model is ScalarFieldChange:
        payload = {"field": "name"}
    elif model is RelationFieldChange:
        payload = {"field": "owner"}
    elif model is HistoryChangeSet:
        payload = {
            "rootRevisionId": "rev-1",
            "action": "update",
            "timestamp": "t",
        }
    elif model is ReadChangeSetsParams:
        payload = {"collection": "orders", "scope": "table"}
    elif model is PreviewRestoreParams:
        payload = {
            "collection": "orders",
            "itemId": "row-1",
            "targetRevision": "rev-1",
        }
    elif model is ApplyRestoreParams:
        payload = {
            "collection": "orders",
            "itemId": "row-1",
            "token": "tok",
        }
    else:  # RestoreResult
        payload = {
            "collection": "orders",
            "itemId": "row-1",
            "restoredToRevision": "rev-1",
        }
    payload["unexpected"] = True
    with pytest.raises(ValidationError):
        model.model_validate(payload)


# ---------------------------------------------------------------------------
# Closed Literal vocabulary rejection
# ---------------------------------------------------------------------------


def test_history_scope_literal_rejects_unknown_value() -> None:
    with pytest.raises(ValidationError):
        ReadChangeSetsParams.model_validate({"collection": "orders", "scope": "column"})


def test_restore_scope_literal_rejects_table() -> None:
    with pytest.raises(ValidationError):
        PreviewRestoreParams.model_validate(
            {
                "collection": "orders",
                "itemId": "row-1",
                "targetRevision": "rev-1",
                "scope": "table",
            }
        )


def test_relation_field_change_kind_literal_rejects_unknown() -> None:
    with pytest.raises(ValidationError):
        RelationFieldChange(field="owner", kind="h2o")  # type: ignore[arg-type]


def test_restore_diagnostic_severity_literal_rejects_unknown() -> None:
    with pytest.raises(ValidationError):
        RestoreDiagnostic(field="f", code="c", message="m", severity="fatal")  # type: ignore[arg-type]


@pytest.mark.parametrize(
    "classification",
    [
        "recoverable",
        "readonly_system",
        "derived",
        "sensitive",
        "schema_retired",
        "permission_denied",
        "incompatible",
        "relation_unsafe",
    ],
)
def test_restore_diagnostic_accepts_all_classifications(
    classification: RestoreClassification,
) -> None:
    diag = RestoreDiagnostic(field="f", classification=classification, code="c", message="m")
    assert diag.classification == classification


def test_restore_diagnostic_rejects_unknown_classification() -> None:
    with pytest.raises(ValidationError):
        RestoreDiagnostic(field="f", classification="magic", code="c", message="m")  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# Length / range boundaries
# ---------------------------------------------------------------------------


def test_change_set_revision_ids_enforces_max_length() -> None:
    with pytest.raises(ValidationError):
        HistoryChangeSet(
            root_revision_id="rev-1",
            action="update",
            timestamp="t",
            revision_ids=[f"rev-{i}" for i in range(1001)],
        )


def test_change_set_affected_records_enforces_minimum() -> None:
    with pytest.raises(ValidationError):
        HistoryChangeSet(
            root_revision_id="rev-1",
            action="update",
            timestamp="t",
            affected_records=0,
        )


def test_read_change_sets_limit_bounds() -> None:
    with pytest.raises(ValidationError):
        ReadChangeSetsParams(collection="orders", item_id="r", limit=0)
    with pytest.raises(ValidationError):
        ReadChangeSetsParams(collection="orders", item_id="r", limit=101)


def test_read_change_sets_offset_bounds() -> None:
    with pytest.raises(ValidationError):
        ReadChangeSetsParams(collection="orders", item_id="r", offset=-1)


def test_read_change_sets_actions_enforces_max_length() -> None:
    with pytest.raises(ValidationError):
        ReadChangeSetsParams(collection="orders", item_id="r", actions=[f"a{i}" for i in range(17)])


def test_restore_preview_token_enforces_max_length() -> None:
    with pytest.raises(ValidationError):
        RestorePreview(
            collection="orders",
            item_id="row-1",
            target_revision="rev-1",
            current_hash="h",
            schema_revision="s",
            token="x" * 2049,
        )


def test_history_record_change_round_trips() -> None:
    change = HistoryRecordChange(
        revision_id="rev-1",
        item_id="row-1",
        action="update",
        record_label="Row 1",
    )
    dumped = change.model_dump(mode="json", by_alias=True)
    assert dumped["recordLabel"] == "Row 1"
    assert dumped["scalarChanges"] == []
    assert dumped["relationChanges"] == []


def test_restore_result_minimal_round_trips() -> None:
    result = RestoreResult(collection="orders", item_id="row-1", restored_to_revision="rev-1")
    assert result.new_revision_id is None
    assert result.item == {}
