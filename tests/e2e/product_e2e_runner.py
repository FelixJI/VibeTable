"""Real WPF/WebView2 product E2E orchestration.

The runner starts the packaged WPF host and connects Playwright to that
process's WebView2 debugging endpoint.  It intentionally never launches a
Chromium/Edge process itself.
"""

from __future__ import annotations

import argparse
import contextlib
import ctypes
import hashlib
import ipaddress
import json
import os
import re
import shutil
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
SCENARIO_MANIFEST = Path(__file__).with_name("pocketbase_product_scenarios.json")
NODE_RUNNER = Path(__file__).with_name("webview_product_scenarios.mjs")
DEFAULT_EVIDENCE = ROOT / "build" / "qa" / "product-e2e"
CDP_TIMEOUT_SECONDS = 60.0
NORMAL_CLOSE_CONTROL_FILE = "host-normal-close.request"

if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from qa.package_check import check_package  # noqa: E402
from scripts.build_next import RepoPaths  # noqa: E402

DEFAULT_PACKAGE = RepoPaths.default(ROOT).publish_root


@dataclass(frozen=True)
class Scenario:
    id: str
    title: str
    requirement: str
    capabilities: tuple[str, ...] = ()


_SCENARIO_FIELDS = frozenset({"id", "title", "requirement", "capabilities"})
_CAPABILITY_PATTERN = re.compile(r"^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$")


def load_scenarios(path: Path = SCENARIO_MANIFEST) -> list[Scenario]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(raw, list) or not raw:
        raise ValueError("scenario manifest must be a non-empty array")
    scenarios: list[Scenario] = []
    for index, item in enumerate(raw):
        if not isinstance(item, dict) or set(item) != _SCENARIO_FIELDS:
            raise ValueError(
                f"scenario[{index}] must contain exactly: " + ", ".join(sorted(_SCENARIO_FIELDS))
            )
        capabilities = item["capabilities"]
        if (
            not isinstance(capabilities, list)
            or not capabilities
            or any(
                not isinstance(capability, str) or _CAPABILITY_PATTERN.fullmatch(capability) is None
                for capability in capabilities
            )
        ):
            raise ValueError(f"scenario[{index}].capabilities must be non-empty stable names")
        if len(set(capabilities)) != len(capabilities):
            raise ValueError(f"scenario[{index}].capabilities must be unique")
        scenario = Scenario(
            id=str(item["id"]),
            title=str(item["title"]),
            requirement=str(item["requirement"]),
            capabilities=tuple(capabilities),
        )
        if not scenario.id or not scenario.title or not scenario.requirement:
            raise ValueError(f"scenario[{index}] identity, title and requirement are required")
        scenarios.append(scenario)
    if len({item.id for item in scenarios}) != len(scenarios):
        raise ValueError("scenario ids must be unique")
    return scenarios


def select_scenarios(
    scenarios: Sequence[Scenario],
    *,
    scenario_ids: Sequence[str] = (),
    capabilities: Sequence[str] = (),
) -> list[Scenario]:
    known_ids = {item.id for item in scenarios}
    unknown_ids = sorted(set(scenario_ids) - known_ids)
    if unknown_ids:
        raise ValueError(f"unknown scenarios: {', '.join(unknown_ids)}")
    known_capabilities = {capability for item in scenarios for capability in item.capabilities}
    unknown_capabilities = sorted(set(capabilities) - known_capabilities)
    if unknown_capabilities:
        raise ValueError(f"unknown capabilities: {', '.join(unknown_capabilities)}")
    if not scenario_ids and not capabilities:
        return list(scenarios)
    selected_ids = set(scenario_ids)
    selected_capabilities = set(capabilities)
    return [
        item
        for item in scenarios
        if item.id in selected_ids or bool(selected_capabilities.intersection(item.capabilities))
    ]


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _package_layout_path(package_root: Path) -> Path:
    packaged = package_root / "resources" / "publish-layout.json"
    return packaged if packaged.is_file() else package_root / "publish-layout.json"


def package_fingerprint(package_root: Path) -> dict[str, Any]:
    files = sorted(path for path in package_root.rglob("*") if path.is_file())
    aggregate = hashlib.sha256()
    entries: dict[str, str] = {}
    for file_path in files:
        relative = file_path.relative_to(package_root).as_posix()
        digest = _sha256(file_path)
        entries[relative] = digest
        aggregate.update(relative.encode("utf-8"))
        aggregate.update(b"\0")
        aggregate.update(digest.encode("ascii"))
        aggregate.update(b"\n")
    return {
        "algorithm": "sha256(path\\0sha256\\n)",
        "packageSha256": aggregate.hexdigest(),
        "fileCount": len(entries),
        "files": entries,
    }


def _latest(paths: Iterable[Path]) -> tuple[float, str] | None:
    newest: tuple[float, str] | None = None
    for path in paths:
        if not path.is_file():
            continue
        value = (path.stat().st_mtime, str(path.relative_to(ROOT)))
        if newest is None or value[0] > newest[0]:
            newest = value
    return newest


def _source_files(base: Path, patterns: Sequence[str]) -> list[Path]:
    result: list[Path] = []
    for pattern in patterns:
        result.extend(
            path
            for path in base.rglob(pattern)
            if "bin" not in path.parts
            and "obj" not in path.parts
            and "node_modules" not in path.parts
            and not path.name.endswith((".test.ts", ".spec.ts", "_test.go"))
            and path.is_file()
        )
    return result


