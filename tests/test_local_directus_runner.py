from __future__ import annotations

import os
import sys
from pathlib import Path
from typing import Any

from backend import local_directus_runner
from scripts.local_directus import run

ENV_TEMPLATE = """\
PORT=8055
KEY=__GENERATE__
SECRET=__GENERATE__
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=__GENERATE__
DB_CLIENT=sqlite3
DB_FILENAME=./data/directus.sqlite
"""


def _redirect_env_files(monkeypatch: Any, tmp_path: Path) -> None:
    template = tmp_path / ".env.template"
    template.write_text(ENV_TEMPLATE, encoding="utf-8")
    monkeypatch.setattr(run, "HERE", tmp_path)
    monkeypatch.setattr(run, "ENV_FILE", tmp_path / ".env")
    monkeypatch.setattr(run, "ENV_TEMPLATE", template)


def test_materialize_env_uses_app_bootstrap_credentials(monkeypatch: Any, tmp_path: Path) -> None:
    _redirect_env_files(monkeypatch, tmp_path)
    monkeypatch.setenv("VIBETABLE_DIRECTUS_BOOTSTRAP_EMAIL", "owner@vibetable.app")
    monkeypatch.setenv("VIBETABLE_DIRECTUS_BOOTSTRAP_PASSWORD", "chosen-password")

    values = run.materialize_env()

    assert values["ADMIN_EMAIL"] == "owner@vibetable.app"
    assert values["ADMIN_PASSWORD"] == "chosen-password"
    assert "chosen-password" in (tmp_path / ".env").read_text(encoding="utf-8")
    assert "VIBETABLE_DIRECTUS_BOOTSTRAP_PASSWORD" not in os.environ


def test_scrub_bootstrap_password_removes_plaintext(monkeypatch: Any, tmp_path: Path) -> None:
    _redirect_env_files(monkeypatch, tmp_path)
    values = run.materialize_env()
    secret = values["ADMIN_PASSWORD"]

    run.scrub_bootstrap_password(values)

    text = (tmp_path / ".env").read_text(encoding="utf-8")
    assert secret not in text
    assert "ADMIN_PASSWORD=__WINDOWS_CREDENTIAL_MANAGER__" in text


def test_start_directus_does_not_inherit_bootstrap_password(
    monkeypatch: Any, tmp_path: Path
) -> None:
    _redirect_env_files(monkeypatch, tmp_path)
    (tmp_path / ".env").write_text(
        "PORT=8055\nADMIN_EMAIL=owner@vibetable.app\nADMIN_PASSWORD=plaintext\n",
        encoding="utf-8",
    )
    captured: dict[str, Any] = {}

    def fake_popen(command: list[str], **kwargs: Any) -> object:
        captured["command"] = command
        captured.update(kwargs)
        return object()

    monkeypatch.setattr(run.shutil, "which", lambda _: "npx.cmd")
    monkeypatch.setattr(run.subprocess, "Popen", fake_popen)

    run.start_directus("8055")

    environment = captured["env"]
    assert "ADMIN_EMAIL" not in environment
    assert "ADMIN_PASSWORD" not in environment


def test_packaged_runner_sets_release_root_and_executes_shipped_script(
    monkeypatch: Any, tmp_path: Path
) -> None:
    runtime = tmp_path / "local-directus"
    runtime.mkdir()
    runner = runtime / "run.py"
    runner.write_text("# test runner\n", encoding="utf-8")
    captured: dict[str, Any] = {}
    original_argv = sys.argv

    def fake_run_path(path: str, *, run_name: str) -> None:
        captured["path"] = path
        captured["run_name"] = run_name
        captured["argv"] = list(sys.argv)

    monkeypatch.setattr(local_directus_runner.runpy, "run_path", fake_run_path)

    resources = tmp_path / "release-v1"
    resources.mkdir()
    local_directus_runner.run_local_directus(str(runtime), str(resources))

    assert captured["path"] == str(runner)
    assert captured["run_name"] == "__main__"
    assert captured["argv"] == [str(runner)]
    assert os.environ["VIBETABLE_LOCAL_DIRECTUS_ROOT"] == str(resources)
    assert sys.argv is original_argv
