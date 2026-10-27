"""Synthetic output package preparation and independent XLSX reader evidence."""

from __future__ import annotations

import json
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path

from tests.e2e.plugin_capability_packages import ROOT, _pack_example


def prepare_output_package(controls: Path) -> None:
    _pack_example(ROOT / "examples/plugins/output-conformance", controls, "output-conformance")


def verify_output_files(controls: Path) -> dict[str, object]:
    """Read real saved XLSX bytes, checking ZIP integrity and spreadsheet content."""
    ns = {"s": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}
    sizes: dict[str, int] = {}
    for name, version, count in (
        ("small.xlsx", "template-v1", 2),
        ("large.xlsx", "template-v1", 12000),
        ("reprint.xlsx", "template-v1", 12000),
        ("new-template.xlsx", "template-v2", 2),
    ):
        target = controls / name
        sizes[name] = target.stat().st_size
        with zipfile.ZipFile(target) as archive:
            if archive.testzip() is not None:
                raise ValueError(f"saved XLSX contains a corrupt ZIP member: {name}")
            worksheet = ET.fromstring(archive.read("xl/worksheets/sheet1.xml"))
            values = worksheet.findall(".//s:t", ns)
            if len(values) != count or any(
                item.text != f"SYNTHETIC-{version}-{i}-NO-BUSINESS-POST"
                for i, item in enumerate(values)
            ):
                raise ValueError(f"saved XLSX content/version differs: {name}")
    if sizes["small.xlsx"] >= 1 << 20 or not (1 << 20) < sizes["large.xlsx"] <= 32 << 20:
        raise ValueError("ordinary/large output fixture does not exercise the declared budgets")
    if (controls / "large.xlsx").read_bytes() != (controls / "reprint.xlsx").read_bytes():
        raise ValueError("reprinting an issued artifact regenerated or changed its bytes")
    proof = json.loads((controls / "output-proof.json").read_text(encoding="utf-8"))
    if (
        proof.get("businessUnchanged") is not True
        or proof.get("oversizeCode") != "plugin_file_limit"
    ):
        raise ValueError("output scene lacks business-isolation or oversize-rejection evidence")
    return {"xlsxReopened": True, "originalReprintBytes": True, "sizes": sizes, "proof": proof}
