"""Private Win32 adapter for :mod:`scripts.qa.windows_process_scope`."""

from __future__ import annotations

import ctypes
import math
import msvcrt
import os
import subprocess
import time
from collections.abc import Callable, Sequence
from ctypes import wintypes
from pathlib import PureWindowsPath
from typing import IO, Protocol

from ._windows_tcp_table import WindowsTcpRow, query_windows_tcp_table
from .windows_process_scope import ProcessLaunchSpec

CREATE_UNICODE_ENVIRONMENT = 0x00000400
EXTENDED_STARTUPINFO_PRESENT = 0x00080000
STARTF_USESTDHANDLES = 0x00000100

JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x00002000
JOB_OBJECT_BASIC_PROCESS_ID_LIST = 3
JOB_OBJECT_EXTENDED_LIMIT_INFORMATION = 9
PROC_THREAD_ATTRIBUTE_HANDLE_LIST = 0x00020002
PROC_THREAD_ATTRIBUTE_JOB_LIST = 0x0002000D

PROCESS_TERMINATE = 0x0001
PROCESS_VM_READ = 0x0010
PROCESS_QUERY_INFORMATION = 0x0400
PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
SYNCHRONIZE = 0x00100000
DUPLICATE_SAME_ACCESS = 0x00000002
HANDLE_FLAG_INHERIT = 0x00000001

GENERIC_READ = 0x80000000
GENERIC_WRITE = 0x40000000
FILE_SHARE_READ = 0x00000001
FILE_SHARE_WRITE = 0x00000002
OPEN_EXISTING = 3
FILE_ATTRIBUTE_NORMAL = 0x00000080

STD_INPUT_HANDLE = -10
STD_OUTPUT_HANDLE = -11
STD_ERROR_HANDLE = -12
WAIT_OBJECT_0 = 0
WAIT_TIMEOUT = 258
INFINITE = 0xFFFFFFFF
ERROR_INVALID_PARAMETER = 87
ERROR_INSUFFICIENT_BUFFER = 122
INVALID_HANDLE_VALUE = ctypes.c_void_p(-1).value


class STARTUPINFOW(ctypes.Structure):
    _fields_ = [
        ("cb", wintypes.DWORD),
        ("lpReserved", wintypes.LPWSTR),
        ("lpDesktop", wintypes.LPWSTR),
        ("lpTitle", wintypes.LPWSTR),
        ("dwX", wintypes.DWORD),
        ("dwY", wintypes.DWORD),
        ("dwXSize", wintypes.DWORD),
        ("dwYSize", wintypes.DWORD),
        ("dwXCountChars", wintypes.DWORD),
        ("dwYCountChars", wintypes.DWORD),
        ("dwFillAttribute", wintypes.DWORD),
        ("dwFlags", wintypes.DWORD),
        ("wShowWindow", wintypes.WORD),
        ("cbReserved2", wintypes.WORD),
        ("lpReserved2", ctypes.POINTER(ctypes.c_ubyte)),
        ("hStdInput", wintypes.HANDLE),
        ("hStdOutput", wintypes.HANDLE),
        ("hStdError", wintypes.HANDLE),
    ]


class STARTUPINFOEXW(ctypes.Structure):
    _fields_ = [("StartupInfo", STARTUPINFOW), ("lpAttributeList", wintypes.LPVOID)]


class ProcessInformation(ctypes.Structure):
    _fields_ = [
        ("hProcess", wintypes.HANDLE),
        ("hThread", wintypes.HANDLE),
        ("dwProcessId", wintypes.DWORD),
        ("dwThreadId", wintypes.DWORD),
    ]


class IoCounters(ctypes.Structure):
    _fields_ = [
        ("ReadOperationCount", ctypes.c_ulonglong),
        ("WriteOperationCount", ctypes.c_ulonglong),
        ("OtherOperationCount", ctypes.c_ulonglong),
        ("ReadTransferCount", ctypes.c_ulonglong),
        ("WriteTransferCount", ctypes.c_ulonglong),
        ("OtherTransferCount", ctypes.c_ulonglong),
    ]


