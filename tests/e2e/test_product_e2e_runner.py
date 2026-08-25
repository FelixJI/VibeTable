from __future__ import annotations

import ast
import hashlib
import json
import sqlite3
import subprocess
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

from scripts.node_toolchain import ensure_node
from tests.e2e import product_e2e_runner as runner
from tests.e2e.windows_process_scope import ProcessScopeLaunchError
from tests.e2e.windows_tcp_listener_owner import (
    OwnerLeaseCleanupReport,
    PortReleaseReport,
    TcpListenerRow,
    WindowsTcpListenerOwnerLease,
    _capture_with_adapter,
)


class _FakeRoot:
    pid = 42

    def __init__(self, exit_code: int | None = None) -> None:
        self.exit_code = exit_code

    def poll(self) -> int | None:
        return self.exit_code

    def wait(self, timeout: float | None = None) -> int:
        if self.exit_code is None:
            raise subprocess.TimeoutExpired(["fake-host"], 0 if timeout is None else timeout)
        return self.exit_code


class _SuccessfulRoot(_FakeRoot):
    def wait(self, timeout: float | None = None) -> int:
        assert timeout is not None
        return 0


class _FakeScope:
    def __init__(self, *, exit_code: int | None = None, members: tuple[int, ...] = ()) -> None:
        self.root = _FakeRoot(exit_code)
        self.members = members
        self.terminate_calls = 0
        self.close_calls = 0

    def snapshot(self) -> Any:
        return SimpleNamespace(members=tuple(SimpleNamespace(pid=pid) for pid in self.members))

    def terminate_all(self) -> Any:
        self.terminate_calls += 1
        self.members = ()
        return runner.ScopeTerminationResult(True, remaining_pids=())

    def wait_empty(self, *, timeout: float = 5.0) -> Any:
        del timeout
        return runner.ScopeWaitResult(self.members)

    def close(self) -> None:
        self.close_calls += 1


class _FakePortOwnerLease:
    def __init__(
        self,
        *,
        released: bool = True,
        close_error: BaseException | None = None,
    ) -> None:
        self.released = released
        self.close_error = close_error
        self.closed = False
        self.close_calls = 0
        self.cleanup_report: OwnerLeaseCleanupReport | None = None
        self.observe_timeouts: list[float] = []

    def observe_release(self, *, timeout: float) -> PortReleaseReport:
        assert 0 <= timeout <= runner.LIFECYCLE_EXIT_TIMEOUT_SECONDS
        self.observe_timeouts.append(timeout)
        return PortReleaseReport(
            owner_pid=42,
            owner_name="msedgewebview2.exe",
            capture_rows=(),
            release_rows=(),
            decision="captured-owner-exited" if self.released else "captured-owner-still-listening",
            released=self.released,
            owner_exited=self.released,
        )

    def close(self) -> OwnerLeaseCleanupReport:
        if self.cleanup_report is not None:
            return self.cleanup_report
        self.closed = True
        self.close_calls += 1
        if self.close_error is not None:
            self.cleanup_report = OwnerLeaseCleanupReport(
                stable_handle_closed=False,
                errors=(
                    "unable to close captured CDP owner handle "
                    f"({type(self.close_error).__name__})",
                ),
            )
        else:
            self.cleanup_report = OwnerLeaseCleanupReport(stable_handle_closed=True)
        return self.cleanup_report

    def __enter__(self) -> _FakePortOwnerLease:
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()


class _CloseFailOwnerHandle:
    pid = 42
    name = "C:\\private\\msedgewebview2.exe"

    def __init__(self) -> None:
        self.close_calls = 0

    def wait(self, timeout: float) -> bool:
        del self, timeout
        return False

    def close(self) -> None:
        self.close_calls += 1
        raise OSError("C:\\private\\handle close denied")


class _CloseFailOwnerAdapter:
    def __init__(self, owner: _CloseFailOwnerHandle) -> None:
        row = TcpListenerRow("TCP", "127.0.0.1:9222", "0.0.0.0:0", "侦听", owner.pid)
        self.samples = iter(((row,), (row,), ()))
        self.owner = owner

    def query_listeners(self, port: int, *, timeout: float) -> tuple[TcpListenerRow, ...]:
        assert port == 9222
        assert timeout > 0
        return next(self.samples)

    def open_owner(self, pid: int) -> _CloseFailOwnerHandle:
        assert pid == self.owner.pid
        return self.owner


def _real_close_failing_owner_lease() -> tuple[
    WindowsTcpListenerOwnerLease,
    _CloseFailOwnerHandle,
]:
    owner = _CloseFailOwnerHandle()
    return _capture_with_adapter(9222, _CloseFailOwnerAdapter(owner)), owner


def _stub_cdp_owner_capture(
    monkeypatch,
    *,
    close_error: BaseException | None = None,
) -> _FakePortOwnerLease:
    lease = _FakePortOwnerLease(close_error=close_error)
    monkeypatch.setattr(
        runner.WindowsTcpListenerOwnerLease,
        "capture",
        lambda _port: lease,
    )
    return lease


def test_manifest_accepts_capability_tagged_scenarios_without_a_fixed_count(
    tmp_path: Path,
) -> None:
    manifest = tmp_path / "scenarios.json"
    manifest.write_text(
        json.dumps(
            [
                {
                    "id": "01-query-window",
                    "title": "窗口查询",
                    "requirement": "只加载可见窗口。",
                    "capabilities": ["view-query.window", "release.smoke"],
                },
                {
                    "id": "02-search-rebuild",
                    "title": "搜索重建",
                    "requirement": "索引重建时工作区仍可用。",
                    "capabilities": ["workspace-search.rebuild"],
                },
            ]
        ),
        encoding="utf-8",
    )

    scenarios = runner.load_scenarios(manifest)

    assert [item.id for item in scenarios] == ["01-query-window", "02-search-rebuild"]
    assert scenarios[0].capabilities == ("view-query.window", "release.smoke")


def test_manifest_accepts_three_digit_scenario_ids(tmp_path: Path) -> None:
    manifest = tmp_path / "scenarios.json"
    manifest.write_text(
        json.dumps(
            [
                {
                    "id": "100-long-lived-suite",
                    "title": "三位数场景",
                    "requirement": "场景序号可在长期演进后超过两位数。",
                    "capabilities": ["release.smoke"],
                }
            ]
        ),
        encoding="utf-8",
    )

    assert runner.load_scenarios(manifest)[0].id == "100-long-lived-suite"


@pytest.mark.parametrize("scenario_id", ["scenario", "01_Invalid", "01-"])
def test_manifest_rejects_ids_outside_the_evidence_contract(
    tmp_path: Path,
    scenario_id: str,
) -> None:
    manifest = tmp_path / "scenarios.json"
    manifest.write_text(
        json.dumps(
            [
                {
                    "id": scenario_id,
                    "title": "非法 ID",
                    "requirement": "manifest 与证据边界必须共享同一 ID 契约。",
                    "capabilities": ["release.smoke"],
                }
            ]
        ),
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="scenario id"):
        runner.load_scenarios(manifest)


def test_manifest_has_unique_capability_tagged_product_scenarios() -> None:
    scenarios = runner.load_scenarios()

    assert scenarios
    assert len({item.id for item in scenarios}) == len(scenarios)
    assert all(item.title and item.requirement and item.capabilities for item in scenarios)
    by_id = {item.id: item.requirement for item in scenarios}
    assert "规范化深比较" in by_id["04-json-round-trip"]
    assert "SHA-256" in by_id["07-attachment-history"]
    assert "幂等键" in by_id["09-atomic-import-scale"]
    assert "BFF" in by_id["10-sse-reconnect"]
    assert "session epoch" in by_id["10-sse-reconnect"]
    assert "外部 ledger 链" in by_id["12-backup-consistency"]
    assert "搜索 generation" in by_id["12-backup-consistency"]
    assert "repository.verify" in by_id["13-protection-policy"]
    assert "retention.apply" in by_id["13-protection-policy"]
    assert "真实 TXT 历史版本" in by_id["14-document-diff"]
    assert "工作区切换" in by_id["15-workspace-snapshot-package"]
    assert "损坏快照包" in by_id["15-workspace-snapshot-package"]
    assert "Dashboard" in by_id["16-dashboard-lifecycle"]
    assert "RecordDocumentLink" in by_id["18-workspace-search"]
    assert "Markdown/JSON" in by_id["18-workspace-search"]
    assert "真实 Tables UI" in by_id["20-kanban-lane-drag"]
    assert "稳定 optionId" in by_id["20-kanban-lane-drag"]
    assert "重开" in by_id["20-kanban-lane-drag"]


def test_node_runner_inventory_matches_the_product_scenario_manifest() -> None:
    completed = subprocess.run(
        [
            str(ensure_node(runner.ROOT)),
            str(runner.NODE_RUNNER),
            "--list-scenarios",
        ],
        cwd=runner.ROOT,
        check=True,
        capture_output=True,
        text=True,
    )

    assert json.loads(completed.stdout) == [scenario.id for scenario in runner.load_scenarios()]


def test_capability_selection_drives_the_release_smoke_subset() -> None:
    selected = runner.select_scenarios(
        runner.load_scenarios(),
        capabilities=("release.smoke",),
    )

    assert [item.id for item in selected] == [
        "01-offline-first-start",
        "02-all-field-schema",
        "08-stale-conflict",
        "16-dashboard-lifecycle",
    ]


def test_new_capability_scenarios_are_driven_through_product_ui() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    workspace = source[
        source.index("async function scenario15") : source.index("async function scenario16")
    ]
    dashboard = source[source.index("async function scenario16") : source.index("const scenarios")]
    interface = source[
        source.index("async function scenario17") : source.index("async function scenario18")
    ]
    search = source[source.index("async function scenario18") : source.index("const scenarios")]
    kanban = source[source.index("async function scenario20") : source.index("const scenarios")]

    assert 'getByTestId("workspace-create")' in workspace
    assert 'getByTestId("snapshot-create")' in workspace
    assert 'getByTestId("snapshot-open-as-new")' in workspace
    assert 'getByTestId("snapshot-export-open")' in workspace
    assert 'getByTestId("snapshot-import")' in workspace
    assert "requestWithStaleWorkspaceScope" in workspace
    assert 'getByTestId("dashboard-create")' in dashboard
    assert 'getByTestId("dashboard-save")' in dashboard
    assert 'getByTestId("dashboard-refresh")' in dashboard
    assert 'getByTestId("nav-interfaces")' in interface
    assert 'getByTestId("interface-add-text")' in interface
    assert 'getByTestId("interface-save")' in interface
    assert 'getByTestId("interface-run")' in interface
    assert 'getByTestId("nav-search")' in search
    assert 'getByTestId("workspace-search-submit")' in source
    assert 'getByTestId("view-create")' in kanban
    assert 'getByTestId("view-kind-kanban")' in kanban
    assert 'getByTestId("field-display-name")' in kanban
    assert '"field-logical-type"' in kanban
    assert 'getByTestId("field-plan-button")' in kanban
    assert 'getByTestId("field-apply-button")' in kanban
    assert '"view-kanban-group-field"' in kanban
    assert '"view-kanban-title-field"' in kanban
    assert ".dragTo(" in kanban
    assert '"query.page"' in kanban
    assert '"table.updateCellRequested"' not in kanban
    assert "createV2Field(page, tableId" not in kanban


