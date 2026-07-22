from __future__ import annotations

import sys
from pathlib import Path

from qa import next as next_gate

REPO_ROOT = Path(__file__).resolve().parent.parent


def test_dev_build_runs_after_package_checks_before_stack_tests() -> None:
    assert next_gate.STAGES[:4] == ("version", "package", "dev-build", "python")


def test_dev_build_stage_uses_real_launcher_build_only_mode() -> None:
    command, cwd = next_gate.stage_command("dev-build")

    assert command == [sys.executable, str(REPO_ROOT / "scripts" / "dev.py"), "--build-only"]
    assert cwd == str(REPO_ROOT)
