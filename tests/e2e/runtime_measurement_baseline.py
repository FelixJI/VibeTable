"""Closed, deterministic measurements for the packaged runtime baseline.

The module accepts already-owned process evidence and a real package tree. It
does not discover processes by parent PID, infer package identity, or silently
fill missing measurements. Launch orchestration is intentionally kept behind
this small report-building seam.
"""

from __future__ import annotations

import json
import re
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Literal, TypedDict, cast

from qa import release_candidate
from scripts.qa.windows_process_scope import ProcessWorkingSetSnapshot

_RELEASE_FIELDS = ("product", "version", "platform", "architecture")


class ReleaseIdentity(TypedDict):
    product: str
    version: str
    platform: str
    architecture: str


class RuntimeImageNames(TypedDict):
    host: str
    backend: str
    sidecar: str


class ElapsedMeasurements(TypedDict):
    launchToHostReady: int
    workspaceOpenRequestToOpened: int
    workspaceOpenRequestToFirstTableStable: int


class WorkingSetMeasurements(TypedDict):
    host: int
    backend: int
    sidecar: int
    total: int


class PackageSizeMeasurements(TypedDict):
    host: int
    backend: int
    sidecar: int
    webGrid: int
    unassigned: int
    total: int


class BaselineIdentity(TypedDict):
    release: ReleaseIdentity
    publishLayoutProtocolVersion: str


class BaselineCoverage(TypedDict):
    phaseTimeline: Literal["caller-supplied"]
    processWorkingSet: Literal["point-in-time"]
    packageFootprint: Literal["measured"]
    packagedRun: Literal["not-measured"]
    rpcLatency: Literal["not-measured"]
    recovery: Literal["not-measured"]


class FoundationMeasurements(TypedDict):
    elapsedNs: ElapsedMeasurements
    workingSetBytes: WorkingSetMeasurements
    packageBytes: PackageSizeMeasurements


class BaselineErrorEvidence(TypedDict):
    code: str
    message: str


class RuntimeMeasurementFoundationReport(TypedDict):
    contractVersion: Literal["1.0"]
    evidenceKind: Literal["runtime-measurement-foundation"]
    status: Literal["partial"]
    coverage: BaselineCoverage
    identity: BaselineIdentity
    measurements: FoundationMeasurements
    errors: list[BaselineErrorEvidence]


class CandidateIdentity(TypedDict):
    component: str
    repository: str
    sourceSha: str
    version: str
    archiveName: str
    archiveSha256: str


class PackagedRuntimeIdentity(TypedDict):
    candidate: CandidateIdentity
    release: ReleaseIdentity
    publishLayoutProtocolVersion: str


class ReleaseCandidateArchiveEvidence(TypedDict):
    name: str
    rootDirectory: str
    sha256: str
    size: int
    treeSha256: str
    fileCount: int
    checksumFile: str


class ReleaseCandidateEvidence(TypedDict):
    schemaVersion: Literal[2]
    packageTreeSha256: str
    packageFileCount: int
    archive: ReleaseCandidateArchiveEvidence


