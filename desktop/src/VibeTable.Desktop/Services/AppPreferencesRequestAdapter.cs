using System.Text.Json;
using VibeTable.Contracts;

namespace VibeTable.Desktop.Services;

/// <summary>Owns settings decoding, correlation, and the single terminal attempt.</summary>
internal sealed class AppPreferencesRequestAdapter
{
    private readonly IWebReplySink _reply;
    private readonly AppPreferencesUseCases _preferences;
    private readonly Action<string> _trace;

    public AppPreferencesRequestAdapter(
        IWebReplySink reply,
        AppPreferencesUseCases preferences,
        Action<string> trace)
    {
        _reply = reply ?? throw new ArgumentNullException(nameof(reply));
        _preferences = preferences ?? throw new ArgumentNullException(nameof(preferences));
        _trace = trace ?? throw new ArgumentNullException(nameof(trace));
    }

    public static bool Handles(string type) => type is
        AppSettingsWireMethods.AppPreferencesGet or AppSettingsWireMethods.AppPreferencesUpdate;

    public void Dispatch(RoutedWebRequest request)
    {
        ArgumentNullException.ThrowIfNull(request);
        AppSettingsPreferences? result = null;
        string? code = null;
        string? message = null;
        if (!Handles(request.Type))
        {
            code = "UNKNOWN_TYPE";
            message = "应用请求类型无效。";
        }
        else if (request.Type == AppSettingsWireMethods.AppPreferencesGet)
        {
            if (request.Payload.ValueKind != JsonValueKind.Object
                || request.Payload.EnumerateObject().Any())
            {
                code = AppSettingsErrorCodes.BadPayload;
                message = "The application preferences request is invalid.";
            }
            else
            {
                try { result = _preferences.Get(); }
                catch (Exception exception)
                {
                    TraceFailure("Application preferences read", exception);
                    code = AppSettingsErrorCodes.ReadFailed;
                    message = "无法读取桌面应用设置。";
                }
            }
        }
        else if (!AppSettingsPatchReader.TryRead(request.Payload, out AppSettingsPreferencesPatch? patch)
            || patch is null)
        {
            code = AppSettingsErrorCodes.BadPayload;
            message = "The application preferences update is invalid.";
        }
        else
        {
            try { result = _preferences.Update(patch); }
            catch (Exception exception)
            {
                TraceFailure("Application preferences update", exception);
                // Persistence or native effects may already have committed; never replay.
                code = AppSettingsErrorCodes.WriteFailed;
                message = "无法保存桌面应用设置，请检查当前用户权限后重试。";
            }
        }

        try
        {
            if (result is not null)
                _reply.PostResponse(request.Type, request.RequestId, result);
            else
                _reply.PostOperationFailed(request.RequestId, message!, code!);
        }
        catch (Exception exception)
        {
            TraceFailure("Application preferences reply", exception);
        }
    }

    private void TraceFailure(string operation, Exception exception)
        => _trace($"{operation} failed; exception={exception.GetType().Name}");
}