class JobObjectBasicLimitInformation(ctypes.Structure):
    _fields_ = [
        ("PerProcessUserTimeLimit", ctypes.c_longlong),
        ("PerJobUserTimeLimit", ctypes.c_longlong),
        ("LimitFlags", wintypes.DWORD),
        ("MinimumWorkingSetSize", ctypes.c_size_t),
        ("MaximumWorkingSetSize", ctypes.c_size_t),
        ("ActiveProcessLimit", wintypes.DWORD),
        ("Affinity", ctypes.c_size_t),
        ("PriorityClass", wintypes.DWORD),
        ("SchedulingClass", wintypes.DWORD),
    ]


class JobObjectExtendedLimitInformation(ctypes.Structure):
    _fields_ = [
        ("BasicLimitInformation", JobObjectBasicLimitInformation),
        ("IoInfo", IoCounters),
        ("ProcessMemoryLimit", ctypes.c_size_t),
        ("JobMemoryLimit", ctypes.c_size_t),
        ("PeakProcessMemoryUsed", ctypes.c_size_t),
        ("PeakJobMemoryUsed", ctypes.c_size_t),
    ]


class ProcessMemoryCounters(ctypes.Structure):
    _fields_ = [
        ("cb", wintypes.DWORD),
        ("PageFaultCount", wintypes.DWORD),
        ("PeakWorkingSetSize", ctypes.c_size_t),
        ("WorkingSetSize", ctypes.c_size_t),
        ("QuotaPeakPagedPoolUsage", ctypes.c_size_t),
        ("QuotaPagedPoolUsage", ctypes.c_size_t),
        ("QuotaPeakNonPagedPoolUsage", ctypes.c_size_t),
        ("QuotaNonPagedPoolUsage", ctypes.c_size_t),
        ("PagefileUsage", ctypes.c_size_t),
        ("PeakPagefileUsage", ctypes.c_size_t),
    ]


kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
kernel32.CreateJobObjectW.argtypes = [wintypes.LPVOID, wintypes.LPCWSTR]
kernel32.CreateJobObjectW.restype = wintypes.HANDLE
kernel32.SetInformationJobObject.argtypes = [
    wintypes.HANDLE,
    ctypes.c_int,
    wintypes.LPVOID,
    wintypes.DWORD,
]
kernel32.SetInformationJobObject.restype = wintypes.BOOL
kernel32.QueryInformationJobObject.argtypes = [
    wintypes.HANDLE,
    ctypes.c_int,
    wintypes.LPVOID,
    wintypes.DWORD,
    ctypes.POINTER(wintypes.DWORD),
]
kernel32.QueryInformationJobObject.restype = wintypes.BOOL
kernel32.TerminateJobObject.argtypes = [wintypes.HANDLE, wintypes.UINT]
kernel32.TerminateJobObject.restype = wintypes.BOOL
kernel32.InitializeProcThreadAttributeList.argtypes = [
    wintypes.LPVOID,
    wintypes.DWORD,
    wintypes.DWORD,
    ctypes.POINTER(ctypes.c_size_t),
]
kernel32.InitializeProcThreadAttributeList.restype = wintypes.BOOL
kernel32.UpdateProcThreadAttribute.argtypes = [
    wintypes.LPVOID,
    wintypes.DWORD,
    ctypes.c_size_t,
    wintypes.LPVOID,
    ctypes.c_size_t,
    wintypes.LPVOID,
    ctypes.POINTER(ctypes.c_size_t),
]
kernel32.UpdateProcThreadAttribute.restype = wintypes.BOOL
kernel32.DeleteProcThreadAttributeList.argtypes = [wintypes.LPVOID]
kernel32.DeleteProcThreadAttributeList.restype = None
kernel32.CreateProcessW.argtypes = [
    wintypes.LPCWSTR,
    wintypes.LPWSTR,
    wintypes.LPVOID,
    wintypes.LPVOID,
    wintypes.BOOL,
    wintypes.DWORD,
    wintypes.LPVOID,
    wintypes.LPCWSTR,
    ctypes.POINTER(STARTUPINFOW),
    ctypes.POINTER(ProcessInformation),
]
kernel32.CreateProcessW.restype = wintypes.BOOL
kernel32.GetCurrentProcess.argtypes = []
kernel32.GetCurrentProcess.restype = wintypes.HANDLE
kernel32.DuplicateHandle.argtypes = [
    wintypes.HANDLE,
    wintypes.HANDLE,
    wintypes.HANDLE,
    ctypes.POINTER(wintypes.HANDLE),
    wintypes.DWORD,
    wintypes.BOOL,
    wintypes.DWORD,
]
kernel32.K32GetProcessMemoryInfo.argtypes = [
    wintypes.HANDLE,
    ctypes.POINTER(ProcessMemoryCounters),
    wintypes.DWORD,
]
kernel32.K32GetProcessMemoryInfo.restype = wintypes.BOOL
kernel32.DuplicateHandle.restype = wintypes.BOOL
kernel32.GetStdHandle.argtypes = [wintypes.DWORD]
kernel32.GetStdHandle.restype = wintypes.HANDLE
kernel32.CreateFileW.argtypes = [
    wintypes.LPCWSTR,
    wintypes.DWORD,
    wintypes.DWORD,
    wintypes.LPVOID,
    wintypes.DWORD,
    wintypes.DWORD,
    wintypes.HANDLE,
]
kernel32.CreateFileW.restype = wintypes.HANDLE
kernel32.SetHandleInformation.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.DWORD]
kernel32.SetHandleInformation.restype = wintypes.BOOL
kernel32.CloseHandle.argtypes = [wintypes.HANDLE]
kernel32.CloseHandle.restype = wintypes.BOOL
kernel32.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
kernel32.WaitForSingleObject.restype = wintypes.DWORD
kernel32.GetExitCodeProcess.argtypes = [wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD)]
kernel32.GetExitCodeProcess.restype = wintypes.BOOL
kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
kernel32.OpenProcess.restype = wintypes.HANDLE
kernel32.IsProcessInJob.argtypes = [
    wintypes.HANDLE,
    wintypes.HANDLE,
    ctypes.POINTER(wintypes.BOOL),
]
kernel32.IsProcessInJob.restype = wintypes.BOOL
kernel32.QueryFullProcessImageNameW.argtypes = [
    wintypes.HANDLE,
    wintypes.DWORD,
    wintypes.LPWSTR,
    ctypes.POINTER(wintypes.DWORD),
]
kernel32.QueryFullProcessImageNameW.restype = wintypes.BOOL
kernel32.TerminateProcess.argtypes = [wintypes.HANDLE, wintypes.UINT]
kernel32.TerminateProcess.restype = wintypes.BOOL


def _check(value: object, action: str) -> None:
    if not value:
        raise ctypes.WinError(ctypes.get_last_error(), action)


def _milliseconds(timeout: float | None) -> int:
    if timeout is None:
        return INFINITE
    if timeout < 0:
        raise ValueError("timeout must be non-negative")
    return min(INFINITE - 1, math.ceil(timeout * 1000))


class _OwnedHandle:
    def __init__(
        self,
        value: int,
        *,
        close_handle: Callable[[int], object] | None = None,
    ) -> None:
        self.value = value
        self._close_handle = close_handle or kernel32.CloseHandle

    def close(self) -> None:
        if self.value:
            _check(self._close_handle(self.value), f"CloseHandle({self.value})")
            self.value = 0


def _close_owned_handles(handles: Sequence[_OwnedHandle]) -> tuple[OSError, ...]:
    errors: list[OSError] = []
    visited: set[int] = set()
    for handle in handles:
        identity = id(handle)
        if identity in visited:
            continue
        visited.add(identity)
        try:
            handle.close()
        except OSError as exc:
            errors.append(exc)
    return tuple(errors)


