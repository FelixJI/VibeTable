"""Cross-component application and pinned sidecar release versions."""

from __future__ import annotations

import hashlib
import json
import os
import re
import tomllib
from dataclasses import dataclass
from pathlib import Path

SEMVER_RE = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")
VERSION_SOURCE = Path("backend/_version.py")
WORKSPACE_VERSION_POLICY = Path("contracts/v2/workspace-version-policy.json")
WORKSPACE_VERSION_POLICY_SCHEMA = Path("contracts/v2/workspace-version-policy.schema.json")
WORKSPACE_COMPATIBILITY_CORPUS = Path("contracts/v2/compatibility-corpus.json")


class VersionError(ValueError):
    """Version metadata is missing, malformed, or inconsistent."""


@dataclass(frozen=True)
class VersionSnapshot:
    expected: str
    actual: dict[str, str]

    @property
    def mismatches(self) -> dict[str, str]:
        return {name: value for name, value in self.actual.items() if value != self.expected}


@dataclass(frozen=True)
class ReleaseVersions:
    app: str
    pocketbase: str
    cel: str
    contract: str
    schema: str
    migration_hash: str


def validate_version(value: str) -> str:
    if not SEMVER_RE.fullmatch(value):
        raise VersionError(f"invalid version {value!r}; expected MAJOR.MINOR.PATCH")
    return value


def read_project_version(repo_root: Path) -> str:
    source = (repo_root / VERSION_SOURCE).read_text(encoding="utf-8")
    return validate_version(
        _extract(
            r'^__version__\s*=\s*"([^"]+)"',
            source,
            "backend._version.__version__",
        )
    )


def _json(path: Path) -> dict:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise VersionError(f"{path} must contain a JSON object")
    return value


def _extract(pattern: str, text: str, label: str) -> str:
    match = re.search(pattern, text, flags=re.MULTILINE)
    if match is None:
        raise VersionError(f"unable to read {label}")
    return match.group(1)


def collect_release_versions(repo_root: Path) -> ReleaseVersions:
    buildinfo = (repo_root / "sidecar" / "internal" / "buildinfo" / "info.go").read_text(
        encoding="utf-8"
    )
    migration_manifest = repo_root / "sidecar" / "migrations" / "manifest.json"
    return ReleaseVersions(
        app=read_project_version(repo_root),
        pocketbase=_extract(
            r'^\s*PocketBaseVersion\s*=\s*"([^"]+)"',
            buildinfo,
            "PocketBase sidecar version",
        ),
        cel=_extract(
            r'^\s*CELVersion\s*=\s*"([^"]+)"',
            buildinfo,
            "CEL sidecar version",
        ),
        contract=_extract(
            r'^\s*ContractVersion\s*=\s*"([^"]+)"',
            buildinfo,
            "sidecar contract version",
        ),
        schema=_extract(
            r'^\s*SchemaVersion\s*=\s*"([^"]+)"',
            buildinfo,
            "sidecar schema version",
        ),
        migration_hash=hashlib.sha256(migration_manifest.read_bytes()).hexdigest(),
    )


def collect_versions(repo_root: Path) -> VersionSnapshot:
    expected = read_project_version(repo_root)
    layout = _json(repo_root / "desktop" / "publish-layout.json")
    workspace_policy = _json(repo_root / WORKSPACE_VERSION_POLICY)
    components = layout.get("components", {})
    actual = {
        "layout host": str(components.get("host", {}).get("version", "")),
        "layout backend": str(components.get("backend", {}).get("version", "")),
        "layout web": str(components.get("web", {}).get("version", "")),
        "layout sidecar": str(components.get("sidecar", {}).get("version", "")),
        "workspace policy current writer": str(
            workspace_policy.get("currentWriter", {}).get("appVersion", "")
        ),
    }
    return VersionSnapshot(expected=expected, actual=actual)


