"""Black-box lifecycle evidence for the packaged WPF host.

This module launches ``VibeTable.Next.exe`` itself. Fixed test-mode files only
ask the real window/tray/session policies to act; they do not launch a fake
sidecar or synthesize a successful host result.
"""

from __future__ import annotations

import argparse
import ctypes
import json
import os
import time
from collections.abc import Iterator
from contextlib import ExitStack, contextmanager
from pathlib import Path
from typing import Any

from scripts.qa.windows_process_scope import WindowsProcessScope
from tests.e2e import product_e2e_runner as product_runner
from tests.e2e.windows_tcp_listener_owner import WindowsTcpListenerOwnerLease

ROOT = Path(__file__).resolve().parents[2]
WINDOW_CLOSE_CONTROL_FILE = "host-window-close.request"
TRAY_EXIT_CONTROL_FILE = "host-tray-exit.request"
OPEN_WORKSPACE_CONTROL_FILE = "host-open-workspace.request"
HOST_STATE_FILE = "host-lifecycle-state.json"
WORKSPACE_ID = "11111111-1111-4111-8111-111111111111"
STARTUP_MIGRATION_FAULT_ENVIRONMENT = "VIBETABLE_E2E_STARTUP_MIGRATION_FAULT_FILE"


def _layout(package_root: Path) -> dict[str, Any]:
    path = product_runner._package_layout_path(package_root)
    decoded = json.loads(path.read_text(encoding="utf-8"))
    assert isinstance(decoded, dict)
    return decoded


def _host_executable(package_root: Path) -> Path:
    layout = _layout(package_root)
    launch = layout.get("launch")
    assert isinstance(launch, dict), "package layout launch section is missing"
    relative = launch.get("host")
    assert isinstance(relative, str), "package host launch path is not a string"
    assert relative, "package host launch path is missing"
    host = (package_root / relative).resolve()
    assert host.is_file(), f"packaged host is missing: {host}"
    assert host.name.casefold() == "vibetable.next.exe", host
    return host


def _wait_for_state(
    controls_dir: Path,
    scope: WindowsProcessScope,
    action: str,
    *,
    timeout: float = 60.0,
) -> dict[str, Any]:
    path = controls_dir / HOST_STATE_FILE
    deadline = time.monotonic() + timeout
    latest: dict[str, Any] | None = None
    while time.monotonic() < deadline:
        latest = product_runner._read_json(path)
        if latest is not None and latest.get("action") == action:
            return latest
        exit_code = scope.root.poll()
        if exit_code is not None:
            raise RuntimeError(f"VibeTable.Next.exe exited with {exit_code} before {action!r}")
        time.sleep(0.1)
    raise TimeoutError(f"packaged host did not report {action!r}; latest={latest!r}")


