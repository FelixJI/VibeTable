#!/usr/bin/env python3
"""Cross-stack CI gate with real PocketBase sidecar verification."""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import suppress
from dataclasses import asdict, dataclass
from datetime import UTC, datetime
from pathlib import Path
from threading import Event

try:
    from qa import handoff as handoff_gate
    from qa import release_candidate
except ModuleNotFoundError:  # pragma: no cover - direct ``python qa/next.py``
    import handoff as handoff_gate  # type: ignore[no-redef]
    import release_candidate  # type: ignore[no-redef]

REPO_ROOT = Path(__file__).resolve().parents[1]
SIDECAR_DIR = REPO_ROOT / "sidecar"
WEB_GRID_DIR = REPO_ROOT / "desktop" / "web-grid"
DESKTOP_SLN = REPO_ROOT / "desktop" / "VibeTable.Desktop.sln"
DEV_LAUNCHER = REPO_ROOT / "scripts" / "dev.py"
SIDECAR_MATRIX = REPO_ROOT / "tests" / "integration" / "packaged_sidecar_matrix.py"
FAULT_INJECTION = REPO_ROOT / "qa" / "fault_injection.py"
GO_FORMAT_CHECK = REPO_ROOT / "qa" / "go_format_check.py"
E2E_SMOKE = REPO_ROOT / "tests" / "e2e" / "test_next_readonly_smoke.py"
UPGRADE_SMOKE = REPO_ROOT / "tests" / "integration" / "test_upgrade_activation_smoke.py"