def check_release_dependency_versions(repo_root: Path) -> list[str]:
    versions = collect_release_versions(repo_root)
    go_mod = (repo_root / "sidecar" / "go.mod").read_text(encoding="utf-8")
    errors: list[str] = []
    for module, expected in (
        ("github.com/pocketbase/pocketbase", f"v{versions.pocketbase}"),
        ("github.com/google/cel-go", f"v{versions.cel}"),
    ):
        match = re.search(
            rf"^\s*{re.escape(module)}\s+(v[^\s]+)(?:\s+//.*)?$",
            go_mod,
            flags=re.MULTILINE,
        )
        actual = match.group(1) if match is not None else "missing"
        if actual != expected:
            errors.append(
                f"sidecar go.mod dependency version mismatch: {module} "
                f"(expected {expected}, got {actual})"
            )
    return errors


def is_complete_formal_release_evidence(entry: object, corpus_root: Path | None) -> bool:
    if not isinstance(entry, dict) or set(entry) != {
        "writerVersion",
        "sourceRelease",
        "artifacts",
        "cases",
    }:
        return False
    version = entry.get("writerVersion")
    source = entry.get("sourceRelease")
    artifacts = entry.get("artifacts")
    cases = entry.get("cases")
    if (
        corpus_root is None
        or not isinstance(version, str)
        or not isinstance(source, dict)
        or set(source) != {"tag", "sourceCommit", "assetName"}
        or source.get("tag") != f"v{version}"
        or not re.fullmatch(r"[0-9a-f]{40}", str(source.get("sourceCommit", "")))
        or not isinstance(source.get("assetName"), str)
        or not source["assetName"]
        or not isinstance(artifacts, list)
        or not isinstance(cases, list)
    ):
        return False
    artifact_kinds: set[str] = set()
    artifact_kinds_by_id: dict[str, str] = {}
    artifact_paths: set[Path] = set()
    artifact_digests: set[str] = set()
    for artifact in artifacts:
        if not isinstance(artifact, dict) or set(artifact) != {"id", "kind", "path", "sha256"}:
            return False
        artifact_id = artifact.get("id")
        kind = artifact.get("kind")
        relative = Path(str(artifact.get("path", "")))
        digest = artifact.get("sha256")
        if (
            not isinstance(artifact_id, str)
            or not artifact_id
            or artifact_id in artifact_kinds_by_id
            or kind not in {"workspace-archive", "snapshot-package"}
            or relative.is_absolute()
            or ".." in relative.parts
            or not re.fullmatch(r"[0-9a-f]{64}", str(digest))
        ):
            return False
        path = (corpus_root / relative).resolve()
        try:
            path.relative_to(corpus_root.resolve())
        except ValueError:
            return False
        if (
            path in artifact_paths
            or str(digest) in artifact_digests
            or not path.is_file()
            or hashlib.sha256(path.read_bytes()).hexdigest() != digest
        ):
            return False
        artifact_kinds_by_id[artifact_id] = str(kind)
        artifact_kinds.add(str(kind))
        artifact_paths.add(path)
        artifact_digests.add(str(digest))
    observed_cases: set[tuple[str, str]] = set()
    referenced_artifact_ids: set[str] = set()
    success_artifact_ids: set[str] = set()
    rejected_artifact_ids: set[str] = set()
    for case in cases:
        if not isinstance(case, dict) or set(case) != {"operation", "artifactId", "expected"}:
            return False
        artifact_id = case.get("artifactId")
        operation = case.get("operation")
        expected = case.get("expected")
        if not isinstance(artifact_id, str) or artifact_id not in artifact_kinds_by_id:
            return False
        required_kind = {
            "workspace.open": "workspace-archive",
            "snapshot.import": "snapshot-package",
        }.get(str(operation))
        if required_kind is None or artifact_kinds_by_id[artifact_id] != required_kind:
            return False
        referenced_artifact_ids.add(artifact_id)
        if expected in {"read", "migrate"}:
            success_artifact_ids.add(str(artifact_id))
            observed_cases.add((str(operation), "read-or-migrate"))
        elif expected == "reject-zero-write" and operation in {"workspace.open", "snapshot.import"}:
            rejected_artifact_ids.add(str(artifact_id))
            observed_cases.add(("reject", "reject-zero-write"))
        else:
            return False
    return (
        artifact_kinds == {"workspace-archive", "snapshot-package"}
        and {
            ("workspace.open", "read-or-migrate"),
            ("snapshot.import", "read-or-migrate"),
            ("reject", "reject-zero-write"),
        }
        <= observed_cases
        and referenced_artifact_ids == set(artifact_kinds_by_id)
        and rejected_artifact_ids.isdisjoint(success_artifact_ids)
    )


