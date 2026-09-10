from __future__ import annotations

import json
import urllib.error
from dataclasses import dataclass
from io import BytesIO
from pathlib import Path

import pytest

from scripts.qa.windows_process_scope import (
    ProcessScopeSnapshot,
    ScopeTerminationResult,
    ScopeWaitResult,
)
from tests.e2e import packaged_replica_hosts as replica_hosts
from tests.e2e import product_e2e_runner as runner
from tests.e2e.windows_tcp_listener_owner import TcpListenerRow, WindowsTcpListenerOwnerLease


class _Root:
    def __init__(self, exit_code: int = 0) -> None:
        self.pid = 100
        self.exit_code = exit_code

    def poll(self) -> None:
        return None

    def wait(self, timeout: float | None = None) -> int:
        del timeout
        return self.exit_code


class _Scope:
    def __init__(self, *, exit_code: int = 0, close_error: bool = False) -> None:
        self.root = _Root(exit_code)
        self.close_error = close_error
        self.closed = 0
        self.terminated = 0

    def snapshot(self) -> ProcessScopeSnapshot:
        return ProcessScopeSnapshot(())

    def wait_empty(self, *, timeout: float = 5.0) -> ScopeWaitResult:
        del timeout
        return ScopeWaitResult(())

    def terminate_all(self) -> ScopeTerminationResult:
        self.terminated += 1
        return ScopeTerminationResult(True, ())

    def close(self) -> None:
        self.closed += 1
        if self.close_error:
            raise OSError("close denied")


@dataclass
class _Owner:
    pid: int
    name: str = "VibeTable.Next.exe"
    closed: int = 0

    def wait(self, timeout: float) -> bool:
        del timeout
        return True

    def close(self) -> None:
        self.closed += 1


class _Listeners:
    def query_listeners(self, port: int, *, timeout: float) -> tuple[TcpListenerRow, ...]:
        del port
        del timeout
        return ()

    def open_owner(self, pid: int) -> _Owner:
        return _Owner(pid)


def _package(root: Path) -> Path:
    root.mkdir()
    (root / "VibeTable.Next.exe").write_bytes(b"host")
    (root / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "VibeTable.Next.exe"}}), encoding="utf-8"
    )
    return root


def _specs(tmp_path: Path) -> tuple[replica_hosts.ReplicaHostSpec, replica_hosts.ReplicaHostSpec]:
    def make(label: str) -> replica_hosts.ReplicaHostSpec:
        selected = tmp_path / f"selected-{label}"
        selected.mkdir()
        return replica_hosts.ReplicaHostSpec(
            label=label,
            runtime_root=tmp_path / "stable" / label,
            selected_root=selected,
        )

    return make("left"), make("right")


def _install_host_test_doubles(
    monkeypatch: pytest.MonkeyPatch,
    scopes: list[_Scope],
    owners: list[_Owner],
    *,
    fail_cdp_port: int | None = None,
    block_lifecycle_evidence: bool = False,
    exit_code: int = 0,
    close_error: bool = False,
) -> list[Path]:
    controls_at_launch: list[Path] = []

    def launch(command: list[str], **_kwargs: object) -> _Scope:
        readiness = Path(command[command.index("--readiness-dir") + 1])
        environment = _kwargs["env"]
        assert isinstance(environment, dict)
        assert environment["VIBETABLE_E2E_WEBVIEW2_USER_DATA_ROOT"] == str(
            (readiness / "webview2-user-data").resolve()
        )
        controls = Path(command[command.index("--e2e-controls-dir") + 1])
        assert not (controls / runner.NORMAL_CLOSE_CONTROL_FILE).exists()
        controls_at_launch.append(controls)
        if block_lifecycle_evidence and controls.parent.name == "right":
            (controls.parent / "lifecycle.json").mkdir()
        local_marker = readiness / "local-data" / "marker.txt"
        local_marker.parent.mkdir(parents=True, exist_ok=True)
        if local_marker.exists():
            assert local_marker.read_text(encoding="utf-8") == "preserved"
        else:
            local_marker.write_text("preserved", encoding="utf-8")
        (readiness / "vibetable-readiness.json").write_text(
            json.dumps({"ready": True}), encoding="utf-8"
        )
        scope = _Scope(exit_code=exit_code, close_error=close_error)
        scopes.append(scope)
        return scope

    def urlopen(endpoint: str, *, timeout: float) -> BytesIO:
        del timeout
        if fail_cdp_port is not None and endpoint.endswith(f":{fail_cdp_port}/json/version"):
            raise urllib.error.URLError("second CDP failed")
        return BytesIO(b'{"webSocketDebuggerUrl":"ws://host"}')

    def capture(port: int) -> WindowsTcpListenerOwnerLease:
        owner = _Owner(port)
        owners.append(owner)
        return WindowsTcpListenerOwnerLease(
            port=port,
            owner=owner,
            capture_rows=(),
            adapter=_Listeners(),
            monotonic=lambda: 1.0,
        )

    ports = iter((9222, 9333, 9444, 9555))
    monkeypatch.setattr(runner, "_launch_host_process", launch)
    monkeypatch.setattr(runner, "_reserve_port", lambda: next(ports))
    monkeypatch.setattr(runner.urllib.request, "urlopen", urlopen)
    monkeypatch.setattr(replica_hosts.WindowsTcpListenerOwnerLease, "capture", capture)
    if fail_cdp_port is not None:
        monkeypatch.setattr(runner, "CDP_TIMEOUT_SECONDS", 0.001)
    return controls_at_launch


