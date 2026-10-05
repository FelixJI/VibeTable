"""Real WPF/WebView2 product E2E orchestration.

The runner starts the packaged WPF host and connects Playwright to that
process's WebView2 debugging endpoint.  It intentionally never launches a
Chromium/Edge process itself.
"""

from __future__ import annotations

import argparse
import base64
import ctypes
import hashlib
import ipaddress
import json
import math
import os
import re
import shutil
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid
import zipfile
from collections.abc import Callable, Iterable, Iterator, Mapping, Sequence
from contextlib import ExitStack, contextmanager
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import IO, Any, Protocol

ROOT = Path(__file__).resolve().parents[2]
NODE_RUNNER = Path(__file__).with_name("webview_product_scenarios.mjs")
DEFAULT_EVIDENCE = ROOT / "build" / "qa" / "product-e2e"
CDP_TIMEOUT_SECONDS = 60.0
NORMAL_CLOSE_CONTROL_FILE = "host-normal-close.request"
LIFECYCLE_EXIT_TIMEOUT_SECONDS = 35.0

if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from qa.package_check import check_package  # noqa: E402
from qa.product_scenario_manifest import (  # noqa: E402
    Scenario,
    load_scenarios,
    select_scenarios,
)
from scripts.build_next import RepoPaths  # noqa: E402
from scripts.node_toolchain import ensure_node  # noqa: E402
from scripts.qa._windows_tcp_table import query_windows_tcp_table  # noqa: E402
from scripts.qa.windows_process_scope import (  # noqa: E402
    ProcessLaunchSpec,
    ProcessScopeSnapshot,
    ProcessWorkingSetSnapshot,
    ScopeTerminationResult,
    ScopeWaitResult,
    TargetTerminationResult,
    WindowsProcessScope,
)
from tests.e2e.windows_tcp_listener_owner import (  # noqa: E402
    OwnerLeaseCleanupReport,
    PortReleaseReport,
    WindowsTcpListenerOwnerLease,
)

DEFAULT_PACKAGE = RepoPaths.default(ROOT).publish_root


@dataclass(frozen=True)
class _PersistentScenarioRun:
    """Persistent data paths shared by a two-phase product acceptance."""

    phase: str
    state_path: Path
    scenario_dir: Path
    readiness_dir: Path
    workspace_root: Path


_NATURAL_AGING_SCENARIO = Scenario(
    id="a1-natural-retention",
    title="A1 natural retention aging",
    requirement="A1 two-phase 24-hour natural retention acceptance",
)

# S40: the legal-capacity Host acceptance. The opt-in Go test-only producer
# builds two fresh synthetic workspaces (near-limit documents/revisions and
# the maximum 4096-revision chain); each scale is then opened by its own real
# cold Host through the normal connect/register/open UI path.
CAPACITY_SCENARIO_ID = "40-file-history-capacity"
CAPACITY_FIXTURE_ENV = "VIBETABLE_415_CAPACITY_HOST_FIXTURE_ROOT"
CAPACITY_FIXTURE_BASE = ROOT / "build" / "qa" / "415-capacity"
CAPACITY_FIXTURE_SCALES = ("near-limit-9980x9990", "depth-4096")
CAPACITY_PRODUCER_TEST = "TestCapacityHostFixtureProduction"
CAPACITY_PRODUCER_TIMEOUT_SECONDS = 15 * 60
CAPACITY_REQUIRED_FIXTURE_FIELDS = (
    "name",
    "workspaceRoot",
    "manifestPath",
    "selectedMultiDocumentId",
    "selectedMultiRelativePath",
)
CAPACITY_REQUIRED_IDENTITY_FIELDS = ("workspaceId", "sessionEpoch")

# Scenarios that prove persistence across a real Host restart run the shared
# seed/resume acceptance below. Each entry names the seed result fields its
# resume phase needs; the two scenarios deliberately require different seed
# contracts instead of sharing a generic state shape.
_PERSISTENT_RESTART_SEED_FIELDS: Mapping[str, tuple[str, ...]] = {
    "33-host-grid-presentation": (
        "workspaceId",
        "tableId",
        "fields",
        "state",
        "revision",
        "commands",
    ),
    "11-plugin-mutation": (
        "workspaceId",
        "projectKey",
        "pluginId",
        "tableId",
        "packageHash",
    ),
    "39-file-workflow-combination": (
        "workspaceId",
        "chain",
        "files",
        "recordId",
    ),
    "44-file-restore-crash": ("workspaceId", "restoreCrash"),
}


class _ScopeRoot(Protocol):
    pid: int

    def poll(self) -> int | None: ...

    def wait(self, timeout: float | None = None) -> int: ...


class _RootScope(Protocol):
    @property
    def root(self) -> _ScopeRoot: ...


class _SnapshotScope(_RootScope, Protocol):
    def snapshot(self) -> ProcessScopeSnapshot: ...


class _FaultScope(Protocol):
    def snapshot(self) -> ProcessScopeSnapshot: ...

    def terminate_unique(self, executable_name: str) -> TargetTerminationResult: ...


class _TerminationScope(Protocol):
    def terminate_all(self) -> ScopeTerminationResult: ...


class _CloseScope(Protocol):
    def close(self) -> None: ...


class _LifecycleScope(_SnapshotScope, _TerminationScope, Protocol):
    def wait_empty(self, *, timeout: float = 5.0) -> ScopeWaitResult: ...


class _ManagedScope(_LifecycleScope, _CloseScope, Protocol):
    pass


class _HostObservationScope(_LifecycleScope, _FaultScope, Protocol):
    def working_set_snapshot(self) -> ProcessWorkingSetSnapshot: ...


class _PortOwnerLease(Protocol):
    def observe_release(self, *, timeout: float) -> PortReleaseReport: ...

    def close(self) -> OwnerLeaseCleanupReport: ...


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _package_layout_path(package_root: Path) -> Path:
    packaged = package_root / "resources" / "publish-layout.json"
    return packaged if packaged.is_file() else package_root / "publish-layout.json"


def package_fingerprint(package_root: Path) -> dict[str, Any]:
    files = sorted(path for path in package_root.rglob("*") if path.is_file())
    aggregate = hashlib.sha256()
    entries: dict[str, str] = {}
    for file_path in files:
        relative = file_path.relative_to(package_root).as_posix()
        digest = _sha256(file_path)
        entries[relative] = digest
        aggregate.update(relative.encode("utf-8"))
        aggregate.update(b"\0")
        aggregate.update(digest.encode("ascii"))
        aggregate.update(b"\n")
    return {
        "algorithm": "sha256(path\\0sha256\\n)",
        "packageSha256": aggregate.hexdigest(),
        "fileCount": len(entries),
        "files": entries,
    }


def _latest(paths: Iterable[Path]) -> tuple[float, str] | None:
    newest: tuple[float, str] | None = None
    for path in paths:
        if not path.is_file():
            continue
        value = (path.stat().st_mtime, str(path.relative_to(ROOT)))
        if newest is None or value[0] > newest[0]:
            newest = value
    return newest


def _source_files(base: Path, patterns: Sequence[str]) -> list[Path]:
    result: list[Path] = []
    for pattern in patterns:
        result.extend(
            path
            for path in base.rglob(pattern)
            if "bin" not in path.parts
            and "obj" not in path.parts
            and "node_modules" not in path.parts
            and not path.name.endswith((".test.ts", ".spec.ts", "_test.go"))
            and path.is_file()
        )
    return result


def package_freshness(package_root: Path) -> dict[str, Any]:
    layout = json.loads(_package_layout_path(package_root).read_text(encoding="utf-8"))
    launch = layout["launch"]
    host_artifact = package_root / "VibeTable.Desktop.dll"
    if not host_artifact.is_file():
        host_artifact = package_root / launch["host"]
    checks: list[tuple[str, Path, list[Path]]] = [
        (
            "desktop-host",
            host_artifact,
            [
                *_source_files(
                    ROOT / "desktop" / "src",
                    ("*.cs", "*.xaml", "*.csproj"),
                ),
                ROOT / "desktop" / "Directory.Build.props",
            ],
        ),
        (
            "web-grid",
            package_root / launch["webGrid"] / "index.html",
            [
                *_source_files(
                    ROOT / "desktop" / "web-grid" / "src",
                    ("*.ts", "*.vue", "*.css", "*.html"),
                ),
                ROOT / "desktop" / "web-grid" / "package.json",
                ROOT / "desktop" / "web-grid" / "package-lock.json",
                ROOT / "desktop" / "web-grid" / "vite.config.ts",
            ],
        ),
        (
            "python-backend",
            package_root / launch["backend"],
            [
                *_source_files(ROOT / "backend", ("*.py",)),
                ROOT / "pyproject.toml",
                ROOT / "uv.lock",
            ],
        ),
        (
            "pocketbase-sidecar",
            package_root / launch["sidecar"],
            [
                *_source_files(ROOT / "sidecar", ("*.go", "*.json")),
                ROOT / "sidecar" / "go.mod",
                ROOT / "sidecar" / "go.sum",
            ],
        ),
    ]
    results: list[dict[str, Any]] = []
    for name, artifact, sources in checks:
        newest = _latest(sources)
        artifact_time = artifact.stat().st_mtime if artifact.is_file() else None
        fresh = (
            artifact_time is not None and newest is not None and artifact_time + 1.0 >= newest[0]
        )
        results.append(
            {
                "component": name,
                "fresh": fresh,
                "artifact": str(artifact.relative_to(package_root)),
                "artifactMtime": artifact_time,
                "newestSource": newest[1] if newest else None,
                "newestSourceMtime": newest[0] if newest else None,
            }
        )
    return {
        "passed": all(item["fresh"] for item in results),
        "components": results,
    }


def audit_package(package_root: Path) -> dict[str, Any]:
    package_root = package_root.resolve()
    errors: list[str] = []
    if not package_root.is_dir():
        return {
            "passed": False,
            "packageRoot": str(package_root),
            "errors": [f"package directory does not exist: {package_root}"],
        }
    try:
        errors.extend(check_package(package_root))
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        errors.append(str(exc))
    try:
        freshness = package_freshness(package_root)
    except (OSError, KeyError, TypeError, json.JSONDecodeError) as exc:
        freshness = {"passed": False, "components": [], "error": str(exc)}
    if not freshness["passed"]:
        errors.append("package is older than one or more product source inputs")
    try:
        fingerprint = package_fingerprint(package_root)
    except OSError as exc:
        fingerprint = {"error": str(exc)}
        errors.append(f"could not fingerprint package: {exc}")
    return {
        "passed": not errors,
        "packageRoot": str(package_root),
        "errors": errors,
        "freshness": freshness,
        "fingerprint": fingerprint,
    }


def _reserve_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def _launch_host_process(
    command: Sequence[str],
    *,
    cwd: Path,
    env: Mapping[str, str],
    stdout: IO[bytes],
    stderr: IO[bytes],
    before_process_create: Callable[[], None] | None = None,
) -> WindowsProcessScope:
    return WindowsProcessScope.launch(
        ProcessLaunchSpec(
            command,
            cwd=cwd,
            env=env,
            stdout=stdout,
            stderr=stderr,
            before_process_create=before_process_create,
        )
    )


def _wait_for_cdp(
    port: int,
    scope: _SnapshotScope,
    process_network: dict[str, Any] | None = None,
    readiness_dir: Path | None = None,
) -> None:
    endpoint = f"http://127.0.0.1:{port}/json/version"
    deadline = time.monotonic() + CDP_TIMEOUT_SECONDS
    last_error = ""
    while time.monotonic() < deadline:
        if process_network is not None:
            _record_process_network(scope, process_network)
        if readiness_dir is not None:
            readiness = _read_json(readiness_dir / "vibetable-readiness.json")
            startup_error = readiness.get("error") if readiness is not None else None
            if (
                readiness is not None
                and readiness.get("ready") is False
                and isinstance(startup_error, str)
                and startup_error.strip()
            ):
                raise RuntimeError(
                    f"WPF host startup failed before CDP became ready: {startup_error}"
                )
        exit_code = scope.root.poll()
        if exit_code is not None:
            raise RuntimeError(f"WPF host exited with code {exit_code} before CDP became ready")
        try:
            with urllib.request.urlopen(endpoint, timeout=0.5) as response:
                payload = json.load(response)
            if payload.get("webSocketDebuggerUrl"):
                return
        except (OSError, urllib.error.URLError, json.JSONDecodeError) as exc:
            last_error = str(exc)
        time.sleep(0.1)
    raise TimeoutError(f"WebView2 CDP endpoint was not ready: {last_error}")


def _read_json(path: Path) -> dict[str, Any] | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except OSError, json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def _write_json_atomic(path: Path, value: dict[str, Any]) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    for attempt in range(20):
        try:
            os.replace(temporary, path)
            return
        except PermissionError:
            if attempt == 19:
                raise
            # Windows can briefly deny replacement while the Node consumer or
            # endpoint protection is closing its read handle. Keep the write
            # atomic and bounded instead of falling back to an in-place write.
            time.sleep(min(0.01 * (attempt + 1), 0.1))


def _wait_for_readiness(readiness_dir: Path, scope: _RootScope) -> dict[str, Any]:
    path = readiness_dir / "vibetable-readiness.json"
    deadline = time.monotonic() + CDP_TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        report = _read_json(path)
        if report is not None:
            return report
        exit_code = scope.root.poll()
        if exit_code is not None:
            raise RuntimeError(f"WPF host exited with code {exit_code} before readiness")
        time.sleep(0.1)
    raise TimeoutError("WPF host did not emit vibetable-readiness.json")


def _lifecycle_exit_report(
    *,
    normal_exit_requested: bool,
    host_exit_code: int | None,
    members_after_exit: list[dict[str, Any]],
    ports_released: bool,
    root_pid: int | None = None,
    port_release: dict[str, object] | None = None,
    owner_lease_cleanup: dict[str, object] | None = None,
    cleanup: dict[str, Any] | None = None,
    errors: Sequence[str] = (),
) -> dict[str, Any]:
    passed = (
        normal_exit_requested
        and host_exit_code == 0
        and not members_after_exit
        and ports_released
        and not errors
    )
    report: dict[str, Any] = {
        "normalExitRequested": normal_exit_requested,
        "hostExitCode": host_exit_code,
        "membersAfterExit": members_after_exit,
        "descendantsAfterExit": [
            {"pid": member["pid"], "name": member.get("name", "unknown")}
            for member in members_after_exit
            if member["pid"] != root_pid
        ],
        "portsReleased": ports_released,
        "errors": list(errors),
        "status": "passed" if passed else "failed",
    }
    if cleanup is not None:
        report["cleanup"] = cleanup
    if port_release is not None:
        report["portRelease"] = port_release
    if owner_lease_cleanup is not None:
        report["ownerLeaseCleanup"] = owner_lease_cleanup
    return report


def _termination_report(result: ScopeTerminationResult) -> dict[str, Any]:
    return {
        "terminationRequested": result.termination_requested,
        "remainingPids": (
            list(result.remaining_pids) if result.remaining_pids is not None else None
        ),
        "errors": list(result.errors),
        "status": "passed" if result.success else "failed",
    }


def _terminate_scope(scope: _TerminationScope) -> dict[str, Any]:
    try:
        return _termination_report(scope.terminate_all())
    except (OSError, RuntimeError) as exc:
        return {
            "terminationRequested": False,
            "remainingPids": None,
            "errors": [str(exc)],
            "status": "failed",
        }


def _close_scope(scope: _CloseScope) -> str | None:
    try:
        scope.close()
    except (OSError, RuntimeError) as exc:
        return str(exc)
    return None


@contextmanager
def _product_scope_lifetime(
    scope: _ManagedScope,
    state: dict[str, Any],
) -> Iterator[None]:
    primary_error: BaseException | None = None
    try:
        yield
    except BaseException as exc:
        primary_error = exc
        raise
    finally:
        result = state.get("result")
        prior_cleanup: object = None
        if isinstance(result, dict):
            prior_lifecycle = result.get("lifecycle")
            if isinstance(prior_lifecycle, dict):
                prior_cleanup = prior_lifecycle.get("cleanup")
        cleanup = (
            prior_cleanup
            if isinstance(prior_cleanup, dict) and prior_cleanup.get("status") == "passed"
            else _terminate_scope(scope)
        )
        close_error = _close_scope(scope)
        if close_error is not None:
            cleanup["errors"].append(close_error)
            cleanup["status"] = "failed"
        failures = [str(item) for item in cleanup["errors"]]
        if isinstance(result, dict):
            lifecycle = result.get("lifecycle")
            if isinstance(lifecycle, dict):
                lifecycle["finalCleanup"] = cleanup
                if failures:
                    lifecycle_errors = lifecycle.setdefault("errors", [])
                    lifecycle_errors.extend(
                        failure for failure in failures if failure not in lifecycle_errors
                    )
                    lifecycle["status"] = "failed"
                    result["lifecycleError"] = "; ".join(lifecycle_errors)
                    if result.get("status") == "passed":
                        result["status"] = "failed"
                        result["error"] = {
                            "code": "HOST_LIFECYCLE_FAILED",
                            "message": "Job 作用域最终清理失败。",
                            "details": lifecycle,
                        }
                    else:
                        result["lifecycleFailure"] = lifecycle
        elif failures:
            message = "Job scope cleanup also failed: " + "; ".join(failures)
            if primary_error is not None:
                primary_error.add_note(message)
            else:
                raise RuntimeError(message)