def _raise_close_errors(errors: Sequence[OSError], context: str) -> None:
    if errors:
        details = "; ".join(str(error) for error in errors)
        raise OSError(f"{context}: {details}")


class _Win32RootProcess:
    def __init__(self, handle: _OwnedHandle, pid: int, command: tuple[str, ...]) -> None:
        self._handle = handle
        self.pid = pid
        self._command = command

    def poll(self) -> int | None:
        result = int(kernel32.WaitForSingleObject(self._handle.value, 0))
        if result == WAIT_TIMEOUT:
            return None
        if result != WAIT_OBJECT_0:
            raise ctypes.WinError(ctypes.get_last_error(), "WaitForSingleObject(root)")
        code = wintypes.DWORD()
        _check(
            kernel32.GetExitCodeProcess(self._handle.value, ctypes.byref(code)),
            "GetExitCodeProcess(root)",
        )
        return int(code.value)

    def wait(self, timeout: float | None = None) -> int:
        result = int(kernel32.WaitForSingleObject(self._handle.value, _milliseconds(timeout)))
        if result == WAIT_TIMEOUT:
            if timeout is None:
                raise RuntimeError("an infinite root-process wait unexpectedly timed out")
            raise subprocess.TimeoutExpired(self._command, timeout)
        if result != WAIT_OBJECT_0:
            raise ctypes.WinError(ctypes.get_last_error(), "WaitForSingleObject(root)")
        code = self.poll()
        if code is None:
            raise RuntimeError("signaled root process has no exit code")
        return code


class _Win32MemberHandle:
    def __init__(self, process: _OwnedHandle, job: _OwnedHandle, pid: int, name: str) -> None:
        self._process = process
        self._job = job
        self.pid = pid
        self.name = name

    def belongs_to_scope(self) -> bool:
        belongs = wintypes.BOOL()
        _check(
            kernel32.IsProcessInJob(
                self._process.value,
                self._job.value,
                ctypes.byref(belongs),
            ),
            "IsProcessInJob(member)",
        )
        return bool(belongs.value)

    def working_set_bytes(self) -> int:
        counters = ProcessMemoryCounters()
        counters.cb = ctypes.sizeof(counters)
        _check(
            kernel32.K32GetProcessMemoryInfo(
                self._process.value,
                ctypes.byref(counters),
                counters.cb,
            ),
            f"K32GetProcessMemoryInfo({self.pid})",
        )
        return int(counters.WorkingSetSize)

    def terminate(self, exit_code: int) -> None:
        if int(kernel32.WaitForSingleObject(self._process.value, 0)) == WAIT_OBJECT_0:
            return
        _check(
            kernel32.TerminateProcess(self._process.value, exit_code), "TerminateProcess(member)"
        )

    def wait(self, timeout: float) -> bool:
        result = int(kernel32.WaitForSingleObject(self._process.value, _milliseconds(timeout)))
        if result == WAIT_TIMEOUT:
            return False
        if result != WAIT_OBJECT_0:
            raise ctypes.WinError(ctypes.get_last_error(), "WaitForSingleObject(member)")
        return True

    def close(self) -> None:
        self._process.close()


class _Win32TcpListenerOwnerHandle:
    def __init__(self, process: _OwnedHandle, pid: int, name: str) -> None:
        self._process = process
        self.pid = pid
        self.name = name

    def wait(self, timeout: float) -> bool:
        result = int(kernel32.WaitForSingleObject(self._process.value, _milliseconds(timeout)))
        if result == WAIT_TIMEOUT:
            return False
        if result != WAIT_OBJECT_0:
            raise ctypes.WinError(ctypes.get_last_error(), "WaitForSingleObject(listener owner)")
        return True

    def close(self) -> None:
        self._process.close()


