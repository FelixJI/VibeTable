"""Read-only Windows development toolchain diagnosis."""

from __future__ import annotations

import json
import os
import platform as platform_module
import re
import subprocess
import sys
import tomllib
from dataclasses import dataclass
from enum import StrEnum
from pathlib import Path
from typing import Protocol

try:
    from scripts.node_toolchain import NODE_DISTRIBUTION
    from scripts.toolchain_metadata import W64DEVKIT_DISTRIBUTION, resolve_executable
except ModuleNotFoundError:  # pragma: no cover - direct script execution
    from node_toolchain import NODE_DISTRIBUTION
    from toolchain_metadata import W64DEVKIT_DISTRIBUTION, resolve_executable


class DoctorProfile(StrEnum):
    MINIMUM = "minimum"
    FULL = "full"


@dataclass(frozen=True)
class PlatformSnapshot:
    system: str
    machine: str
    build: int
    python_version: tuple[int, int, int]
    python_executable: str


@dataclass(frozen=True)
class CommandResult:
    returncode: int
    stdout: str
    stderr: str


@dataclass(frozen=True)
class DoctorCheck:
    code: str
    passed: bool
    expected: str
    actual: str
    remediation: str


@dataclass(frozen=True)
class DoctorReport:
    profile: DoctorProfile
    checks: tuple[DoctorCheck, ...]

    @property
    def passed(self) -> bool:
        return all(check.passed for check in self.checks)


class DoctorAdapter(Protocol):
    def platform(self) -> PlatformSnapshot: ...

    def which(self, executable: str) -> str | None: ...

    def read_text(self, path: Path) -> str | None: ...

    def run(
        self,
        command: tuple[str, ...],
        *,
        cwd: Path,
        env: dict[str, str] | None = None,
    ) -> CommandResult: ...


class SystemAdapter:
    def platform(self) -> PlatformSnapshot:
        windows_version = getattr(sys, "getwindowsversion", None)
        build = windows_version().build if windows_version is not None else 0
        return PlatformSnapshot(
            system=platform_module.system(),
            machine=platform_module.machine(),
            build=build,
            python_version=(sys.version_info.major, sys.version_info.minor, sys.version_info.micro),
            python_executable=sys.executable,
        )

    def which(self, executable: str) -> str | None:
        return resolve_executable(executable)

    def read_text(self, path: Path) -> str | None:
        try:
            return path.read_text(encoding="utf-8")
        except OSError:
            return None

    def run(
        self,
        command: tuple[str, ...],
        *,
        cwd: Path,
        env: dict[str, str] | None = None,
    ) -> CommandResult:
        try:
            completed = subprocess.run(
                command,
                cwd=cwd,
                env={**os.environ, **(env or {})},
                capture_output=True,
                text=True,
                errors="replace",
                check=False,
            )
        except OSError as exc:
            return CommandResult(returncode=127, stdout="", stderr=str(exc))
        return CommandResult(completed.returncode, completed.stdout, completed.stderr)


SYSTEM_ADAPTER = SystemAdapter()
_VERSION_PATTERN = re.compile(r"(?<!\d)(\d+)\.(\d+)(?:\.(\d+))?")


def _version_tuple(value: str) -> tuple[int, int, int] | None:
    match = _VERSION_PATTERN.search(value)
    if match is None:
        return None
    major, minor, patch = match.groups()
    return int(major), int(minor), int(patch or 0)


def _format_version(version: tuple[int, int, int]) -> str:
    return ".".join(str(part) for part in version)


def _minimum_from_specifier(value: str) -> tuple[int, int, int]:
    match = re.fullmatch(r">=\s*(\d+)\.(\d+)(?:\.(\d+))?", value.strip())
    if match is None:
        raise ValueError(f"unsupported minimum version specifier: {value}")
    major, minor, patch = match.groups()
    return int(major), int(minor), int(patch or 0)


