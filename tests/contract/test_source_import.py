"""Go migration history must preserve partial outcomes across the RPC boundary."""

import pytest
from pydantic import ValidationError

from backend.contracts.source_import import SourceImportResult


def result() -> dict[str, object]:
    return {
        "contract": "vibetable.source-import.v1",
        "jobId": "migration-1",
        "provider": "synthetic",
        "containerId": "container-1",
        "sourceName": "合成来源",
        "state": "unknown",
        "stage": "records",
        "created": 400,
        "total": 803,
        "notSubmitted": 3,
        "unknownRecords": 400,
        "unknownBatch": "migration-1-b000007",
        "targets": [
            {"sourceTableId": "a", "tableId": "tbl_a", "name": "迁移 A", "collection": "t_a"}
        ],
        "batches": [],
        "diagnostics": [],
        "startedAt": "2026-10-04T08:00:00Z",
        "sessionEpoch": 7,
        "readWindow": {
            "startedAt": "2026-10-04T07:00:00Z",
            "finishedAt": "2026-10-04T07:01:00Z",
            "consistency": "window",
        },
        "fields": [],
    }


def test_partial_result_preserves_committed_targets_and_unknown_subset() -> None:
    receipt = SourceImportResult.model_validate(result())
    assert (receipt.created, receipt.unknown_records, receipt.not_submitted) == (400, 400, 3)
    assert receipt.targets[0].collection == "t_a"


@pytest.mark.parametrize(
    "change", [{"unknownBatch": ""}, {"notSubmitted": 403}, {"state": "succeeded"}]
)
def test_result_rejects_false_certainty(change: dict[str, object]) -> None:
    with pytest.raises(ValidationError):
        SourceImportResult.model_validate({**result(), **change})


@pytest.mark.parametrize("stage", ["relations", "attachments", "constraints"])
def test_inserted_rows_do_not_mean_all_stages_succeeded(stage: str) -> None:
    with pytest.raises(ValidationError):
        SourceImportResult.model_validate(
            {
                **result(),
                "state": "succeeded",
                "stage": stage,
                "created": 803,
                "unknownRecords": 0,
                "notSubmitted": 0,
                "unknownBatch": "",
                "finishedAt": "2026-10-04T08:01:00Z",
            }
        )