def _launch_host(
    package_root: Path,
    runtime_root: Path,
    *,
    autostart: bool,
    tray_lifecycle: bool,
    extra_environment: dict[str, str] | None = None,
) -> tuple[WindowsProcessScope, int, Path, ExitStack]:
    host = _host_executable(package_root)
    readiness_dir = runtime_root / "host"
    controls_dir = runtime_root / "controls"
    readiness_dir.mkdir(parents=True, exist_ok=True)
    controls_dir.mkdir(exist_ok=True)
    port = product_runner._reserve_port()
    environment = os.environ.copy()
    environment["VIBETABLE_WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"] = (
        f"--remote-debugging-port={port} --disable-gpu"
    )
    environment["VIBETABLE_E2E_WEBVIEW2_USER_DATA_ROOT"] = str(
        (readiness_dir / "webview2-user-data").resolve()
    )
    environment.update(extra_environment or {})
    command = [
        str(host),
        "--test-mode",
        "--readiness-dir",
        str(readiness_dir),
        "--e2e-controls-dir",
        str(controls_dir),
    ]
    if tray_lifecycle:
        command.append("--test-mode-tray-lifecycle")
    if autostart:
        command.append("--autostart")
    runtime_root.mkdir(exist_ok=True)
    (runtime_root / "launch.json").write_text(
        json.dumps(
            {
                "command": command,
                "cwd": str(package_root),
                "hostExecutable": str(host),
                "cdpUrl": f"http://127.0.0.1:{port}",
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    streams = ExitStack()
    try:
        stdout = streams.enter_context((runtime_root / "host-stdout.log").open("wb"))
        stderr = streams.enter_context((runtime_root / "host-stderr.log").open("wb"))
        scope = product_runner._launch_host_process(
            command,
            cwd=package_root,
            env=environment,
            stdout=stdout,
            stderr=stderr,
        )
    except BaseException as exc:
        try:
            streams.close()
        except BaseException as close_error:
            exc.add_note(f"closing packaged host log streams also failed: {close_error}")
        raise
    return scope, port, controls_dir, streams


def _request_tray_exit(
    scope: WindowsProcessScope,
    controls_dir: Path,
    cdp_owner: WindowsTcpListenerOwnerLease,
) -> dict[str, Any]:
    (controls_dir / TRAY_EXIT_CONTROL_FILE).write_text("tray-exit\n", encoding="utf-8")
    return product_runner._observe_scope_exit(scope, cdp_owner=cdp_owner)


def _request_window_close(
    scope: WindowsProcessScope,
    cdp_owner: WindowsTcpListenerOwnerLease,
) -> dict[str, Any]:
    """Send the standard WM_CLOSE used by a user closing the legacy Host window."""

    assert os.name == "nt"
    from ctypes import wintypes

    windows: list[int] = []
    callback_type = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)

    @callback_type
    def collect(hwnd: int, _parameter: int) -> bool:
        pid = wintypes.DWORD()
        ctypes.windll.user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
        if int(pid.value) == scope.root.pid and ctypes.windll.user32.IsWindowVisible(hwnd):
            windows.append(int(hwnd))
        return True

    ctypes.windll.user32.EnumWindows(collect, 0)
    assert windows, "legacy packaged Host has no visible main window"
    assert ctypes.windll.user32.PostMessageW(windows[0], 0x0010, 0, 0), (
        "WM_CLOSE could not be posted to the legacy packaged Host"
    )
    return product_runner._observe_scope_exit(scope, cdp_owner=cdp_owner)


def _cleanup_scope(scope: WindowsProcessScope) -> dict[str, Any]:
    cleanup = product_runner._terminate_scope(scope)
    close_error = product_runner._close_scope(scope)
    if close_error is not None:
        cleanup["errors"].append(close_error)
        cleanup["status"] = "failed"
    return cleanup


@contextmanager
def _scope_lifetime(scope: WindowsProcessScope, streams: ExitStack) -> Iterator[None]:
    primary_error: BaseException | None = None
    try:
        yield
    except BaseException as exc:
        primary_error = exc
        raise
    finally:
        cleanup = _cleanup_scope(scope)
        try:
            streams.close()
        except BaseException as exc:
            cleanup["errors"].append(str(exc))
            cleanup["status"] = "failed"
        if cleanup["status"] != "passed":
            message = f"packaged host Job cleanup failed: {cleanup!r}"
            if primary_error is not None:
                primary_error.add_note(message)
            else:
                raise RuntimeError(message)


@contextmanager
def _close_owner_on_primary_error(
    cdp_owner: WindowsTcpListenerOwnerLease,
) -> Iterator[None]:
    try:
        yield
    except BaseException as exc:
        cleanup = cdp_owner.close()
        for error in cleanup.errors:
            exc.add_note(error)
        raise


def _run_tray_case(package_root: Path, runtime_root: Path) -> dict[str, Any]:
    scope, port, controls, streams = _launch_host(
        package_root,
        runtime_root,
        autostart=False,
        tray_lifecycle=True,
        extra_environment=None,
    )
    with _scope_lifetime(scope, streams), ExitStack() as owner_resources:
        product_runner._wait_for_cdp(port, scope)
        cdp_owner = WindowsTcpListenerOwnerLease.capture(port)
        owner_resources.enter_context(_close_owner_on_primary_error(cdp_owner))
        readiness = product_runner._wait_for_readiness(runtime_root / "host", scope)
        visible = _wait_for_state(controls, scope, "visible-startup")
        assert readiness.get("ready") is True, readiness
        assert visible.get("windowVisible") is True, visible
        assert visible.get("trayVisible") is True, visible
        (controls / WINDOW_CLOSE_CONTROL_FILE).write_text("window-close\n", encoding="utf-8")
        minimized = _wait_for_state(controls, scope, "close-to-tray")
        assert scope.root.poll() is None, "close-to-tray terminated the packaged host"
        assert minimized.get("windowVisible") is False, minimized
        assert minimized.get("trayVisible") is True, minimized
        lifecycle = _request_tray_exit(scope, controls, cdp_owner)
        return {
            "status": lifecycle["status"],
            "startup": visible,
            "closeToTray": minimized,
            "lifecycle": lifecycle,
        }


def _run_silent_startup_case(package_root: Path, runtime_root: Path) -> dict[str, Any]:
    scope, port, controls, streams = _launch_host(
        package_root,
        runtime_root,
        autostart=True,
        tray_lifecycle=True,
        extra_environment=None,
    )
    with _scope_lifetime(scope, streams), ExitStack() as owner_resources:
        product_runner._wait_for_cdp(port, scope)
        cdp_owner = WindowsTcpListenerOwnerLease.capture(port)
        owner_resources.enter_context(_close_owner_on_primary_error(cdp_owner))
        readiness = product_runner._wait_for_readiness(runtime_root / "host", scope)
        silent = _wait_for_state(controls, scope, "silent-startup")
        assert readiness.get("ready") is True, readiness
        assert silent.get("windowVisible") is False, silent
        assert silent.get("trayVisible") is True, silent
        lifecycle = _request_tray_exit(scope, controls, cdp_owner)
        return {"status": lifecycle["status"], "startup": silent, "lifecycle": lifecycle}


def run_lifecycle(package_root: Path, evidence_root: Path) -> dict[str, Any]:
    assert os.name == "nt", "packaged WPF host lifecycle requires Windows"
    assert not evidence_root.exists(), f"evidence root must be fresh: {evidence_root}"
    evidence_root.mkdir(parents=True)
    tray = _run_tray_case(package_root, evidence_root / "close-to-tray")
    silent = _run_silent_startup_case(package_root, evidence_root / "silent-startup")
    return {
        "ok": tray["status"] == "passed" and silent["status"] == "passed",
        "evidenceKind": "packaged-host-lifecycle",
        "hostExecutable": "VibeTable.Next.exe",
        "closeToTrayAndTrayExit": tray,
        "silentStartup": silent,
    }


def _seed_workspace_registry(readiness_dir: Path, workspace_root: Path) -> None:
    manifest = json.loads(
        (workspace_root / ".vibetable" / "workspace.json").read_text(encoding="utf-8")
    )
    workspace_id = manifest.get("workspaceId")
    assert workspace_id == WORKSPACE_ID, manifest
    registry = {
        "formatVersion": 2,
        "workspaces": [
            {
                "contractVersion": "2.0",
                "workspaceId": workspace_id,
                "displayName": manifest.get("displayName", "Legacy upgrade"),
                "selectedRoot": str(workspace_root.resolve()),
                "activityRoot": None,
                "storageKind": "fixed",
                "coordinationStrength": "strong",
                "lastOpenedAt": None,
                "lastKnownHealth": "healthy",
                "lastSnapshotAt": None,
                "lastSyncAt": None,
                "pendingSync": False,
            }
        ],
    }
    target = readiness_dir / "local-data" / "VibeTable" / "shell"
    target.mkdir(parents=True)
    (target / "workspace-registry-v2.json").write_text(
        json.dumps(registry, ensure_ascii=False, separators=(",", ":")),
        encoding="utf-8",
    )


def workspace_open_evidence(
    observed: dict[str, Any],
    *,
    expect_open_failure: bool,
) -> dict[str, Any]:
    """Validate and normalize the packaged Host's observed session state."""

    workspace_id = observed.get("workspaceId")
    session_epoch = observed.get("sessionEpoch")
    session_state = observed.get("sessionState")
    session_published = any(
        value is not None for value in (workspace_id, session_epoch, session_state)
    )
    writable_session_published = session_state == "openedWritable"
    if expect_open_failure:
        assert not session_published, (
            f"faulted packaged Host published a workspace session: {observed!r}"
        )
        assert observed.get("error"), observed
        open_outcome = "rejected-before-open"
    else:
        assert str(workspace_id) == WORKSPACE_ID, observed
        assert isinstance(session_epoch, int), observed
        assert session_epoch > 0, observed
        assert writable_session_published, observed
        open_outcome = "opened-writable"
    return {
        "workspaceId": workspace_id,
        "sessionEpoch": session_epoch,
        "sessionState": session_state,
        "openOutcome": open_outcome,
        "writableSessionPublished": writable_session_published,
    }


def open_workspace_with_packaged_host(
    package_root: Path,
    workspace_root: Path,
    evidence_root: Path,
    *,
    startup_fault_file: Path | None = None,
    expect_open_failure: bool = False,
) -> dict[str, Any]:
    """Open and migrate an existing workspace through the packaged WPF host."""

    assert os.name == "nt", "packaged WPF host upgrade evidence requires Windows"
    assert not evidence_root.exists(), f"host evidence root must be fresh: {evidence_root}"
    evidence_root.mkdir(parents=True)
    readiness_dir = evidence_root / "host"
    _seed_workspace_registry(readiness_dir, workspace_root)
    scope, port, controls, streams = _launch_host(
        package_root,
        evidence_root,
        autostart=False,
        tray_lifecycle=False,
        extra_environment=(
            {STARTUP_MIGRATION_FAULT_ENVIRONMENT: str(startup_fault_file.resolve())}
            if startup_fault_file is not None
            else None
        ),
    )
    with _scope_lifetime(scope, streams), ExitStack() as owner_resources:
        product_runner._wait_for_cdp(port, scope)
        cdp_owner = WindowsTcpListenerOwnerLease.capture(port)
        owner_resources.enter_context(_close_owner_on_primary_error(cdp_owner))
        readiness = product_runner._wait_for_readiness(readiness_dir, scope)
        assert readiness.get("ready") is True, readiness
        (controls / OPEN_WORKSPACE_CONTROL_FILE).write_text(WORKSPACE_ID + "\n", encoding="utf-8")
        expected_action = "workspace-open-failed" if expect_open_failure else "workspace-opened"
        opened = _wait_for_state(controls, scope, expected_action)
        assert opened.get("hostExecutable", "").casefold() == "vibetable.next.exe", opened
        open_evidence = workspace_open_evidence(
            opened,
            expect_open_failure=expect_open_failure,
        )
        if expect_open_failure and startup_fault_file is not None:
            assert not startup_fault_file.exists(), "startup fault was not consumed once"
        lifecycle = product_runner._request_normal_exit(
            scope,
            controls_dir=controls,
            cdp_owner=cdp_owner,
        )
        return {
            "status": lifecycle["status"],
            "evidenceKind": "packaged-host-workspace-open",
            "hostExecutable": opened["hostExecutable"],
            **open_evidence,
            "error": opened.get("error"),
            "startupFaultConsumed": (
                startup_fault_file is not None and not startup_fault_file.exists()
            ),
            "readiness": readiness,
            "lifecycle": lifecycle,
        }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package-root", type=Path, required=True)
    parser.add_argument("--evidence-root", type=Path, required=True)
    parser.add_argument("--json-report", type=Path, required=True)
    args = parser.parse_args(argv)
    report = run_lifecycle(args.package_root.resolve(), args.evidence_root.resolve())
    args.json_report.parent.mkdir(parents=True, exist_ok=True)
    args.json_report.write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