def _repository_facts(repo_root: Path) -> tuple[tuple[int, int, int], tuple[int, int, int]]:
    try:
        pyproject = tomllib.loads((repo_root / "pyproject.toml").read_text(encoding="utf-8"))
        python_specifier = pyproject["project"]["requires-python"]
        build_dependencies = pyproject["dependency-groups"]["build"]
        if not isinstance(python_specifier, str) or not isinstance(build_dependencies, list):
            raise TypeError("requires-python must be text and the build group must be a list")
        python_minimum = _minimum_from_specifier(python_specifier)
    except (KeyError, TypeError) as exc:
        raise ValueError(f"invalid pyproject toolchain declaration: {exc}") from exc
    uv_entries = [
        item for item in build_dependencies if isinstance(item, str) and item.startswith("uv>=")
    ]
    if len(uv_entries) != 1:
        raise ValueError("pyproject build group must contain one uv minimum")
    uv_minimum = _minimum_from_specifier(uv_entries[0].removeprefix("uv"))
    return python_minimum, uv_minimum


def _check(
    code: str,
    passed: bool,
    expected: str,
    actual: str,
    remediation: str,
) -> DoctorCheck:
    return DoctorCheck(code, passed, expected, actual, remediation)


def _command_actual(result: CommandResult, executable: str | Path) -> str:
    primary = result.stderr if result.returncode != 0 else result.stdout
    diagnostic = primary.strip() or result.stdout.strip() or result.stderr.strip()
    first_line = diagnostic.splitlines()[0] if diagnostic else "no diagnostic output"
    return f"{first_line[:240]} ({executable})"


def _minimum_checks(
    repo_root: Path,
    adapter: DoctorAdapter,
) -> list[DoctorCheck]:
    snapshot = adapter.platform()
    python_minimum, uv_minimum = _repository_facts(repo_root)
    checks = [
        _check(
            "platform.windows",
            snapshot.system.casefold() == "windows",
            "Windows",
            snapshot.system,
            "Use a Windows 10/11 x64 development host.",
        ),
        _check(
            "platform.x64",
            snapshot.machine.casefold() in {"amd64", "x86_64"},
            "x64",
            snapshot.machine,
            "Use a Windows x64 host.",
        ),
        _check(
            "platform.windows_build",
            snapshot.build >= 10240,
            "Windows build >= 10240",
            str(snapshot.build),
            "Upgrade to a supported Windows 10/11 build.",
        ),
    ]

    declared_python = _version_tuple(
        (repo_root / ".python-version").read_text(encoding="utf-8").strip()
    )
    python_ok = snapshot.python_version >= python_minimum and (
        declared_python is not None and declared_python >= python_minimum
    )
    checks.append(
        _check(
            "python.version",
            python_ok,
            f">= {_format_version(python_minimum)}",
            f"{_format_version(snapshot.python_version)} ({snapshot.python_executable})",
            "Install a supported Python and select the repository default in `.python-version`.",
        )
    )

    uv_path = adapter.which("uv")
    uv_result = adapter.run((uv_path, "--version"), cwd=repo_root) if uv_path is not None else None
    uv_version = _version_tuple(uv_result.stdout) if uv_result is not None else None
    uv_ok = (
        uv_result is not None
        and uv_result.returncode == 0
        and uv_version is not None
        and uv_version >= uv_minimum
    )
    uv_actual = "uv not found"
    if uv_result is not None and uv_path is not None:
        uv_actual = _command_actual(uv_result, uv_path)
    checks.append(
        _check(
            "uv.version",
            uv_ok,
            f">= {_format_version(uv_minimum)}",
            uv_actual,
            "Install a supported uv version using the project documentation.",
        )
    )

    node_declarations = {
        ".node-version": (repo_root / ".node-version").read_text(encoding="utf-8").strip(),
        ".nvmrc": (repo_root / ".nvmrc").read_text(encoding="utf-8").strip(),
        "distribution": NODE_DISTRIBUTION.version,
    }
    node_ok = len(set(node_declarations.values())) == 1
    checks.append(
        _check(
            "node.declarations",
            node_ok,
            "all Node declarations agree",
            json.dumps(node_declarations, ensure_ascii=False, sort_keys=True),
            "Keep `.node-version`, `.nvmrc`, and the Node distribution metadata aligned.",
        )
    )

    dotnet_path = adapter.which("dotnet")
    dotnet_result = (
        adapter.run(
            (dotnet_path, "--version"),
            cwd=repo_root,
            env={
                "DOTNET_CLI_TELEMETRY_OPTOUT": "1",
                "DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE": "1",
            },
        )
        if dotnet_path is not None
        else None
    )
    dotnet_ok = dotnet_result is not None and dotnet_result.returncode == 0
    dotnet_actual = "dotnet not found"
    if dotnet_result is not None and dotnet_path is not None:
        dotnet_actual = _command_actual(dotnet_result, dotnet_path)
    global_sdk: dict[str, object] | None = None
    global_sdk_error: str | None = None
    try:
        global_json = json.loads((repo_root / "global.json").read_text(encoding="utf-8"))
        candidate = global_json["sdk"]
        if not isinstance(candidate, dict):
            raise ValueError("sdk must be an object")
        for key in ("version", "rollForward"):
            if not isinstance(candidate.get(key), str) or not candidate[key]:
                raise ValueError(f"sdk.{key} must be a non-empty string")
        global_sdk = candidate
    except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as exc:
        global_sdk_error = str(exc)
    dotnet_ok = dotnet_ok and global_sdk is not None
    dotnet_expected = "valid global.json SDK declaration"
    if global_sdk is not None:
        dotnet_expected = f"global.json {global_sdk['version']} ({global_sdk['rollForward']})"
    if global_sdk_error is not None:
        dotnet_actual = f"invalid global.json: {global_sdk_error}; {dotnet_actual}"
    checks.append(
        _check(
            "dotnet.sdk",
            dotnet_ok,
            dotnet_expected,
            dotnet_actual,
            "Install a .NET SDK that the repository `global.json` can resolve.",
        )
    )

    gcc_path = W64DEVKIT_DISTRIBUTION.gcc_path(repo_root)
    gcc_restored = gcc_path.is_file()
    extractor_path = None if gcc_restored else adapter.which("7z")
    extractor_result = (
        adapter.run((extractor_path, "i"), cwd=repo_root) if extractor_path is not None else None
    )
    extractor_ok = gcc_restored or (
        extractor_result is not None and extractor_result.returncode == 0
    )
    if gcc_restored:
        extractor_actual = f"repository GCC restored ({gcc_path})"
    elif extractor_path is None or extractor_result is None:
        extractor_actual = "7z not found; repository GCC not restored"
    elif extractor_result.returncode == 0:
        extractor_actual = f"callable 7z ({extractor_path})"
    else:
        extractor_actual = _command_actual(extractor_result, extractor_path)
    checks.append(
        _check(
            "bootstrap.extractor",
            extractor_ok,
            "repository GCC restored or callable 7z",
            extractor_actual,
            "Install 7-Zip, or restore w64devkit through the repository bootstrap.",
        )
    )
    return checks


