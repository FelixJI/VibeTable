from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path

import pytest

from scripts import automation_project, toolchain_metadata, windows_doctor
from scripts.node_toolchain import NODE_DISTRIBUTION
from scripts.toolchain_metadata import W64DEVKIT_DISTRIBUTION
from scripts.windows_doctor import (
    CommandResult,
    DoctorCheck,
    DoctorProfile,
    DoctorReport,
    PlatformSnapshot,
    SystemAdapter,
    diagnose_windows_toolchain,
    read_go_module_version,
    render_report,
)

SOURCE_ROOT = Path(__file__).resolve().parents[2]


@dataclass
class MemoryAdapter:
    platform_snapshot: PlatformSnapshot
    executables: dict[str, str | None]
    results: dict[tuple[str, ...], CommandResult]
    files: dict[Path, str] = field(default_factory=dict)
    which_calls: list[str] = field(default_factory=list)
    run_calls: list[tuple[tuple[str, ...], Path, dict[str, str] | None]] = field(
        default_factory=list
    )
    read_calls: list[Path] = field(default_factory=list)

    def platform(self) -> PlatformSnapshot:
        return self.platform_snapshot

    def which(self, executable: str) -> str | None:
        self.which_calls.append(executable)
        return self.executables.get(executable)

    def run(
        self,
        command: tuple[str, ...],
        *,
        cwd: Path,
        env: dict[str, str] | None = None,
    ) -> CommandResult:
        self.run_calls.append((command, cwd, env))
        return self.results.get(command, CommandResult(returncode=1, stdout="", stderr="missing"))

    def read_text(self, path: Path) -> str | None:
        self.read_calls.append(path)
        return self.files.get(path)


def _write_repository_facts(repo_root: Path, *, with_gcc: bool) -> None:
    for relative_path in (
        ".python-version",
        ".node-version",
        ".nvmrc",
        "pyproject.toml",
        "global.json",
        "sidecar/go.mod",
        "tools/recovery-tools/go.mod",
    ):
        target = repo_root / relative_path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes((SOURCE_ROOT / relative_path).read_bytes())
    if with_gcc:
        gcc = repo_root / ".tools/w64devkit/w64devkit/bin/gcc.exe"
        gcc.parent.mkdir(parents=True)
        gcc.touch()


def _minimum_adapter() -> MemoryAdapter:
    python_parts = tuple(
        int(part)
        for part in (SOURCE_ROOT / ".python-version").read_text(encoding="utf-8").split(".")
    )
    python_default = (*python_parts, *(0 for _ in range(3 - len(python_parts))))
    python_newer = (python_default[0], python_default[1] + 2, python_default[2])
    return MemoryAdapter(
        platform_snapshot=PlatformSnapshot(
            system="Windows",
            machine="AMD64",
            build=22631,
            python_version=python_newer,
            python_executable="C:/Python/python.exe",
        ),
        executables={
            "uv": "C:/tools/uv.exe",
            "dotnet": "C:/Program Files/dotnet/dotnet.exe",
            "7z": None,
            "node": None,
            "go": None,
            "git": None,
        },
        results={
            ("C:/tools/uv.exe", "--version"): CommandResult(0, "uv 999.0.0\n", ""),
            ("C:/Program Files/dotnet/dotnet.exe", "--version"): CommandResult(
                0, "resolved SDK\n", ""
            ),
        },
    )


def _write_full_tool_placeholders(repo_root: Path) -> tuple[Path, Path, Path]:
    node_root = repo_root / ".tools/node" / NODE_DISTRIBUTION.directory_name
    node_root.mkdir(parents=True)
    node = node_root / "node.exe"
    npm = node_root / "npm.cmd"
    node.touch()
    npm.touch()
    gcc = W64DEVKIT_DISTRIBUTION.gcc_path(repo_root)
    return node, npm, gcc


