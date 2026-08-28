"""Race-free Windows process ownership for packaged-host QA launches.

The public module owns one kernel Job Object generation. Callers never infer
ownership from parent PIDs and never terminate a process by an unverified PID.
"""

from __future__ import annotations

import sys
from _thread import LockType
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from dataclasses import dataclass
from os import PathLike
from threading import Lock
from typing import IO, Protocol
from weakref import ref

__all__ = [
    "ProcessLaunchSpec",
    "ProcessScopeClosedError",
    "ProcessScopeLaunchError",
    "ProcessScopeMember",
    "ProcessScopeQueryError",
    "ProcessScopeSnapshot",
    "ScopeTerminationResult",
    "ScopeWaitResult",
    "TargetTerminationResult",
    "WindowsProcessScope",
]


class ProcessScopeQueryError(RuntimeError):
    """Kernel Job membership could not be proven."""


class ProcessScopeLaunchError(RuntimeError):
    """The process was not atomically launched into a new Job scope."""


class ProcessScopeClosedError(RuntimeError):
    """An operation was attempted after the process scope started closing."""


@dataclass(frozen=True)
class ProcessLaunchSpec:
    """Inputs whose subprocess semantics must survive the Win32 adapter."""

    command: tuple[str, ...]
    cwd: str | PathLike[str] | None = None
    env: Mapping[str, str] | None = None
    stdin: IO[bytes] | int | None = None
    stdout: IO[bytes] | int | None = None
    stderr: IO[bytes] | int | None = None

    def __init__(
        self,
        command: Sequence[str],
        *,
        cwd: str | PathLike[str] | None = None,
        env: Mapping[str, str] | None = None,
        stdin: IO[bytes] | int | None = None,
        stdout: IO[bytes] | int | None = None,
        stderr: IO[bytes] | int | None = None,
    ) -> None:
        normalized = tuple(command)
        if not normalized or any(not item or "\0" in item for item in normalized):
            raise ValueError("command must contain non-empty, NUL-free arguments")
        object.__setattr__(self, "command", normalized)
        object.__setattr__(self, "cwd", cwd)
        object.__setattr__(self, "env", env)
        object.__setattr__(self, "stdin", stdin)
        object.__setattr__(self, "stdout", stdout)
        object.__setattr__(self, "stderr", stderr)


@dataclass(frozen=True)
class ProcessScopeMember:
    pid: int
    executable_name: str = "unknown"
    identity_verified: bool = False


@dataclass(frozen=True)
class ProcessScopeSnapshot:
    members: tuple[ProcessScopeMember, ...]


@dataclass(frozen=True)
class TargetTerminationResult:
    status: str
    terminated_pid: int | None = None
    matched_pids: tuple[int, ...] = ()
    unverified_pids: tuple[int, ...] = ()
    errors: tuple[str, ...] = ()


@dataclass(frozen=True)
class ScopeTerminationResult:
    termination_requested: bool
    remaining_pids: tuple[int, ...] | None = None
    errors: tuple[str, ...] = ()

    @property
    def success(self) -> bool:
        return self.termination_requested and self.remaining_pids == () and not self.errors


@dataclass(frozen=True)
class ScopeWaitResult:
    remaining_pids: tuple[int, ...] | None
    errors: tuple[str, ...] = ()

    @property
    def success(self) -> bool:
        return self.remaining_pids == () and not self.errors


class _RootProcess(Protocol):
    pid: int

    def poll(self) -> int | None: ...

    def wait(self, timeout: float | None = None) -> int: ...


class _PlatformMemberHandle(Protocol):
    pid: int
    name: str

    def belongs_to_scope(self) -> bool: ...

    def terminate(self, exit_code: int) -> None: ...

    def wait(self, timeout: float) -> bool: ...

    def close(self) -> None: ...


