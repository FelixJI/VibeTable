from __future__ import annotations

import json
import subprocess
import sys
from collections import Counter
from pathlib import Path

import pytest

from qa import release_eligibility
from qa.product_scenario_manifest import Scenario, load_scenarios


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
                    "command": (
                        [
                            "test",
                            *(
                                part
                                for scenario_id in release_eligibility.product_e2e_partition(
                                    load_scenarios(), stage
                                )
                                for part in ("--scenario", scenario_id)
                            ),
                        ]
                        if stage in {"product-e2e", "product-e2e-data-io", "product-e2e-file-io"}
                        else ["test"]
                    ),
                    "returncode": 0,
                    "elapsed": 1.0,
                    "stdout": "",
                    "stderr": "",
                    "cwd": "repo",
                    "webview2_evidence": "required-passed" if stage == "smoke" else None,
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


def test_lane_allocation_covers_every_required_stage_with_four_race_shards() -> None:
    allocated = [stage for stages in release_eligibility.LANE_STAGES.values() for stage in stages]
    counts = Counter(allocated)

    assert set(allocated) == set(release_eligibility.REQUIRED_STAGES)
    assert counts["go-race"] == 4
    assert all(count == 1 for stage, count in counts.items() if stage != "go-race")


def test_product_e2e_partitions_exactly_cover_the_live_manifest() -> None:
    scenarios = load_scenarios()
    extra = Scenario("36-future-product-case", "future", "future", ("future.case",))
    expanded = [*scenarios, extra]

    standard = release_eligibility.product_e2e_partition(expanded, "product-e2e")
    data_io = release_eligibility.product_e2e_partition(expanded, "product-e2e-data-io")
    file_io = release_eligibility.product_e2e_partition(expanded, "product-e2e-file-io")

    assert set(standard).isdisjoint(data_io)
    assert set(standard).isdisjoint(file_io)
    assert set(data_io).isdisjoint(file_io)
    assert set(standard) | set(data_io) | set(file_io) == {item.id for item in expanded}
    assert extra.id in standard
    assert set(data_io) == {
        "02-all-field-schema",
        "05-formula-lifecycle",
        "06-relation-fanout",
        "23-directory-replica-recovery",
        "24-directory-replica-conflict",
        "26-lookup-definition-read",
        "34-relation-lookup-data-io",
        "35-data-io-interoperability",
        "37-collection-formula-journey",
        "38-calculation-chain-journey",
    }
    assert set(file_io) == {
        "39-file-workflow-combination",
        "40-file-history-capacity",
        "41-file-document-operations",
        "42-file-document-native-operations",
        "43-file-revision-leaves",
        "44-file-restore-crash",
        "45-common-field-display",
        "48-plugin-applications",
        "49-plugin-application-security",
        "50-plugin-headless-budget",
        "51-plugin-application-commit-unknown",
        "52-plugin-capability-services",
        "53-record-manager",
        "54-plugin-models",
    }


@pytest.mark.parametrize(
    ("missing_id", "label"),
    [
        ("35-data-io-interoperability", "data IO"),
        ("40-file-history-capacity", "file IO"),
    ],
)
def test_product_e2e_partition_rejects_a_missing_declared_lane_scenario(
    missing_id: str,
    label: str,
) -> None:
    scenarios = [item for item in load_scenarios() if item.id != missing_id]

    with pytest.raises(release_eligibility.EligibilityError, match=f"missing {label} scenario"):
        release_eligibility.product_e2e_partition(scenarios, "product-e2e")


def test_product_e2e_partition_rejects_overlapping_lane_scenarios(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        release_eligibility,
        "LANE_STAGE_SCENARIO_IDS",
        {
            **release_eligibility.LANE_STAGE_SCENARIO_IDS,
            "product-e2e-file-io": (
                *release_eligibility.FILE_IO_SCENARIO_IDS,
                "35-data-io-interoperability",
            ),
        },
    )

    with pytest.raises(release_eligibility.EligibilityError, match="overlaps"):
        release_eligibility.product_e2e_partition(load_scenarios(), "product-e2e-file-io")


def test_aggregate_rejects_missing_product_e2e_scenario_selection(
    tmp_path: Path,
    aggregate_identity,
) -> None:
    _write_lane_reports(tmp_path)
    report_path = tmp_path / "data-io" / "report.json"
    payload = json.loads(report_path.read_text(encoding="utf-8"))
    payload["results"][0]["command"] = ["test", "--scenario", "34-relation-lookup-data-io"]
    report_path.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(release_eligibility.EligibilityError, match="scenario coverage mismatch"):
        release_eligibility.aggregate_reports(
            tmp_path,
            tmp_path / "VibeTable.Next",
            tmp_path / "candidate.zip",
        )