def package_freshness(package_root: Path) -> dict[str, Any]:
    layout = json.loads(_package_layout_path(package_root).read_text(encoding="utf-8"))
    launch = layout["launch"]
    host_artifact = package_root / "VibeTable.Desktop.dll"
    if not host_artifact.is_file():
        host_artifact = package_root / launch["host"]
    checks: list[tuple[str, Path, list[Path]]] = [
        (
            "desktop-host",
            host_artifact,
            [
                *_source_files(
                    ROOT / "desktop" / "src",
                    ("*.cs", "*.xaml", "*.csproj"),
                ),
                ROOT / "desktop" / "Directory.Build.props",
            ],
        ),
        (
            "web-grid",
            package_root / launch["webGrid"] / "index.html",
            [
                *_source_files(
                    ROOT / "desktop" / "web-grid" / "src",
                    ("*.ts", "*.vue", "*.css", "*.html"),
                ),
                ROOT / "desktop" / "web-grid" / "package.json",
                ROOT / "desktop" / "web-grid" / "package-lock.json",
                ROOT / "desktop" / "web-grid" / "vite.config.ts",
            ],
        ),
        (
            "python-backend",
            package_root / launch["backend"],
            [
                *_source_files(ROOT / "backend", ("*.py",)),
                ROOT / "pyproject.toml",
                ROOT / "uv.lock",
            ],
        ),
        (
            "pocketbase-sidecar",
            package_root / launch["sidecar"],
            [
                *_source_files(ROOT / "sidecar", ("*.go", "*.json")),
                ROOT / "sidecar" / "go.mod",
                ROOT / "sidecar" / "go.sum",
            ],
        ),
    ]
    results: list[dict[str, Any]] = []
    for name, artifact, sources in checks:
        newest = _latest(sources)
        artifact_time = artifact.stat().st_mtime if artifact.is_file() else None
        fresh = (
            artifact_time is not None and newest is not None and artifact_time + 1.0 >= newest[0]
        )
        results.append(
            {
                "component": name,
                "fresh": fresh,
                "artifact": str(artifact.relative_to(package_root)),
                "artifactMtime": artifact_time,
                "newestSource": newest[1] if newest else None,
                "newestSourceMtime": newest[0] if newest else None,
            }
        )
    return {
        "passed": all(item["fresh"] for item in results),
        "components": results,
    }


def audit_package(package_root: Path) -> dict[str, Any]:
    package_root = package_root.resolve()
    errors: list[str] = []
    if not package_root.is_dir():
        return {
            "passed": False,
            "packageRoot": str(package_root),
            "errors": [f"package directory does not exist: {package_root}"],
        }
    try:
        errors.extend(check_package(package_root))
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        errors.append(str(exc))
    try:
        freshness = package_freshness(package_root)
    except (OSError, KeyError, TypeError, json.JSONDecodeError) as exc:
        freshness = {"passed": False, "components": [], "error": str(exc)}
    if not freshness["passed"]:
        errors.append("package is older than one or more product source inputs")
    try:
        fingerprint = package_fingerprint(package_root)
    except OSError as exc:
        fingerprint = {"error": str(exc)}
        errors.append(f"could not fingerprint package: {exc}")
    return {
        "passed": not errors,
        "packageRoot": str(package_root),
        "errors": errors,
        "freshness": freshness,
        "fingerprint": fingerprint,
    }


def _reserve_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def _wait_for_cdp(
    port: int,
    process: subprocess.Popen[bytes],
    process_network: dict[str, Any] | None = None,
) -> None:
    endpoint = f"http://127.0.0.1:{port}/json/version"
    deadline = time.monotonic() + CDP_TIMEOUT_SECONDS
    last_error = ""
    while time.monotonic() < deadline:
        if process_network is not None:
            _record_process_network(process.pid, process_network)
        if process.poll() is not None:
            raise RuntimeError(
                f"WPF host exited with code {process.returncode} before CDP became ready"
            )
        try:
            with urllib.request.urlopen(endpoint, timeout=0.5) as response:
                payload = json.load(response)
            if payload.get("webSocketDebuggerUrl"):
                return
        except (OSError, urllib.error.URLError, json.JSONDecodeError) as exc:
            last_error = str(exc)
        time.sleep(0.1)
    raise TimeoutError(f"WebView2 CDP endpoint was not ready: {last_error}")


def _read_json(path: Path) -> dict[str, Any] | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return value if isinstance(value, dict) else None


def _write_json_atomic(path: Path, value: dict[str, Any]) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    for attempt in range(20):
        try:
            os.replace(temporary, path)
            return
        except PermissionError:
            if attempt == 19:
                raise
            # Windows can briefly deny replacement while the Node consumer or
            # endpoint protection is closing its read handle. Keep the write
            # atomic and bounded instead of falling back to an in-place write.
            time.sleep(min(0.01 * (attempt + 1), 0.1))


def _wait_for_readiness(readiness_dir: Path, process: subprocess.Popen[bytes]) -> dict[str, Any]:
    path = readiness_dir / "vibetable-readiness.json"
    deadline = time.monotonic() + CDP_TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        report = _read_json(path)
        if report is not None:
            return report
        if process.poll() is not None:
            raise RuntimeError(f"WPF host exited with code {process.returncode} before readiness")
        time.sleep(0.1)
    raise TimeoutError("WPF host did not emit vibetable-readiness.json")


def _stop_process_tree(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    if os.name == "nt":
        subprocess.run(
            ["taskkill", "/PID", str(process.pid), "/T", "/F"],
            check=False,
            capture_output=True,
            timeout=15,
        )
    else:
        process.terminate()
    try:
        process.wait(timeout=15)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=10)


def _stop_process_ids(process_ids: Sequence[int]) -> None:
    if os.name != "nt":
        return
    for process_id in sorted({item for item in process_ids if item > 0}):
        subprocess.run(
            ["taskkill", "/PID", str(process_id), "/T", "/F"],
            check=False,
            capture_output=True,
            timeout=15,
        )


def _lifecycle_exit_report(
    *,
    normal_exit_requested: bool,
    host_exit_code: int | None,
    descendants_after_exit: list[dict[str, Any]],
    ports_released: bool,
) -> dict[str, Any]:
    passed = (
        normal_exit_requested
        and host_exit_code == 0
        and not descendants_after_exit
        and ports_released
    )
    return {
        "normalExitRequested": normal_exit_requested,
        "hostExitCode": host_exit_code,
        "descendantsAfterExit": descendants_after_exit,
        "portsReleased": ports_released,
        "status": "passed" if passed else "failed",
    }


def _request_normal_exit(
    process: subprocess.Popen[bytes],
    *,
    controls_dir: Path,
    cdp_port: int,
) -> dict[str, Any]:
    tracked = {process.pid: "VibeTable.Next.exe"}
    for pid, name in _descendants(process.pid):
        tracked[pid] = name
    control = controls_dir / NORMAL_CLOSE_CONTROL_FILE
    control.write_text("normal-close\n", encoding="utf-8")

    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        for pid, name in _descendants(process.pid):
            tracked[pid] = name
        if process.poll() is not None:
            break
        time.sleep(0.1)

    tracked_pids = set(tracked)

    # WebView2 may outlive the WPF window by a few scheduler turns while its
    # browser-process tree flushes profile state. Observe that bounded normal
    # shutdown before classifying an already tracked process as a leak.
    for _ in range(50):
        alive = {pid for pid, _parent, _name in _windows_processes()}
        if not ((tracked_pids - {process.pid}) & alive):
            break
        time.sleep(0.1)

    surviving = {pid: name for pid, _parent, name in _windows_processes() if pid in tracked_pids}
    descendants_after_exit = [
        {"pid": pid, "name": name}
        for pid, name in tracked.items()
        if pid != process.pid and pid in surviving
    ]
    try:
        occupied_by_tracked = [
            row
            for row in _netstat_tcp_rows()
            if int(row["pid"]) in tracked_pids or str(row["local"]).endswith(f":{cdp_port}")
        ]
        ports_released = not occupied_by_tracked
    except (OSError, RuntimeError, subprocess.SubprocessError):
        ports_released = False
    return _lifecycle_exit_report(
        normal_exit_requested=True,
        host_exit_code=process.poll(),
        descendants_after_exit=descendants_after_exit,
        ports_released=ports_released,
    )


