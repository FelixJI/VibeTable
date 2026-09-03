from __future__ import annotations

from dataclasses import dataclass

import pytest

from scripts.qa.windows_process_scope import (
    ProcessLaunchSpec,
    ProcessScopeClosedError,
    ProcessScopeMember,
    ProcessScopeQueryError,
    ProcessWorkingSetMember,
    _launch_with_adapter,
)


@dataclass
class _FakeRoot:
    pid: int

    def poll(self) -> int | None:
        return None

    def wait(self, timeout: float | None = None) -> int:
        del timeout
        return 0


class _FakePlatformScope:
    def __init__(
        self,
        members: dict[int, str] | None = None,
        *,
        outside_pids: set[int] | None = None,
        query_error: OSError | None = None,
        residual_pids: set[int] | None = None,
        wait_error: OSError | None = None,
        terminate_error: OSError | None = None,
        unavailable_pids: set[int] | None = None,
        working_sets: dict[int, int | OSError] | None = None,
    ) -> None:
        self.root = _FakeRoot(42)
        self.members = members or {42: "host.exe", 7: "child.exe"}
        self.outside_pids = outside_pids or set()
        self.query_error = query_error
        self.residual_pids = residual_pids or set()
        self.termination_requested = False
        self.wait_error = wait_error
        self.terminate_error = terminate_error
        self.unavailable_pids = unavailable_pids or set()
        self.working_sets = working_sets or {pid: pid * 1024 for pid in self.members}
        self.working_set_queries: list[int] = []

    def member_pids(self) -> tuple[int, ...]:
        if self.query_error is not None:
            raise self.query_error
        return tuple(self.members)

    def open_member(self, pid: int):
        if pid in self.unavailable_pids:
            return None
        name = self.members.get(pid)
        return None if name is None else _FakeMemberHandle(self, pid, name)

    def open_member_for_working_set(self, pid: int):
        return self.open_member(pid)

    def terminate_all(self, exit_code: int) -> None:
        del exit_code
        if self.terminate_error is not None:
            raise self.terminate_error
        self.termination_requested = True
        self.members = {
            pid: name for pid, name in self.members.items() if pid in self.residual_pids
        }

    def wait_until_empty(self, timeout: float) -> tuple[int, ...]:
        del timeout
        if self.wait_error is not None:
            raise self.wait_error
        return tuple(self.members)

    def close(self) -> None:
        pass


@dataclass
class _FakeMemberHandle:
    scope: _FakePlatformScope
    pid: int
    name: str
    terminated: bool = False

    def belongs_to_scope(self) -> bool:
        return self.pid not in self.scope.outside_pids

    def working_set_bytes(self) -> int:
        self.scope.working_set_queries.append(self.pid)
        value = self.scope.working_sets[self.pid]
        if isinstance(value, OSError):
            raise value
        return value

    def terminate(self, exit_code: int) -> None:
        del exit_code
        self.terminated = True
        self.scope.members.pop(self.pid, None)

    def wait(self, timeout: float) -> bool:
        del timeout
        return self.terminated

    def close(self) -> None:
        pass


class _FakeAdapter:
    def __init__(self, platform_scope: _FakePlatformScope | None = None) -> None:
        self.platform_scope = platform_scope or _FakePlatformScope()

    def launch(self, spec: ProcessLaunchSpec) -> _FakePlatformScope:
        del spec
        return self.platform_scope


def test_launch_returns_root_and_kernel_owned_membership() -> None:
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe", "--test-mode")),
        _FakeAdapter(),
    )

    assert scope.root.pid == 42
    assert [member.pid for member in scope.snapshot().members] == [42, 7]
    assert [
        (member.executable_name, member.identity_verified) for member in scope.snapshot().members
    ] == [
        ("host.exe", True),
        ("child.exe", True),
    ]


def test_snapshot_keeps_an_unavailable_member_with_unknown_identity() -> None:
    platform = _FakePlatformScope(
        {42: "host.exe", 7: "child.exe"},
        unavailable_pids={7},
    )
    scope = _launch_with_adapter(ProcessLaunchSpec(("host.exe",)), _FakeAdapter(platform))

    assert scope.snapshot().members == (
        ProcessScopeMember(42, "host.exe", True),
        ProcessScopeMember(7),
    )


def test_working_set_snapshot_reads_memory_only_from_verified_member_handles() -> None:
    platform = _FakePlatformScope(
        {42: "host.exe", 7: "child.exe", 8: "outside.exe"},
        outside_pids={8},
        working_sets={42: 4096, 7: 2048, 8: 8192},
    )
    scope = _launch_with_adapter(ProcessLaunchSpec(("host.exe",)), _FakeAdapter(platform))

    assert scope.working_set_snapshot().members == (
        ProcessWorkingSetMember(42, "host.exe", True, 4096),
        ProcessWorkingSetMember(7, "child.exe", True, 2048),
        ProcessWorkingSetMember(8),
    )
    assert platform.working_set_queries == [42, 7]


def test_disappeared_member_is_unverified_and_is_not_a_termination_target() -> None:
    platform = _FakePlatformScope(
        {42: "host.exe", 7: "child.exe"},
        unavailable_pids={7},
    )
    with _launch_with_adapter(ProcessLaunchSpec(("host.exe",)), _FakeAdapter(platform)) as scope:
        assert scope.snapshot().members[-1] == ProcessScopeMember(7)
        assert scope.working_set_snapshot().members[-1] == ProcessWorkingSetMember(7)
        result = scope.terminate_unique("child.exe")
        assert result.status == "not_found"
        assert result.terminated_pid is None
        assert platform.members == {42: "host.exe", 7: "child.exe"}


