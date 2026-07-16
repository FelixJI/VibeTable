"""架构守护测试：锁定 Directus-first 生产路径的依赖边界。"""

import ast
import re
from pathlib import Path

ROOT = Path(__file__).parent.parent


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


class TestLayerDependencies:
    """现行 Python 分层与工具链约束。"""

    def test_backend_is_ui_and_qt_free(self):
        assert (ROOT / "backend").is_dir(), "backend/ package must exist"
        violations = _scan_imports(
            "backend",
            [r"ui(\.|$)", r"PySide6(\.|$)", r"qasync(\.|$)"],
        )
        assert not violations, "backend 违规依赖 UI/Qt:\n" + "\n".join(violations)

    def test_nvmrc_pins_node_24(self):
        nvmrc = ROOT / ".nvmrc"
        assert nvmrc.is_file(), ".nvmrc must exist"
        assert nvmrc.read_text(encoding="utf-8").strip() == "24.18.0"


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
        composition = ROOT / "desktop" / "src" / "VibeTable.Desktop" / "MainWindow.xaml.cs"
        source = composition.read_text(encoding="utf-8")
        forbidden = (
            "LazySupervisorGateway",
            "JsonRpcTableGateway",
            "WpfDatabasePicker",
            "OpenFileDialog",
        )
        assert not [name for name in forbidden if name in source]
        assert not (
            ROOT / "desktop" / "src" / "VibeTable.Desktop" / "Services" / "JsonRpcTableGateway.cs"
        ).exists()
