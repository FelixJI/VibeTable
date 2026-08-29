from __future__ import annotations

import json
import subprocess
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path

import pytest

from qa import release_candidate
from scripts.qa.windows_process_scope import (
    ProcessWorkingSetMember,
    ProcessWorkingSetSnapshot,
)
from tests.e2e import packaged_runtime_baseline
from tests.e2e.packaged_host_lifecycle import (
    PackagedWorkspacePhaseObserver,
    WorkspaceLifecycleEvidence,
    WorkspaceOpenEvidence,
)

SOURCE_SHA = "a" * 40


def _write_candidate(
    package_root: Path,
    artifacts_root: Path,
) -> tuple[Path, Path, dict[str, object]]:
    files = {
        "VibeTable.Next.exe": b"host",
        "resources/backend/vibetable-backend.exe": b"backend",
        "resources/sidecar/vibetable-pb.exe": b"sidecar",
        "resources/web-grid/app.js": b"web",
    }
    for relative, content in files.items():
        target = package_root / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)
    release = {
        "product": "VibeTable",
        "version": "0.5.1",
        "platform": "windows",
        "architecture": "x64",
    }
    (package_root / "release.json").write_text(json.dumps(release), encoding="utf-8")
    (package_root / "resources/publish-layout.json").write_text(
        json.dumps(
            {
                "protocolVersion": "2.0",
                "launch": {
                    "host": "VibeTable.Next.exe",
                    "backend": "resources/backend/vibetable-backend.exe",
                    "sidecar": "resources/sidecar/vibetable-pb.exe",
                    "webGrid": "resources/web-grid",
                },
            }
        ),
        encoding="utf-8",
    )
    archive = artifacts_root / "VibeTable-v0.5.1-win-x64.zip"
    evidence = release_candidate.create_archive(package_root, archive)
    archive_evidence = evidence["archive"]
    assert isinstance(archive_evidence, dict)
    archive_sha = archive_evidence["sha256"]
    assert isinstance(archive_sha, str)
    identity_path = artifacts_root / "build-identity.json"
    identity_path.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "project": {
                    "component": "vibetable",
                    "repository": "FelixJI/VibeTable",
                    "version": "0.5.1",
                    "source_sha": SOURCE_SHA,
                },
                "build": {
                    "archive": archive.name,
                    "archive_sha256": archive_sha,
                    "package_identity": release,
                    "package_identity_sha256": "c" * 64,
                },
            }
        ),
        encoding="utf-8",
    )
    return archive, identity_path, evidence


def _working_sets() -> ProcessWorkingSetSnapshot:
    return ProcessWorkingSetSnapshot(
        (
            ProcessWorkingSetMember(10, "VibeTable.Next.exe", True, 100),
            ProcessWorkingSetMember(11, "vibetable-backend.exe", True, 200),
            ProcessWorkingSetMember(12, "vibetable-pb.exe", True, 300),
        )
    )


def _probe_evidence() -> dict[str, object]:
    return {
        "contractVersion": "1.0",
        "evidenceKind": "packaged-runtime-ui-probe",
        "status": "passed",
        "tableId": "tbl_runtime_baseline",
        "sameTableIdentity": True,
        "tableSummaryVisible": True,
        "errorOverlayVisible": False,
        "rowCount": 0,
        "stableWindowMs": 1_000,
        "errors": [],
    }


def _workspace() -> WorkspaceOpenEvidence:
    return {
        "workspaceId": "11111111-1111-4111-8111-111111111111",
        "sessionEpoch": 7,
        "sessionState": "openedWritable",
        "openOutcome": "opened-writable",
        "writableSessionPublished": True,
    }


def _lifecycle() -> WorkspaceLifecycleEvidence:
    return {
        "normalExitRequested": True,
        "hostExitCode": 0,
        "membersAfterExit": (),
        "portsReleased": True,
        "ownerLeaseStableHandleClosed": True,
        "errors": (),
        "status": "passed",
    }


class _Session:
    def __init__(self, events: list[str]) -> None:
        self.cdp_url = "http://127.0.0.1:9222"
        self.workspace_evidence = _workspace()
        self.lifecycle: WorkspaceLifecycleEvidence
        self._events = events

    def working_set_snapshot(self) -> ProcessWorkingSetSnapshot:
        self._events.append("snapshot")
        return _working_sets()