def test_content_search_and_restore_scenarios_cover_m6_m7_m8_product_boundaries() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    restore = source[
        source.index("async function scenario12") : source.index("async function scenario13")
    ]
    search = source[source.index("async function scenario18") : source.index("const scenarios")]

    assert 'getByTestId("content-profile-save")' in search
    assert 'getByTestId("content-record-save")' in search
    assert 'getByTestId("content-link-create")' in search
    assert 'getByTestId("content-link-repair")' in search
    assert 'getByTestId("document-unlink")' in search
    assert 'getByTestId("document-unlink-confirm")' in search
    assert '"fileHistory.queryDocuments"' in search
    assert '{ field: "extension", operator: "eq", value: "md" }' in search
    assert '{ field: "extension", operator: "eq", value: ".md" }' not in search
    assert (
        'requestSidecarKill(runtime, "verify ContentProfile and repaired link survive restart")'
        in search
    )
    assert "submitWorkspaceSearch(page, { keyboard: true })" in search
    assert 'kinds.includes("attachment")' in search
    assert 'andKinds.every((kind) => ["record", "attachment"].includes(kind))' in search
    assert 'andKinds.includes("record")' in search
    assert 'andKinds.includes("attachment")' in search
    assert '!andKinds.includes("file")' in search
    assert 'andKinds.every((kind) => kind === "record")' not in search
    assert 'const staleBefore = await rawBridgeRequest(page, "query.page"' in search
    assert "expectedDigest: staleBeforeRow.__vibetableDigest" in search
    assert "contentSaved.payload?.rows?.[0]?.__vibetableDigest" not in search
    assert 'staleMutation.type !== "mutation.apply"' in search
    assert 'staleMutation.payload?.status !== "applied"' in search
    assert "staleMutation.payload?.error?.code" in search
    assert "staleAfterRevision > staleBeforeRevision" in search
    assert "staleMessageWasVisible" in search
    assert 'page.getByTestId("workspace-search-input").locator("input")' in search
    assert "await activeSearchInput.isVisible()" in search
    assert "activeSearchInput.evaluate((element) => element === document.activeElement)" in search
    assert "staleRecord.evaluate((element) => element === document.activeElement)" not in search
    assert '"workspaceSearch.status"' in restore
    assert "restoredSearchStatus.result?.generation" in restore
    assert "snapshotStorageProof.auditLedger.anchorHash" in restore


def test_content_search_runner_provisions_real_markdown_json_and_attachment_files() -> None:
    source = Path(runner.__file__).read_text(encoding="utf-8")

    assert '"18-workspace-search": "e2e-search-attachment"' in source
    assert 'content_markdown_source = controls_dir / "content-reference-a.md"' in source
    assert 'content_json_source = controls_dir / "content-reference-b.json"' in source
    assert "Marigold appears in visible Markdown text" in source
    assert "Cobalt appears in JSON content" in source


def test_lifecycle_harness_requires_a_normal_close_and_empty_process_evidence() -> None:
    report = runner._lifecycle_exit_report(
        normal_exit_requested=True,
        host_exit_code=0,
        members_after_exit=[],
        ports_released=True,
    )

    assert report == {
        "normalExitRequested": True,
        "hostExitCode": 0,
        "membersAfterExit": [],
        "descendantsAfterExit": [],
        "portsReleased": True,
        "errors": [],
        "status": "passed",
    }


def test_host_launch_uses_atomic_job_scope_and_preserves_process_inputs(
    monkeypatch,
    tmp_path: Path,
) -> None:
    launched: list[Any] = []
    fake_scope = object()

    def fake_launch(spec: Any) -> object:
        launched.append(spec)
        return fake_scope

    monkeypatch.setattr(runner.WindowsProcessScope, "launch", fake_launch)
    environment = {"VIBETABLE_TEST": "1"}
    with (
        (tmp_path / "stdout.log").open("wb") as stdout,
        (tmp_path / "stderr.log").open("wb") as stderr,
    ):
        result = runner._launch_host_process(
            ["host.exe", "--quoted", "value with spaces"],
            cwd=tmp_path,
            env=environment,
            stdout=stdout,
            stderr=stderr,
        )

    assert result is fake_scope
    assert len(launched) == 1
    spec = launched[0]
    assert spec.command == ("host.exe", "--quoted", "value with spaces")
    assert spec.cwd == tmp_path
    assert spec.env is environment
    assert spec.stdout is stdout
    assert spec.stderr is stderr


def test_lifecycle_harness_fails_closed_when_a_job_member_or_port_remains() -> None:
    report = runner._lifecycle_exit_report(
        normal_exit_requested=True,
        host_exit_code=0,
        members_after_exit=[{"pid": 7}],
        ports_released=False,
    )

    assert report["status"] == "failed"
    assert report["membersAfterExit"] == [{"pid": 7}]
    assert report["portsReleased"] is False


def test_fault_controller_processes_distinct_sidecar_and_packaged_backend_requests(
    monkeypatch,
    tmp_path: Path,
) -> None:
    class FakeScope:
        root = _FakeRoot()

        def __init__(self) -> None:
            self.targets: list[str] = []

        @staticmethod
        def snapshot() -> Any:
            return SimpleNamespace(members=())

        def terminate_unique(self, executable_name: str) -> Any:
            self.targets.append(executable_name)
            pid = 7 if executable_name == "vibetable-pb.exe" else 8
            return runner.TargetTerminationResult(
                "terminated", terminated_pid=pid, matched_pids=(pid,)
            )

    class FakeNodeProcess:
        returncode = 0

        def __init__(self) -> None:
            self.poll_count = 0

        def poll(self) -> int | None:
            self.poll_count += 1
            action = {
                1: ("sidecar-1", "kill-sidecar"),
                2: ("backend-1", "kill-backend"),
            }.get(self.poll_count)
            if action is not None:
                request_id, fault_action = action
                (tmp_path / "fault-request.json").write_text(
                    json.dumps({"requestId": request_id, "action": fault_action}),
                    encoding="utf-8",
                )
                return None
            return 0

        @staticmethod
        def communicate(timeout: int) -> tuple[str, str]:
            assert timeout == 10
            return "", ""

    scope = FakeScope()
    monkeypatch.setattr(runner.subprocess, "Popen", lambda *args, **kwargs: FakeNodeProcess())
    monkeypatch.setattr(runner.time, "sleep", lambda _seconds: None)

    exit_code, stdout, stderr = runner._run_node_runner(
        ["node", "scenario.mjs"],
        scenario_dir=tmp_path,
        local_data=tmp_path / "local-data",
        host_scope=scope,
    )

    result = json.loads((tmp_path / "fault-result.json").read_text(encoding="utf-8"))
    assert (exit_code, stdout, stderr) == (0, "", "")
    assert result["requestId"] == "backend-1"
    assert result["action"] == "kill-backend"
    assert result["processName"] == "vibetable-backend.exe"
    assert scope.targets == ["vibetable-pb.exe", "vibetable-backend.exe"]


@pytest.mark.parametrize(
    ("scope_result", "expected"),
    [
        (
            runner.TargetTerminationResult("not_found"),
            {
                "status": "failed",
                "code": "BACKEND_PROCESS_NOT_FOUND",
                "matches": [],
            },
        ),
        (
            runner.TargetTerminationResult(
                "terminated",
                terminated_pid=8,
                matched_pids=(8,),
            ),
            {
                "status": "completed",
                "action": "kill-backend",
                "pid": 8,
                "processName": "vibetable-backend.exe",
            },
        ),
        (
            runner.TargetTerminationResult(
                "ambiguous",
                matched_pids=(8, 9),
            ),
            {
                "status": "failed",
                "code": "BACKEND_PROCESS_NOT_UNIQUE",
                "matches": [8, 9],
            },
        ),
    ],
)
def test_fault_controller_uses_verified_job_target_cardinality(
    scope_result: Any,
    expected: dict[str, Any],
) -> None:
    class FakeScope:
        @staticmethod
        def terminate_unique(executable_name: str) -> Any:
            assert executable_name == "vibetable-backend.exe"
            return scope_result

    response = runner._handle_fault_request(
        {"requestId": "backend", "action": "kill-backend"},
        FakeScope(),
    )

    assert response == expected


def test_normal_exit_reports_residual_job_members_and_terminates_them(
    monkeypatch,
    tmp_path: Path,
) -> None:
    class FakeScope:
        root = _FakeRoot(0)

        def __init__(self) -> None:
            self.terminated = 0

        @staticmethod
        def snapshot() -> Any:
            return SimpleNamespace(members=(SimpleNamespace(pid=7), SimpleNamespace(pid=8)))

        def terminate_all(self) -> Any:
            self.terminated += 1
            return runner.ScopeTerminationResult(True, remaining_pids=())

        @staticmethod
        def wait_empty(*, timeout: float = 5.0) -> Any:
            del timeout
            return runner.ScopeWaitResult((7, 8))

    scope = FakeScope()
    monkeypatch.setattr(runner.time, "sleep", lambda _seconds: None)

    report = runner._request_normal_exit(
        scope,
        controls_dir=tmp_path,
        cdp_owner=_FakePortOwnerLease(),
    )

    assert report["status"] == "failed"
    assert [member["pid"] for member in report["membersAfterExit"]] == [7, 8]
    assert report["cleanup"]["status"] == "passed"
    assert scope.terminated == 1


def test_normal_exit_passes_when_the_job_is_empty(
    monkeypatch,
    tmp_path: Path,
) -> None:
    class FakeScope:
        root = _FakeRoot(0)

        @staticmethod
        def snapshot() -> Any:
            return SimpleNamespace(members=())

        @staticmethod
        def terminate_all() -> Any:
            pytest.fail("an empty normal-exit scope must not be terminated")

        @staticmethod
        def wait_empty(*, timeout: float = 5.0) -> Any:
            del timeout
            return runner.ScopeWaitResult(())

    monkeypatch.setattr(runner.time, "sleep", lambda _seconds: None)

    report = runner._request_normal_exit(
        FakeScope(),
        controls_dir=tmp_path,
        cdp_owner=_FakePortOwnerLease(),
    )

    assert report["status"] == "passed"
    assert report["membersAfterExit"] == []


def test_normal_exit_timeout_keeps_root_in_members_but_not_legacy_descendants(
    monkeypatch,
    tmp_path: Path,
) -> None:
    scope = _FakeScope(members=(42, 7))
    report = runner._request_normal_exit(
        scope,
        controls_dir=tmp_path,
        cdp_owner=_FakePortOwnerLease(),
    )

    assert [member["pid"] for member in report["membersAfterExit"]] == [42, 7]
    assert report["descendantsAfterExit"] == [{"pid": 7, "name": "unknown"}]
    assert report["hostExitCode"] is None


