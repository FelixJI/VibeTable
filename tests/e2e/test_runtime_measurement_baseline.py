from __future__ import annotations

import json
from collections.abc import Iterator
from pathlib import Path

import pytest

from scripts.qa.windows_process_scope import (
    ProcessWorkingSetMember,
    ProcessWorkingSetSnapshot,
)
from tests.e2e.runtime_measurement_baseline import (
    BaselineMeasurementError,
    RuntimePhaseDurations,
    RuntimePhaseTimeline,
    build_runtime_measurement_foundation_report,
)


class _Clock:
    def __init__(self, values: list[int]) -> None:
        self._values: Iterator[int] = iter(values)

    def monotonic_ns(self) -> int:
        return next(self._values)


def _write_candidate(
    package_root: Path,
    *,
    web_grid: str = "resources/web-grid",
    protocol: str = "2.0",
    host_name: str = "VibeTable.Next.exe",
    backend_name: str = "vibetable-backend.exe",
    sidecar_name: str = "vibetable-pb.exe",
) -> None:
    files = {
        host_name: b"h" * 10,
        f"resources/backend/{backend_name}": b"b" * 20,
        "resources/backend/runtime.dll": b"r" * 3,
        f"resources/sidecar/{sidecar_name}": b"s" * 30,
        "resources/web-grid/app.js": b"w" * 40,
        "resources/contracts/v2/catalog.json": b"c" * 5,
    }
    for relative, content in files.items():
        target = package_root / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)
    (package_root / "release.json").write_text(
        json.dumps(
            {
                "product": "VibeTable",
                "version": "0.5.1",
                "platform": "windows",
                "architecture": "x64",
            }
        ),
        encoding="utf-8",
    )
    (package_root / "resources/publish-layout.json").write_text(
        json.dumps(
            {
                "protocolVersion": protocol,
                "launch": {
                    "host": host_name,
                    "backend": f"resources/backend/{backend_name}",
                    "sidecar": f"resources/sidecar/{sidecar_name}",
                    "webGrid": web_grid,
                    "previewHost": host_name,
                },
            }
        ),
        encoding="utf-8",
    )


def _phases() -> RuntimePhaseDurations:
    timeline = RuntimePhaseTimeline(_Clock([1_000, 3_000, 4_000, 9_000, 12_000]).monotonic_ns)
    timeline.host_ready()
    timeline.workspace_open_requested()
    timeline.workspace_opened()
    timeline.first_table_stable()
    return timeline.finish()


def _working_sets(*members: ProcessWorkingSetMember) -> ProcessWorkingSetSnapshot:
    return ProcessWorkingSetSnapshot(
        members
        or (
            ProcessWorkingSetMember(10, "VibeTable.Next.exe", True, 100),
            ProcessWorkingSetMember(11, "vibetable-backend.exe", True, 200),
            ProcessWorkingSetMember(12, "vibetable-pb.exe", True, 300),
            ProcessWorkingSetMember(13, "msedgewebview2.exe", True, 400),
        )
    )


def test_report_has_closed_identity_timing_working_set_and_package_groups(tmp_path: Path) -> None:
    package_root = tmp_path / "VibeTable.Next"
    _write_candidate(package_root)

    report = build_runtime_measurement_foundation_report(
        package_root=package_root,
        phases=_phases(),
        working_sets=_working_sets(),
    )

    assert set(report) == {
        "contractVersion",
        "evidenceKind",
        "status",
        "coverage",
        "identity",
        "measurements",
        "errors",
    }
    assert report["status"] == "partial"
    assert report["coverage"] == {
        "phaseTimeline": "caller-supplied",
        "processWorkingSet": "point-in-time",
        "packageFootprint": "measured",
        "packagedRun": "not-measured",
        "rpcLatency": "not-measured",
        "recovery": "not-measured",
    }
    assert report["identity"] == {
        "release": {
            "product": "VibeTable",
            "version": "0.5.1",
            "platform": "windows",
            "architecture": "x64",
        },
        "publishLayoutProtocolVersion": "2.0",
    }
    assert report["measurements"]["elapsedNs"] == {
        "launchToHostReady": 2_000,
        "workspaceOpenRequestToOpened": 5_000,
        "workspaceOpenRequestToFirstTableStable": 8_000,
    }
    assert report["measurements"]["workingSetBytes"] == {
        "host": 100,
        "backend": 200,
        "sidecar": 300,
        "total": 600,
    }
    package_sizes = report["measurements"]["packageBytes"]
    assert set(package_sizes) == {
        "host",
        "backend",
        "sidecar",
        "webGrid",
        "unassigned",
        "total",
    }
    assert package_sizes["host"] == 10
    assert package_sizes["backend"] == 23
    assert package_sizes["sidecar"] == 30
    assert package_sizes["webGrid"] == 40
    assert package_sizes["total"] == sum(
        path.stat().st_size for path in package_root.rglob("*") if path.is_file()
    )
    assert (
        sum(package_sizes[name] for name in package_sizes if name != "total")
        == package_sizes["total"]
    )


