"""Replay the fixed Python Dashboard producer and compare every frozen sample."""

from __future__ import annotations

import argparse
import io
import json
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path, PurePosixPath

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "a3ca78b9181a529d978f9fba46586fbb924ecada"
ORACLE = ROOT / "contracts/v2/dashboard-python-oracle.json"
CAPTURE = ROOT / "contracts/v2/capture_dashboard_python_oracle.py"
BUILD_ROOT = ROOT / "build/contract-oracles/dashboard-python"


def extract_backend(archive: bytes, destination: Path) -> None:
    """Extract only ordinary backend files into a new isolated producer directory."""
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as source:
        members = source.getmembers()
        if not members:
            raise ValueError("The fixed Python producer archive is empty")
        seen: set[str] = set()
        for member in members:
            path = PurePosixPath(member.name)
            if (
                not member.name
                or "\\" in member.name
                or path.is_absolute()
                or ".." in path.parts
                or ":" in member.name
                or not path.parts
                or path.parts[0] != "backend"
                or not (member.isdir() or member.isfile())
                or member.name in seen
            ):
                raise ValueError(f"Unsafe Python producer archive member: {member.name}")
            seen.add(member.name)
        destination.mkdir()
        for member in members:
            target = destination.joinpath(*PurePosixPath(member.name).parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            stream = source.extractfile(member)
            if stream is None:
                raise ValueError(f"Missing Python producer bytes: {member.name}")
            with stream, target.open("xb") as output:
                output.write(stream.read())


def replay_producer() -> dict[str, object]:
    """Use the reachable historical backend, never the current Go/Python owner."""
    # The fixed build root contains per-run evidence; no existing tree is removed or overlaid.
    if not BUILD_ROOT.resolve().is_relative_to((ROOT / "build").resolve()) or not (
        ROOT / "build"
    ).resolve().is_relative_to(ROOT.resolve()):
        raise ValueError("Dashboard oracle build directory escapes the worktree")
    BUILD_ROOT.mkdir(parents=True, exist_ok=True)
    run_root = Path(tempfile.mkdtemp(prefix="replay-", dir=BUILD_ROOT))
    archive = subprocess.check_output(["git", "archive", PRODUCER, "backend"], cwd=ROOT)
    producer_root = run_root / "producer"
    extract_backend(archive, producer_root)
    result_path = run_root / "captured.json"
    # -I ignores inherited PYTHONPATH and user site; only this archived backend is admitted.
    launcher = (
        "import runpy,sys; sys.path.insert(0,sys.argv.pop(1)); "
        "runpy.run_path(sys.argv.pop(1),run_name='__main__')"
    )
    completed = subprocess.run(
        [
            sys.executable,
            "-I",
            "-c",
            launcher,
            str(producer_root),
            str(CAPTURE),
            "--producer-root",
            str(producer_root),
            "--output",
            str(result_path),
        ],
        cwd=producer_root,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
        timeout=60,
    )
    (run_root / "stdout.log").write_text(completed.stdout, encoding="utf-8")
    (run_root / "stderr.log").write_text(completed.stderr, encoding="utf-8")
    completed.check_returncode()
    captured = json.loads(result_path.read_text(encoding="utf-8"))
    if not isinstance(captured, dict) or captured.get("producer") != PRODUCER:
        raise ValueError("Capture did not identify the fixed Python producer")
    return captured


def verify_capture(captured: dict[str, object], oracle: Path = ORACLE) -> None:
    current = json.loads(oracle.read_text(encoding="utf-8"))
    if json.dumps(current, sort_keys=True, ensure_ascii=False) != json.dumps(
        captured, sort_keys=True, ensure_ascii=False
    ):
        raise ValueError("Dashboard oracle differs from the fixed Python producer replay")
    if current.get("producer") != PRODUCER or not current.get("cases"):
        raise ValueError("The fixed Python Dashboard corpus is incomplete")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()
    captured = replay_producer()
    if args.write:
        ORACLE.write_text(
            json.dumps(captured, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
    verify_capture(captured)
    print("Validated Python Dashboard samples by exact historical producer replay.")


if __name__ == "__main__":
    main()
