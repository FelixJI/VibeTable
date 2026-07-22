using System;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>Maps backend history failures to stable, renderer-safe error codes.</summary>
public static class HistoryErrorMapper
{
    public readonly record struct Failure(string Code, string Message);

    public static Failure Map(Exception exception, string fallbackCode)
    {
        if (exception is OperationCanceledException)
        {
            return new Failure("history_cancelled", "历史操作已取消。");
        }
        if (exception is BackendUnavailableException)
        {
            return new Failure("history_backend_unavailable", "历史服务暂不可用，请稍后重试。");
        }
        if (exception is RpcRemoteException remote)
        {
            return MapBackendCode(ReadBackendCode(remote.ErrorData), fallbackCode);
        }
        return Fallback(fallbackCode);
    }

    public static Failure MapBackendCode(string? code, string fallbackCode)
        => code switch
        {
            "history_not_allowed" =>
                new("history_not_allowed", "当前数据表不支持历史查询。"),
            "history_field_unreadable" =>
                new("history_field_unreadable", "当前字段不可读取，无法查看或恢复其历史。"),
            "archive_not_supported" =>
                new("archive_not_supported", "当前数据表未配置软归档字段。"),
            "restore_not_allowed" =>
                new("restore_not_allowed", "当前数据表不允许恢复历史版本。"),
            "restore_token_unknown" =>
                new("restore_token_unknown", "恢复预览已失效，请重新预览。"),
            "restore_token_expired" =>
                new("restore_token_expired", "恢复预览已过期，请重新预览。"),
            "restore_scope_mismatch" =>
                new("restore_scope_mismatch", "恢复范围与预览不一致，请重新预览。"),
            "restore_conflict" =>
                new("restore_conflict", "数据已发生变化，请刷新并重新预览。"),
            "schema_drift" =>
                new("schema_drift", "数据表结构已发生变化，请刷新后重试。"),
            "restore_no_fields" =>
                new("restore_no_fields", "目标版本没有可恢复的字段。"),
            "target_revision_invalid" =>
                new("target_revision_invalid", "目标历史版本无效或不可访问。"),
            "relation_target_unavailable" =>
                new("relation_target_unavailable", "关联目标不可用，无法安全恢复。"),
            "revision_not_created" =>
                new("revision_not_created", "恢复未生成新的修订记录。"),
            _ => Fallback(fallbackCode),
        };

    private static string? ReadBackendCode(JsonElement? data)
    {
        if (data is null || data.Value.ValueKind != JsonValueKind.Object
            || !data.Value.TryGetProperty("code", out var code)
            || code.ValueKind != JsonValueKind.String)
        {
            return null;
        }
        return code.GetString();
    }

    private static Failure Fallback(string code)
        => code switch
        {
            "HISTORY_QUERY_FAILED" => new("history_query_failed", "历史记录加载失败，请稍后重试。"),
            "HISTORY_PREVIEW_FAILED" => new("history_preview_failed", "恢复预览失败，请稍后重试。"),
            "HISTORY_APPLY_FAILED" => new("history_apply_failed", "历史版本恢复失败，请稍后重试。"),
            _ => new("history_operation_failed", "历史操作失败，请稍后重试。"),
        };
}
