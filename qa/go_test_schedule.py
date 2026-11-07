"""Run the complete Go app inventory in two owned processes, then the other packages."""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from collections import Counter
from contextlib import ExitStack
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

if __package__ in (None, ""):  # Direct invocation from the sidecar working directory.
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from scripts.qa.windows_process_scope import ProcessLaunchSpec, WindowsProcessScope

SIDECAR_PACKAGE = "github.com/vibetable/vibetable/sidecar"
APP_PACKAGE = f"{SIDECAR_PACKAGE}/internal/app"
APP_TEST_TIMEOUT = "10m"
APP_TIMEOUT_SECONDS = 600
POLL_SECONDS = 0.05
APP_WORKER_FAILURE = "Go app worker execution failed"
_DIAGNOSTIC_ENV = (
    "VIBETABLE_PLUGIN_DATA_CAPTURE",
    "VIBETABLE_TEST_STARTUP_DIAGNOSTIC_DATA",
)
_TEST_NAME = re.compile(r"(?:Test|Example|Fuzz)\w*")
_DEFAULT_TEST_MAIN = [
    "m := testing.MainStart(testdeps.TestDeps{}, tests, benchmarks, fuzzTargets, examples)",
    "os.Exit(m.Run())",
    "}",
]


@dataclass(frozen=True, eq=False)
class Command:
    label: str
    args: tuple[str, ...]
    stdout: Path
    stderr: Path | None = None
    stdin: Path | None = None
    environment: dict[str, str] | None = None

    def launch(self, cwd: Path) -> WindowsProcessScope:
        executable = self.args[0]
        if not Path(executable).is_absolute():
            executable = shutil.which(executable) or executable
        with ExitStack() as files:
            output = files.enter_context(self.stdout.open("wb"))
            errors = output if self.stderr is None else files.enter_context(self.stderr.open("wb"))
            source = None if self.stdin is None else files.enter_context(self.stdin.open("rb"))
            return WindowsProcessScope.launch(
                ProcessLaunchSpec(
                    (executable, *self.args[1:]),
                    cwd=cwd,
                    env=self.environment,
                    stdin=source,
                    stdout=output,
                    stderr=errors,
                )
            )


@dataclass
class _Running:
    scope: WindowsProcessScope
    started: float
    root_exited: float | None = None


def _timing(label: str, status: str) -> None:
    print(f"Go {label} {status} at {datetime.now(UTC).isoformat()}", flush=True)


def _reclaim(active: dict[Command, _Running]) -> None:
    started = time.monotonic()
    # Signal all jobs before waiting for any one of them.
    for command, running in active.items():
        try:
            termination = running.scope.terminate_all(timeout=0)
            if not termination.termination_requested:
                print(f"Go {command.label} termination: {termination.errors}", file=sys.stderr)
        except (OSError, RuntimeError) as error:
            print(f"Go {command.label} termination: {error}", file=sys.stderr)
    for command, running in active.items():
        try:
            result = running.scope.wait_empty(timeout=5)
            if not result.success:
                print(f"Go {command.label} cleanup incomplete: {result}", file=sys.stderr)
        except (OSError, RuntimeError) as error:
            print(f"Go {command.label} cleanup: {error}", file=sys.stderr)
        finally:
            try:
                running.scope.close()
            except (OSError, RuntimeError) as error:
                print(f"Go {command.label} close: {error}", file=sys.stderr)
    print(f"Go process-tree failure cleanup: {time.monotonic() - started:.3f}s", flush=True)