def _scope_members(scope: _SnapshotScope) -> list[dict[str, Any]]:
    return [
        {
            "pid": member.pid,
            "name": getattr(member, "executable_name", "unknown"),
            "identityVerified": getattr(member, "identity_verified", False),
        }
        for member in scope.snapshot().members
    ]


def _close_owner_lease(cdp_owner: _PortOwnerLease) -> OwnerLeaseCleanupReport:
    try:
        cleanup = cdp_owner.close()
    except (OSError, RuntimeError) as exc:
        return OwnerLeaseCleanupReport(
            stable_handle_closed=False,
            errors=(f"unable to close captured CDP owner handle ({type(exc).__name__})",),
        )
    if cleanup is None:
        return OwnerLeaseCleanupReport(
            stable_handle_closed=False,
            errors=("captured CDP owner cleanup returned no report",),
        )
    return cleanup


@contextmanager
def _close_owner_on_primary_error(cdp_owner: _PortOwnerLease) -> Iterator[None]:
    try:
        yield
    except BaseException as exc:
        cleanup = _close_owner_lease(cdp_owner)
        for error in cleanup.errors:
            exc.add_note(error)
        raise


def _abort_scope(
    scope: _LifecycleScope,
    *,
    reason: str,
    cdp_owner: _PortOwnerLease | None = None,
) -> dict[str, Any]:
    errors = [reason]
    try:
        host_exit_code = scope.root.poll()
    except RuntimeError as exc:
        host_exit_code = None
        errors.append(str(exc))
    try:
        members = _scope_members(scope)
    except (OSError, RuntimeError) as exc:
        members = []
        errors.append(str(exc))
    owner_cleanup = None if cdp_owner is None else _close_owner_lease(cdp_owner)
    if owner_cleanup is not None:
        errors.extend(owner_cleanup.errors)
    return _lifecycle_exit_report(
        normal_exit_requested=False,
        host_exit_code=host_exit_code,
        members_after_exit=members,
        ports_released=False,
        root_pid=scope.root.pid,
        cleanup=_terminate_scope(scope),
        owner_lease_cleanup=(None if owner_cleanup is None else owner_cleanup.as_artifact()),
        errors=errors,
    )


def _request_normal_exit(
    scope: _LifecycleScope,
    *,
    controls_dir: Path,
    cdp_owner: _PortOwnerLease,
) -> dict[str, Any]:
    control = controls_dir / NORMAL_CLOSE_CONTROL_FILE
    control.write_text("normal-close\n", encoding="utf-8")
    return _observe_scope_exit(scope, cdp_owner=cdp_owner)


def _observe_scope_exit(
    scope: _LifecycleScope,
    *,
    cdp_owner: _PortOwnerLease,
) -> dict[str, Any]:
    deadline = time.monotonic() + LIFECYCLE_EXIT_TIMEOUT_SECONDS

    def remaining() -> float:
        return max(0.0, deadline - time.monotonic())

    host_exit_code: int | None = None
    errors: list[str] = []
    try:
        host_exit_code = scope.root.wait(timeout=min(30.0, remaining()))
    except subprocess.TimeoutExpired:
        pass
    except (OSError, RuntimeError) as exc:
        errors.append(str(exc))
    wait_result = scope.wait_empty(timeout=min(5.0, remaining()))
    errors.extend(wait_result.errors)
    port_release: PortReleaseReport | None = None
    try:
        members_after_exit = _scope_members(scope)
        port_release = cdp_owner.observe_release(timeout=remaining())
        ports_released = port_release.released
        errors.extend(port_release.errors)
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        members_after_exit = []
        ports_released = False
        errors.append(f"CDP listener ownership observation failed ({type(exc).__name__})")
    owner_cleanup = _close_owner_lease(cdp_owner)
    errors.extend(owner_cleanup.errors)
    cleanup = None
    if host_exit_code is None or members_after_exit or errors or not ports_released:
        cleanup = _terminate_scope(scope)
    return _lifecycle_exit_report(
        normal_exit_requested=True,
        host_exit_code=host_exit_code,
        members_after_exit=members_after_exit,
        ports_released=ports_released,
        root_pid=scope.root.pid,
        port_release=(None if port_release is None else port_release.as_artifact()),
        owner_lease_cleanup=owner_cleanup.as_artifact(),
        cleanup=cleanup,
        errors=errors,
    )


def _endpoint_host(endpoint: str) -> str:
    if endpoint.startswith("["):
        closing = endpoint.find("]")
        return endpoint[1:closing] if closing > 0 else endpoint
    return endpoint.rsplit(":", 1)[0]


def _is_loopback_endpoint(endpoint: str) -> bool:
    host = _endpoint_host(endpoint)
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return host.casefold() == "localhost"


def _is_unspecified_endpoint(endpoint: str) -> bool:
    host = _endpoint_host(endpoint)
    try:
        return ipaddress.ip_address(host).is_unspecified
    except ValueError:
        return False


def _record_process_network(
    scope: _SnapshotScope,
    evidence: dict[str, Any],
) -> None:
    try:
        before_snapshot = scope.snapshot()
        before = {member.pid: member for member in before_snapshot.members}
        rows = query_windows_tcp_table(timeout=10.0)
        after_snapshot = scope.snapshot()
        after = {member.pid: member for member in after_snapshot.members}
        members = set(before) & set(after)
        observed = evidence.setdefault("observations", {})
        for row in rows:
            pid = row.pid
            if pid not in members:
                continue
            member = after[pid]
            name = (
                getattr(member, "executable_name", "unknown")
                if getattr(member, "identity_verified", False)
                else "unknown"
            )
            item = row.as_artifact() | {"processName": name}
            key = "|".join(str(item[field]) for field in ("pid", "local", "remote", "state"))
            observed[key] = item
        evidence["samples"] = int(evidence.get("samples", 0)) + 1
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        errors = evidence.setdefault("errors", [])
        message = str(exc)
        if message not in errors:
            errors.append(message)


def _process_network_report(evidence: dict[str, Any], *, status: str) -> dict[str, Any]:
    observations = list(evidence.get("observations", {}).values())
    observations.sort(
        key=lambda item: (
            int(item["pid"]),
            str(item["local"]),
            str(item["remote"]),
            str(item["state"]),
        )
    )
    unexpected = []
    for item in observations:
        if item["state"] == "LISTENING" or _is_unspecified_endpoint(str(item["remote"])):
            if not _is_loopback_endpoint(str(item["local"])):
                unexpected.append(item | {"reason": "non_loopback_listener"})
        elif not _is_loopback_endpoint(str(item["remote"])):
            unexpected.append(item | {"reason": "non_loopback_remote"})
    webview_runtime_background = [
        item
        for item in unexpected
        if str(item.get("processName", "")).casefold() == "msedgewebview2.exe"
    ]
    product_unexpected = [
        item
        for item in unexpected
        if str(item.get("processName", "")).casefold() != "msedgewebview2.exe"
    ]
    errors = list(evidence.get("errors", []))
    return {
        "status": status if not errors else "failed",
        "source": "netstat -ano -p tcp",
        "scope": (
            "VibeTable-owned processes must use loopback; WebView2 Runtime "
            "background traffic is retained as a non-gating diagnostic"
        ),
        "samples": int(evidence.get("samples", 0)),
        "observations": observations,
        "unexpectedNonLoopback": unexpected,
        "unexpectedProductNonLoopback": product_unexpected,
        "webViewRuntimeBackgroundNetwork": webview_runtime_background,
        "errors": errors,
    }


def _handle_storage_proof(
    request: dict[str, Any],
    local_data: Path,
) -> dict[str, Any]:
    request_id = request.get("requestId")
    if not isinstance(request_id, str) or not request_id:
        return {
            "status": "failed",
            "code": "STORAGE_PROOF_REQUEST_ID_REQUIRED",
        }
    table_id = request.get("tableId")
    if not isinstance(table_id, str) or not table_id:
        return {
            "status": "failed",
            "code": "STORAGE_PROOF_TABLE_REQUIRED",
            "requestId": request_id,
        }
    data_db = local_data / "pocketbase" / "data.db"
    if not data_db.is_file():
        registry_path = local_data / "VibeTable" / "shell" / "workspace-registry-v2.json"
        try:
            registry = json.loads(registry_path.read_text(encoding="utf-8"))
            workspaces = registry.get("workspaces")
            if not isinstance(workspaces, list) or not workspaces:
                raise ValueError("workspace registry contains no workspaces")
            latest = max(
                (item for item in workspaces if isinstance(item, dict)),
                key=lambda item: str(item.get("lastOpenedAt", "")),
            )
            selected_root = latest.get("selectedRoot")
            if not isinstance(selected_root, str) or not selected_root:
                raise ValueError("workspace registry selectedRoot is invalid")
            data_db = Path(selected_root) / ".vibetable" / "data" / "data.db"
        except OSError, ValueError, json.JSONDecodeError:
            pass
    if not data_db.is_file():
        return {
            "status": "failed",
            "code": "STORAGE_PROOF_DATABASE_MISSING",
            "requestId": request_id,
            "database": str(data_db),
        }
    try:
        connection = sqlite3.connect(
            f"{data_db.resolve().as_uri()}?mode=ro",
            uri=True,
            timeout=5,
        )
        try:
            row = connection.execute(
                "SELECT physical_name FROM vibetable_tables WHERE table_id = ?",
                (table_id,),
            ).fetchone()
            if row is None or not isinstance(row[0], str):
                raise RuntimeError(f"table definition not found: {table_id}")
            physical_name = row[0]
            if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", physical_name):
                raise RuntimeError(f"table definition has unsafe physical name: {physical_name!r}")
            records = connection.execute(f'SELECT COUNT(*) FROM "{physical_name}"').fetchone()[0]
            audit = connection.execute(
                "SELECT COUNT(*) FROM vibetable_audit_events WHERE table_id = ?",
                (table_id,),
            ).fetchone()[0]
            idempotency = connection.execute(
                """
                SELECT COUNT(*) FROM vibetable_idempotency_keys
                WHERE key NOT LIKE 'metadata:%'
                  AND key NOT LIKE 'field-v2:%'
                """
            ).fetchone()[0]
            outbox = connection.execute(
                """
                SELECT COUNT(*) FROM vibetable_outbox
                WHERE payload_json LIKE ?
                """,
                (f'%"tableId":"{table_id}"%',),
            ).fetchone()[0]
        finally:
            connection.close()
    except (OSError, RuntimeError, sqlite3.Error) as exc:
        return {
            "status": "failed",
            "code": "STORAGE_PROOF_READ_FAILED",
            "requestId": request_id,
            "message": str(exc),
        }
    try:
        audit_ledger = _audit_ledger_proof(data_db.parent.parent / "audit" / "ledger.db")
    except (OSError, RuntimeError, sqlite3.Error, TypeError, ValueError) as exc:
        return {
            "status": "failed",
            "code": "AUDIT_LEDGER_PROOF_FAILED",
            "requestId": request_id,
            "message": str(exc),
        }
    return {
        "status": "completed",
        "requestId": request_id,
        "tableId": table_id,
        "physicalName": physical_name,
        "database": {
            "path": str(data_db),
            "readOnly": True,
        },
        "counts": {
            "records": int(records),
            "audit": int(audit),
            "idempotency": int(idempotency),
            "outbox": int(outbox),
        },
        "auditLedger": audit_ledger,
    }


def _audit_ledger_proof(ledger_path: Path) -> dict[str, Any]:
    if not ledger_path.is_file():
        raise RuntimeError(f"audit ledger not found: {ledger_path}")
    connection = sqlite3.connect(
        f"{ledger_path.resolve().as_uri()}?mode=ro",
        uri=True,
        timeout=5,
    )
    try:
        rows = connection.execute(
            """
            SELECT ledger_sequence, event_id, source_epoch, source_sequence,
                   mutation_identity, payload_hash, payload, occurred_at,
                   previous_hash, hash
            FROM audit_ledger
            ORDER BY ledger_sequence
            """
        ).fetchall()
    finally:
        connection.close()
    previous_hash = ""
    records: list[dict[str, Any]] = []
    source_high_watermarks: dict[str, int] = {}
    for expected_sequence, row in enumerate(rows, start=1):
        (
            ledger_sequence,
            event_id,
            source_epoch,
            source_sequence,
            mutation_identity,
            payload_hash,
            payload_raw,
            occurred_at,
            linked_previous_hash,
            record_hash,
        ) = row
        payload_bytes = bytes(payload_raw)
        expected_payload_hash = "sha256:" + hashlib.sha256(payload_bytes).hexdigest()
        payload = json.loads(payload_bytes)
        envelope = {
            "eventId": event_id,
            "sourceEpoch": source_epoch,
            "sourceSequence": int(source_sequence),
            "mutationIdentity": mutation_identity,
            "payloadHash": payload_hash,
            "payload": payload,
            "occurredAt": occurred_at,
        }
        hash_input = {
            "ledgerSequence": int(ledger_sequence),
            "previousHash": linked_previous_hash,
            "envelope": envelope,
        }
        expected_record_hash = (
            "sha256:"
            + hashlib.sha256(
                json.dumps(
                    hash_input,
                    ensure_ascii=False,
                    separators=(",", ":"),
                ).encode("utf-8")
            ).hexdigest()
        )
        if (
            ledger_sequence != expected_sequence
            or linked_previous_hash != previous_hash
            or payload_hash != expected_payload_hash
            or record_hash != expected_record_hash
        ):
            raise RuntimeError(f"audit ledger chain is invalid at sequence {ledger_sequence}")
        prior_source_sequence = source_high_watermarks.get(source_epoch, 0)
        if source_sequence != prior_source_sequence + 1:
            raise RuntimeError(f"audit source sequence is invalid for {source_epoch!r}")
        source_high_watermarks[source_epoch] = int(source_sequence)
        previous_hash = record_hash
        records.append(
            {
                "ledgerSequence": int(ledger_sequence),
                "eventId": event_id,
                "sourceEpoch": source_epoch,
                "sourceSequence": int(source_sequence),
                "mutationIdentity": mutation_identity,
                "payload": payload,
                "hash": record_hash,
            }
        )
    return {
        "path": str(ledger_path),
        "readOnly": True,
        "verified": True,
        "count": len(records),
        "anchorHash": previous_hash,
        "sourceHighWatermarks": source_high_watermarks,
        "records": records,
    }


def _handle_fault_request(request: dict[str, Any], host_scope: _FaultScope) -> dict[str, Any]:
    action = request.get("action")
    if action == "observe-processes":
        try:
            snapshot = host_scope.snapshot()
        except (OSError, RuntimeError) as exc:
            return {
                "status": "failed",
                "code": "PROCESS_SCOPE_OBSERVATION_FAILED",
                "errors": [str(exc)],
            }
        members = [
            {
                "pid": member.pid,
                "processName": member.executable_name,
                "identityVerified": member.identity_verified,
            }
            for member in snapshot.members
        ]
        if any(not member.identity_verified for member in snapshot.members):
            return {
                "status": "failed",
                "code": "PROCESS_SCOPE_MEMBER_UNVERIFIED",
                "members": members,
            }
        return {"status": "completed", "action": action, "members": members}
    targets = {
        "kill-sidecar": (
            "vibetable-pb.exe",
            "SIDECAR_PROCESS_NOT_FOUND",
            "SIDECAR_PROCESS_NOT_UNIQUE",
        ),
        "kill-backend": (
            "vibetable-backend.exe",
            "BACKEND_PROCESS_NOT_FOUND",
            "BACKEND_PROCESS_NOT_UNIQUE",
        ),
    }
    target = targets.get(action) if isinstance(action, str) else None
    if target is None:
        return {
            "status": "failed",
            "code": "UNKNOWN_FAULT_ACTION",
            "action": action,
        }
    process_name, not_found_code, non_unique_code = target
    result: TargetTerminationResult = host_scope.terminate_unique(process_name)
    if result.status == "not_found":
        return {
            "status": "failed",
            "code": not_found_code,
            "matches": [],
        }
    if result.status == "ambiguous":
        return {
            "status": "failed",
            "code": non_unique_code,
            "matches": list(result.matched_pids),
        }
    if result.status == "terminated" and result.terminated_pid is not None:
        return {
            "status": "completed",
            "action": action,
            "pid": result.terminated_pid,
            "processName": process_name,
        }
    return {
        "status": "failed",
        "code": "PROCESS_SCOPE_TARGET_TERMINATION_FAILED",
        "matches": list(result.matched_pids),
        "unverifiedPids": list(result.unverified_pids),
        "errors": list(result.errors),
    }


