from __future__ import annotations

import json
from pathlib import Path

import pytest

from qa import release_eligibility


def _identity() -> dict[str, object]:
    return {
        "commit": "c" * 40,
        "artifactHashes": {"sidecar": "a" * 64},
        "sourceHash": "s" * 64,
        "releaseCandidate": {
            "schemaVersion": 1,
            "packageTreeSha256": "p" * 64,
            "packageFileCount": 1,
            "archive": {"sha256": "z" * 64},
        },
    }


def _write_lane_reports(root: Path) -> None:
    identity = _identity()
    for lane, stages in release_eligibility.LANE_STAGES.items():
        payload = {
            "schemaVersion": 2,
            "reportKind": "lane",
            "lane": lane,
            "ok": True,
            "releaseEligible": False,
            "generatedAt": "2026-08-02T00:00:00Z",
            **identity,
            "results": [
                {
                    "stage": stage,
                    "command": ["test"],
                    "returncode": 0,
                    "elapsed": 1.0,
                    "stdout": "",
                    "stderr": "",
                    "cwd": "repo",
                }
                for stage in stages
            ],
        }
        lane_dir = root / lane
        lane_dir.mkdir(parents=True)
        (lane_dir / "report.json").write_text(json.dumps(payload), encoding="utf-8")


@pytest.fixture
def aggregate_identity(monkeypatch):
    identity = _identity()
    monkeypatch.setattr(
        release_eligibility.handoff_gate,
        "load_dependencies",
        lambda: {"requiredGateStages": list(release_eligibility.REQUIRED_STAGES)},
    )
    monkeypatch.setattr(
        release_eligibility.handoff_gate,
        "git_head_sha",
        lambda: identity["commit"],
    )
    monkeypatch.setattr(
        release_eligibility.handoff_gate,
        "artifact_hashes",
        lambda _dependencies: identity["artifactHashes"],
    )
    monkeypatch.setattr(
        release_eligibility.handoff_gate,
        "release_source_hash",
        lambda _dependencies: identity["sourceHash"],
    )
    monkeypatch.setattr(
        release_eligibility.release_candidate,
        "candidate_evidence",
        lambda _root, _archive: identity["releaseCandidate"],
    )
    return identity


def test_lane_allocation_covers_every_required_stage_once() -> None:
    allocated = [stage for stages in release_eligibility.LANE_STAGES.values() for stage in stages]

    assert len(allocated) == len(set(allocated))
    assert set(allocated) == set(release_eligibility.REQUIRED_STAGES)


def test_aggregate_reports_preserves_required_stage_order(
    tmp_path: Path,
    aggregate_identity,
) -> None:
    _write_lane_reports(tmp_path)

    report = release_eligibility.aggregate_reports(
        tmp_path,
        tmp_path / "VibeTable.Next",
        tmp_path / "candidate.zip",
    )

    assert report["reportKind"] == "aggregate"
    assert report["releaseEligible"] is True
    assert [item["stage"] for item in report["results"]] == list(
        release_eligibility.REQUIRED_STAGES
    )
    assert {lane["lane"] for lane in report["lanes"]} == set(release_eligibility.REQUIRED_LANES)


@pytest.mark.parametrize(
    ("mutation", "message"),
    [
        (lambda payload: payload.update(ok=False), "did not complete"),
        (lambda payload: payload.update(commit="d" * 40), "identity mismatch"),
        (lambda payload: payload["results"].pop(), "stage coverage mismatch"),
        (
            lambda payload: payload["results"][0].update(returncode=1),
            "did not complete|failed stage",
        ),
    ],
)
def test_aggregate_reports_rejects_invalid_lane_evidence(
    tmp_path: Path,
    aggregate_identity,
    mutation,
    message: str,
) -> None:
    _write_lane_reports(tmp_path)
    report_path = tmp_path / "core" / "report.json"
    payload = json.loads(report_path.read_text(encoding="utf-8"))
    mutation(payload)
    report_path.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(release_eligibility.EligibilityError, match=message):
        release_eligibility.aggregate_reports(
            tmp_path,
            tmp_path / "VibeTable.Next",
            tmp_path / "candidate.zip",
        )


def test_aggregate_reports_rejects_missing_or_duplicate_lanes(
    tmp_path: Path,
    aggregate_identity,
) -> None:
    _write_lane_reports(tmp_path)
    (tmp_path / "race" / "report.json").unlink()

    with pytest.raises(release_eligibility.EligibilityError, match="expected 4 lane reports"):
        release_eligibility.aggregate_reports(
            tmp_path,
            tmp_path / "VibeTable.Next",
            tmp_path / "candidate.zip",
        )

    core = tmp_path / "core" / "report.json"
    duplicate = tmp_path / "duplicate.json"
    duplicate.write_text(core.read_text(encoding="utf-8"), encoding="utf-8")
    with pytest.raises(release_eligibility.EligibilityError, match="duplicate lane"):
        release_eligibility.aggregate_reports(
            tmp_path,
            tmp_path / "VibeTable.Next",
            tmp_path / "candidate.zip",
        )
