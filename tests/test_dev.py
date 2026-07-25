from __future__ import annotations

import subprocess
from pathlib import Path

from scripts import dev


def test_build_only_runs_product_builds_without_launching(monkeypatch) -> None:
    calls: list[tuple[bool, bool, bool]] = []
    monkeypatch.setattr(
        dev,
        "build",
        lambda *, web, sidecar, host: calls.append((web, sidecar, host)),
    )
    monkeypatch.setattr(dev, "launch", lambda _path: 99)

    result = dev.main(["--build-only"])

    assert result == 0
    assert calls == [(True, True, True)]


def test_build_uses_restored_dependencies_and_never_installs_runtime(
    monkeypatch,
    tmp_path: Path,
) -> None:
    calls: list[tuple[list[str], Path]] = []
    monkeypatch.setattr(dev, "BUILD_DIR", tmp_path / "build")
    monkeypatch.setattr(dev, "SIDECAR_BINARY", tmp_path / "build" / "vibetable-pb.exe")
    monkeypatch.setattr(dev, "_resolve", lambda name: name)
    monkeypatch.setattr(
        dev,
        "_run",
        lambda command, *, cwd: calls.append((command, cwd)),
    )

    dev.build()

    commands = [command for command, _ in calls]
    assert commands[0][:3] == ["go", "build", "-trimpath"]
    assert commands[1] == ["npm", "run", "build"]
    assert commands[2][:2] == ["dotnet", "build"]
    assert not any("install" in token or token == "ci" for command in commands for token in command)
    assert not any("dire" "ctus" in token.lower() for command in commands for token in command)


def test_launch_opens_desktop_host_which_owns_runtime(
    monkeypatch,
    tmp_path: Path,
) -> None:
    host_binary = tmp_path / "VibeTable.Desktop.exe"
    host_binary.write_bytes(b"desktop-host")
    monkeypatch.setattr(dev, "HOST_BINARY", host_binary, raising=False)
    monkeypatch.setattr(dev, "BUILD_DIR", tmp_path)
    monkeypatch.setattr(dev, "SIDECAR_BINARY", tmp_path / "vibetable-pb.exe")
    monkeypatch.setattr(dev, "_resolve", lambda name: name)
    monkeypatch.setenv("VIBETABLE_STATE_DIR", "stale-state")
    monkeypatch.setenv(
        "VIBETABLE_E2E_WEBVIEW2_USER_DATA_ROOT",
        "stale-webview",
    )
    monkeypatch.setenv(
        "VIBETABLE_E2E_MUTATION_BARRIER_DIR",
        "stale-mutation-barrier",
    )
    monkeypatch.setenv(
        "VIBETABLE_WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
        "--remote-debugging-port=9222",
    )
    monkeypatch.setenv("VIBETABLE_SIDECAR_SESSION_SECRET", "stale-secret")
    monkeypatch.setenv("VIBETABLE_SIDECAR_URL", "http://127.0.0.1:1")
    dev.PROCESSES.clear()
    launched: list[dict[str, object]] = []

    class FakeProcess:
        def __init__(self, command, **kwargs):
            self.command = command
            self.returncode = 0
            launched.append({"command": command, **kwargs})

        def poll(self):
            return 0

        def wait(self, timeout=None):
            return 0

        def terminate(self):
            return None

        def kill(self):
            return None

    monkeypatch.setattr(dev.subprocess, "Popen", FakeProcess)

    assert dev.launch(tmp_path / "runtime-data") == 0

    runtime_data = (tmp_path / "runtime-data").resolve()
    assert [item["command"] for item in launched] == [[
        str(host_binary),
        "--dev-data-root",
        str(runtime_data),
    ]]
    environment = launched[0]["env"]
    assert isinstance(environment, dict)
    assert environment["VIBETABLE_PYTHON"] == dev.sys.executable
    for variable in dev.UNSAFE_INHERITED_RUNTIME_VARIABLES:
        assert variable not in environment


def test_cleanup_terminates_then_kills_stuck_children(monkeypatch) -> None:
    actions: list[str] = []

    class FakeProcess:
        def poll(self):
            return None

        def terminate(self):
            actions.append("terminate")

        def wait(self, timeout=None):
            actions.append("wait")
            raise subprocess.TimeoutExpired("process", timeout)

        def kill(self):
            actions.append("kill")

    dev.PROCESSES[:] = [FakeProcess()]
    dev._cleanup()

    assert actions == ["terminate", "wait", "kill"]
    dev.PROCESSES.clear()