def _windows_processes() -> list[tuple[int, int, str]]:
    if os.name != "nt":
        return []
    from ctypes import wintypes

    class ProcessEntry32W(ctypes.Structure):
        _fields_ = [
            ("dwSize", wintypes.DWORD),
            ("cntUsage", wintypes.DWORD),
            ("th32ProcessID", wintypes.DWORD),
            ("th32DefaultHeapID", ctypes.c_size_t),
            ("th32ModuleID", wintypes.DWORD),
            ("cntThreads", wintypes.DWORD),
            ("th32ParentProcessID", wintypes.DWORD),
            ("pcPriClassBase", ctypes.c_long),
            ("dwFlags", wintypes.DWORD),
            ("szExeFile", wintypes.WCHAR * 260),
        ]

    snapshot = ctypes.windll.kernel32.CreateToolhelp32Snapshot(0x00000002, 0)
    if snapshot in (0, ctypes.c_void_p(-1).value):
        raise OSError("CreateToolhelp32Snapshot failed")
    entry = ProcessEntry32W()
    entry.dwSize = ctypes.sizeof(entry)
    result: list[tuple[int, int, str]] = []
    try:
        more = ctypes.windll.kernel32.Process32FirstW(snapshot, ctypes.byref(entry))
        while more:
            result.append(
                (
                    int(entry.th32ProcessID),
                    int(entry.th32ParentProcessID),
                    str(entry.szExeFile),
                )
            )
            more = ctypes.windll.kernel32.Process32NextW(snapshot, ctypes.byref(entry))
    finally:
        ctypes.windll.kernel32.CloseHandle(snapshot)
    return result


def _descendants(parent_pid: int) -> list[tuple[int, str]]:
    processes = _windows_processes()
    children: dict[int, list[tuple[int, str]]] = {}
    for pid, parent, name in processes:
        children.setdefault(parent, []).append((pid, name))
    result: list[tuple[int, str]] = []
    pending = [parent_pid]
    while pending:
        parent = pending.pop()
        for child in children.get(parent, []):
            result.append(child)
            pending.append(child[0])
    return result


def _endpoint_host(endpoint: str) -> str:
    if endpoint.startswith("["):
        closing = endpoint.find("]")
        return endpoint[1:closing] if closing > 0 else endpoint
    return endpoint.rsplit(":", 1)[0]


def _is_loopback_endpoint(endpoint: str) -> bool:
    host = _endpoint_host(endpoint)
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return host.casefold() == "localhost"


def _is_unspecified_endpoint(endpoint: str) -> bool:
    host = _endpoint_host(endpoint)
    try:
        return ipaddress.ip_address(host).is_unspecified
    except ValueError:
        return False


def _netstat_tcp_rows() -> list[dict[str, Any]]:
    completed = subprocess.run(
        ["netstat", "-ano", "-p", "tcp"],
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=10,
    )
    if completed.returncode != 0:
        raise RuntimeError(
            f"netstat failed with {completed.returncode}: {completed.stderr.strip()}"
        )
    rows: list[dict[str, Any]] = []
    for raw_line in completed.stdout.splitlines():
        parts = raw_line.split()
        if len(parts) != 5 or parts[0].casefold() != "tcp":
            continue
        try:
            pid = int(parts[4])
        except ValueError:
            continue
        rows.append(
            {
                "protocol": "TCP",
                "local": parts[1],
                "remote": parts[2],
                "state": parts[3].upper(),
                "pid": pid,
            }
        )
    return rows


def _record_process_network(
    host_pid: int,
    evidence: dict[str, Any],
) -> None:
    try:
        descendants = _descendants(host_pid)
        names = {host_pid: "VibeTable.Next.exe", **dict(descendants)}
        observed = evidence.setdefault("observations", {})
        for row in _netstat_tcp_rows():
            pid = int(row["pid"])
            if pid not in names:
                continue
            item = row | {"processName": names[pid]}
            key = "|".join(str(item[field]) for field in ("pid", "local", "remote", "state"))
            observed[key] = item
        evidence["samples"] = int(evidence.get("samples", 0)) + 1
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        errors = evidence.setdefault("errors", [])
        message = str(exc)
        if message not in errors:
            errors.append(message)


def _process_network_report(evidence: dict[str, Any], *, status: str) -> dict[str, Any]:
    observations = list(evidence.get("observations", {}).values())
    observations.sort(
        key=lambda item: (
            int(item["pid"]),
            str(item["local"]),
            str(item["remote"]),
            str(item["state"]),
        )
    )
    unexpected = []
    for item in observations:
        if item["state"] == "LISTENING" or _is_unspecified_endpoint(str(item["remote"])):
            if not _is_loopback_endpoint(str(item["local"])):
                unexpected.append(item | {"reason": "non_loopback_listener"})
        elif not _is_loopback_endpoint(str(item["remote"])):
            unexpected.append(item | {"reason": "non_loopback_remote"})
    webview_runtime_background = [
        item
        for item in unexpected
        if str(item.get("processName", "")).casefold() == "msedgewebview2.exe"
    ]
    product_unexpected = [
        item
        for item in unexpected
        if str(item.get("processName", "")).casefold() != "msedgewebview2.exe"
    ]
    errors = list(evidence.get("errors", []))
    return {
        "status": status if not errors else "failed",
        "source": "netstat -ano -p tcp",
        "scope": (
            "VibeTable-owned processes must use loopback; WebView2 Runtime "
            "background traffic is retained as a non-gating diagnostic"
        ),
        "samples": int(evidence.get("samples", 0)),
        "observations": observations,
        "unexpectedNonLoopback": unexpected,
        "unexpectedProductNonLoopback": product_unexpected,
        "webViewRuntimeBackgroundNetwork": webview_runtime_background,
        "errors": errors,
    }