def test_working_set_snapshot_preserves_verified_identity_when_memory_query_fails() -> None:
    platform = _FakePlatformScope(
        {42: "host.exe", 7: "child.exe"},
        working_sets={42: 4096, 7: OSError("memory query denied")},
    )
    scope = _launch_with_adapter(ProcessLaunchSpec(("host.exe",)), _FakeAdapter(platform))

    assert scope.working_set_snapshot().members == (
        ProcessWorkingSetMember(42, "host.exe", True, 4096),
        ProcessWorkingSetMember(7, "child.exe", True, None),
    )


def test_wait_empty_returns_structured_timeout_and_query_error() -> None:
    residual = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(_FakePlatformScope({7: "child.exe"})),
    ).wait_empty(timeout=0)
    failed = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(_FakePlatformScope(wait_error=OSError("query denied"))),
    ).wait_empty(timeout=0)

    assert residual.success is False
    assert residual.remaining_pids == (7,)
    assert "deadline" in residual.errors[0]
    assert failed.remaining_pids is None
    assert failed.errors == ("unable to observe Job becoming empty: query denied",)


def test_terminate_unique_fails_closed_when_target_is_absent() -> None:
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(),
    )

    result = scope.terminate_unique("vibetable-pb.exe")

    assert result.status == "not_found"
    assert result.terminated_pid is None


def test_terminate_unique_stops_the_only_kernel_owned_target() -> None:
    platform = _FakePlatformScope({42: "host.exe", 7: "vibetable-pb.exe"})
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(platform),
    )

    result = scope.terminate_unique("VIBETABLE-PB.EXE")

    assert result.status == "terminated"
    assert result.terminated_pid == 7
    assert [member.pid for member in scope.snapshot().members] == [42]


def test_terminate_unique_never_guesses_between_multiple_targets() -> None:
    platform = _FakePlatformScope({42: "host.exe", 7: "vibetable-pb.exe", 8: "vibetable-pb.exe"})
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(platform),
    )

    result = scope.terminate_unique("vibetable-pb.exe")

    assert result.status == "ambiguous"
    assert result.matched_pids == (7, 8)
    assert [member.pid for member in scope.snapshot().members] == [42, 7, 8]


def test_terminate_unique_rechecks_job_membership_on_the_open_handle() -> None:
    platform = _FakePlatformScope(
        {42: "host.exe", 7: "vibetable-pb.exe"},
        outside_pids={7},
    )
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(platform),
    )

    result = scope.terminate_unique("vibetable-pb.exe")

    assert result.status == "not_found"
    assert result.unverified_pids == (7,)
    assert 7 in platform.members


def test_snapshot_fails_closed_when_kernel_membership_cannot_be_queried() -> None:
    platform = _FakePlatformScope(query_error=OSError("job query denied"))
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(platform),
    )

    with pytest.raises(ProcessScopeQueryError, match="job query denied"):
        scope.snapshot()


def test_terminate_unique_reports_query_failure_without_selecting_a_pid() -> None:
    platform = _FakePlatformScope(query_error=OSError("job query denied"))
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(platform),
    )

    result = scope.terminate_unique("vibetable-pb.exe")

    assert result.status == "failed"
    assert result.terminated_pid is None
    assert result.errors == ("unable to query Job membership: job query denied",)


def test_terminate_all_reports_residual_members_instead_of_claiming_success() -> None:
    platform = _FakePlatformScope(
        {42: "host.exe", 7: "child.exe"},
        residual_pids={7},
    )
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(platform),
    )

    result = scope.terminate_all()

    assert result.success is False
    assert result.termination_requested is True
    assert result.remaining_pids == (7,)


def test_terminate_all_reports_unknown_membership_when_verification_fails() -> None:
    platform = _FakePlatformScope(wait_error=OSError("job query denied"))
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(platform),
    )

    result = scope.terminate_all()

    assert result.success is False
    assert result.termination_requested is True
    assert result.remaining_pids is None
    assert result.errors == ("unable to verify Job termination: job query denied",)


def test_terminate_all_preserves_the_job_termination_failure() -> None:
    platform = _FakePlatformScope(terminate_error=OSError("TerminateJobObject denied"))
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(platform),
    )

    result = scope.terminate_all()

    assert result.success is False
    assert result.termination_requested is False
    assert result.remaining_pids is None
    assert result.errors == ("unable to terminate Job: TerminateJobObject denied",)


@pytest.mark.parametrize(
    "operation",
    [
        lambda scope: scope.root.poll(),
        lambda scope: scope.root.wait(),
        lambda scope: scope.snapshot(),
        lambda scope: scope.terminate_unique("vibetable-pb.exe"),
        lambda scope: scope.terminate_all(),
        lambda scope: scope.wait_empty(),
    ],
    ids=["root-poll", "root-wait", "snapshot", "terminate-unique", "terminate-all", "wait-empty"],
)
def test_closed_scope_rejects_every_public_process_operation(operation) -> None:
    scope = _launch_with_adapter(
        ProcessLaunchSpec(("host.exe",)),
        _FakeAdapter(),
    )
    scope.close()

    with pytest.raises(ProcessScopeClosedError, match="closed"):
        operation(scope)

    scope.close()


def test_external_seam_does_not_expose_construction_or_adapter_injection() -> None:
    constructor = __import__("scripts.qa.windows_process_scope", fromlist=["x"]).WindowsProcessScope
    with pytest.raises(TypeError):
        constructor(_FakePlatformScope())

    launch = constructor.launch
    with pytest.raises(TypeError, match="adapter"):
        launch(
            ProcessLaunchSpec(("host.exe",)),
            adapter=_FakeAdapter(),
        )