def _file_restore_storage_proof(request: Mapping[str, Any], local_data: Path) -> dict[str, Any]:
    workspace_id, operation_id = request["workspaceId"], request["operationId"]
    for value in (workspace_id, operation_id):
        if not isinstance(value, str) or str(uuid.UUID(value)) != value:
            raise ValueError("Restore crash proof requires canonical UUIDs")
    root = local_data / "workspaces" / workspace_id
    if _resolve_persistent_workspace_root(root, local_data.parent, workspace_id) != root.resolve():
        raise ValueError("Restore crash proof requires this Host's registered synthetic root")
    metadata = root / ".vibetable"
    paths = (
        metadata / "topology" / "filehistory-head.db",
        metadata / "coordination" / "write-coordinator.db",
        metadata / "coordination" / "workspace-v2.db",
        metadata / "coordination" / "file-materializer" / "journal.json",
        root / "files" / "restore-crash-44.txt",
    )
    for path in paths:
        for item in (path, *path.parents):
            if item == local_data.parent:
                break
            if item.exists() and getattr(item.lstat(), "st_file_attributes", 0) & 0x400:
                raise ValueError("Restore crash proof path contains a reparse point")

    def read_rows(
        database: Path, statement: str, parameters: tuple[object, ...]
    ) -> list[tuple[object, ...]]:
        db = sqlite3.connect(f"{database.resolve().as_uri()}?mode=ro", uri=True, timeout=5)
        try:
            db.execute("PRAGMA query_only=ON")
            return db.execute(statement, parameters).fetchall()
        finally:
            db.close()

    heads = read_rows(
        paths[0],
        "SELECT mutation_revision, session_epoch, fence_epoch, claim_id "
        "FROM filehistory_heads WHERE workspace_id = ?",
        (workspace_id,),
    )
    receipts = read_rows(
        paths[0],
        "SELECT method, scope, request_hash, result_json "
        "FROM filehistory_operation_receipts WHERE workspace_id = ? "
        "AND operation_id = ?",
        (workspace_id, operation_id),
    )
    if len(heads) != 1 or len(receipts) != 1 or receipts[0][0] != "fileHistory.restore":
        raise ValueError("Restore crash proof has no unique actual Restore publication")
    intents = read_rows(
        paths[1],
        "SELECT mutation_revision, session_epoch, fence_epoch, claim_id, "
        "state FROM mutation_intents WHERE workspace_id = ? AND mutation_revision = ?",
        (workspace_id, heads[0][0]),
    )
    counters = read_rows(
        paths[1],
        "SELECT mutation_revision FROM coordination_state WHERE singleton = 1 AND workspace_id = ?",
        (workspace_id,),
    )
    cached = read_rows(
        paths[2],
        "SELECT method, result_json FROM rpc_operation_receipts "
        "WHERE workspace_id = ? AND operation_id = ?",
        (workspace_id, operation_id),
    )
    if len(intents) != 1 or len(counters) != 1 or len(cached) > 1:
        raise ValueError("Restore crash proof has inconsistent durable coordination")
    journal = _read_json(paths[3])
    if paths[3].exists() and journal is None:
        raise ValueError("Restore crash materializer journal is invalid")
    receipt_result = receipts[0][3]
    if not isinstance(receipt_result, (str, bytes, bytearray)):
        raise ValueError("Restore authority receipt result is not JSON storage")
    cached_receipt = None
    if cached:
        cached_result = cached[0][1]
        if not isinstance(cached_result, (str, bytes, bytearray)):
            raise ValueError("Restore cached receipt result is not JSON storage")
        cached_receipt = {"method": cached[0][0], "result": json.loads(cached_result)}
    proof = {
        "head": dict(
            zip(
                ("mutationRevision", "sessionEpoch", "fenceEpoch", "claimId"), heads[0], strict=True
            )
        ),
        "intent": dict(
            zip(
                ("mutationRevision", "sessionEpoch", "fenceEpoch", "claimId", "state"),
                intents[0],
                strict=True,
            )
        ),
        "committedMutationRevision": counters[0][0],
        "receipt": {
            "method": receipts[0][0],
            "scope": receipts[0][1],
            "requestHash": receipts[0][2],
            "result": json.loads(receipt_result),
        },
        "cachedReceipt": cached_receipt,
        "journal": journal,
        "bytes": paths[4].read_bytes().decode("utf-8"),
    }
    if "restoreIntent" in request:
        binding = request["restoreIntent"]
        fields = {"mutationRevision", "sessionEpoch", "fenceEpoch", "claimId"}
        if not isinstance(binding, dict) or set(binding) != fields:
            raise ValueError("Restore intent binding has unknown or missing fields")
        for field in ("mutationRevision", "sessionEpoch", "fenceEpoch"):
            value = binding[field]
            if type(value) is not int or not 1 <= value <= (1 << 63) - 1:
                raise ValueError("Restore intent counters must be positive SQLite uint values")
        claim_id = binding["claimId"]
        if not isinstance(claim_id, str) or str(uuid.UUID(claim_id)) != claim_id:
            raise ValueError("Restore intent claim must be a canonical UUID")
        restored_intents = read_rows(
            paths[1],
            "SELECT mutation_revision, session_epoch, fence_epoch, claim_id, state "
            "FROM mutation_intents WHERE workspace_id = ? AND mutation_revision = ? "
            "AND session_epoch = ? AND fence_epoch = ? AND claim_id = ?",
            (
                workspace_id,
                binding["mutationRevision"],
                binding["sessionEpoch"],
                binding["fenceEpoch"],
                claim_id,
            ),
        )
        if len(restored_intents) != 1:
            raise ValueError("seed Restore intent does not match its exact persisted binding")
        proof["restoreIntent"] = dict(
            zip(
                ("mutationRevision", "sessionEpoch", "fenceEpoch", "claimId", "state"),
                restored_intents[0],
                strict=True,
            )
        )
    return proof


def _handle_restore_crash_request(
    request: Mapping[str, Any],
    *,
    host_scope: _HostObservationScope,
    local_data: Path,
    controls: Path,
    allow_owned_host_crash: bool = False,
) -> dict[str, Any]:
    try:
        fields = {"requestId", "action", "workspaceId", "operationId"}
        if request.get("action") == "observe-restore-storage":
            fields.add("restoreIntent")
        if set(request) != fields:
            raise ValueError("Restore crash request has unknown or missing fields")
        if request["action"] not in {"kill-restore-sidecar", "observe-restore-storage"}:
            raise ValueError("unknown Restore crash action")
        if request["action"] == "kill-restore-sidecar" and not allow_owned_host_crash:
            raise ValueError("owned Host crash is restricted to the 44 seed phase")
        proof = _file_restore_storage_proof(request, local_data)
        if request["action"] == "observe-restore-storage":
            if proof["restoreIntent"]["state"] != "committed":
                raise ValueError("seed Restore intent did not finish its committed mutation")
            return {"status": "completed", "proof": proof}
        evidence_dir = controls.parent.parent
        checkpoint = _read_json(evidence_dir / "44-restore-crash-checkpoint.json")
        if checkpoint is None or checkpoint.get("workspaceId") != request["workspaceId"]:
            raise ValueError("Restore crash has no pre-kill live checkpoint")
        seed = checkpoint.get("restoreCrash") or {}
        outbound = seed.get("request") or {}
        if (
            outbound.get("type") != "workspace.v2.request"
            or (outbound.get("payload") or {}).get("method") != "fileHistory.restore"
            or ((outbound.get("payload") or {}).get("wire") or {}).get("operationId")
            != request["operationId"]
        ):
            raise ValueError("Restore crash checkpoint has a different actual outbound request")
        observation = seed.get("checkpoint") or {}
        diagnostics = observation.get("bridgeDiagnostics") or {}
        pending = diagnostics.get("pending")
        if (
            diagnostics.get("failures") != []
            or not isinstance(pending, list)
            or len(pending) != 1
            or pending[0].get("requestId") != outbound.get("requestId")
            or pending[0].get("requestType") != "fileHistory.restore"
            or observation.get("rendererDiagnosticsClean") is not True
        ):
            raise ValueError(
                "Restore crash checkpoint does not have exactly one clean bound pending Restore"
            )
        for field in ("screenshot", "trace"):
            asset = Path(observation.get(field, ""))
            if (
                not asset.is_absolute()
                or asset.resolve().parent != evidence_dir.resolve()
                or not asset.is_file()
            ):
                raise ValueError(
                    "Restore crash checkpoint is missing its live screenshot or completed trace"
                )
        ready = _read_json(controls / "file-restore-barrier.ready.json")
        if ready is None or any(
            ready.get(key) != request[key] for key in ("workspaceId", "operationId")
        ):
            raise ValueError("Restore crash barrier does not bind the requested operation")
        if ready.get("point") != "before-finish-committed-mutation":
            raise ValueError("Restore crash barrier has the wrong persistence point")
        head, intent, journal = proof["head"], proof["intent"], proof["journal"]
        if (
            intent != head | {"state": "prepared"}
            or proof["committedMutationRevision"] >= head["mutationRevision"]
            or proof["cachedReceipt"] is not None
            or not isinstance(journal, dict)
            or journal.get("state") != "applied"
            or journal.get("workspaceId") != request["workspaceId"]
            or any(
                journal.get(key) != value or ready.get(key) != value for key, value in head.items()
            )
            or ready.get("result") != proof["receipt"]["result"]
        ):
            raise ValueError("Restore crash independent receipt/head/journal proof is incomplete")
        snapshot = host_scope.snapshot()
        roots = [member for member in snapshot.members if member.pid == host_scope.root.pid]
        if len(roots) != 1 or not roots[0].identity_verified:
            raise ValueError("Restore crash Host root is not identity-verified in its owned scope")
        members = [
            member
            for member in snapshot.members
            if member.executable_name.casefold() == "vibetable-pb.exe"
        ]
        if (
            len(members) != 1
            or not members[0].identity_verified
            or members[0].pid != ready.get("pid")
        ):
            raise ValueError("Restore crash barrier PID is not this Host's unique verified sidecar")
        result = host_scope.terminate_unique("vibetable-pb.exe")
        if result.status != "terminated" or result.terminated_pid != members[0].pid:
            raise RuntimeError("Restore crash sidecar termination was not identity-verified")
        # Stop the entire same owned Job immediately: the supervisor otherwise
        # restarts the sidecar and could recover in the first Host. No foreign
        # process/window scope is consulted or terminated.
        termination = _terminate_scope(host_scope)
        wait = host_scope.wait_empty(timeout=5.0)
        owned_host = {
            "rootPid": host_scope.root.pid,
            "termination": termination,
            "remainingPids": list(wait.remaining_pids) if wait.remaining_pids is not None else None,
            "errors": list(wait.errors),
        }
        if termination["status"] != "passed" or not wait.success:
            return {
                "status": "failed",
                "code": "RESTORE_CRASH_HOST_SCOPE_NOT_EMPTY",
                "ownedHost": owned_host,
            }
        after_crash = _file_restore_storage_proof(request, local_data)
        if after_crash != proof:
            raise RuntimeError("the first Host changed Restore persistence before its scope exited")
        return {
            "status": "completed",
            "pid": result.terminated_pid,
            "proof": proof,
            "afterCrashProof": after_crash,
            "ownedHost": owned_host,
        }
    except (OSError, ValueError, RuntimeError, sqlite3.Error, KeyError, TypeError) as exc:
        return {"status": "failed", "code": "RESTORE_CRASH_PROOF_FAILED", "message": str(exc)}


def _document_native_source(request: Mapping[str, Any], local_data: Path, controls: Path) -> Path:
    action = request.get("action")
    fields = {"requestId", "operationId", "action", "workspaceId", "relativePath"}
    if action in {"copy", "cancel"}:
        fields |= {"cssX", "cssY", "devicePixelRatio"}
    elif action not in {"open-baseline", "open-observe", "preview-observe"}:
        raise ValueError("native document action is outside the closed allowlist")
    if set(request) != fields:
        raise ValueError("native document request has unknown or missing fields")
    for key in ("requestId", "operationId", "workspaceId"):
        value = request[key]
        if not isinstance(value, str) or str(uuid.UUID(value)) != value:
            raise ValueError(f"native document {key} must be a canonical UUID")
    relative = request["relativePath"]
    if not isinstance(relative, str):
        raise ValueError("native observation requires its synthetic filename")
    if action == "preview-observe" and re.fullmatch(
        r"document-native-preview-[0-9a-f-]{36}\.html", relative
    ):
        fixture_id = relative[24:-5]
        expected = (
            "<!doctype html><html><body><pre>VibeTable Task 415 native preview\n"
            f"{relative}\n</pre></body></html>\n"
        )
    else:
        if not re.fullmatch(r"document-native-[0-9a-f-]{36}\.txt", relative):
            raise ValueError("native observation only accepts its action's unique synthetic file")
        fixture_id = relative[16:-4]
        expected = f"VibeTable Task 415 native FileDocument\n{relative}\n"
    if str(uuid.UUID(fixture_id)) != fixture_id:
        raise ValueError("native observation fixture UUID must be canonical")
    workspace = local_data / "workspaces" / request["workspaceId"]
    source = workspace / "files" / relative
    for path in (source, workspace / ".vibetable/workspace.json", controls / "document-drop"):
        for entry in (path, *path.parents):
            if entry.exists() and (
                entry.is_symlink() or getattr(entry.lstat(), "st_file_attributes", 0) & 0x400
            ):
                raise ValueError("native document path contains a reparse point")
    manifest = _read_json(workspace / ".vibetable/workspace.json")
    if manifest is None or manifest.get("workspaceId") != request["workspaceId"]:
        raise ValueError("native document workspace manifest UUID changed")
    if source.read_bytes() != expected.encode("utf-8"):
        raise ValueError("native document source is not the exact synthetic fixture")
    return source.resolve()