def _handle_storage_proof(
    request: dict[str, Any],
    local_data: Path,
) -> dict[str, Any]:
    request_id = request.get("requestId")
    if not isinstance(request_id, str) or not request_id:
        return {
            "status": "failed",
            "code": "STORAGE_PROOF_REQUEST_ID_REQUIRED",
        }
    table_id = request.get("tableId")
    if not isinstance(table_id, str) or not table_id:
        return {
            "status": "failed",
            "code": "STORAGE_PROOF_TABLE_REQUIRED",
            "requestId": request_id,
        }
    data_db = local_data / "pocketbase" / "data.db"
    if not data_db.is_file():
        registry_path = local_data / "VibeTable" / "shell" / "workspace-registry-v2.json"
        try:
            registry = json.loads(registry_path.read_text(encoding="utf-8"))
            workspaces = registry.get("workspaces")
            if not isinstance(workspaces, list) or not workspaces:
                raise ValueError("workspace registry contains no workspaces")
            latest = max(
                (item for item in workspaces if isinstance(item, dict)),
                key=lambda item: str(item.get("lastOpenedAt", "")),
            )
            selected_root = latest.get("selectedRoot")
            if not isinstance(selected_root, str) or not selected_root:
                raise ValueError("workspace registry selectedRoot is invalid")
            data_db = Path(selected_root) / ".vibetable" / "data" / "data.db"
        except (OSError, ValueError, json.JSONDecodeError):
            pass
    if not data_db.is_file():
        return {
            "status": "failed",
            "code": "STORAGE_PROOF_DATABASE_MISSING",
            "requestId": request_id,
            "database": str(data_db),
        }
    try:
        connection = sqlite3.connect(
            f"{data_db.resolve().as_uri()}?mode=ro",
            uri=True,
            timeout=5,
        )
        try:
            row = connection.execute(
                "SELECT physical_name FROM vibetable_tables WHERE table_id = ?",
                (table_id,),
            ).fetchone()
            if row is None or not isinstance(row[0], str):
                raise RuntimeError(f"table definition not found: {table_id}")
            physical_name = row[0]
            if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", physical_name):
                raise RuntimeError(f"table definition has unsafe physical name: {physical_name!r}")
            records = connection.execute(f'SELECT COUNT(*) FROM "{physical_name}"').fetchone()[0]
            audit = connection.execute(
                "SELECT COUNT(*) FROM vibetable_audit_events WHERE table_id = ?",
                (table_id,),
            ).fetchone()[0]
            idempotency = connection.execute(
                """
                SELECT COUNT(*) FROM vibetable_idempotency_keys
                WHERE key NOT LIKE 'metadata:%'
                  AND key NOT LIKE 'field-v2:%'
                """
            ).fetchone()[0]
            outbox = connection.execute(
                """
                SELECT COUNT(*) FROM vibetable_outbox
                WHERE payload_json LIKE ?
                """,
                (f'%"tableId":"{table_id}"%',),
            ).fetchone()[0]
        finally:
            connection.close()
    except (OSError, RuntimeError, sqlite3.Error) as exc:
        return {
            "status": "failed",
            "code": "STORAGE_PROOF_READ_FAILED",
            "requestId": request_id,
            "message": str(exc),
        }
    try:
        audit_ledger = _audit_ledger_proof(data_db.parent.parent / "audit" / "ledger.db")
    except (OSError, RuntimeError, sqlite3.Error, TypeError, ValueError) as exc:
        return {
            "status": "failed",
            "code": "AUDIT_LEDGER_PROOF_FAILED",
            "requestId": request_id,
            "message": str(exc),
        }
    return {
        "status": "completed",
        "requestId": request_id,
        "tableId": table_id,
        "physicalName": physical_name,
        "database": {
            "path": str(data_db),
            "readOnly": True,
        },
        "counts": {
            "records": int(records),
            "audit": int(audit),
            "idempotency": int(idempotency),
            "outbox": int(outbox),
        },
        "auditLedger": audit_ledger,
    }


def _audit_ledger_proof(ledger_path: Path) -> dict[str, Any]:
    if not ledger_path.is_file():
        raise RuntimeError(f"audit ledger not found: {ledger_path}")
    connection = sqlite3.connect(
        f"{ledger_path.resolve().as_uri()}?mode=ro",
        uri=True,
        timeout=5,
    )
    try:
        rows = connection.execute(
            """
            SELECT ledger_sequence, event_id, source_epoch, source_sequence,
                   mutation_identity, payload_hash, payload, occurred_at,
                   previous_hash, hash
            FROM audit_ledger
            ORDER BY ledger_sequence
            """
        ).fetchall()
    finally:
        connection.close()
    previous_hash = ""
    records: list[dict[str, Any]] = []
    source_high_watermarks: dict[str, int] = {}
    for expected_sequence, row in enumerate(rows, start=1):
        (
            ledger_sequence,
            event_id,
            source_epoch,
            source_sequence,
            mutation_identity,
            payload_hash,
            payload_raw,
            occurred_at,
            linked_previous_hash,
            record_hash,
        ) = row
        payload_bytes = bytes(payload_raw)
        expected_payload_hash = "sha256:" + hashlib.sha256(payload_bytes).hexdigest()
        payload = json.loads(payload_bytes)
        envelope = {
            "eventId": event_id,
            "sourceEpoch": source_epoch,
            "sourceSequence": int(source_sequence),
            "mutationIdentity": mutation_identity,
            "payloadHash": payload_hash,
            "payload": payload,
            "occurredAt": occurred_at,
        }
        hash_input = {
            "ledgerSequence": int(ledger_sequence),
            "previousHash": linked_previous_hash,
            "envelope": envelope,
        }
        expected_record_hash = (
            "sha256:"
            + hashlib.sha256(
                json.dumps(
                    hash_input,
                    ensure_ascii=False,
                    separators=(",", ":"),
                ).encode("utf-8")
            ).hexdigest()
        )
        if (
            ledger_sequence != expected_sequence
            or linked_previous_hash != previous_hash
            or payload_hash != expected_payload_hash
            or record_hash != expected_record_hash
        ):
            raise RuntimeError(f"audit ledger chain is invalid at sequence {ledger_sequence}")
        prior_source_sequence = source_high_watermarks.get(source_epoch, 0)
        if source_sequence != prior_source_sequence + 1:
            raise RuntimeError(f"audit source sequence is invalid for {source_epoch!r}")
        source_high_watermarks[source_epoch] = int(source_sequence)
        previous_hash = record_hash
        records.append(
            {
                "ledgerSequence": int(ledger_sequence),
                "eventId": event_id,
                "sourceEpoch": source_epoch,
                "sourceSequence": int(source_sequence),
                "mutationIdentity": mutation_identity,
                "payload": payload,
                "hash": record_hash,
            }
        )
    return {
        "path": str(ledger_path),
        "readOnly": True,
        "verified": True,
        "count": len(records),
        "anchorHash": previous_hash,
        "sourceHighWatermarks": source_high_watermarks,
        "records": records,
    }


