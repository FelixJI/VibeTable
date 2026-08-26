"""Shared metadata for repository-managed Windows toolchain distributions."""

from __future__ import annotations

import shutil
from dataclasses import dataclass
from pathlib import Path

PREFERRED_DOTNET = Path(r"C:\Program Files\dotnet\dotnet.exe")


def _repository_tool_roots(repo_root: Path) -> tuple[Path, ...]:
    roots = [repo_root.resolve()]
    git_marker = roots[0] / ".git"
    if git_marker.is_file():
        try:
            marker = git_marker.read_text(encoding="utf-8").strip()
            if marker.startswith("gitdir:"):
                git_dir = Path(marker.removeprefix("gitdir:").strip())
                if not git_dir.is_absolute():
                    git_dir = roots[0] / git_dir
                common_root = git_dir.resolve().parents[2]
                if common_root not in roots:
                    roots.append(common_root)
        except (OSError, IndexError):
            pass
    return tuple(roots)


def resolve_executable(
    executable: str,
    *,
    path: str | None = None,
    repo_root: Path | None = None,
) -> str | None:
    """Resolve repository commands with the same Windows-specific preferences."""
    if executable.casefold() == "dotnet":
        if repo_root is not None:
            for tool_root in _repository_tool_roots(repo_root):
                bundled = tool_root / ".tools" / "dotnet" / "dotnet.exe"
                if bundled.is_file():
                    return str(bundled)
        if PREFERRED_DOTNET.is_file():
            return str(PREFERRED_DOTNET)
    return shutil.which(executable, path=path)


@dataclass(frozen=True)
class W64DevkitDistribution:
    version: str
    archive_sha256: str
    gcc_version: str

    @property
    def archive_name(self) -> str:
        return f"w64devkit-{self.version}.7z.exe"

    @property
    def url(self) -> str:
        return (
            "https://github.com/skeeto/w64devkit/releases/download/"
            f"v{self.version}/w64devkit-x64-{self.version}.7z.exe"
        )

    def gcc_path(self, repo_root: Path) -> Path:
        return repo_root / ".tools/w64devkit/w64devkit/bin/gcc.exe"


W64DEVKIT_DISTRIBUTION = W64DevkitDistribution(
    version="2.8.0",
    archive_sha256="6252bf34fe2231a55ac7f03d482b36d2c7c58697990551bba508102cfb3f342e",
    gcc_version="16.1.0",
)