class _Win32TcpListenerOwnerAdapter:
    """Read TCP rows and open a non-terminating stable owner handle."""

    @staticmethod
    def query_listeners(port: int, *, timeout: float) -> tuple[WindowsTcpRow, ...]:
        return tuple(
            row for row in query_windows_tcp_table(timeout=timeout) if row.is_listener_on(port)
        )

    @staticmethod
    def open_owner(pid: int) -> _Win32TcpListenerOwnerHandle:
        access = PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE
        handle = kernel32.OpenProcess(access, False, pid)
        if not handle:
            raise ctypes.WinError(ctypes.get_last_error(), f"OpenProcess({pid})")
        process = _OwnedHandle(int(handle))
        try:
            capacity = wintypes.DWORD(32768)
            path = ctypes.create_unicode_buffer(capacity.value)
            _check(
                kernel32.QueryFullProcessImageNameW(
                    process.value,
                    0,
                    path,
                    ctypes.byref(capacity),
                ),
                f"QueryFullProcessImageNameW({pid})",
            )
            return _Win32TcpListenerOwnerHandle(
                process,
                pid,
                PureWindowsPath(path.value).name,
            )
        except BaseException:
            process.close()
            raise


class _QueryJobInformation(Protocol):
    def __call__(
        self,
        job: int,
        information_class: int,
        buffer: ctypes.Array[ctypes.c_char],
        size: int,
        return_length: object,
        /,
    ) -> object: ...


def _query_job_member_pids(
    job: int,
    *,
    query_job: _QueryJobInformation | None = None,
) -> tuple[int, ...]:
    operation = query_job or kernel32.QueryInformationJobObject
    capacity = 16
    header_size = ctypes.sizeof(wintypes.DWORD) * 2
    while True:
        size = header_size + capacity * ctypes.sizeof(ctypes.c_size_t)
        buffer = ctypes.create_string_buffer(size)
        _check(
            operation(
                job,
                JOB_OBJECT_BASIC_PROCESS_ID_LIST,
                buffer,
                size,
                None,
            ),
            "QueryInformationJobObject(process ids)",
        )
        assigned = int(wintypes.DWORD.from_buffer(buffer, 0).value)
        returned = int(wintypes.DWORD.from_buffer(buffer, ctypes.sizeof(wintypes.DWORD)).value)
        if returned >= assigned:
            array = (ctypes.c_size_t * returned).from_buffer(buffer, header_size)
            return tuple(sorted(int(pid) for pid in array))
        capacity = max(capacity + 1, assigned)