def test_packaged_baseline_binds_real_candidate_and_measures_owned_lifecycle(
    monkeypatch,
    tmp_path: Path,
) -> None:
    package_root = tmp_path / "package"
    archive, identity_path, candidate_evidence = _write_candidate(
        package_root,
        tmp_path / "artifacts",
    )
    events: list[str] = []
    session = _Session(events)

    @contextmanager
    def opened(
        *_args: object,
        observer: PackagedWorkspacePhaseObserver | None,
        **_kwargs: object,
    ) -> Iterator[_Session]:
        assert observer is not None
        observer.launch_started()
        observer.host_ready()
        observer.workspace_open_requested()
        observer.workspace_opened()
        yield session
        session.lifecycle = _lifecycle()
        events.append("exit")

    def probe(_cdp_url: str, _report_path: Path) -> dict[str, object]:
        events.append("probe")
        return _probe_evidence()

    monkeypatch.setattr(packaged_runtime_baseline, "opened_packaged_workspace", opened)
    clock = iter((0, 10, 20, 30, 40))
    report_path = tmp_path / "report.json"

    report = packaged_runtime_baseline.run_packaged_runtime_baseline(
        package_root=package_root,
        package_archive=archive,
        workspace_root=tmp_path / "workspace",
        evidence_root=tmp_path / "evidence",
        build_identity_path=identity_path,
        expected_source_sha=SOURCE_SHA,
        json_report=report_path,
        monotonic_ns=lambda: next(clock),
        sleep=lambda seconds: events.append(f"sleep:{seconds}"),
        probe=probe,
    )

    assert events == ["probe", "sleep:1.0", "snapshot", "exit"]
    assert report["status"] == "passed"
    assert report["releaseCandidate"] == candidate_evidence
    measurements = report["measurements"]
    assert isinstance(measurements, dict)
    assert measurements["elapsedNs"] == {
        "launchToHostReady": 10,
        "workspaceOpenRequestToOpened": 10,
        "workspaceOpenRequestToFirstTableStable": 20,
    }
    assert report["sampling"] == {
        "processLifecycle": "fresh",
        "webView2UserData": "fresh",
        "osFileCache": "not-cleared",
        "firstTableStableWindowMs": 1_000,
        "workingSetQuietWindowMs": 1_000,
    }
    assert "thresholds" not in report
    assert "budgets" not in report
    assert json.loads(report_path.read_text(encoding="utf-8")) == report

    returned_candidate = report["releaseCandidate"]
    original_archive = candidate_evidence["archive"]
    assert isinstance(returned_candidate, dict)
    assert isinstance(returned_candidate["archive"], dict)
    assert isinstance(original_archive, dict)
    returned_candidate["archive"]["name"] = "mutated.zip"
    assert original_archive["name"] == archive.name


def test_packaged_baseline_never_persists_passed_before_normal_cleanup(
    monkeypatch,
    tmp_path: Path,
) -> None:
    package_root = tmp_path / "package"
    archive, identity_path, _evidence = _write_candidate(
        package_root,
        tmp_path / "artifacts",
    )
    session = _Session([])

    @contextmanager
    def failing_exit(
        *_args: object,
        observer: PackagedWorkspacePhaseObserver | None,
        **_kwargs: object,
    ) -> Iterator[_Session]:
        assert observer is not None
        observer.launch_started()
        observer.host_ready()
        observer.workspace_open_requested()
        observer.workspace_opened()
        yield session
        raise RuntimeError("normal cleanup failed")

    monkeypatch.setattr(
        packaged_runtime_baseline,
        "opened_packaged_workspace",
        failing_exit,
    )
    clock = iter((0, 10, 20, 30, 40))
    report_path = tmp_path / "report.json"

    report = packaged_runtime_baseline.run_packaged_runtime_baseline(
        package_root=package_root,
        package_archive=archive,
        workspace_root=tmp_path / "workspace",
        evidence_root=tmp_path / "evidence",
        build_identity_path=identity_path,
        expected_source_sha=SOURCE_SHA,
        json_report=report_path,
        monotonic_ns=lambda: next(clock),
        sleep=lambda _seconds: None,
        probe=lambda *_args: _probe_evidence(),
    )

    persisted = json.loads(report_path.read_text(encoding="utf-8"))
    assert report["status"] == persisted["status"] == "failed"
    assert persisted["measurements"] is None
    assert persisted["sampling"]["processLifecycle"] == "not-measured"
    assert persisted["sampling"]["webView2UserData"] == "not-measured"
    assert persisted["errors"] == [
        {
            "code": "PACKAGED_RUNTIME_BASELINE_FAILED",
            "message": "normal cleanup failed",
        }
    ]