class BaselineMeasurementError(RuntimeError):
    """A stable fail-closed reason for an unusable baseline measurement."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


class VerifiedRuntimeCandidate:
    """An immutable capability produced only by the authoritative candidate verifier."""

    __slots__ = ("_identity", "_package_root", "_release_candidate")
    _TOKEN = object()

    def __init__(
        self,
        *,
        token: object,
        package_root: Path,
        identity: PackagedRuntimeIdentity,
        candidate: ReleaseCandidateEvidence,
    ) -> None:
        if token is not self._TOKEN:
            raise TypeError("use verify_runtime_candidate()")
        self._package_root = package_root
        self._identity = identity
        self._release_candidate = candidate

    @property
    def package_root(self) -> Path:
        return self._package_root

    @property
    def identity(self) -> PackagedRuntimeIdentity:
        return {
            "candidate": self._identity["candidate"].copy(),
            "release": self._identity["release"].copy(),
            "publishLayoutProtocolVersion": self._identity["publishLayoutProtocolVersion"],
        }

    @property
    def release_candidate(self) -> ReleaseCandidateEvidence:
        return {
            **self._release_candidate,
            "archive": self._release_candidate["archive"].copy(),
        }


@dataclass(frozen=True)
class RuntimePhaseDurations:
    cold_start_ns: int
    workspace_open_ns: int
    first_table_ns: int

    def __post_init__(self) -> None:
        if min(self.cold_start_ns, self.workspace_open_ns, self.first_table_ns) < 0:
            raise BaselineMeasurementError(
                "PHASE_DURATION_INVALID",
                "runtime phase durations must be non-negative",
            )


class RuntimePhaseTimeline:
    """Record the fixed launch/open/first-table sequence with a monotonic clock."""

    _ORDER = (
        "launch",
        "host_ready",
        "workspace_open_requested",
        "workspace_opened",
        "first_table_stable",
    )

    def __init__(self, monotonic_ns: Callable[[], int]) -> None:
        self._clock = monotonic_ns
        self._marks: dict[str, int] = {}

    def _mark(self, name: str) -> None:
        if name in self._marks:
            raise BaselineMeasurementError("PHASE_DUPLICATED", f"phase {name!r} was repeated")
        index = self._ORDER.index(name)
        previous = self._ORDER[index - 1]
        if previous not in self._marks:
            raise BaselineMeasurementError(
                "PHASE_OUT_OF_ORDER",
                f"phase {name!r} requires {previous!r}",
            )
        observed = self._clock()
        if observed < self._marks[previous]:
            raise BaselineMeasurementError(
                "CLOCK_NOT_MONOTONIC",
                f"monotonic clock moved backwards at phase {name!r}",
            )
        self._marks[name] = observed

    def host_ready(self) -> None:
        self._mark("host_ready")

    def launch_started(self) -> None:
        if self._marks:
            raise BaselineMeasurementError(
                "PHASE_DUPLICATED",
                "phase 'launch' was repeated",
            )
        self._marks["launch"] = self._clock()

    def workspace_open_requested(self) -> None:
        self._mark("workspace_open_requested")

    def workspace_opened(self) -> None:
        self._mark("workspace_opened")

    def first_table_stable(self) -> None:
        self._mark("first_table_stable")

    def finish(self) -> RuntimePhaseDurations:
        missing = [name for name in self._ORDER if name not in self._marks]
        if missing:
            raise BaselineMeasurementError(
                "PHASE_INCOMPLETE",
                f"runtime phase timeline is incomplete: {', '.join(missing)}",
            )
        return RuntimePhaseDurations(
            cold_start_ns=self._marks["host_ready"] - self._marks["launch"],
            workspace_open_ns=(
                self._marks["workspace_opened"] - self._marks["workspace_open_requested"]
            ),
            first_table_ns=(
                self._marks["first_table_stable"] - self._marks["workspace_open_requested"]
            ),
        )


def _read_object(path: Path, *, code: str) -> dict[str, object]:
    try:
        decoded = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise BaselineMeasurementError(code, f"cannot read {path.name}: {exc}") from exc
    if not isinstance(decoded, dict):
        raise BaselineMeasurementError(code, f"{path.name} must contain a JSON object")
    if not all(isinstance(key, str) for key in decoded):
        raise BaselineMeasurementError(code, f"{path.name} object keys must be strings")
    return cast(dict[str, object], decoded)


def _release_identity(package_root: Path) -> ReleaseIdentity:
    decoded = _read_object(package_root / "release.json", code="PACKAGE_IDENTITY_INVALID")
    if set(decoded) != set(_RELEASE_FIELDS):
        raise BaselineMeasurementError(
            "PACKAGE_IDENTITY_INVALID",
            "release.json must contain exactly product, version, platform, and architecture",
        )
    values: dict[str, str] = {}
    for field in _RELEASE_FIELDS:
        value = decoded[field]
        if not isinstance(value, str) or not value:
            raise BaselineMeasurementError(
                "PACKAGE_IDENTITY_INVALID",
                f"release.json field {field!r} must be a non-empty string",
            )
        values[field] = value
    if (
        values["product"] != "VibeTable"
        or values["platform"] != "windows"
        or values["architecture"] != "x64"
    ):
        raise BaselineMeasurementError(
            "PACKAGE_IDENTITY_INVALID",
            "release.json does not identify a VibeTable Windows x64 package",
        )
    return {
        "product": values["product"],
        "version": values["version"],
        "platform": values["platform"],
        "architecture": values["architecture"],
    }


def _package_path(package_root: Path, relative: object, *, field: str) -> Path:
    if not isinstance(relative, str) or not relative or "\\" in relative or "\0" in relative:
        raise BaselineMeasurementError(
            "LAYOUT_PATH_INVALID",
            f"publish layout path {field!r} must be a non-empty POSIX relative path",
        )
    try:
        pure = PurePosixPath(relative)
        parts = pure.parts
    except (OSError, ValueError) as exc:
        raise BaselineMeasurementError(
            "LAYOUT_PATH_INVALID",
            f"publish layout path {field!r} is invalid",
        ) from exc
    if (
        not parts
        or pure.is_absolute()
        or any(part in {".", ".."} or ":" in part or "\0" in part for part in parts)
    ):
        raise BaselineMeasurementError(
            "LAYOUT_PATH_INVALID",
            f"publish layout path {field!r} escapes the package root",
        )
    try:
        candidate = package_root.joinpath(*parts).resolve()
    except (OSError, ValueError) as exc:
        raise BaselineMeasurementError(
            "LAYOUT_PATH_INVALID",
            f"publish layout path {field!r} cannot be resolved",
        ) from exc
    if not candidate.is_relative_to(package_root):
        raise BaselineMeasurementError(
            "LAYOUT_PATH_INVALID",
            f"publish layout path {field!r} escapes the package root",
        )
    return candidate


def _is_within(path: Path, root: Path) -> bool:
    return path == root or path.is_relative_to(root)


def _package_sizes(
    package_root: Path,
) -> tuple[str, PackageSizeMeasurements, RuntimeImageNames]:
    layout = _read_object(
        package_root / "resources" / "publish-layout.json",
        code="PACKAGE_LAYOUT_INVALID",
    )
    protocol = layout.get("protocolVersion")
    if not isinstance(protocol, str) or not protocol:
        raise BaselineMeasurementError(
            "PACKAGE_LAYOUT_INVALID",
            "publish-layout.json has no protocolVersion",
        )
    if protocol != "2.0":
        raise BaselineMeasurementError(
            "PACKAGE_LAYOUT_PROTOCOL_UNSUPPORTED",
            f"unsupported publish layout protocol: {protocol}",
        )
    launch_value = layout.get("launch")
    if not isinstance(launch_value, dict) or not all(isinstance(key, str) for key in launch_value):
        raise BaselineMeasurementError(
            "PACKAGE_LAYOUT_INVALID",
            "publish-layout.json has no launch object",
        )
    launch = cast(dict[str, object], launch_value)
    host = _package_path(package_root, launch.get("host"), field="launch.host")
    backend = _package_path(package_root, launch.get("backend"), field="launch.backend")
    sidecar = _package_path(package_root, launch.get("sidecar"), field="launch.sidecar")
    web_grid = _package_path(package_root, launch.get("webGrid"), field="launch.webGrid")
    for path, kind in ((host, "file"), (backend, "file"), (sidecar, "file"), (web_grid, "dir")):
        valid = path.is_file() if kind == "file" else path.is_dir()
        if not valid:
            raise BaselineMeasurementError(
                "LAYOUT_TARGET_MISSING",
                f"publish layout target is missing: {path}",
            )
    directory_groups = {
        "backend": backend.parent,
        "sidecar": sidecar.parent,
        "webGrid": web_grid,
    }
    roots = list(directory_groups.items())
    for index, (left_name, left) in enumerate(roots):
        for right_name, right in roots[index + 1 :]:
            if _is_within(left, right) or _is_within(right, left):
                raise BaselineMeasurementError(
                    "LAYOUT_GROUP_OVERLAP",
                    f"package groups {left_name!r} and {right_name!r} overlap",
                )
    if any(_is_within(host, root) for root in directory_groups.values()):
        raise BaselineMeasurementError(
            "LAYOUT_GROUP_OVERLAP",
            "host executable overlaps another package group",
        )

    sizes: dict[str, int] = dict.fromkeys(
        ("host", "backend", "sidecar", "webGrid", "unassigned"), 0
    )
    for path in sorted(package_root.rglob("*")):
        if path.is_symlink():
            raise BaselineMeasurementError(
                "PACKAGE_SYMLINK_UNSUPPORTED",
                f"package measurement refuses symbolic links: {path}",
            )
        if not path.is_file():
            continue
        resolved = path.resolve()
        if not resolved.is_relative_to(package_root):
            raise BaselineMeasurementError(
                "LAYOUT_PATH_INVALID",
                f"package file escapes the package root: {path}",
            )
        if resolved == host:
            group = "host"
        else:
            matches = [
                name for name, root in directory_groups.items() if _is_within(resolved, root)
            ]
            if len(matches) > 1:
                raise BaselineMeasurementError(
                    "LAYOUT_GROUP_OVERLAP",
                    f"package file belongs to multiple groups: {path}",
                )
            group = matches[0] if matches else "unassigned"
        sizes[group] += path.stat().st_size
    total = sum(sizes.values())
    package_sizes: PackageSizeMeasurements = {
        "host": sizes["host"],
        "backend": sizes["backend"],
        "sidecar": sizes["sidecar"],
        "webGrid": sizes["webGrid"],
        "unassigned": sizes["unassigned"],
        "total": total,
    }
    runtime_images: RuntimeImageNames = {
        "host": host.name.casefold(),
        "backend": backend.name.casefold(),
        "sidecar": sidecar.name.casefold(),
    }
    return protocol, package_sizes, runtime_images


def _working_sets(
    snapshot: ProcessWorkingSetSnapshot,
    runtime_images: RuntimeImageNames,
) -> WorkingSetMeasurements:
    values: dict[str, int] = {}
    for role, image in (
        ("host", runtime_images["host"]),
        ("backend", runtime_images["backend"]),
        ("sidecar", runtime_images["sidecar"]),
    ):
        matches = [
            member for member in snapshot.members if member.executable_name.casefold() == image
        ]
        if not matches:
            raise BaselineMeasurementError(
                "RUNTIME_PROCESS_MISSING",
                f"required runtime process is missing: {image}",
            )
        if len(matches) > 1:
            raise BaselineMeasurementError(
                "RUNTIME_PROCESS_AMBIGUOUS",
                f"required runtime process is not unique: {image}",
            )
        member = matches[0]
        if not member.identity_verified:
            raise BaselineMeasurementError(
                "RUNTIME_PROCESS_UNVERIFIED",
                f"required runtime process identity is unverified: {image}",
            )
        if member.working_set_bytes is None or member.working_set_bytes <= 0:
            raise BaselineMeasurementError(
                "WORKING_SET_UNAVAILABLE",
                f"required runtime Working Set is unavailable: {image}",
            )
        values[role] = member.working_set_bytes
    return {
        "host": values["host"],
        "backend": values["backend"],
        "sidecar": values["sidecar"],
        "total": sum(values.values()),
    }


def build_runtime_measurement_foundation_report(
    *,
    package_root: Path,
    phases: RuntimePhaseDurations,
    working_sets: ProcessWorkingSetSnapshot,
) -> RuntimeMeasurementFoundationReport:
    """Build an explicitly partial report for the reusable measurement foundations."""

    package_root = package_root.resolve()
    if not package_root.is_dir():
        raise BaselineMeasurementError(
            "PACKAGE_ROOT_INVALID",
            f"package root does not exist: {package_root}",
        )
    release = _release_identity(package_root)
    layout_protocol, package_sizes, runtime_images = _package_sizes(package_root)
    point_in_time_working_sets = _working_sets(working_sets, runtime_images)
    return {
        "contractVersion": "1.0",
        "evidenceKind": "runtime-measurement-foundation",
        "status": "partial",
        "coverage": {
            "phaseTimeline": "caller-supplied",
            "processWorkingSet": "point-in-time",
            "packageFootprint": "measured",
            "packagedRun": "not-measured",
            "rpcLatency": "not-measured",
            "recovery": "not-measured",
        },
        "identity": {
            "release": release,
            "publishLayoutProtocolVersion": layout_protocol,
        },
        "measurements": {
            "elapsedNs": {
                "launchToHostReady": phases.cold_start_ns,
                "workspaceOpenRequestToOpened": phases.workspace_open_ns,
                "workspaceOpenRequestToFirstTableStable": phases.first_table_ns,
            },
            "workingSetBytes": point_in_time_working_sets,
            "packageBytes": package_sizes,
        },
        "errors": [],
    }


def _closed_object(
    value: object,
    *,
    fields: tuple[str, ...],
    code: str,
    label: str,
) -> dict[str, object]:
    if not isinstance(value, dict) or not all(isinstance(key, str) for key in value):
        raise BaselineMeasurementError(code, f"{label} must be a JSON object")
    decoded = cast(dict[str, object], value)
    if set(decoded) != set(fields):
        raise BaselineMeasurementError(code, f"{label} fields do not match its contract")
    return decoded


def _candidate_identity(
    path: Path,
    *,
    release: ReleaseIdentity,
    expected_source_sha: str,
    archive_evidence: ReleaseCandidateArchiveEvidence,
) -> CandidateIdentity:
    identity = _read_object(path, code="CANDIDATE_IDENTITY_INVALID")
    schema_version = identity.get("schema_version")
    if type(schema_version) is not int or schema_version != 1:
        raise BaselineMeasurementError(
            "CANDIDATE_IDENTITY_INVALID",
            "build identity schema_version must be 1",
        )
    if set(identity) != {"schema_version", "project", "build"}:
        raise BaselineMeasurementError(
            "CANDIDATE_IDENTITY_INVALID",
            "build identity fields do not match schema 1",
        )
    project = _closed_object(
        identity.get("project"),
        fields=("component", "repository", "version", "source_sha"),
        code="CANDIDATE_IDENTITY_INVALID",
        label="build identity project",
    )
    build = _closed_object(
        identity.get("build"),
        fields=(
            "archive",
            "archive_sha256",
            "package_identity",
            "package_identity_sha256",
        ),
        code="CANDIDATE_IDENTITY_INVALID",
        label="build identity build",
    )
    source_sha = _strict_hex(
        project["source_sha"],
        length=40,
        code="CANDIDATE_IDENTITY_INVALID",
        label="build identity source SHA",
    )
    expected_source_sha = _strict_hex(
        expected_source_sha,
        length=40,
        code="CANDIDATE_SOURCE_MISMATCH",
        label="expected source SHA",
    )
    if source_sha != expected_source_sha:
        raise BaselineMeasurementError(
            "CANDIDATE_SOURCE_MISMATCH",
            "build identity source SHA does not match the gate commit",
        )
    component = project["component"]
    repository = project["repository"]
    version = project["version"]
    if component != "vibetable" or repository != "FelixJI/VibeTable":
        raise BaselineMeasurementError(
            "CANDIDATE_PROJECT_MISMATCH",
            "build identity does not identify FelixJI/VibeTable",
        )
    package_identity = build["package_identity"]
    if package_identity != release or version != release["version"]:
        raise BaselineMeasurementError(
            "CANDIDATE_PACKAGE_MISMATCH",
            "build identity package does not match release.json",
        )
    if not isinstance(version, str) or not version:
        raise BaselineMeasurementError(
            "CANDIDATE_IDENTITY_INVALID",
            "build identity version must be a non-empty string",
        )
    archive = build["archive"]
    expected_archive = f"VibeTable-v{release['version']}-win-x64.zip"
    if archive != expected_archive or archive != archive_evidence["name"]:
        raise BaselineMeasurementError(
            "CANDIDATE_ARCHIVE_MISMATCH",
            "build identity archive name does not match the verified candidate",
        )
    archive_sha256 = _strict_hex(
        build["archive_sha256"],
        length=64,
        code="CANDIDATE_IDENTITY_INVALID",
        label="build identity archive SHA-256",
    )
    _strict_hex(
        build["package_identity_sha256"],
        length=64,
        code="CANDIDATE_IDENTITY_INVALID",
        label="build identity package identity SHA-256",
    )
    if archive_sha256 != archive_evidence["sha256"]:
        raise BaselineMeasurementError(
            "CANDIDATE_ARCHIVE_MISMATCH",
            "build identity archive SHA-256 does not match the verified candidate",
        )
    assert isinstance(component, str)
    assert isinstance(repository, str)
    assert isinstance(archive, str)
    return {
        "component": component,
        "repository": repository,
        "sourceSha": source_sha,
        "version": version,
        "archiveName": archive,
        "archiveSha256": archive_sha256,
    }


def _strict_hex(
    value: object,
    *,
    length: int,
    code: str,
    label: str,
) -> str:
    if not isinstance(value, str) or re.fullmatch(rf"[0-9a-f]{{{length}}}", value) is None:
        raise BaselineMeasurementError(code, f"{label} must be {length} lowercase hex characters")
    return value


def _strict_non_negative_int(value: object, *, label: str) -> int:
    if type(value) is not int or value < 0:
        raise BaselineMeasurementError(
            "CANDIDATE_EVIDENCE_INVALID",
            f"{label} must be a non-negative integer",
        )
    return value


def _verified_candidate_evidence(
    package_root: Path,
    package_archive: Path,
) -> ReleaseCandidateEvidence:
    try:
        raw = release_candidate.candidate_evidence(package_root, package_archive)
    except release_candidate.CandidateError as exc:
        raise BaselineMeasurementError("CANDIDATE_PACKAGE_MISMATCH", str(exc)) from exc
    candidate = _closed_object(
        raw,
        fields=("schemaVersion", "packageTreeSha256", "packageFileCount", "archive"),
        code="CANDIDATE_EVIDENCE_INVALID",
        label="release candidate evidence",
    )
    archive = _closed_object(
        candidate["archive"],
        fields=(
            "name",
            "rootDirectory",
            "sha256",
            "size",
            "treeSha256",
            "fileCount",
            "checksumFile",
        ),
        code="CANDIDATE_EVIDENCE_INVALID",
        label="release candidate archive evidence",
    )
    if candidate["schemaVersion"] != release_candidate.SCHEMA_VERSION:
        raise BaselineMeasurementError(
            "CANDIDATE_EVIDENCE_INVALID",
            "release candidate evidence schema is unsupported",
        )
    package_tree_sha = _strict_hex(
        candidate["packageTreeSha256"],
        length=64,
        code="CANDIDATE_EVIDENCE_INVALID",
        label="package tree SHA-256",
    )
    archive_sha = _strict_hex(
        archive["sha256"],
        length=64,
        code="CANDIDATE_EVIDENCE_INVALID",
        label="archive SHA-256",
    )
    archive_tree_sha = _strict_hex(
        archive["treeSha256"],
        length=64,
        code="CANDIDATE_EVIDENCE_INVALID",
        label="archive tree SHA-256",
    )
    name = archive["name"]
    root_directory = archive["rootDirectory"]
    checksum_file = archive["checksumFile"]
    if (
        not isinstance(name, str)
        or not name
        or not isinstance(root_directory, str)
        or root_directory != release_candidate.ARCHIVE_ROOT_NAME
        or not isinstance(checksum_file, str)
        or checksum_file != f"{name}.sha256"
        or package_tree_sha != archive_tree_sha
    ):
        raise BaselineMeasurementError(
            "CANDIDATE_EVIDENCE_INVALID",
            "release candidate evidence is not internally consistent",
        )
    return {
        "schemaVersion": 2,
        "packageTreeSha256": package_tree_sha,
        "packageFileCount": _strict_non_negative_int(
            candidate["packageFileCount"], label="package file count"
        ),
        "archive": {
            "name": name,
            "rootDirectory": release_candidate.ARCHIVE_ROOT_NAME,
            "sha256": archive_sha,
            "size": _strict_non_negative_int(archive["size"], label="archive size"),
            "treeSha256": archive_tree_sha,
            "fileCount": _strict_non_negative_int(archive["fileCount"], label="archive file count"),
            "checksumFile": checksum_file,
        },
    }


def verify_runtime_candidate(
    *,
    package_root: Path,
    package_archive: Path,
    build_identity_path: Path,
    expected_source_sha: str,
) -> VerifiedRuntimeCandidate:
    """Bind one package tree, its archive, build identity, and source SHA."""

    root = package_root.resolve()
    evidence = _verified_candidate_evidence(root, package_archive.resolve())
    release = _release_identity(root)
    protocol, _package_bytes, _runtime_images = _package_sizes(root)
    candidate = _candidate_identity(
        build_identity_path,
        release=release,
        expected_source_sha=expected_source_sha,
        archive_evidence=evidence["archive"],
    )
    return VerifiedRuntimeCandidate(
        token=VerifiedRuntimeCandidate._TOKEN,
        package_root=root,
        identity={
            "candidate": candidate,
            "release": release,
            "publishLayoutProtocolVersion": protocol,
        },
        candidate=evidence,
    )
