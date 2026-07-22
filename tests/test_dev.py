from __future__ import annotations

import os
import sys
from pathlib import Path

from scripts import dev


def test_build_only_builds_host_without_launching(monkeypatch) -> None:
    calls: list[str] = []
    monkeypatch.setattr(dev, "_ensure_host_built", lambda: calls.append("build"))
    monkeypatch.setattr(
        dev,
        "_launch_host",
        lambda *_args, **_kwargs: calls.append("launch"),
    )
    monkeypatch.setattr(sys, "argv", ["dev.py", "--build-only"])

    assert dev.main() == 0
    assert calls == ["build"]


def test_ensure_host_built_runs_asset_checks_and_msbuild(monkeypatch, tmp_path: Path) -> None:
    host = tmp_path / "VibeTable.Desktop.exe"
    host.write_bytes(b"host")
    calls: list[tuple[str, list[str], Path]] = []

    monkeypatch.setattr(dev, "HOST_EXE", host)
    monkeypatch.setattr(dev, "DOTNET", str(tmp_path / "dotnet.exe"))
    Path(dev.DOTNET).write_bytes(b"dotnet")
    monkeypatch.setattr(dev, "_ensure_web_built", lambda: calls.append(("web", [], tmp_path)))
    monkeypatch.setattr(
        dev,
        "_ensure_directus_extensions_built",
        lambda: calls.append(("extensions", [], tmp_path)),
    )
    monkeypatch.setattr(
        dev,
        "_run_build",
        lambda label, command, cwd, **_: calls.append((label, command, cwd)),
    )

    assert dev._ensure_host_built() == host
    assert [call[0] for call in calls[:2]] == ["web", "extensions"]
    assert calls[2][1][:2] == [dev.DOTNET, "build"]


def test_node_project_build_installs_dependencies_only_when_needed(
    monkeypatch, tmp_path: Path
) -> None:
    project = tmp_path / "web"
    project.mkdir()
    (project / "package-lock.json").write_text("{}", encoding="utf-8")
    calls: list[list[str]] = []
    monkeypatch.setattr(
        dev,
        "_run_build",
        lambda _label, command, _cwd, **_: calls.append(command),
    )

    dev._ensure_node_dependencies(project)
    assert calls == [[dev.NPM, "ci"]]

    marker = project / "node_modules" / ".package-lock.json"
    marker.parent.mkdir()
    marker.write_text("{}", encoding="utf-8")
    os.utime(marker, (marker.stat().st_atime, (project / "package-lock.json").stat().st_mtime + 1))
    calls.clear()
    dev._ensure_node_dependencies(project)
    assert calls == []


def test_launch_host_uses_explicit_routing_and_preserves_proxy_settings(
    monkeypatch, tmp_path: Path
) -> None:
    host = tmp_path / "VibeTable.Desktop.exe"
    host.write_bytes(b"host")
    captured: dict[str, object] = {}

    class FakeProcess:
        def __init__(self, argv, **kwargs):
            captured["argv"] = argv
            captured.update(kwargs)

        def poll(self):
            return None

    monkeypatch.setattr(dev, "_ensure_host_built", lambda: host)
    monkeypatch.setattr(dev.subprocess, "Popen", FakeProcess)
    monkeypatch.setenv("VIBETABLE_DIRECTUS_URL", "https://must-not-leak.example")
    monkeypatch.setenv("HTTPS_PROXY", "http://proxy.example:8080")
    dev._PROCS.clear()

    dev._launch_host("https://directus.example/", no_directus_auto=True)

    assert captured["argv"] == [str(host), "--no-directus-auto"]
    env = captured["env"]
    assert isinstance(env, dict)
    assert env["VIBETABLE_DIRECTUS_URL"] == "https://directus.example/"
    assert env["VIBETABLE_PYTHON"] == dev.PYTHON
    assert env["HTTPS_PROXY"] == "http://proxy.example:8080"