class _Win32PlatformScope:
    def __init__(
        self,
        job: _OwnedHandle,
        root_handle: _OwnedHandle,
        root_pid: int,
        command: tuple[str, ...],
    ) -> None:
        self._job = job
        self._root_handle = root_handle
        self.root = _Win32RootProcess(root_handle, root_pid, command)

    def member_pids(self) -> tuple[int, ...]:
        return _query_job_member_pids(self._job.value)

    def _open_member(self, pid: int, *, access: int) -> _Win32MemberHandle | None:
        handle = kernel32.OpenProcess(access, False, pid)
        if not handle:
            error = ctypes.get_last_error()
            if error == ERROR_INVALID_PARAMETER:
                return None
            raise ctypes.WinError(error, f"OpenProcess({pid})")
        process = _OwnedHandle(int(handle))
        try:
            capacity = wintypes.DWORD(32768)
            path = ctypes.create_unicode_buffer(capacity.value)
            if not kernel32.QueryFullProcessImageNameW(
                process.value, 0, path, ctypes.byref(capacity)
            ):
                error = ctypes.get_last_error()
                # An enumerated member can exit after OpenProcess succeeds.
                if int(kernel32.WaitForSingleObject(process.value, 0)) == WAIT_OBJECT_0:
                    process.close()
                    return None
                raise ctypes.WinError(error, f"QueryFullProcessImageNameW({pid})")
            name = PureWindowsPath(path.value).name
            return _Win32MemberHandle(process, self._job, pid, name)
        except BaseException:
            process.close()
            raise

    def open_member(self, pid: int) -> _Win32MemberHandle | None:
        access = PROCESS_TERMINATE | PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE
        return self._open_member(pid, access=access)

    def open_member_for_working_set(self, pid: int) -> _Win32MemberHandle | None:
        access = PROCESS_QUERY_INFORMATION | PROCESS_VM_READ | SYNCHRONIZE
        return self._open_member(pid, access=access)

    def terminate_all(self, exit_code: int) -> None:
        _check(kernel32.TerminateJobObject(self._job.value, exit_code), "TerminateJobObject")

    def wait_until_empty(self, timeout: float) -> tuple[int, ...]:
        deadline = time.monotonic() + timeout
        while True:
            pids = self.member_pids()
            if not pids:
                return ()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return pids
            waited = False
            for pid in pids:
                handle = self.open_member(pid)
                if handle is None:
                    continue
                try:
                    if not handle.belongs_to_scope():
                        continue
                    handle.wait(remaining)
                    waited = True
                    break
                finally:
                    handle.close()
            if not waited:
                # Every queried PID disappeared or was reused outside the Job.
                # Re-query immediately; no unverified process is ever waited on.
                continue

    def close(self) -> None:
        errors = _close_owned_handles((self._root_handle, self._job))
        _raise_close_errors(errors, "unable to close owned process-scope handles")


def _environment_block(env: dict[str, str] | None) -> ctypes.Array[ctypes.c_wchar] | None:
    if env is None:
        return None
    entries: list[str] = []
    for key, value in sorted(env.items(), key=lambda item: item[0].casefold()):
        if not isinstance(key, str) or not isinstance(value, str):
            raise TypeError("environment names and values must be strings")
        if not key or "\0" in key or "\0" in value:
            raise ValueError(
                "environment names must be non-empty; names and values must be NUL-free"
            )
        entries.append(f"{key}={value}\0")
    return ctypes.create_unicode_buffer("".join(entries) + "\0")


def _source_handle(
    value: IO[bytes] | int | None, std_handle: int, *, write: bool
) -> tuple[int, _OwnedHandle | None]:
    if isinstance(value, int) and value == subprocess.PIPE:
        raise ValueError("subprocess.PIPE is not supported by ProcessLaunchSpec")
    if isinstance(value, int) and value == subprocess.STDOUT:
        raise ValueError("subprocess.STDOUT is valid only for stderr")
    if isinstance(value, int) and value == subprocess.DEVNULL:
        desired = GENERIC_WRITE if write else GENERIC_READ
        handle = kernel32.CreateFileW(
            "NUL",
            desired,
            FILE_SHARE_READ | FILE_SHARE_WRITE,
            None,
            OPEN_EXISTING,
            FILE_ATTRIBUTE_NORMAL,
            None,
        )
        if not handle or int(handle) == INVALID_HANDLE_VALUE:
            raise ctypes.WinError(ctypes.get_last_error(), "CreateFileW(NUL)")
        owned = _OwnedHandle(int(handle))
        return owned.value, owned
    if value is None:
        handle = kernel32.GetStdHandle(std_handle)
        if handle and int(handle) != INVALID_HANDLE_VALUE:
            return int(handle), None
        return _source_handle(subprocess.DEVNULL, std_handle, write=write)
    file_descriptor = value if isinstance(value, int) else value.fileno()
    return int(msvcrt.get_osfhandle(file_descriptor)), None