def test_lifecycle_waits_share_one_absolute_budget_without_polling(monkeypatch) -> None:
    class Clock:
        value = 100.0

        def monotonic(self) -> float:
            return self.value

        def advance(self, seconds: float) -> None:
            self.value += seconds

    clock = Clock()
    waits: list[tuple[str, float]] = []

    class BudgetRoot(_FakeRoot):
        def wait(self, timeout: float | None = None) -> int:
            assert timeout is not None
            waits.append(("host", timeout))
            clock.advance(timeout)
            raise subprocess.TimeoutExpired(["fake-host"], timeout)

    class BudgetScope(_FakeScope):
        def __init__(self) -> None:
            super().__init__(members=())
            self.root = BudgetRoot()

        def wait_empty(self, *, timeout: float = 5.0) -> Any:
            waits.append(("job", timeout))
            clock.advance(timeout)
            return runner.ScopeWaitResult(())

    class BudgetOwner(_FakePortOwnerLease):
        def observe_release(self, *, timeout: float) -> PortReleaseReport:
            waits.append(("owner", timeout))
            assert timeout == 0.0
            return PortReleaseReport(
                owner_pid=42,
                owner_name="msedgewebview2.exe",
                capture_rows=(),
                release_rows=(),
                decision="release-observation-budget-exhausted",
                released=False,
                owner_exited=None,
                errors=("CDP listener release observation exceeded its deadline",),
            )

    monkeypatch.setattr(runner.time, "monotonic", clock.monotonic)
    monkeypatch.setattr(runner.time, "sleep", lambda _seconds: pytest.fail("no polling"))

    report = runner._observe_scope_exit(BudgetScope(), cdp_owner=BudgetOwner())

    assert waits == [("host", 30.0), ("job", 5.0), ("owner", 0.0)]
    assert sum(timeout for _name, timeout in waits) == runner.LIFECYCLE_EXIT_TIMEOUT_SECONDS
    assert report["portsReleased"] is False
    assert report["portRelease"]["decision"] == "release-observation-budget-exhausted"
    assert report["status"] == "failed"


@pytest.mark.parametrize(
    "failure",
    [TimeoutError("node timed out"), ValueError("bad state"), AssertionError("asserted")],
)
def test_run_scenario_reports_lifecycle_when_post_launch_logic_raises(
    monkeypatch,
    tmp_path: Path,
    failure: Exception,
) -> None:
    scenario = runner.Scenario(
        id="02-schema-edit",
        title="schema",
        requirement="fail closed",
    )
    package = tmp_path / "package"
    package.mkdir()
    (package / "VibeTable.Next.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "VibeTable.Next.exe"}}),
        encoding="utf-8",
    )

    scope = _FakeScope(members=(42, 7))
    monkeypatch.setattr(runner, "_launch_host_process", lambda *_args, **_kwargs: scope)
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    cdp_owner = _stub_cdp_owner_capture(monkeypatch)
    monkeypatch.setattr(
        runner,
        "_run_node_runner",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(failure),
    )

    result = runner.run_scenario(
        scenario,
        package_root=package,
        evidence_root=tmp_path / "evidence",
        node="node",
    )

    assert result["status"] == "failed"
    assert result["error"]["code"] == "E2E_INFRASTRUCTURE_FAILED"
    assert result["lifecycle"]["status"] == "failed"
    assert [member["pid"] for member in result["lifecycle"]["membersAfterExit"]] == [42, 7]
    assert result["lifecycle"]["descendantsAfterExit"] == [{"pid": 7, "name": "unknown"}]
    assert scope.terminate_calls == 1
    assert scope.close_calls == 1
    assert cdp_owner.closed is True


def test_run_scenario_success_closes_the_cdp_owner_lease(
    monkeypatch,
    tmp_path: Path,
) -> None:
    scenario = runner.Scenario(id="02-schema-edit", title="schema", requirement="success cleanup")
    package = tmp_path / "package"
    package.mkdir()
    (package / "host.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "host.exe"}}),
        encoding="utf-8",
    )
    scope = _FakeScope()
    scope.root = _SuccessfulRoot()
    monkeypatch.setattr(runner, "_launch_host_process", lambda *_args, **_kwargs: scope)
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    cdp_owner = _stub_cdp_owner_capture(monkeypatch)
    monkeypatch.setattr(runner, "_wait_for_readiness", lambda *_args: {"ready": True})

    def successful_node(
        _command: list[str],
        *,
        scenario_dir: Path,
        local_data: Path,
        host_scope: Any,
        process_network: dict[str, Any] | None = None,
    ) -> tuple[int, str, str]:
        del local_data, host_scope, process_network
        (scenario_dir / f"{scenario.id}-result.json").write_text(
            json.dumps({"scenario": scenario.id, "status": "passed"}),
            encoding="utf-8",
        )
        return 0, "", ""

    monkeypatch.setattr(runner, "_run_node_runner", successful_node)

    result = runner.run_scenario(
        scenario,
        package_root=package,
        evidence_root=tmp_path / "evidence",
        node="node",
    )

    assert result["status"] == "passed"
    assert result["lifecycle"]["status"] == "passed"
    assert result["lifecycle"]["ownerLeaseCleanup"]["stableHandleClosed"] is True
    assert cdp_owner.observe_timeouts
    assert cdp_owner.closed is True
    assert scope.close_calls == 1


def test_run_scenario_reports_owner_handle_close_failure_without_interrupting_result(
    monkeypatch,
    tmp_path: Path,
) -> None:
    scenarios = (
        runner.Scenario(id="02-schema-edit", title="schema", requirement="close report"),
        runner.Scenario(id="03-view-crud", title="view", requirement="next scenario"),
    )
    package = tmp_path / "package"
    package.mkdir()
    (package / "host.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "host.exe"}}),
        encoding="utf-8",
    )
    scopes: list[_FakeScope] = []

    def launch(*_args: object, **_kwargs: object) -> _FakeScope:
        scope = _FakeScope()
        scope.root = _SuccessfulRoot()
        scopes.append(scope)
        return scope

    monkeypatch.setattr(runner, "_launch_host_process", launch)
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    owners: list[_CloseFailOwnerHandle] = []

    def capture(_port: int) -> WindowsTcpListenerOwnerLease:
        cdp_owner, owner = _real_close_failing_owner_lease()
        owners.append(owner)
        return cdp_owner

    monkeypatch.setattr(
        runner.WindowsTcpListenerOwnerLease,
        "capture",
        capture,
    )
    monkeypatch.setattr(runner, "_wait_for_readiness", lambda *_args: {"ready": True})

    def successful_node(
        command: list[str],
        *,
        scenario_dir: Path,
        **_kwargs: object,
    ) -> tuple[int, str, str]:
        scenario_id = command[command.index("--scenario") + 1]
        (scenario_dir / f"{scenario_id}-result.json").write_text(
            json.dumps({"scenario": scenario_id, "status": "passed"}),
            encoding="utf-8",
        )
        return 0, "", ""

    monkeypatch.setattr(runner, "_run_node_runner", successful_node)

    results = [
        runner.run_scenario(
            scenario,
            package_root=package,
            evidence_root=tmp_path / f"evidence-{index}",
            node="node",
        )
        for index, scenario in enumerate(scenarios)
    ]

    assert len(results) == 2
    for result in results:
        assert result["status"] == "failed"
        assert result["error"]["code"] == "HOST_LIFECYCLE_FAILED"
        assert result["lifecycle"]["ownerLeaseCleanup"] == {
            "stableHandleClosed": False,
            "errors": ["unable to close captured CDP owner handle (OSError)"],
            "status": "failed",
        }
        assert "private" not in result["lifecycleError"]
    assert [owner.close_calls for owner in owners] == [1, 1]
    assert len(scopes) == 2


def test_run_scenario_closes_real_owner_lease_without_replacing_base_exception(
    monkeypatch,
    tmp_path: Path,
) -> None:
    scenario = runner.Scenario(id="02-schema-edit", title="schema", requirement="base error")
    package = tmp_path / "package"
    package.mkdir()
    (package / "host.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "host.exe"}}),
        encoding="utf-8",
    )
    scope = _FakeScope()
    monkeypatch.setattr(runner, "_launch_host_process", lambda *_args, **_kwargs: scope)
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    cdp_owner, owner = _real_close_failing_owner_lease()
    monkeypatch.setattr(
        runner.WindowsTcpListenerOwnerLease,
        "capture",
        lambda _port: cdp_owner,
    )
    monkeypatch.setattr(
        runner,
        "_run_node_runner",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(KeyboardInterrupt("primary interrupt")),
    )

    with pytest.raises(KeyboardInterrupt, match="primary interrupt") as raised:
        runner.run_scenario(
            scenario,
            package_root=package,
            evidence_root=tmp_path / "evidence",
            node="node",
        )

    assert owner.close_calls == 1
    assert raised.value.__notes__ == ["unable to close captured CDP owner handle (OSError)"]
    assert "private" not in str(raised.value.__notes__)


def test_missing_owner_cleanup_report_fails_closed() -> None:
    class MissingReportLease:
        def __init__(self) -> None:
            self.closed = False
            self.close_calls = 0

        def close(self) -> None:
            self.closed = True
            self.close_calls += 1
            return None

    lease: Any = MissingReportLease()
    cleanup = runner._close_owner_lease(lease)

    assert cleanup.as_artifact() == {
        "stableHandleClosed": False,
        "errors": ["captured CDP owner cleanup returned no report"],
        "status": "failed",
    }


def test_run_scenario_scope_launch_failure_executes_no_followup_app_logic(
    monkeypatch,
    tmp_path: Path,
) -> None:
    scenario = runner.Scenario(id="02-schema-edit", title="schema", requirement="atomic")
    package = tmp_path / "package"
    package.mkdir()
    (package / "host.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "host.exe"}}),
        encoding="utf-8",
    )
    monkeypatch.setattr(
        runner,
        "_launch_host_process",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(
            ProcessScopeLaunchError("nested Job denied")
        ),
    )
    monkeypatch.setattr(
        runner,
        "_wait_for_cdp",
        lambda *_args, **_kwargs: pytest.fail("CDP must not run after launch failure"),
    )
    monkeypatch.setattr(
        runner,
        "_run_node_runner",
        lambda *_args, **_kwargs: pytest.fail("Node must not run after launch failure"),
    )

    result = runner.run_scenario(
        scenario,
        package_root=package,
        evidence_root=tmp_path / "evidence",
        node="node",
    )

    assert result["status"] == "failed"
    assert result["error"]["code"] == "PROCESS_SCOPE_LAUNCH_FAILED"
    assert result["lifecycle"]["normalExitRequested"] is False