def _json_schema_type_matches(value: object, expected: str) -> bool:
    return {
        "array": isinstance(value, list),
        "boolean": isinstance(value, bool),
        "integer": isinstance(value, int) and not isinstance(value, bool),
        "number": isinstance(value, (int, float)) and not isinstance(value, bool),
        "object": isinstance(value, dict),
        "string": isinstance(value, str),
    }.get(expected, False)


def _validate_json_schema_instance(
    instance: object,
    schema: object,
    root: dict,
    path: str = "$",
) -> None:
    if not isinstance(schema, dict):
        raise VersionError(f"{path}: JSON Schema node must be an object")
    reference = schema.get("$ref")
    if reference is not None:
        prefix = "#/$defs/"
        if not isinstance(reference, str) or not reference.startswith(prefix):
            raise VersionError(f"{path}: unsupported JSON Schema reference")
        definitions = root.get("$defs")
        target = (
            definitions.get(reference.removeprefix(prefix))
            if isinstance(definitions, dict)
            else None
        )
        if not isinstance(target, dict):
            raise VersionError(f"{path}: unresolved JSON Schema reference")
        _validate_json_schema_instance(instance, target, root, path)
        return

    expected_type = schema.get("type")
    if isinstance(expected_type, str) and not _json_schema_type_matches(instance, expected_type):
        raise VersionError(f"{path}: expected JSON type {expected_type}")
    if "const" in schema and instance != schema["const"]:
        raise VersionError(f"{path}: value does not match const")
    enum = schema.get("enum")
    if isinstance(enum, list) and instance not in enum:
        raise VersionError(f"{path}: value is outside the closed enum")

    if isinstance(instance, str):
        pattern = schema.get("pattern")
        if isinstance(pattern, str) and re.search(pattern, instance) is None:
            raise VersionError(f"{path}: string does not match pattern")
    elif isinstance(instance, (int, float)) and not isinstance(instance, bool):
        minimum = schema.get("minimum")
        if isinstance(minimum, (int, float)) and instance < minimum:
            raise VersionError(f"{path}: number is below minimum")
    elif isinstance(instance, list):
        minimum_items = schema.get("minItems")
        if isinstance(minimum_items, int) and len(instance) < minimum_items:
            raise VersionError(f"{path}: array is shorter than minItems")
        if schema.get("uniqueItems") is True:
            canonical = [json.dumps(item, sort_keys=True) for item in instance]
            if len(canonical) != len(set(canonical)):
                raise VersionError(f"{path}: array items are not unique")
        item_schema = schema.get("items")
        if item_schema is not None:
            for index, item in enumerate(instance):
                _validate_json_schema_instance(item, item_schema, root, f"{path}[{index}]")
    elif isinstance(instance, dict):
        required = schema.get("required")
        if isinstance(required, list):
            missing = [key for key in required if key not in instance]
            if missing:
                raise VersionError(f"{path}: missing required property {missing[0]!r}")
        properties = schema.get("properties")
        if not isinstance(properties, dict):
            properties = {}
        if schema.get("additionalProperties") is False:
            extra = sorted(set(instance) - set(properties))
            if extra:
                raise VersionError(f"{path}: unexpected property {extra[0]!r}")
        for key, value in instance.items():
            child = properties.get(key)
            if isinstance(child, dict):
                _validate_json_schema_instance(value, child, root, f"{path}.{key}")


