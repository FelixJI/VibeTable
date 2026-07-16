"""
测试配置和pytest fixtures
"""

import sqlite3

import pytest


@pytest.fixture
def test_db_path(tmp_path):
    """创建临时测试数据库路径"""
    db_path = tmp_path / "test.db"
    # 创建测试表
    conn = sqlite3.connect(db_path)
    conn.execute(
        """
        CREATE TABLE test_table (
            id INTEGER PRIMARY KEY,
            name TEXT NOT NULL,
            value INTEGER
        )
    """
    )
    conn.execute(
        """
        INSERT INTO test_table (name, value) VALUES ('test1', 100)
    """
    )
    conn.execute(
        """
        INSERT INTO test_table (name, value) VALUES ('test2', 200)
    """
    )
    conn.commit()
    conn.close()
    return db_path


@pytest.fixture
def sample_data():
    """提供示例数据"""
    return {
        "table1": [
            {"id": 1, "name": "Alice", "value": 100},
            {"id": 2, "name": "Bob", "value": 200},
        ],
        "table2": [
            {"id": 1, "name": "Charlie", "value": 300},
            {"id": 2, "name": "David", "value": 400},
        ],
    }