def test_run_scenario_aggregates_cleanup_failure_without_replacing_primary_error(
    monkeypatch,
    tmp_path: Path,
) -> None:
    scenario = runner.Scenario(id="02-schema-edit", title="schema", requirement="cleanup")
    package = tmp_path / "package"
    package.mkdir()
    (package / "host.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "host.exe"}}),
        encoding="utf-8",
    )

    class FailingCleanupScope(_FakeScope):
        def terminate_all(self) -> Any:
            return runner.ScopeTerminationResult(
                False,
                remaining_pids=None,
                errors=("terminate denied",),
            )

        def close(self) -> None:
            raise OSError("close denied")

    scope = FailingCleanupScope(members=(42, 7))
    monkeypatch.setattr(runner, "_launch_host_process", lambda *_args, **_kwargs: scope)
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    cdp_owner = _stub_cdp_owner_capture(monkeypatch)
    monkeypatch.setattr(
        runner,
        "_run_node_runner",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(ValueError("primary failure")),
    )

    result = runner.run_scenario(
        scenario,
        package_root=package,
        evidence_root=tmp_path / "evidence",
        node="node",
    )

    assert result["error"]["message"] == "primary failure"
    assert "terminate denied" in result["lifecycleError"]
    assert "close denied" in result["lifecycleError"]
    assert cdp_owner.observe_timeouts == []
    assert cdp_owner.closed is True


def test_run_scenario_fails_closed_when_lifecycle_observation_raises(
    monkeypatch,
    tmp_path: Path,
) -> None:
    scenario = runner.Scenario(
        id="02-schema-edit",
        title="schema",
        requirement="fail closed",
    )
    package = tmp_path / "package"
    package.mkdir()
    (package / "host.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "host.exe"}}),
        encoding="utf-8",
    )

    scope = _FakeScope(members=(42, 7))
    monkeypatch.setattr(runner, "_launch_host_process", lambda *_args, **_kwargs: scope)
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    cdp_owner = _stub_cdp_owner_capture(monkeypatch)
    monkeypatch.setattr(
        runner,
        "_run_node_runner",
        lambda *_args, **_kwargs: (0, "", ""),
    )
    monkeypatch.setattr(runner, "_wait_for_readiness", lambda *_args: {"ready": True})
    monkeypatch.setattr(
        runner,
        "_request_normal_exit",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(OSError("process inspection failed")),
    )

    result = runner.run_scenario(
        scenario,
        package_root=package,
        evidence_root=tmp_path / "evidence",
        node="node",
    )

    assert result["status"] == "failed"
    assert result["lifecycle"]["normalExitRequested"] is False
    assert result["lifecycle"]["status"] == "failed"
    assert result["lifecycle"]["errors"] == ["process inspection failed"]
    assert scope.terminate_calls == 1
    assert scope.close_calls == 1
    assert cdp_owner.observe_timeouts == []
    assert cdp_owner.closed is True


def test_normal_close_control_is_limited_to_the_test_mode_host_boundary() -> None:
    composition = (
        runner.ROOT / "desktop" / "src" / "VibeTable.Desktop" / "MainWindow.Product.cs"
    ).read_text(encoding="utf-8")
    controller = (
        runner.ROOT
        / "desktop"
        / "src"
        / "VibeTable.Desktop"
        / "Services"
        / "TestModeHostController.cs"
    ).read_text(encoding="utf-8")

    assert "_e2eControlsDir = startup.TestMode" in composition
    assert "new TestModeHostController(" in composition
    assert "Dispatcher.HasShutdownStarted" in composition
    assert "Dispatcher.HasShutdownFinished" in composition
    assert '"host-normal-close.request"' in controller
    assert 'TryConsume("host-normal-close.request")' in controller
    assert 'request.Type == "host.normalClose"' not in composition
    assert 'request.Type == "host.normalClose"' not in controller


def test_packaged_host_lifecycle_uses_fixed_test_mode_tray_controls() -> None:
    from tests.e2e import packaged_host_lifecycle

    assert packaged_host_lifecycle.WINDOW_CLOSE_CONTROL_FILE == "host-window-close.request"
    assert packaged_host_lifecycle.TRAY_EXIT_CONTROL_FILE == "host-tray-exit.request"
    source = packaged_host_lifecycle.__file__
    assert source is not None
    text = Path(source).read_text(encoding="utf-8")
    assert '"--test-mode-tray-lifecycle"' in text
    assert '"--autostart"' in text
    assert "VibeTable.Next.exe" in text


def test_host_lifecycle_artifacts_have_no_pid_tree_ownership_fallback() -> None:
    from tests.e2e import packaged_host_lifecycle

    obsolete_helpers = {
        "_descendants",
        "_windows_processes",
        "_stop_process_tree",
        "_stop_process_ids",
    }
    for module in (runner, packaged_host_lifecycle):
        module_path = module.__file__
        assert module_path is not None
        tree = ast.parse(Path(module_path).read_text(encoding="utf-8"))
        defined = {
            node.name
            for node in ast.walk(tree)
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        }
        assert defined.isdisjoint(obsolete_helpers)
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call) or not node.args:
                continue
            command = node.args[0]
            if not isinstance(command, (ast.List, ast.Tuple)):
                continue
            literal_args = {
                item.value.casefold()
                for item in command.elts
                if isinstance(item, ast.Constant) and isinstance(item.value, str)
            }
            assert "taskkill" not in literal_args


def test_packaged_state_failure_terminates_and_closes_the_job_scope(
    monkeypatch,
    tmp_path: Path,
) -> None:
    from tests.e2e import packaged_host_lifecycle

    scope = _FakeScope(members=(42, 7))
    streams = SimpleNamespace(close=lambda: None)
    monkeypatch.setattr(
        packaged_host_lifecycle,
        "_launch_host",
        lambda *_args, **_kwargs: (scope, 9222, tmp_path, streams),
    )
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    cdp_owner = _stub_cdp_owner_capture(monkeypatch)
    monkeypatch.setattr(runner, "_wait_for_readiness", lambda *_args: {"ready": True})
    monkeypatch.setattr(
        packaged_host_lifecycle,
        "_wait_for_state",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(TimeoutError("state missing")),
    )

    with pytest.raises(TimeoutError, match="state missing"):
        packaged_host_lifecycle._run_tray_case(tmp_path, tmp_path / "runtime")

    assert scope.terminate_calls == 1
    assert scope.close_calls == 1
    assert cdp_owner.observe_timeouts == []
    assert cdp_owner.closed is True


def test_packaged_cleanup_failure_is_not_allowed_to_mask_the_primary_error(
    monkeypatch,
    tmp_path: Path,
) -> None:
    from tests.e2e import packaged_host_lifecycle

    class FailingCleanupScope(_FakeScope):
        def terminate_all(self) -> Any:
            self.terminate_calls += 1
            return runner.ScopeTerminationResult(
                False,
                remaining_pids=None,
                errors=("terminate denied",),
            )

    scope = FailingCleanupScope(members=(42, 7))
    streams = SimpleNamespace(close=lambda: None)
    monkeypatch.setattr(
        packaged_host_lifecycle,
        "_launch_host",
        lambda *_args, **_kwargs: (scope, 9222, tmp_path, streams),
    )
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    cdp_owner = _stub_cdp_owner_capture(monkeypatch)
    monkeypatch.setattr(runner, "_wait_for_readiness", lambda *_args: {"ready": True})
    monkeypatch.setattr(
        packaged_host_lifecycle,
        "_wait_for_state",
        lambda *_args, **_kwargs: (_ for _ in ()).throw(ValueError("bad state")),
    )

    with pytest.raises(ValueError, match="bad state") as raised:
        packaged_host_lifecycle._run_tray_case(tmp_path, tmp_path / "runtime")

    assert any("terminate denied" in note for note in raised.value.__notes__)
    assert cdp_owner.observe_timeouts == []
    assert cdp_owner.closed is True


def test_packaged_tray_success_observes_and_closes_the_cdp_owner_lease(
    monkeypatch,
    tmp_path: Path,
) -> None:
    from tests.e2e import packaged_host_lifecycle

    scope = _FakeScope()
    scope.root = _SuccessfulRoot()
    streams = SimpleNamespace(close=lambda: None)
    monkeypatch.setattr(
        packaged_host_lifecycle,
        "_launch_host",
        lambda *_args, **_kwargs: (scope, 9222, tmp_path, streams),
    )
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    monkeypatch.setattr(runner, "_wait_for_readiness", lambda *_args: {"ready": True})
    states = iter(
        (
            {"action": "visible-startup", "windowVisible": True, "trayVisible": True},
            {"action": "close-to-tray", "windowVisible": False, "trayVisible": True},
        )
    )
    monkeypatch.setattr(packaged_host_lifecycle, "_wait_for_state", lambda *_args: next(states))
    cdp_owner = _stub_cdp_owner_capture(monkeypatch)

    result = packaged_host_lifecycle._run_tray_case(tmp_path, tmp_path / "runtime")

    assert result["status"] == "passed"
    assert result["lifecycle"]["status"] == "passed"
    assert result["lifecycle"]["ownerLeaseCleanup"]["stableHandleClosed"] is True
    assert cdp_owner.observe_timeouts
    assert cdp_owner.closed is True
    assert scope.close_calls == 1


def test_packaged_tray_reports_owner_handle_close_failure_without_raising(
    monkeypatch,
    tmp_path: Path,
) -> None:
    from tests.e2e import packaged_host_lifecycle

    scope = _FakeScope()
    scope.root = _SuccessfulRoot()
    streams = SimpleNamespace(close=lambda: None)
    monkeypatch.setattr(
        packaged_host_lifecycle,
        "_launch_host",
        lambda *_args, **_kwargs: (scope, 9222, tmp_path, streams),
    )
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    monkeypatch.setattr(runner, "_wait_for_readiness", lambda *_args: {"ready": True})
    states = iter(
        (
            {"action": "visible-startup", "windowVisible": True, "trayVisible": True},
            {"action": "close-to-tray", "windowVisible": False, "trayVisible": True},
        )
    )
    monkeypatch.setattr(packaged_host_lifecycle, "_wait_for_state", lambda *_args: next(states))
    cdp_owner = _stub_cdp_owner_capture(
        monkeypatch,
        close_error=OSError("C:\\private\\handle close denied"),
    )

    result = packaged_host_lifecycle._run_tray_case(tmp_path, tmp_path / "runtime")

    assert result["status"] == "failed"
    assert result["lifecycle"]["status"] == "failed"
    assert result["lifecycle"]["ownerLeaseCleanup"]["stableHandleClosed"] is False
    assert result["lifecycle"]["errors"] == ["unable to close captured CDP owner handle (OSError)"]
    assert "private" not in str(result)
    assert cdp_owner.close_calls == 1


def test_packaged_launch_closes_stdout_when_opening_stderr_fails(
    monkeypatch,
    tmp_path: Path,
) -> None:
    from tests.e2e import packaged_host_lifecycle

    package = tmp_path / "package"
    package.mkdir()
    (package / "VibeTable.Next.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "VibeTable.Next.exe"}}),
        encoding="utf-8",
    )
    runtime = tmp_path / "runtime"
    real_open = Path.open

    def fail_stderr_open(path: Path, *args: Any, **kwargs: Any) -> Any:
        if path.name == "host-stderr.log":
            raise OSError("stderr denied")
        return real_open(path, *args, **kwargs)

    monkeypatch.setattr(Path, "open", fail_stderr_open)
    monkeypatch.setattr(runner, "_reserve_port", lambda: 9222)

    with pytest.raises(OSError, match="stderr denied"):
        packaged_host_lifecycle._launch_host(
            package,
            runtime,
            autostart=False,
            tray_lifecycle=False,
        )

    (runtime / "host-stdout.log").rename(runtime / "stdout-closed.log")


