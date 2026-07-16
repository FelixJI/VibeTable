"""VibeTable 后端根包。

WPF 迁移后的 Python 后端，承载应用服务、对外契约与 RPC 入口。
架构约束：backend 不得依赖 ui / PySide6 / qasync（由架构守护测试锁定）。
"""
