from __future__ import annotations

import json
import re
from collections.abc import Iterator, Mapping
from contextlib import ExitStack, contextmanager
from dataclasses import dataclass
from pathlib import Path
from types import MappingProxyType

from tests.e2e import packaged_host_lifecycle
from tests.e2e import product_e2e_runner as product_runner
from tests.e2e.windows_tcp_listener_owner import WindowsTcpListenerOwnerLease

_READINESS_FILE = "vibetable-readiness.json"


@dataclass(frozen=True)
class ReplicaHostSpec:
    label: str
    runtime_root: Path
    selected_root: Path


@dataclass(frozen=True)
class OpenedReplicaHost:
    label: str
    cdp_url: str
    host_root: Path
    local_data_root: Path
    controls_dir: Path
    evidence_dir: Path
    lifecycle_evidence_path: Path
    webview_user_data_root: Path


class PackagedReplicaHostLifecycleError(RuntimeError):
    def __init__(self, host: OpenedReplicaHost) -> None:
        super().__init__(f"packaged replica host {host.label!r} normal exit failed")
        self.host = host


def _overlap(left: Path, right: Path) -> bool:
    return left == right or left in right.parents or right in left.parents


def _validate(specs: tuple[ReplicaHostSpec, ReplicaHostSpec], evidence_root: Path) -> None:
    if len({spec.label for spec in specs}) != 2 or any(
        re.fullmatch(r"[a-z0-9][a-z0-9-]*", spec.label) is None for spec in specs
    ):
        raise ValueError("specs must identify two distinct simple-slug labels")
    roots = [evidence_root.resolve()]
    for spec in specs:
        if not spec.selected_root.is_dir():
            raise ValueError(f"selected root does not exist: {spec.selected_root}")
        roots.extend((spec.runtime_root.resolve(), spec.selected_root.resolve()))
    for index, root in enumerate(roots):
        for other in roots[index + 1 :]:
            if _overlap(root, other):
                raise ValueError(f"host isolation paths overlap: {root} and {other}")


def _write_json(path: Path, value: Mapping[str, object]) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def _record(path: Path, value: Mapping[str, object], primary: BaseException | None = None) -> None:
    try:
        _write_json(path, value)
    except BaseException as exc:
        if primary is None:
            raise
        primary.add_note(f"writing packaged replica evidence also failed: {exc}")


def _archive_readiness(runtime_root: Path, evidence_dir: Path) -> None:
    source = runtime_root / "host" / _READINESS_FILE
    target = evidence_dir / "readiness.json"
    if not source.is_file():
        raise RuntimeError(f"packaged host did not retain readiness evidence: {source}")
    source.replace(target)


@contextmanager
def _opened_replica_host(
    package_root: Path,
    spec: ReplicaHostSpec,
    evidence_dir: Path,
) -> Iterator[OpenedReplicaHost]:
    readiness_path = spec.runtime_root / "host" / _READINESS_FILE
    if readiness_path.exists():
        raise RuntimeError(f"unarchived readiness evidence blocks restart: {readiness_path}")
    controls_dir = evidence_dir / "controls"
    controls_dir.mkdir(parents=True)
    (controls_dir / "workspace-root.txt").write_text(
        str(spec.selected_root.resolve()) + "\n", encoding="utf-8"
    )
    host: OpenedReplicaHost | None = None
    try:
        scope, port, controls, streams = packaged_host_lifecycle._launch_host(
            package_root,
            spec.runtime_root,
            autostart=False,
            tray_lifecycle=False,
            evidence_root=evidence_dir,
        )
        with packaged_host_lifecycle._scope_lifetime(scope, streams), ExitStack() as owners:
            product_runner._wait_for_cdp(port, scope)
            cdp_owner = WindowsTcpListenerOwnerLease.capture(port)
            owners.enter_context(packaged_host_lifecycle._close_owner_on_primary_error(cdp_owner))
            readiness = product_runner._wait_for_readiness(spec.runtime_root / "host", scope)
            assert readiness.get("ready") is True, readiness
            _archive_readiness(spec.runtime_root, evidence_dir)
            local_data = spec.runtime_root / "host" / "local-data"
            host = OpenedReplicaHost(
                label=spec.label,
                cdp_url=f"http://127.0.0.1:{port}",
                host_root=spec.runtime_root / "host",
                local_data_root=local_data,
                controls_dir=controls,
                evidence_dir=evidence_dir,
                lifecycle_evidence_path=evidence_dir / "lifecycle.json",
                webview_user_data_root=spec.runtime_root / "host" / "webview2-user-data",
            )
            try:
                yield host
            except BaseException as primary:
                try:
                    lifecycle = product_runner._abort_scope(
                        scope,
                        reason="replica host context body failed",
                        cdp_owner=cdp_owner,
                    )
                    _record(evidence_dir / "lifecycle.json", lifecycle, primary)
                except BaseException as cleanup:
                    primary.add_note(f"aborting packaged replica host also failed: {cleanup}")
                raise
            try:
                lifecycle = product_runner._request_normal_exit(
                    scope,
                    controls_dir=controls,
                    cdp_owner=cdp_owner,
                )
            except BaseException as primary:
                _record(
                    evidence_dir / "lifecycle.json",
                    {"status": "failed", "phase": "normal-close", "error": str(primary)},
                    primary,
                )
                raise
            if lifecycle.get("status") != "passed":
                lifecycle_error = PackagedReplicaHostLifecycleError(host)
                _record(evidence_dir / "lifecycle.json", lifecycle, lifecycle_error)
                raise lifecycle_error
            _record(evidence_dir / "lifecycle.json", lifecycle)
    except BaseException as exc:
        if host is None:
            _record(
                evidence_dir / "lifecycle.json",
                {"status": "failed", "phase": "startup", "error": str(exc)},
                exc,
            )
        raise


@contextmanager
def opened_replica_hosts(
    package_root: Path,
    evidence_root: Path,
    specs: tuple[ReplicaHostSpec, ReplicaHostSpec],
) -> Iterator[Mapping[str, OpenedReplicaHost]]:
    """Keep ``runtime_root`` stable while each fresh evidence root owns logs and controls.

    The observed transient readiness file is moved into that invocation's evidence.
    """

    if evidence_root.exists():
        raise ValueError(f"evidence root must be fresh: {evidence_root}")
    _validate(specs, evidence_root)
    evidence_root.mkdir(parents=True)
    with ExitStack() as hosts:
        opened = {
            spec.label: hosts.enter_context(
                _opened_replica_host(package_root, spec, evidence_root / "hosts" / spec.label)
            )
            for spec in specs
        }
        yield MappingProxyType(opened)
