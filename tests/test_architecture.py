"""架构守护测试：锁定 PocketBase-only 生产路径的依赖边界。"""

import ast
import os
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
        allowed_roots = (
            ROOT / "docs" / "research",
            ROOT / "docs" / "adr",
        )
        ignored_parts = {
            ".git",
            ".codex-go-cache",
            ".idea",
            ".pytest_cache",
            ".superpowers",
            ".tmp-shot",
            ".tmp",
            ".tools",
            ".venv",
            ".worktrees",
            ".zcode",
            "bin",
            "build",
            "coverage",
            "dist",
            "htmlcov",
            "node_modules",
            "obj",
            "__pycache__",
        }
        text_suffixes = {
            ".cs",
            ".go",
            ".html",
            ".js",
            ".json",
            ".md",
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

        def is_ignored(part: str) -> bool:
            return part in ignored_parts or part.startswith(".e2e-") or part.startswith(".codex-")

        allowed_resolved = tuple(root.resolve() for root in allowed_roots)
        violations: list[str] = []
        for directory, child_dirs, file_names in os.walk(
            ROOT,
            topdown=True,
            onerror=lambda _error: None,
        ):
            directory_path = Path(directory)
            resolved_directory = directory_path.resolve()
            if any(
                resolved_directory == allowed or allowed in resolved_directory.parents
                for allowed in allowed_resolved
            ):
                child_dirs.clear()
                continue
            child_dirs[:] = [child for child in child_dirs if not is_ignored(child)]
            for file_name in file_names:
                if is_ignored(file_name):
                    continue
                path = directory_path / file_name
                if retired in path.name.casefold():
                    violations.append(str(path.relative_to(ROOT)))
                    continue
                if path.suffix.casefold() not in text_suffixes and path.name not in {
                    ".gitignore",
                    ".nvmrc",
                }:
                    continue
                try:
                    content = path.read_text(encoding="utf-8")
                except (OSError, UnicodeDecodeError):
                    continue
                if retired in content.casefold():
                    violations.append(str(path.relative_to(ROOT)))
        assert not violations, (
            "removed provider references must live only in docs/research or docs/adr:\n"
            + "\n".join(sorted(set(violations)))
        )