def _supervise(commands: tuple[Command, ...], cwd: Path, *, timeout: int | None = None) -> None:
    # The outer QA stage owns the 900s budget, including any unbounded single command.
    deadline = None if timeout is None else time.monotonic() + timeout
    active: dict[Command, _Running] = {}

    def remaining() -> float:
        if deadline is None:
            return POLL_SECONDS
        seconds = deadline - time.monotonic()
        if seconds <= 0:
            raise TimeoutError(f"Go app exceeded its shared {timeout}s deadline")
        return min(seconds, POLL_SECONDS)

    try:
        for command in commands:
            remaining()
            _timing(command.label, f"started (output {command.stdout})")
            active[command] = _Running(command.launch(cwd), time.monotonic())
        while active:
            remaining()
            for command, running in tuple(active.items()):
                code = running.scope.root.poll()
                if code is None:
                    continue
                result = running.scope.wait_empty(timeout=0)
                if not result.success:
                    now = time.monotonic()
                    if running.root_exited is None:
                        running.root_exited = now
                    # The signaled root PID can briefly remain in the Job list.
                    # Any other PID or unobservable membership is already a failure.
                    if (
                        result.remaining_pids == (running.scope.root.pid,)
                        and now - running.root_exited < 1
                    ):
                        continue
                    raise RuntimeError(
                        f"Go {command.label} exited with an unverified Job: {result}"
                    )
                remaining()
                running.scope.close()
                del active[command]
                _timing(
                    command.label,
                    f"finished with empty Job, exit={code} ({time.monotonic() - running.started:.3f}s)",
                )
                if code:
                    raise subprocess.CalledProcessError(code, command.args)
            if active:
                time.sleep(remaining())
    finally:
        if active:
            _reclaim(active)


def _read_output(path: Path) -> str:
    return path.read_text(encoding="utf-8", errors="replace") if path.exists() else ""


def _show_output(path: Path | None, *, tail: bool = False) -> None:
    if path is not None:
        output = _read_output(path)
        if tail:
            output = "\n".join(output.split("\n")[-100:]).replace("\x16", "")
        if output:
            print(output, end="" if output.endswith("\n") else "\n", flush=True)


def run_command(
    command: tuple[str, ...],
    cwd: Path,
    directory: Path,
    label: str,
    *,
    echo: bool = True,
    environment: dict[str, str] | None = None,
) -> str:
    spec = Command(
        label,
        command,
        directory / f"{label}.stdout.log",
        directory / f"{label}.stderr.log",
        environment=environment,
    )
    try:
        _supervise((spec,), cwd)
    except OSError, RuntimeError, subprocess.CalledProcessError:
        _show_output(spec.stdout)
        raise
    finally:
        _show_output(spec.stderr)
    if echo:
        _show_output(spec.stdout)
    return _read_output(spec.stdout)


def is_windows_tempdir_cleanup_flake(output: str) -> bool:
    """Recognize only the narrow Windows TempDir cleanup race, never Go races."""

    if os.name != "nt":
        return False
    if "WARNING: DATA RACE" in output or "panic:" in output:
        return False
    if any(
        marker in output
        for marker in ("fatal error:", "[build failed]", "fork/exec", "paging file")
    ):
        # Mixed output also carries a toolchain failure (out-of-memory
        # abort, failed build, or a paging-file spawn error). Those are never
        # the cleanup race and must fail closed instead of consuming the
        # bounded cleanup retries.
        return False
    if "TempDir RemoveAll cleanup:" not in output:
        return False
    if "The directory is not empty" not in output:
        return False
    diagnostics = re.findall(r"^\s+([^:\r\n]+\.go):\d+:", output, flags=re.MULTILINE)
    return bool(diagnostics) and all(Path(source).name == "testing.go" for source in diagnostics)


def run_test_command(
    command: tuple[str, ...],
    cwd: Path,
    directory: Path,
    label: str,
    *,
    profile: Path | None = None,
) -> None:
    try:
        run_command(command, cwd, directory, label)
    except subprocess.CalledProcessError:
        output = _read_output(directory / f"{label}.stdout.log")
        errors = _read_output(directory / f"{label}.stderr.log")
        if not is_windows_tempdir_cleanup_flake(output + "\n" + errors):
            raise
        if profile is not None:
            profile.unlink(missing_ok=True)
        print(f"Go {label}: retrying known Windows TempDir cleanup failure once", flush=True)
        run_command(command, cwd, directory, f"{label}-retry")