def _handle_fault_request(
    request: dict[str, Any], host_process: subprocess.Popen[bytes]
) -> dict[str, Any]:
    action = request.get("action")
    targets = {
        "kill-sidecar": ("vibetable-pb.exe", "SIDECAR_PROCESS_NOT_UNIQUE"),
        "kill-backend": ("vibetable-backend.exe", "BACKEND_PROCESS_NOT_UNIQUE"),
    }
    target = targets.get(action) if isinstance(action, str) else None
    if target is None:
        return {
            "status": "failed",
            "code": "UNKNOWN_FAULT_ACTION",
            "action": action,
        }
    process_name, non_unique_code = target
    matches = [
        (pid, name)
        for pid, name in _descendants(host_process.pid)
        if name.casefold() == process_name
    ]
    if len(matches) != 1:
        return {
            "status": "failed",
            "code": non_unique_code,
            "matches": matches,
        }
    pid, name = matches[0]
    killed = subprocess.run(
        ["taskkill", "/PID", str(pid), "/F"],
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=15,
    )
    return {
        "status": "completed" if killed.returncode == 0 else "failed",
        "action": action,
        "pid": pid,
        "processName": name,
        "returncode": killed.returncode,
        "stdout": killed.stdout,
        "stderr": killed.stderr,
    }