def read_go_module_version(path: Path) -> str | None:
    match = re.search(r"^go\s+(\d+\.\d+(?:\.\d+)?)\s*$", path.read_text(encoding="utf-8"), re.M)
    return match.group(1) if match is not None else None


def _full_checks(repo_root: Path, adapter: DoctorAdapter) -> list[DoctorCheck]:
    checks: list[DoctorCheck] = []
    node_path = repo_root / ".tools" / "node" / NODE_DISTRIBUTION.directory_name / "node.exe"
    node_result = (
        adapter.run((str(node_path), "--version"), cwd=repo_root) if node_path.is_file() else None
    )
    node_version = _version_tuple(node_result.stdout) if node_result is not None else None
    expected_node = _version_tuple(NODE_DISTRIBUTION.version)
    node_ok = (
        node_result is not None
        and node_result.returncode == 0
        and node_version is not None
        and node_version == expected_node
    )
    node_actual = "repository Node not restored"
    if node_result is not None:
        node_actual = _command_actual(node_result, node_path)
    checks.append(
        _check(
            "node.version",
            node_ok,
            f"v{NODE_DISTRIBUTION.version}",
            node_actual,
            "Run the repository bootstrap to restore the pinned Node distribution.",
        )
    )

    npm_path = node_path.with_name("npm.cmd")
    npm_result = (
        adapter.run((str(npm_path), "--version"), cwd=repo_root) if npm_path.is_file() else None
    )
    npm_ok = npm_result is not None and npm_result.returncode == 0
    npm_actual = "repository npm not restored"
    if npm_result is not None:
        npm_actual = _command_actual(npm_result, npm_path)
    checks.append(
        _check(
            "npm.executable",
            npm_ok,
            "npm from the repository Node distribution",
            npm_actual,
            "Run the repository bootstrap to restore npm from the pinned Node distribution.",
        )
    )

    go_versions = {
        "sidecar": read_go_module_version(repo_root / "sidecar/go.mod"),
        "recovery": read_go_module_version(repo_root / "tools/recovery-tools/go.mod"),
    }
    declared_go_versions = tuple(go_versions.values())
    declared_go = (
        declared_go_versions[0]
        if all(version is not None for version in declared_go_versions)
        and len(set(declared_go_versions)) == 1
        else None
    )
    go_path = adapter.which("go")
    go_version_path = Path(go_path).parent.parent / "VERSION" if go_path is not None else None
    go_metadata = adapter.read_text(go_version_path) if go_version_path is not None else None
    actual_go = _version_tuple(go_metadata or "")
    expected_go = _version_tuple(declared_go or "")
    go_ok = (
        declared_go is not None
        and go_metadata is not None
        and actual_go is not None
        and actual_go == expected_go
    )
    go_actual = "go not found"
    if go_path is not None and go_metadata is None:
        go_actual = f"Go VERSION metadata not found ({go_version_path}; {go_path})"
    elif go_path is not None and go_metadata is not None:
        first_line = go_metadata.strip().splitlines()[0] if go_metadata.strip() else "empty VERSION"
        go_actual = f"{first_line[:240]} ({go_version_path}; {go_path})"
    checks.append(
        _check(
            "go.version",
            go_ok,
            declared_go or f"matching module declarations: {go_versions}",
            go_actual,
            "Install the exact Go version declared by both modules; auto-download stays disabled.",
        )
    )

    gcc_path = W64DEVKIT_DISTRIBUTION.gcc_path(repo_root)
    gcc_result = (
        adapter.run((str(gcc_path), "--version"), cwd=repo_root) if gcc_path.is_file() else None
    )
    gcc_version = _version_tuple(gcc_result.stdout) if gcc_result is not None else None
    expected_gcc = _version_tuple(W64DEVKIT_DISTRIBUTION.gcc_version)
    gcc_ok = (
        gcc_result is not None
        and gcc_result.returncode == 0
        and gcc_version is not None
        and gcc_version == expected_gcc
    )
    gcc_actual = "repository GCC not restored"
    if gcc_result is not None:
        gcc_actual = _command_actual(gcc_result, gcc_path)
    checks.append(
        _check(
            "gcc.version",
            gcc_ok,
            W64DEVKIT_DISTRIBUTION.gcc_version,
            gcc_actual,
            "Run the repository bootstrap to restore the pinned w64devkit/GCC distribution.",
        )
    )

    git_path = adapter.which("git")
    git_result = (
        adapter.run((git_path, "--version"), cwd=repo_root) if git_path is not None else None
    )
    git_ok = git_result is not None and git_result.returncode == 0
    git_actual = "git not found"
    if git_result is not None and git_path is not None:
        git_actual = _command_actual(git_result, git_path)
    checks.append(
        _check(
            "git.executable",
            git_ok,
            "Git executable",
            git_actual,
            "Install Git for Windows and make git available to the current environment.",
        )
    )
    return checks


def diagnose_windows_toolchain(
    repo_root: Path,
    profile: DoctorProfile,
    *,
    adapter: DoctorAdapter = SYSTEM_ADAPTER,
) -> DoctorReport:
    checks = _minimum_checks(repo_root, adapter)
    if profile is DoctorProfile.FULL:
        checks.extend(_full_checks(repo_root, adapter))
    return DoctorReport(profile=profile, checks=tuple(checks))


def render_report(report: DoctorReport) -> str:
    status = "PASS" if report.passed else "FAIL"
    lines = [f"Windows toolchain doctor ({report.profile.value}): {status}"]
    for check in report.checks:
        check_status = "PASS" if check.passed else "FAIL"
        lines.extend(
            (
                f"[{check_status}] {check.code}",
                f"  expected: {check.expected}",
                f"  actual: {check.actual}",
            )
        )
        if not check.passed:
            lines.append(f"  remediation: {check.remediation}")
    lines.append(f"toolchain ready: {'yes' if report.passed else 'no'}")
    return "\n".join(lines) + "\n"
