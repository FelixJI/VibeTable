"""Resolve and restore the repository-pinned Windows Node.js toolchain."""

from __future__ import annotations

import hashlib
import json
import shutil
import subprocess
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


NPM_VERSION = "12.2.0"

NODE_DISTRIBUTION = NodeDistribution(
    version="26.10.0",
    archive_sha256="9fef7eca6743a6b910989cd8e78712376b394fcb9b6e1e9c44a0799a287f90c5",
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


def ensure_npm(repo_root: Path) -> Path:
    """Restore the pinned npm locally, using npm's existing integrity checks."""
    node = ensure_node(repo_root)
    prefix = repo_root / ".tools" / "npm"
    package = prefix / "node_modules" / "npm" / "package.json"
    if (
        not package.is_file()
        or json.loads(package.read_text(encoding="utf-8"))["version"] != NPM_VERSION
    ):
        subprocess.run(
            [
                str(node),
                str(node.parent / "node_modules" / "npm" / "bin" / "npm-cli.js"),
                "install",
                "--prefix",
                str(prefix),
                f"npm@{NPM_VERSION}",
                "--no-save",
                "--package-lock=false",
                "--no-audit",
                "--no-fund",
            ],
            check=True,
        )
    if json.loads(package.read_text(encoding="utf-8"))["version"] != NPM_VERSION:
        raise RuntimeError(f"npm restore did not produce version {NPM_VERSION}")
    return prefix / "node_modules" / ".bin"