def _duplicate_inheritable(source: int) -> _OwnedHandle:
    duplicate = wintypes.HANDLE()
    current = kernel32.GetCurrentProcess()
    _check(
        kernel32.DuplicateHandle(
            current,
            source,
            current,
            ctypes.byref(duplicate),
            0,
            True,
            DUPLICATE_SAME_ACCESS,
        ),
        "DuplicateHandle(stdio)",
    )
    duplicated_handle = duplicate.value
    if duplicated_handle is None:
        raise RuntimeError("DuplicateHandle succeeded without returning a handle")
    result = _OwnedHandle(duplicated_handle)
    try:
        _check(
            kernel32.SetHandleInformation(
                result.value,
                HANDLE_FLAG_INHERIT,
                HANDLE_FLAG_INHERIT,
            ),
            "SetHandleInformation(stdio)",
        )
    except BaseException:
        result.close()
        raise
    return result


def _stdio_handles(spec: ProcessLaunchSpec) -> tuple[_OwnedHandle, _OwnedHandle, _OwnedHandle]:
    duplicates: list[_OwnedHandle] = []
    temporary_sources: list[_OwnedHandle] = []

    def duplicate(value: IO[bytes] | int | None, std_handle: int, *, write: bool) -> _OwnedHandle:
        source, owned_source = _source_handle(value, std_handle, write=write)
        if owned_source is not None:
            temporary_sources.append(owned_source)
        try:
            result = _duplicate_inheritable(source)
            duplicates.append(result)
            return result
        finally:
            if owned_source is not None:
                owned_source.close()
                temporary_sources.remove(owned_source)

    try:
        stdin = duplicate(spec.stdin, STD_INPUT_HANDLE, write=False)
        stdout = duplicate(spec.stdout, STD_OUTPUT_HANDLE, write=True)
        if spec.stderr == subprocess.STDOUT:
            stderr = stdout
        else:
            stderr = duplicate(spec.stderr, STD_ERROR_HANDLE, write=True)
        return stdin, stdout, stderr
    except BaseException as exc:
        errors = _close_owned_handles((*duplicates, *temporary_sources))
        if errors:
            exc.add_note("stdio cleanup failures: " + "; ".join(str(error) for error in errors))
        raise


_InitializeAttributeList = Callable[[object, int, int, object], object]


def _initialize_attribute_list(
    attribute_count: int,
    *,
    initialize: _InitializeAttributeList | None = None,
) -> ctypes.Array[ctypes.c_char]:
    operation = initialize or kernel32.InitializeProcThreadAttributeList
    attribute_size = ctypes.c_size_t()
    ctypes.set_last_error(0)
    sizing_result = operation(None, attribute_count, 0, ctypes.byref(attribute_size))
    sizing_error = ctypes.get_last_error()
    if sizing_result:
        raise OSError(
            "InitializeProcThreadAttributeList attribute-list sizing unexpectedly succeeded"
        )
    if sizing_error != ERROR_INSUFFICIENT_BUFFER:
        raise ctypes.WinError(
            sizing_error,
            "InitializeProcThreadAttributeList attribute-list sizing",
        )
    if not attribute_size.value:
        raise OSError("InitializeProcThreadAttributeList returned an empty attribute-list size")
    attributes = ctypes.create_string_buffer(attribute_size.value)
    _check(
        operation(
            attributes,
            attribute_count,
            0,
            ctypes.byref(attribute_size),
        ),
        "InitializeProcThreadAttributeList",
    )
    return attributes


