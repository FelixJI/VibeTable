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
import shutil
import subprocess
import time
from contextlib import ExitStack
from pathlib import Path
from typing import Any

from tests.e2e import product_e2e_runner as product_runner

ROOT = Path(__file__).resolve().parents[2]
WINDOW_CLOSE_CONTROL_FILE = "host-window-close.request"
TRAY_EXIT_CONTROL_FILE = "host-tray-exit.request"
OPEN_WORKSPACE_CONTROL_FILE = "host-open-workspace.request"
HOST_STATE_FILE = "host-lifecycle-state.json"
LEGACY_OPEN_RUNNER = Path(__file__).with_name("legacy_host_workspace_open.mjs")
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
    process: subprocess.Popen[bytes],
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
        if process.poll() is not None:
            raise RuntimeError(
                f"VibeTable.Next.exe exited with {process.returncode} before {action!r}"
            )
        time.sleep(0.1)
    raise TimeoutError(f"packaged host did not report {action!r}; latest={latest!r}")


def _launch_host(
    package_root: Path,
    runtime_root: Path,
    *,
    autostart: bool,
    tray_lifecycle: bool,
    extra_environment: dict[str, str] | None = None,
) -> tuple[subprocess.Popen[bytes], int, Path, ExitStack]:
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
    stdout = streams.enter_context((runtime_root / "host-stdout.log").open("wb"))
    stderr = streams.enter_context((runtime_root / "host-stderr.log").open("wb"))
    process = subprocess.Popen(
        command,
        cwd=package_root,
        env=environment,
        stdout=stdout,
        stderr=stderr,
    )
    return process, port, controls_dir, streams


def _observe_requested_exit(
    process: subprocess.Popen[bytes],
    *,
    tracked: dict[int, str],
    cdp_port: int,
) -> dict[str, Any]:
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        for pid, name in product_runner._descendants(process.pid):
            tracked[pid] = name
        if process.poll() is not None:
            break
        time.sleep(0.1)
    tracked_pids = set(tracked)
    for _ in range(50):
        alive = {pid for pid, _parent, _name in product_runner._windows_processes()}
        if not ((tracked_pids - {process.pid}) & alive):
            break
        time.sleep(0.1)
    surviving = {
        pid: name
        for pid, _parent, name in product_runner._windows_processes()
        if pid in tracked_pids
    }
    descendants_after_exit = [
        {"pid": pid, "name": name}
        for pid, name in tracked.items()
        if pid != process.pid and pid in surviving
    ]
    try:
        occupied = [
            row
            for row in product_runner._netstat_tcp_rows()
            if int(row["pid"]) in tracked_pids
            or (str(row["local"]).endswith(f":{cdp_port}") and row["state"] == "LISTENING")
        ]
        ports_released = not occupied
    except (OSError, RuntimeError, subprocess.SubprocessError):
        ports_released = False
    return product_runner._lifecycle_exit_report(
        normal_exit_requested=True,
        host_exit_code=process.poll(),
        descendants_after_exit=descendants_after_exit,
        ports_released=ports_released,
    )


def _request_tray_exit(
    process: subprocess.Popen[bytes], controls_dir: Path, port: int
) -> dict[str, Any]:
    tracked = {process.pid: "VibeTable.Next.exe"}
    for pid, name in product_runner._descendants(process.pid):
        tracked[pid] = name
    (controls_dir / TRAY_EXIT_CONTROL_FILE).write_text("tray-exit\n", encoding="utf-8")
    return _observe_requested_exit(process, tracked=tracked, cdp_port=port)


