"""Exactly replay the original Python plugin catalog producer from its commit."""

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
from typing import NoReturn

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "aa564213d9526d79a93182cd1db2a4dcbc08f1ef"
ORACLE = ROOT / "contracts/v2/plugin-catalog-python-oracle.json"
CAPTURE = ROOT / "contracts/v2/capture_plugin_catalog_oracle.py"
CASE_COUNT = 11
EVIDENCE_ROOT = ROOT / "build/qa/task375-oracle/producer"


def _fail(message: str) -> NoReturn:
    raise SystemExit(message)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument("--check", action="store_true")
    modes.add_argument("--write", action="store_true")
    parser.parse_args()
    write = "--write" in sys.argv[1:]
    archive_bytes: bytes = subprocess.check_output(
        ["git", "archive", "--format=zip", PRODUCER, "backend"], cwd=ROOT
    )
    EVIDENCE_ROOT.mkdir(parents=True, exist_ok=True)
    run_root = Path(tempfile.mkdtemp(prefix=PRODUCER[:8] + "-", dir=EVIDENCE_ROOT)).resolve()
    if not run_root.is_relative_to((ROOT / "build").resolve()):
        _fail("Producer output escaped the fixed build directory")
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
                _fail("Unexpected producer archive path")
            target = run_root.joinpath(*path.parts)
            if not target.resolve().is_relative_to(run_root):
                _fail("Producer archive escaped its run directory")
            if entry.is_dir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with target.open("xb") as output:
                    output.write(archive.read(entry))
    # -I ignores the caller's PYTHONPATH and cwd. Only the archived producer
    # backend enters the subprocess path; dependencies remain in the same uv venv.
    bootstrap = """
import json, runpy, sys
from pathlib import Path
producer_root = Path(sys.argv[1]).resolve()
sys.path.insert(0, str(producer_root))
import backend
if Path(backend.__file__).resolve() != producer_root / "backend/__init__.py":
    raise SystemExit("Wrong Python producer imported")
capture = runpy.run_path(sys.argv[2], run_name="plugin_catalog_capture")
sys.stdout.write(json.dumps(capture["replay"](), ensure_ascii=True))
"""
    environment = os.environ.copy()
    environment["PYTHONUTF8"] = "1"
    raw: bytes = subprocess.check_output(
        [sys.executable, "-I", "-c", bootstrap, str(run_root), str(CAPTURE)],
        cwd=run_root,
        env=environment,
    )
    (run_root / "captured.json").write_bytes(raw)
    captured: object = json.loads(raw)
    if not isinstance(captured, dict):
        _fail("Replayed producer corpus is not a JSON object")
    producer: object = captured.get("producer")
    cases: object = captured.get("cases")
    if producer != PRODUCER or not isinstance(cases, list) or len(cases) != CASE_COUNT:
        _fail("Replayed producer corpus is structurally invalid")
    if write:
        ORACLE.write_text(
            json.dumps(captured, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        print(f"Wrote {len(cases)} cases from producer {PRODUCER}")
        return
    frozen: object = json.loads(ORACLE.read_text(encoding="utf-8"))
    if captured != frozen:
        _fail("Frozen plugin catalog corpus differs from its original Python producer")
    print(f"Exactly replayed {CASE_COUNT} plugin catalog cases from {PRODUCER}")


if __name__ == "__main__":
    main()