def test_timeline_rejects_duplicate_out_of_order_and_non_monotonic_marks() -> None:
    duplicate = RuntimePhaseTimeline(_Clock([10, 20, 30]).monotonic_ns)
    duplicate.host_ready()
    with pytest.raises(BaselineMeasurementError) as duplicate_error:
        duplicate.host_ready()
    assert duplicate_error.value.code == "PHASE_DUPLICATED"

    out_of_order = RuntimePhaseTimeline(_Clock([10, 20]).monotonic_ns)
    with pytest.raises(BaselineMeasurementError) as order_error:
        out_of_order.workspace_open_requested()
    assert order_error.value.code == "PHASE_OUT_OF_ORDER"

    reversed_clock = RuntimePhaseTimeline(_Clock([20, 10]).monotonic_ns)
    with pytest.raises(BaselineMeasurementError) as clock_error:
        reversed_clock.host_ready()
    assert clock_error.value.code == "CLOCK_NOT_MONOTONIC"


@pytest.mark.parametrize(
    ("members", "code"),
    [
        (
            (
                ProcessWorkingSetMember(10, "VibeTable.Next.exe", True, 100),
                ProcessWorkingSetMember(12, "vibetable-pb.exe", True, 300),
            ),
            "RUNTIME_PROCESS_MISSING",
        ),
        (
            (
                ProcessWorkingSetMember(10, "VibeTable.Next.exe", True, 100),
                ProcessWorkingSetMember(11, "vibetable-backend.exe", True, 200),
                ProcessWorkingSetMember(14, "vibetable-backend.exe", True, 250),
                ProcessWorkingSetMember(12, "vibetable-pb.exe", True, 300),
            ),
            "RUNTIME_PROCESS_AMBIGUOUS",
        ),
        (
            (
                ProcessWorkingSetMember(10, "VibeTable.Next.exe", True, 100),
                ProcessWorkingSetMember(11, "vibetable-backend.exe", False, None),
                ProcessWorkingSetMember(12, "vibetable-pb.exe", True, 300),
            ),
            "RUNTIME_PROCESS_UNVERIFIED",
        ),
        (
            (
                ProcessWorkingSetMember(10, "VibeTable.Next.exe", True, 100),
                ProcessWorkingSetMember(11, "vibetable-backend.exe", True, None),
                ProcessWorkingSetMember(12, "vibetable-pb.exe", True, 300),
            ),
            "WORKING_SET_UNAVAILABLE",
        ),
    ],
)
def test_report_fails_closed_for_missing_ambiguous_or_unmeasured_runtime(
    tmp_path: Path,
    members: tuple[ProcessWorkingSetMember, ...],
    code: str,
) -> None:
    package_root = tmp_path / "VibeTable.Next"
    _write_candidate(package_root)

    with pytest.raises(BaselineMeasurementError) as error:
        build_runtime_measurement_foundation_report(
            package_root=package_root,
            phases=_phases(),
            working_sets=_working_sets(*members),
        )

    assert error.value.code == code


@pytest.mark.parametrize(
    ("web_grid", "code"),
    [
        ("../outside", "LAYOUT_PATH_INVALID"),
        ("C:/outside", "LAYOUT_PATH_INVALID"),
        (".", "LAYOUT_PATH_INVALID"),
        ("foo:/bar", "LAYOUT_PATH_INVALID"),
        ("bad\0path", "LAYOUT_PATH_INVALID"),
        ("resources/backend", "LAYOUT_GROUP_OVERLAP"),
    ],
)
def test_report_rejects_unsafe_or_overlapping_package_layout(
    tmp_path: Path,
    web_grid: str,
    code: str,
) -> None:
    package_root = tmp_path / "VibeTable.Next"
    _write_candidate(package_root, web_grid=web_grid)

    with pytest.raises(BaselineMeasurementError) as error:
        build_runtime_measurement_foundation_report(
            package_root=package_root,
            phases=_phases(),
            working_sets=_working_sets(),
        )

    assert error.value.code == code


def test_report_derives_required_process_images_from_the_package_layout(tmp_path: Path) -> None:
    package_root = tmp_path / "VibeTable.Next"
    _write_candidate(
        package_root,
        host_name="Renamed.Host.exe",
        backend_name="renamed-backend.exe",
        sidecar_name="renamed-sidecar.exe",
    )
    working_sets = ProcessWorkingSetSnapshot(
        (
            ProcessWorkingSetMember(10, "RENAMED.HOST.EXE", True, 100),
            ProcessWorkingSetMember(11, "renamed-backend.exe", True, 200),
            ProcessWorkingSetMember(12, "renamed-sidecar.exe", True, 300),
        )
    )

    report = build_runtime_measurement_foundation_report(
        package_root=package_root,
        phases=_phases(),
        working_sets=working_sets,
    )

    assert report["measurements"]["workingSetBytes"]["total"] == 600


def test_report_rejects_unknown_publish_layout_protocol(tmp_path: Path) -> None:
    package_root = tmp_path / "VibeTable.Next"
    _write_candidate(package_root, protocol="3.0")

    with pytest.raises(BaselineMeasurementError) as error:
        build_runtime_measurement_foundation_report(
            package_root=package_root,
            phases=_phases(),
            working_sets=_working_sets(),
        )

    assert error.value.code == "PACKAGE_LAYOUT_PROTOCOL_UNSUPPORTED"