def _request_window_close(process: subprocess.Popen[bytes], port: int) -> dict[str, Any]:
    """Send the standard WM_CLOSE used by a user closing the legacy Host window."""

    assert os.name == "nt"
    tracked = {process.pid: "VibeTable.Next.exe"}
    for pid, name in product_runner._descendants(process.pid):
        tracked[pid] = name
    from ctypes import wintypes

    windows: list[int] = []
    callback_type = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)

    @callback_type
    def collect(hwnd: int, _parameter: int) -> bool:
        pid = wintypes.DWORD()
        ctypes.windll.user32.GetWindowThreadProcessId(hwnd, ctypes.byref(pid))
        if int(pid.value) == process.pid and ctypes.windll.user32.IsWindowVisible(hwnd):
            windows.append(int(hwnd))
        return True

    ctypes.windll.user32.EnumWindows(collect, 0)
    assert windows, "legacy packaged Host has no visible main window"
    assert ctypes.windll.user32.PostMessageW(windows[0], 0x0010, 0, 0), (
        "WM_CLOSE could not be posted to the legacy packaged Host"
    )
    return _observe_requested_exit(process, tracked=tracked, cdp_port=port)


def _run_tray_case(package_root: Path, runtime_root: Path) -> dict[str, Any]:
    process, port, controls, streams = _launch_host(
        package_root,
        runtime_root,
        autostart=False,
        tray_lifecycle=True,
        extra_environment=None,
    )
    try:
        product_runner._wait_for_cdp(port, process)
        readiness = product_runner._wait_for_readiness(runtime_root / "host", process)
        visible = _wait_for_state(controls, process, "visible-startup")
        assert readiness.get("ready") is True, readiness
        assert visible.get("windowVisible") is True, visible
        assert visible.get("trayVisible") is True, visible
        (controls / WINDOW_CLOSE_CONTROL_FILE).write_text("window-close\n", encoding="utf-8")
        minimized = _wait_for_state(controls, process, "close-to-tray")
        assert process.poll() is None, "close-to-tray terminated the packaged host"
        assert minimized.get("windowVisible") is False, minimized
        assert minimized.get("trayVisible") is True, minimized
        lifecycle = _request_tray_exit(process, controls, port)
        assert lifecycle["status"] == "passed", lifecycle
        return {
            "status": "passed",
            "startup": visible,
            "closeToTray": minimized,
            "lifecycle": lifecycle,
        }
    finally:
        product_runner._stop_process_tree(process)
        streams.close()


def _run_silent_startup_case(package_root: Path, runtime_root: Path) -> dict[str, Any]:
    process, port, controls, streams = _launch_host(
        package_root,
        runtime_root,
        autostart=True,
        tray_lifecycle=True,
        extra_environment=None,
    )
    try:
        product_runner._wait_for_cdp(port, process)
        readiness = product_runner._wait_for_readiness(runtime_root / "host", process)
        silent = _wait_for_state(controls, process, "silent-startup")
        assert readiness.get("ready") is True, readiness
        assert silent.get("windowVisible") is False, silent
        assert silent.get("trayVisible") is True, silent
        lifecycle = _request_tray_exit(process, controls, port)
        assert lifecycle["status"] == "passed", lifecycle
        return {"status": "passed", "startup": silent, "lifecycle": lifecycle}
    finally:
        product_runner._stop_process_tree(process)
        streams.close()


