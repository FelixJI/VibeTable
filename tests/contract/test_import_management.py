"""Import history must not manufacture certainty or accept workspace authority."""

import pytest
from pydantic import ValidationError

from backend.contracts.import_management import ImportHistoryEntry, ImportHistoryParams


def unknown_receipt() -> dict[str, object]:
    return {
        "taskId": "task-controlled",
        "collection": "orders",
        "sourceType": "csv",
        "sourceName": "sample.csv",
        "state": "interrupted",
        "commitState": "unknown",
        "createdCount": None,
        "updatedCount": None,
        "startedAt": "2026-10-04T02:00:00Z",
        "finishedAt": None,
        "sessionEpoch": 1,
        "errorCode": "import.interrupted",
    }


def test_interrupted_receipt_preserves_unknown_counts() -> None:
    receipt = ImportHistoryEntry.model_validate(unknown_receipt())
    assert receipt.created_count is None
    assert receipt.updated_count is None


@pytest.mark.parametrize(
    "change",
    [
        {"createdCount": 0},
        {"updatedCount": 0},
        {"state": "succeeded"},
        {"commitState": "committed"},
    ],
)
def test_receipt_rejects_false_commit_certainty(change: dict[str, object]) -> None:
    with pytest.raises(ValidationError):
        ImportHistoryEntry.model_validate({**unknown_receipt(), **change})


def test_committed_receipt_keeps_actual_zero_and_nonzero_counts() -> None:
    receipt = ImportHistoryEntry.model_validate(
        {
            **unknown_receipt(),
            "state": "succeeded",
            "commitState": "committed",
            "createdCount": 3,
            "updatedCount": 0,
            "finishedAt": "2026-10-04T02:00:01Z",
            "errorCode": None,
        }
    )
    assert (receipt.created_count, receipt.updated_count) == (3, 0)


@pytest.mark.parametrize("field", ["workspaceId", "sessionEpoch", "token", "accessToken"])
def test_history_cannot_take_renderer_authority_or_credentials(field: str) -> None:
    with pytest.raises(ValidationError):
        ImportHistoryParams.model_validate({field: "untrusted"})
