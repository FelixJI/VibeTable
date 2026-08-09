"""架构守护测试：锁定 PocketBase-only 生产路径的依赖边界。"""

import ast
import re
from pathlib import Path

ROOT = Path(__file__).parent.parent

_RETIRED_PROVIDER_SCAN_ROOTS = (
    Path("backend"),
    Path("contracts"),
    Path("desktop/src"),
    Path("desktop/web-grid/src"),
    Path("sidecar"),
    Path("scripts"),
    Path(".ci"),
    Path(".github"),
)
_RETIRED_PROVIDER_TEXT_SUFFIXES = {
    ".cs",
    ".go",
    ".html",
    ".js",
    ".json",
    ".mjs",
    ".py",
    ".toml",
    ".ts",
    ".tsx",
    ".vue",
    ".xaml",
    ".xml",
    ".yaml",
    ".yml",
}


def _scan_imports(dir_name: str, forbidden_patterns: list[str]) -> list[str]:
    """扫描目录中的 Python import，返回匹配禁用模块的引用。"""
    violations: list[str] = []
    import_re = re.compile(r"^\s*(?:from\s+(\S+)|import\s+(\S+))")
    for py_file in (ROOT / dir_name).rglob("*.py"):
        if "__pycache__" in py_file.parts:
            continue
        rel = py_file.relative_to(ROOT)
        for lineno, line in enumerate(py_file.read_text(encoding="utf-8").splitlines(), 1):
            match = import_re.match(line)
            if not match:
                continue
            module = match.group(1) or match.group(2)
            for pattern in forbidden_patterns:
                if re.match(pattern, module):
                    violations.append(f"{rel}:{lineno}: {line.strip()}")
    return violations


def _scan_retired_provider_references(root: Path, retired: str) -> list[str]:
    """只扫描受控的生产源码与配置根，避免本地笔记影响架构门禁。"""
    violations: list[str] = []
    for relative_root in _RETIRED_PROVIDER_SCAN_ROOTS:
        scan_root = root / relative_root
        if not scan_root.is_dir():
            continue
        for path in scan_root.rglob("*"):
            if not path.is_file():
                continue
            if retired in path.name.casefold():
                violations.append(path.relative_to(root).as_posix())
                continue
            if path.suffix.casefold() not in _RETIRED_PROVIDER_TEXT_SUFFIXES:
                continue
            try:
                content = path.read_text(encoding="utf-8")
            except (OSError, UnicodeDecodeError):
                continue
            if retired in content.casefold():
                violations.append(path.relative_to(root).as_posix())
    return sorted(set(violations))


class TestLayerDependencies:
    """现行 Python 分层与工具链约束。"""

    def test_backend_is_ui_and_qt_free(self):
        assert (ROOT / "backend").is_dir(), "backend/ package must exist"
        violations = _scan_imports(
            "backend",
            [r"ui(\.|$)", r"PySide6(\.|$)", r"qasync(\.|$)"],
        )
        assert not violations, "backend 违规依赖 UI/Qt:\n" + "\n".join(violations)

    def test_application_does_not_import_outer_layers(self):
        application = ROOT / "backend" / "application"
        forbidden_prefixes = ("backend.adapters", "backend.infrastructure")
        violations: list[str] = []

        for py_file in application.rglob("*.py"):
            tree = ast.parse(py_file.read_text(encoding="utf-8"))
            for node in ast.walk(tree):
                modules: list[str] = []
                if isinstance(node, ast.Import):
                    modules.extend(alias.name for alias in node.names)
                elif isinstance(node, ast.ImportFrom) and node.module is not None:
                    modules.append(node.module)
                for module in modules:
                    if module.startswith(forbidden_prefixes):
                        relative = py_file.relative_to(ROOT).as_posix()
                        violations.append(f"{relative}:{node.lineno}: {module}")

        assert not violations, (
            "application imports outer adapter or infrastructure layers:\n" + "\n".join(violations)
        )

    def test_nvmrc_pins_node_24(self):
        nvmrc = ROOT / ".nvmrc"
        assert nvmrc.is_file(), ".nvmrc must exist"
        assert nvmrc.read_text(encoding="utf-8").strip() == "24.18.0"

    def test_product_renderer_csp_forbids_direct_network_access(self):
        index = ROOT / "desktop" / "web-grid" / "index.html"
        source = index.read_text(encoding="utf-8")
        assert "connect-src 'none'" in source
        assert "connect-src https:" not in source
        assert "frame-src https://*.plugins.vibetable.local" in source
        assert source.count("frame-src ") == 1


