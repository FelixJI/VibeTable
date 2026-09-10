"""Check the immutable Python Interface corpus; the capture tool lives in 86639eb5."""

from __future__ import annotations

import argparse
import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "12556e5db81dd49592d69b5af1780007ccd36c37"
FREEZE = "86639eb5af956956e637fc142e62e10160118f35"
ORACLE = "contracts/v2/surface-python-oracle.json"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", required=True)
    parser.parse_args()
    frozen = json.loads(subprocess.check_output(["git", "show", f"{FREEZE}:{ORACLE}"], cwd=ROOT))
    current = json.loads((ROOT / ORACLE).read_text(encoding="utf-8"))
    if current != frozen or current["producer"] != PRODUCER:
        raise SystemExit("The fixed Python Interface corpus was modified.")
    if len(current["cases"]) != 78:
        raise SystemExit("The fixed Interface corpus is incomplete.")
    print("Validated 78 immutable Python Interface cases; no Go-generated replacement.")


if __name__ == "__main__":
    main()