def _remaining_packages(listing: str) -> tuple[str, ...]:
    packages = tuple(listing.splitlines())
    if (
        len(packages) < 2
        or APP_PACKAGE not in packages
        or len(set(packages)) != len(packages)
        or any(
            not (package == SIDECAR_PACKAGE or package.startswith(f"{SIDECAR_PACKAGE}/"))
            or "..." in package
            or any(character.isspace() for character in package)
            for package in packages
        )
    ):
        raise ValueError("go list returned an invalid full sidecar package inventory")
    return tuple(package for package in packages if package != APP_PACKAGE)


def _app_inventory(listing: str) -> tuple[str, ...]:
    tests = tuple(listing.splitlines())
    if (
        not tests
        or len(set(tests)) != len(tests)
        or any(not _TEST_NAME.fullmatch(test) for test in tests)
    ):
        raise ValueError("app binary returned an invalid Test/Example/Fuzz inventory")
    return tests


def _require_default_test_main(build_dir: Path) -> None:
    sources = list(build_dir.glob("go-build*/b*/_testmain.go"))
    if len(sources) != 1:
        raise ValueError("cannot verify the app binary's generated test main")
    _, marker, body = sources[0].read_text(encoding="utf-8").partition("\nfunc main() {\n")
    lines = [line.strip() for line in body.splitlines() if line.strip()]
    if not marker or lines != _DEFAULT_TEST_MAIN:
        raise ValueError("app worker scheduling requires Go's default test main")


def _verify_results(events: str, expected: tuple[str, ...], label: str) -> None:
    runs: Counter[str] = Counter()
    completed: Counter[str] = Counter()
    package_results = []
    outcomes = []
    failed = []
    for line in events.splitlines():
        event = json.loads(line)
        action = event["Action"]
        name = event.get("Test")
        if name is None:
            if action in {"pass", "skip", "fail"}:
                package_results.append(action)
        elif "/" not in name:
            if action == "run":
                runs[name] += 1
            elif action in {"pass", "skip", "fail"}:
                if action == "fail":
                    failed.append(name)
                else:
                    completed[name] += 1
                outcomes.append(f"{action.upper()} {name} ({event.get('Elapsed', 0)}s)")
    for outcome in outcomes:
        print(f"Go {label}: {outcome}", flush=True)
    inventory = Counter(expected)
    if package_results != ["pass"] or runs != inventory or completed != inventory or failed:
        raise ValueError(
            f"{label} did not run and complete its inventory exactly once: "
            f"missing starts={dict(inventory - runs)}, unexpected starts={dict(runs - inventory)}, "
            f"missing results={dict(inventory - completed)}, "
            f"unexpected results={dict(completed - inventory)}, failed={failed}, package={package_results}"
        )
    print(f"Go {label}: verified {len(expected)} top-level results", flush=True)


def _coverage_environment(directory: Path) -> dict[str, str]:
    directory.mkdir()
    return {**os.environ, "GOCOVERDIR": str(directory)}


