from __future__ import annotations

import json
from pathlib import Path

from scripts import generate_product_e2e_capability_index as capability_index
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


def test_repository_capability_index_is_current() -> None:
    assert capability_index.check_generated() == []