def test_packaged_baseline_rejects_boolean_numeric_probe_fields(
    monkeypatch,
    tmp_path: Path,
) -> None:
    package_root = tmp_path / "package"
    archive, identity_path, _evidence = _write_candidate(
        package_root,
        tmp_path / "artifacts",
    )
    session = _Session([])

    @contextmanager
    def opened(
        *_args: object,
        observer: PackagedWorkspacePhaseObserver | None,
        **_kwargs: object,
    ) -> Iterator[_Session]:
        assert observer is not None
        observer.launch_started()
        observer.host_ready()
        observer.workspace_open_requested()
        observer.workspace_opened()
        yield session
        session.lifecycle = _lifecycle()

    invalid = _probe_evidence()
    invalid["stableWindowMs"] = True
    monkeypatch.setattr(packaged_runtime_baseline, "opened_packaged_workspace", opened)
    clock = iter((0, 10, 20, 30))

    report = packaged_runtime_baseline.run_packaged_runtime_baseline(
        package_root=package_root,
        package_archive=archive,
        workspace_root=tmp_path / "workspace",
        evidence_root=tmp_path / "evidence",
        build_identity_path=identity_path,
        expected_source_sha=SOURCE_SHA,
        json_report=tmp_path / "report.json",
        monotonic_ns=lambda: next(clock),
        sleep=lambda _seconds: None,
        probe=lambda *_args: invalid,
    )

    assert report["status"] == "failed"
    assert report["errors"] == [
        {
            "code": "FIRST_TABLE_PROBE_REPORT_INVALID",
            "message": "first table stable window must be a positive integer",
        }
    ]


def test_first_table_probe_normalizes_nonzero_exit_before_reading_report(
    monkeypatch,
    tmp_path: Path,
) -> None:
    monkeypatch.setattr(packaged_runtime_baseline, "ensure_node", lambda _root: Path("node"))
    monkeypatch.setattr(
        packaged_runtime_baseline.subprocess,
        "run",
        lambda *_args, **_kwargs: subprocess.CompletedProcess([], 1),
    )

    with pytest.raises(packaged_runtime_baseline.BaselineMeasurementError) as raised:
        packaged_runtime_baseline.run_first_table_probe(
            "http://127.0.0.1:9222",
            tmp_path / "missing-report.json",
        )

    assert raised.value.code == "FIRST_TABLE_PROBE_FAILED"


def test_packaged_baseline_rejects_candidate_before_opening_workspace(
    monkeypatch,
    tmp_path: Path,
) -> None:
    package_root = tmp_path / "package"
    archive, identity_path, _evidence = _write_candidate(
        package_root,
        tmp_path / "artifacts",
    )
    (package_root / "resources/web-grid/app.js").write_bytes(b"changed after archive")
    opened = False

    @contextmanager
    def forbidden_open(*_args: object, **_kwargs: object) -> Iterator[object]:
        nonlocal opened
        opened = True
        yield object()

    monkeypatch.setattr(
        packaged_runtime_baseline,
        "opened_packaged_workspace",
        forbidden_open,
    )
    report = packaged_runtime_baseline.run_packaged_runtime_baseline(
        package_root=package_root,
        package_archive=archive,
        workspace_root=tmp_path / "workspace",
        evidence_root=tmp_path / "evidence",
        build_identity_path=identity_path,
        expected_source_sha=SOURCE_SHA,
        json_report=tmp_path / "report.json",
    )

    assert opened is False
    assert report["status"] == "failed"
    errors = report["errors"]
    assert isinstance(errors, list)
    assert isinstance(errors[0], dict)
    assert errors[0]["code"] == "CANDIDATE_PACKAGE_MISMATCH"
