"""Produce immutable synthetic application packages through the normal packer."""

from __future__ import annotations

import copy
import json
import shutil
import zipfile
from pathlib import Path

from backend.infrastructure.plugin_package import _integrity_content, pack_plugin


def prepare_application_packages(controls: Path) -> None:
    source = Path(__file__).resolve().parents[1] / "fixtures/plugins/application-boundary"
    original = json.loads((source / "manifest.json").read_text(encoding="utf-8"))
    for name, plugin_id, version, extra, incompatible in (
        ("app-a", "com.vibetable.e2e.application-a", "1.0.0", False, False),
        ("app-b", "com.vibetable.e2e.application-b", "1.0.0", False, False),
        ("app-upgrade", "com.vibetable.e2e.application-a", "2.0.0", True, False),
        ("app-incompatible", "com.vibetable.e2e.application-a", "3.0.0", True, True),
    ):
        target = controls / name
        shutil.copytree(source, target)
        manifest = copy.deepcopy(original)
        manifest["pluginId"], manifest["version"] = plugin_id, version
        writer = copy.deepcopy(manifest["actions"][0])
        writer.update(actionId="headless-write", presentation="headless", entry="ui/write.js")
        writer["displayName"] = {"zh-CN": "无界面写任务取消"}
        manifest["actions"].append(writer)
        # Real contributed application entries exercise the small-window rail.
        for index in range(18):
            action = copy.deepcopy(manifest["actions"][0])
            action["actionId"] = f"open-extra-{index}"
            action["displayName"] = {"zh-CN": f"合法应用 {index}"}
            manifest["actions"].append(action)
        if extra:
            manifest["permissions"]["data"][0]["operations"].append("create")
        (target / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        archive_path = controls / (name + ".vtplugin")
        pack_plugin(target, archive_path)
        if incompatible:
            # Negative fixture: keep the existing package integrity producer,
            # while deliberately declaring an unsupported future Host version.
            with zipfile.ZipFile(archive_path) as archive:
                entries = [
                    (info.filename, archive.read(info))
                    for info in archive.infolist()
                    if info.filename not in {"manifest.json", "integrity.json"}
                ]
            manifest["compatibility"]["minHostVersion"] = "99.0.0"
            entries.append(
                (
                    "manifest.json",
                    (json.dumps(manifest, ensure_ascii=False, indent=2) + "\n").encode("utf-8"),
                )
            )
            entries.append(("integrity.json", _integrity_content(entries)))
            with zipfile.ZipFile(archive_path, "w") as archive:
                for member, content in entries:
                    archive.writestr(member, content)