def run_lifecycle(package_root: Path, evidence_root: Path) -> dict[str, Any]:
    assert os.name == "nt", "packaged WPF host lifecycle requires Windows"
    assert not evidence_root.exists(), f"evidence root must be fresh: {evidence_root}"
    evidence_root.mkdir(parents=True)
    tray = _run_tray_case(package_root, evidence_root / "close-to-tray")
    silent = _run_silent_startup_case(package_root, evidence_root / "silent-startup")
    return {
        "ok": True,
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


def prepare_workspace_with_legacy_packaged_host(
    legacy_package_root: Path,
    workspace_root: Path,
    evidence_root: Path,
) -> dict[str, Any]:
    """Open a fresh workspace through the pinned old Host before seeding data."""

    assert os.name == "nt", "legacy packaged Host evidence requires Windows"
    assert not evidence_root.exists(), f"legacy host evidence root must be fresh: {evidence_root}"
    evidence_root.mkdir(parents=True)
    readiness_dir = evidence_root / "host"
    _seed_workspace_registry(readiness_dir, workspace_root)
    process, port, _controls, streams = _launch_host(
        legacy_package_root,
        evidence_root,
        autostart=False,
        tray_lifecycle=False,
        extra_environment=None,
    )
    try:
        product_runner._wait_for_cdp(port, process)
        readiness = product_runner._wait_for_readiness(readiness_dir, process)
        assert readiness.get("ready") is True, readiness
        manifest = json.loads(
            (workspace_root / ".vibetable" / "workspace.json").read_text(encoding="utf-8")
        )
        node = shutil.which("node.exe") or shutil.which("node")
        assert node is not None, "locked node.exe is missing from PATH"
        completed = subprocess.run(
            [
                node,
                str(LEGACY_OPEN_RUNNER),
                "--cdp-url",
                f"http://127.0.0.1:{port}",
                "--workspace-id",
                WORKSPACE_ID,
                "--display-name",
                str(manifest["displayName"]),
            ],
            cwd=ROOT,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=90,
            check=False,
        )
        (evidence_root / "runner-stdout.log").write_text(completed.stdout, encoding="utf-8")
        (evidence_root / "runner-stderr.log").write_text(completed.stderr, encoding="utf-8")
        assert completed.returncode == 0, completed.stderr
        opened = json.loads(completed.stdout)
        assert opened.get("status") == "passed", opened
        session = opened.get("session")
        assert isinstance(session, dict)
        assert session.get("workspaceId") == WORKSPACE_ID
        assert session.get("state") == "openedWritable"
        coordination = workspace_root / ".vibetable" / "coordination"
        authority = coordination / "desktop-runtime-authority.json"
        coordinator = coordination / "write-coordinator.db"
        assert authority.is_file(), "legacy Host did not publish authority metadata"
        assert coordinator.is_file(), "legacy Host did not create its write coordinator"
        authority_payload = json.loads(authority.read_text(encoding="utf-8"))
        session_epoch = authority_payload.get("lastSessionEpoch")
        assert isinstance(session_epoch, int), authority_payload
        assert session_epoch > 0, authority_payload
        lifecycle = _request_window_close(process, port)
        assert lifecycle["status"] == "passed", lifecycle
        return {
            "status": "passed",
            "evidenceKind": "legacy-packaged-host-workspace-open",
            "hostExecutable": "VibeTable.Next.exe",
            "workspaceId": session["workspaceId"],
            "sessionEpoch": session_epoch,
            "sessionState": session["state"],
            "authorityMetadata": str(authority),
            "coordinator": str(coordinator),
            "lifecycle": lifecycle,
        }
    finally:
        product_runner._stop_process_tree(process)
        streams.close()


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
    process, port, controls, streams = _launch_host(
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
    try:
        product_runner._wait_for_cdp(port, process)
        readiness = product_runner._wait_for_readiness(readiness_dir, process)
        assert readiness.get("ready") is True, readiness
        (controls / OPEN_WORKSPACE_CONTROL_FILE).write_text(WORKSPACE_ID + "\n", encoding="utf-8")
        expected_action = "workspace-open-failed" if expect_open_failure else "workspace-opened"
        opened = _wait_for_state(controls, process, expected_action)
        assert opened.get("hostExecutable", "").casefold() == "vibetable.next.exe", opened
        open_evidence = workspace_open_evidence(
            opened,
            expect_open_failure=expect_open_failure,
        )
        if expect_open_failure and startup_fault_file is not None:
            assert not startup_fault_file.exists(), "startup fault was not consumed once"
        lifecycle = product_runner._request_normal_exit(
            process,
            controls_dir=controls,
            cdp_port=port,
        )
        assert lifecycle["status"] == "passed", lifecycle
        return {
            "status": "passed",
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
    finally:
        product_runner._stop_process_tree(process)
        streams.close()


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