def _full_adapter(repo_root: Path) -> MemoryAdapter:
    adapter = _minimum_adapter()
    node, npm, gcc = _write_full_tool_placeholders(repo_root)
    adapter.executables.update(
        {
            "go": "C:/Go/bin/go.exe",
            "git": "C:/Program Files/Git/cmd/git.exe",
        }
    )
    adapter.results.update(
        {
            (str(node), "--version"): CommandResult(0, "v0.0.0\n", ""),
            (str(npm), "--version"): CommandResult(0, "callable npm\n", ""),
            (str(gcc), "--version"): CommandResult(0, "gcc.exe (GCC) 0.0.0\n", ""),
            ("C:/Program Files/Git/cmd/git.exe", "--version"): CommandResult(
                0, "git version 2.53.0.windows.1\n", ""
            ),
        }
    )
    adapter.files[Path("C:/Go/VERSION")] = "go0.0.0\n"
    return adapter


def test_minimum_accepts_supported_newer_python_and_missing_bootstrap_managed_tools(
    tmp_path: Path,
) -> None:
    _write_repository_facts(tmp_path, with_gcc=True)
    adapter = _minimum_adapter()

    report = diagnose_windows_toolchain(tmp_path, DoctorProfile.MINIMUM, adapter=adapter)

    assert report.passed
    assert tuple(check.code for check in report.checks) == (
        "platform.windows",
        "platform.x64",
        "platform.windows_build",
        "python.version",
        "uv.version",
        "node.declarations",
        "dotnet.sdk",
        "bootstrap.extractor",
    )
    assert "7z" not in adapter.which_calls
    assert "node" not in adapter.which_calls
    assert "go" not in adapter.which_calls
    assert "git" not in adapter.which_calls


def test_minimum_requires_7z_only_when_w64devkit_is_absent(tmp_path: Path) -> None:
    _write_repository_facts(tmp_path, with_gcc=False)
    adapter = _minimum_adapter()

    report = diagnose_windows_toolchain(tmp_path, DoctorProfile.MINIMUM, adapter=adapter)

    assert not report.passed
    extractor = next(check for check in report.checks if check.code == "bootstrap.extractor")
    assert not extractor.passed
    assert extractor.actual == "7z not found; repository GCC not restored"
    assert adapter.which_calls.count("7z") == 1


def test_minimum_rejects_an_uncallable_7z_alias(tmp_path: Path) -> None:
    _write_repository_facts(tmp_path, with_gcc=False)
    adapter = _minimum_adapter()
    adapter.executables["7z"] = "C:/Users/example/AppData/Local/Microsoft/WindowsApps/7z.exe"
    adapter.results[(adapter.executables["7z"], "i")] = CommandResult(
        1,
        "",
        "application execution alias is unavailable",
    )

    report = diagnose_windows_toolchain(tmp_path, DoctorProfile.MINIMUM, adapter=adapter)

    extractor = next(check for check in report.checks if check.code == "bootstrap.extractor")
    assert not extractor.passed
    assert "application execution alias is unavailable" in extractor.actual


def test_minimum_does_not_treat_a_stale_virtualenv_as_an_installed_uv(
    tmp_path: Path,
) -> None:
    _write_repository_facts(tmp_path, with_gcc=True)
    stale_uv = tmp_path / ".venv/Scripts/uv.exe"
    stale_uv.parent.mkdir(parents=True)
    stale_uv.touch()
    adapter = _minimum_adapter()
    adapter.executables["uv"] = None

    report = diagnose_windows_toolchain(tmp_path, DoctorProfile.MINIMUM, adapter=adapter)

    uv_check = next(check for check in report.checks if check.code == "uv.version")
    assert not uv_check.passed
    assert uv_check.actual == "uv not found"
    assert adapter.which_calls.count("uv") == 1


def test_full_reports_all_tool_version_mismatches_and_disables_go_auto_download(
    tmp_path: Path,
) -> None:
    _write_repository_facts(tmp_path, with_gcc=True)
    adapter = _full_adapter(tmp_path)

    report = diagnose_windows_toolchain(tmp_path, DoctorProfile.FULL, adapter=adapter)

    assert not report.passed
    assert {check.code for check in report.checks if not check.passed} == {
        "node.version",
        "go.version",
        "gcc.version",
    }
    assert tuple(check.code for check in report.checks[-5:]) == (
        "node.version",
        "npm.executable",
        "go.version",
        "gcc.version",
        "git.executable",
    )
    assert Path("C:/Go/VERSION") in adapter.read_calls
    assert not any(call[0][0] == "C:/Go/bin/go.exe" for call in adapter.run_calls)


