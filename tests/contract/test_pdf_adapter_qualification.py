"""Exercise the qualification runner through its real isolated process boundary."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]
ARTIFACTS = ROOT / "build" / "qa" / "pdf-adapter" / "artifacts"


@pytest.fixture(scope="module")
def qualification_executable() -> Path:
    subprocess.run(
        [
            "dotnet",
            "build",
            str(ROOT / "qa/pdf-adapter/PdfAdapterQualification.csproj"),
            "--configuration",
            "Release",
            "--artifacts-path",
            str(ARTIFACTS),
            "-p:RestoreLockedMode=true",
        ],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
        timeout=180,
    )
    return ARTIFACTS / "bin/PdfAdapterQualification/release/PdfAdapterQualification.exe"


@pytest.mark.integration
def test_pdf_worker_process_limits_and_cleanup(qualification_executable: Path) -> None:
    completed = subprocess.run(
        [str(qualification_executable), "--check-process-boundary"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
        timeout=90,
    )
    report = json.loads(completed.stdout)
    assert report["passed"] is True
    assert len(report["checks"]) == 12
    assert all(check["passed"] for check in report["checks"])


@pytest.fixture(scope="module")
def pdf_corpus() -> Path:
    subprocess.run(
        [
            "uv",
            "run",
            "--frozen",
            "--no-sync",
            "python",
            "tests/contract/generate_pdf_qualification_corpus.py",
        ],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
        timeout=60,
    )
    return ROOT / "build/qa/pdf-qualification/v1"


@pytest.mark.integration
@pytest.mark.parametrize("valid", [True, False])
def test_output_limit_still_validates_later_pages(
    qualification_executable: Path, pdf_corpus: Path, valid: bool
) -> None:
    filename = f"output-limit-later-{'valid' if valid else 'invalid'}-page.pdf"
    completed = subprocess.run(
        [
            str(qualification_executable),
            "--run",
            str(pdf_corpus / filename),
            "--memory-mib",
            "1024",
        ],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
        timeout=45,
    )
    observation = json.loads(completed.stdout)
    assert observation["allProcessesExited"] is True
    assert observation["workerReason"] == "Succeeded"
    result = observation["result"]
    assert result["status"] == ("truncated" if valid else "failed")
    assert result["errorCode"] == ("extract.text_limit" if valid else "extract.pdf_stream_invalid")
    assert result["text"] == ("B" * 2_000_000 if valid else "")
