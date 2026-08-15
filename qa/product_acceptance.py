#!/usr/bin/env python3
"""Run capability-selected real WPF/WebView2 PocketBase product scenarios."""

from __future__ import annotations

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from tests.e2e.product_e2e_runner import main  # noqa: E402, I001


if __name__ == "__main__":
    raise SystemExit(main())