class TestFLegacyRemoval:
    """证明生产运行时已移除 Qt、业务 SQLite 与旧更新路径。"""

    _QT_MODULES = ("PySide6", "qasync", "PyQt5", "PyQt6")
    _SQLITE_LEGACY_MODULES = (
        "core.database",
        "backend.application.table_read_service",
        "backend.application.table_mutation_service",
        "backend.application.field_schema_service",
        "backend.application.sql_query_compiler",
    )

    @staticmethod
    def _imported_modules(py_file: Path) -> set[str]:
        tree = ast.parse(py_file.read_text(encoding="utf-8"))
        modules: set[str] = set()
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                for alias in node.names:
                    modules.add(alias.name.split(".")[0])
            elif isinstance(node, ast.ImportFrom) and node.module:
                modules.add(node.module)
                modules.add(node.module.split(".")[0])
        return modules

    def test_backend_has_no_qt_imports(self):
        violations = []
        for py_file in (ROOT / "backend").rglob("*.py"):
            if "__pycache__" in py_file.parts:
                continue
            imported = self._imported_modules(py_file)
            for qt_module in self._QT_MODULES:
                if any(qt_module in module for module in imported):
                    violations.append(f"{py_file.name}: imports {qt_module!r}")
        assert not violations, "backend must be Qt-free:\n" + "\n".join(violations)

    def test_backend_has_no_legacy_sqlite_business_imports(self):
        violations = []
        for py_file in (ROOT / "backend").rglob("*.py"):
            if "__pycache__" in py_file.parts:
                continue
            imported = self._imported_modules(py_file)
            for legacy in self._SQLITE_LEGACY_MODULES:
                if any(legacy in module for module in imported):
                    violations.append(f"{py_file.name}: imports {legacy!r}")
        assert not violations, "backend must not import deleted SQLite services:\n" + "\n".join(
            violations
        )

    def test_removed_runtime_paths_have_no_python_sources(self):
        legacy_paths = (
            ROOT / "ui",
            ROOT / "plugins",
            ROOT / "core",
            ROOT / "shared",
        )
        violations = [
            path.relative_to(ROOT)
            for root in legacy_paths
            if root.exists()
            for path in root.rglob("*.py")
            if "__pycache__" not in path.parts
        ]
        assert not violations, "removed runtime paths contain Python sources: " + ", ".join(
            map(str, violations)
        )

    def test_legacy_main_entrypoint_absent(self):
        assert not (ROOT / "main.py").exists(), "main.py should not exist after F stage"

    def test_legacy_database_and_update_scripts_absent(self):
        legacy_paths = (
            ROOT / "scripts" / "upgrade_database.py",
            ROOT / "scripts" / "deprecated" / "upgrade_database_v2.py",
            ROOT / "scripts" / "updater.py",
        )
        assert not [path for path in legacy_paths if path.exists()]

    def test_desktop_has_no_legacy_sqlite_runtime_path(self):
        composition = ROOT / "desktop" / "src" / "VibeTable.Desktop" / "MainWindow.Product.cs"
        source = composition.read_text(encoding="utf-8")
        forbidden = (
            "LazySupervisorGateway",
            "JsonRpcTableGateway",
            "WpfDatabasePicker",
        )
        assert not [name for name in forbidden if name in source]
        assert not (
            ROOT / "desktop" / "src" / "VibeTable.Desktop" / "Services" / "JsonRpcTableGateway.cs"
        ).exists()

    def test_removed_provider_runtime_paths_are_physically_absent(self):
        retired = "".join(["di", "rectus"])
        removed = (
            ROOT / "backend" / "adapters" / retired,
            ROOT / retired,
            ROOT / "scripts" / f"local_{retired}",
            ROOT / "scripts" / f"migrate_{retired}_files_to_workspace.py",
            ROOT / "backend" / "application" / f"{retired}_service.py",
            ROOT / "backend" / "application" / "flow_binding_manager.py",
            ROOT / "backend" / "application" / "collaboration_service.py",
            ROOT / "backend" / "contracts" / f"{retired}.py",
            ROOT / "backend" / "contracts" / "collaboration.py",
        )
        assert not [path.relative_to(ROOT) for path in removed if path.exists()]

    def test_backend_has_no_removed_provider_imports(self):
        retired = "".join(["di", "rectus"])
        violations = _scan_imports(
            "backend",
            [
                rf"backend\.adapters\.{retired}(\.|$)",
                rf"backend\.contracts\.{retired}(\.|$)",
                r"backend\.contracts\.collaboration(\.|$)",
                rf"backend\.application\.{retired}_service(\.|$)",
                r"backend\.application\.flow_binding_manager(\.|$)",
                r"backend\.application\.collaboration_service(\.|$)",
            ],
        )
        assert not violations, "backend imports removed provider modules:\n" + "\n".join(violations)

    def test_removed_provider_name_is_confined_to_research_archive(self):
        retired = "".join(["di", "rectus"])
        violations = _scan_retired_provider_references(ROOT, retired)
        assert not violations, (
            "removed provider references must not appear in controlled runtime source/config roots:\n"
            + "\n".join(violations)
        )

    def test_retired_provider_scan_ignores_uncontrolled_local_notes(self, tmp_path: Path):
        retired = "".join(["di", "rectus"])
        (tmp_path / "backend").mkdir()
        notes = tmp_path / "local-notes"
        notes.mkdir()
        (notes / "investigation.md").write_text(retired, encoding="utf-8")

        assert _scan_retired_provider_references(tmp_path, retired) == []

    def test_retired_provider_scan_checks_controlled_source_roots(self, tmp_path: Path):
        retired = "".join(["di", "rectus"])
        backend = tmp_path / "backend"
        backend.mkdir()
        (backend / "adapter.py").write_text(retired, encoding="utf-8")

        assert _scan_retired_provider_references(tmp_path, retired) == ["backend/adapter.py"]
