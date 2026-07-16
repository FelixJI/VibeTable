# Directus schema snapshots

`vibetable-empty-postgres.json` 由空白 Directus 项目在执行以下命令后生成：

```powershell
$env:DIRECTUS_URL = 'https://directus.example'
$env:DIRECTUS_ADMIN_TOKEN = '<deployment-only-token>'
python scripts/directus_project.py apply --yes
python scripts/directus_project.py snapshot
```

管理员 token 只允许出现在部署环境变量中，禁止写入 snapshot、manifest、日志或客户端配置。

首次生成的 snapshot 必须使用 Directus CLI 执行 `schema apply --dry-run` 验证；版本或数据库 vendor 不一致时不得使用 `force` 绕过，除非另有审核记录。
