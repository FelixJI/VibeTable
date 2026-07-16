#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ -x "$ROOT/.venv/bin/python" ]; then
  PYTHON="$ROOT/.venv/bin/python"
elif [ -x "$ROOT/.venv/Scripts/python.exe" ]; then
  PYTHON="$ROOT/.venv/Scripts/python.exe"
else
  echo "[FAIL] 未找到项目 .venv Python" >&2
  exit 2
fi

cd "$ROOT"
"$PYTHON" qa/run.py --all --ci --no-report
"$PYTHON" qa/next.py --ci
echo "[OK] 全部质量门禁通过"