class _PlatformProcessScope(Protocol):
    @property
    def root(self) -> _RootProcess: ...

    def member_pids(self) -> tuple[int, ...]: ...

    def open_member(self, pid: int) -> _PlatformMemberHandle | None: ...

    def terminate_all(self, exit_code: int) -> None: ...

    def wait_until_empty(self, timeout: float) -> tuple[int, ...]: ...

    def close(self) -> None: ...


class _ProcessScopeAdapter(Protocol):
    def launch(self, spec: ProcessLaunchSpec) -> _PlatformProcessScope: ...


class _ScopedRootProcess:
    def __init__(self, owner: WindowsProcessScope, root: _RootProcess) -> None:
        self._owner_reference = ref(owner)
        self._root = root
        self.pid = root.pid

    def _owner(self) -> WindowsProcessScope:
        owner = self._owner_reference()
        if owner is None:
            raise ProcessScopeClosedError("Windows process scope is closed")
        return owner

    def poll(self) -> int | None:
        with self._owner()._operation():
            return self._root.poll()

    def wait(self, timeout: float | None = None) -> int:
        with self._owner()._operation():
            return self._root.wait(timeout)


class WindowsProcessScope:
    """Deep module for one atomically Job-owned host launch."""

    _platform_scope: _PlatformProcessScope
    _closed: bool
    _operation_lock: LockType
    root: _ScopedRootProcess

    def __init__(self) -> None:
        raise TypeError("WindowsProcessScope cannot be constructed directly; use launch(spec)")

    @classmethod
    def _create(cls, platform_scope: _PlatformProcessScope) -> WindowsProcessScope:
        scope = object.__new__(cls)
        scope._platform_scope = platform_scope
        scope._closed = False
        scope._operation_lock = Lock()
        scope.root = _ScopedRootProcess(scope, platform_scope.root)
        return scope

    def _require_open(self) -> None:
        if self._closed:
            raise ProcessScopeClosedError("Windows process scope is closed")

    @contextmanager
    def _operation(self) -> Iterator[None]:
        with self._operation_lock:
            self._require_open()
            yield

    @classmethod
    def launch(
        cls,
        spec: ProcessLaunchSpec,
    ) -> WindowsProcessScope:
        if sys.platform != "win32":
            raise ProcessScopeLaunchError("Windows Job process scopes require Windows")
        from ._windows_process_scope_win32 import _Win32ProcessScopeAdapter

        return _launch_with_adapter(spec, _Win32ProcessScopeAdapter())

    def snapshot(self) -> ProcessScopeSnapshot:
        with self._operation():
            return self._snapshot()

    def _snapshot(self) -> ProcessScopeSnapshot:
        try:
            pids = self._platform_scope.member_pids()
        except OSError as exc:
            raise ProcessScopeQueryError(f"unable to query Job membership: {exc}") from exc
        members: list[ProcessScopeMember] = []
        for pid in pids:
            handle: _PlatformMemberHandle | None = None
            try:
                handle = self._platform_scope.open_member(pid)
                if handle is None or not handle.belongs_to_scope():
                    members.append(ProcessScopeMember(pid))
                else:
                    members.append(ProcessScopeMember(pid, handle.name, True))
            except OSError:
                members.append(ProcessScopeMember(pid))
            finally:
                if handle is not None:
                    handle.close()
        return ProcessScopeSnapshot(tuple(members))

    def wait_empty(self, *, timeout: float = 5.0) -> ScopeWaitResult:
        with self._operation():
            try:
                remaining = self._platform_scope.wait_until_empty(timeout)
            except OSError as exc:
                return ScopeWaitResult(
                    None,
                    errors=(f"unable to observe Job becoming empty: {exc}",),
                )
            errors = (
                (f"Job still contains processes after the wait deadline: {remaining}",)
                if remaining
                else ()
            )
            return ScopeWaitResult(remaining, errors)

    def terminate_unique(
        self,
        executable_name: str,
        *,
        exit_code: int = 1,
        timeout: float = 15.0,
    ) -> TargetTerminationResult:
        with self._operation():
            return self._terminate_unique(executable_name, exit_code=exit_code, timeout=timeout)

    def _terminate_unique(
        self,
        executable_name: str,
        *,
        exit_code: int,
        timeout: float,
    ) -> TargetTerminationResult:
        expected = executable_name.casefold()
        matches: list[_PlatformMemberHandle] = []
        unverified: list[int] = []
        try:
            pids = self._platform_scope.member_pids()
        except OSError as exc:
            return TargetTerminationResult(
                "failed",
                errors=(f"unable to query Job membership: {exc}",),
            )
        try:
            for pid in pids:
                handle = self._platform_scope.open_member(pid)
                if handle is None:
                    continue
                try:
                    if not handle.belongs_to_scope():
                        unverified.append(pid)
                        continue
                    if handle.name.casefold() != expected:
                        continue
                    matches.append(handle)
                    handle = None
                finally:
                    if handle is not None:
                        handle.close()
        except OSError as exc:
            matched_pids = tuple(handle.pid for handle in matches)
            for handle in matches:
                handle.close()
            return TargetTerminationResult(
                "failed",
                matched_pids=matched_pids,
                unverified_pids=tuple(unverified),
                errors=(f"unable to verify a Job member: {exc}",),
            )
        if not matches:
            return TargetTerminationResult("not_found", unverified_pids=tuple(unverified))
        if len(matches) > 1:
            matched_pids = tuple(handle.pid for handle in matches)
            for handle in matches:
                handle.close()
            return TargetTerminationResult("ambiguous", matched_pids=matched_pids)
        target = matches[0]
        try:
            try:
                target.terminate(exit_code)
                exited = target.wait(timeout)
            except OSError as exc:
                return TargetTerminationResult(
                    "failed",
                    matched_pids=(target.pid,),
                    errors=(f"unable to terminate process {target.pid}: {exc}",),
                )
            if not exited:
                return TargetTerminationResult(
                    "failed",
                    matched_pids=(target.pid,),
                    errors=(f"process {target.pid} did not exit before the deadline",),
                )
            return TargetTerminationResult(
                "terminated",
                terminated_pid=target.pid,
                matched_pids=(target.pid,),
            )
        finally:
            target.close()

    def terminate_all(
        self,
        *,
        exit_code: int = 1,
        timeout: float = 15.0,
    ) -> ScopeTerminationResult:
        with self._operation():
            return self._terminate_all(exit_code=exit_code, timeout=timeout)

    def _terminate_all(
        self,
        *,
        exit_code: int,
        timeout: float,
    ) -> ScopeTerminationResult:
        try:
            self._platform_scope.terminate_all(exit_code)
        except OSError as exc:
            return ScopeTerminationResult(
                False,
                errors=(f"unable to terminate Job: {exc}",),
            )
        try:
            remaining = self._platform_scope.wait_until_empty(timeout)
        except OSError as exc:
            return ScopeTerminationResult(
                True,
                remaining_pids=None,
                errors=(f"unable to verify Job termination: {exc}",),
            )
        errors = (
            (f"Job still contains processes after termination: {remaining}",) if remaining else ()
        )
        return ScopeTerminationResult(True, remaining_pids=remaining, errors=errors)

    def close(self) -> None:
        with self._operation_lock:
            self._closed = True
            self._platform_scope.close()

    def __enter__(self) -> WindowsProcessScope:
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()


def _launch_with_adapter(
    spec: ProcessLaunchSpec,
    adapter: _ProcessScopeAdapter,
) -> WindowsProcessScope:
    try:
        platform_scope = adapter.launch(spec)
    except OSError as exc:
        raise ProcessScopeLaunchError(
            f"unable to launch an atomic Job process scope: {exc}"
        ) from exc
    return WindowsProcessScope._create(platform_scope)
