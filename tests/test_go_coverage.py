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


def _coverage_path(command: list[str]) -> Path:
    return Path(
        next(
            arg.removeprefix("-coverprofile=")
            for arg in command
            if arg.startswith("-coverprofile=")
        )
    )


def test_main_runs_disjoint_core_and_authority_groups_from_project_config(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    config = tmp_path / "project.json"
    _write_config(config)
    repository_root = tmp_path / "repository"
    sidecar = repository_root / "sidecar"
    sidecar.mkdir(parents=True)
    evidence_root = repository_root / "build/qa/go-coverage"
    for group in ("core", "authority"):
        stale = evidence_root / group
        stale.mkdir(parents=True)
        (stale / "coverage.out").write_text("stale", encoding="utf-8")
        (stale / "report.json").write_text("stale", encoding="utf-8")

    app = go_test_schedule.APP_PACKAGE
    migrations = f"{go_test_schedule.SIDECAR_PACKAGE}/migrations"
    integration = f"{go_test_schedule.SIDECAR_PACKAGE}/tests/integration"
    covered_block = "github.com/vibetable/vibetable/sidecar/internal/query/query.go:1.1,2.1 1"
    observed: list[tuple[list[str], Path]] = []

    def fake_run(
        command: list[str],
        *,
        cwd: Path,
        check: bool,
        stdout: int | None = None,
        encoding: str | None = None,
    ) -> subprocess.CompletedProcess[str]:
        assert check is True
        observed.append((command, Path(cwd)))
        if command[1] == "list":
            assert command == ["go", "list", "./..."]
            assert stdout == subprocess.PIPE
            assert encoding == "utf-8"
            return subprocess.CompletedProcess(
                command, 0, stdout=f"{app}\n{migrations}\n{integration}\n"
            )
        if command[1] == "test":
            profile = _coverage_path(command)
            assert not profile.exists()
            if profile.name == "coverage.out":
                assert not (profile.parent / "report.json").exists()
                profile.write_text("mode: count\n", encoding="utf-8")
            else:
                group_root = profile.parent.parent
                assert not (group_root / "coverage.out").exists()
                assert not (group_root / "report.json").exists()
                count = 2 if profile.name == "app.out" else 3
                profile.write_text(f"mode: count\n{covered_block} {count}\n", encoding="utf-8")
        if command[1] == "run" and "authority" in command:
            profile = Path(command[command.index("--profile") + 1])
            assert (
                profile.read_text(encoding="utf-8")
                == f"mode: count\n{covered_block} 2\n{covered_block} 3\n"
            )
        return subprocess.CompletedProcess(command, 0)

    monkeypatch.setattr(go_coverage, "REPO_ROOT", repository_root)
    monkeypatch.setattr(go_coverage, "SIDECAR", sidecar)
    monkeypatch.setattr(go_coverage.subprocess, "run", fake_run)

    assert go_coverage.main(["--go", "go", "--config", str(config)]) == 0

    assert [command[1] for command, _cwd in observed] == [
        "test",
        "run",
        "list",
        "test",
        "test",
        "run",
    ]
    test_commands = [command for command, _cwd in observed if command[1] == "test"]
    report_commands = [command for command, _cwd in observed if command[1] == "run"]
    assert all(cwd == sidecar for _command, cwd in observed)

    core_test, app_test, remaining_test = test_commands
    assert core_test[-2:] == ["./internal/schemacore", "./internal/relatedcomputation"]
    assert app_test[6:] == [app]
    assert remaining_test[6:] == [migrations, integration]
    assert core_test[4] == ("-coverpkg=./internal/schemacore,./internal/relatedcomputation")
    expected_options = [
        "-count=1",
        "-covermode=count",
        "-coverpkg=./internal/filehistory,./internal/restore,./internal/query,./internal/mutation",
    ]
    assert app_test[2:5] == remaining_test[2:5] == expected_options
    assert len({_coverage_path(command) for command in test_commands}) == 3

    core_report, authority_report = report_commands
    assert core_report[core_report.index("--group") + 1] == "core"
    assert authority_report[authority_report.index("--group") + 1] == "authority"
    assert core_report[core_report.index("--line-min") + 1] == "85"
    assert authority_report[authority_report.index("--branch-min") + 1] == "52"
    assert authority_report[authority_report.index("--diff-min") + 1] == "90"
    assert [
        authority_report[index + 1]
        for index, value in enumerate(authority_report)
        if value == "--scope"
    ] == [
        "sidecar/internal/filehistory",
        "sidecar/internal/restore",
        "sidecar/internal/query",
        "sidecar/internal/mutation",
    ]


@pytest.mark.parametrize("failure", ["app", "remaining", "mode", "missing"])
def test_full_coverage_rejects_failed_batches_and_invalid_profiles(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, failure: str
) -> None:
    app = go_test_schedule.APP_PACKAGE
    peer = f"{go_test_schedule.SIDECAR_PACKAGE}/migrations"
    final = tmp_path / "coverage.out"
    observed: list[str] = []

    def fake_run(
        command: list[str], *, cwd: Path, check: bool, **_kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        assert cwd == tmp_path
        assert check
        if command[1] == "list":
            return subprocess.CompletedProcess(command, 0, stdout=f"{app}\n{peer}\n")
        part = _coverage_path(command)
        observed.append(part.stem)
        if part.stem == failure:
            raise subprocess.CalledProcessError(1, command)
        if part.stem != "remaining" or failure != "missing":
            mode = "set" if part.stem == "remaining" and failure == "mode" else "count"
            part.write_text(f"mode: {mode}\n", encoding="utf-8")
        return subprocess.CompletedProcess(command, 0)

    monkeypatch.setattr(go_coverage, "SIDECAR", tmp_path)
    monkeypatch.setattr(go_coverage.subprocess, "run", fake_run)
    error = {
        "app": subprocess.CalledProcessError,
        "remaining": subprocess.CalledProcessError,
        "mode": ValueError,
        "missing": FileNotFoundError,
    }[failure]

    with pytest.raises(error):
        go_coverage._run_full_coverage("go", ("-covermode=count",), final)

    assert observed == (["app"] if failure == "app" else ["app", "remaining"])
    assert not final.exists()


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
