from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from qa import go_coverage, go_test_schedule


def _write_config(path: Path) -> None:
    path.write_text(
        json.dumps(
            {
                "quality": {
                    "go_coverage": {
                        "groups": {
                            "core": {
                                "cover_packages": [
                                    "./internal/schemacore",
                                    "./internal/relatedcomputation",
                                ],
                                "test_packages": [
                                    "./internal/schemacore",
                                    "./internal/relatedcomputation",
                                ],
                                "minimum": {"line": 85, "branch": 75, "diff": 90},
                            },
                            "authority": {
                                "cover_packages": [
                                    "./internal/filehistory",
                                    "./internal/restore",
                                    "./internal/query",
                                    "./internal/mutation",
                                ],
                                "test_packages": ["./..."],
                                "minimum": {"line": 63, "branch": 52, "diff": 90},
                            },
                        }
                    }
                }
            }
        ),
        encoding="utf-8",
    )


def test_main_runs_core_and_reports_the_three_complete_authority_profiles(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    config = tmp_path / "project.json"
    _write_config(config)
    sidecar = tmp_path / "sidecar"
    output = tmp_path / "build/qa/go-coverage/authority"
    output.mkdir(parents=True)
    (output / "coverage.out").write_text("old profile", encoding="utf-8")
    (output / "report.json").write_text("old report", encoding="utf-8")
    block = "github.com/vibetable/vibetable/sidecar/internal/query/query.go:1.1,2.1 1"
    observed: list[str] = []

    def core(
        command: tuple[str, ...],
        cwd: Path,
        evidence: Path,
        label: str,
        *,
        profile: Path | None = None,
    ) -> None:
        observed.append(label)
        assert cwd == sidecar
        assert evidence == tmp_path
        assert command[-2:] == ("./internal/schemacore", "./internal/relatedcomputation")
        assert command[2:5] == (
            "-count=1",
            "-covermode=count",
            "-coverpkg=./internal/schemacore,./internal/relatedcomputation",
        )
        assert profile is not None
        assert not profile.exists()
        profile.write_text("mode: count\n", encoding="utf-8")

    def suite(
        go: str,
        cwd: Path,
        evidence: Path,
        *,
        cover_options: tuple[str, ...],
        profile_dir: Path,
    ) -> tuple[Path, ...]:
        observed.append("authority-suite")
        assert go == "go"
        assert cwd == sidecar
        assert evidence == tmp_path
        assert cover_options == (
            "-covermode=count",
            "-coverpkg=./internal/filehistory,./internal/restore,./internal/query,./internal/mutation",
        )
        assert not (output / "report.json").exists()
        assert (output / "coverage.out").read_text(encoding="utf-8") == "old profile"
        parts = tuple(profile_dir / f"{label}.out" for label in ("app-1", "app-2", "remaining"))
        for index, part in enumerate(parts):
            assert not part.exists()
            part.write_text(f"mode: count\n{block} {index + 1}\n", encoding="utf-8")
        return parts

    def report(command: tuple[str, ...], cwd: Path, evidence: Path, label: str) -> str:
        observed.append(label)
        assert cwd == sidecar
        assert evidence == tmp_path
        assert command[:3] == ("go", "run", "./cmd/go-coverage-report")
        group = command[command.index("--group") + 1]
        profile = Path(command[command.index("--profile") + 1])
        assert command[command.index("--line-min") + 1] == ("85" if group == "core" else "63")
        assert command[command.index("--branch-min") + 1] == ("75" if group == "core" else "52")
        assert command[command.index("--diff-min") + 1] == "90"
        if group == "authority":
            assert profile.read_text(encoding="utf-8") == (
                f"mode: count\n{block} 1\n{block} 2\n{block} 3\n"
            )
            assert [
                command[index + 1] for index, value in enumerate(command) if value == "--scope"
            ] == [
                "sidecar/internal/filehistory",
                "sidecar/internal/restore",
                "sidecar/internal/query",
                "sidecar/internal/mutation",
            ]
        Path(command[command.index("--report") + 1]).write_text(group, encoding="utf-8")
        return ""

    monkeypatch.setattr(go_coverage, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(go_coverage, "SIDECAR", sidecar)
    monkeypatch.setattr(go_test_schedule, "evidence_directory", lambda *_args: tmp_path)
    monkeypatch.setattr(go_test_schedule, "run_test_command", core)
    monkeypatch.setattr(go_test_schedule, "run_full_suite", suite)
    monkeypatch.setattr(go_test_schedule, "run_command", report)

    assert go_coverage.main(["--go", "go", "--config", str(config)]) == 0
    assert observed == ["core-test", "core-report", "authority-suite", "authority-report"]
    assert (output / "coverage.out").read_text(encoding="utf-8") == (
        f"mode: count\n{block} 1\n{block} 2\n{block} 3\n"
    )
    assert (output / "report.json").read_text(encoding="utf-8") == "authority"


@pytest.mark.parametrize(
    "failure", ["app", "remaining", "mode", "missing", "report", "report-missing"]
)
def test_authority_failure_preserves_old_profile_and_invalidates_old_report(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    failure: str,
) -> None:
    output = tmp_path / "authority"
    output.mkdir()
    final = output / "coverage.out"
    final.write_text("old profile", encoding="utf-8")
    (output / "report.json").write_text("old report", encoding="utf-8")
    observed: list[str] = []

    def suite(
        go: str,
        cwd: Path,
        evidence: Path,
        *,
        cover_options: tuple[str, ...],
        profile_dir: Path,
    ) -> tuple[Path, ...]:
        profiles = tuple(profile_dir / f"{part}.out" for part in ("app-1", "app-2", "remaining"))
        for part in profiles:
            observed.append(part.stem)
            if (failure == "app" and part.stem == "app-2") or failure == part.stem:
                raise subprocess.CalledProcessError(1, [go])
            if part.stem != "remaining" or failure != "missing":
                mode = "set" if part.stem == "remaining" and failure == "mode" else "count"
                part.write_text(f"mode: {mode}\n", encoding="utf-8")
        return profiles

    def report(*_args: object) -> str:
        observed.append("report")
        if failure == "report":
            raise subprocess.CalledProcessError(1, ["go"])
        return ""

    group = go_coverage.CoverageGroup("authority", ("./internal/query",), ("./...",), 1, 1, 1)
    monkeypatch.setattr(go_test_schedule, "run_full_suite", suite)
    monkeypatch.setattr(go_test_schedule, "run_command", report)
    error = {
        "app": subprocess.CalledProcessError,
        "remaining": subprocess.CalledProcessError,
        "mode": ValueError,
        "missing": FileNotFoundError,
        "report": subprocess.CalledProcessError,
        "report-missing": ValueError,
    }[failure]
    with pytest.raises(error):
        go_coverage._run_group("go", group, output, tmp_path)

    expected = ["app-1", "app-2"]
    if failure != "app":
        expected.append("remaining")
    if failure.startswith("report"):
        expected.append("report")
    assert observed == expected
    assert final.read_text(encoding="utf-8") == "old profile"
    assert not (output / "report.json").exists()


def test_project_inventory_ratchets_authority_as_an_independent_group() -> None:
    groups = {group.name: group for group in go_coverage.load_groups()}

    assert set(groups) == {"core", "authority"}
    authority = groups["authority"]
    assert authority.cover_packages == (
        "./internal/filehistory",
        "./internal/restore",
        "./internal/query",
        "./internal/mutation",
        "./internal/productrpc",
    )
    assert authority.test_packages == ("./...",)
    assert (
        authority.line_minimum,
        authority.branch_minimum,
        authority.diff_minimum,
    ) == (73, 61, 90)


@pytest.mark.parametrize(
    ("mutation", "message"),
    [
        ("unknown-group-field", "unknown fields"),
        ("overlapping-package", "must not overlap"),
        ("unsafe-package", "invalid Go package"),
        ("trailing-slash", "invalid Go package"),
        ("empty-tests", "test_packages must be a non-empty list"),
        ("missing-threshold", "must declare line, branch, and diff"),
    ],
)
def test_load_groups_fails_closed_on_invalid_inventory(
    tmp_path: Path,
    mutation: str,
    message: str,
) -> None:
    config = tmp_path / "project.json"
    _write_config(config)
    payload = json.loads(config.read_text(encoding="utf-8"))
    groups = payload["quality"]["go_coverage"]["groups"]
    if mutation == "unknown-group-field":
        groups["core"]["exclude_packages"] = ["./internal/legacy"]
    elif mutation == "overlapping-package":
        groups["authority"]["cover_packages"].append("./internal/schemacore")
    elif mutation == "unsafe-package":
        groups["authority"]["cover_packages"][0] = "../outside"
    elif mutation == "trailing-slash":
        groups["authority"]["cover_packages"][0] = "./internal/filehistory/"
    elif mutation == "empty-tests":
        groups["authority"]["test_packages"] = []
    else:
        del groups["authority"]["minimum"]["branch"]
    config.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(ValueError, match=message):
        go_coverage.load_groups(config)
