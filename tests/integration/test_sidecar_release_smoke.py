from __future__ import annotations

import json
import os
import queue
import subprocess
import threading
import urllib.request
from pathlib import Path

from scripts.build_next import RepoPaths, build_sidecar_command
from scripts.versioning import collect_release_versions

REPO_ROOT = Path(__file__).resolve().parents[2]


def _readline_with_timeout(stream, seconds: float) -> str:
    lines: queue.Queue[str] = queue.Queue(maxsize=1)
    threading.Thread(
        target=lambda: lines.put(stream.readline()),
        daemon=True,
    ).start()
    return lines.get(timeout=seconds)


def _sidecar_binary(tmp_path: Path) -> Path:
    suffix = ".exe" if os.name == "nt" else ""
    output = tmp_path / f"vibetable-pb{suffix}"
    paths = RepoPaths.default(REPO_ROOT)
    subprocess.run(
        build_sidecar_command(
            paths,
            output=output,
            commit="qa-smoke",
            build_time="2026-07-24T00:00:00Z",
        ),
        cwd=paths.sidecar_source_dir,
        check=True,
    )
    return output


def test_real_release_sidecar_boots_health_checks_and_stops(tmp_path: Path) -> None:
    binary = _sidecar_binary(tmp_path)
    data_dir = tmp_path / "user-data"
    secret = "01" * 32
    environment = os.environ.copy()
    environment.update(
        {
            "VIBETABLE_SIDECAR_SESSION_SECRET": secret,
            "VIBETABLE_SIDECAR_DATA_DIR": str(data_dir),
        }
    )
    process = subprocess.Popen(
        [str(binary)],
        cwd=tmp_path,
        env=environment,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    try:
        assert process.stdout is not None
        ready = json.loads(_readline_with_timeout(process.stdout, 30))
        assert ready["contract"] == "vibetable.sidecar.ready.v1"
        assert ready["event"] == "sidecar.ready"
        address = ready["address"]
        request = urllib.request.Request(
            f"http://{address}/api/vibetable/v1/health",
            headers={"X-VibeTable-Session": secret},
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            health = json.load(response)
        assert response.status == 200
        assert health["status"] == "ok"
        versions = collect_release_versions(REPO_ROOT)
        assert ready["build"]["pocketBaseVersion"] == versions.pocketbase
        assert ready["build"]["celVersion"] == versions.cel
        assert ready["build"]["migrationHash"] == versions.migration_hash

        shutdown = urllib.request.Request(
            f"http://{address}/api/vibetable/v1/shutdown",
            method="POST",
            headers={"X-VibeTable-Session": secret},
        )
        with urllib.request.urlopen(shutdown, timeout=10) as response:
            assert response.status == 202
        assert process.wait(timeout=15) == 0
        assert (data_dir / "data.db").is_file()
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=10)
        if process.stdout is not None:
            process.stdout.close()
        if process.stderr is not None:
            process.stderr.close()