@pytest.mark.parametrize("failure_stage", ["stderr-open", "scope-launch"])
def test_packaged_launch_preserves_primary_error_when_log_close_fails(
    monkeypatch,
    tmp_path: Path,
    failure_stage: str,
) -> None:
    from tests.e2e import packaged_host_lifecycle

    package = tmp_path / "package"
    package.mkdir()
    (package / "VibeTable.Next.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "VibeTable.Next.exe"}}),
        encoding="utf-8",
    )
    runtime = tmp_path / "runtime"
    real_open = Path.open

    class CloseFailure:
        def __init__(self, stream: Any) -> None:
            self.stream = stream

        def __enter__(self) -> Any:
            return self.stream.__enter__()

        def __exit__(self, *exc: object) -> None:
            self.stream.__exit__(*exc)
            raise OSError("stdout close denied")

    def controlled_open(path: Path, *args: Any, **kwargs: Any) -> Any:
        if path.name == "host-stderr.log" and failure_stage == "stderr-open":
            raise ValueError("stderr primary")
        stream = real_open(path, *args, **kwargs)
        return CloseFailure(stream) if path.name == "host-stdout.log" else stream

    monkeypatch.setattr(Path, "open", controlled_open)
    monkeypatch.setattr(runner, "_reserve_port", lambda: 9222)
    if failure_stage == "scope-launch":
        monkeypatch.setattr(
            runner,
            "_launch_host_process",
            lambda *_args, **_kwargs: (_ for _ in ()).throw(
                ProcessScopeLaunchError("scope primary")
            ),
        )

    expected = "stderr primary" if failure_stage == "stderr-open" else "scope primary"
    with pytest.raises((ValueError, ProcessScopeLaunchError), match=expected) as raised:
        packaged_host_lifecycle._launch_host(
            package,
            runtime,
            autostart=False,
            tray_lifecycle=False,
        )

    assert any("stdout close denied" in note for note in raised.value.__notes__)


def test_empty_job_ignores_unowned_system_network_activity(monkeypatch) -> None:
    scope = _FakeScope(exit_code=0)
    monkeypatch.setattr(
        runner,
        "query_windows_tcp_table",
        lambda **_kwargs: (TcpListenerRow("TCP", "0.0.0.0:445", "0.0.0.0:0", "LISTENING", 4),),
    )
    evidence: dict[str, Any] = {"observations": {}, "errors": [], "samples": 0}

    runner._record_process_network(scope, evidence)

    assert evidence["observations"] == {}


def test_empty_job_accepts_a_reused_numeric_port_when_the_listener_is_unowned(
    monkeypatch,
) -> None:
    scope = _FakeScope(exit_code=0)

    class ExitedOwnerLease:
        @staticmethod
        def observe_release(*, timeout: float) -> PortReleaseReport:
            assert 0 <= timeout <= 35
            return PortReleaseReport(
                owner_pid=42,
                owner_name="msedgewebview2.exe",
                capture_rows=(
                    TcpListenerRow("TCP", "127.0.0.1:9222", "0.0.0.0:0", "LISTENING", 42),
                ),
                release_rows=(
                    TcpListenerRow("TCP", "127.0.0.1:9222", "0.0.0.0:0", "LISTENING", 99),
                ),
                decision="captured-owner-exited-port-reused-unowned",
                released=True,
                owner_exited=True,
            )

        @staticmethod
        def close() -> OwnerLeaseCleanupReport:
            return OwnerLeaseCleanupReport(stable_handle_closed=True)

    report = runner._observe_scope_exit(scope, cdp_owner=ExitedOwnerLease())

    assert report["status"] == "passed"
    assert report["portsReleased"] is True
    assert report["portRelease"]["decision"] == ("captured-owner-exited-port-reused-unowned")
    assert report["portRelease"]["releaseRows"][0]["ownership"] == "unowned"


def test_network_evidence_accepts_only_stable_job_members(monkeypatch) -> None:
    class FakeScope:
        root = _FakeRoot()

        def __init__(self) -> None:
            self.samples = iter(
                (
                    ((42, "VibeTable.Next.exe"), (7, "msedgewebview2.exe"), (8, "stale.exe")),
                    ((42, "VibeTable.Next.exe"), (7, "msedgewebview2.exe")),
                )
            )

        def snapshot(self) -> Any:
            return SimpleNamespace(
                members=tuple(
                    SimpleNamespace(
                        pid=pid,
                        executable_name=name,
                        identity_verified=True,
                    )
                    for pid, name in next(self.samples)
                )
            )

    monkeypatch.setattr(
        runner,
        "query_windows_tcp_table",
        lambda **_kwargs: (
            TcpListenerRow("TCP", "127.0.0.1:1", "0.0.0.0:0", "LISTENING", 42),
            TcpListenerRow("TCP", "0.0.0.0:2", "0.0.0.0:0", "LISTENING", 7),
            TcpListenerRow("TCP", "127.0.0.1:4", "127.0.0.1:5", "ESTABLISHED", 8),
            TcpListenerRow("TCP", "0.0.0.0:6", "0.0.0.0:0", "LISTENING", 99),
        ),
    )
    evidence: dict[str, Any] = {"observations": {}, "errors": [], "samples": 0}

    runner._record_process_network(FakeScope(), evidence)

    assert {item["pid"] for item in evidence["observations"].values()} == {42, 7}
    report = runner._process_network_report(evidence, status="completed")
    assert [item["pid"] for item in report["webViewRuntimeBackgroundNetwork"]] == [7]
    assert report["unexpectedProductNonLoopback"] == []
    assert evidence["samples"] == 1


def test_abort_scope_preserves_query_and_termination_failures() -> None:
    class FakeScope:
        root = _FakeRoot()

        @staticmethod
        def snapshot() -> Any:
            raise RuntimeError("membership unavailable")

        @staticmethod
        def terminate_all() -> Any:
            return runner.ScopeTerminationResult(
                False,
                remaining_pids=None,
                errors=("TerminateJobObject failed",),
            )

        @staticmethod
        def wait_empty(*, timeout: float = 5.0) -> Any:
            del timeout
            return runner.ScopeWaitResult(
                None,
                errors=("membership unavailable",),
            )

    report = runner._abort_scope(FakeScope(), reason="readiness failed")

    assert report["status"] == "failed"
    assert report["errors"] == ["readiness failed", "membership unavailable"]
    assert report["cleanup"] == {
        "terminationRequested": False,
        "remainingPids": None,
        "errors": ["TerminateJobObject failed"],
        "status": "failed",
    }


def test_missing_package_is_a_strict_preflight_failure(tmp_path: Path) -> None:
    audit = runner.audit_package(tmp_path / "missing")

    assert audit["passed"] is False
    assert "does not exist" in audit["errors"][0]


def test_atomic_json_write_retries_transient_windows_replace_denial(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    destination = tmp_path / "result.json"
    destination.write_text('{"old": true}\n', encoding="utf-8")
    real_replace = runner.os.replace
    attempts = 0

    def transient_replace(source: Path, target: Path) -> None:
        nonlocal attempts
        attempts += 1
        if attempts < 3:
            raise PermissionError(5, "access denied", str(target))
        real_replace(source, target)

    monkeypatch.setattr(runner.os, "replace", transient_replace)
    monkeypatch.setattr(runner.time, "sleep", lambda _seconds: None)

    runner._write_json_atomic(destination, {"requestId": "second"})

    assert attempts == 3
    assert json.loads(destination.read_text(encoding="utf-8")) == {
        "requestId": "second",
    }
    assert not destination.with_suffix(".json.tmp").exists()


def test_package_fingerprint_is_content_and_path_sensitive(tmp_path: Path) -> None:
    package = tmp_path / "package"
    package.mkdir()
    (package / "a.txt").write_text("alpha", encoding="utf-8")
    first = runner.package_fingerprint(package)
    (package / "a.txt").write_text("beta", encoding="utf-8")
    second = runner.package_fingerprint(package)
    (package / "nested").mkdir()
    (package / "nested" / "a.txt").write_text("alpha", encoding="utf-8")
    third = runner.package_fingerprint(package)

    assert first["packageSha256"] != second["packageSha256"]
    assert first["packageSha256"] != third["packageSha256"]
    assert third["fileCount"] == 2


def test_aggregate_reports_failures_without_skips(tmp_path: Path) -> None:
    scenarios = runner.load_scenarios()
    results = [
        runner._failure_result(
            scenario,
            code="CAPABILITY_MISSING",
            message="not implemented",
        )
        for scenario in scenarios
    ]
    output = tmp_path / "report.json"

    report = runner.write_aggregate(
        output,
        audit={"passed": True},
        results=results,
    )

    assert report["summary"] == {
        "total": len(scenarios),
        "passed": 0,
        "failed": len(scenarios),
        "skipped": 0,
    }
    assert report["status"] == "failed"
    assert (
        json.loads(output.read_text(encoding="utf-8"))["transport"]["browserLaunchAllowed"] is False
    )


def test_document_diff_scenario_uses_closed_ui_operation_and_rejects_raw_materialize() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario14") : source.index("async function scenario15")
    ]

    assert "await waitForShell(page, recorder);" in scenario
    assert 'getByTestId("document-import")' in scenario
    assert 'rawWorkspaceV2Request(page, "fileHistory.restore"' in scenario
    assert 'getByTestId("compare-revision")' in scenario
    assert 'getByTestId("diff-result")' in scenario
    assert "operationId: crypto.randomUUID()" in scenario
    assert 'rawWorkspaceV2Request(page, "fileHistory.materializeDiffPair"' in scenario
    assert "fileHistory.materializeDiffPair" in scenario


def test_protection_policy_scenario_opens_a_real_workspace_before_settings() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario13") : source.index("async function scenario14")
    ]
    shell = "await waitForShell(page, recorder);"
    settings = 'getByTestId("nav-settings")'
    verify = 'getByTestId("repository-verify")'

    assert shell in scenario
    assert scenario.index(shell) < scenario.index(settings) < scenario.index(verify)
    assert "repository-verify" in scenario
    assert "!button.disabled" in scenario


def test_protection_policy_scenario_applies_the_previewed_cleanup_plan_through_ui() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario13") : source.index("async function scenario14")
    ]

    preview = 'getByTestId("retention-plan-preview")'
    apply = 'getByTestId("retention-plan-apply")'
    preview_capture = 'beginWorkspaceV2MethodCapture(page, "retention.plan")'
    apply_capture = 'beginWorkspaceV2MethodCapture(page, "retention.apply")'
    response = "const retentionApply = await waitForCapturedBridgeMessage(page, 30_000);"

    assert preview in scenario
    assert apply in scenario
    assert preview_capture in scenario
    assert apply_capture in scenario
    assert response in scenario
    assert scenario.index(preview) < scenario.index(preview_capture) < scenario.index(apply)
    assert (
        scenario.index(apply)
        < scenario.index(apply_capture)
        < scenario.index("await apply.click();")
    )
    assert "cleanupIsEmpty" in scenario
    assert "refusing to apply a non-empty retention cleanup plan" in scenario
    assert "retentionApply.payload?.ok === true" in scenario
    assert "deletedObjects === 0" in scenario
    assert "reclaimedBytes === 0" in scenario


