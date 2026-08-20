from __future__ import annotations

import ctypes
import json
import os
import sys
import uuid
from ctypes import wintypes
from pathlib import Path

import pytest

from tests.e2e.windows_process_scope import (
    ProcessLaunchSpec,
    ProcessScopeClosedError,
    ProcessScopeLaunchError,
    WindowsProcessScope,
    _launch_with_adapter,
)

pytestmark = pytest.mark.skipif(sys.platform != "win32", reason="Windows Job Object contract")

FIXTURE = "tests.e2e.windows_process_scope_fixture"
SYNCHRONIZE = 0x00100000
WAIT_OBJECT_0 = 0
WAIT_TIMEOUT = 258


class _NamedEvent:
    def __init__(self) -> None:
        self.name = f"Local\\VibeTableProcessScope-{uuid.uuid4()}"
        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel32.CreateEventW.argtypes = [
            wintypes.LPVOID,
            wintypes.BOOL,
            wintypes.BOOL,
            wintypes.LPCWSTR,
        ]
        kernel32.CreateEventW.restype = wintypes.HANDLE
        handle = kernel32.CreateEventW(None, True, False, self.name)
        if not handle:
            raise ctypes.WinError(ctypes.get_last_error(), "CreateEventW")
        self._handle = int(handle)

    def set(self) -> None:
        if not ctypes.windll.kernel32.SetEvent(self._handle):
            raise ctypes.WinError(ctypes.get_last_error(), "SetEvent")

    def wait(self, timeout_seconds: float = 15.0) -> None:
        result = int(
            ctypes.windll.kernel32.WaitForSingleObject(
                self._handle,
                int(timeout_seconds * 1000),
            )
        )
        if result != 0:
            raise TimeoutError(f"event {self.name} was not signaled: wait={result}")

    def close(self) -> None:
        if self._handle:
            ctypes.windll.kernel32.CloseHandle(self._handle)
            self._handle = 0

    def __enter__(self) -> _NamedEvent:
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()


def _read_pid(path: Path) -> int:
    return int(path.read_text(encoding="utf-8"))


def _job_name() -> str:
    return f"Local\\VibeTableProcessScopeJob-{uuid.uuid4()}"


def _launch_named_process_scope(
    spec: ProcessLaunchSpec,
    job_name: str,
) -> WindowsProcessScope:
    from tests.e2e._windows_process_scope_win32 import _Win32ProcessScopeAdapter

    return _launch_with_adapter(spec, _Win32ProcessScopeAdapter(job_name=job_name))


def _open_process_for_wait(pid: int) -> tuple[ctypes.WinDLL, int]:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    handle = kernel32.OpenProcess(SYNCHRONIZE, False, pid)
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error(), f"OpenProcess({pid})")
    return kernel32, int(handle)


def _current_process_handle_count() -> int:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.GetCurrentProcess.argtypes = []
    kernel32.GetCurrentProcess.restype = wintypes.HANDLE
    kernel32.GetProcessHandleCount.argtypes = [wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD)]
    kernel32.GetProcessHandleCount.restype = wintypes.BOOL
    count = wintypes.DWORD()
    if not kernel32.GetProcessHandleCount(kernel32.GetCurrentProcess(), ctypes.byref(count)):
        raise ctypes.WinError(ctypes.get_last_error(), "GetProcessHandleCount")
    return int(count.value)


def _launch_and_close_batch(size: int) -> None:
    for _index in range(size):
        with WindowsProcessScope.launch(ProcessLaunchSpec([sys.executable, "-c", "pass"])) as scope:
            assert scope.root.wait(timeout=15) == 0


def test_atomic_job_keeps_child_owned_after_root_exit_and_terminates_it(
    tmp_path: Path,
) -> None:
    child_pid_path = tmp_path / "child.pid"
    job_name = _job_name()
    with (
        _NamedEvent() as ready,
        _NamedEvent() as root_release,
        _NamedEvent() as child_release,
        _launch_named_process_scope(
            ProcessLaunchSpec(
                [
                    sys.executable,
                    "-m",
                    FIXTURE,
                    job_name,
                    "root",
                    ready.name,
                    root_release.name,
                    child_release.name,
                    str(child_pid_path),
                ]
            ),
            job_name,
        ) as scope,
    ):
        ready.wait()
        child_pid = _read_pid(child_pid_path)
        initial = {member.pid for member in scope.snapshot().members}
        assert scope.root.pid in initial
        assert child_pid in initial

        root_release.set()
        assert scope.root.wait(timeout=15) == 0
        after_root = {member.pid for member in scope.snapshot().members}
        assert scope.root.pid not in after_root
        assert child_pid in after_root

        result = scope.terminate_all()
        assert result.success is True
        assert scope.snapshot().members == ()