class _Win32ProcessScopeAdapter:
    def __init__(self, *, job_name: str | None = None) -> None:
        self._job_name = job_name

    def launch(self, spec: ProcessLaunchSpec) -> _Win32PlatformScope:
        raw_job = kernel32.CreateJobObjectW(None, self._job_name)
        if not raw_job:
            raise ctypes.WinError(ctypes.get_last_error(), "CreateJobObjectW")
        job = _OwnedHandle(int(raw_job))
        owned_handles: list[_OwnedHandle] = [job]
        inherited: tuple[_OwnedHandle, ...] = ()
        attributes: ctypes.Array[ctypes.c_char] | None = None
        attributes_initialized = False
        try:
            _check(
                kernel32.SetHandleInformation(job.value, HANDLE_FLAG_INHERIT, 0),
                "SetHandleInformation(job)",
            )
            limits = JobObjectExtendedLimitInformation()
            limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
            _check(
                kernel32.SetInformationJobObject(
                    job.value,
                    JOB_OBJECT_EXTENDED_LIMIT_INFORMATION,
                    ctypes.byref(limits),
                    ctypes.sizeof(limits),
                ),
                "SetInformationJobObject(KILL_ON_JOB_CLOSE)",
            )

            inherited = _stdio_handles(spec)
            owned_handles.extend(inherited)
            unique_handles = tuple(dict.fromkeys(handle.value for handle in inherited))
            attributes = _initialize_attribute_list(2)
            attributes_initialized = True
            job_list = (wintypes.HANDLE * 1)(job.value)
            _check(
                kernel32.UpdateProcThreadAttribute(
                    attributes,
                    0,
                    PROC_THREAD_ATTRIBUTE_JOB_LIST,
                    job_list,
                    ctypes.sizeof(job_list),
                    None,
                    None,
                ),
                "UpdateProcThreadAttribute(JOB_LIST)",
            )
            handle_list = (wintypes.HANDLE * len(unique_handles))(*unique_handles)
            _check(
                kernel32.UpdateProcThreadAttribute(
                    attributes,
                    0,
                    PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
                    handle_list,
                    ctypes.sizeof(handle_list),
                    None,
                    None,
                ),
                "UpdateProcThreadAttribute(HANDLE_LIST)",
            )

            startup = STARTUPINFOEXW()
            startup.StartupInfo.cb = ctypes.sizeof(startup)
            startup.StartupInfo.dwFlags = STARTF_USESTDHANDLES
            startup.StartupInfo.hStdInput = inherited[0].value
            startup.StartupInfo.hStdOutput = inherited[1].value
            startup.StartupInfo.hStdError = inherited[2].value
            startup.lpAttributeList = ctypes.cast(attributes, wintypes.LPVOID)
            command_line = ctypes.create_unicode_buffer(subprocess.list2cmdline(spec.command))
            environment = _environment_block(None if spec.env is None else dict(spec.env))
            environment_pointer = (
                None if environment is None else ctypes.cast(environment, wintypes.LPVOID)
            )
            cwd = None if spec.cwd is None else os.fspath(spec.cwd)
            process_info = ProcessInformation()
            if spec.before_process_create is not None:
                spec.before_process_create()
            _check(
                kernel32.CreateProcessW(
                    spec.command[0],
                    command_line,
                    None,
                    None,
                    True,
                    EXTENDED_STARTUPINFO_PRESENT | CREATE_UNICODE_ENVIRONMENT,
                    environment_pointer,
                    cwd,
                    ctypes.byref(startup.StartupInfo),
                    ctypes.byref(process_info),
                ),
                f"CreateProcessW({spec.command[0]})",
            )
            process_handle = process_info.hProcess
            if process_handle is None:
                raise RuntimeError("CreateProcessW succeeded without returning a process handle")
            thread_handle = process_info.hThread
            if thread_handle is None:
                raise RuntimeError("CreateProcessW succeeded without returning a thread handle")
            root_handle = _OwnedHandle(process_handle)
            owned_thread = _OwnedHandle(thread_handle)
            owned_handles.extend((root_handle, owned_thread))
            owned_thread.close()
            _raise_close_errors(
                _close_owned_handles(inherited),
                "unable to close inherited stdio duplicates after launch",
            )
            platform_scope = _Win32PlatformScope(
                job,
                root_handle,
                int(process_info.dwProcessId),
                spec.command,
            )
            owned_handles.clear()
            return platform_scope
        except BaseException as exc:
            errors = _close_owned_handles(owned_handles)
            if errors:
                exc.add_note(
                    "process-scope launch cleanup failures: "
                    + "; ".join(str(error) for error in errors)
                )
            raise
        finally:
            if attributes is not None and attributes_initialized:
                kernel32.DeleteProcThreadAttributeList(attributes)
