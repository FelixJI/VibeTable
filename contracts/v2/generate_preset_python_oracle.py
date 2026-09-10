"""Exactly replay the original Python Preset producer without retired branch objects."""

from __future__ import annotations

import argparse
import io
import json
import os
import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path, PurePosixPath

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "146a9c2cac5998ee013daebc78eedff0bd4a7ca5"
ORACLE = ROOT / "contracts/v2/preset-python-oracle.json"
CAPTURE = ROOT / "contracts/v2/preset_python_capture.py"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", required=True)
    parser.parse_args()
    archive_bytes = subprocess.check_output(
        ["git", "archive", "--format=zip", PRODUCER, "backend"], cwd=ROOT
    )
    evidence_root = ROOT / "build/preset-python-producer"
    evidence_root.mkdir(parents=True, exist_ok=True)
    run_root = Path(tempfile.mkdtemp(prefix=PRODUCER[:8] + "-", dir=evidence_root)).resolve()
    if not run_root.is_relative_to((ROOT / "build").resolve()):
        raise SystemExit("Producer output escaped the fixed build directory")
    with zipfile.ZipFile(io.BytesIO(archive_bytes)) as archive:
        for entry in archive.infolist():
            path = PurePosixPath(entry.filename)
            if (
                path.is_absolute()
                or not path.parts
                or path.parts[0] != "backend"
                or ".." in path.parts
                or "\\" in entry.filename
                or (entry.external_attr >> 16) & 0o170000 == 0o120000
            ):
                raise SystemExit("Unexpected producer archive path")
            target = run_root.joinpath(*path.parts)
            if not target.resolve().is_relative_to(run_root):
                raise SystemExit("Producer archive escaped its run directory")
            if entry.is_dir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with target.open("xb") as output:
                    output.write(archive.read(entry))
    # -I ignores the caller's PYTHONPATH and cwd. Only the archived producer
    # backend enters the subprocess path; dependencies remain in the same uv venv.
    bootstrap = """
import asyncio, json, runpy, sys
from pathlib import Path
producer_root = Path(sys.argv[1]).resolve()
sys.path.insert(0, str(producer_root))
import backend
if Path(backend.__file__).resolve() != producer_root / "backend/__init__.py":
    raise SystemExit("Wrong Python producer imported")
capture = runpy.run_path(sys.argv[2], run_name="preset_original_capture")
sys.stdout.write(json.dumps(asyncio.run(capture["replay"]()), ensure_ascii=True))
"""
    environment = os.environ.copy()
    environment["PYTHONUTF8"] = "1"
    raw = subprocess.check_output(
        [sys.executable, "-I", "-c", bootstrap, str(run_root), str(CAPTURE)],
        cwd=run_root,
        env=environment,
    )
    (run_root / "captured.json").write_bytes(raw)
    captured = json.loads(raw)
    frozen = json.loads(ORACLE.read_text(encoding="utf-8"))
    if captured != frozen or captured["producer"] != PRODUCER or len(captured["cases"]) != 53:
        raise SystemExit("Frozen Preset corpus differs from its original Python producer")
    print("Exactly replayed 53 original Python Preset cases from " + PRODUCER)


if __name__ == "__main__":
    main()
