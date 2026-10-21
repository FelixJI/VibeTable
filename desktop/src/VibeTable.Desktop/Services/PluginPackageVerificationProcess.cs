using System.Buffers.Binary;
using System.Diagnostics;
using System.IO;
using System.Text;
using VibeTable.Infrastructure.Backend;

namespace VibeTable.Desktop.Services;

internal static class PluginPackageVerificationProcess
{
    // Same isolated-file parser budgets as DocumentDiffWorkerSupervisor/Coordinator.
    private const ulong MemoryLimit = 1024UL * 1024 * 1024;
    private static readonly TimeSpan Deadline = TimeSpan.FromSeconds(30);
    private static readonly TimeSpan ExitGrace = TimeSpan.FromSeconds(5);
    private static ReadOnlySpan<byte> Ready => "VTPLUGIN_VERIFY 1\n"u8;

    internal static Task VerifyAsync(FileStream source, string packageHash, CancellationToken token) =>
        VerifyAsync(source, packageHash, CreateStartInfo(), Deadline, token);

    internal static ProcessStartInfo CreateStartInfo()
    {
        BackendLaunchOptions options = BackendLaunchOptions.ResolveForHost();
        if (!Path.IsPathFullyQualified(options.Command) || options.Arguments is not ("" or "-m backend"))
            throw new IOException("A trusted packaged backend or development interpreter is required.");
        var start = new ProcessStartInfo(options.Command)
        {
            Arguments = string.IsNullOrEmpty(options.Arguments)
                ? "--verify-retained-plugin-package"
                : options.Arguments + " --verify-retained-plugin-package",
            WorkingDirectory = options.WorkingDirectory ?? AppContext.BaseDirectory,
        };
        foreach ((string key, string value) in options.Environment) start.Environment[key] = value;
        return start;
    }

    // Internal process/timeout seam is for fault tests; the product always uses the fixed mode above.
    internal static async Task VerifyAsync(FileStream source, string packageHash,
        ProcessStartInfo start, TimeSpan timeout, CancellationToken token, Action<int>? onStarted = null)
    {
        if (!OperatingSystem.IsWindows())
            throw new PlatformNotSupportedException("Package verification requires Windows jobs.");
        if (source.Length > 64 * 1024 * 1024) throw new IOException("Package exceeds its installed size limit.");
        if (timeout <= TimeSpan.Zero || timeout > Deadline) throw new ArgumentOutOfRangeException(nameof(timeout));
        token.ThrowIfCancellationRequested();
        start.UseShellExecute = false;
        start.CreateNoWindow = true;
        start.RedirectStandardInput = true;
        start.RedirectStandardOutput = true;
        start.RedirectStandardError = true;
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(token);
        deadline.CancelAfter(timeout);
        using JobObject job = JobObject.Create(processMemoryLimit: MemoryLimit);
        using var process = new Process { StartInfo = start };
        bool started = false;
        Task<byte[]> output = Task.FromResult(Array.Empty<byte>());
        Task<byte[]> error = Task.FromResult(Array.Empty<byte>());
        try
        {
            if (!process.Start()) throw new IOException("Package verifier did not start.");
            started = true;
            // No untrusted input is sent until the job owns the process and fixed mode is ready.
            job.AssignProcess(process.SafeHandle.DangerousGetHandle());
            onStarted?.Invoke(process.Id);
            error = ReadBoundedAsync(process.StandardError.BaseStream, 8192, deadline);
            byte[] ready = new byte[Ready.Length];
            await process.StandardOutput.BaseStream.ReadExactlyAsync(ready, deadline.Token).ConfigureAwait(false);
            if (!ready.AsSpan().SequenceEqual(Ready)) throw new IOException("Invalid package verifier handshake.");
            output = ReadBoundedAsync(process.StandardOutput.BaseStream, 72, deadline);
            source.Position = 0;
            byte[] header = new byte[8];
            BinaryPrimitives.WriteUInt64BigEndian(header, (ulong)source.Length);
            await process.StandardInput.BaseStream.WriteAsync(header, deadline.Token).ConfigureAwait(false);
            byte[] buffer = new byte[64 * 1024];
            long remaining = source.Length;
            while (remaining > 0)
            {
                int count = await source.ReadAsync(buffer.AsMemory(0, (int)Math.Min(buffer.Length, remaining)),
                    deadline.Token).ConfigureAwait(false);
                if (count == 0) throw new IOException("Incomplete retained package.");
                await process.StandardInput.BaseStream.WriteAsync(buffer.AsMemory(0, count), deadline.Token).ConfigureAwait(false);
                remaining -= count;
            }
            await process.StandardInput.BaseStream.FlushAsync(deadline.Token).ConfigureAwait(false);
            process.StandardInput.Close();
            await process.WaitForExitAsync(deadline.Token).ConfigureAwait(false);
            byte[] result = await output.ConfigureAwait(false);
            byte[] diagnostics = await error.ConfigureAwait(false);
            if (process.ExitCode != 0 || diagnostics.Length != 0 || result.Length != 72 || result[^1] != (byte)'\n'
                || Encoding.ASCII.GetString(result.AsSpan(0, 71)) != packageHash)
                throw new IOException("Retained package differs from its installed identity or exceeds verification resources.");
        }
        catch (Exception failure)
        {
            if (started)
            {
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
                    throw new IOException("Package verifier exit could not be confirmed.", exitFailure);
                }
            }
            token.ThrowIfCancellationRequested();
            if (output.IsFaulted) await output.ConfigureAwait(false);
            if (error.IsFaulted) await error.ConfigureAwait(false);
            if (failure is OperationCanceledException)
                throw new TimeoutException("Package verifier exceeded its execution budget.", failure);
            throw;
        }
        finally
        {
            deadline.Cancel();
            try { await Task.WhenAll(output, error).ConfigureAwait(false); }
            catch (Exception drainFailure) when (drainFailure is OperationCanceledException or IOException) { }
        }
    }

    private static async Task<byte[]> ReadBoundedAsync(Stream stream, int limit, CancellationTokenSource deadline)
    {
        using var output = new MemoryStream();
        byte[] buffer = new byte[Math.Min(1024, limit + 1)];
        while (true)
        {
            int count = await stream.ReadAsync(buffer.AsMemory(0, Math.Min(buffer.Length,
                limit - (int)output.Length + 1)), deadline.Token).ConfigureAwait(false);
            if (count == 0) return output.ToArray();
            if (output.Length + count > limit)
            {
                deadline.Cancel();
                throw new IOException("Package verifier output exceeds its limit.");
            }
            output.Write(buffer, 0, count);
        }
    }
}
