"""Collect isolated candidate observations for the existing Go corpus comparator."""

from __future__ import annotations

import argparse
import json
import subprocess
from pathlib import Path
from typing import TypedDict


class ObservationReport(TypedDict):
    corpusVersion: int
    budgets: dict[str, int]
    processLimits: dict[str, int]
    observations: list[dict[str, object]]


def collect(
    manifest: Path, directory: Path, executable: Path, memory_mib: int
) -> ObservationReport:
    if memory_mib <= 0:
        raise ValueError("memory budget must be positive")
    definition = json.loads(manifest.read_text(encoding="utf-8"))
    if definition["corpusVersion"] != 1 or not definition["cases"]:
        raise ValueError("unsupported or empty qualification corpus")
    observations = []
    for case in definition["cases"]:
        filename = case["file"]
        if Path(filename).name != filename or not filename.endswith(".pdf"):
            raise ValueError("invalid qualification file name")
        source = directory / filename
        completed = subprocess.run(
            [str(executable), "--run", str(source.resolve()), "--memory-mib", str(memory_mib)],
            check=True,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=45,
        )
        observation = json.loads(completed.stdout)
        if observation["file"] != filename:
            raise ValueError("worker returned another corpus file")
        observations.append(observation)
    return {
        "corpusVersion": 1,
        "budgets": definition["budgets"],
        "processLimits": {
            "memoryBytes": memory_mib * 1024 * 1024,
            "cpuSeconds": 30,
            "deadlineSeconds": 30,
        },
        "observations": observations,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("directory", type=Path)
    parser.add_argument("executable", type=Path)
    parser.add_argument("--memory-mib", type=int, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report = collect(args.manifest, args.directory, args.executable.resolve(), args.memory_mib)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False) + "\n", encoding="utf-8")
    print(
        f"Collected {len(report['observations'])} observations; corpus comparison is still required."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