class _DocumentNativeWindows:
    """Bounded Win32 input/window observation for the one synthetic FileDocument."""

    def __init__(self) -> None:
        if os.name != "nt":
            raise OSError("native document observation requires Windows")
        from ctypes import wintypes

        self.user32 = ctypes.WinDLL("user32", use_last_error=True)
        self.callback_type = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
        signatures = {
            "EnumWindows": ([self.callback_type, wintypes.LPARAM], wintypes.BOOL),
            "GetWindowThreadProcessId": (
                [wintypes.HWND, ctypes.POINTER(wintypes.DWORD)],
                wintypes.DWORD,
            ),
            "IsWindowVisible": ([wintypes.HWND], wintypes.BOOL),
            "GetWindowTextW": ([wintypes.HWND, wintypes.LPWSTR, ctypes.c_int], ctypes.c_int),
            "GetAncestor": ([wintypes.HWND, wintypes.UINT], wintypes.HWND),
            "WindowFromPoint": ([wintypes.POINT], wintypes.HWND),
            "GetDpiForWindow": ([wintypes.HWND], wintypes.UINT),
            "SetForegroundWindow": ([wintypes.HWND], wintypes.BOOL),
            "GetForegroundWindow": ([], wintypes.HWND),
            "SendMessageTimeoutW": (
                [
                    wintypes.HWND,
                    wintypes.UINT,
                    wintypes.WPARAM,
                    wintypes.LPARAM,
                    wintypes.UINT,
                    wintypes.UINT,
                    ctypes.POINTER(ctypes.c_size_t),
                ],
                ctypes.c_ssize_t,
            ),
            "SetThreadDpiAwarenessContext": ([wintypes.HANDLE], wintypes.HANDLE),
            "PostMessageW": (
                [wintypes.HWND, wintypes.UINT, wintypes.WPARAM, wintypes.LPARAM],
                wintypes.BOOL,
            ),
        }
        for name, (arguments, result) in signatures.items():
            function = getattr(self.user32, name)
            function.argtypes, function.restype = arguments, result
        self.previous_dpi = self.user32.SetThreadDpiAwarenessContext(ctypes.c_void_p(-4))
        if not self.previous_dpi:
            raise ctypes.WinError(ctypes.get_last_error())

    def close(self) -> None:
        self.user32.SetThreadDpiAwarenessContext(self.previous_dpi)

    def owner(self, hwnd: int) -> int:
        from ctypes import wintypes

        pid = wintypes.DWORD()
        if not self.user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid)):
            raise OSError("native document HWND no longer exists")
        return int(pid.value)

    def windows(self) -> list[dict[str, Any]]:
        windows: list[dict[str, Any]] = []

        @self.callback_type
        def collect(hwnd: int, _parameter: int) -> bool:
            from ctypes import wintypes

            pid = wintypes.DWORD()
            if self.user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid)):
                title = ctypes.create_unicode_buffer(1024)
                self.user32.GetWindowTextW(hwnd, title, len(title))
                windows.append(
                    {
                        "hwnd": int(hwnd),
                        "pid": int(pid.value),
                        "title": title.value,
                        "visible": bool(self.user32.IsWindowVisible(hwnd)),
                    }
                )
            return True

        if not self.user32.EnumWindows(collect, 0):
            raise OSError("native document window enumeration failed")
        return windows

    def _root_at(self, x: float, y: float) -> int:
        from ctypes import wintypes

        hwnd = self.user32.WindowFromPoint(wintypes.POINT(round(x), round(y)))
        return int(self.user32.GetAncestor(hwnd, 2) or 0)

    def foreground(self, root: int, host_pid: int) -> None:
        if self.owner(root) != host_pid:
            raise ValueError("native document foreground HWND owner changed")
        before = int(self.user32.GetForegroundWindow() or 0)
        observation = {"beforeHwnd": before, "hostHwnd": root, "hostPid": host_pid}
        self.foreground_observation = observation
        if before != root:
            observation["activationAccepted"] = bool(self.user32.SetForegroundWindow(root))
            if not observation["activationAccepted"]:
                raise OSError("packaged Host foreground activation was denied")
            # Cross-input-queue activation is asynchronous. WM_NULL synchronizes
            # one accepted activation; it does not retry or grant foreground rights.
            result = ctypes.c_size_t()
            observation["activationSynchronized"] = bool(
                self.user32.SendMessageTimeoutW(root, 0, 0, 0, 0x22, 2000, ctypes.byref(result))
            )
            if not observation["activationSynchronized"]:
                raise OSError("packaged Host foreground activation did not synchronize")
        observation["afterHwnd"] = int(self.user32.GetForegroundWindow() or 0)
        if observation["afterHwnd"] != root or self.owner(root) != host_pid:
            raise OSError("packaged Host cannot become the foreground input owner")

    def drag(
        self, request: Mapping[str, Any], target: Mapping[str, Any], controls: Path, host_pid: int
    ) -> None:
        from ctypes import wintypes

        class Mouse(ctypes.Structure):
            _fields_ = [
                ("dx", wintypes.LONG),
                ("dy", wintypes.LONG),
                ("data", wintypes.DWORD),
                ("flags", wintypes.DWORD),
                ("time", wintypes.DWORD),
                ("extra", ctypes.c_size_t),
            ]

        class Keyboard(ctypes.Structure):
            _fields_ = [
                ("vk", wintypes.WORD),
                ("scan", wintypes.WORD),
                ("flags", wintypes.DWORD),
                ("time", wintypes.DWORD),
                ("extra", ctypes.c_size_t),
            ]

        class Payload(ctypes.Union):
            _fields_ = (("mouse", Mouse), ("keyboard", Keyboard))

        class Input(ctypes.Structure):
            _fields_ = [("kind", wintypes.DWORD), ("payload", Payload)]

        send = self.user32.SendInput
        send.argtypes = [wintypes.UINT, ctypes.POINTER(Input), ctypes.c_int]
        send.restype = wintypes.UINT

        def inject(*events: Input) -> None:
            inputs = (Input * len(events))(*events)
            if send(len(events), inputs, ctypes.sizeof(Input)) != len(events):
                raise OSError("SendInput inserted fewer events than requested (possibly UIPI)")

        def mouse(flags: int, x: float = 0, y: float = 0) -> Input:
            if flags & 1:
                left, top = self.user32.GetSystemMetrics(76), self.user32.GetSystemMetrics(77)
                width, height = self.user32.GetSystemMetrics(78), self.user32.GetSystemMetrics(79)
                if not (left <= x < left + width and top <= y < top + height):
                    raise ValueError("native document point is outside the virtual desktop")
                x, y = (
                    round((x - left) * 65535 / (width - 1)),
                    round((y - top) * 65535 / (height - 1)),
                )
                flags |= 0xC000  # ABSOLUTE | VIRTUALDESK
            return Input(0, Payload(mouse=Mouse(round(x), round(y), 0, flags, 0, 0)))

        def escape() -> tuple[Input, Input]:
            return (
                Input(1, Payload(keyboard=Keyboard(0x1B, 0, 0, 0, 0))),
                Input(1, Payload(keyboard=Keyboard(0x1B, 0, 2, 0, 0))),
            )

        if target.get("hostProcessId") != host_pid:
            raise ValueError("native document target is not owned by the packaged Host")
        root, receiver = target["hostHwnd"], target["targetHwnd"]
        if self.owner(root) != host_pid or self.owner(receiver) != host_pid:
            raise ValueError("native document HWND owner changed")
        for field in ("cssX", "cssY", "devicePixelRatio"):
            value = request[field]
            if (
                isinstance(value, bool)
                or not isinstance(value, (int, float))
                or not math.isfinite(value)
            ):
                raise ValueError("native document point/DPI is invalid")
        x, y = request["cssX"], request["cssY"]
        scale = target["scaleX"]
        if (
            not 0 <= x < target["webviewWidth"] - 20
            or not 0 <= y < target["webviewHeight"]
            or abs(request["devicePixelRatio"] - scale) > 0.01
            or abs(self.user32.GetDpiForWindow(root) / 96 - scale) > 0.01
            or abs(scale - target["scaleY"]) > 0.01
        ):
            raise ValueError("native document point does not match the observed WebView DPI/bounds")
        x, y = target["webviewX"] + x * scale, target["webviewY"] + y * scale
        tx, ty = target["targetX"], target["targetY"]
        self.foreground(root, host_pid)
        if self.owner(receiver) != host_pid:
            raise ValueError("native document receiver HWND owner changed")
        if self._root_at(x, y) != root or self._root_at(tx, ty) != receiver:
            raise OSError("native document source/target is obscured by another window")
        down = True
        try:
            inject(mouse(1, x, y), mouse(2), mouse(1, x + 16 * scale, y))
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                started = _read_json(controls / "document-native-drag-started.json")
                if started is not None and started.get("operationId") == request["operationId"]:
                    break
                time.sleep(0.025)
            else:
                raise OSError("real HTML dragstart did not enter the production DoDragDrop")
            if request["action"] == "cancel":
                inject(*escape(), mouse(4))
            else:
                if (
                    self.owner(receiver) != host_pid
                    or self._root_at(tx, ty) != receiver
                    or (self.user32.GetForegroundWindow() not in (root, receiver))
                ):
                    raise OSError("native FileDrop receiver ownership changed before release")
                inject(mouse(1, tx, ty), mouse(4))
            down = False
        finally:
            if down:
                inject(*escape(), mouse(4))

    def document_text(self, hwnd: int) -> list[dict[str, str]]:
        # Only a newly observed HWND is inspected. Windows' own UIA avoids a
        # Python COM dependency and does not launch or control an editor.
        script = """
[Console]::Error.WriteLine("UIA_STAGE script begin elapsedMs=0 apartment=$([System.Threading.Thread]::CurrentThread.GetApartmentState()) utc=$([DateTime]::UtcNow.ToString('o'))")
$clock = [System.Diagnostics.Stopwatch]::StartNew()
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
[Console]::Error.WriteLine("UIA_STAGE assemblies begin elapsedMs=$($clock.ElapsedMilliseconds)")
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
# The default-proxy loader scans callers' ReflectedType. Public MethodInfo.Invoke
# supplies a named framework frame without compiling C# inside the 5s budget.
$assembly = [System.Windows.Automation.AutomationElement].Assembly.GetName()
$assembly.Name = "UIAutomationClientsideProviders"
[System.Windows.Automation.ClientSettings].GetMethod(
    "RegisterClientSideProviderAssembly").Invoke($null, @($assembly))
[Console]::Error.WriteLine("UIA_STAGE assemblies end elapsedMs=$($clock.ElapsedMilliseconds)")
[Console]::Error.WriteLine("UIA_STAGE root begin elapsedMs=$($clock.ElapsedMilliseconds)")
$root = [System.Windows.Automation.AutomationElement]::FromHandle(
    [IntPtr]::new([int64]$env:VIBETABLE_QA_NATIVE_HWND))
[Console]::Error.WriteLine("UIA_STAGE root end elapsedMs=$($clock.ElapsedMilliseconds)")
$documentCondition = [System.Windows.Automation.PropertyCondition]::new(
    [System.Windows.Automation.AutomationElement]::ControlTypeProperty,
    [System.Windows.Automation.ControlType]::Document)
$editCondition = [System.Windows.Automation.PropertyCondition]::new(
    [System.Windows.Automation.AutomationElement]::ControlTypeProperty,
    [System.Windows.Automation.ControlType]::Edit)
$condition = [System.Windows.Automation.OrCondition]::new($documentCondition, $editCondition)
[Console]::Error.WriteLine("UIA_STAGE document-query begin elapsedMs=$($clock.ElapsedMilliseconds)")
# A visible Shell window can precede its UIA document provider. Wait only
# for absence; the enclosing subprocess still enforces the original 5s budget.
do {
    $documents = @(foreach ($document in $root.FindAll(
        [System.Windows.Automation.TreeScope]::Descendants, $condition)) {
        # Classic Notepad exposes its status panes as Edit controls. Their
        # parent role distinguishes them from document editors, without names.
        $parent = [System.Windows.Automation.TreeWalker]::RawViewWalker.GetParent($document)
        if ($document.Current.ControlType.ProgrammaticName -eq "ControlType.Edit" -and
            $parent.Current.ControlType.ProgrammaticName -eq "ControlType.StatusBar") { continue }
        $document
    })
    if ($documents.Count -eq 0) { Start-Sleep -Milliseconds 25 }
} while ($documents.Count -eq 0)
[Console]::Error.WriteLine("UIA_STAGE document-query end elapsedMs=$($clock.ElapsedMilliseconds) count=$($documents.Count)")
if ($documents.Count -ne 1) {
    $metadata = @(foreach ($document in @($documents | Select-Object -First 8)) {
        $current = $document.Current
        $parent = [System.Windows.Automation.TreeWalker]::RawViewWalker.GetParent($document)
        $class = [string]$current.ClassName
        $id = [string]$current.AutomationId
        $parentClass = [string]$parent.Current.ClassName
        @{ type = $current.ControlType.ProgrammaticName;
           class = $class.Substring(0, [Math]::Min(80, $class.Length));
           offscreen = $current.IsOffscreen; content = $current.IsContentElement;
           id = $id.Substring(0, [Math]::Min(80, $id.Length)); hwnd = $current.NativeWindowHandle;
           parentType = $parent.Current.ControlType.ProgrammaticName;
           parentClass = $parentClass.Substring(0, [Math]::Min(80, $parentClass.Length)) }
    })
    $json = ConvertTo-Json -InputObject $metadata -Compress
    [Console]::Error.WriteLine("UIA_DOCUMENT_METADATA count=$($documents.Count) items=$json")
    throw "UIA_DOCUMENT_COUNT expected=1 actual=$($documents.Count)"
}
$tabCondition = [System.Windows.Automation.PropertyCondition]::new(
    [System.Windows.Automation.AutomationElement]::ControlTypeProperty,
    [System.Windows.Automation.ControlType]::TabItem)
[Console]::Error.WriteLine("UIA_STAGE tab-query begin elapsedMs=$($clock.ElapsedMilliseconds)")
$tabs = $root.FindAll([System.Windows.Automation.TreeScope]::Descendants, $tabCondition)
[Console]::Error.WriteLine("UIA_STAGE tab-query end elapsedMs=$($clock.ElapsedMilliseconds) count=$($tabs.Count)")
if ($tabs.Count -gt 1) { throw "UIA_TAB_COUNT maximum=1 actual=$($tabs.Count)" }
$items = @(foreach ($document in $documents) {
    $pattern = $null
    [Console]::Error.WriteLine("UIA_STAGE pattern begin elapsedMs=$($clock.ElapsedMilliseconds)")
    if ($document.TryGetCurrentPattern([System.Windows.Automation.TextPattern]::Pattern, [ref]$pattern)) {
        [Console]::Error.WriteLine("UIA_STAGE pattern end elapsedMs=$($clock.ElapsedMilliseconds) kind=text")
        [Console]::Error.WriteLine("UIA_STAGE read-text begin elapsedMs=$($clock.ElapsedMilliseconds)")
        @{ name = $document.Current.Name; text = $pattern.DocumentRange.GetText(4096) }
        [Console]::Error.WriteLine("UIA_STAGE read-text end elapsedMs=$($clock.ElapsedMilliseconds)")
    } elseif ($document.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern, [ref]$pattern)) {
        [Console]::Error.WriteLine("UIA_STAGE pattern end elapsedMs=$($clock.ElapsedMilliseconds) kind=value")
        [Console]::Error.WriteLine("UIA_STAGE read-text begin elapsedMs=$($clock.ElapsedMilliseconds)")
        @{ name = $document.Current.Name; text = $pattern.Current.Value }
        [Console]::Error.WriteLine("UIA_STAGE read-text end elapsedMs=$($clock.ElapsedMilliseconds)")
    } else {
        [Console]::Error.WriteLine("UIA_STAGE pattern end elapsedMs=$($clock.ElapsedMilliseconds) kind=unavailable")
        throw 'UIA_TEXT_PATTERN unavailable'
    }
})
[Console]::Error.WriteLine("UIA_STAGE serialize begin elapsedMs=$($clock.ElapsedMilliseconds)")
$output = ConvertTo-Json -InputObject $items -Compress
[Console]::Error.WriteLine("UIA_STAGE serialize end elapsedMs=$($clock.ElapsedMilliseconds) length=$($output.Length)")
[Console]::Out.Write($output)
[Console]::Error.WriteLine("UIA_STAGE output end elapsedMs=$($clock.ElapsedMilliseconds)")
"""
        environment = os.environ.copy()
        environment["VIBETABLE_QA_NATIVE_HWND"] = str(hwnd)
        encoded = base64.b64encode(script.encode("utf-16-le")).decode("ascii")
        result = subprocess.run(
            ["powershell.exe", "-Mta", "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded],
            env=environment,
            capture_output=True,
            text=True,
            encoding="utf-8",
            check=True,
            timeout=5,
        )
        documents = json.loads(result.stdout)
        if not isinstance(documents, list):
            raise ValueError("new editor UIA document result is not an array")
        return documents

    def close_new_document(self, hwnd: int, pid: int) -> bool:
        if self.owner(hwnd) != pid:
            raise OSError("new synthetic editor window ownership changed before WM_CLOSE")
        if not self.user32.PostMessageW(hwnd, 0x0010, 0, 0):
            raise OSError("new synthetic editor window rejected WM_CLOSE")
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            if not self.user32.IsWindowVisible(hwnd):
                return True
            time.sleep(0.025)
        return False


def _uia_stdout_summary(stdout: str | bytes | None) -> dict[str, int | str | None]:
    """Bounded TimeoutExpired stdout shape diagnostics: never content.

    Reports only the captured output's length and JSON shape so a timeout can
    be distinguished from output that never reached the pipe. This describes
    the captured bytes only — it is not evidence about process exit state.
    Over-limit payloads are reported as length-only; they are not parsed.
    """
    if stdout is None:
        return {
            "stdoutType": None,
            "stdoutLength": 0,
            "stdoutDocuments": None,
            "stdoutIsSingleDocumentJson": None,
        }
    if isinstance(stdout, bytes):
        decoded = stdout.decode("utf-8", errors="replace")
        kind = "bytes"
    else:
        decoded = stdout
        kind = "str"
    summary: dict[str, int | str | None] = {"stdoutType": kind, "stdoutLength": len(decoded)}
    # GetText caps at 4096 chars per document; JSON escaping plus structure
    # keeps a legitimate payload far below this bound. Anything larger is not
    # parsed and its shape stays unverified.
    if len(decoded) > 32768:
        summary["stdoutDocuments"] = None
        summary["stdoutIsSingleDocumentJson"] = None
        return summary
    try:
        parsed = json.loads(decoded)
    except json.JSONDecodeError, ValueError:
        parsed = None
    if isinstance(parsed, list) and all(isinstance(item, dict) for item in parsed):
        summary["stdoutDocuments"] = len(parsed)
        summary["stdoutIsSingleDocumentJson"] = (
            len(parsed) == 1
            and isinstance(parsed[0].get("name"), str)
            and isinstance(parsed[0].get("text"), str)
        )
    else:
        summary["stdoutDocuments"] = None
        summary["stdoutIsSingleDocumentJson"] = False
    return summary


def _handle_document_native_request(
    request: Mapping[str, Any],
    *,
    host_scope: _SnapshotScope,
    local_data: Path,
    controls: Path,
) -> dict[str, Any]:
    native: _DocumentNativeWindows | None = None
    window: dict[str, Any] | None = None
    window_observation: dict[str, Any] | None = None
    # Parent-side observation timing: pairs the whole native observation with
    # the script's own UIA_STAGE clock (script begin carries a UTC timestamp).
    started_at_utc: str | None = None
    started_at: float | None = None
    try:
        source = _document_native_source(request, local_data, controls)
        native = _DocumentNativeWindows()
        action = request["action"]
        if action in {"copy", "cancel"}:
            target = _read_json(controls / "document-native-target.json")
            if (
                target is None
                or any(target.get(key) != request[key] for key in ("operationId", "workspaceId"))
                or (target.get("mode") != action or target.get("source") != str(source))
            ):
                raise ValueError("native document target does not match this operation/source")
            native.drag(request, target, controls, host_scope.root.pid)
            return {
                "requestId": request["requestId"],
                "status": "injected",
                "action": action,
                "foreground": native.foreground_observation,
            }
        if action == "preview-observe":
            evidence = _read_json(controls / "document-native-preview-result.json")
            if (
                evidence is None
                or evidence.get("outcome") != "do-preview-returned"
                or (evidence.get("source") != str(source))
            ):
                raise OSError("real FileDocument COM DoPreview did not produce a success record")
            pid, hwnd = evidence["processId"], evidence["hwnd"]
            members = host_scope.snapshot().members
            if (
                not any(
                    member.pid == pid
                    and member.identity_verified
                    and member.executable_name.casefold() == "vibetable.next.exe"
                    for member in members
                )
                or pid == host_scope.root.pid
            ):
                raise OSError("preview helper is not a verified child in the packaged Host Job")
            if native.owner(hwnd) != pid or not native.user32.IsWindowVisible(hwnd):
                raise OSError("real preview COM host HWND is missing or has another owner")
            return {
                "requestId": request["requestId"],
                "status": "observed",
                "action": action,
                "preview": evidence,
                "helperIdentityVerified": True,
            }
        baseline_path = controls / "document-native-open-baseline.json"
        if action == "open-baseline":
            baseline = {
                "operationId": request["operationId"],
                "source": str(source),
                "windows": [
                    {"hwnd": item["hwnd"], "pid": item["pid"]} for item in native.windows()
                ],
            }
            _write_json_atomic(baseline_path, baseline)
            return {"requestId": request["requestId"], "status": "observed", "action": action}
        baseline = _read_json(baseline_path)
        if (
            baseline is None
            or baseline.get("operationId") != request["operationId"]
            or (baseline.get("source") != str(source))
        ):
            raise ValueError("native Shell Open baseline is missing or belongs to another file")
        previous = {item["hwnd"] for item in baseline["windows"]}
        deadline = time.monotonic() + 2
        while True:
            windows = native.windows()
            candidates = [
                item
                for item in windows
                if item["hwnd"] not in previous and item["visible"] and source.name in item["title"]
            ]
            if candidates or time.monotonic() >= deadline:
                break
            time.sleep(0.025)
        if len(candidates) != 1:
            related = [item for item in windows if source.stem in item["title"]]
            window_observation = {
                "candidateCount": len(candidates),
                "matchingStemWindowCount": len(related),
                "truncated": len(related) > 8,
                "windows": [
                    {
                        "hwnd": item["hwnd"],
                        "pid": item["pid"],
                        "title": item["title"][:1024],
                        "visible": item["visible"],
                        "baselineHwnd": item["hwnd"] in previous,
                        "newWindow": item["hwnd"] not in previous,
                        "fullNameMatch": source.name in item["title"],
                        "stemMatch": True,
                    }
                    for item in related[:8]
                ],
            }
            raise OSError("Shell Open has no unique newly created window for the synthetic TXT")
        window = candidates[0]
        # Observation clock: taken immediately before the UIA observation call
        # so its delta against the script's entry UTC locates the PowerShell
        # startup, not the validation/window search that precedes it.
        started_at_utc = datetime.now(UTC).isoformat()
        started_at = time.monotonic()
        documents = native.document_text(window["hwnd"])
        expected = source.read_text(encoding="utf-8").rstrip("\n")
        if (
            len(documents) != 1
            or documents[0].get("text", "").replace("\r\n", "\n").rstrip("\n") != expected
        ):
            raise OSError("new Shell window does not expose the exact sole synthetic document")
        if not native.close_new_document(window["hwnd"], window["pid"]):
            raise OSError("verified new synthetic editor window did not close after WM_CLOSE")
        return {
            "requestId": request["requestId"],
            "status": "observed",
            "action": action,
            "source": str(source),
            "window": window,
            "document": documents[0],
            "newWindowClosed": True,
        }
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as exception:
        uia_failure: dict[str, str | int | float | None] | None = None
        error = str(exception)
        if isinstance(exception, (subprocess.CalledProcessError, subprocess.TimeoutExpired)):
            stderr = exception.stderr or ""
            if isinstance(stderr, bytes):
                stderr = stderr.decode("utf-8", errors="replace")
            uia_failure = {
                "startedAtUtc": started_at_utc,
                "elapsedMs": (
                    round((time.monotonic() - started_at) * 1000)
                    if started_at is not None
                    else None
                ),
                "returnCode": exception.returncode
                if isinstance(exception, subprocess.CalledProcessError)
                else None,
                "stderr": stderr[:4096],
            }
            if isinstance(exception, subprocess.TimeoutExpired):
                uia_failure["timeoutSeconds"] = exception.timeout
                uia_failure.update(_uia_stdout_summary(exception.stdout))
                error = f"Shell UIA observation timed out after {exception.timeout} seconds"
            else:
                error = f"Shell UIA observation failed (exit {exception.returncode})"
        return {
            "requestId": request.get("requestId"),
            "status": "unverified",
            "action": request.get("action"),
            "error": error,
            "window": window,
            "windowObservation": window_observation,
            "uiaFailure": uia_failure,
            "foreground": getattr(native, "foreground_observation", None),
        }
    finally:
        if native is not None:
            native.close()


def _run_node_runner(
    command: list[str],
    *,
    scenario_dir: Path,
    local_data: Path,
    host_scope: _HostObservationScope,
    process_network: dict[str, Any] | None = None,
    measure_diff_worker: bool = False,
) -> tuple[int, str, str]:
    fault_request = scenario_dir / "fault-request.json"
    fault_result = scenario_dir / "fault-result.json"
    storage_request = scenario_dir / "storage-proof-request.json"
    storage_result = scenario_dir / "storage-proof-result.json"
    process_network_path = scenario_dir / "process-network-observations.json"
    node_process = subprocess.Popen(
        command,
        cwd=ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    handled_fault_ids: set[str] = set()
    invalid_fault_reported = False
    handled_storage_proof_ids: set[str] = set()
    handled_document_native_ids: set[str] = set()
    controls = (
        Path(command[command.index("--controls-dir") + 1]) if "--controls-dir" in command else None
    )
    worker_memory: dict[str, Any] = {
        "method": "identity-verified Job members, sampled working set every driver loop (nominal 50 ms)",
        "blindSpots": "Short-lived workers and allocations between samples can be missed; this is an observed peak, not the Job memory limit or a kernel peak counter.",
        "samples": 0,
        "workers": {},
        "errors": [],
    }
    next_network_sample = 0.0
    deadline = time.monotonic() + 180
    while node_process.poll() is None:
        if time.monotonic() >= deadline:
            node_process.kill()
            stdout, stderr = node_process.communicate(timeout=10)
            raise subprocess.TimeoutExpired(command, 180, stdout, stderr)
        request = _read_json(fault_request)
        if request is not None:
            fault_request_id = request.get("requestId")
            if isinstance(fault_request_id, str) and fault_request_id:
                if fault_request_id not in handled_fault_ids:
                    handled_fault_ids.add(fault_request_id)
                    response = (
                        _handle_restore_crash_request(
                            request,
                            host_scope=host_scope,
                            local_data=local_data,
                            controls=controls,
                            allow_owned_host_crash=(
                                "--scenario" in command
                                and command[command.index("--scenario") + 1]
                                == "44-file-restore-crash"
                                and "--persistent-phase" in command
                                and command[command.index("--persistent-phase") + 1] == "seed"
                            ),
                        )
                        if request.get("action")
                        in {"kill-restore-sidecar", "observe-restore-storage"}
                        and controls is not None
                        else _handle_fault_request(request, host_scope)
                    )
                    if time.monotonic() >= deadline:
                        node_process.kill()
                        stdout, stderr = node_process.communicate(timeout=10)
                        raise subprocess.TimeoutExpired(command, 180, stdout, stderr)
                    _write_json_atomic(
                        fault_result,
                        {
                            "requestId": fault_request_id,
                            **response,
                        },
                    )
            elif not invalid_fault_reported:
                invalid_fault_reported = True
                _write_json_atomic(
                    fault_result,
                    {
                        "requestId": None,
                        "status": "failed",
                        "code": "FAULT_REQUEST_ID_INVALID",
                    },
                )
        request = _read_json(storage_request)
        if request is not None:
            storage_request_id = request.get("requestId")
            if (
                isinstance(storage_request_id, str)
                and storage_request_id not in handled_storage_proof_ids
            ):
                handled_storage_proof_ids.add(storage_request_id)
                _write_json_atomic(
                    storage_result,
                    _handle_storage_proof(request, local_data),
                )
        native_request_path = scenario_dir / "file-document-native-request.json"
        native_request = _read_json(native_request_path)
        if native_request is not None and controls is not None:
            native_request_id = native_request.get("requestId")
            if (
                isinstance(native_request_id, str)
                and native_request_id not in handled_document_native_ids
            ):
                # Consume the synthetic request slot BEFORE handling: the Node
                # journey awaits this requestId's ack and only then renames its
                # next complete .tmp onto the same path, so after consumption
                # the rename targets a NONEXISTENT file. Keeping the stale slot
                # would race the next plain read handle — on Windows
                # MoveFileExW cannot replace an open destination regardless of
                # share flags (CI 37239042992 scenario-42 EPERM). Deletion is a
                # hard precondition: failures propagate, never retried.
                native_request_path.unlink()
                handled_document_native_ids.add(native_request_id)
                native_result = _handle_document_native_request(
                    native_request,
                    host_scope=host_scope,
                    local_data=local_data,
                    controls=controls,
                )
                if time.monotonic() >= deadline:
                    node_process.kill()
                    stdout, stderr = node_process.communicate(timeout=10)
                    raise subprocess.TimeoutExpired(command, 180, stdout, stderr)
                _write_json_atomic(scenario_dir / "file-document-native-result.json", native_result)
        if process_network is not None and time.monotonic() >= next_network_sample:
            _record_process_network(host_scope, process_network)
            _write_json_atomic(
                process_network_path,
                _process_network_report(process_network, status="monitoring"),
            )
            next_network_sample = time.monotonic() + 0.25
        if measure_diff_worker:
            try:
                snapshot = host_scope.working_set_snapshot()
                worker_memory["samples"] += 1
                for member in snapshot.members:
                    if member.executable_name.casefold() != "vibetable.documentdiff.worker.exe":
                        continue
                    if not member.identity_verified or member.working_set_bytes is None:
                        worker_memory["errors"].append(
                            f"worker {member.pid} identity/memory unavailable"
                        )
                        continue
                    observed = worker_memory["workers"].setdefault(
                        str(member.pid),
                        {
                            "pid": member.pid,
                            "samples": 0,
                            "peakWorkingSetBytes": 0,
                        },
                    )
                    observed["samples"] += 1
                    observed["peakWorkingSetBytes"] = max(
                        observed["peakWorkingSetBytes"], member.working_set_bytes
                    )
            except (OSError, RuntimeError) as exc:
                worker_memory["errors"].append(str(exc))
        time.sleep(0.05)
    if measure_diff_worker:
        _write_json_atomic(scenario_dir / "document-diff-worker-memory.json", worker_memory)
    if process_network is not None:
        _record_process_network(host_scope, process_network)
        _write_json_atomic(
            process_network_path,
            _process_network_report(process_network, status="completed"),
        )
    stdout, stderr = node_process.communicate(timeout=10)
    return node_process.returncode, stdout, stderr


def _failure_result(
    scenario: Scenario,
    *,
    code: str,
    message: str,
    dependency: str | None = None,
) -> dict[str, Any]:
    return {
        "scenario": scenario.id,
        "title": scenario.title,
        "requirement": scenario.requirement,
        "status": "failed",
        "error": {
            "code": code,
            "message": message,
            "dependency": dependency,
        },
    }


def _nearest_rank(values: list[float], percentile: int) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, (len(ordered) * percentile + 99) // 100 - 1))
    return round(ordered[index], 2)


def summarize_performance(results: Sequence[dict[str, Any]]) -> dict[str, Any]:
    scenario_timings: list[dict[str, Any]] = []
    operation_samples: dict[str, list[dict[str, Any]]] = {}
    ui_samples: dict[str, list[float]] = {}
    pending_requests = 0
    recovery_names = {
        "recovery.sidecar.killToReadableTable",
        "recovery.backend.killToWritableSession",
        "recovery.workspace.closeAfterBackendExit",
        "recovery.workspace.reopenAfterBackendExit",
    }
    recovery_runs: list[dict[str, object]] = []
    unmeasured_recovery_runs = 0
    for result in results:
        duration = result.get("durationMs")
        if isinstance(duration, (int, float)) and not isinstance(duration, bool):
            timing: dict[str, Any] = {
                "scenario": result.get("scenario"),
                "status": result.get("status"),
                "durationMs": round(float(duration), 2),
            }
            wall_clock = result.get("runnerWallClockMs")
            if isinstance(wall_clock, (int, float)) and not isinstance(wall_clock, bool):
                timing["wallClockMs"] = round(float(wall_clock), 2)
            scenario_timings.append(timing)
        recovery_timings: list[tuple[str, float | None]] = []
        ui_timings = result.get("uiTimings")
        if isinstance(ui_timings, list):
            for timing in ui_timings:
                if not isinstance(timing, dict):
                    continue
                name = timing.get("name")
                if isinstance(name, str) and name.startswith("recovery."):
                    value = timing.get("durationMs")
                    duration = (
                        float(value)
                        if isinstance(value, (int, float))
                        and not isinstance(value, bool)
                        and math.isfinite(value)
                        and value >= 0
                        else None
                    )
                    recovery_timings.append((name, duration))
                    continue
                ui_duration = timing.get("durationMs")
                if (
                    isinstance(name, str)
                    and isinstance(ui_duration, (int, float))
                    and not isinstance(ui_duration, bool)
                ):
                    ui_samples.setdefault(name, []).append(float(ui_duration))
        if result.get("scenario") == "10-sse-reconnect":
            lifecycle = result.get("lifecycle")
            if (
                result.get("status") == "passed"
                and isinstance(lifecycle, dict)
                and lifecycle.get("status") == "passed"
                and len(recovery_timings) == len(recovery_names)
                and {name for name, _ in recovery_timings} == recovery_names
                and all(duration is not None for _, duration in recovery_timings)
            ):
                recovery_runs.append(
                    {
                        "durationsMs": dict(recovery_timings),
                    }
                )
            else:
                unmeasured_recovery_runs += 1
        diagnostics = result.get("bridgeDiagnostics")
        if not isinstance(diagnostics, dict):
            continue
        pending = diagnostics.get("pending")
        if isinstance(pending, list):
            pending_requests += len(pending)
        round_trips = diagnostics.get("roundTrips")
        if not isinstance(round_trips, list):
            continue
        for sample in round_trips:
            if not isinstance(sample, dict):
                continue
            request_type = sample.get("requestType")
            sample_duration = sample.get("durationMs")
            if (
                not isinstance(request_type, str)
                or not isinstance(sample_duration, (int, float))
                or isinstance(sample_duration, bool)
            ):
                continue
            operation_samples.setdefault(request_type, []).append(
                {
                    "scenario": result.get("scenario"),
                    "durationMs": float(sample_duration),
                    "failed": sample.get("responseType") == "operation.failed",
                    "code": sample.get("code"),
                }
            )

    by_operation: list[dict[str, Any]] = []
    for request_type, samples in sorted(operation_samples.items()):
        durations = [sample["durationMs"] for sample in samples]
        by_operation.append(
            {
                "requestType": request_type,
                "count": len(samples),
                "failures": sum(sample["failed"] for sample in samples),
                "p50Ms": _nearest_rank(durations, 50),
                "p95Ms": _nearest_rank(durations, 95),
                "maxMs": round(max(durations), 2),
            }
        )

    history = next(
        (operation for operation in by_operation if operation["requestType"] == "history.query"),
        None,
    )
    history_status = "not-measured"
    if history is not None:
        history_max = history.get("maxMs")
        history_p95 = history.get("p95Ms")
        if isinstance(history_max, (int, float)) and history_max > 2_000:
            history_status = "hard-limit-exceeded"
        elif isinstance(history_p95, (int, float)) and history_p95 > 500:
            history_status = "warning"
        else:
            history_status = "within-budget"
    by_ui_action = [
        {
            "name": name,
            "count": len(durations),
            "p50Ms": _nearest_rank(durations, 50),
            "p95Ms": _nearest_rank(durations, 95),
            "maxMs": round(max(durations), 2),
        }
        for name, durations in sorted(ui_samples.items())
    ]
    history_ui = next(
        (action for action in by_ui_action if action["name"] == "history.drawer.initialLoad"),
        None,
    )
    history_ui_status = "not-measured"
    if history_ui is not None:
        history_ui_max = history_ui.get("maxMs")
        history_ui_p95 = history_ui.get("p95Ms")
        if isinstance(history_ui_max, (int, float)) and history_ui_max > 2_000:
            history_ui_status = "hard-limit-exceeded"
        elif isinstance(history_ui_p95, (int, float)) and history_ui_p95 > 750:
            history_ui_status = "warning"
        else:
            history_ui_status = "within-budget"
    return {
        "thresholds": {
            "historyQueryP95WarningMs": 500,
            "historyQueryHardLimitMs": 2_000,
            "historyDrawerP95WarningMs": 750,
            "historyDrawerHardLimitMs": 2_000,
            "scenarioHardTimeoutMs": 180_000,
            "note": (
                "Scenario duration includes app startup and fixture work. "
                "Deliberate sidecar recovery is assessed separately from normal bridge latency."
            ),
        },
        "assessment": {
            "historyQuery": history_status,
            "historyDrawer": history_ui_status,
            "pendingRequests": pending_requests,
            "bridgeFailures": sum(operation["failures"] for operation in by_operation),
        },
        "scenarios": scenario_timings,
        "recovery": {
            "status": "measured"
            if recovery_runs and not unmeasured_recovery_runs
            else "not-measured",
            "clock": "node-performance-now",
            "runs": recovery_runs,
            "unmeasuredRuns": unmeasured_recovery_runs,
        },
        "byUiAction": by_ui_action,
        "byOperation": by_operation,
    }


def _scenario_runtime_directory(evidence_root: Path, scenario: Scenario) -> Path:
    scenario_number = scenario.id.partition("-")[0]
    if len(scenario_number) != 2 or not scenario_number.isdecimal():
        raise ValueError(f"Scenario id must start with a two-digit number: {scenario.id}")
    return (evidence_root / "_runtime" / scenario_number).resolve()


def _write_sparse_diff_workbooks(controls_dir: Path) -> None:
    """Reuse a real closed OPC fixture for 10,004 sparse cells and 75 known changes."""
    fixture = (
        ROOT
        / "desktop/tests/VibeTable.DocumentDiff.OpenXml.Tests/TestData/Qualification/xlsx/content-before.xlsx"
    )
    with zipfile.ZipFile(fixture) as source:
        parts = {name: source.read(name) for name in source.namelist()}
    ns = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
    parts["xl/sharedStrings.xml"] = (
        f'<sst xmlns="{ns}"><si><t>{"长共享文本" * 40000}</t></si></sst>'.encode()
    )
    for changed in (False, True):
        output = controls_dir / f"sparse-{'after' if changed else 'before'}.xlsx"
        with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as archive:
            for name, content in parts.items():
                if name not in ("xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"):
                    archive.writestr(name, content)
            for sheet in (1, 2):
                rows = []
                for i in range(5000):
                    value = 2 * i + 1
                    if changed and sheet == 1 and i < 75:
                        value = -value
                    row = i * 200 + 1
                    cell = f'<c r="A{row}"><v>{value}</v></c>'
                    if i == 0:
                        cell += '<c r="B1" t="s"><v>0</v></c>'
                    rows.append(f'<row r="{row}">{cell}</row>')
                rows.append('<row r="1048576"><c r="XFD1048576"><v>1</v></c></row>')
                archive.writestr(
                    f"xl/worksheets/sheet{sheet}.xml",
                    f'<worksheet xmlns="{ns}"><dimension ref="A1:XFD1048576"/><sheetData>{"".join(rows)}</sheetData></worksheet>',
                )


def run_scenario(
    scenario: Scenario,
    *,
    package_root: Path,
    evidence_root: Path,
    node: str,
    persistent_run: _PersistentScenarioRun | None = None,
) -> dict[str, Any]:
    if scenario.id == "24-directory-replica-conflict":
        from tests.e2e.directory_replica_conflict import run_directory_replica_conflict

        return run_directory_replica_conflict(
            package_root=package_root, evidence_root=evidence_root, node=node
        ) | {"title": scenario.title, "requirement": scenario.requirement}
    # The packaged host runs with the package as its working directory. Keep
    # every test-mode file protocol absolute so WPF and the orchestrator refer
    # to the same isolated evidence/data tree.
    scenario_dir = (
        persistent_run.scenario_dir.resolve()
        if persistent_run is not None
        else (evidence_root / scenario.id).resolve()
    )
    scenario_dir.mkdir(parents=True, exist_ok=True)
    runtime_dir = (
        scenario_dir / "_runtime"
        if persistent_run is not None
        else _scenario_runtime_directory(evidence_root, scenario)
    )
    readiness_dir = (
        persistent_run.readiness_dir if persistent_run is not None else runtime_dir / "host"
    )
    readiness_dir.mkdir(parents=True, exist_ok=persistent_run is not None)
    controls_dir = runtime_dir / "controls"
    controls_dir.mkdir(parents=True)
    import_source = controls_dir / "import-source.csv"
    import_source.write_text(
        (
            "payload\n"
            '"{""items"":[1,{""code"":""A""}],'
            '""nested"":{""label"":""import"",""value"":9},'
            '""enabled"":true}"\n'
        ),
        encoding="utf-8",
    )
    export_target = controls_dir / "export-result.csv"
    (controls_dir / "import-source.txt").write_text(
        str(import_source.resolve()) + "\n",
        encoding="utf-8",
    )
    (controls_dir / "export-target.txt").write_text(
        str(export_target.resolve()) + "\n",
        encoding="utf-8",
    )
    plugin_fixture = ROOT / "tests" / "fixtures" / "plugins" / "mutation-boundary"
    (controls_dir / "plugin-source.txt").write_text(
        str(plugin_fixture.resolve()) + "\n",
        encoding="utf-8",
    )
    plugin_read_source = controls_dir / "plugin-read-source.txt"
    plugin_read_source.write_text("native plugin file grant\n", encoding="utf-8")
    (controls_dir / "plugin-file-read.txt").write_text(
        str(plugin_read_source.resolve()) + "\n", encoding="utf-8"
    )
    (controls_dir / "plugin-file-write.txt").write_text(
        str((controls_dir / "plugin-write-result.txt").resolve()) + "\n", encoding="utf-8"
    )
    attachment_base = {
        "12-backup-consistency": "backup",
        "18-workspace-search": "e2e-search-attachment",
    }.get(scenario.id, "attachment")
    attachment_source = controls_dir / f"{attachment_base}-original.txt"
    attachment_source.write_text(f"{attachment_base}-original\n", encoding="utf-8")
    attachment_replacement = controls_dir / f"{attachment_base}-replacement.txt"
    attachment_replacement.write_text(
        f"{attachment_base}-replacement\n",
        encoding="utf-8",
    )
    (controls_dir / "attachment-source.txt").write_text(
        str(attachment_source.resolve()) + "\n",
        encoding="utf-8",
    )
    (controls_dir / "attachment-replacement-source.txt").write_text(
        str(attachment_replacement.resolve()) + "\n",
        encoding="utf-8",
    )
    document_source = controls_dir / "document-diff-source.txt"
    document_source.write_text(
        "VibeTable document diff product E2E\n",
        encoding="utf-8",
    )
    (controls_dir / "document-source.txt").write_text(
        str(document_source.resolve()) + "\n",
        encoding="utf-8",
    )
    content_markdown_source = controls_dir / "content-reference-a.md"
    content_markdown_source.write_text(
        "# E2E content reference\n\nMarigold appears in visible Markdown text.\n",
        encoding="utf-8",
    )
    content_json_source = controls_dir / "content-reference-b.json"
    content_json_source.write_text(
        json.dumps(
            {"title": "E2E JSON reference", "body": "Cobalt appears in JSON content."},
            ensure_ascii=False,
        )
        + "\n",
        encoding="utf-8",
    )
    if scenario.id in {"14-document-diff", "39-file-workflow-combination"} and (
        persistent_run is None or persistent_run.phase == "seed"
    ):
        _write_sparse_diff_workbooks(controls_dir)
    if scenario.id == "18-workspace-search":
        for fixture_name, document_name in (
            ("docx-contract-split", "search-visible.docx"),
            ("xlsx-ledger", "search-visible.xlsx"),
            ("pptx-slides-reordered", "search-visible.pptx"),
        ):
            fixture_root = ROOT / "tests" / "fixtures" / "ooxml" / fixture_name
            with zipfile.ZipFile(
                controls_dir / document_name, "w", zipfile.ZIP_DEFLATED
            ) as package:
                for part in sorted(fixture_root.rglob("*")):
                    if part.is_file() and part.name != "expected.txt":
                        package.write(part, part.relative_to(fixture_root).as_posix())
    workspace_root = (
        persistent_run.workspace_root
        if persistent_run is not None
        else controls_dir / "workspace-root"
    )
    if persistent_run is None:
        workspace_root.mkdir()
    snapshot_package = controls_dir / "workspace-snapshot.vtsnapshot"
    snapshot_extract = controls_dir / "snapshot-extract.bin"
    for control_name, target in (
        ("workspace-root.txt", workspace_root),
        ("snapshot-export-target.txt", snapshot_package),
        ("snapshot-import-source.txt", snapshot_package),
        ("snapshot-extract-target.txt", snapshot_extract),
        ("file-upgrade-source.txt", document_source),
    ):
        (controls_dir / control_name).write_text(
            str(target.resolve()) + "\n",
            encoding="utf-8",
        )
    layout = json.loads(_package_layout_path(package_root).read_text(encoding="utf-8"))
    host = (package_root / layout["launch"]["host"]).resolve()
    port = _reserve_port()
    environment = os.environ.copy()
    environment["VIBETABLE_WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"] = (
        f"--remote-debugging-port={port} --disable-gpu"
    )
    environment["VIBETABLE_E2E_WEBVIEW2_USER_DATA_ROOT"] = str(
        (
            (runtime_dir if persistent_run is not None else readiness_dir) / "webview2-user-data"
        ).resolve()
    )
    if scenario.id == "05-formula-lifecycle":
        environment["VIBETABLE_E2E_MIGRATION_FAULT_FILE"] = str(
            (controls_dir / "migration-fault.phase").resolve()
        )
    mutation_barrier = None
    if (
        scenario.id == "44-file-restore-crash"
        and persistent_run is not None
        and persistent_run.phase == "seed"
    ):
        environment["VIBETABLE_E2E_FILE_RESTORE_BARRIER_DIR"] = str(controls_dir)
    if scenario.id in {"09-atomic-import-scale", "36-backend-import-exit"}:
        barrier_arm = controls_dir / "mutation-barrier.arm"
        barrier_arm.write_text("armed\n", encoding="utf-8")
        environment["VIBETABLE_E2E_MUTATION_BARRIER_DIR"] = str(controls_dir)
        mutation_barrier = {
            "environment": "VIBETABLE_E2E_MUTATION_BARRIER_DIR",
            "arm": str(barrier_arm),
            "ready": str(controls_dir / "mutation-barrier.ready.json"),
            "point": "after_record",
        }
    command = [
        str(host),
        "--test-mode",
        "--readiness-dir",
        str(readiness_dir),
        "--e2e-controls-dir",
        str(controls_dir),
    ]
    (scenario_dir / "launch.json").write_text(
        json.dumps(
            {
                "command": command,
                "cwd": str(package_root),
                "cdpUrl": f"http://127.0.0.1:{port}",
                "dataRoot": str(readiness_dir / "local-data"),
                "controlsDirectory": str(controls_dir),
                "mutationBarrier": mutation_barrier,
                "renderer": "the WPF host's WebView2; no browser launch",
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    with ExitStack() as resources:
        stdout = resources.enter_context((scenario_dir / "host-stdout.log").open("wb"))
        stderr = resources.enter_context((scenario_dir / "host-stderr.log").open("wb"))
        # Wall-clock segments use the existing launch/CDP, driver, and exit
        # boundaries so report consumers can separate in-scenario action time
        # (durationMs) from orchestration overhead.
        phase_timings: dict[str, float] = {}
        startup_started = time.monotonic()
        try:
            scope = _launch_host_process(
                command,
                cwd=package_root,
                env=environment,
                stdout=stdout,
                stderr=stderr,
            )
        except (OSError, RuntimeError) as exc:
            return _failure_result(
                scenario,
                code="PROCESS_SCOPE_LAUNCH_FAILED",
                message=str(exc),
            ) | {
                "evidenceDirectory": str(scenario_dir),
                "lifecycle": {
                    "normalExitRequested": False,
                    "hostExitCode": None,
                    "membersAfterExit": [],
                    "descendantsAfterExit": [],
                    "portsReleased": False,
                    "errors": [str(exc)],
                    "status": "failed",
                },
                "lifecycleError": str(exc),
            }
        cleanup_state: dict[str, Any] = {}
        resources.enter_context(_product_scope_lifetime(scope, cleanup_state))
        result: dict[str, Any]
        normal_exit_allowed = False
        infrastructure_error: str | None = None
        cdp_owner: WindowsTcpListenerOwnerLease | None = None
        try:
            process_network = (
                {"observations": {}, "errors": [], "samples": 0}
                if scenario.id in {"01-offline-first-start", "35-data-io-interoperability"}
                else None
            )
            _wait_for_cdp(port, scope, process_network, readiness_dir)
            cdp_owner = WindowsTcpListenerOwnerLease.capture(port)
            resources.enter_context(_close_owner_on_primary_error(cdp_owner))
            phase_timings["startupMs"] = time.monotonic() - startup_started
            node_command = [
                node,
                str(NODE_RUNNER),
                "--cdp-url",
                f"http://127.0.0.1:{port}",
                "--scenario",
                scenario.id,
                "--evidence-dir",
                str(scenario_dir),
                "--controls-dir",
                str(controls_dir),
                "--python-executable",
                sys.executable,
                "--data-root",
                str(readiness_dir / "local-data"),
            ]
            if persistent_run is not None:
                node_command.extend(
                    [
                        "--persistent-phase",
                        persistent_run.phase,
                        "--state",
                        str(persistent_run.state_path),
                    ]
                )
            driver_started = time.monotonic()
            node_returncode, node_stdout, node_stderr = _run_node_runner(
                node_command,
                scenario_dir=scenario_dir,
                local_data=readiness_dir / "local-data",
                host_scope=scope,
                process_network=process_network,
                measure_diff_worker=scenario.id
                in {"14-document-diff", "39-file-workflow-combination"}
                and (persistent_run is None or persistent_run.phase == "seed"),
            )
            phase_timings["driverMs"] = time.monotonic() - driver_started
            (scenario_dir / "runner-stdout.log").write_text(node_stdout, encoding="utf-8")
            (scenario_dir / "runner-stderr.log").write_text(node_stderr, encoding="utf-8")
            readiness = _wait_for_readiness(readiness_dir, scope)
            normal_exit_allowed = readiness.get("ready") is True
            result_path = scenario_dir / f"{scenario.id}-result.json"
            result = _read_json(result_path) or _failure_result(
                scenario,
                code="RUNNER_RESULT_MISSING",
                message="Playwright runner did not write a structured result",
            )
            process_network_report = _read_json(scenario_dir / "process-network-observations.json")
            result.update(
                {
                    "title": scenario.title,
                    "requirement": scenario.requirement,
                    "readiness": readiness,
                    "hostExitCodeBeforeCleanup": scope.root.poll(),
                    "hostRootPid": scope.root.pid,
                    "nodeExitCode": node_returncode,
                    "evidenceDirectory": str(scenario_dir),
                    "processNetwork": process_network_report,
                    "documentDiffWorkerMemory": _read_json(
                        scenario_dir / "document-diff-worker-memory.json"
                    ),
                }
            )
            if readiness.get("ready") is not True:
                result["status"] = "failed"
                result["error"] = {
                    "code": "HOST_NOT_READY",
                    "message": str(readiness.get("error") or readiness),
                }
            elif scenario.id in {"01-offline-first-start", "35-data-io-interoperability"} and (
                process_network_report is None
                or process_network_report.get("status") != "completed"
                or process_network_report.get("samples", 0) < 1
                or bool(process_network_report.get("errors"))
                or bool(process_network_report.get("unexpectedProductNonLoopback"))
            ):
                result["status"] = "failed"
                result["error"] = {
                    "code": "PROCESS_NETWORK_OBSERVATION_FAILED",
                    "message": (
                        "Job-scope network observation was unavailable or found "
                        "a VibeTable-owned non-loopback listener/remote endpoint"
                    ),
                    "details": process_network_report,
                }
            elif node_returncode != 0:
                result["status"] = "failed"
                # The Node runner catches scenario assertions, persists their
                # structured error, and intentionally exits non-zero. Preserve
                # that root cause; only synthesize an infrastructure error when
                # Node crashed or returned a contradictory passing document.
                if not isinstance(result.get("error"), dict):
                    result["error"] = {
                        "code": "NODE_RUNNER_FAILED",
                        "message": (
                            f"Playwright runner exited with code {node_returncode}: "
                            f"{node_stderr.strip() or 'no stderr'}"
                        ),
                    }
            elif (
                scenario.id in {"14-document-diff", "39-file-workflow-combination"}
                and (persistent_run is None or persistent_run.phase == "seed")
                and (
                    not (result.get("documentDiffWorkerMemory") or {}).get("workers")
                    or (result.get("documentDiffWorkerMemory") or {}).get("errors")
                )
            ):
                result["status"] = "failed"
                result["error"] = {
                    "code": "DOCUMENT_DIFF_WORKER_MEMORY_UNVERIFIED",
                    "message": "The actual packaged Worker had no verified memory samples or observation errors.",
                    "details": result.get("documentDiffWorkerMemory"),
                }
            elif result.get("status") != "passed":
                result["status"] = "failed"
                result["error"] = {
                    "code": "RESULT_EXIT_MISMATCH",
                    "message": "runner exited zero without a passing result",
                }
        except Exception as exc:
            infrastructure_error = str(exc)
            result = _failure_result(
                scenario,
                code="E2E_INFRASTRUCTURE_FAILED",
                message=str(exc),
            ) | {"evidenceDirectory": str(scenario_dir)}
        exit_started = time.monotonic()
        restore_crash = _read_json(scenario_dir / "fault-result.json")
        intentional_restore_crash = (
            scenario.id == "44-file-restore-crash"
            and persistent_run is not None
            and persistent_run.phase == "seed"
            and restore_crash is not None
            and restore_crash.get("status") == "completed"
            and result.get("intentionalRestoreCrash") == restore_crash
            and cdp_owner is not None
        )
        if intentional_restore_crash:
            assert restore_crash is not None
            assert cdp_owner is not None
            lifecycle = _observe_restore_crash_exit(scope, restore_crash, cdp_owner)
        elif normal_exit_allowed and cdp_owner is not None:
            try:
                lifecycle = _request_normal_exit(
                    scope,
                    controls_dir=controls_dir,
                    cdp_owner=cdp_owner,
                )
            except Exception as exc:
                lifecycle = _abort_scope(scope, reason=str(exc), cdp_owner=cdp_owner)
        else:
            lifecycle = _abort_scope(
                scope,
                reason=(
                    infrastructure_error
                    or (
                        "CDP listener owner lease was unavailable"
                        if normal_exit_allowed
                        else "host readiness did not permit normal exit"
                    )
                ),
                cdp_owner=cdp_owner,
            )
        phase_timings["exitMs"] = time.monotonic() - exit_started
        result["lifecycle"] = lifecycle
        lifecycle_errors = list(lifecycle.get("errors", []))
        lifecycle_cleanup = lifecycle.get("cleanup")
        if isinstance(lifecycle_cleanup, dict):
            lifecycle_errors.extend(str(item) for item in lifecycle_cleanup.get("errors", []))
        if lifecycle_errors:
            result["lifecycleError"] = "; ".join(lifecycle_errors)
        if lifecycle["status"] != "passed":
            if result.get("status") == "passed":
                result["status"] = "failed"
                result["error"] = {
                    "code": "HOST_LIFECYCLE_FAILED",
                    "message": "正常关闭后 Job 作用域仍有成员、端口或清理错误。",
                    "details": lifecycle,
                }
            else:
                result["lifecycleFailure"] = lifecycle
        if phase_timings:
            result["runnerPhasesMs"] = {
                key: round(value * 1000, 2) for key, value in phase_timings.items()
            }
        cleanup_state["result"] = result
        return result


def _observe_restore_crash_exit(
    scope: _LifecycleScope,
    crash: Mapping[str, Any],
    cdp_owner: _PortOwnerLease,
) -> dict[str, Any]:
    """The 44 seed's explicit owned Job crash has a separate exit contract."""
    # TerminateJobObject is asynchronous: the Job members, ports and cleanup
    # can already be settled while the root's exit code is not yet observable
    # through a zero-wait poll (CI 37243530031 seed: poll None at 34 ms —
    # settled Job/ports state does NOT guarantee the root already signaled;
    # the observer must wait for the real exit). Mirror the normal exit
    # observer's discipline: one absolute lifecycle budget, wait on the root's
    # already-held stable handle (never reopen the pid), then observe the
    # members and the CDP listener with the remaining budget. No polling, no
    # fabricated exit code, no widened timeout; a missed or failing wait
    # stays failed.
    deadline = time.monotonic() + LIFECYCLE_EXIT_TIMEOUT_SECONDS

    def remaining() -> float:
        return max(0.0, deadline - time.monotonic())

    errors: list[str] = []
    release: PortReleaseReport | None = None
    exit_code: int | None = None
    try:
        exit_code = scope.root.wait(timeout=min(30.0, remaining()))
    except subprocess.TimeoutExpired:
        pass
    except (OSError, RuntimeError) as exc:
        errors.append(str(exc))
    try:
        members = _scope_members(scope)
        release = cdp_owner.observe_release(timeout=remaining())
        errors.extend(release.errors)
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        members, exit_code = [], None
        errors.append(str(exc))
    owner_cleanup = _close_owner_lease(cdp_owner)
    errors.extend(owner_cleanup.errors)
    owned_host = crash.get("ownedHost") or {}
    passed = (
        owned_host.get("rootPid") == scope.root.pid
        and (owned_host.get("termination") or {}).get("status") == "passed"
        and owned_host.get("remainingPids") == []
        and not owned_host.get("errors")
        and exit_code is not None
        and not members
        and release is not None
        and release.released
        and not errors
    )
    return {
        "mode": "intentional-restore-crash",
        "normalExitRequested": False,
        "hostExitCode": exit_code,
        "membersAfterExit": members,
        "portsReleased": release is not None and release.released,
        "portRelease": None if release is None else release.as_artifact(),
        "ownerLeaseCleanup": owner_cleanup.as_artifact(),
        "ownedHost": owned_host,
        "errors": errors,
        "cleanup": owned_host.get("termination"),
        "status": "passed" if passed else "failed",
    }


def write_aggregate(
    path: Path,
    *,
    audit: dict[str, Any],
    results: list[dict[str, Any]],
) -> dict[str, Any]:
    passed = sum(item.get("status") == "passed" for item in results)
    report = {
        "contractVersion": "2.0",
        "generatedAt": datetime.now(UTC).isoformat(),
        "reportPath": str(path.resolve()),
        "status": "passed" if audit["passed"] and passed == len(results) else "failed",
        "transport": {
            "driver": "playwright-core",
            "connection": "chromium.connectOverCDP",
            "browserLaunchAllowed": False,
            "target": "real packaged WPF WebView2",
        },
        "packageAudit": audit,
        "summary": {
            "total": len(results),
            "passed": passed,
            "failed": len(results) - passed,
            "skipped": 0,
        },
        "performance": summarize_performance(results),
        "scenarios": results,
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    _write_json_atomic(path, report)
    return report


def run_product_acceptance(
    *,
    package_root: Path = DEFAULT_PACKAGE,
    evidence_root: Path = DEFAULT_EVIDENCE,
    selected: Sequence[str] = (),
    capabilities: Sequence[str] = (),
) -> tuple[int, dict[str, Any]]:
    scenarios = select_scenarios(
        load_scenarios(),
        scenario_ids=selected,
        capabilities=capabilities,
    )
    audit = audit_package(package_root)
    run_root = evidence_root / datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
    report_path = run_root / "product-e2e-report.json"
    if not audit["passed"]:
        results = [
            _failure_result(
                scenario,
                code="PACKAGE_AUDIT_FAILED",
                message="发布包完整性或新鲜度检查失败，场景未启动。",
                dependency="先用 scripts/build_next.py 生成与当前源代码一致的发布包。",
            )
            for scenario in scenarios
        ]
        return 1, write_aggregate(
            report_path,
            audit=audit,
            results=results,
        )
    if sys.platform != "win32":
        results = [
            _failure_result(
                scenario,
                code="WINDOWS_WEBVIEW2_REQUIRED",
                message="真实 WPF WebView2 产品 E2E 只能在 Windows 桌面会话运行。",
            )
            for scenario in scenarios
        ]
        return 1, write_aggregate(report_path, audit=audit, results=results)
    node = shutil.which("node")
    if not node:
        results = [
            _failure_result(
                scenario,
                code="NODE_REQUIRED",
                message="找不到 Node.js，无法运行 playwright-core CDP 客户端。",
            )
            for scenario in scenarios
        ]
        return 1, write_aggregate(report_path, audit=audit, results=results)
    results = [
        _failure_result(
            scenario,
            code="SCENARIO_NOT_STARTED",
            message="Runner did not start this scenario; the acceptance run was interrupted.",
        )
        for scenario in scenarios
    ]
    # Refresh the report around every scenario boundary so a killed process
    # still leaves one complete, parseable JSON that names the interruption
    # point. Unstarted and running scenarios stay explicit failed entries, so a
    # completed subset can never be mistaken for a passing run.
    write_aggregate(report_path, audit=audit, results=results)
    for index, scenario in enumerate(scenarios):
        results[index] = _failure_result(
            scenario,
            code="SCENARIO_IN_PROGRESS",
            message="Runner was interrupted while this scenario was executing.",
        )
        write_aggregate(report_path, audit=audit, results=results)
        print(f"[product-e2e] scenario {scenario.id} start", flush=True)
        scenario_started = time.monotonic()
        if scenario.id in _PERSISTENT_RESTART_SEED_FIELDS:
            result = _run_host_presentation_restart_acceptance(
                scenario,
                package_root=package_root.resolve(),
                run_root=run_root,
                node=node,
            )
        elif scenario.id == CAPACITY_SCENARIO_ID:
            result = _run_file_history_capacity_acceptance(
                scenario,
                package_root=package_root.resolve(),
                run_root=run_root,
                node=node,
            )
        else:
            result = run_scenario(
                scenario,
                package_root=package_root.resolve(),
                evidence_root=run_root,
                node=node,
            )
        wall_clock_ms = round((time.monotonic() - scenario_started) * 1000, 2)
        result["runnerWallClockMs"] = wall_clock_ms
        results[index] = result
        write_aggregate(report_path, audit=audit, results=results)
        print(
            f"[product-e2e] scenario {scenario.id} {result.get('status')} "
            f"wallClockMs={wall_clock_ms}",
            flush=True,
        )
        if result.get("status") != "passed":
            # A later scenario can hit the outer stage timeout before main()
            # prints its summary; flush the failed diagnostics inline so the
            # lane log still names this scenario and its error code.
            print(_format_failed_scenario(result), flush=True)
    report = write_aggregate(report_path, audit=audit, results=results)
    return (0 if report["status"] == "passed" else 1), report


def _run_host_presentation_restart_acceptance(
    scenario: Scenario,
    *,
    package_root: Path,
    run_root: Path,
    node: str,
) -> dict[str, Any]:
    """Run a restart scenario in two real Host processes sharing approved data."""
    required = _PERSISTENT_RESTART_SEED_FIELDS.get(scenario.id)
    if required is None:
        raise ValueError(f"scenario has no persistent restart acceptance: {scenario.id}")
    persistent_root = (run_root / scenario.id / "persistent").resolve()
    state_path = persistent_root / "seed-state.json"
    readiness_dir = persistent_root / "host"
    workspace_root = persistent_root / "workspace"
    seed_dir = run_root / scenario.id / "seed" / datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
    persistent_root.mkdir(parents=True, exist_ok=True)
    workspace_root.mkdir()
    seed = run_scenario(
        scenario,
        package_root=package_root,
        evidence_root=run_root,
        node=node,
        persistent_run=_PersistentScenarioRun(
            phase="seed",
            state_path=state_path,
            scenario_dir=seed_dir,
            readiness_dir=readiness_dir,
            workspace_root=workspace_root,
        ),
    )
    phase_results: dict[str, Any] = {"seed": seed}
    if seed.get("status") != "passed" or seed.get("lifecycle", {}).get("status") != "passed":
        return _host_presentation_phase_failure(
            scenario, phase_results, "HOST_PRESENTATION_SEED_FAILED"
        )
    created_workspace_root = _resolve_persistent_workspace_root(
        workspace_root, readiness_dir, seed.get("workspaceId")
    )
    if not all(seed.get(name) is not None for name in required) or created_workspace_root is None:
        return _host_presentation_phase_failure(
            scenario, phase_results, "HOST_PRESENTATION_SEED_INVALID"
        )
    workspace_root = created_workspace_root
    try:
        _write_json_atomic(
            state_path,
            {
                "formatVersion": 1,
                "workspaceRoot": str(workspace_root),
                "localData": str((readiness_dir / "local-data").resolve()),
                **{name: seed[name] for name in required},
            },
        )
    except OSError:
        return _host_presentation_phase_failure(
            scenario, phase_results, "HOST_PRESENTATION_STATE_WRITE_FAILED"
        )
    # The first host has proven normal close before the second is allowed to
    # consume its local data. Its old readiness is deliberately not evidence of
    # the second launch.
    (readiness_dir / "vibetable-readiness.json").unlink(missing_ok=True)
    resume_dir = run_root / scenario.id / "resume" / datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
    resume = run_scenario(
        scenario,
        package_root=package_root,
        evidence_root=run_root,
        node=node,
        persistent_run=_PersistentScenarioRun(
            phase="resume",
            state_path=state_path,
            scenario_dir=resume_dir,
            readiness_dir=readiness_dir,
            workspace_root=workspace_root,
        ),
    )
    phase_results["resume"] = resume
    if resume.get("status") != "passed" or resume.get("lifecycle", {}).get("status") != "passed":
        return _host_presentation_phase_failure(
            scenario, phase_results, "HOST_PRESENTATION_RESUME_FAILED"
        )
    if scenario.id == "44-file-restore-crash" and (
        seed.get("lifecycle", {}).get("mode") != "intentional-restore-crash"
        or type(seed.get("hostRootPid")) is not int
        or type(resume.get("hostRootPid")) is not int
        or seed.get("lifecycle", {}).get("ownedHost", {}).get("rootPid") != seed.get("hostRootPid")
        or seed.get("hostRootPid") == resume.get("hostRootPid")
        or resume.get("lifecycle", {}).get("normalExitRequested") is not True
    ):
        return _host_presentation_phase_failure(
            scenario, phase_results, "RESTORE_CRASH_COLD_HOST_UNVERIFIED"
        )
    return {
        **resume,
        "scenario": scenario.id,
        "title": scenario.title,
        "requirement": scenario.requirement,
        "phases": phase_results,
        "persistentState": str(state_path),
    }


def _host_presentation_phase_failure(
    scenario: Scenario,
    phases: Mapping[str, Any],
    code: str,
) -> dict[str, Any]:
    return {
        **_failure_result(
            scenario,
            code=code,
            message="两阶段真实 Host 重启资格未完成。",
        ),
        "phases": dict(phases),
    }


def _capacity_producer_wait_empty(scope: _ManagedScope) -> dict[str, Any]:
    try:
        result = scope.wait_empty(timeout=5.0)
    except (OSError, RuntimeError) as exc:
        return {"remainingPids": None, "errors": [str(exc)], "status": "failed"}
    return {
        "remainingPids": (
            list(result.remaining_pids) if result.remaining_pids is not None else None
        ),
        "errors": list(result.errors),
        "status": "passed" if result.success else "failed",
    }


@contextmanager
def _capacity_producer_scope_lifetime(
    scope: _ManagedScope,
    evidence: dict[str, Any],
) -> Iterator[None]:
    """Prove the producer's Job scope is drained on success, failure or timeout.

    A normally exited root is demonstrated empty via wait_empty and then
    closed without an unconditional termination; any remaining members (the
    timeout case leaves the whole go test tree in the Job) are terminated and
    the outcome is recorded, never swallowed into a passing producer.
    """
    try:
        yield
    finally:
        cleanup: dict[str, Any] = {}
        wait = _capacity_producer_wait_empty(scope)
        cleanup["waitEmpty"] = wait
        termination: dict[str, Any] | None = None
        if wait["status"] != "passed":
            termination = _terminate_scope(scope)
            cleanup["termination"] = termination
        close_error = _close_scope(scope)
        cleanup["closeError"] = close_error
        failures = [
            *wait["errors"],
            *(termination["errors"] if termination is not None else []),
            *([close_error] if close_error is not None else []),
        ]
        cleanup["errors"] = failures
        cleanup["status"] = (
            "passed"
            if wait["status"] == "passed"
            and (termination is None or termination["status"] == "passed")
            and close_error is None
            and not failures
            else "failed"
        )
        evidence["lifecycle"] = cleanup


def _produce_file_history_capacity_fixtures(persistent_root: Path) -> dict[str, Any]:
    """Run the opt-in Go fixture producer once, fail-closed and bounded."""
    from qa.fault_injection import _resolve as resolve_tool  # reuse the exact Go resolver

    persistent_root.mkdir(parents=True, exist_ok=True)
    evidence: dict[str, Any] = {
        "status": "failed",
        "env": CAPACITY_FIXTURE_ENV,
        "test": CAPACITY_PRODUCER_TEST,
        "timeoutSeconds": CAPACITY_PRODUCER_TIMEOUT_SECONDS,
    }
    try:
        go = resolve_tool("go")
    except (OSError, RuntimeError) as exc:
        return evidence | {
            "code": "CAPACITY_GO_TOOLCHAIN_UNAVAILABLE",
            "error": str(exc),
        }
    fixture_root = (CAPACITY_FIXTURE_BASE / datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")).resolve()
    if fixture_root.exists():
        return evidence | {
            "code": "CAPACITY_FIXTURE_TARGET_EXISTS",
            "error": f"refusing to overwrite an existing fixture run: {fixture_root}",
        }
    environment = os.environ.copy()
    environment[CAPACITY_FIXTURE_ENV] = str(fixture_root)
    environment["GOTOOLCHAIN"] = "local"
    environment.setdefault("GOCACHE", str(ROOT / "build" / "go-cache"))
    environment.setdefault("GOTMPDIR", str(ROOT / "build" / "go-tmp"))
    Path(environment["GOCACHE"]).mkdir(parents=True, exist_ok=True)
    Path(environment["GOTMPDIR"]).mkdir(parents=True, exist_ok=True)
    command = [
        go,
        "test",
        "-count=1",
        f"-run=^{CAPACITY_PRODUCER_TEST}$",
        "./internal/workspacev2",
    ]
    stdout_path = persistent_root / "producer-stdout.log"
    stderr_path = persistent_root / "producer-stderr.log"
    evidence |= {
        "command": command,
        "fixtureRoot": str(fixture_root),
        "stdout": str(stdout_path),
        "stderr": str(stderr_path),
    }
    started = time.monotonic()
    timed_out = False
    returncode: int | None = None
    with ExitStack() as resources:
        stdout = resources.enter_context(stdout_path.open("wb"))
        stderr = resources.enter_context(stderr_path.open("wb"))
        try:
            scope = _launch_host_process(
                command,
                cwd=ROOT / "sidecar",
                env=environment,
                stdout=stdout,
                stderr=stderr,
            )
        except (OSError, RuntimeError) as exc:
            return evidence | {
                "code": "CAPACITY_PRODUCER_SCOPE_LAUNCH_FAILED",
                "error": str(exc),
            }
        # The log files stream directly from the Job's go test tree, so any
        # partial output before a timeout or failure is preserved as evidence.
        with _capacity_producer_scope_lifetime(scope, evidence):
            try:
                returncode = scope.root.wait(timeout=CAPACITY_PRODUCER_TIMEOUT_SECONDS)
            except subprocess.TimeoutExpired:
                timed_out = True
    elapsed = round(time.monotonic() - started, 3)
    evidence |= {"returncode": returncode, "elapsedSeconds": elapsed}
    for scale in CAPACITY_FIXTURE_SCALES:
        metadata_path = fixture_root / scale / "fixture.json"
        if metadata_path.is_file():
            retained = persistent_root / "fixtures" / f"{scale}.json"
            retained.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(metadata_path, retained)
    lifecycle = evidence.get("lifecycle")
    if timed_out:
        return evidence | {
            "code": "CAPACITY_FIXTURE_PRODUCTION_TIMEOUT",
            "error": (
                f"go test exceeded {CAPACITY_PRODUCER_TIMEOUT_SECONDS}s; "
                "Job cleanup evidence is recorded in lifecycle; no retry"
            ),
        }
    if not isinstance(lifecycle, dict) or lifecycle.get("status") != "passed":
        cleanup_errors = lifecycle.get("errors", []) if isinstance(lifecycle, dict) else []
        return evidence | {
            "code": "CAPACITY_PRODUCER_CLEANUP_FAILED",
            "error": "producer Job scope cleanup did not complete cleanly: "
            + "; ".join(str(item) for item in cleanup_errors),
        }
    if returncode != 0:
        return evidence | {
            "code": "CAPACITY_FIXTURE_PRODUCTION_FAILED",
            "error": f"go test exited {returncode}; no retry",
        }
    fixtures: dict[str, Path] = {}
    problems: list[str] = []
    for scale in CAPACITY_FIXTURE_SCALES:
        metadata_path = fixture_root / scale / "fixture.json"
        metadata = _read_json(metadata_path)
        entries = metadata.get("fixtures") if isinstance(metadata, dict) else None
        entry = entries[0] if isinstance(entries, list) and len(entries) == 1 else None
        if not isinstance(entry, dict):
            problems.append(f"{scale}: fixture.json lacks fixtures[0]")
            continue
        missing = [
            name
            for name in CAPACITY_REQUIRED_FIXTURE_FIELDS
            if not isinstance(entry.get(name), str) or not entry[name]
        ]
        nested_identity = entry.get("identity")
        identity = nested_identity if isinstance(nested_identity, dict) else entry
        missing += [
            f"identity.{name}"
            for name in CAPACITY_REQUIRED_IDENTITY_FIELDS
            if not isinstance(identity.get(name), (str, int)) or identity.get(name) in (None, "", 0)
        ]
        if missing:
            problems.append(f"{scale}: missing {', '.join(missing)}")
            continue
        expected_workspace = (fixture_root / scale / "workspace").resolve()
        expected_manifest = expected_workspace / ".vibetable" / "workspace.json"
        if (
            entry["name"] != scale
            or Path(entry["workspaceRoot"]).resolve() != expected_workspace
            or Path(entry["manifestPath"]).resolve() != expected_manifest
        ):
            problems.append(f"{scale}: workspace paths escape the synthetic fixture")
            continue
        fixtures[scale] = metadata_path
    if problems or set(fixtures) != set(CAPACITY_FIXTURE_SCALES):
        return evidence | {
            "code": "CAPACITY_FIXTURE_METADATA_INVALID",
            "error": "; ".join(problems) or "scale set mismatch",
        }
    return evidence | {"status": "passed", "fixtures": {k: str(v) for k, v in fixtures.items()}}


def _run_file_history_capacity_acceptance(
    scenario: Scenario,
    *,
    package_root: Path,
    run_root: Path,
    node: str,
) -> dict[str, Any]:
    """Produce the capacity fixtures once, then run each scale in its own cold Host."""
    persistent_root = (run_root / scenario.id / "persistent").resolve()
    persistent_root.mkdir(parents=True, exist_ok=True)
    producer = _produce_file_history_capacity_fixtures(persistent_root)
    phase_results: dict[str, Any] = {"producer": producer}
    if producer.get("status") != "passed":
        return _capacity_phase_failure(
            scenario, phase_results, "CAPACITY_FIXTURE_PRODUCTION_FAILED"
        )
    scale_summaries: dict[str, Any] = {}
    combined_ui_timings: list[Any] = []
    scale_failures: list[str] = []
    for scale in CAPACITY_FIXTURE_SCALES:
        fixture_path = Path(str(producer["fixtures"][scale])).resolve()
        metadata = _read_json(fixture_path) or {}
        entry = (metadata.get("fixtures") or [{}])[0]
        identity = entry.get("identity") if isinstance(entry.get("identity"), dict) else entry
        workspace_root = Path(str(entry["workspaceRoot"])).resolve()
        manifest = _read_json(workspace_root / ".vibetable" / "workspace.json")
        if manifest is None or manifest.get("workspaceId") != identity.get("workspaceId"):
            phase_results[scale] = {
                "status": "failed",
                "error": {"code": "CAPACITY_FIXTURE_WORKSPACE_IDENTITY_INVALID"},
                "fixture": str(fixture_path),
            }
            return _capacity_phase_failure(scenario, phase_results, "CAPACITY_SCALE_FAILED")
        scale_dir = (
            run_root / scenario.id / "scales" / scale / datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
        )
        readiness_dir = persistent_root / "host" / scale
        result = run_scenario(
            scenario,
            package_root=package_root.resolve(),
            evidence_root=run_root,
            node=node,
            persistent_run=_PersistentScenarioRun(
                phase="capacity",
                state_path=fixture_path,
                scenario_dir=scale_dir,
                readiness_dir=readiness_dir,
                workspace_root=workspace_root,
            ),
        )
        phase_results[scale] = result
        combined_ui_timings.extend(result.get("uiTimings") or [])
        scale_summaries[scale] = {
            "status": result.get("status"),
            "lifecycle": (result.get("lifecycle") or {}).get("status"),
            "evidenceDirectory": result.get("evidenceDirectory"),
            "runnerPhasesMs": result.get("runnerPhasesMs"),
            "runnerWallClockMs": result.get("runnerWallClockMs"),
        }
        if (
            result.get("status") != "passed"
            or (result.get("lifecycle") or {}).get("status") != "passed"
        ):
            scale_failures.append(scale)
    if scale_failures:
        return _capacity_phase_failure(scenario, phase_results, "CAPACITY_SCALE_FAILED")
    return {
        "scenario": scenario.id,
        "title": scenario.title,
        "requirement": scenario.requirement,
        "status": "passed",
        "durationMs": round(
            sum(
                float(phase_results[scale].get("durationMs") or 0.0)
                for scale in CAPACITY_FIXTURE_SCALES
            )
            + float(producer.get("elapsedSeconds") or 0.0) * 1000,
            2,
        ),
        "runnerPhasesMs": {
            scale: phase_results[scale].get("runnerPhasesMs") for scale in CAPACITY_FIXTURE_SCALES
        },
        "uiTimings": combined_ui_timings,
        "scales": scale_summaries,
        "phases": phase_results,
        "persistentState": str(persistent_root),
    }


def _capacity_phase_failure(
    scenario: Scenario,
    phases: Mapping[str, Any],
    code: str,
) -> dict[str, Any]:
    return {
        **_failure_result(
            scenario,
            code=code,
            message="真实 Host 合法容量资格未完成。",
        ),
        "phases": dict(phases),
    }


def run_natural_aging_phase(
    *,
    phase: str,
    state_path: Path,
    package_root: Path,
    evidence_root: Path,
    package_audit: Mapping[str, Any],
) -> dict[str, Any]:
    """Run the dedicated cross-day retention acceptance phase."""
    if phase not in {"seed", "resume"}:
        raise ValueError("natural retention aging phase must be seed or resume")
    if sys.platform != "win32":
        raise ValueError("natural retention aging requires Windows WebView2")
    state_path = state_path.resolve()
    state_root = state_path.parent
    if phase == "seed":
        if state_path.exists():
            raise ValueError("natural retention seed state already exists")
        workspace_root = state_root / "workspace"
        readiness_dir = state_root / "host"
        local_data = readiness_dir / "local-data"
        if workspace_root.exists() or local_data.exists():
            raise ValueError("natural retention seed root must be fresh")
    else:
        seed = _read_json(state_path)
        if seed is None:
            raise ValueError("natural retention seed state is unavailable")
        workspace_root = Path(str(seed["workspaceRoot"])).resolve()
        local_data = Path(str(seed["localData"])).resolve()
        readiness_dir = local_data.parent
    run_dir = (
        evidence_root.resolve()
        / "natural-retention-aging"
        / phase
        / datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
    )
    if phase == "seed":
        state_root.mkdir(parents=True, exist_ok=True)
        workspace_root.mkdir()
    readiness_dir.mkdir(parents=True, exist_ok=True)
    (readiness_dir / "vibetable-readiness.json").unlink(missing_ok=True)
    result = run_scenario(
        _NATURAL_AGING_SCENARIO,
        package_root=package_root.resolve(),
        evidence_root=evidence_root.resolve(),
        node=str(ensure_node(ROOT)),
        persistent_run=_PersistentScenarioRun(
            phase=phase,
            state_path=state_path,
            scenario_dir=run_dir,
            readiness_dir=readiness_dir,
            workspace_root=workspace_root,
        ),
    )

    if phase == "seed" and result.get("status") == "passed":
        required = ("workspaceId", "olderSnapshotId", "newerSnapshotId")
        if not all(isinstance(result.get(name), str) and result[name] for name in required):
            result["status"] = "failed"
            result["error"] = {"code": "NATURAL_AGING_RESULT_INVALID"}
        else:
            created_workspace_root = _resolve_persistent_workspace_root(
                workspace_root, readiness_dir, result["workspaceId"]
            )
            if created_workspace_root is None:
                result["status"] = "failed"
                result["error"] = {"code": "NATURAL_AGING_WORKSPACE_IDENTITY_INVALID"}
            else:
                workspace_root = created_workspace_root

    report_path = run_dir / "natural-retention-aging-report.json"
    report = write_aggregate(report_path, audit=dict(package_audit), results=[result])
    if report["status"] != "passed":
        result["status"] = "failed"
        return result
    if phase == "seed":
        completed_at = datetime.now(UTC)
        try:
            _write_json_atomic(
                state_path,
                {
                    "formatVersion": 1,
                    "phase": "seeded",
                    "completedAt": completed_at.isoformat(),
                    "notBefore": (completed_at + timedelta(hours=24)).isoformat(),
                    "workspaceRoot": str(workspace_root.resolve()),
                    "workspaceId": result["workspaceId"],
                    "localData": str(local_data.resolve()),
                    "packageFingerprint": package_audit["fingerprint"],
                    "olderSnapshotId": result["olderSnapshotId"],
                    "newerSnapshotId": result["newerSnapshotId"],
                    "seedEvidence": str(run_dir),
                },
            )
        except OSError as exc:
            result["status"] = "failed"
            result["error"] = {"code": "NATURAL_AGING_STATE_WRITE_FAILED", "message": str(exc)}
            write_aggregate(report_path, audit=dict(package_audit), results=[result])
            raise
    return result


def _resolve_persistent_workspace_root(
    workspace_root: Path,
    readiness_dir: Path,
    workspace_id: object,
) -> Path | None:
    """Bind the created UUID to its registered picker or managed-default root."""
    if not isinstance(workspace_id, str) or not workspace_id:
        return None
    registry = _read_json(
        readiness_dir / "local-data" / "VibeTable" / "shell" / "workspace-registry-v2.json"
    )
    if registry is None:
        return None
    workspaces = registry.get("workspaces")
    if not isinstance(workspaces, list):
        return None
    matches = [
        item
        for item in workspaces
        if isinstance(item, dict) and item.get("workspaceId") == workspace_id
    ]
    if len(matches) != 1 or not isinstance(matches[0].get("selectedRoot"), str):
        return None
    selected_root = Path(matches[0]["selectedRoot"])
    if not selected_root.is_absolute():
        return None
    selected_root = selected_root.resolve()
    managed_parent = (readiness_dir / "local-data" / "workspaces").resolve()
    is_managed_root = selected_root.parent == managed_parent and selected_root.name == workspace_id
    if selected_root != workspace_root.resolve() and not is_managed_root:
        return None
    manifest = _read_json(selected_root / ".vibetable" / "workspace.json")
    if manifest is None or manifest.get("workspaceId") != workspace_id:
        return None
    return selected_root


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package-root", type=Path, default=DEFAULT_PACKAGE)
    parser.add_argument("--evidence-root", type=Path, default=DEFAULT_EVIDENCE)
    parser.add_argument("--scenario", action="append", default=[])
    parser.add_argument("--capability", action="append", default=[])
    return parser


def _format_failed_scenario(item: dict[str, Any]) -> str:
    error = item.get("error")
    if not isinstance(error, dict):
        error = {}
    summary = (
        f"  - {item.get('scenario', '<unknown>')}: "
        f"{error.get('code', 'FAILED')} {error.get('message', '')}"
    )
    diagnostics = item.get("bridgeDiagnostics")
    if isinstance(diagnostics, dict):
        failures = diagnostics.get("failures")
        pending = diagnostics.get("pending")
        round_trips = diagnostics.get("roundTrips")
        compact_diagnostics: dict[str, Any] = {}
        if isinstance(failures, list) and failures:
            compact_diagnostics["failures"] = failures
        if isinstance(pending, list) and pending:
            compact_diagnostics["pending"] = pending
        if isinstance(round_trips, list) and round_trips:
            compact_diagnostics["recentRoundTrips"] = round_trips[-12:]
        if compact_diagnostics:
            compact = json.dumps(
                compact_diagnostics,
                ensure_ascii=False,
                separators=(",", ":"),
            )
            summary += f" bridgeDiagnostics={compact}"
    max_chars = 4_000
    if len(summary) > max_chars:
        return f"{summary[: max_chars - 3]}..."
    return summary


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        code, report = run_product_acceptance(
            package_root=args.package_root,
            evidence_root=args.evidence_root,
            selected=args.scenario,
            capabilities=args.capability,
        )
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"[FAIL] product E2E configuration: {exc}", file=sys.stderr)
        return 2
    summary = report["summary"]
    print(
        f"[{report['status'].upper()}] real WPF/WebView2 product E2E: "
        f"{summary['passed']}/{summary['total']} passed, "
        f"{summary['failed']} failed, 0 skipped"
    )
    print("report:", report["reportPath"])
    for item in report["scenarios"]:
        if item["status"] != "passed":
            print(_format_failed_scenario(item))
    return code


if __name__ == "__main__":
    raise SystemExit(main())
