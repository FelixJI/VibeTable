"""Small event-driven child tree used by Windows process-scope contract tests."""

from __future__ import annotations

import argparse
import ctypes
import os
import subprocess
import sys
from ctypes import wintypes
from pathlib import Path

from scripts.qa.windows_process_scope import ProcessLaunchSpec, _launch_with_adapter

EVENT_MODIFY_STATE = 0x0002
JOB_OBJECT_QUERY = 0x0004
SYNCHRONIZE = 0x00100000
INFINITE = 0xFFFFFFFF


def _event(name: str) -> int:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenEventW.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.LPCWSTR]
    kernel32.OpenEventW.restype = wintypes.HANDLE
    handle = kernel32.OpenEventW(EVENT_MODIFY_STATE | SYNCHRONIZE, False, name)
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error(), f"OpenEventW({name})")
    return int(handle)


def _require_current_process_in_job(job_name: str) -> None:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenJobObjectW.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.LPCWSTR]
    kernel32.OpenJobObjectW.restype = wintypes.HANDLE
    kernel32.GetCurrentProcess.argtypes = []
    kernel32.GetCurrentProcess.restype = wintypes.HANDLE
    kernel32.IsProcessInJob.argtypes = [
        wintypes.HANDLE,
        wintypes.HANDLE,
        ctypes.POINTER(wintypes.BOOL),
    ]
    kernel32.IsProcessInJob.restype = wintypes.BOOL
    job = kernel32.OpenJobObjectW(JOB_OBJECT_QUERY, False, job_name)
    if not job:
        raise ctypes.WinError(ctypes.get_last_error(), f"OpenJobObjectW({job_name})")
    try:
        belongs = wintypes.BOOL()
        if not kernel32.IsProcessInJob(
            kernel32.GetCurrentProcess(),
            job,
            ctypes.byref(belongs),
        ):
            raise ctypes.WinError(ctypes.get_last_error(), f"IsProcessInJob({job_name})")
        if not belongs.value:
            raise RuntimeError(f"fixture process does not belong to target Job {job_name}")
    finally:
        kernel32.CloseHandle(job)


def _signal(name: str) -> None:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    handle = _event(name)
    try:
        if not kernel32.SetEvent(handle):
            raise ctypes.WinError(ctypes.get_last_error(), f"SetEvent({name})")
    finally:
        kernel32.CloseHandle(handle)


def _wait(name: str) -> None:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    handle = _event(name)
    try:
        result = int(kernel32.WaitForSingleObject(handle, INFINITE))
        if result != 0:
            raise ctypes.WinError(ctypes.get_last_error(), f"WaitForSingleObject({name})")
    finally:
        kernel32.CloseHandle(handle)


FIXTURE_MODULE = "tests.e2e.windows_process_scope_fixture"


def _run_child(ready: str, release: str, pid_path: str) -> int:
    Path(pid_path).write_text(str(os.getpid()), encoding="utf-8")
    _signal(ready)
    _wait(release)
    return 0


def _run_root(
    job_name: str,
    ready: str,
    root_release: str,
    child_release: str,
    pid_path: str,
) -> int:
    subprocess.Popen(
        [
            sys.executable,
            "-m",
            FIXTURE_MODULE,
            job_name,
            "child",
            ready,
            child_release,
            pid_path,
        ]
    )
    _wait(root_release)
    return 0


def _run_nested_root(
    child_ready: str,
    root_release: str,
    child_release: str,
    pid_path: str,
    inner_job_name: str,
) -> int:
    from scripts.qa._windows_process_scope_win32 import _Win32ProcessScopeAdapter

    with _launch_with_adapter(
        ProcessLaunchSpec(
            [
                sys.executable,
                "-m",
                FIXTURE_MODULE,
                inner_job_name,
                "child",
                child_ready,
                child_release,
                pid_path,
            ]
        ),
        _Win32ProcessScopeAdapter(job_name=inner_job_name),
    ) as inner:
        _wait(root_release)
        result = inner.terminate_all()
        return 0 if result.success else 3


def main(argv: list[str] | None = None) -> int:
    arguments = list(sys.argv[1:] if argv is None else argv)
    if not arguments:
        raise ValueError("target Job name is required")
    job_name = arguments.pop(0)
    _require_current_process_in_job(job_name)
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="mode", required=True)
    child = subparsers.add_parser("child")
    child.add_argument("ready")
    child.add_argument("release")
    child.add_argument("pid_path")
    root = subparsers.add_parser("root")
    root.add_argument("ready")
    root.add_argument("root_release")
    root.add_argument("child_release")
    root.add_argument("pid_path")
    nested = subparsers.add_parser("nested-root")
    nested.add_argument("child_ready")
    nested.add_argument("root_release")
    nested.add_argument("child_release")
    nested.add_argument("pid_path")
    nested.add_argument("inner_job_name")
    args = parser.parse_args(arguments)
    if args.mode == "child":
        return _run_child(args.ready, args.release, args.pid_path)
    if args.mode == "root":
        return _run_root(
            job_name,
            args.ready,
            args.root_release,
            args.child_release,
            args.pid_path,
        )
    return _run_nested_root(
        args.child_ready,
        args.root_release,
        args.child_release,
        args.pid_path,
        args.inner_job_name,
    )


if __name__ == "__main__":
    raise SystemExit(main())
