using System.Text;
using System.Text.Json;
using VibeTable.Contracts;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Closed host text-clipboard routes. Every request captures a live workspace
/// session lease, runs under a bounded deadline, and replies through the
/// emission-gated sink so the session check commits atomically with the real
/// UI-queue post. Empty-success stays strictly apart from
/// unavailable/busy/failed/timeout; an over-budget reply fails as a whole;
/// clipboard bodies never enter logs or failure messages.
/// </summary>
internal sealed class ClipboardRequestController(
    IWebReplySink reply, WorkspaceSessionEnvelopeFilter sessions, ITextClipboard clipboard,
    TimeSpan? requestDeadline = null)
{
    // Below the renderer bridge's 10 s budget so the host replies a typed
    // CLIPBOARD_TIMEOUT instead of letting the bridge abandon silently.
    internal static readonly TimeSpan DefaultDeadline = TimeSpan.FromSeconds(8);

    private static readonly JsonSerializerOptions WebOptions = new(JsonSerializerDefaults.Web);

    internal static bool Handles(string type) => type is "clipboard.readText" or "clipboard.writeText";

    internal async Task DispatchAsync(RoutedWebRequest request)
    {
        if (request.Scope is not { } scope || !sessions.TryCapture(scope, out var lease))
        {
            reply.PostOperationFailed(request.RequestId, "Clipboard requires the current workspace.", "BAD_WORKSPACE_SCOPE");
            return;
        }
        using (lease)
        using (var timeout = CancellationTokenSource.CreateLinkedTokenSource(lease!.CancellationToken))
        {
            timeout.CancelAfter(requestDeadline ?? DefaultDeadline);
            try
            {
                object payload;
                switch (request.Type)
                {
                    case "clipboard.readText":
                    {
                        Empty(request.Payload);
                        TextClipboardResult read = await clipboard.ReadTextAsync(timeout.Token);
                        if (read.Outcome != TextClipboardOutcome.Succeeded)
                        {
                            PostOutcomeFailure(request, lease, DeadlineOutcome(lease, timeout, read.Outcome));
                            return;
                        }
                        var readPayload = new { text = read.Text };
                        if (ReplyEnvelopeBytes(request.Type, request.RequestId, readPayload) > WebMessageRouter.MaxMessageBytes)
                        {
                            PostFailure(request, lease, "剪贴板内容超过消息预算，请改用文件导入。", "CLIPBOARD_TOO_LARGE");
                            return;
                        }
                        payload = readPayload;
                        break;
                    }
                    case "clipboard.writeText":
                    {
                        string text = ReadTextParameter(request.Payload);
                        TextClipboardResult write = await clipboard.WriteTextAsync(text, timeout.Token);
                        if (write.Outcome != TextClipboardOutcome.Succeeded)
                        {
                            PostOutcomeFailure(request, lease, DeadlineOutcome(lease, timeout, write.Outcome));
                            return;
                        }
                        payload = new { written = true };
                        break;
                    }
                    default:
                        PostFailure(request, lease, $"Unhandled clipboard request '{request.Type}'.", "UNKNOWN_TYPE");
                        return;
                }
                TryPostResponseWhileCurrent(lease, request.Type, request.RequestId, payload);
            }
            catch (OperationCanceledException)
            {
                PostFailure(request, lease, "Workspace session changed.", "WORKSPACE_SESSION_STALE");
            }
            catch (JsonException)
            {
                PostFailure(request, lease, "Invalid clipboard request.", "BAD_PAYLOAD");
            }
        }
    }

    /// <summary>An abort traced to the request deadline (not the session) is a timeout.</summary>
    private static TextClipboardOutcome DeadlineOutcome(
        WorkspaceRequestEpochLease lease,
        CancellationTokenSource timeout,
        TextClipboardOutcome outcome)
        => outcome == TextClipboardOutcome.Unavailable
            && timeout.Token.IsCancellationRequested
            && !lease.CancellationToken.IsCancellationRequested
                ? TextClipboardOutcome.TimedOut
                : outcome;

    private void PostOutcomeFailure(RoutedWebRequest request, WorkspaceRequestEpochLease? lease, TextClipboardOutcome outcome)
    {
        (string message, string code) = outcome switch
        {
            TextClipboardOutcome.Busy => ("系统剪贴板正被其他程序占用，请重试。", "CLIPBOARD_BUSY"),
            TextClipboardOutcome.Unavailable => ("系统剪贴板当前不可用。", "CLIPBOARD_UNAVAILABLE"),
            TextClipboardOutcome.TimedOut => ("系统剪贴板操作超时，请重试。", "CLIPBOARD_TIMEOUT"),
            _ => ("剪贴板操作失败，请重试。", "CLIPBOARD_FAILED"),
        };
        PostFailure(request, lease, message, code);
    }

    private void PostFailure(RoutedWebRequest request, WorkspaceRequestEpochLease? lease, string message, string code)
        => reply.TryPostOperationFailed(
            request.RequestId, message, code,
            emitGate: commit => sessions.TryUseCurrent(lease, commit));

    /// <summary>Session check and the real post commit atomically at emission.</summary>
    private void TryPostResponseWhileCurrent(
        WorkspaceRequestEpochLease? lease, string type, string? requestId, object payload)
        => reply.TryPostResponse(
            type, requestId, payload,
            emitGate: commit => sessions.TryUseCurrent(lease, commit));

    private static int ReplyEnvelopeBytes(string type, string? requestId, object payload)
    {
        var envelope = new Dictionary<string, object?>
        {
            ["type"] = type,
            ["requestId"] = requestId,
            ["payload"] = payload,
        };
        return Encoding.UTF8.GetByteCount(JsonSerializer.Serialize(envelope, WebOptions));
    }

    private static void Empty(JsonElement payload)
    {
        if (payload.ValueKind != JsonValueKind.Object || payload.EnumerateObject().Any())
        {
            throw new JsonException("Expected empty parameters.");
        }
    }

    private static string ReadTextParameter(JsonElement payload)
    {
        if (payload.ValueKind != JsonValueKind.Object
            || payload.EnumerateObject().Count() != 1
            || !payload.TryGetProperty("text", out JsonElement text)
            || text.ValueKind != JsonValueKind.String)
        {
            throw new JsonException("Expected { text }.");
        }
        return text.GetString()!;
    }
}