def _run_app(
    go: str,
    cwd: Path,
    directory: Path,
    cover_options: tuple[str, ...],
    profiles: tuple[Path, ...],
) -> tuple[Path, ...]:
    for name in _DIAGNOSTIC_ENV:
        if name in os.environ:
            raise ValueError(f"Go app worker scheduling rejects the diagnostic environment {name}")
    with tempfile.TemporaryDirectory(dir=directory, prefix="app-") as scratch:
        build_dir = Path(scratch)
        executable = build_dir / "app.test.exe"
        build = Command(
            "app-build",
            (go, "test", "-c", "-work", *cover_options, "-o", str(executable), APP_PACKAGE),
            directory / "app-build.log",
            environment={**os.environ, "GOTMPDIR": scratch},
        )
        try:
            _supervise((build,), cwd)
        finally:
            _show_output(build.stdout)
        _require_default_test_main(build_dir)
        app_cwd = cwd / "internal" / "app"
        listing = run_command(
            (str(executable), "-test.list=^(Test|Example|Fuzz)"),
            app_cwd,
            directory,
            "app-inventory",
            echo=False,
            environment=_coverage_environment(build_dir / "cover-list") if profiles else None,
        )
        inventory = _app_inventory(listing)
        partitions = tuple(part for part in (inventory[::2], inventory[1::2]) if part)
        parts = profiles[: len(partitions)]
        commands = []
        for index, part in enumerate(partitions):
            label = f"app-worker-{index + 1}"
            pattern = "^(" + "|".join(map(re.escape, part)) + ")$"
            cover: tuple[str, ...] = ()
            environment = None
            if parts:
                counters = build_dir / f"cover-{index + 1}"
                environment = _coverage_environment(counters)
                cover = (f"-test.coverprofile={parts[index]}", f"-test.gocoverdir={counters}")
            commands.append(
                Command(
                    label,
                    (
                        str(executable),
                        "-test.count=1",
                        f"-test.timeout={APP_TEST_TIMEOUT}",
                        "-test.v=test2json",
                        "-test.paniconexit0",
                        f"-test.run={pattern}",
                        *cover,
                    ),
                    directory / f"{label}.log",
                    environment=environment,
                )
            )
            print(f"Go {label} inventory: {len(part)} of {len(inventory)}", flush=True)
        try:
            _supervise(tuple(commands), app_cwd, timeout=APP_TIMEOUT_SECONDS)
            for command, expected in zip(commands, partitions, strict=True):
                converted = Command(
                    f"{command.label}-results",
                    (go, "tool", "test2json", "-t", "-p", APP_PACKAGE),
                    directory / f"{command.label}.jsonl",
                    directory / f"{command.label}-results.stderr.log",
                    stdin=command.stdout,
                )
                try:
                    _supervise((converted,), cwd)
                finally:
                    _show_output(converted.stderr)
                _verify_results(_read_output(converted.stdout), expected, command.label)
        except (OSError, RuntimeError, ValueError, subprocess.CalledProcessError) as error:
            for command in commands:
                _show_output(command.stdout, tail=True)
            raise RuntimeError(f"{APP_WORKER_FAILURE}: {error}") from error
    return parts


def evidence_directory(cwd: Path, label: str) -> Path:
    root = cwd.parent / "build" / "qa" / "go-test-schedule"
    root.mkdir(parents=True, exist_ok=True)
    return Path(tempfile.mkdtemp(prefix=f"{label}-", dir=root))


def run_full_suite(
    go: str,
    cwd: Path,
    directory: Path,
    *,
    cover_options: tuple[str, ...] = (),
    profile_dir: Path | None = None,
) -> tuple[Path, ...]:
    packages = _remaining_packages(
        run_command((go, "list", "./..."), cwd, directory, "packages", echo=False)
    )
    profiles = () if profile_dir is None else tuple(profile_dir / f"app-{n}.out" for n in (1, 2))
    parts = _run_app(go, cwd, directory, cover_options, profiles)
    remaining = () if profile_dir is None else (profile_dir / "remaining.out",)
    options = (*cover_options, f"-coverprofile={remaining[0]}") if remaining else ()
    print(f"Go remaining inventory: {len(packages)} packages", flush=True)
    run_test_command(
        (go, "test", "-count=1", *options, *packages),
        cwd,
        directory,
        "remaining-test",
        profile=remaining[0] if remaining else None,
    )
    return (*parts, *remaining)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    args = parser.parse_args(argv)
    cwd = Path.cwd()
    try:
        run_full_suite(args.go, cwd, evidence_directory(cwd, "plain"))
    except subprocess.CalledProcessError as error:
        return error.returncode
    except (OSError, RuntimeError, ValueError) as error:
        print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