def test_plugin_invalid_upgrade_is_recorded_as_an_expected_bridge_failure() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario11") : source.index("async function scenario12")
    ]

    assert 'beginBridgeMessageCapture(page, ["operation.failed"])' in scenario
    assert "invalidUpgradeFailure = await waitForCapturedBridgeMessage" in scenario
    assert "await acknowledgeExpectedBridgeFailure(page, invalidUpgradeFailure);" in scenario


def test_plugin_lifecycle_waits_for_the_install_enable_request_to_settle() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario11") : source.index("async function scenario12")
    ]

    assert (
        "const databaseOpened = await waitForShell(page, recorder, "
        "{ requireDatabaseOpened: true })" in scenario
    )
    assert "const projectKey = databaseOpened.payload.projectKey.trim()" in scenario
    assert 'projectKey: "local:default"' not in scenario
    first_toggle = scenario.index("await toggle.click();")
    stable_enabled = scenario.index('lifecycleToggle.classList.contains("enabled")')
    assert stable_enabled < first_toggle
    assert scenario.count("!lifecycleToggle.disabled") == 3
    assert scenario.count("runButton.disabled") >= 3


def test_snapshot_corruption_uses_a_stable_selector_and_exact_bridge_failure() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario15") : source.index("async function scenario16")
    ]

    assert 'page.getByTestId("snapshot-operation-error")' in scenario
    assert 'corruptRoundTrip?.code === "snapshot.package_invalid"' in scenario
    assert "await acknowledgeExpectedBridgeFailure(page, corruptRoundTrip);" in scenario
    assert '.locator(".n-alert--error")' not in scenario


def test_long_scenario_names_do_not_expand_the_packaged_runtime_path(tmp_path: Path) -> None:
    scenario = runner.Scenario(
        id="15-workspace-snapshot-package",
        title="snapshot",
        requirement="bounded Windows paths",
    )

    runtime_dir = runner._scenario_runtime_directory(tmp_path / "evidence", scenario)

    assert runtime_dir == (tmp_path / "evidence" / "_runtime" / "15").resolve()
    assert scenario.id not in str(runtime_dir)


def test_dashboard_scenario_clears_the_default_name_before_asserting_empty_validation() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[source.index("async function scenario16") : source.index("const scenarios")]
    clear = 'getByTestId("dashboard-create-name").locator("input").fill("")'
    disabled = 'recorder.check("Dashboard rejects an empty name before persistence"'

    assert clear in scenario
    assert scenario.index(clear) < scenario.index(disabled)


def test_dashboard_filter_waits_for_the_unbound_metric_to_settle() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[source.index("async function scenario16") : source.index("const scenarios")]
    filter_action = scenario.index(
        'await addVisibleNTagOption(page, "dashboard-filter-value-region"'
    )
    assertion = scenario.index(
        'recorder.check("the real FilterBar limits only its explicitly bound record panel"'
    )
    setup = scenario[:filter_action]
    synchronization = scenario[filter_action:assertion]

    assert "const filterQueryStart = await page.evaluate" in setup
    assert "__vibetableE2EBridgeDiagnostics?.roundTrips" in synchronization
    assert ".slice(start)" in synchronization
    assert 'item.requestType === "dashboard.queryRequested"' in synchronization
    assert 'item.responseType === "dashboard.queryLoaded"' in synchronization
    assert 'querySelector(".metric-panel strong")?.textContent?.trim()' in synchronization
    assert 'metricValue === "2"' in synchronization
    assert 'locator(".metric-panel strong").innerText()' in synchronization
    assert 'metricValue.trim() === "2"' in scenario[filter_action:]


def test_dashboard_conflict_reload_reads_the_winning_note_through_settings() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[source.index("async function scenario16") : source.index("const scenarios")]
    reload_conflict = scenario.index('getByTestId("dashboard-reload-conflict").click()')
    setup = scenario[:reload_conflict]
    recovery = scenario[reload_conflict:]

    assert "const conflictReloadStart = await page.evaluate" in setup
    assert ".slice(start)" in recovery
    assert 'item.requestType === "dashboard.readRequested"' in recovery
    assert 'item.responseType === "dashboard.loaded"' in recovery
    assert 'const editAfterReload = page.getByTestId("dashboard-edit")' in recovery
    assert "await editAfterReload.click()" in recovery
    assert 'const configureAfterReload = page.getByTestId("dashboard-configure")' in recovery
    assert "await configureAfterReload.click()" in recovery
    assert 'getByTestId("dashboard-settings-note")' in recovery
    assert 'reloadedNote === "competing E2E save"' in recovery


def test_workspace_switch_scenario_accepts_the_closed_stale_session_error() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario15") : source.index("async function scenario16")
    ]

    assert 'stale.payload?.error?.code === "workspace.session_stale"' in scenario


def test_performance_summary_reports_scenarios_bridge_percentiles_and_failures() -> None:
    summary = runner.summarize_performance(
        [
            {
                "scenario": "07-attachment-history",
                "status": "passed",
                "durationMs": 9_500,
                "uiTimings": [{"name": "history.drawer.initialLoad", "durationMs": 125}],
                "bridgeDiagnostics": {
                    "pending": [],
                    "roundTrips": [
                        {
                            "requestType": "history.query",
                            "responseType": "workspace.v2.response",
                            "durationMs": 20,
                        },
                        {
                            "requestType": "history.query",
                            "responseType": "workspace.v2.response",
                            "durationMs": 80,
                        },
                    ],
                },
            },
            {
                "scenario": "10-sse-reconnect",
                "status": "passed",
                "durationMs": 8_000,
                "bridgeDiagnostics": {
                    "pending": [{"requestType": "query.page"}],
                    "roundTrips": [
                        {
                            "requestType": "query.page",
                            "responseType": "operation.failed",
                            "code": "BACKEND_UNAVAILABLE",
                            "durationMs": 3_000,
                        }
                    ],
                },
            },
        ]
    )

    assert summary["assessment"] == {
        "historyQuery": "within-budget",
        "historyDrawer": "within-budget",
        "pendingRequests": 1,
        "bridgeFailures": 1,
    }
    assert summary["scenarios"][0]["durationMs"] == 9_500
    assert summary["byUiAction"] == [
        {
            "name": "history.drawer.initialLoad",
            "count": 1,
            "p50Ms": 125.0,
            "p95Ms": 125.0,
            "maxMs": 125.0,
        }
    ]
    history = next(
        item for item in summary["byOperation"] if item["requestType"] == "history.query"
    )
    assert history == {
        "requestType": "history.query",
        "count": 2,
        "failures": 0,
        "p50Ms": 20.0,
        "p95Ms": 80.0,
        "maxMs": 80.0,
    }


def test_node_runner_only_attaches_to_existing_webview2() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")

    assert "chromium.connectOverCDP" in source
    assert "chromium.launch(" not in source
    assert "chromium.launchPersistentContext(" not in source
    assert "CAPABILITY_MISSING" not in source
    assert "pendingScenario" not in source


def test_node_runner_enforces_closed_history_and_no_external_http() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")

    assert '"history.read"' not in source
    assert '"history.query"' in source
    assert "rawWorkspaceV2Request(" in source
    assert '"history.queryRequested"' not in source
    assert "externalRequests.length === 0" in source
    assert 'url.hostname === "app.vibetable.local"' in source
    assert '["127.0.0.1", "::1", "localhost"]' in source
    assert "process-network-observations.json" in source
    assert "unexpectedProductNonLoopback.length === 0" in source
    assert "assertCleanBridgeDiagnostics(recorder" in source
    assert "failures.length === 0 && pending.length === 0" in source
    assert "allowedBridgeFailureCodes" not in source
    assert "allowedPageErrors" not in source


def test_node_runner_waits_for_bridge_quiescence_instead_of_a_fixed_delay() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    helper = source[
        source.index("async function waitForBridgeDiagnosticsToSettle") : source.index(
            "async function acknowledgeExpectedBridgeFailure"
        )
    ]
    completion_start = source.index("const implementation = scenarios[args.scenario]")
    completion = source[
        completion_start : source.index("assertCleanRendererDiagnostics(recorder", completion_start)
    ]

    assert "timeoutMs = 10_000" in helper
    assert "quietMs = 250" in helper
    assert "if (failures.length > 0) return diagnostics" in helper
    assert "if (pending.length === 0)" in helper
    assert "quietSince = null" in helper
    assert "waitForBridgeDiagnosticsToSettle(page)" in completion
    assert "await page.waitForTimeout(250)" not in completion
    assert "JSON.stringify(details)" in source
    assert "serialized.slice(0, 4_000)" in source


def test_expected_bridge_rejection_is_acknowledged_only_after_the_scenario_asserts_it() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario03") : source.index("async function scenario04")
    ]

    assert "await acknowledgeExpectedBridgeFailure(page, legacy)" in scenario
    assert "diagnostics.acknowledgedFailures" in source
    assert "diagnostics.failures.splice(index, 1)" in source


def test_failed_scenario_summary_includes_bounded_bridge_diagnostics() -> None:
    summary = runner._format_failed_scenario(
        {
            "scenario": "04-json-round-trip",
            "status": "failed",
            "error": {"code": "SCENARIO_FAILED", "message": "preview timed out"},
            "bridgeDiagnostics": {
                "failures": [
                    {
                        "requestType": "data.previewImport",
                        "code": "IMPORT_INVALID",
                    }
                ],
                "pending": [{"requestType": "data.importSourceRequested"}],
                "roundTrips": [
                    {"requestType": f"request.{index}", "responseType": "response"}
                    for index in range(15)
                ],
            },
        }
    )

    assert summary.startswith("  - 04-json-round-trip: SCENARIO_FAILED preview timed out")
    assert '"requestType":"data.previewImport"' in summary
    assert '"code":"IMPORT_INVALID"' in summary
    assert '"requestType":"data.importSourceRequested"' in summary
    assert '"recentRoundTrips"' in summary
    assert '"requestType":"request.14"' in summary
    assert '"requestType":"request.0"' not in summary
    assert '"requestType":"request.2"' not in summary
    assert '"requestType":"request.3"' in summary
    assert len(summary) <= 4_500


def test_invalid_field_assertion_matches_typed_v2_diagnostic_contract() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario03") : source.index("async function scenario04")
    ]

    assert '"field.change.plan"' in source
    assert 'v2Response.type === "field.change.plan"' in scenario
    assert '"field.contract.invalid"' in scenario
    assert "v2Response.payload?.error?.code" in scenario
    assert '"schema.validate"' in scenario
    assert 'legacy.type === "operation.failed"' in scenario


