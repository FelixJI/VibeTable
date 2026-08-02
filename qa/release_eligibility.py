#!/usr/bin/env python3
"""Aggregate immutable-candidate release gate lanes into one eligibility report."""

from __future__ import annotations

import argparse
import json
import sys
from datetime import UTC, datetime
from pathlib import Path

try:
    from qa import handoff as handoff_gate
    from qa import release_candidate
except ModuleNotFoundError:  # pragma: no cover - direct ``python qa/release_eligibility.py``
    import handoff as handoff_gate  # type: ignore[no-redef]
    import release_candidate  # type: ignore[no-redef]

SCHEMA_VERSION = 2
REQUIRED_STAGES = (
    "version",
    "package",
    "go-fmt",
    "go-vet",
    "go-test",
    "go-race",
    "go-build",
    "sidecar-smoke",
    "upgrade-smoke",
    "dev-build",
    "python",
    "contracts",
    "tooling",
    "dotnet",
    "web-test",
    "web-build",
    "fault-injection",
    "product-e2e",
    "smoke",
)
LANE_STAGES = {
    "core": (
        "version",
        "go-fmt",
        "go-vet",
        "go-test",
        "go-build",
        "sidecar-smoke",
        "upgrade-smoke",
        "dev-build",
        "python",
        "contracts",
        "tooling",
        "dotnet",
        "web-test",
        "web-build",
        "smoke",
    ),
    "race": ("go-race",),
    "resilience": ("fault-injection", "product-e2e"),
    "release": ("package",),
}
PARALLEL_LANES = ("core", "race", "resilience")
REQUIRED_LANES = (*PARALLEL_LANES, "release")


class EligibilityError(RuntimeError):
    """Raised when lane evidence cannot prove release eligibility."""


def _read_report(path: Path) -> dict[str, object]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise EligibilityError(f"invalid lane report {path}: {exc}") from exc
    if not isinstance(payload, dict):
        raise EligibilityError(f"lane report must be an object: {path}")
    return payload


def _current_identity(
    package_root: Path,
    package_archive: Path,
) -> tuple[str, dict[str, str], str, dict[str, object]]:
    dependencies = handoff_gate.load_dependencies()
    configured_stages = tuple(dependencies["requiredGateStages"])
    if configured_stages != REQUIRED_STAGES:
        raise EligibilityError("lane stage allocation is out of sync with requiredGateStages")
    return (
        handoff_gate.git_head_sha(),
        handoff_gate.artifact_hashes(dependencies),
        handoff_gate.release_source_hash(dependencies),
        release_candidate.candidate_evidence(package_root, package_archive),
    )


def aggregate_reports(
    reports_dir: Path,
    package_root: Path,
    package_archive: Path,
) -> dict[str, object]:
    paths = sorted(reports_dir.rglob("*.json"))
    if len(paths) != len(REQUIRED_LANES):
        raise EligibilityError(f"expected {len(REQUIRED_LANES)} lane reports, found {len(paths)}")

    commit, artifact_hashes, source_hash, candidate = _current_identity(
        package_root,
        package_archive,
    )
    expected_identity = {
        "commit": commit,
        "artifactHashes": artifact_hashes,
        "sourceHash": source_hash,
        "releaseCandidate": candidate,
    }
    reports: dict[str, dict[str, object]] = {}
    stage_results: dict[str, dict[str, object]] = {}
    lane_summaries: list[dict[str, object]] = []

    for path in paths:
        report = _read_report(path)
        lane = report.get("lane")
        if not isinstance(lane, str) or lane not in LANE_STAGES:
            raise EligibilityError(f"unknown lane in {path}: {lane!r}")
        if lane in reports:
            raise EligibilityError(f"duplicate lane report: {lane}")
        if report.get("schemaVersion") != SCHEMA_VERSION or report.get("reportKind") != "lane":
            raise EligibilityError(f"invalid lane report schema: {lane}")
        if report.get("ok") is not True or report.get("releaseEligible") is not False:
            raise EligibilityError(f"lane did not complete successfully: {lane}")
        for key, value in expected_identity.items():
            if report.get(key) != value:
                raise EligibilityError(f"lane identity mismatch for {lane}: {key}")

        raw_results = report.get("results")
        if not isinstance(raw_results, list):
            raise EligibilityError(f"lane results must be a list: {lane}")
        observed_stages = tuple(
            item.get("stage") if isinstance(item, dict) else None for item in raw_results
        )
        if observed_stages != LANE_STAGES[lane]:
            raise EligibilityError(f"lane stage coverage mismatch: {lane}")
        for item in raw_results:
            assert isinstance(item, dict)  # narrowed by observed_stages validation
            stage = item["stage"]
            if item.get("returncode") != 0:
                raise EligibilityError(f"failed stage in lane {lane}: {stage}")
            if stage in stage_results:
                raise EligibilityError(f"duplicate stage result: {stage}")
            stage_results[stage] = item
        reports[lane] = report
        lane_summaries.append(
            {
                "lane": lane,
                "generatedAt": report.get("generatedAt"),
                "elapsedSeconds": round(
                    sum(float(item.get("elapsed", 0.0)) for item in raw_results),
                    3,
                ),
            }
        )

    if set(reports) != set(REQUIRED_LANES):
        missing = sorted(set(REQUIRED_LANES) - set(reports))
        unknown = sorted(set(reports) - set(REQUIRED_LANES))
        raise EligibilityError(f"lane set mismatch: missing={missing}, unknown={unknown}")
    if set(stage_results) != set(REQUIRED_STAGES):
        missing = sorted(set(REQUIRED_STAGES) - set(stage_results))
        unknown = sorted(set(stage_results) - set(REQUIRED_STAGES))
        raise EligibilityError(f"stage set mismatch: missing={missing}, unknown={unknown}")

    return {
        "schemaVersion": SCHEMA_VERSION,
        "reportKind": "aggregate",
        "ok": True,
        "releaseEligible": True,
        "generatedAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
        "commit": commit,
        "artifactHashes": artifact_hashes,
        "sourceHash": source_hash,
        "releaseCandidate": candidate,
        "lanes": lane_summaries,
        "results": [stage_results[stage] for stage in REQUIRED_STAGES],
    }


def _summary_markdown(report: dict[str, object]) -> str:
    lines = [
        "## Release eligibility lanes",
        "",
        "| Lane | Stages | CPU time |",
        "| --- | ---: | ---: |",
    ]
    for lane in report["lanes"]:
        assert isinstance(lane, dict)
        name = str(lane["lane"])
        lines.append(
            f"| {name} | {len(LANE_STAGES[name])} | {float(lane['elapsedSeconds']):.1f}s |"
        )
    lines.extend(["", "Result: **eligible** (all immutable-candidate lanes passed).", ""])
    return "\n".join(lines)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reports-dir", type=Path, required=True)
    parser.add_argument("--package-root", type=Path, required=True)
    parser.add_argument("--package-archive", type=Path, required=True)
    parser.add_argument("--json-report", type=Path, required=True)
    parser.add_argument("--github-summary", type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        report = aggregate_reports(args.reports_dir, args.package_root, args.package_archive)
    except (EligibilityError, release_candidate.CandidateError, OSError, ValueError) as exc:
        print(f"release eligibility aggregation failed: {exc}", file=sys.stderr)
        return 1
    args.json_report.parent.mkdir(parents=True, exist_ok=True)
    args.json_report.write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    if args.github_summary:
        args.github_summary.parent.mkdir(parents=True, exist_ok=True)
        with args.github_summary.open("a", encoding="utf-8") as summary:
            summary.write(_summary_markdown(report))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
