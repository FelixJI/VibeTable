using System;
using System.Collections.Generic;
using System.Text.Json;
using VibeTable.Infrastructure.Rpc;

namespace VibeTable.Desktop.Services;

/// <summary>Maps dashboard failures to stable, renderer-safe errors.</summary>
public static class DashboardErrorMapper
{
    public readonly record struct Failure(string Code, string Message);

    private static readonly IReadOnlyDictionary<string, string> SafeMessages =
        new Dictionary<string, string>(StringComparer.Ordinal)
        {
            ["dashboard_collection_disabled"] = "当前数据表不可用于仪表盘。",
            ["dashboard_collection_mismatch"] = "仪表盘查询的数据表无效。",
            ["dashboard_field_unavailable"] = "查询字段不存在或当前用户不可读取。",
            ["dashboard_filter_invalid"] = "仪表盘筛选条件无效。",
            ["dashboard_query_invalid"] = "仪表盘查询配置无效。",
            ["dashboard_measure_invalid"] = "仪表盘指标配置无效。",
            ["dashboard_measure_type_invalid"] = "指标与字段类型不兼容。",
            ["dashboard_time_bucket_invalid"] = "时间分组配置无效。",
            ["dashboard_time_zone_unsupported"] = "当前不支持该时区。",
            ["dashboard_atomic_endpoint_unavailable"] = "仪表盘保存服务不可用。",
            ["dashboard_atomic_response_invalid"] = "仪表盘服务返回了无效结果。",
            ["dashboard_not_found"] = "仪表盘不存在或当前用户无权读取。",
            ["dashboard_revision_required"] = "缺少仪表盘版本，请重新加载。",
            ["dashboard_edit_conflict"] = "仪表盘已被其他用户修改，请重新加载。",
            ["dashboard_panel_membership_invalid"] = "面板不属于当前仪表盘。",
            ["dashboard_panel_limit_exceeded"] = "仪表盘面板数量已达到上限。",
            ["dashboard_panel_options_too_large"] = "面板配置内容过大。",
            ["panel_type_unknown"] = "该面板类型不受支持。",
        };

    public static Failure Map(Exception exception)
    {
        if (exception is BackendUnavailableException)
            return new("DASHBOARD_BACKEND_UNAVAILABLE", "仪表盘服务暂不可用，请稍后重试。");
        if (exception is RpcRemoteException remote)
        {
            string? code = ReadBackendCode(remote.ErrorData);
            if (code is not null && SafeMessages.TryGetValue(code, out string? message))
                return new(code, message);
        }
        return new("DASHBOARD_OPERATION_FAILED", "仪表盘操作失败，请稍后重试。");
    }

    private static string? ReadBackendCode(JsonElement? data)
    {
        if (data is not JsonElement value || value.ValueKind != JsonValueKind.Object
            || !value.TryGetProperty("code", out var code)
            || code.ValueKind != JsonValueKind.String)
            return null;
        return code.GetString();
    }
}