def test_hosts_keep_stable_product_data_across_fresh_evidence_runs(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    specs = _specs(tmp_path)
    scopes: list[_Scope] = []
    owners: list[_Owner] = []
    controls = _install_host_test_doubles(monkeypatch, scopes, owners)
    package = _package(tmp_path / "package")

    with replica_hosts.opened_replica_hosts(package, tmp_path / "run-one", specs) as opened:
        assert set(opened) == {"left", "right"}
        assert opened["left"].local_data_root == specs[0].runtime_root / "host" / "local-data"
        assert opened["left"].webview_user_data_root == (
            opened["left"].host_root / "webview2-user-data"
        )
    first_log = tmp_path / "run-one" / "hosts" / "left" / "host-stdout.log"
    first_log.write_text("first invocation", encoding="utf-8")

    with replica_hosts.opened_replica_hosts(package, tmp_path / "run-two", specs):
        pass

    assert all(owner.closed == 1 for owner in owners)
    assert all(scope.closed >= 1 for scope in scopes)
    assert controls[0] != controls[2]
    assert (controls[0] / runner.NORMAL_CLOSE_CONTROL_FILE).is_file()
    assert first_log.read_text(encoding="utf-8") == "first invocation"
    assert (tmp_path / "run-two" / "hosts" / "left" / "host-stdout.log").is_file()
    assert not (specs[0].runtime_root / "host" / "vibetable-readiness.json").exists()
    assert (tmp_path / "run-one" / "hosts" / "left" / "readiness.json").is_file()


@pytest.mark.parametrize("reason", ["body", "normal-close", "normal-close-evidence"])
def test_hosts_close_every_scope_without_masking_primary_failure(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, reason: str
) -> None:
    specs = _specs(tmp_path)
    scopes: list[_Scope] = []
    owners: list[_Owner] = []
    _install_host_test_doubles(
        monkeypatch,
        scopes,
        owners,
        exit_code=1 if reason.startswith("normal-close") else 0,
        close_error=reason == "body",
        block_lifecycle_evidence=reason == "normal-close-evidence",
    )
    package = _package(tmp_path / "package")

    if reason == "body":
        with (
            pytest.raises(ValueError, match="primary body failure") as raised,
            replica_hosts.opened_replica_hosts(package, tmp_path / "run", specs),
        ):
            raise ValueError("primary body failure")
        assert any("close denied" in note for note in raised.value.__notes__)
    else:
        with (
            pytest.raises(replica_hosts.PackagedReplicaHostLifecycleError) as raised,
            replica_hosts.opened_replica_hosts(package, tmp_path / "run", specs),
        ):
            pass
        if reason == "normal-close-evidence":
            assert any(
                "writing packaged replica evidence also failed" in note
                for note in raised.value.__notes__
            )

    assert len(scopes) == 2
    assert all(scope.closed >= 1 for scope in scopes)
    assert all(owner.closed >= 1 for owner in owners)


def test_second_host_cdp_failure_aborts_first_host_and_preserves_evidence(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    specs = _specs(tmp_path)
    scopes: list[_Scope] = []
    owners: list[_Owner] = []
    _install_host_test_doubles(
        monkeypatch,
        scopes,
        owners,
        fail_cdp_port=9333,
        block_lifecycle_evidence=True,
    )

    with (
        pytest.raises(TimeoutError, match="CDP endpoint") as raised,
        replica_hosts.opened_replica_hosts(_package(tmp_path / "package"), tmp_path / "run", specs),
    ):
        pass

    assert len(scopes) == 2
    assert all(scope.closed >= 1 for scope in scopes)
    assert owners[0].closed >= 1
    assert any(
        "writing packaged replica evidence also failed" in note for note in raised.value.__notes__
    )
    assert (tmp_path / "run" / "hosts" / "right" / "launch.json").is_file()


def test_hosts_reject_overlapping_isolation_inputs(tmp_path: Path) -> None:
    selected = tmp_path / "selected"
    selected.mkdir()
    selected_child = selected / "child"
    selected_child.mkdir()
    other_selected = tmp_path / "other-selected"
    other_selected.mkdir()
    left = replica_hosts.ReplicaHostSpec("left", tmp_path / "stable-left", selected)
    right = replica_hosts.ReplicaHostSpec("right", tmp_path / "stable-right", other_selected)
    cases = (
        (
            tmp_path / "label-evidence",
            (replica_hosts.ReplicaHostSpec("../left", left.runtime_root, selected), right),
            "simple-slug",
        ),
        (
            tmp_path / "runtime-evidence",
            (
                left,
                replica_hosts.ReplicaHostSpec("right", left.runtime_root / "child", other_selected),
            ),
            "paths overlap",
        ),
        (
            tmp_path / "selected-evidence",
            (left, replica_hosts.ReplicaHostSpec("right", right.runtime_root, selected_child)),
            "paths overlap",
        ),
        (left.runtime_root, (left, right), "paths overlap"),
    )
    package = _package(tmp_path / "package")
    for evidence, specs, message in cases:
        with (
            pytest.raises(ValueError, match=message),
            replica_hosts.opened_replica_hosts(package, evidence, specs),
        ):
            pass