def test_process_already_in_outer_job_can_launch_an_inner_job(tmp_path: Path) -> None:
    child_pid_path = tmp_path / "nested-child.pid"
    outer_job_name = _job_name()
    inner_job_name = _job_name()
    with (
        _NamedEvent() as child_ready,
        _NamedEvent() as root_release,
        _NamedEvent() as child_release,
        _launch_named_process_scope(
            ProcessLaunchSpec(
                [
                    sys.executable,
                    "-m",
                    FIXTURE,
                    outer_job_name,
                    "nested-root",
                    child_ready.name,
                    root_release.name,
                    child_release.name,
                    str(child_pid_path),
                    inner_job_name,
                ]
            ),
            outer_job_name,
        ) as outer,
    ):
        child_wait: tuple[ctypes.WinDLL, int] | None = None
        try:
            child_ready.wait()
            child_pid = _read_pid(child_pid_path)
            child_wait = _open_process_for_wait(child_pid)
            kernel32, child_handle = child_wait
            outer_pids = {member.pid for member in outer.snapshot().members}
            assert outer.root.pid in outer_pids
            assert child_pid in outer_pids
            assert kernel32.WaitForSingleObject(child_handle, 0) == WAIT_TIMEOUT

            root_release.set()
            assert outer.root.wait(timeout=15) == 0
            assert kernel32.WaitForSingleObject(child_handle, 15_000) == WAIT_OBJECT_0
            assert outer.snapshot().members == ()
        finally:
            root_release.set()
            child_release.set()
            if child_wait is not None:
                kernel32, child_handle = child_wait
                kernel32.WaitForSingleObject(child_handle, 15_000)
                kernel32.CloseHandle(child_handle)


def test_launch_preserves_command_environment_cwd_and_standard_handles(tmp_path: Path) -> None:
    cwd = tmp_path / "working directory"
    cwd.mkdir()
    stdin_path = tmp_path / "stdin.bin"
    stdout_path = tmp_path / "stdout.json"
    stderr_path = tmp_path / "stderr.txt"
    stdin_path.write_bytes(b"input bytes")
    argument = 'space and "quotes" with trailing\\'
    environment = dict(os.environ)
    environment["VIBETABLE_SCOPE_VALUE"] = "值 with spaces"
    code = (
        "import json, os, sys; "
        "print(json.dumps({"
        "'cwd': os.getcwd(), "
        "'env': os.environ['VIBETABLE_SCOPE_VALUE'], "
        "'arg': sys.argv[1], "
        "'stdin': sys.stdin.buffer.read().decode('ascii')"
        "})); "
        "print('stderr-line', file=sys.stderr)"
    )

    with (
        stdin_path.open("rb") as stdin,
        stdout_path.open("wb") as stdout,
        stderr_path.open("wb") as stderr,
        WindowsProcessScope.launch(
            ProcessLaunchSpec(
                [sys.executable, "-c", code, argument],
                cwd=cwd,
                env=environment,
                stdin=stdin,
                stdout=stdout,
                stderr=stderr,
            )
        ) as scope,
    ):
        assert scope.root.wait(timeout=15) == 0

    payload = json.loads(stdout_path.read_text(encoding="utf-8"))
    assert payload == {
        "cwd": str(cwd),
        "env": "值 with spaces",
        "arg": argument,
        "stdin": "input bytes",
    }
    assert stderr_path.read_text(encoding="utf-8") == "stderr-line\n"


def test_launch_failure_does_not_run_an_application(tmp_path: Path) -> None:
    marker = tmp_path / "must-not-exist"
    missing_executable = tmp_path / "missing.exe"

    with pytest.raises(ProcessScopeLaunchError, match="CreateProcessW"):
        WindowsProcessScope.launch(
            ProcessLaunchSpec([str(missing_executable), "--write-marker", str(marker)])
        )

    assert not marker.exists()


