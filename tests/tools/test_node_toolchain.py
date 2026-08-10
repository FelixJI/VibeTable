from __future__ import annotations

import hashlib
import io
import zipfile
from pathlib import Path

import pytest

from scripts import node_toolchain

ROOT = Path(__file__).resolve().parents[2]


def _archive(version: str, content: bytes = b"locked node") -> bytes:
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w") as archive:
        archive.writestr(f"node-v{version}-win-x64/node.exe", content)
        archive.writestr(f"node-v{version}-win-x64/LICENSE", "Node.js license")
    return output.getvalue()


def _distribution(version: str, archive: bytes) -> node_toolchain.NodeDistribution:
    return node_toolchain.NodeDistribution(
        version=version,
        archive_sha256=hashlib.sha256(archive).hexdigest(),
    )


def test_ensure_node_installs_the_verified_locked_windows_runtime(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    version = "24.99.1"
    archive = _archive(version)
    distribution = _distribution(version, archive)
    (tmp_path / ".node-version").write_text(version + "\n", encoding="utf-8")
    monkeypatch.setattr(
        node_toolchain.urllib.request,
        "urlopen",
        lambda _url, timeout: io.BytesIO(archive),
    )

    executable = node_toolchain.ensure_node(tmp_path, distribution=distribution)

    assert executable == (tmp_path / ".tools" / "node" / f"node-v{version}-win-x64" / "node.exe")
    assert executable.read_bytes() == b"locked node"
    assert (tmp_path / "build" / "tooling" / f"node-v{version}-win-x64.zip").read_bytes() == archive


def test_ensure_node_rejects_an_untrusted_archive(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    version = "24.99.2"
    archive = _archive(version)
    distribution = node_toolchain.NodeDistribution(
        version=version,
        archive_sha256="0" * 64,
    )
    (tmp_path / ".node-version").write_text(version + "\n", encoding="utf-8")
    monkeypatch.setattr(
        node_toolchain.urllib.request,
        "urlopen",
        lambda _url, timeout: io.BytesIO(archive),
    )

    with pytest.raises(RuntimeError, match="checksum mismatch"):
        node_toolchain.ensure_node(tmp_path, distribution=distribution)

    assert not (tmp_path / ".tools" / "node" / f"node-v{version}-win-x64" / "node.exe").exists()


def test_ensure_node_does_not_publish_a_partial_extraction(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    version = "24.99.4"
    archive = _archive(version)
    distribution = _distribution(version, archive)
    (tmp_path / ".node-version").write_text(version + "\n", encoding="utf-8")
    cached = tmp_path / "build" / "tooling" / distribution.archive_name
    cached.parent.mkdir(parents=True)
    cached.write_bytes(archive)
    original_extractall = zipfile.ZipFile.extractall

    def interrupt_after_partial_write(
        bundle: zipfile.ZipFile,
        destination: Path,
    ) -> None:
        original_extractall(bundle, destination, members=[bundle.infolist()[0]])
        raise OSError("simulated interrupted extraction")

    monkeypatch.setattr(zipfile.ZipFile, "extractall", interrupt_after_partial_write)

    with pytest.raises(OSError, match="interrupted extraction"):
        node_toolchain.ensure_node(tmp_path, distribution=distribution)

    executable = tmp_path / ".tools" / "node" / f"node-v{version}-win-x64" / "node.exe"
    assert not executable.exists()
    assert not list((tmp_path / ".tools" / "node").glob("node-install-*"))


def test_resolve_node_prefers_the_locked_toolchain_then_the_system_install(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    version = "24.99.3"
    distribution = node_toolchain.NodeDistribution(
        version=version,
        archive_sha256="1" * 64,
    )
    pinned = tmp_path / ".tools" / "node" / f"node-v{version}-win-x64" / "node.exe"
    pinned.parent.mkdir(parents=True)
    pinned.write_bytes(b"pinned")
    monkeypatch.setattr(node_toolchain.shutil, "which", lambda _name: "C:/system/node.exe")

    assert node_toolchain.resolve_node(tmp_path, distribution=distribution) == str(pinned)

    pinned.unlink()
    assert node_toolchain.resolve_node(tmp_path, distribution=distribution) == "C:/system/node.exe"


def test_repository_uses_the_restorable_toolchain_instead_of_committed_node() -> None:
    assert not (ROOT / "runtime" / "node").exists()
    plugin_cli = (ROOT / "scripts" / "vibetable_plugin.py").read_text(encoding="utf-8")
    assert 'runtime" / "node' not in plugin_cli
    assert "resolve_node(REPO_ROOT)" in plugin_cli