def test_aggregate_rejects_missing_data_io_lane(
    tmp_path: Path,
    aggregate_identity,
) -> None:
    _write_lane_reports(tmp_path)
    (tmp_path / "data-io" / "report.json").unlink()

    with pytest.raises(release_eligibility.EligibilityError, match="expected 9 lane reports"):
        release_eligibility.aggregate_reports(
            tmp_path,
            tmp_path / "VibeTable.Next",
            tmp_path / "candidate.zip",
        )


def test_runtime_baseline_is_allocated_once_to_resilience_lane() -> None:
    allocated = [
        lane
        for lane, stages in release_eligibility.LANE_STAGES.items()
        if "runtime-baseline" in stages
    ]

    assert allocated == ["resilience"]
    assert release_eligibility.LANE_STAGES["resilience"].index(
        "product-e2e"
    ) < release_eligibility.LANE_STAGES["resilience"].index("runtime-baseline")


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


@pytest.mark.parametrize("evidence", [None, "required-skipped", "not-required"])
def test_aggregate_rejects_missing_or_skipped_required_webview2_evidence(
    tmp_path: Path,
    aggregate_identity,
    evidence: str | None,
) -> None:
    _write_lane_reports(tmp_path)
    report_path = tmp_path / "core" / "report.json"
    payload = json.loads(report_path.read_text(encoding="utf-8"))
    smoke = next(item for item in payload["results"] if item["stage"] == "smoke")
    if evidence is None:
        smoke.pop("webview2_evidence", None)
    else:
        smoke["webview2_evidence"] = evidence
    report_path.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(release_eligibility.EligibilityError, match="WebView2 evidence"):
        release_eligibility.aggregate_reports(
            tmp_path,
            tmp_path / "VibeTable.Next",
            tmp_path / "candidate.zip",
        )


def test_aggregate_combines_all_four_successful_race_shards(
    tmp_path: Path,
    aggregate_identity,
) -> None:
    _write_lane_reports(tmp_path)
    for lane, elapsed in (("race-a", 7.0), ("race-b", 11.0), ("race-c", 5.0), ("race-d", 9.0)):
        path = tmp_path / lane / "report.json"
        payload = json.loads(path.read_text(encoding="utf-8"))
        payload["results"][0].update(elapsed=elapsed, stdout=f"{lane} passed")
        path.write_text(json.dumps(payload), encoding="utf-8")

    report = release_eligibility.aggregate_reports(
        tmp_path,
        tmp_path / "VibeTable.Next",
        tmp_path / "candidate.zip",
    )

    race = next(item for item in report["results"] if item["stage"] == "go-race")
    assert race["command"] == ["parallel race shards", "race-a", "race-b", "race-c", "race-d"]
    assert race["elapsed"] == 11.0
    assert "[race-a]\nrace-a passed" in race["stdout"]
    for lane in release_eligibility.RACE_LANES:
        assert f"[{lane}]\n{lane} passed" in race["stdout"]


@pytest.mark.parametrize("lane", release_eligibility.RACE_LANES)
def test_aggregate_rejects_a_failed_race_shard(
    lane: str,
    tmp_path: Path,
    aggregate_identity,
) -> None:
    _write_lane_reports(tmp_path)
    path = tmp_path / lane / "report.json"
    payload = json.loads(path.read_text(encoding="utf-8"))
    payload["results"][0]["returncode"] = 1
    path.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(release_eligibility.EligibilityError, match=r"failed stage.*go-race"):
        release_eligibility.aggregate_reports(
            tmp_path,
            tmp_path / "VibeTable.Next",
            tmp_path / "candidate.zip",
        )


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
    (tmp_path / "race-b" / "report.json").unlink()

    with pytest.raises(release_eligibility.EligibilityError, match="expected 9 lane reports"):
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


@pytest.mark.parametrize("entrypoint", ["qa/next.py", "qa/release_eligibility.py"])
def test_release_entrypoints_start_without_site_packages(entrypoint: str) -> None:
    completed = subprocess.run(
        [sys.executable, "-S", entrypoint, "--help"],
        cwd=Path(__file__).resolve().parents[1],
        capture_output=True,
        text=True,
        timeout=30,
        check=False,
    )
    assert completed.returncode == 0, completed.stderr
    assert "usage:" in completed.stdout