def validate_workspace_version_policy_document(
    policy: dict,
    schema: dict,
    corpus: dict | None = None,
    corpus_root: Path | None = None,
) -> list[str]:
    """Validate the closed policy interface and its cross-field relations."""
    try:
        _validate_json_schema_instance(policy, schema, schema)
    except VersionError as error:
        return [f"workspace version policy violates its closed schema: {error}"]
    return validate_workspace_version_policy(policy, corpus, corpus_root)


def validate_workspace_version_policy(
    policy: dict, corpus: dict | None = None, corpus_root: Path | None = None
) -> list[str]:
    """Validate cross-field relations that JSON Schema cannot express."""
    current_writer = policy.get("currentWriter")
    compatibility = policy.get("writerCompatibility")
    workspace_manifest = policy.get("workspaceManifest")
    snapshot_package = policy.get("snapshotPackage")
    if not all(
        isinstance(section, dict)
        for section in (
            current_writer,
            compatibility,
            workspace_manifest,
            snapshot_package,
        )
    ):
        return ["workspace version policy sections must be objects"]

    accepted = compatibility.get("accepted")
    pending = compatibility.get("pending")
    if not isinstance(accepted, list) or not isinstance(pending, list):
        return ["workspace version policy writer lists must be arrays"]

    errors: list[str] = []
    if compatibility.get("verificationGate") != "disabled-until-packaged-runtime-evidence":
        errors.append(
            "workspace compatibility verification gate must remain disabled "
            "until packaged runtime evidence"
        )
    current_entries = [
        entry for entry in accepted if isinstance(entry, dict) and entry.get("status") == "current"
    ]
    if len(current_entries) != 1:
        errors.append("workspace policy must have exactly one accepted current writer")
    elif current_entries[0].get("appVersion") != current_writer.get("appVersion"):
        errors.append("accepted current writer must match currentWriter.appVersion")

    if len(pending) > 1:
        errors.append("workspace policy must have at most one pending writer")
    target = compatibility.get("nMinusOneTarget")
    pending_versions = {entry.get("appVersion") for entry in pending if isinstance(entry, dict)}
    accepted_versions = {entry.get("appVersion") for entry in accepted if isinstance(entry, dict)}
    declared_version_values = [
        entry.get("appVersion") for entry in (*accepted, *pending) if isinstance(entry, dict)
    ]
    if len(declared_version_values) != len(set(declared_version_values)):
        errors.append("writer compatibility appVersion values must be unique")
    if accepted_versions & pending_versions:
        errors.append("accepted and pending writer versions must be disjoint")
    if len(pending) == 1 and target not in pending_versions:
        errors.append("pending writer must be the nMinusOneTarget")
    target_occurrences = sum(
        1
        for entry in (*accepted, *pending)
        if isinstance(entry, dict) and entry.get("appVersion") == target
    )
    if target_occurrences != 1:
        errors.append("nMinusOneTarget must appear exactly once in accepted or pending")
    if accepted_versions | pending_versions != {current_writer.get("appVersion"), target}:
        errors.append(
            "writer compatibility versions must be exactly currentWriter and nMinusOneTarget"
        )

    verified_versions = {
        entry.get("appVersion")
        for entry in accepted
        if isinstance(entry, dict) and entry.get("status") == "verified"
    }
    if verified_versions:
        errors.append(
            "workspace compatibility promotion is disabled until packaged runtime evidence"
        )
    compatibility_corpus = policy.get("compatibilityCorpus")
    immutable = (
        compatibility_corpus.get("immutablePrefix")
        if isinstance(compatibility_corpus, dict)
        else None
    )
    formal_release_count = (
        immutable.get("formalReleaseCount") if isinstance(immutable, dict) else None
    )
    formal_releases = corpus.get("previousFormalReleases") if isinstance(corpus, dict) else None
    if (
        not isinstance(formal_release_count, int)
        or isinstance(formal_release_count, bool)
        or formal_release_count < 0
        or not isinstance(formal_releases, list)
        or formal_release_count > len(formal_releases)
    ):
        errors.append("formal release corpus evidence prefix is invalid")
    else:
        frozen_entries = formal_releases[:formal_release_count]
        complete_entries = [
            entry
            for entry in frozen_entries
            if is_complete_formal_release_evidence(entry, corpus_root)
        ]
        if len(complete_entries) != len(frozen_entries):
            errors.append("formal release corpus evidence prefix is invalid")
        frozen_versions = {entry.get("writerVersion") for entry in complete_entries}
        if not verified_versions <= frozen_versions:
            errors.append("formal release evidence is incomplete")
    try:
        current_version = tuple(
            int(part)
            for part in validate_version(str(current_writer.get("appVersion", ""))).split(".")
        )
        target_version = tuple(int(part) for part in validate_version(str(target)).split("."))
        if target_version >= current_version:
            errors.append("nMinusOneTarget must precede current writer")
    except VersionError:
        errors.append("workspace writer versions must use MAJOR.MINOR.PATCH")

    if current_writer.get("workspaceManifestFormat") != workspace_manifest.get("supportedFormat"):
        errors.append("workspace manifest format must match current writer")
    if current_writer.get("snapshotPackageFormat") != snapshot_package.get("currentFormat"):
        errors.append("snapshot package format must match current writer")
    if snapshot_package.get("minimumSupportedFormat") != snapshot_package.get("currentFormat"):
        errors.append("snapshot package minimum must equal its only supported format")
    return errors


