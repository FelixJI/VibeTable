using System.Diagnostics;
using System.IO;
using System.Text.Json;
using VibeTable.Infrastructure.Backend;

namespace VibeTable.Desktop.Services;

internal static class DocumentDiffWorkerSupervisor
{
    private const ulong MemoryLimit = 1024UL * 1024 * 1024;
    private static readonly TimeSpan ExitGrace = TimeSpan.FromSeconds(5);

    public static Task<int> RunAsync(string executablePath, string operationDirectory,
        TimeSpan timeout, CancellationToken cancellationToken) =>
        RunAsync(new ProcessStartInfo(executablePath), operationDirectory, timeout, cancellationToken);

    // ProcessStartInfo is an internal launch seam for real-process fault tests;
    // the product caller always selects the fixed worker from its package layout.
    internal static async Task<int> RunAsync(ProcessStartInfo startInfo, string operationDirectory,
        TimeSpan timeout, CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(startInfo);
        if (!OperatingSystem.IsWindows())
            throw new PlatformNotSupportedException("Document workers require Windows jobs.");
        if (timeout <= TimeSpan.Zero || timeout > TimeSpan.FromMinutes(10))
            throw new ArgumentOutOfRangeException(nameof(timeout));
        cancellationToken.ThrowIfCancellationRequested();
        startInfo.UseShellExecute = false;
        startInfo.CreateNoWindow = true;
        startInfo.RedirectStandardInput = true;
        startInfo.RedirectStandardOutput = true;
        startInfo.RedirectStandardError = true;
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        deadline.CancelAfter(timeout);
        using JobObject job = JobObject.Create(processMemoryLimit: MemoryLimit);
        using var process = new Process { StartInfo = startInfo };
        bool started = false;
        Task output = Task.CompletedTask;
        Task error = Task.CompletedTask;
        try
        {
            if (!process.Start())
                throw new IOException("Document worker did not start.");
            started = true;
            job.AssignProcess(process.SafeHandle.DangerousGetHandle());
            output = process.StandardOutput.BaseStream.CopyToAsync(Stream.Null, deadline.Token);
            error = process.StandardError.BaseStream.CopyToAsync(Stream.Null, deadline.Token);
            await JsonSerializer.SerializeAsync(process.StandardInput.BaseStream,
                new { version = 1, operationDirectory }, cancellationToken: deadline.Token).ConfigureAwait(false);
            await process.StandardInput.BaseStream.FlushAsync(deadline.Token).ConfigureAwait(false);
            process.StandardInput.Close();
            await process.WaitForExitAsync(deadline.Token).ConfigureAwait(false);
            await Task.WhenAll(output, error).ConfigureAwait(false);
            return process.ExitCode;
        }
        catch (Exception failure)
        {
            if (started)
            {
                // Never return a cancellation/timeout until the exact process
                // has exited. Unknown exit is a separate preserve-artifacts state.
                job.Dispose();
                try
                {
                    if (!process.HasExited)
                    {
                        try { process.Kill(entireProcessTree: true); }
                        catch (InvalidOperationException) when (process.HasExited) { }
                        catch (System.ComponentModel.Win32Exception) when (process.HasExited) { }
                    }
                    using var exitDeadline = new CancellationTokenSource(ExitGrace);
                    await process.WaitForExitAsync(exitDeadline.Token).ConfigureAwait(false);
                }
                catch (Exception exitFailure)
                {
                    throw new DocumentDiffWorkerExitUnknownException(process.Id, exitFailure);
                }
            }
            if (failure is OperationCanceledException && !cancellationToken.IsCancellationRequested)
                throw new TimeoutException("Document worker exceeded its execution budget.", failure);
            throw;
        }
        finally
        {
            deadline.Cancel();
            // Drain tasks own no files. Observe their failures without retaining
            // document-controlled output or replacing the primary process result.
            try { await Task.WhenAll(output, error).ConfigureAwait(false); }
            catch (Exception drainFailure) when (drainFailure is OperationCanceledException or IOException) { }
        }
    }
}

internal sealed class DocumentDiffWorkerExitUnknownException(int processId, Exception inner)
    : IOException("Document worker exit could not be confirmed; retain its artifacts.", inner)
{
    public int ProcessId { get; } = processId;
}