def _run_node_runner(
    command: list[str],
    *,
    scenario_dir: Path,
    local_data: Path,
    host_process: subprocess.Popen[bytes],
    process_network: dict[str, Any] | None = None,
) -> tuple[int, str, str]:
    fault_request = scenario_dir / "fault-request.json"
    fault_result = scenario_dir / "fault-result.json"
    storage_request = scenario_dir / "storage-proof-request.json"
    storage_result = scenario_dir / "storage-proof-result.json"
    process_network_path = scenario_dir / "process-network-observations.json"
    node_process = subprocess.Popen(
        command,
        cwd=ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    handled_fault_ids: set[str] = set()
    invalid_fault_reported = False
    handled_storage_proof_ids: set[str] = set()
    next_network_sample = 0.0
    deadline = time.monotonic() + 180
    while node_process.poll() is None:
        if time.monotonic() >= deadline:
            node_process.kill()
            stdout, stderr = node_process.communicate(timeout=10)
            raise subprocess.TimeoutExpired(command, 180, stdout, stderr)
        request = _read_json(fault_request)
        if request is not None:
            fault_request_id = request.get("requestId")
            if isinstance(fault_request_id, str) and fault_request_id:
                if fault_request_id not in handled_fault_ids:
                    handled_fault_ids.add(fault_request_id)
                    _write_json_atomic(
                        fault_result,
                        {
                            "requestId": fault_request_id,
                            **_handle_fault_request(request, host_process),
                        },
                    )
            elif not invalid_fault_reported:
                invalid_fault_reported = True
                _write_json_atomic(
                    fault_result,
                    {
                        "requestId": None,
                        "status": "failed",
                        "code": "FAULT_REQUEST_ID_INVALID",
                    },
                )
        request = _read_json(storage_request)
        if request is not None:
            storage_request_id = request.get("requestId")
            if (
                isinstance(storage_request_id, str)
                and storage_request_id not in handled_storage_proof_ids
            ):
                handled_storage_proof_ids.add(storage_request_id)
                _write_json_atomic(
                    storage_result,
                    _handle_storage_proof(request, local_data),
                )
        if process_network is not None and time.monotonic() >= next_network_sample:
            _record_process_network(host_process.pid, process_network)
            _write_json_atomic(
                process_network_path,
                _process_network_report(process_network, status="monitoring"),
            )
            next_network_sample = time.monotonic() + 0.25
        time.sleep(0.05)
    if process_network is not None:
        _record_process_network(host_process.pid, process_network)
        _write_json_atomic(
            process_network_path,
            _process_network_report(process_network, status="completed"),
        )
    stdout, stderr = node_process.communicate(timeout=10)
    return node_process.returncode, stdout, stderr


def _failure_result(
    scenario: Scenario,
    *,
    code: str,
    message: str,
    dependency: str | None = None,
) -> dict[str, Any]:
    return {
        "scenario": scenario.id,
        "title": scenario.title,
        "requirement": scenario.requirement,
        "status": "failed",
        "error": {
            "code": code,
            "message": message,
            "dependency": dependency,
        },
    }


def _nearest_rank(values: list[float], percentile: int) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, (len(ordered) * percentile + 99) // 100 - 1))
    return round(ordered[index], 2)


def summarize_performance(results: Sequence[dict[str, Any]]) -> dict[str, Any]:
    scenario_timings: list[dict[str, Any]] = []
    operation_samples: dict[str, list[dict[str, Any]]] = {}
    ui_samples: dict[str, list[float]] = {}
    pending_requests = 0
    for result in results:
        duration = result.get("durationMs")
        if isinstance(duration, (int, float)) and not isinstance(duration, bool):
            scenario_timings.append(
                {
                    "scenario": result.get("scenario"),
                    "status": result.get("status"),
                    "durationMs": round(float(duration), 2),
                }
            )
        ui_timings = result.get("uiTimings")
        if isinstance(ui_timings, list):
            for timing in ui_timings:
                if not isinstance(timing, dict):
                    continue
                name = timing.get("name")
                ui_duration = timing.get("durationMs")
                if (
                    isinstance(name, str)
                    and isinstance(ui_duration, (int, float))
                    and not isinstance(ui_duration, bool)
                ):
                    ui_samples.setdefault(name, []).append(float(ui_duration))
        diagnostics = result.get("bridgeDiagnostics")
        if not isinstance(diagnostics, dict):
            continue
        pending = diagnostics.get("pending")
        if isinstance(pending, list):
            pending_requests += len(pending)
        round_trips = diagnostics.get("roundTrips")
        if not isinstance(round_trips, list):
            continue
        for sample in round_trips:
            if not isinstance(sample, dict):
                continue
            request_type = sample.get("requestType")
            sample_duration = sample.get("durationMs")
            if (
                not isinstance(request_type, str)
                or not isinstance(sample_duration, (int, float))
                or isinstance(sample_duration, bool)
            ):
                continue
            operation_samples.setdefault(request_type, []).append(
                {
                    "scenario": result.get("scenario"),
                    "durationMs": float(sample_duration),
                    "failed": sample.get("responseType") == "operation.failed",
                    "code": sample.get("code"),
                }
            )

    by_operation: list[dict[str, Any]] = []
    for request_type, samples in sorted(operation_samples.items()):
        durations = [sample["durationMs"] for sample in samples]
        by_operation.append(
            {
                "requestType": request_type,
                "count": len(samples),
                "failures": sum(sample["failed"] for sample in samples),
                "p50Ms": _nearest_rank(durations, 50),
                "p95Ms": _nearest_rank(durations, 95),
                "maxMs": round(max(durations), 2),
            }
        )

    history = next(
        (operation for operation in by_operation if operation["requestType"] == "history.query"),
        None,
    )
    history_status = "not-measured"
    if history is not None:
        if history["maxMs"] > 2_000:
            history_status = "hard-limit-exceeded"
        elif history["p95Ms"] > 500:
            history_status = "warning"
        else:
            history_status = "within-budget"
    by_ui_action = [
        {
            "name": name,
            "count": len(durations),
            "p50Ms": _nearest_rank(durations, 50),
            "p95Ms": _nearest_rank(durations, 95),
            "maxMs": round(max(durations), 2),
        }
        for name, durations in sorted(ui_samples.items())
    ]
    history_ui = next(
        (action for action in by_ui_action if action["name"] == "history.drawer.initialLoad"),
        None,
    )
    history_ui_status = "not-measured"
    if history_ui is not None:
        if history_ui["maxMs"] > 2_000:
            history_ui_status = "hard-limit-exceeded"
        elif history_ui["p95Ms"] > 750:
            history_ui_status = "warning"
        else:
            history_ui_status = "within-budget"
    return {
        "thresholds": {
            "historyQueryP95WarningMs": 500,
            "historyQueryHardLimitMs": 2_000,
            "historyDrawerP95WarningMs": 750,
            "historyDrawerHardLimitMs": 2_000,
            "scenarioHardTimeoutMs": 180_000,
            "note": (
                "Scenario duration includes app startup and fixture work. "
                "Deliberate sidecar recovery is assessed separately from normal bridge latency."
            ),
        },
        "assessment": {
            "historyQuery": history_status,
            "historyDrawer": history_ui_status,
            "pendingRequests": pending_requests,
            "bridgeFailures": sum(operation["failures"] for operation in by_operation),
        },
        "scenarios": scenario_timings,
        "byUiAction": by_ui_action,
        "byOperation": by_operation,
    }


def _scenario_runtime_directory(evidence_root: Path, scenario: Scenario) -> Path:
    scenario_number = scenario.id.partition("-")[0]
    if len(scenario_number) != 2 or not scenario_number.isdecimal():
        raise ValueError(f"Scenario id must start with a two-digit number: {scenario.id}")
    return (evidence_root / "_runtime" / scenario_number).resolve()


def run_scenario(
    scenario: Scenario,
    *,
    package_root: Path,
    evidence_root: Path,
    node: str,
) -> dict[str, Any]:
    # The packaged host runs with the package as its working directory. Keep
    # every test-mode file protocol absolute so WPF and the orchestrator refer
    # to the same isolated evidence/data tree.
    scenario_dir = (evidence_root / scenario.id).resolve()
    scenario_dir.mkdir(parents=True, exist_ok=True)
    runtime_dir = _scenario_runtime_directory(evidence_root, scenario)
    readiness_dir = runtime_dir / "host"
    readiness_dir.mkdir(parents=True)
    controls_dir = runtime_dir / "controls"
    controls_dir.mkdir()
    import_source = controls_dir / "import-source.csv"
    import_source.write_text(
        (
            "payload\n"
            '"{""items"":[1,{""code"":""A""}],'
            '""nested"":{""label"":""import"",""value"":9},'
            '""enabled"":true}"\n'
        ),
        encoding="utf-8",
    )
    export_target = controls_dir / "export-result.csv"
    (controls_dir / "import-source.txt").write_text(
        str(import_source.resolve()) + "\n",
        encoding="utf-8",
    )
    (controls_dir / "export-target.txt").write_text(
        str(export_target.resolve()) + "\n",
        encoding="utf-8",
    )
    plugin_fixture = ROOT / "tests" / "fixtures" / "plugins" / "mutation-boundary"
    (controls_dir / "plugin-source.txt").write_text(
        str(plugin_fixture.resolve()) + "\n",
        encoding="utf-8",
    )
    attachment_base = {
        "12-backup-consistency": "backup",
        "18-workspace-search": "e2e-search-attachment",
    }.get(scenario.id, "attachment")
    attachment_source = controls_dir / f"{attachment_base}-original.txt"
    attachment_source.write_text(f"{attachment_base}-original\n", encoding="utf-8")
    attachment_replacement = controls_dir / f"{attachment_base}-replacement.txt"
    attachment_replacement.write_text(
        f"{attachment_base}-replacement\n",
        encoding="utf-8",
    )
    (controls_dir / "attachment-source.txt").write_text(
        str(attachment_source.resolve()) + "\n",
        encoding="utf-8",
    )
    (controls_dir / "attachment-replacement-source.txt").write_text(
        str(attachment_replacement.resolve()) + "\n",
        encoding="utf-8",
    )
    document_source = controls_dir / "document-diff-source.txt"
    document_source.write_text(
        "VibeTable document diff product E2E\n",
        encoding="utf-8",
    )
    (controls_dir / "document-source.txt").write_text(
        str(document_source.resolve()) + "\n",
        encoding="utf-8",
    )
    content_markdown_source = controls_dir / "content-reference-a.md"
    content_markdown_source.write_text(
        "# E2E content reference\n\nMarigold appears in visible Markdown text.\n",
        encoding="utf-8",
    )
    content_json_source = controls_dir / "content-reference-b.json"
    content_json_source.write_text(
        json.dumps(
            {"title": "E2E JSON reference", "body": "Cobalt appears in JSON content."},
            ensure_ascii=False,
        )
        + "\n",
        encoding="utf-8",
    )
    workspace_root = controls_dir / "workspace-root"
    workspace_root.mkdir()
    snapshot_package = controls_dir / "workspace-snapshot.vtsnapshot"
    snapshot_extract = controls_dir / "snapshot-extract.bin"
    for control_name, target in (
        ("workspace-root.txt", workspace_root),
        ("snapshot-export-target.txt", snapshot_package),
        ("snapshot-import-source.txt", snapshot_package),
        ("snapshot-extract-target.txt", snapshot_extract),
        ("file-upgrade-source.txt", document_source),
    ):
        (controls_dir / control_name).write_text(
            str(target.resolve()) + "\n",
            encoding="utf-8",
        )
    layout = json.loads(_package_layout_path(package_root).read_text(encoding="utf-8"))
    host = (package_root / layout["launch"]["host"]).resolve()
    port = _reserve_port()
    environment = os.environ.copy()
    # TEMPORARY CI DEBUG - REVERT: surface backend transport/realtime behavior
    # in the CI job log because lane evidence directories stay on the runner.
    debug_log = evidence_root / "transport-debug.log"
    environment["VIBETABLE_TRANSPORT_DEBUG_LOG"] = str(debug_log)
    environment["VIBETABLE_WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"] = (
        f"--remote-debugging-port={port} --disable-gpu"
    )
    environment["VIBETABLE_E2E_WEBVIEW2_USER_DATA_ROOT"] = str(
        (readiness_dir / "webview2-user-data").resolve()
    )
    if scenario.id == "05-formula-lifecycle":
        environment["VIBETABLE_E2E_MIGRATION_FAULT_FILE"] = str(
            (controls_dir / "migration-fault.phase").resolve()
        )
    mutation_barrier = None
    if scenario.id == "09-atomic-import-scale":
        barrier_arm = controls_dir / "mutation-barrier.arm"
        barrier_arm.write_text("armed\n", encoding="utf-8")
        environment["VIBETABLE_E2E_MUTATION_BARRIER_DIR"] = str(controls_dir)
        mutation_barrier = {
            "environment": "VIBETABLE_E2E_MUTATION_BARRIER_DIR",
            "arm": str(barrier_arm),
            "ready": str(controls_dir / "mutation-barrier.ready.json"),
            "point": "after_record",
        }
    command = [
        str(host),
        "--test-mode",
        "--readiness-dir",
        str(readiness_dir),
        "--e2e-controls-dir",
        str(controls_dir),
    ]
    (scenario_dir / "launch.json").write_text(
        json.dumps(
            {
                "command": command,
                "cwd": str(package_root),
                "cdpUrl": f"http://127.0.0.1:{port}",
                "dataRoot": str(readiness_dir / "local-data"),
                "controlsDirectory": str(controls_dir),
                "mutationBarrier": mutation_barrier,
                "renderer": "the WPF host's WebView2; no browser launch",
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    with (
        (scenario_dir / "host-stdout.log").open("wb") as stdout,
        (scenario_dir / "host-stderr.log").open("wb") as stderr,
    ):
        process = subprocess.Popen(
            command,
            cwd=package_root,
            env=environment,
            stdout=stdout,
            stderr=stderr,
        )
        result: dict[str, Any]
        try:
            process_network = (
                {"observations": {}, "errors": [], "samples": 0}
                if scenario.id == "01-offline-first-start"
                else None
            )
            _wait_for_cdp(port, process, process_network)
            node_command = [
                node,
                str(NODE_RUNNER),
                "--cdp-url",
                f"http://127.0.0.1:{port}",
                "--scenario",
                scenario.id,
                "--evidence-dir",
                str(scenario_dir),
                "--controls-dir",
                str(controls_dir),
                "--data-root",
                str(readiness_dir / "local-data"),
            ]
            node_returncode, node_stdout, node_stderr = _run_node_runner(
                node_command,
                scenario_dir=scenario_dir,
                local_data=readiness_dir / "local-data",
                host_process=process,
                process_network=process_network,
            )
            (scenario_dir / "runner-stdout.log").write_text(node_stdout, encoding="utf-8")
            (scenario_dir / "runner-stderr.log").write_text(node_stderr, encoding="utf-8")
            # TEMPORARY CI DEBUG - REVERT: echo the backend debug log slice and
            # the host trace so the CI job log shows CI-time behavior.
            debug_slice = ""
            with contextlib.suppress(OSError):
                debug_slice = debug_log.read_text(encoding="utf-8", errors="replace")
            print(
                f"[debug-dump:{scenario.id}:transport]\n{debug_slice}",
                flush=True,
            )
            with contextlib.suppress(OSError):
                debug_log.write_text("", encoding="utf-8")
            host_trace = ""
            with contextlib.suppress(OSError):
                host_trace = (readiness_dir / "vibetable-trace.log").read_text(
                    encoding="utf-8", errors="replace"
                )
            print(f"[debug-dump:{scenario.id}:host-trace]\n{host_trace}", flush=True)
            readiness = _wait_for_readiness(readiness_dir, process)
            result_path = scenario_dir / f"{scenario.id}-result.json"
            result = _read_json(result_path) or _failure_result(
                scenario,
                code="RUNNER_RESULT_MISSING",
                message="Playwright runner did not write a structured result",
            )
            process_network_report = _read_json(scenario_dir / "process-network-observations.json")
            result.update(
                {
                    "title": scenario.title,
                    "requirement": scenario.requirement,
                    "readiness": readiness,
                    "hostExitCodeBeforeCleanup": process.poll(),
                    "nodeExitCode": node_returncode,
                    "evidenceDirectory": str(scenario_dir),
                    "processNetwork": process_network_report,
                }
            )
            if readiness.get("ready") is not True:
                result["status"] = "failed"
                result["error"] = {
                    "code": "HOST_NOT_READY",
                    "message": str(readiness.get("error") or readiness),
                }
            elif scenario.id == "01-offline-first-start" and (
                process_network_report is None
                or process_network_report.get("status") != "completed"
                or process_network_report.get("samples", 0) < 1
                or bool(process_network_report.get("unexpectedProductNonLoopback"))
            ):
                result["status"] = "failed"
                result["error"] = {
                    "code": "PROCESS_NETWORK_OBSERVATION_FAILED",
                    "message": (
                        "process-tree network observation was unavailable or found "
                        "a VibeTable-owned non-loopback listener/remote endpoint"
                    ),
                    "details": process_network_report,
                }
            elif node_returncode != 0:
                result["status"] = "failed"
                # The Node runner catches scenario assertions, persists their
                # structured error, and intentionally exits non-zero. Preserve
                # that root cause; only synthesize an infrastructure error when
                # Node crashed or returned a contradictory passing document.
                if not isinstance(result.get("error"), dict):
                    result["error"] = {
                        "code": "NODE_RUNNER_FAILED",
                        "message": (
                            f"Playwright runner exited with code {node_returncode}: "
                            f"{node_stderr.strip() or 'no stderr'}"
                        ),
                    }
            elif result.get("status") != "passed":
                result["status"] = "failed"
                result["error"] = {
                    "code": "RESULT_EXIT_MISMATCH",
                    "message": "runner exited zero without a passing result",
                }
        except (OSError, RuntimeError, TimeoutError, subprocess.TimeoutExpired) as exc:
            result = _failure_result(
                scenario,
                code="E2E_INFRASTRUCTURE_FAILED",
                message=str(exc),
            ) | {"evidenceDirectory": str(scenario_dir)}

        lifecycle_error: str | None = None
        try:
            lifecycle = _request_normal_exit(
                process,
                controls_dir=controls_dir,
                cdp_port=port,
            )
        except (OSError, RuntimeError, TimeoutError, subprocess.SubprocessError) as exc:
            lifecycle_error = str(exc)
            lifecycle = _lifecycle_exit_report(
                normal_exit_requested=False,
                host_exit_code=process.poll(),
                descendants_after_exit=[],
                ports_released=False,
            )
        result["lifecycle"] = lifecycle
        if lifecycle_error is not None:
            result["lifecycleError"] = lifecycle_error
        if lifecycle["status"] != "passed":
            if result.get("status") == "passed":
                result["status"] = "failed"
                result["error"] = {
                    "code": "HOST_LIFECYCLE_FAILED",
                    "message": "正常关闭后仍有宿主进程、子进程或端口残留。",
                    "details": lifecycle,
                }
            else:
                result["lifecycleFailure"] = lifecycle
            _stop_process_tree(process)
            _stop_process_ids(
                [
                    int(item["pid"])
                    for item in lifecycle["descendantsAfterExit"]
                    if isinstance(item, dict) and isinstance(item.get("pid"), int)
                ]
            )
        return result


def write_aggregate(
    path: Path,
    *,
    audit: dict[str, Any],
    results: list[dict[str, Any]],
) -> dict[str, Any]:
    passed = sum(item.get("status") == "passed" for item in results)
    report = {
        "contractVersion": "2.0",
        "generatedAt": datetime.now(UTC).isoformat(),
        "reportPath": str(path.resolve()),
        "status": "passed" if audit["passed"] and passed == len(results) else "failed",
        "transport": {
            "driver": "playwright-core",
            "connection": "chromium.connectOverCDP",
            "browserLaunchAllowed": False,
            "target": "real packaged WPF WebView2",
        },
        "packageAudit": audit,
        "summary": {
            "total": len(results),
            "passed": passed,
            "failed": len(results) - passed,
            "skipped": 0,
        },
        "performance": summarize_performance(results),
        "scenarios": results,
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    return report


def run_product_acceptance(
    *,
    package_root: Path = DEFAULT_PACKAGE,
    evidence_root: Path = DEFAULT_EVIDENCE,
    selected: Sequence[str] = (),
    capabilities: Sequence[str] = (),
) -> tuple[int, dict[str, Any]]:
    scenarios = select_scenarios(
        load_scenarios(),
        scenario_ids=selected,
        capabilities=capabilities,
    )
    audit = audit_package(package_root)
    run_root = evidence_root / datetime.now(UTC).strftime("%Y%m%dT%H%M%SZ")
    report_path = run_root / "product-e2e-report.json"
    if not audit["passed"]:
        results = [
            _failure_result(
                scenario,
                code="PACKAGE_AUDIT_FAILED",
                message="发布包完整性或新鲜度检查失败，场景未启动。",
                dependency="先用 scripts/build_next.py 生成与当前源代码一致的发布包。",
            )
            for scenario in scenarios
        ]
        return 1, write_aggregate(
            report_path,
            audit=audit,
            results=results,
        )
    if sys.platform != "win32":
        results = [
            _failure_result(
                scenario,
                code="WINDOWS_WEBVIEW2_REQUIRED",
                message="真实 WPF WebView2 产品 E2E 只能在 Windows 桌面会话运行。",
            )
            for scenario in scenarios
        ]
        return 1, write_aggregate(report_path, audit=audit, results=results)
    node = shutil.which("node")
    if not node:
        results = [
            _failure_result(
                scenario,
                code="NODE_REQUIRED",
                message="找不到 Node.js，无法运行 playwright-core CDP 客户端。",
            )
            for scenario in scenarios
        ]
        return 1, write_aggregate(report_path, audit=audit, results=results)
    results = [
        run_scenario(
            scenario,
            package_root=package_root.resolve(),
            evidence_root=run_root,
            node=node,
        )
        for scenario in scenarios
    ]
    report = write_aggregate(report_path, audit=audit, results=results)
    return (0 if report["status"] == "passed" else 1), report


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package-root", type=Path, default=DEFAULT_PACKAGE)
    parser.add_argument("--evidence-root", type=Path, default=DEFAULT_EVIDENCE)
    parser.add_argument("--scenario", action="append", default=[])
    parser.add_argument("--capability", action="append", default=[])
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        code, report = run_product_acceptance(
            package_root=args.package_root,
            evidence_root=args.evidence_root,
            selected=args.scenario,
            capabilities=args.capability,
        )
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"[FAIL] product E2E configuration: {exc}", file=sys.stderr)
        return 2
    summary = report["summary"]
    print(
        f"[{report['status'].upper()}] real WPF/WebView2 product E2E: "
        f"{summary['passed']}/{summary['total']} passed, "
        f"{summary['failed']} failed, 0 skipped"
    )
    print("report:", report["reportPath"])
    for item in report["scenarios"]:
        if item["status"] != "passed":
            error = item.get("error", {})
            print(
                f"  - {item['scenario']}: {error.get('code', 'FAILED')} {error.get('message', '')}"
            )
    return code


if __name__ == "__main__":
    raise SystemExit(main())
