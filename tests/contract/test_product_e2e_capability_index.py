from __future__ import annotations

import json
from pathlib import Path

import pytest

from scripts import generate_product_e2e_capability_index as capability_index
from tests.e2e.product_e2e_runner import write_aggregate
from tests.e2e.product_scenario_manifest import Scenario


def _write_manifest(path: Path, scenarios: list[Scenario]) -> None:
    path.write_text(
        json.dumps(
            [
                {
                    "id": scenario.id,
                    "title": scenario.title,
                    "requirement": scenario.requirement,
                    "capabilities": list(scenario.capabilities),
                }
                for scenario in scenarios
            ],
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
        newline="",
    )


def _evidence_scenarios() -> list[Scenario]:
    return [
        Scenario(
            id="01-first",
            title="第一场景",
            requirement="验证当前证据。",
            capabilities=("example.read",),
        ),
        Scenario(
            id="02-second",
            title="第二场景",
            requirement="验证场景引用。",
            capabilities=("example.write",),
        ),
    ]


def _evidence_documents() -> dict[Path, str]:
    canonical_link = "../e2e-performance.md#当前产品-e2e-证据"
    return {
        Path("docs/e2e-performance.md"): """# 产品 E2E 性能基线

## 当前产品 E2E 证据

- source SHA：`GitHub/main@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`
- GitHub run：https://github.com/FelixJI/VibeTable/actions/runs/123456789
- 报告契约：`contractVersion=2.0`
- 结果：2/2 passed、0 failed、0 skipped。
- 场景：`01-first`、`02-second`。
""",
        Path("docs/quality/capability-matrix.md"): (
            "# 能力闭环矩阵\n\n"
            f"> 运行结论见[当前产品 E2E 证据]({canonical_link})。\n\n"
            "`01-first` 已闭环。\n"
        ),
        Path("docs/quality/stabilization-ledger.md"): (
            "# 稳定化台账\n\n"
            f"> 当前基线见[当前产品 E2E 证据]({canonical_link})。\n\n"
            "`02-second` 已通过。\n"
        ),
    }


def test_evidence_document_contract_uses_manifest_and_report_version() -> None:
    assert (
        capability_index.check_product_e2e_evidence_documents(
            _evidence_documents(),
            _evidence_scenarios(),
            report_contract_version="2.0",
        )
        == []
    )


def test_evidence_document_contract_rejects_stale_local_or_unknown_evidence() -> None:
    documents = _evidence_documents()
    documents[Path("docs/e2e-performance.md")] = documents[Path("docs/e2e-performance.md")].replace(
        "2/2 passed", "12/12 passed"
    )
    documents[Path("docs/quality/capability-matrix.md")] += (
        "`build\\q\\old\\report.json` 与 `19-removed` 曾通过；2/2 passed。\n"
    )
    documents[Path("docs/quality/stabilization-ledger.md")] += (
        "历史实施基线 `GitHub/main@bd06158e`。\n"
    )

    errors = capability_index.check_product_e2e_evidence_documents(
        documents,
        _evidence_scenarios(),
        report_contract_version="2.0",
    )

    assert any("2/2 passed" in error for error in errors)
    assert any("temporary product E2E report path" in error for error in errors)
    assert any("unknown product E2E scenario: 19-removed" in error for error in errors)
    assert any("duplicate product E2E run metadata" in error for error in errors)


def test_evidence_document_contract_requires_metadata_in_current_section() -> None:
    documents = _evidence_documents()
    canonical_path = Path("docs/e2e-performance.md")
    documents[canonical_path] = documents[canonical_path].replace(
        "## 当前产品 E2E 证据\n\n",
        "- source SHA：`GitHub/main@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`\n"
        "- GitHub run：https://github.com/FelixJI/VibeTable/actions/runs/123456789\n"
        "- 报告契约：`contractVersion=2.0`\n"
        "- 结果：2/2 passed、0 failed、0 skipped。\n\n"
        "## 当前产品 E2E 证据\n\n",
    )
    current_section = documents[canonical_path].split("## 当前产品 E2E 证据", 1)[1]
    documents[canonical_path] = documents[canonical_path].replace(current_section, "\n")

    errors = capability_index.check_product_e2e_evidence_documents(
        documents,
        _evidence_scenarios(),
        report_contract_version="2.0",
    )

    assert any("40-character GitHub/main source SHA" in error for error in errors)
    assert any("GitHub Actions run URL" in error for error in errors)
    assert any("contractVersion=2.0" in error for error in errors)
    assert any("2/2 passed" in error for error in errors)


def test_evidence_document_contract_rejects_metadata_outside_current_section() -> None:
    documents = _evidence_documents()
    canonical_path = Path("docs/e2e-performance.md")
    documents[canonical_path] += """

## 历史记录

- source SHA：`GitHub/main@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb`
- GitHub run：https://github.com/FelixJI/VibeTable/actions/runs/987654321
- 报告契约：`contractVersion=2.0`
"""

    errors = capability_index.check_product_e2e_evidence_documents(
        documents,
        _evidence_scenarios(),
        report_contract_version="2.0",
    )

    assert any("duplicate product E2E run metadata" in error for error in errors)


@pytest.mark.parametrize(
    ("old", "new", "expected_error"),
    [
        (
            "contractVersion=2.0",
            "contractVersion=2.0.1",
            "contractVersion=2.0",
        ),
        (
            "actions/runs/123456789",
            "actions/runs/123456789abc",
            "GitHub Actions run URL",
        ),
        (
            "## 当前产品 E2E 证据\n",
            "## 当前产品 E2E 证据\n\n- 历史 source SHA：`GitHub/main@bd06158e`\n",
            "40-character GitHub/main source SHA",
        ),
    ],
)
def test_evidence_document_contract_requires_exact_unique_metadata_tokens(
    old: str,
    new: str,
    expected_error: str,
) -> None:
    documents = _evidence_documents()
    canonical_path = Path("docs/e2e-performance.md")
    documents[canonical_path] = documents[canonical_path].replace(old, new)

    errors = capability_index.check_product_e2e_evidence_documents(
        documents,
        _evidence_scenarios(),
        report_contract_version="2.0",
    )

    assert any(expected_error in error for error in errors)


def test_evidence_document_contract_requires_exact_scenario_and_zero_result() -> None:
    documents = _evidence_documents()
    canonical_path = Path("docs/e2e-performance.md")
    documents[canonical_path] = (
        documents[canonical_path]
        .replace("0 failed", "1 failed")
        .replace("`02-second`", "`01-first`")
    )

    errors = capability_index.check_product_e2e_evidence_documents(
        documents,
        _evidence_scenarios(),
        report_contract_version="2.0",
    )

    assert any("2/2 passed, 0 failed, 0 skipped" in error for error in errors)
    assert any("must list every manifest scenario exactly once" in error for error in errors)


def test_evidence_document_contract_detects_manifest_growth() -> None:
    scenarios = _evidence_scenarios()
    scenarios.append(
        Scenario(
            id="03-added",
            title="新增场景",
            requirement="manifest 增长必须使旧结果失败。",
            capabilities=("example.added",),
        )
    )

    errors = capability_index.check_product_e2e_evidence_documents(
        _evidence_documents(),
        scenarios,
        report_contract_version="2.0",
    )

    assert any("3/3 passed" in error for error in errors)


def test_report_contract_version_matches_document_checker(tmp_path: Path) -> None:
    report = write_aggregate(
        tmp_path / "product-e2e-report.json",
        audit={"passed": True},
        results=[],
    )

    assert report["contractVersion"] == capability_index.PRODUCT_E2E_REPORT_CONTRACT_VERSION


def test_render_index_derives_counts_and_stable_bidirectional_order() -> None:
    scenarios = [
        Scenario(
            id="02-second",
            title="第二场景",
            requirement="保留 manifest 顺序。",
            capabilities=("zeta.read", "alpha.write"),
        ),
        Scenario(
            id="01-first",
            title="第一场景",
            requirement="能力索引按名称排序。",
            capabilities=("alpha.write", "release.smoke"),
        ),
    ]

    rendered = capability_index.render_index(scenarios)

    assert "- 场景：2" in rendered
    assert "- 唯一能力：3" in rendered
    assert "- 场景—能力关联：4" in rendered
    assert "- `release.smoke` 场景：1" in rendered
    assert rendered.index("| `alpha.write` |") < rendered.index("| `release.smoke` |")
    assert rendered.index("| `release.smoke` |") < rendered.index("| `zeta.read` |")
    assert rendered.index("| <code>02-second</code> | 第二场景 |") < rendered.index(
        "| <code>01-first</code> | 第一场景 |"
    )
    assert "<code>02-second</code>（第二场景）、<code>01-first</code>（第一场景）" in rendered


def test_render_index_escapes_loader_valid_markdown_text() -> None:
    rendered = capability_index.render_index(
        [
            Scenario(
                id="id|`tick`\nnext",
                title="标题|`值`<em>&",
                requirement="第一行\r\n第二行\r路径\\文件<tag>&entity",
                capabilities=("example.read",),
            )
        ]
    )

    assert "<code>id&#124;`tick`<br>next</code>（标题\\|\\`值\\`&lt;em&gt;&amp;）" in rendered
    assert (
        "| <code>id&#124;`tick`<br>next</code> | 标题\\|\\`值\\`&lt;em&gt;&amp; | "
        "第一行<br>第二行<br>路径\\\\文件&lt;tag&gt;&amp;entity | `example.read` |"
    ) in rendered


def test_check_generated_detects_manual_and_manifest_drift(tmp_path: Path) -> None:
    manifest_path = tmp_path / "scenarios.json"
    output_path = tmp_path / "capability-index.md"
    scenarios = [
        Scenario(
            id="01-example",
            title="示例",
            requirement="生成稳定索引。",
            capabilities=("example.read",),
        )
    ]
    _write_manifest(manifest_path, scenarios)

    assert capability_index.check_generated(manifest_path, output_path) == [
        "missing generated product E2E capability index: capability-index.md"
    ]
    assert not output_path.exists()
    assert capability_index.write_index(manifest_path, output_path) == [output_path]
    assert capability_index.write_index(manifest_path, output_path) == []
    assert capability_index.check_generated(manifest_path, output_path) == []

    output_path.write_bytes(output_path.read_bytes().replace(b"\n", b"\r\n"))
    assert capability_index.check_generated(manifest_path, output_path) == [
        "stale generated product E2E capability index: capability-index.md"
    ]
    assert capability_index.write_index(manifest_path, output_path) == [output_path]

    output_path.write_text("manual edit\n", encoding="utf-8")
    assert capability_index.check_generated(manifest_path, output_path) == [
        "stale generated product E2E capability index: capability-index.md"
    ]

    capability_index.write_index(manifest_path, output_path)
    scenarios.append(
        Scenario(
            id="02-added",
            title="新增",
            requirement="manifest 变化必须重新生成。",
            capabilities=("example.write",),
        )
    )
    _write_manifest(manifest_path, scenarios)
    assert capability_index.check_generated(manifest_path, output_path) == [
        "stale generated product E2E capability index: capability-index.md"
    ]


def test_main_check_includes_product_e2e_evidence_contract(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setattr(capability_index, "check_generated", lambda: [])
    monkeypatch.setattr(
        capability_index,
        "check_repository_product_e2e_evidence",
        lambda: ["stale product E2E evidence"],
    )

    assert capability_index.main(["--check"]) == 1
    assert "stale product E2E evidence" in capsys.readouterr().err


def test_repository_capability_index_and_evidence_are_current() -> None:
    assert capability_index.check_generated() == []
    assert capability_index.check_repository_product_e2e_evidence() == []