STAGES = (
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
DEFAULT_STAGE_TIMEOUT_SECONDS = 15 * 60
STAGE_TIMEOUT_SECONDS = {
    "fault-injection": 30 * 60,
    "product-e2e": 30 * 60,
}
RACE_COMMAND_TIMEOUT_SECONDS = 7 * 60
RACE_LONG_COMMAND_TIMEOUT_SECONDS = 16 * 60
RACE_LONG_TEST_TIMEOUT = "15m"
RACE_BINARY_DIR = REPO_ROOT / "build" / "qa" / "race-tests"
RACE_PACKAGE_WORKERS = 2
RACE_LONG_TESTS = frozenset(
    {
        "TestFormulaBackfillScaleCancelsResumesWithoutDuplicateAudit",
        "TestMutationKernelOneThousandOperationsCommitOrFullyRollback",
        "TestQueryPortPagesFiltersAndSortsTwentyFiveThousandRows",
    }
)
TIMEOUT_RETURNCODE = 124
WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS = 3
QA_TEMP_PARENT_ENV = "VIBETABLE_QA_TEMP_PARENT"
QA_RUN_TEMP_DIR: Path | None = None


def _qa_temp_dir() -> Path:
    """Return this invocation's secure, short-lived QA workspace."""

    global QA_RUN_TEMP_DIR
    if QA_RUN_TEMP_DIR is None:
        # Several real tests add pytest, backup, timestamp and previous-install
        # segments below this root. A short system location avoids legacy
        # Windows MAX_PATH failures; mkdtemp also creates it exclusively. An
        # outer QA process exports its original parent so nested gate tests
        # create a sibling instead of recursively nesting under TMP.
        parent = Path(os.environ.get(QA_TEMP_PARENT_ENV, tempfile.gettempdir())).resolve()
        if not parent.is_dir():
            raise RuntimeError(f"QA temporary parent does not exist: {parent}")
        QA_RUN_TEMP_DIR = Path(tempfile.mkdtemp(prefix="vtqa-", dir=parent))
    return QA_RUN_TEMP_DIR


def _cleanup_qa_temp_dir() -> None:
    global QA_RUN_TEMP_DIR
    if QA_RUN_TEMP_DIR is None:
        return
    shutil.rmtree(QA_RUN_TEMP_DIR)
    QA_RUN_TEMP_DIR = None


@dataclass
class StageResult:
    stage: str
    command: list[str]
    returncode: int
    elapsed: float
    stdout: str
    stderr: str
    cwd: str

    def to_dict(self) -> dict:
        return asdict(self)


def _resolve(name: str) -> str:
    if name == "go":
        suffix = "go.exe" if os.name == "nt" else "go"
        candidate = REPO_ROOT / ".tools" / "go-full" / "go" / "bin" / suffix
        if candidate.is_file():
            return str(candidate)
    if name == "gcc" and os.name == "nt":
        for candidate in (
            REPO_ROOT / ".tools" / "w64devkit" / "bin" / "gcc.exe",
            REPO_ROOT / ".tools" / "w64devkit" / "w64devkit" / "bin" / "gcc.exe",
        ):
            if candidate.is_file():
                return str(candidate)
    if name == "dotnet":
        bundled = REPO_ROOT / ".tools" / "dotnet" / "dotnet.exe"
        if bundled.is_file():
            return str(bundled)
        system = Path(r"C:\Program Files\dotnet\dotnet.exe")
        if system.is_file():
            return str(system)
    return shutil.which(name) or name


def stage_command(
    stage: str,
    package_root: Path | None = None,
    package_archive: Path | None = None,
) -> tuple[list[str], str]:
    go = _resolve("go")
    if stage == "version":
        return [sys.executable, "qa/version_check.py"], str(REPO_ROOT)
    if stage == "package":
        command = [sys.executable, "qa/package_check.py"]
        if package_root is not None:
            command.append(str(package_root))
        if package_archive is not None:
            command.extend(["--package-archive", str(package_archive)])
        return command, str(REPO_ROOT)
    if stage == "go-fmt":
        return [sys.executable, str(GO_FORMAT_CHECK)], str(REPO_ROOT)
    if stage == "go-vet":
        return [go, "vet", "./..."], str(SIDECAR_DIR)
    if stage == "go-test":
        return [go, "test", "./..."], str(SIDECAR_DIR)
    if stage == "go-race":
        return [
            go,
            "test",
            "-race",
            "all non-integration packages, then integration tests in isolated batches",
        ], str(SIDECAR_DIR)
    if stage == "go-build":
        output = (
            REPO_ROOT / "build" / "qa" / ("vibetable-pb.exe" if os.name == "nt" else "vibetable-pb")
        )
        return [
            go,
            "build",
            "-trimpath",
            "-buildvcs=true",
            "-o",
            str(output),
            "./cmd/vibetable-pb",
        ], str(SIDECAR_DIR)
    if stage == "sidecar-smoke":
        command = [
            sys.executable,
            str(SIDECAR_MATRIX),
            "--json-report",
            str(REPO_ROOT / "build" / "qa" / "packaged-sidecar-matrix.json"),
        ]
        if package_root is not None:
            command.extend(["--skip-build", "--package-root", str(package_root)])
        return command, str(REPO_ROOT)
    if stage == "upgrade-smoke":
        return [
            sys.executable,
            "-m",
            "pytest",
            str(UPGRADE_SMOKE),
            "-q",
            "--basetemp",
            str(_qa_temp_dir() / "u"),
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    if stage == "dev-build":
        return [sys.executable, str(DEV_LAUNCHER), "--build-only"], str(REPO_ROOT)
    if stage == "python":
        return [
            sys.executable,
            "-m",
            "pytest",
            "tests/backend",
            "-q",
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    if stage == "contracts":
        return [
            sys.executable,
            "-m",
            "pytest",
            "tests/contract",
            "-q",
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    if stage == "tooling":
        return [
            sys.executable,
            "-m",
            "pytest",
            "tests/test_release_tooling.py",
            "tests/test_architecture.py",
            "tests/test_dev.py",
            "tests/test_handoff_artifacts.py",
            "tests/test_next_gate.py",
            "-q",
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    if stage == "dotnet":
        return [
            _resolve("dotnet"),
            "test",
            str(DESKTOP_SLN),
            "--configuration",
            "Release",
            "/p:CollectCoverage=true",
            "/p:CoverletOutputFormat=cobertura",
        ], str(REPO_ROOT)
    if stage == "web-test":
        return [_resolve("npm"), "run", "test:coverage"], str(WEB_GRID_DIR)
    if stage == "web-build":
        return [_resolve("npm"), "run", "build"], str(WEB_GRID_DIR)
    if stage == "fault-injection":
        return [sys.executable, str(FAULT_INJECTION)], str(REPO_ROOT)
    if stage == "product-e2e":
        command = [
            sys.executable,
            "qa/product_acceptance.py",
            "--evidence-root",
            # Product acceptance creates timestamp, scenario, runtime,
            # workspace and content-addressed package-cache descendants. Keep
            # this segment minimal so the deepest real Windows path remains
            # below the legacy MAX_PATH boundary.
            str(_qa_temp_dir() / "p"),
        ]
        if package_root is not None:
            command.extend(["--package-root", str(package_root)])
        return command, str(REPO_ROOT)
    if stage == "smoke":
        return [
            sys.executable,
            "-m",
            "pytest",
            str(E2E_SMOKE),
            "-q",
            "-o",
            "addopts=",
            "-p",
            "no:cacheprovider",
        ], str(REPO_ROOT)
    raise ValueError(f"unknown stage: {stage}")


def _stage_environment(
    stage: str,
    command: list[str],
    package_root: Path | None = None,
) -> dict[str, str]:
    # Keep every gate invocation isolated. Reusing the parent directory lets
    # pytest discover a stale ``pytest-of-<user>`` folder whose ACL may belong
    # to a previous Windows sandbox account, failing before any test executes.
    qa_tmp = _qa_temp_dir()
    qa_tmp.mkdir(parents=True, exist_ok=True)
    environment = os.environ.copy()
    environment["TMP"] = str(qa_tmp)
    environment["TEMP"] = str(qa_tmp)
    environment[QA_TEMP_PARENT_ENV] = str(qa_tmp.parent)
    if stage == "fault-injection":
        environment["VIBETABLE_FAULT_EVIDENCE_ROOT"] = str(qa_tmp / "fault-injection")
    if stage.startswith("go-"):
        go_cache = REPO_ROOT / "build" / "qa" / "go-cache"
        go_tmp = REPO_ROOT / "build" / "qa" / "go-tmp"
        go_cache.mkdir(parents=True, exist_ok=True)
        go_tmp.mkdir(parents=True, exist_ok=True)
        environment["GOCACHE"] = str(go_cache)
        environment["GOTMPDIR"] = str(go_tmp)
    if stage in {"go-test", "go-race"} and package_root is not None:
        layout_path = package_root / "resources" / "publish-layout.json"
        try:
            layout = json.loads(layout_path.read_text(encoding="utf-8"))
            recovery_tools = layout["assets"]["recoveryTools"]
            tool_paths = {
                "VIBETABLE_KOPIA_CLI": package_root / recovery_tools["kopia"],
                "VIBETABLE_AGE_CLI": package_root / recovery_tools["age"],
            }
            resolved_root = package_root.resolve()
            resolved_tools = {
                name: path.resolve() for name, path in tool_paths.items() if path.is_file()
            }
            if len(resolved_tools) == len(tool_paths) and all(
                path.is_relative_to(resolved_root) for path in resolved_tools.values()
            ):
                environment.update({name: str(path) for name, path in resolved_tools.items()})
        except (KeyError, OSError, json.JSONDecodeError, TypeError):
            pass
    if stage == "go-race" and os.name == "nt":
        # Go's Windows race runtime requires cgo plus a MinGW-w64 runtime that
        # provides libsynchronization.a. Prefer the repository-local,
        # hash-verified w64devkit toolchain while still allowing a system gcc.
        compiler = _resolve("gcc")
        environment["CGO_ENABLED"] = "1"
        environment["CC"] = compiler
        compiler_dir = str(Path(compiler).parent)
        # w64devkit's collect2 resolves ld through COMPILER_PATH when invoked
        # indirectly by the Go linker. PATH alone is not reliable in a clean
        # Windows process environment.
        environment["COMPILER_PATH"] = compiler_dir
        environment["PATH"] = compiler_dir + os.pathsep + environment.get("PATH", "")
    if stage == "go-build":
        Path(command[command.index("-o") + 1]).parent.mkdir(parents=True, exist_ok=True)
    if stage == "smoke" and package_root is not None:
        environment["VIBETABLE_E2E_HOST"] = str(package_root / "VibeTable.Next.exe")
    return environment


def _terminate_process_tree(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return
    if os.name == "nt":
        with suppress(OSError, subprocess.TimeoutExpired):
            subprocess.run(
                ["taskkill", "/PID", str(process.pid), "/T", "/F"],
                check=False,
                capture_output=True,
                timeout=30,
            )
    else:
        with suppress(ProcessLookupError):
            os.killpg(process.pid, signal.SIGKILL)
    try:
        process.wait(timeout=30)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=30)


def _timeout_text(value: str | bytes | None) -> str:
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value or ""


def _run_command(
    command: list[str],
    *,
    cwd: str,
    environment: dict[str, str],
    timeout: int,
) -> tuple[int, str, str]:
    popen_kwargs: dict[str, object] = {}
    if os.name == "nt":
        popen_kwargs["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
    else:
        popen_kwargs["start_new_session"] = True
    try:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
            **popen_kwargs,
        )
    except OSError as exc:
        return 127, "", str(exc)
    try:
        stdout, stderr = process.communicate(timeout=timeout)
        return process.returncode, stdout or "", stderr or ""
    except subprocess.TimeoutExpired as exc:
        _terminate_process_tree(process)
        final_stdout, final_stderr = process.communicate()
        stdout = _timeout_text(exc.output) + (final_stdout or "")
        stderr = _timeout_text(exc.stderr) + (final_stderr or "")
        stderr += f"\nprocess tree timed out after {timeout}s and was terminated\n"
        return TIMEOUT_RETURNCODE, stdout, stderr


def _run_race_package(
    commands: list[tuple[list[str], int, str]],
    *,
    environment: dict[str, str],
    stop_event: Event,
    compile_command: list[str] | None = None,
    compile_cwd: str | None = None,
    binary: Path | None = None,
) -> tuple[int, list[str], list[str]]:
    output: list[str] = []
    errors: list[str] = []
    try:
        if stop_event.is_set():
            output.append("race package stopped after another package failed")
            return 0, output, errors
        if compile_command is not None:
            if compile_cwd is None:
                raise ValueError("race package compile command requires compile cwd")
            output.append("$ " + subprocess.list2cmdline(compile_command))
            compile_code, compile_stdout, compile_stderr = _run_command(
                compile_command,
                cwd=compile_cwd,
                environment=environment,
                timeout=RACE_COMMAND_TIMEOUT_SECONDS,
            )
            output.append(compile_stdout)
            errors.append(compile_stderr)
            if compile_code:
                stop_event.set()
                return compile_code, output, errors

        for command, timeout, command_cwd in commands:
            if stop_event.is_set():
                output.append("race package stopped after another package failed")
                return 0, output, errors
            output.append("$ " + subprocess.list2cmdline(command))
            code, stdout, stderr = _run_command(
                command,
                cwd=command_cwd,
                environment=environment,
                timeout=timeout,
            )
            output.append(stdout)
            errors.append(stderr)
            combined = stdout + "\n" + stderr
            attempt = 1
            while (
                code
                and attempt < WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS
                and _is_windows_tempdir_cleanup_flake(combined)
            ):
                attempt += 1
                output.append(
                    "known Windows TempDir watcher cleanup flake; "
                    "retrying this exact race command in a fresh process "
                    f"(attempt {attempt}/{WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS})"
                )
                code, stdout, stderr = _run_command(
                    command,
                    cwd=command_cwd,
                    environment=environment,
                    timeout=timeout,
                )
                output.append(stdout)
                errors.append(stderr)
                combined = stdout + "\n" + stderr
            if code:
                stop_event.set()
                return code, output, errors
        return 0, output, errors
    finally:
        if binary is not None:
            try:
                binary.unlink(missing_ok=True)
            except OSError as exc:
                errors.append(f"race binary cleanup failed for {binary}: {exc}")


def _run_go_race(
    *,
    cwd: str,
    environment: dict[str, str],
) -> tuple[int, str, str]:
    """Compile race tests once per package, then isolate every named test process."""

    go = _resolve("go")
    package_command = [
        go,
        "list",
        "-f",
        "{{.ImportPath}}\t{{.Dir}}",
        "./...",
    ]
    package_code, package_stdout, package_stderr = _run_command(
        package_command,
        cwd=cwd,
        environment=environment,
        timeout=RACE_COMMAND_TIMEOUT_SECONDS,
    )
    output = ["$ " + subprocess.list2cmdline(package_command), package_stdout]
    errors = [package_stderr]
    if package_code:
        return package_code, "\n".join(output), "\n".join(errors)
    packages: list[tuple[str, str]] = []
    for line in package_stdout.splitlines():
        if not line.strip():
            continue
        package, separator, package_dir = line.partition("\t")
        if not separator or not package.strip() or not package_dir.strip():
            return (
                1,
                "\n".join(output),
                f"invalid race package discovery row: {line!r}",
            )
        packages.append((package.strip(), package_dir.strip()))
    if not packages:
        return 1, "\n".join(output), "race package discovery found zero packages"

    RACE_BINARY_DIR.mkdir(parents=True, exist_ok=True)
    package_plans: list[
        tuple[
            list[tuple[list[str], int, str]],
            list[str] | None,
            Path | None,
        ]
    ] = []
    discovered_count = 0
    for package_index, (package, package_dir) in enumerate(packages):
        list_command = [
            go,
            "test",
            package,
            "-list",
            "^(Test|Example|Fuzz)",
        ]
        listed_code, listed_stdout, listed_stderr = _run_command(
            list_command,
            cwd=cwd,
            environment=environment,
            timeout=RACE_COMMAND_TIMEOUT_SECONDS,
        )
        output.extend(("$ " + subprocess.list2cmdline(list_command), listed_stdout))
        errors.append(listed_stderr)
        if listed_code:
            return listed_code, "\n".join(output), "\n".join(errors)
        names = [
            line.strip()
            for line in listed_stdout.splitlines()
            if re.fullmatch(r"(?:Test|Example|Fuzz)[A-Za-z0-9_]+", line.strip())
        ]
        if not names:
            package_plans.append(
                (
                    [
                        (
                            [
                                go,
                                "test",
                                "-race",
                                "-count=1",
                                "-timeout=5m",
                                package,
                            ],
                            RACE_COMMAND_TIMEOUT_SECONDS,
                            cwd,
                        )
                    ],
                    None,
                    None,
                )
            )
            continue

        binary_suffix = ".exe" if os.name == "nt" else ".test"
        binary = (RACE_BINARY_DIR / f"package-{package_index:03d}{binary_suffix}").resolve()
        compile_command = [
            go,
            "test",
            "-c",
            "-race",
            "-o",
            str(binary),
            package,
        ]

        discovered_count += len(names)
        regular = [name for name in names if name not in RACE_LONG_TESTS]
        long_running = [name for name in names if name in RACE_LONG_TESTS]
        package_commands: list[tuple[list[str], int, str]] = []
        for name in (*regular, *long_running):
            is_long = name in RACE_LONG_TESTS
            go_timeout = RACE_LONG_TEST_TIMEOUT if is_long else "5m"
            package_commands.append(
                (
                    [
                        str(binary),
                        "-test.count=1",
                        "-test.parallel=1",
                        f"-test.timeout={go_timeout}",
                        f"-test.run=^{re.escape(name)}$",
                    ],
                    (
                        RACE_LONG_COMMAND_TIMEOUT_SECONDS
                        if is_long
                        else RACE_COMMAND_TIMEOUT_SECONDS
                    ),
                    package_dir,
                )
            )
        package_plans.append((package_commands, compile_command, binary))
    if not discovered_count:
        return 1, "\n".join(output), "race discovery found zero named Go tests"

    package_plans.sort(key=lambda plan: len(plan[0]), reverse=True)
    stop_event = Event()
    worker_count = min(RACE_PACKAGE_WORKERS, len(package_plans))
    with ThreadPoolExecutor(
        max_workers=worker_count,
        thread_name_prefix="vibetable-go-race",
    ) as executor:
        futures = [
            executor.submit(
                _run_race_package,
                package_commands,
                environment=environment,
                stop_event=stop_event,
                compile_command=compile_command,
                compile_cwd=cwd,
                binary=binary,
            )
            for package_commands, compile_command, binary in package_plans
        ]
        package_results = [future.result() for future in futures]
    returncode = 0
    for code, package_output, package_errors in package_results:
        output.extend(package_output)
        errors.extend(package_errors)
        if code and not returncode:
            returncode = code
    return returncode, "\n".join(output), "\n".join(errors)


def _is_windows_tempdir_cleanup_flake(output: str) -> bool:
    """Recognize only the narrow PocketBase watcher cleanup race, never Go races."""

    if os.name != "nt":
        return False
    if "WARNING: DATA RACE" in output or "panic:" in output:
        return False
    if "TempDir RemoveAll cleanup:" not in output:
        return False
    if "The directory is not empty" not in output:
        return False
    diagnostics = re.findall(r"^\s+([^:\r\n]+\.go):\d+:", output, flags=re.MULTILINE)
    return bool(diagnostics) and all(Path(source).name == "testing.go" for source in diagnostics)


def run_stage(
    stage: str,
    package_root: Path | None = None,
    package_archive: Path | None = None,
) -> StageResult:
    command, cwd = (
        stage_command(stage, package_root)
        if package_archive is None
        else stage_command(stage, package_root, package_archive)
    )
    environment = _stage_environment(stage, command, package_root)
    started = time.monotonic()
    if stage == "go-race":
        returncode, stdout, stderr = _run_go_race(
            cwd=cwd,
            environment=environment,
        )
    else:
        returncode, stdout, stderr = _run_command(
            command,
            cwd=cwd,
            environment=environment,
            timeout=STAGE_TIMEOUT_SECONDS.get(
                stage,
                DEFAULT_STAGE_TIMEOUT_SECONDS,
            ),
        )
        attempt = 1
        while (
            stage == "go-test"
            and returncode
            and attempt < WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS
            and _is_windows_tempdir_cleanup_flake(stdout + "\n" + stderr)
        ):
            attempt += 1
            previous_stdout, previous_stderr = stdout, stderr
            returncode, stdout, stderr = _run_command(
                command,
                cwd=cwd,
                environment=environment,
                timeout=STAGE_TIMEOUT_SECONDS.get(
                    stage,
                    DEFAULT_STAGE_TIMEOUT_SECONDS,
                ),
            )
            stdout = "\n".join(
                (
                    previous_stdout,
                    "known Windows TempDir watcher cleanup flake; "
                    "retried the exact go-test command in a fresh process "
                    f"(attempt {attempt}/"
                    f"{WINDOWS_TEMPDIR_CLEANUP_MAX_ATTEMPTS})",
                    stdout,
                )
            )
            stderr = "\n".join((previous_stderr, stderr))
    return StageResult(
        stage=stage,
        command=command,
        returncode=returncode,
        elapsed=time.monotonic() - started,
        stdout=stdout,
        stderr=stderr,
        cwd=cwd,
    )


def _write_console_text(stream: object, value: str) -> None:
    """Write captured UTF-8 output without crashing on a legacy Windows code page."""

    if not value:
        return
    try:
        stream.write(value)  # type: ignore[attr-defined]
    except UnicodeEncodeError:
        encoding = getattr(stream, "encoding", None) or "utf-8"
        safe_value = value.encode(encoding, errors="backslashreplace").decode(encoding)
        stream.write(safe_value)  # type: ignore[attr-defined]
    stream.flush()  # type: ignore[attr-defined]


def run_ci(
    package_root: Path | None = None,
    package_archive: Path | None = None,
) -> tuple[int, list[StageResult]]:
    results: list[StageResult] = []
    for stage in STAGES:
        result = run_stage(stage, package_root, package_archive)
        results.append(result)
        _write_console_text(sys.stdout, result.stdout)
        _write_console_text(sys.stderr, result.stderr)
        if result.returncode:
            return result.returncode, results
    return 0, results


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--list", action="store_true")
    parser.add_argument("--ci", action="store_true")
    parser.add_argument("--stage", choices=STAGES)
    parser.add_argument("--json-report", type=Path)
    parser.add_argument("--package-root", type=Path)
    parser.add_argument("--package-archive", type=Path)
    return parser


def _main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.list:
        print("\n".join(STAGES))
        return 0
    try:
        starting_commit = handoff_gate.git_head_sha()
        dependencies = handoff_gate.load_dependencies()
        starting_hashes = handoff_gate.artifact_hashes(dependencies)
        starting_source_hash = handoff_gate.release_source_hash(dependencies)
    except (OSError, RuntimeError, ValueError) as exc:
        print(f"failed to capture gate identity: {exc}", file=sys.stderr)
        return 2
    starting_candidate: dict[str, object] | None = None
    if args.ci:
        if args.package_root is None or args.package_archive is None:
            print(
                "--ci requires --package-root and --package-archive for immutable candidate binding",
                file=sys.stderr,
            )
            return 2
        try:
            starting_candidate = release_candidate.candidate_evidence(
                args.package_root,
                args.package_archive,
            )
        except release_candidate.CandidateError as exc:
            print(f"failed to capture release candidate: {exc}", file=sys.stderr)
            return 2
    if args.stage:
        result = (
            run_stage(args.stage, args.package_root)
            if args.package_archive is None
            else run_stage(args.stage, args.package_root, args.package_archive)
        )
        results = [result]
        _write_console_text(sys.stdout, result.stdout)
        _write_console_text(sys.stderr, result.stderr)
        code = result.returncode
    elif args.ci:
        code, results = run_ci(args.package_root, args.package_archive)
    else:
        _parser().error("choose --list, --stage, or --ci")
    ending_commit = handoff_gate.git_head_sha()
    ending_dependencies = handoff_gate.load_dependencies()
    ending_hashes = handoff_gate.artifact_hashes(ending_dependencies)
    ending_source_hash = handoff_gate.release_source_hash(ending_dependencies)
    identity_stable = (
        starting_commit == ending_commit
        and starting_hashes == ending_hashes
        and starting_source_hash == ending_source_hash
    )
    if not identity_stable:
        print(
            "release identity changed while the gate was running",
            file=sys.stderr,
        )
        code = code or 1
    ending_candidate: dict[str, object] | None = None
    candidate_stable = not args.ci
    if args.ci and args.package_root is not None and args.package_archive is not None:
        try:
            ending_candidate = release_candidate.candidate_evidence(
                args.package_root,
                args.package_archive,
            )
            candidate_stable = starting_candidate == ending_candidate
        except release_candidate.CandidateError as exc:
            print(f"release candidate verification failed: {exc}", file=sys.stderr)
            candidate_stable = False
        if not candidate_stable:
            print("release candidate changed while the gate was running", file=sys.stderr)
            code = code or 1
    release_eligible = bool(
        args.ci
        and code == 0
        and identity_stable
        and candidate_stable
        and ending_candidate is not None
    )
    if args.json_report:
        args.json_report.parent.mkdir(parents=True, exist_ok=True)
        args.json_report.write_text(
            json.dumps(
                {
                    "schemaVersion": 2,
                    "ok": code == 0,
                    "releaseEligible": release_eligible,
                    "generatedAt": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
                    "commit": ending_commit,
                    "artifactHashes": ending_hashes,
                    "sourceHash": ending_source_hash,
                    "releaseCandidate": ending_candidate,
                    "results": [item.to_dict() for item in results],
                },
                ensure_ascii=False,
                indent=2,
            )
            + "\n",
            encoding="utf-8",
        )
    return code


def main(argv: list[str] | None = None) -> int:
    code: int | None = None
    try:
        code = _main(argv)
        return code
    finally:
        if QA_RUN_TEMP_DIR is not None:
            if code == 0:
                try:
                    _cleanup_qa_temp_dir()
                except OSError as exc:
                    print(
                        f"could not clean QA temporary evidence at {QA_RUN_TEMP_DIR}: {exc}",
                        file=sys.stderr,
                    )
            else:
                print(f"QA failure evidence retained at {QA_RUN_TEMP_DIR}", file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
