from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path

from scripts.build_next import RepoPaths, build_sidecar_command
from scripts.release import activate_upgrade, prepare_upgrade

REPO_ROOT = Path(__file__).resolve().parents[2]


def _candidate(tmp_path: Path) -> Path:
    suffix = ".exe" if os.name == "nt" else ""
    built = REPO_ROOT / "build" / "qa" / f"vibetable-pb{suffix}"
    if built.is_file():
        return built
    output = tmp_path / f"candidate-vibetable-pb{suffix}"
    paths = RepoPaths.default(REPO_ROOT)
    subprocess.run(
        build_sidecar_command(
            paths,
            output=output,
            commit="upgrade-smoke",
            build_time="2026-07-24T00:00:00Z",
        ),
        cwd=paths.sidecar_source_dir,
        check=True,
    )
    return output


def test_real_candidate_migrates_copy_before_atomic_activation(tmp_path: Path) -> None:
    candidate = _candidate(tmp_path)
    install = tmp_path / "install"
    data = tmp_path / "pocketbase"
    install.mkdir()
    data.mkdir()
    current = install / candidate.name
    current.write_bytes(b"previous-binary")
    transaction = prepare_upgrade(
        install_dir=install,
        data_dir=data,
        current_binary=current,
    )

    activate_upgrade(
        transaction,
        install_dir=install,
        data_dir=data,
        current_binary=current,
        new_binary=candidate,
    )

    assert current.read_bytes() == candidate.read_bytes()
    assert (data / "data.db").is_file()
    manifest = json.loads(transaction.manifest.read_text(encoding="utf-8"))
    assert manifest["activation"]["status"] == "committed"
    assert not (data / "pb_data").exists()