def test_close_kills_members_without_leaking_the_job_handle(tmp_path: Path) -> None:
    pid_path = tmp_path / "root.pid"
    job_name = _job_name()
    with _NamedEvent() as ready, _NamedEvent() as release:
        scope = _launch_named_process_scope(
            ProcessLaunchSpec(
                [
                    sys.executable,
                    "-m",
                    FIXTURE,
                    job_name,
                    "child",
                    ready.name,
                    release.name,
                    str(pid_path),
                ]
            ),
            job_name,
        )
        process_wait: tuple[ctypes.WinDLL, int] | None = None
        try:
            ready.wait()
            process_wait = _open_process_for_wait(scope.root.pid)
            kernel32, process_handle = process_wait
            scope.close()
            assert kernel32.WaitForSingleObject(process_handle, 15_000) == WAIT_OBJECT_0
            closed_operations = (
                scope.root.poll,
                scope.root.wait,
                scope.snapshot,
                lambda: scope.terminate_unique("definitely-not-a-real-process.exe"),
                scope.terminate_all,
            )
            for operation in closed_operations:
                with pytest.raises(ProcessScopeClosedError, match="closed"):
                    operation()
        finally:
            release.set()
            scope.close()
            if process_wait is not None:
                kernel32, process_handle = process_wait
                kernel32.WaitForSingleObject(process_handle, 15_000)
                kernel32.CloseHandle(process_handle)


def test_handle_keeps_ownership_when_close_handle_fails() -> None:
    from tests.e2e._windows_process_scope_win32 import _OwnedHandle

    close_results = iter((False, True))

    def close_handle(_handle: int) -> bool:
        result = next(close_results)
        if not result:
            ctypes.set_last_error(5)
        return result

    handle = _OwnedHandle(123, close_handle=close_handle)

    with pytest.raises(OSError, match="CloseHandle"):
        handle.close()
    assert handle.value == 123

    handle.close()
    assert handle.value == 0


def test_scope_close_attempts_every_owned_handle_and_retries_failures() -> None:
    from tests.e2e._windows_process_scope_win32 import _OwnedHandle, _Win32PlatformScope

    root_results = iter((False, True))

    def close_root(_handle: int) -> bool:
        result = next(root_results)
        if not result:
            ctypes.set_last_error(5)
        return result

    root = _OwnedHandle(123, close_handle=close_root)
    job = _OwnedHandle(456, close_handle=lambda _handle: True)
    platform_scope = _Win32PlatformScope(job, root, 42, ("host.exe",))

    with pytest.raises(OSError, match=r"CloseHandle\(123\)"):
        platform_scope.close()
    assert root.value == 123
    assert job.value == 0

    platform_scope.close()
    assert root.value == 0


def test_repeated_launch_and_close_does_not_leak_parent_handles() -> None:
    _launch_and_close_batch(3)
    baseline = _current_process_handle_count()

    _launch_and_close_batch(16)
    after_first_batch = _current_process_handle_count()
    _launch_and_close_batch(16)
    after_second_batch = _current_process_handle_count()

    assert after_first_batch == baseline
    assert after_second_batch == baseline


def test_job_member_query_expands_the_pid_buffer_to_the_reported_size() -> None:
    from tests.e2e._windows_process_scope_win32 import _query_job_member_pids

    expected = tuple(range(100, 117))
    capacities: list[int] = []
    header_size = ctypes.sizeof(wintypes.DWORD) * 2
    pointer_size = ctypes.sizeof(ctypes.c_size_t)

    def query_job(
        _job: int,
        _information_class: int,
        buffer: ctypes.Array[ctypes.c_char],
        size: int,
        _return_length: object,
    ) -> bool:
        capacity = (size - header_size) // pointer_size
        capacities.append(capacity)
        returned = min(capacity, len(expected))
        wintypes.DWORD.from_buffer(buffer, 0).value = len(expected)
        wintypes.DWORD.from_buffer(buffer, ctypes.sizeof(wintypes.DWORD)).value = returned
        pids = (ctypes.c_size_t * returned).from_buffer(buffer, header_size)
        for index, pid in enumerate(expected[:returned]):
            pids[index] = pid
        return True

    assert _query_job_member_pids(456, query_job=query_job) == expected
    assert capacities == [16, 17]


def test_attribute_list_sizing_reports_the_unexpected_win32_error() -> None:
    from tests.e2e._windows_process_scope_win32 import _initialize_attribute_list

    def fail_sizing(
        _attributes: object,
        _count: int,
        _flags: int,
        _size_pointer: object,
    ) -> bool:
        ctypes.set_last_error(5)
        return False

    with pytest.raises(OSError, match="attribute-list sizing") as failure:
        _initialize_attribute_list(2, initialize=fail_sizing)

    assert failure.value.winerror == 5