def test_atomic_import_fault_waits_for_transactional_barrier() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    orchestrator = Path(runner.__file__).read_text(encoding="utf-8")

    assert '"mutation-barrier.ready.json"' in source
    assert 'ready.point === "after_record"' in source
    assert "fault.pid === barrier.pid" in source
    assert "VIBETABLE_E2E_MUTATION_BARRIER_DIR" in orchestrator
    assert '"09-atomic-import-scale"' in orchestrator
    assert "confirmImportPreview(page)" in source
    assert 'getByTestId("import-preview-panel")' in source
    assert 'getByTestId("import-confirm")' in source
    assert (
        'page.once("dialog"'
        not in source[
            source.index("async function scenario09") : source.index("async function scenario10")
        ]
    )
    assert '"storage-proof-request.json"' in source
    assert "storageProof.counts?.idempotency === 0" in source
    assert "key NOT LIKE 'field-v2:%'" in orchestrator
    assert "storageProof.counts?.outbox === 0" in source
    assert "handled_storage_proof_ids" in orchestrator
    assert "result.requestId !== requestId" in source


def test_product_json_scenario_uses_keyboard_and_normalized_deep_comparisons() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario04") : source.index("async function scenario05")
    ]

    assert 'jsonCell.press("Enter")' in scenario
    assert 'page.keyboard.press("Escape")' in scenario
    assert 'jsonCell.press("Shift+F10")' in scenario
    assert 'operation: "capture"' in scenario
    assert 'target: "json"' in scenario
    assert "hasDialogFocusLeaseTerminalInPage" in scenario
    assert "readDialogFocusLeaseEvidenceInPage" in scenario
    assert 'focusLeaseEvidence.terminal?.state === "restored"' in scenario
    assert scenario.index('operation: "capture"') < scenario.index('page.keyboard.press("Escape")')
    assert "focusRestoration.documentHasFocus\n      &&" in scenario
    assert "!focusRestoration.documentHasFocus" not in scenario
    assert "document.activeElement === element" in scenario
    assert "`${jsonField}\\n" in scenario
    assert 'setProductLocale(page, "en-US")' in scenario
    assert 'setProductLocale(page, "zh-CN")' in scenario
    assert "canonicalJsonSet(authoritativeValues)" in scenario
    assert "canonicalJsonSet(exportedValues)" in scenario


def test_verified_purge_receipt_is_exact_evidence() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    backup = source[source.index("async function scenario12") : source.index("const scenarios")]

    assert "preservedChangeSetIds" in backup
    assert "postSnapshotValuePreserved" in backup
    assert "postSnapshotAttachmentPreserved" in backup
    assert "beforeAuditSnapshot === afterAuditSnapshot" not in backup
    assert "snapshotStorageProof.auditLedger?.verified === true" in backup
    assert "preservedSnapshotAnchor === snapshotStorageProof.auditLedger.anchorHash" in backup
    assert 'record.sourceEpoch.startsWith("snapshot-restore:")' in backup
    assert 'record.sourceEpoch === "business-v2"' in backup


def test_node_runner_receives_the_authoritative_compact_runtime_data_root() -> None:
    source = Path(runner.__file__).read_text(encoding="utf-8")
    node_source = runner.NODE_RUNNER.read_text(encoding="utf-8")

    assert '"--data-root",\n                str(readiness_dir / "local-data")' in source
    assert '"data-root"' in node_source[node_source.index("function parseArgs") :]
    assert 'dataRoot: path.resolve(args["data-root"])' in node_source


def test_workspace_creation_reports_the_bridge_failure_without_a_locator_timeout() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    shell = source[
        source.index("async function waitForShell") : source.index("async function selectNValue")
    ]

    assert "const creationOutcome = await Promise.race" in shell
    assert 'item.requestType === "workspace.create"' in shell
    assert "workspace creation failed:" in shell


def test_backup_consistency_uses_current_snapshot_versions_ui_contract() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[source.index("async function scenario12") : source.index("const scenarios")]

    assert 'getByTestId("settings-nav-versions")' in scenario
    assert 'getByTestId("snapshot-create")' in scenario
    assert 'getByTestId("snapshot-restore-open")' in scenario
    assert 'getByTestId("snapshot-restore-preview")' in scenario
    assert "settings-nav-backup" not in scenario
    assert "backup-create" not in scenario
    assert "backup-restore-" not in scenario


def test_fault_scenarios_use_host_allocated_table_and_field_identities() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario09 = source[
        source.index("async function scenario09") : source.index("async function scenario10")
    ]
    scenario11 = source[
        source.index("async function scenario11") : source.index("async function scenario12")
    ]
    scenario12 = source[source.index("async function scenario12") : source.index("const scenarios")]

    assert '"tbl_e2e_atomic_import"' not in scenario09
    assert '"tbl_e2e_plugin_target"' not in scenario11
    assert '"tbl_e2e_backup_consistency"' not in scenario12
    assert 'const tableId = await createEmptyTable(page, "E2E Backup Consistency")' in scenario12
    assert "createV2Field(" in scenario12
    assert "valueField.physicalName" in scenario12
    assert "formulaField.physicalName" in scenario12
    assert "attachmentField.physicalName" in scenario12
    assert "create-table-field-name-" not in scenario12


def test_storage_proof_reads_all_transactional_surfaces_read_only(
    tmp_path: Path,
) -> None:
    local_data = tmp_path / "runtime" / "local-data"
    data_root = local_data / "pocketbase"
    data_root.mkdir(parents=True)
    database = data_root / "data.db"
    connection = sqlite3.connect(database)
    try:
        connection.executescript(
            """
            CREATE TABLE vibetable_tables(table_id TEXT, physical_name TEXT);
            CREATE TABLE e2e_atomic_import(id TEXT);
            CREATE TABLE vibetable_audit_events(table_id TEXT);
            CREATE TABLE vibetable_idempotency_keys(key TEXT);
            CREATE TABLE vibetable_outbox(event_id TEXT, payload_json TEXT);
            INSERT INTO vibetable_tables(table_id, physical_name)
            VALUES ('tbl_e2e_atomic_import', 'e2e_atomic_import');
            INSERT INTO vibetable_idempotency_keys(key)
            VALUES ('metadata:settings:update:test');
            INSERT INTO vibetable_outbox(event_id, payload_json)
            VALUES ('metadata-event', '{"tableId":"metadata:shared_settings"}');
            """
        )
        connection.commit()
    finally:
        connection.close()
    audit_root = data_root.parent / "audit"
    audit_root.mkdir()
    ledger = sqlite3.connect(audit_root / "ledger.db")
    try:
        ledger.execute(
            """
            CREATE TABLE audit_ledger (
                ledger_sequence INTEGER PRIMARY KEY,
                event_id TEXT NOT NULL UNIQUE,
                source_epoch TEXT NOT NULL,
                source_sequence INTEGER NOT NULL,
                mutation_identity TEXT NOT NULL,
                payload_hash TEXT NOT NULL,
                payload BLOB NOT NULL,
                occurred_at TEXT NOT NULL,
                previous_hash TEXT NOT NULL,
                hash TEXT NOT NULL,
                UNIQUE(source_epoch, source_sequence)
            )
            """
        )
        ledger.commit()
    finally:
        ledger.close()

    proof = runner._handle_storage_proof(
        {
            "requestId": "11111111-1111-4111-8111-111111111111",
            "tableId": "tbl_e2e_atomic_import",
        },
        local_data,
    )

    assert proof["status"] == "completed"
    assert proof["requestId"] == "11111111-1111-4111-8111-111111111111"
    assert proof["database"]["readOnly"] is True
    assert proof["auditLedger"] == {
        "path": str(audit_root / "ledger.db"),
        "readOnly": True,
        "verified": True,
        "count": 0,
        "anchorHash": "",
        "sourceHighWatermarks": {},
        "records": [],
    }
    assert proof["counts"] == {
        "records": 0,
        "audit": 0,
        "idempotency": 0,
        "outbox": 0,
    }


def test_storage_proof_resolves_the_workspace_v2_database_from_the_runtime_registry(
    tmp_path: Path,
) -> None:
    local_data = tmp_path / "isolated-host" / "local-data"
    selected_root = tmp_path / "workspace-root"
    data_root = selected_root / ".vibetable" / "data"
    data_root.mkdir(parents=True)
    database = data_root / "data.db"
    connection = sqlite3.connect(database)
    try:
        connection.executescript(
            """
            CREATE TABLE vibetable_tables(table_id TEXT, physical_name TEXT);
            CREATE TABLE workspace_records(id TEXT);
            CREATE TABLE vibetable_audit_events(table_id TEXT);
            CREATE TABLE vibetable_idempotency_keys(key TEXT);
            CREATE TABLE vibetable_outbox(event_id TEXT, payload_json TEXT);
            INSERT INTO vibetable_tables(table_id, physical_name)
            VALUES ('tbl_workspace', 'workspace_records');
            """
        )
        connection.commit()
    finally:
        connection.close()
    audit_root = selected_root / ".vibetable" / "audit"
    audit_root.mkdir()
    ledger = sqlite3.connect(audit_root / "ledger.db")
    try:
        ledger.execute(
            """
            CREATE TABLE audit_ledger (
                ledger_sequence INTEGER PRIMARY KEY,
                event_id TEXT NOT NULL UNIQUE,
                source_epoch TEXT NOT NULL,
                source_sequence INTEGER NOT NULL,
                mutation_identity TEXT NOT NULL,
                payload_hash TEXT NOT NULL,
                payload BLOB NOT NULL,
                occurred_at TEXT NOT NULL,
                previous_hash TEXT NOT NULL,
                hash TEXT NOT NULL,
                UNIQUE(source_epoch, source_sequence)
            )
            """
        )
        ledger.commit()
    finally:
        ledger.close()
    registry_path = local_data / "VibeTable" / "shell" / "workspace-registry-v2.json"
    registry_path.parent.mkdir(parents=True)
    registry_path.write_text(
        json.dumps(
            {
                "formatVersion": 2,
                "workspaces": [
                    {
                        "workspaceId": "11111111-1111-4111-8111-111111111111",
                        "selectedRoot": str(selected_root),
                        "lastOpenedAt": "2026-08-09T12:00:00Z",
                    }
                ],
            }
        ),
        encoding="utf-8",
    )

    proof = runner._handle_storage_proof(
        {
            "requestId": "22222222-2222-4222-8222-222222222222",
            "tableId": "tbl_workspace",
        },
        local_data,
    )

    assert proof["status"] == "completed"
    assert proof["database"] == {"path": str(database), "readOnly": True}
    assert proof["counts"] == {
        "records": 0,
        "audit": 0,
        "idempotency": 0,
        "outbox": 0,
    }