def check_versions(repo_root: Path) -> list[str]:
    snapshot = collect_versions(repo_root)
    errors = [
        f"{name}: {actual!r}, expected {snapshot.expected!r}"
        for name, actual in snapshot.mismatches.items()
    ]
    errors.extend(check_release_dependency_versions(repo_root))
    errors.extend(
        validate_workspace_version_policy_document(
            _json(repo_root / WORKSPACE_VERSION_POLICY),
            _json(repo_root / WORKSPACE_VERSION_POLICY_SCHEMA),
            _json(repo_root / WORKSPACE_COMPATIBILITY_CORPUS),
            repo_root / "contracts" / "v2",
        )
    )
    pyproject = (repo_root / "pyproject.toml").read_text(encoding="utf-8")
    if 'dynamic = ["version"]' not in pyproject or (
        'version = {attr = "backend._version.__version__"}' not in pyproject
    ):
        errors.append("pyproject.toml must derive version from backend._version.__version__")
    backend_contract = (repo_root / "backend" / "contracts" / "system.py").read_text(
        encoding="utf-8"
    )
    if (
        "from backend._version import __version__" not in backend_contract
        or "BACKEND_VERSION: Final[str] = __version__" not in backend_contract
    ):
        errors.append("backend handshake must derive version from backend._version")
    props = (repo_root / "desktop" / "Directory.Build.props").read_text(encoding="utf-8")
    if (
        "backend\\_version.py" not in props
        or "System.Text.RegularExpressions.Regex" not in props
        or re.search(r"<Version>\d+\.\d+\.\d+</Version>", props)
    ):
        errors.append("desktop assembly must derive version from backend/_version.py")
    supervisor = (
        repo_root
        / "desktop"
        / "src"
        / "VibeTable.Infrastructure"
        / "Backend"
        / "PythonBackendSupervisor.cs"
    ).read_text(encoding="utf-8")
    if "ApplicationVersion.FromAssembly" not in supervisor:
        errors.append("desktop backend handshake must use assembly informational version")
    for relative in (
        Path("desktop/web-grid/package.json"),
        Path("desktop/web-grid/package-lock.json"),
    ):
        package = _json(repo_root / relative)
        if "version" in package or (
            relative.name == "package-lock.json"
            and "version" in package.get("packages", {}).get("", {})
        ):
            errors.append(f"{relative.as_posix()} must not duplicate the application version")
    with (repo_root / "uv.lock").open("rb") as stream:
        uv_lock = tomllib.load(stream)
    editable_package = next(
        (
            package
            for package in uv_lock.get("package", [])
            if package.get("name") == "vibetable"
            and package.get("source", {}).get("editable") == "."
        ),
        None,
    )
    if editable_package is None or "version" in editable_package:
        errors.append("uv.lock editable package must derive the dynamic application version")
    return errors


