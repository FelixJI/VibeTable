"""Resolve and restore the repository-pinned Windows Node.js toolchain."""

from __future__ import annotations

import hashlib
import shutil
import tempfile
import urllib.request
import zipfile
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class NodeDistribution:
    version: str
    archive_sha256: str

    @property
    def archive_name(self) -> str:
        return f"node-v{self.version}-win-x64.zip"

    @property
    def directory_name(self) -> str:
        return f"node-v{self.version}-win-x64"

    @property
    def url(self) -> str:
        return f"https://nodejs.org/dist/v{self.version}/{self.archive_name}"


NODE_DISTRIBUTION = NodeDistribution(
    version="24.19.0",
    archive_sha256="57f71ab3652e797d84acddc79c81cc9ff1c6ddb2a1974cdb83f00fee9bff4c73",
)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def _node_path(repo_root: Path, distribution: NodeDistribution) -> Path:
    return repo_root / ".tools" / "node" / distribution.directory_name / "node.exe"


def resolve_node(
    repo_root: Path,
    *,
    distribution: NodeDistribution = NODE_DISTRIBUTION,
) -> str | None:
    """Prefer the repository-pinned toolchain, then an explicit system install."""
    pinned = _node_path(repo_root, distribution)
    if pinned.is_file():
        return str(pinned)
    return shutil.which("node")


def ensure_node(
    repo_root: Path,
    *,
    distribution: NodeDistribution = NODE_DISTRIBUTION,
) -> Path:
    """Restore the trusted Node distribution into the declared `.tools/node` directory."""
    locked_version = (repo_root / ".node-version").read_text(encoding="utf-8").strip()
    if locked_version != distribution.version:
        raise RuntimeError(
            ".node-version does not match the trusted Node distribution: "
            f"{locked_version!r} != {distribution.version!r}"
        )

    executable = _node_path(repo_root, distribution)
    if executable.is_file():
        return executable

    archive = repo_root / "build" / "tooling" / distribution.archive_name
    archive.parent.mkdir(parents=True, exist_ok=True)
    if not archive.is_file() or _sha256(archive) != distribution.archive_sha256:
        if archive.exists():
            archive.unlink()
        with (
            urllib.request.urlopen(distribution.url, timeout=120) as response,
            archive.open("wb") as output,
        ):
            shutil.copyfileobj(response, output)

    actual = _sha256(archive)
    if actual != distribution.archive_sha256:
        raise RuntimeError(f"Node.js archive checksum mismatch: {actual}")

    tools_root = (repo_root / ".tools" / "node").resolve()
    tools_root.mkdir(parents=True, exist_ok=True)
    # Extract beside the final directory and publish only after the complete
    # archive has been written. An interrupted bootstrap must never leave a
    # half-written node.exe that a later resolver mistakes for a valid install.
    with tempfile.TemporaryDirectory(prefix="node-install-", dir=tools_root) as temporary:
        staging_root = Path(temporary).resolve()
        with zipfile.ZipFile(archive) as bundle:
            for member in bundle.infolist():
                target = (staging_root / member.filename).resolve()
                if not target.is_relative_to(staging_root):
                    raise RuntimeError(
                        f"Node.js archive contains an unsafe path: {member.filename}"
                    )
            bundle.extractall(staging_root)
        staged_distribution = staging_root / distribution.directory_name
        staged_executable = staged_distribution / "node.exe"
        if not staged_executable.is_file():
            raise RuntimeError("Node.js archive did not produce node.exe")
        final_distribution = executable.parent
        if final_distribution.exists():
            shutil.rmtree(final_distribution)
        staged_distribution.replace(final_distribution)
    if not executable.is_file():
        raise RuntimeError("Node.js archive did not produce node.exe")
    return executable
