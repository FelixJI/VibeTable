"""Keep the unchanged command/shortcut regressions against their fixed Python producer."""

from __future__ import annotations

import ast
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET
from pathlib import Path

from contracts.v2.generate_content_version_python_oracle import extract_backend

ROOT = Path(__file__).resolve().parents[2]
PRODUCER = "4259359697e812e962b6859c92be60cc0c11bbb3"
BUILD_ROOT = ROOT / "build" / "host-command-oracle"


def test_original_command_service_regressions_run_against_isolated_producer() -> None:
    BUILD_ROOT.mkdir(parents=True, exist_ok=True)
    run = Path(tempfile.mkdtemp(prefix="legacy-service-", dir=BUILD_ROOT))
    producer = run / "producer"
    extract_backend(
        subprocess.check_output(["git", "archive", PRODUCER, "backend"], cwd=ROOT), producer
    )
    legacy = producer / "test_legacy_command_service.py"
    legacy.write_bytes(
        subprocess.check_output(
            [
                "git",
                "show",
                f"{PRODUCER}:tests/backend/application/test_settings_command_service.py",
            ],
            cwd=ROOT,
        )
    )
    source = legacy.read_text(encoding="utf-8")
    names = [
        node.name
        for node in ast.parse(source).body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name.startswith("test_")
    ]
    assert len(names) == 14
    config = producer / "pytest.ini"
    config.write_text("[pytest]\nasyncio_mode=auto\n", encoding="utf-8")
    launcher = (
        "import sys;sys.path.insert(0,sys.argv.pop(1));import pytest;"
        "raise SystemExit(pytest.main(sys.argv[1:]))"
    )
    report = producer / "legacy-results.xml"
    result = subprocess.run(
        [
            sys.executable,
            "-I",
            "-c",
            launcher,
            str(producer),
            *(str(legacy) + "::" + name for name in names),
            "-q",
            f"--junitxml={report}",
            "--noconftest",
            "-c",
            str(config),
            "--no-cov",
        ],
        cwd=producer,
        capture_output=True,
        text=True,
        encoding="utf-8",
        timeout=60,
        check=False,
    )
    (run / "pytest.log").write_text(result.stdout + result.stderr, encoding="utf-8")
    assert result.returncode == 0, result.stdout + result.stderr
    suites = ET.parse(report).getroot().findall("testsuite")
    assert sum(int(suite.attrib["tests"]) for suite in suites) == 14
    for attribute in ("errors", "failures", "skipped"):
        assert sum(int(suite.attrib[attribute]) for suite in suites) == 0
    cases = [case for suite in suites for case in suite.findall("testcase")]
    assert len(cases) == 14
    assert {case.attrib["name"].split("[", 1)[0] for case in cases} == set(names)