def bump_version(current: str, part: str) -> str:
    major, minor, patch = (int(value) for value in validate_version(current).split("."))
    if part == "major":
        return f"{major + 1}.0.0"
    if part == "minor":
        return f"{major}.{minor + 1}.0"
    if part == "patch":
        return f"{major}.{minor}.{patch + 1}"
    raise VersionError(f"unknown version part: {part}")


def _replace_once(text: str, pattern: str, replacement: str, label: str) -> str:
    updated, count = re.subn(
        pattern,
        replacement,
        text,
        count=1,
        flags=re.MULTILINE,
    )
    if count != 1:
        raise VersionError(f"unable to update {label}")
    return updated


def _render_json(value: dict) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2) + "\n"


def _updated_contents(repo_root: Path, version: str) -> dict[Path, str]:
    version = validate_version(version)
    changes: dict[Path, str] = {}
    version_source = repo_root / VERSION_SOURCE
    changes[version_source] = _replace_once(
        version_source.read_text(encoding="utf-8"),
        r'^(__version__\s*=\s*)"[^"]+"',
        rf'\g<1>"{version}"',
        "application version source",
    )
    layout_path = repo_root / "desktop" / "publish-layout.json"
    layout = _json(layout_path)
    components = layout.setdefault("components", {})
    for name in ("host", "backend", "web"):
        components[name] = {"version": version}
    sidecar = components.setdefault("sidecar", {})
    sidecar["version"] = version
    changes[layout_path] = _render_json(layout)
    policy_path = repo_root / WORKSPACE_VERSION_POLICY
    policy = _json(policy_path)
    current_writer = policy.get("currentWriter")
    compatibility = policy.get("writerCompatibility")
    if not isinstance(current_writer, dict) or not isinstance(compatibility, dict):
        raise VersionError("workspace version policy is missing writer metadata")
    previous_writer = validate_version(str(current_writer.get("appVersion", "")))
    accepted = compatibility.get("accepted")
    if not isinstance(accepted, list):
        raise VersionError("workspace version policy accepted writers must be an array")
    current_entries = [
        entry for entry in accepted if isinstance(entry, dict) and entry.get("status") == "current"
    ]
    if len(current_entries) != 1 or current_entries[0].get("appVersion") != previous_writer:
        raise VersionError(
            "workspace version policy must have exactly one current writer matching currentWriter"
        )
    current_writer["appVersion"] = version
    current_entries[0]["appVersion"] = version
    policy_errors = validate_workspace_version_policy_document(
        policy,
        _json(repo_root / WORKSPACE_VERSION_POLICY_SCHEMA),
        _json(repo_root / WORKSPACE_COMPATIBILITY_CORPUS),
        repo_root / "contracts" / "v2",
    )
    if policy_errors:
        raise VersionError("invalid workspace version policy: " + "; ".join(policy_errors))
    changes[policy_path] = _render_json(policy)
    return changes


def update_versions(
    repo_root: Path,
    version: str,
    *,
    dry_run: bool = False,
) -> list[Path]:
    changes = _updated_contents(repo_root.resolve(), version)
    changed = [
        path for path, content in changes.items() if path.read_text(encoding="utf-8") != content
    ]
    if dry_run:
        return changed
    originals = {path: path.read_text(encoding="utf-8") for path in changed}
    replaced: list[Path] = []
    try:
        for path in changed:
            temporary = path.with_name(path.name + ".vibetable-version.tmp")
            temporary.write_text(changes[path], encoding="utf-8", newline="")
            os.replace(temporary, path)
            replaced.append(path)
    except OSError:
        for path in reversed(replaced):
            path.write_text(originals[path], encoding="utf-8", newline="")
        raise
    return changed
