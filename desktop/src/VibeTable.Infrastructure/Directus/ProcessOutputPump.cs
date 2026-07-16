using System;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace VibeTable.Infrastructure.Directus;

/// <summary>Drains both redirected process streams concurrently and forwards lines live.</summary>
internal static class ProcessOutputPump
{
    internal static async Task<(string Stdout, string Stderr)> CaptureUntilExitAsync(
        Process process,
        TimeSpan timeout,
        CancellationToken cancellationToken,
        Action<string>? stdoutLine = null,
        Action<string>? stderrLine = null)
    {
        Task<string> stdoutTask = ReadLinesAsync(
            process.StandardOutput, stdoutLine, cancellationToken);
        Task<string> stderrTask = ReadLinesAsync(
            process.StandardError, stderrLine, cancellationToken);

        try
        {
            await process.WaitForExitAsync(cancellationToken)
                .WaitAsync(timeout, cancellationToken)
                .ConfigureAwait(false);
        }
        catch
        {
            try
            {
                if (!process.HasExited)
                {
                    process.Kill(entireProcessTree: true);
                }
            }
            catch
            {
                // The original timeout/cancellation remains authoritative.
            }
            try { await Task.WhenAll(stdoutTask, stderrTask).ConfigureAwait(false); }
            catch { /* preserve the original exception */ }
            throw;
        }

        string[] output = await Task.WhenAll(stdoutTask, stderrTask).ConfigureAwait(false);
        return (output[0], output[1]);
    }

    private static async Task<string> ReadLinesAsync(
        StreamReader reader,
        Action<string>? onLine,
        CancellationToken cancellationToken)
    {
        var buffer = new StringBuilder();
        while (true)
        {
            string? line = await reader.ReadLineAsync(cancellationToken).ConfigureAwait(false);
            if (line is null)
            {
                return buffer.ToString();
            }
            buffer.AppendLine(line);
            try { onLine?.Invoke(line); }
            catch { /* observers must never stop draining a child process */ }
        }
    }
}