def test_full_rejects_a_go_declaration_missing_from_either_module(tmp_path: Path) -> None:
    _write_repository_facts(tmp_path, with_gcc=True)
    (tmp_path / "tools/recovery-tools/go.mod").write_text(
        "module example.invalid/recovery\n",
        encoding="utf-8",
    )
    adapter = _full_adapter(tmp_path)
    adapter.files[Path("C:/Go/VERSION")] = (
        f"go{read_go_module_version(SOURCE_ROOT / 'sidecar/go.mod')}\n"
    )

    report = diagnose_windows_toolchain(tmp_path, DoctorProfile.FULL, adapter=adapter)

    go_check = next(check for check in report.checks if check.code == "go.version")
    assert not go_check.passed
    assert "recovery" in go_check.expected
    assert "None" in go_check.expected


def test_system_and_bootstrap_resolvers_prefer_the_same_dotnet_install(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    preferred = tmp_path / "dotnet.exe"
    preferred.touch()
    repository = tmp_path / "repository"
    repository.mkdir()
    monkeypatch.setattr(automation_project, "REPO_ROOT", repository)
    monkeypatch.setattr(toolchain_metadata, "PREFERRED_DOTNET", preferred)
    monkeypatch.setattr(
        toolchain_metadata.shutil,
        "which",
        lambda executable, **kwargs: "C:/Program Files (x86)/dotnet/dotnet.exe",
    )

    assert SystemAdapter().which("dotnet") == str(preferred)
    assert automation_project._resolve_executable("dotnet") == str(preferred)


def test_minimum_reports_invalid_global_json_without_a_traceback(tmp_path: Path) -> None:
    _write_repository_facts(tmp_path, with_gcc=True)
    (tmp_path / "global.json").write_text(
        json.dumps({"sdk": {"rollForward": "latestFeature"}}),
        encoding="utf-8",
    )

    report = diagnose_windows_toolchain(
        tmp_path,
        DoctorProfile.MINIMUM,
        adapter=_minimum_adapter(),
    )

    dotnet = next(check for check in report.checks if check.code == "dotnet.sdk")
    assert not dotnet.passed
    assert dotnet.expected == "valid global.json SDK declaration"
    assert "version" in dotnet.actual


def test_doctor_cli_reports_invalid_pyproject_without_a_traceback(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    _write_repository_facts(tmp_path, with_gcc=True)
    (tmp_path / "pyproject.toml").write_text(
        '[project]\nname = "invalid-toolchain-declaration"\n',
        encoding="utf-8",
    )
    monkeypatch.setattr(automation_project, "REPO_ROOT", tmp_path)

    assert automation_project.main(["doctor", "--profile", "minimum"]) == 1

    captured = capsys.readouterr()
    assert "invalid pyproject toolchain declaration" in captured.err
    assert "Traceback" not in captured.err


def test_full_passes_when_every_declared_toolchain_is_available(tmp_path: Path) -> None:
    _write_repository_facts(tmp_path, with_gcc=True)
    adapter = _full_adapter(tmp_path)
    node = tmp_path / ".tools/node" / NODE_DISTRIBUTION.directory_name / "node.exe"
    gcc = W64DEVKIT_DISTRIBUTION.gcc_path(tmp_path)
    adapter.results[(str(node), "--version")] = CommandResult(
        0, f"v{NODE_DISTRIBUTION.version}\n", ""
    )
    adapter.files[Path("C:/Go/VERSION")] = (
        f"go{read_go_module_version(SOURCE_ROOT / 'sidecar/go.mod')}\n"
    )
    adapter.results[(str(gcc), "--version")] = CommandResult(
        0,
        (
            f"gcc.exe (GCC) {W64DEVKIT_DISTRIBUTION.gcc_version}\n"
            "Copyright should not enter the stable report\n"
        ),
        "",
    )

    report = diagnose_windows_toolchain(tmp_path, DoctorProfile.FULL, adapter=adapter)

    assert report.passed
    gcc_check = next(check for check in report.checks if check.code == "gcc.version")
    assert gcc_check.actual == (f"gcc.exe (GCC) {W64DEVKIT_DISTRIBUTION.gcc_version} ({gcc})")


def test_doctor_is_read_only_and_runs_only_version_probes(tmp_path: Path) -> None:
    _write_repository_facts(tmp_path, with_gcc=True)
    adapter = _full_adapter(tmp_path)
    before = {
        path.relative_to(tmp_path): path.read_bytes()
        for path in tmp_path.rglob("*")
        if path.is_file()
    }

    diagnose_windows_toolchain(tmp_path, DoctorProfile.FULL, adapter=adapter)

    after = {
        path.relative_to(tmp_path): path.read_bytes()
        for path in tmp_path.rglob("*")
        if path.is_file()
    }
    assert after == before
    node = tmp_path / ".tools/node" / NODE_DISTRIBUTION.directory_name / "node.exe"
    npm = node.with_name("npm.cmd")
    gcc = W64DEVKIT_DISTRIBUTION.gcc_path(tmp_path)
    assert adapter.which_calls == ["uv", "dotnet", "go", "git"]
    assert adapter.read_calls == [Path("C:/Go/VERSION")]
    assert adapter.run_calls == [
        (("C:/tools/uv.exe", "--version"), tmp_path, None),
        (
            ("C:/Program Files/dotnet/dotnet.exe", "--version"),
            tmp_path,
            {
                "DOTNET_CLI_TELEMETRY_OPTOUT": "1",
                "DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE": "1",
            },
        ),
        ((str(node), "--version"), tmp_path, None),
        ((str(npm), "--version"), tmp_path, None),
        ((str(gcc), "--version"), tmp_path, None),
        (("C:/Program Files/Git/cmd/git.exe", "--version"), tmp_path, None),
    ]


def test_system_adapter_reads_go_metadata_without_starting_go_or_touching_user_config(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    go_root = tmp_path / "go"
    go_root.mkdir()
    version_file = go_root / "VERSION"
    version_file.write_text("go1.27.0\ntime 2026-01-01T00:00:00Z\n", encoding="utf-8")
    user_config = tmp_path / "user-config"
    user_config.mkdir()
    monkeypatch.setenv("APPDATA", str(user_config))

    def reject_process(*args: object, **kwargs: object) -> None:
        raise AssertionError("reading Go installation metadata must not start a process")

    monkeypatch.setattr(windows_doctor.subprocess, "run", reject_process)

    assert SystemAdapter().read_text(version_file) == version_file.read_text(encoding="utf-8")
    assert not tuple(user_config.iterdir())


def test_render_report_uses_stable_codes_without_claiming_release_readiness() -> None:
    report = DoctorReport(
        profile=DoctorProfile.FULL,
        checks=(
            DoctorCheck(
                code="go.version",
                passed=False,
                expected="1.27.0",
                actual="go1.24.0 (C:/Go/bin/go.exe)",
                remediation="安装声明版本。",
            ),
        ),
    )

    rendered = render_report(report)

    assert "Windows toolchain doctor (full): FAIL" in rendered
    assert "[FAIL] go.version" in rendered
    assert "expected: 1.27.0" in rendered
    assert "actual: go1.24.0 (C:/Go/bin/go.exe)" in rendered
    assert "toolchain ready: no" in rendered
    assert "release ready" not in rendered.casefold()


def test_repository_toolchain_metadata_matches_authoritative_sources() -> None:
    repo_root = SOURCE_ROOT

    assert {
        (repo_root / ".node-version").read_text(encoding="utf-8").strip(),
        (repo_root / ".nvmrc").read_text(encoding="utf-8").strip(),
        NODE_DISTRIBUTION.version,
    } == {NODE_DISTRIBUTION.version}
    assert read_go_module_version(repo_root / "sidecar/go.mod") == read_go_module_version(
        repo_root / "tools/recovery-tools/go.mod"
    )
    assert automation_project.W64DEVKIT_DISTRIBUTION is W64DEVKIT_DISTRIBUTION
