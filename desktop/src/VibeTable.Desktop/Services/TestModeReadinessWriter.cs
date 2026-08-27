using System;
using System.Collections.Generic;
using System.IO;
using System.Text.Json;

namespace VibeTable.Desktop.Services;

/// <summary>
/// Writes the machine-readable shell-readiness file consumed by the desktop
/// smoke test. The file is written once after backend handshake, WebView2
/// navigation, and the renderer <c>app.ready</c> handshake all succeed.
/// </summary>
/// <remarks>
/// <para>
/// The smoke test polls for the file and asserts all three startup boundaries.
/// </para>
/// <para>
/// On a fatal startup error, the writer emits
/// <c>{"ready":false,...,"error":"&lt;message&gt;"}</c> so the test surfaces a
/// clear failure instead of timing out.
/// </para>
/// </remarks>
public sealed class TestModeReadinessWriter
{
    private const string ReadinessFileName = "vibetable-readiness.json";

    private readonly string _directory;
    private int _written;

    public TestModeReadinessWriter(string? directory)
    {
        _directory = string.IsNullOrWhiteSpace(directory)
            ? Path.GetTempPath()
            : directory!;
        Directory.CreateDirectory(_directory);
    }

    /// <summary>The full path to the readiness file.</summary>
    public string ReadinessPath => Path.Combine(_directory, ReadinessFileName);

    /// <summary>
    /// Appends a diagnostic trace line (test mode only) to
    /// <c>vibetable-trace.log</c> in the readiness directory. Used to diagnose where
    /// startup stalls; harmless in production (writer is null there).
    /// </summary>
    public void Trace(string message)
    {
        try
        {
            var line = $"[{DateTimeOffset.UtcNow:O}] {message}{Environment.NewLine}";
            File.AppendAllText(Path.Combine(_directory, "vibetable-trace.log"), line);
        }
        catch
        {
            // Best-effort: tracing must never break the flow.
        }
    }

    /// <summary>
    /// Writes the shell smoke result after the real backend
    /// handshake, WebView2 navigation, and renderer <c>app.ready</c> bridge
    /// handshake have all completed. No external server is required
    /// for this startup-contract check.
    /// </summary>
    public void WriteShellReady()
    {
        if (System.Threading.Interlocked.Exchange(ref _written, 1) != 0)
        {
            return;
        }
        var payload = new
        {
            ready = true,
            mode = "shell",
            backendReady = true,
            webViewReady = true,
            rendererReady = true,
            error = (string?)null,
            writtenAt = DateTimeOffset.UtcNow.ToString("o"),
        };
        Write(payload);
    }

    /// <summary>
    /// Writes shell readiness together with the update-only workspace health
    /// evidence. Keeping <c>mode=shell</c> preserves the packaged-host smoke
    /// contract while the nested receipt proves whether a real workspace was
    /// checked or no workspace was registered in the isolated profile.
    /// </summary>
    internal void WriteUpdateReady(UpdateWorkspaceHealthProbeReceipt receipt)
    {
        ArgumentNullException.ThrowIfNull(receipt);
        if (System.Threading.Interlocked.Exchange(ref _written, 1) != 0)
        {
            return;
        }
        var payload = new
        {
            ready = true,
            mode = "shell",
            backendReady = true,
            webViewReady = true,
            rendererReady = true,
            workspaceProbe = new
            {
                status = receipt.Status switch
                {
                    UpdateWorkspaceHealthProbeStatus.Healthy => "healthy",
                    _ => "skippedNoRegisteredWorkspace",
                },
                workspaceId = receipt.WorkspaceId?.ToString("D"),
                sessionEpoch = receipt.SessionEpoch,
                tableCount = receipt.TableCount,
            },
            error = (string?)null,
            writtenAt = DateTimeOffset.UtcNow.ToString("o"),
        };
        Write(payload);
    }

    /// <summary>
    /// Writes a failure readiness report (the smoke test surfaces this as an
    /// error rather than timing out).
    /// </summary>
    public void WriteError(string message)
    {
        if (System.Threading.Interlocked.Exchange(ref _written, 1) != 0)
        {
            return;
        }
        var payload = new
        {
            ready = false,
            mode = (string?)null,
            error = message,
            writtenAt = DateTimeOffset.UtcNow.ToString("o"),
        };
        Write(payload);
    }

    private void Write(object payload)
    {
        var json = JsonSerializer.Serialize(payload,
            new JsonSerializerOptions(JsonSerializerDefaults.Web));
        // Write atomically: write to a temp file then move, so the test never
        // observes a half-written file.
        var tmp = ReadinessPath + ".tmp";
        File.WriteAllText(tmp, json);
        if (File.Exists(ReadinessPath))
        {
            File.Delete(ReadinessPath);
        }
        File.Move(tmp, ReadinessPath);
    }
}