def test_audit_ledger_proof_verifies_payload_links_and_record_hashes(
    tmp_path: Path,
) -> None:
    ledger_path = tmp_path / "ledger.db"
    connection = sqlite3.connect(ledger_path)
    payload = b'{"type":"workspace.snapshotRestored"}'
    payload_hash = "sha256:" + hashlib.sha256(payload).hexdigest()
    occurred_at = "2026-07-29T00:00:00Z"
    envelope = {
        "eventId": "snapshot-restore:operation",
        "sourceEpoch": "snapshot-restore:operation",
        "sourceSequence": 1,
        "mutationIdentity": "snapshot-restore:operation",
        "payloadHash": payload_hash,
        "payload": json.loads(payload),
        "occurredAt": occurred_at,
    }
    record_hash = (
        "sha256:"
        + hashlib.sha256(
            json.dumps(
                {
                    "ledgerSequence": 1,
                    "previousHash": "",
                    "envelope": envelope,
                },
                ensure_ascii=False,
                separators=(",", ":"),
            ).encode()
        ).hexdigest()
    )
    try:
        connection.executescript(
            """
            CREATE TABLE audit_ledger (
                ledger_sequence INTEGER PRIMARY KEY,
                event_id TEXT NOT NULL UNIQUE,
                source_epoch TEXT NOT NULL,
                source_sequence INTEGER NOT NULL,
                mutation_identity TEXT NOT NULL,
                payload_hash TEXT NOT NULL,
                payload BLOB NOT NULL,
                occurred_at TEXT NOT NULL,
                previous_hash TEXT NOT NULL,
                hash TEXT NOT NULL,
                UNIQUE(source_epoch, source_sequence)
            );
            """
        )
        connection.execute(
            """
            INSERT INTO audit_ledger VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                1,
                envelope["eventId"],
                envelope["sourceEpoch"],
                1,
                envelope["mutationIdentity"],
                payload_hash,
                payload,
                occurred_at,
                "",
                record_hash,
            ),
        )
        connection.commit()
    finally:
        connection.close()

    proof = runner._audit_ledger_proof(ledger_path)

    assert proof["verified"] is True
    assert proof["count"] == 1
    assert proof["anchorHash"] == record_hash
    assert proof["sourceHighWatermarks"] == {"snapshot-restore:operation": 1}

    connection = sqlite3.connect(ledger_path)
    try:
        connection.execute("UPDATE audit_ledger SET hash = 'sha256:tampered'")
        connection.commit()
    finally:
        connection.close()
    with pytest.raises(RuntimeError, match="chain is invalid"):
        runner._audit_ledger_proof(ledger_path)


def test_process_network_report_rejects_listener_and_remote_non_loopback() -> None:
    evidence = {
        "samples": 2,
        "errors": [],
        "observations": {
            "loopback": {
                "protocol": "TCP",
                "local": "127.0.0.1:4000",
                "remote": "0.0.0.0:0",
                "state": "侦听",
                "pid": 10,
                "processName": "vibetable-pb.exe",
            },
            "listener": {
                "protocol": "TCP",
                "local": "0.0.0.0:5000",
                "remote": "0.0.0.0:0",
                "state": "侦听",
                "pid": 11,
                "processName": "VibeTable.Next.exe",
            },
            "remote": {
                "protocol": "TCP",
                "local": "192.0.2.5:5001",
                "remote": "203.0.113.9:443",
                "state": "ESTABLISHED",
                "pid": 12,
                "processName": "msedgewebview2.exe",
            },
        },
    }

    report = runner._process_network_report(evidence, status="completed")

    assert report["status"] == "completed"
    assert {item["reason"] for item in report["unexpectedNonLoopback"]} == {
        "non_loopback_listener",
        "non_loopback_remote",
    }
    assert [item["processName"] for item in report["unexpectedProductNonLoopback"]] == [
        "VibeTable.Next.exe"
    ]
    assert [item["processName"] for item in report["webViewRuntimeBackgroundNetwork"]] == [
        "msedgewebview2.exe"
    ]


def test_schema_scenario_uses_authoritative_capabilities_and_stable_identities() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario02") : source.index("async function rawBridgeRequest")
    ]

    assert "createEmptyTable(page" in scenario
    assert "createV2Field(page" in scenario
    assert '"field.settings.describe"' in scenario
    assert '"field.change.plan"' in source
    assert '"field.change.apply"' in source
    assert 'field.fieldId?.startsWith("fld_")' in scenario
    assert 'field.physicalName?.startsWith("f_")' in scenario
    assert "updated.planned?.payload?.classes?.every" in scenario
    assert '"field.recycleBin.list"' in scenario
    assert '"retire"' in scenario
    assert '"restore"' in scenario
    assert 'legacyWrite.type === "operation.failed"' in scenario


def test_schema_scenario_waits_for_submit_completion_without_a_fixed_delay() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario02") : source.index("async function rawBridgeRequest")
    ]

    assert "createEmptyTable(page" in scenario
    assert "waitForCreateTableSubmission(page, submit)" in source
    assert "waitForTimeout(1_000)" not in scenario
    assert "create table submission did not complete before timeout" in source
    assert "inputVisible" in source
    assert "submitDisabled" in source


def test_realtime_scenario_refreshes_the_active_table_without_reselection() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario10") : source.index("async function scenario11")
    ]

    assert "waitForTableRecovery" not in scenario
    assert "waitForActiveTableBackend" in scenario
    assert '"mutation.apply"' in scenario
    assert '"after-reconnect"' in scenario
    assert "waitForStableGridState(page" in scenario
    assert "expectedMatchingCells: 1" in scenario
    assert "stableForMs: 1_500" in scenario
    assert "waitForTimeout(750)" not in scenario
    assert "grid did not reach a stable expected state" in source


def test_bridge_recovery_and_workspace_wire_contracts_use_the_locked_node_runtime() -> None:
    test_files = [
        runner.NODE_RUNNER.with_name("bridge_failure_policy.test.mjs"),
        runner.NODE_RUNNER.with_name("bridge_capture_wait.test.mjs"),
        runner.NODE_RUNNER.with_name("bridge_diagnostics_instrumentation.test.mjs"),
        runner.NODE_RUNNER.with_name("dialog_focus_terminal.test.mjs"),
        runner.NODE_RUNNER.with_name("bridge_raw_request.test.mjs"),
        runner.NODE_RUNNER.with_name("scenario18_recovery_boundary.test.mjs"),
        runner.NODE_RUNNER.with_name("workspace_activation_readiness.test.mjs"),
        runner.NODE_RUNNER.with_name("workspace_search_terminal.test.mjs"),
        runner.NODE_RUNNER.with_name("workspace_v2_method_terminal.test.mjs"),
    ]
    completed = subprocess.run(
        [str(ensure_node(runner.ROOT)), "--test", *(str(path) for path in test_files)],
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=30,
    )

    assert completed.returncode == 0, completed.stdout + completed.stderr


def test_realtime_scenario_recovers_a_packaged_backend_with_a_fresh_safe_session() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")
    scenario = source[
        source.index("async function scenario10") : source.index("async function scenario11")
    ]

    assert "requestPackagedProcessKill(" in scenario
    assert 'return requestPackagedProcessKill(runtime, "kill-sidecar", reason)' in source
    assert '"kill-backend"' in scenario
    assert 'backendFault.processName === "vibetable-backend.exe"' in scenario
    assert "rawWorkspaceV2Request(" in scenario
    assert '"workspace.close"' in scenario
    assert "beginWritableWorkspaceBootstrapCapture" in scenario
    assert "recoveredSession.sessionEpoch > backendSourceSession.sessionEpoch" in scenario
    assert "requestWithStaleWorkspaceScope" in scenario
    assert '"retention.update"' in scenario
    assert 'staleBackendWrite.payload?.error?.code === "workspace.session_stale"' in scenario
    assert (
        "retentionAfterStaleWrite.policyRevision === retentionBeforeStaleWrite.policyRevision"
        in scenario
    )
    assert '"after-backend-recovery"' in scenario


def test_plugin_confirmation_assertion_is_not_constant_true() -> None:
    source = runner.NODE_RUNNER.read_text(encoding="utf-8")

    constant_true_assertion = (
        'recorder.check("authorized mutation plan required explicit confirmation", true'
    )
    assert constant_true_assertion not in source
    assert "/FINAL WRITE CONFIRMATION/i.test(confirmationText)" in source
    assert 'confirmationTargetCount.trim() === "1"' in source
    assert "await confirmationApprove.isEnabled()" in source
    assert 'getByTestId("plugin-toggle")' in source
    assert 'getByTestId("plugin-upgrade")' in source
    assert 'path.join(runtime.controlsDir, "invalid-plugin-upgrade")' in source
    assert "invalid upgrade source is rejected without replacing the installation" in source


@pytest.mark.parametrize(
    ("node_result", "expected_code", "expected_message"),
    [
        (
            {"status": "passed"},
            "NODE_RUNNER_FAILED",
            "node crashed",
        ),
        (
            {
                "status": "failed",
                "error": {
                    "code": "SCENARIO_FAILED",
                    "message": "authoritative assertion failed",
                },
            },
            "SCENARIO_FAILED",
            "authoritative assertion failed",
        ),
    ],
)
def test_nonzero_node_exit_preserves_structured_scenario_failure_but_rejects_passing_json(
    monkeypatch,
    tmp_path: Path,
    node_result: dict[str, Any],
    expected_code: str,
    expected_message: str,
) -> None:
    scenario = runner.Scenario(
        id="01-offline-first-start",
        title="offline",
        requirement="fail closed",
    )
    package = tmp_path / "package"
    package.mkdir()
    (package / "host.exe").write_bytes(b"host")
    (package / "publish-layout.json").write_text(
        json.dumps({"launch": {"host": "host.exe"}}),
        encoding="utf-8",
    )

    scope = _FakeScope()
    monkeypatch.setattr(runner, "_launch_host_process", lambda *_args, **_kwargs: scope)
    monkeypatch.setattr(runner, "_wait_for_cdp", lambda *_args: None)
    _stub_cdp_owner_capture(monkeypatch)
    monkeypatch.setattr(
        runner,
        "_wait_for_readiness",
        lambda *_args: {"ready": True},
    )
    monkeypatch.setattr(
        runner,
        "_request_normal_exit",
        lambda *_args, **_kwargs: {
            "normalExitRequested": True,
            "hostExitCode": 0,
            "membersAfterExit": [],
            "portsReleased": True,
            "errors": [],
            "status": "passed",
        },
    )

    def fake_node(
        _command: list[str],
        *,
        scenario_dir: Path,
        local_data: Path,
        host_scope: Any,
        process_network: dict[str, Any] | None = None,
    ) -> tuple[int, str, str]:
        del local_data
        del host_scope
        del process_network
        (scenario_dir / f"{scenario.id}-result.json").write_text(
            json.dumps({"scenario": scenario.id, **node_result}),
            encoding="utf-8",
        )
        (scenario_dir / "process-network-observations.json").write_text(
            json.dumps(
                {
                    "status": "completed",
                    "samples": 1,
                    "observations": [],
                    "unexpectedNonLoopback": [],
                    "errors": [],
                }
            ),
            encoding="utf-8",
        )
        return 7, "", "node crashed"

    monkeypatch.setattr(runner, "_run_node_runner", fake_node)
    result = runner.run_scenario(
        scenario,
        package_root=package,
        evidence_root=tmp_path / "evidence",
        node="node",
    )

    assert result["status"] == "failed"
    assert result["nodeExitCode"] == 7
    assert result["error"]["code"] == expected_code
    assert expected_message in result["error"]["message"]
