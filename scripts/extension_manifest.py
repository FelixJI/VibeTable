"""Directus extension manifest loader.

The manifest (``directus/extensions/manifest.json``) is the single source of
truth for which Directus extensions exist in the repository. QA, build and
release iterate the manifest instead of hard-coding a single extension name.

This module is shared by ``scripts/build_next.py``, ``qa/next.py`` and
``scripts/versioning.py`` so all three stacks discover extensions identically.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

#: Default relative path of the manifest inside the repo.
MANIFEST_REL = Path("directus") / "extensions" / "manifest.json"


class ExtensionManifestError(ValueError):
    """Raised when the extension manifest is missing, unreadable or invalid."""


@dataclass(frozen=True)
class ExtensionEntry:
    """One extension declared in ``directus/extensions/manifest.json``."""

    name: str
    type: str
    source: str
    entry: str
    directus_host: str
    capability: str
    stage: str
    description: str


def manifest_path(repo_root: Path) -> Path:
    """Return the absolute path to the extension manifest."""
    return repo_root / MANIFEST_REL


def load_manifest(repo_root: Path) -> dict:
    """Load and return the raw manifest dict.

    Raises :class:`ExtensionManifestError` if the file is missing or is not
    valid JSON.
    """
    path = manifest_path(repo_root)
    if not path.is_file():
        raise ExtensionManifestError(f"extension manifest missing: {path}")
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ExtensionManifestError(f"extension manifest is invalid JSON: {path}: {exc}") from exc
    return data


def list_extensions(repo_root: Path) -> list[ExtensionEntry]:
    """Return the ordered list of declared extensions.

    Raises :class:`ExtensionManifestError` if any entry is missing a required
    field or the ``extensions`` array is empty.
    """
    data = load_manifest(repo_root)
    raw = data.get("extensions")
    if not isinstance(raw, list) or not raw:
        raise ExtensionManifestError(
            "extension manifest must contain a non-empty 'extensions' array"
        )
    entries: list[ExtensionEntry] = []
    for index, item in enumerate(raw):
        if not isinstance(item, dict):
            raise ExtensionManifestError(f"extension #{index} is not an object")
        try:
            entries.append(
                ExtensionEntry(
                    name=item["name"],
                    type=item["type"],
                    source=item["source"],
                    entry=item["entry"],
                    directus_host=item["directusHost"],
                    capability=item["capability"],
                    stage=item["stage"],
                    description=item.get("description", ""),
                )
            )
        except KeyError as exc:
            raise ExtensionManifestError(
                f"extension #{index} ({item.get('name', '?')}) is missing field {exc}"
            ) from exc
    return entries


def extension_names(repo_root: Path) -> list[str]:
    """Return just the ordered extension names."""
    return [entry.name for entry in list_extensions(repo_root)]


def extension_dir(repo_root: Path, name: str) -> Path:
    """Return the source directory for extension ``name``."""
    return repo_root / "directus" / "extensions" / name
