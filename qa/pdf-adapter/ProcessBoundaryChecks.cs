using System.Reflection;
using System.Runtime.InteropServices;
using System.Text.Json;

namespace PdfAdapterQualification;

internal static class ProcessBoundaryChecks
{
    internal static async Task<int> RunAsync()
    {
        string executable = Environment.ProcessPath
            ?? throw new InvalidOperationException("Missing qualification executable.");
        string[] prefix = Path.GetFileNameWithoutExtension(executable)
            .Equals("dotnet", StringComparison.OrdinalIgnoreCase)
            ? [Assembly.GetExecutingAssembly().Location] : [];
        var checks = new List<object>();
        int failures = 0;

        foreach (string name in new[]
        {
            "success", "deadline", "cancel", "pre-cancel", "cpu", "managed-memory",
            "native-memory", "stdout", "stderr", "descendants", "partial-failure", "start-failure",
        })
        {
            using var cancellation = new CancellationTokenSource();
            if (name == "cancel") cancellation.CancelAfter(200);
            if (name == "pre-cancel") cancellation.Cancel();
            var limits = new WorkerLimits(128L * 1024 * 1024,
                name == "cpu" ? TimeSpan.FromMilliseconds(200) : TimeSpan.FromSeconds(10),
                name == "deadline" ? TimeSpan.FromMilliseconds(300) :
                name == "descendants" ? TimeSpan.FromSeconds(2) : TimeSpan.FromSeconds(10),
                4096, 4096);
            string fixture = name switch
            {
                "deadline" or "cancel" or "pre-cancel" => "sleep",
                "cpu" => "spin",
                "managed-memory" => "allocate",
                "native-memory" => "native-allocate",
                "descendants" => "spawn",
                "partial-failure" => "partial-fail",
                _ => name,
            };
            bool boundaryFixture = name is "native-memory" or "stderr" or "partial-failure";
            string[] arguments = [.. prefix,
                boundaryFixture ? "--boundary-fixture" : "--fixture-worker", fixture];
            WorkerRunResult result = await BoundedProcessRunner.RunAsync(
                name == "start-failure" ? executable + ".does-not-exist" : executable,
                arguments, limits, cancellation.Token);
            string expected = name switch
            {
                "success" => "zero exit and complete stdout",
                "deadline" => "deadline terminates worker",
                "cancel" => "cancellation terminates worker",
                "pre-cancel" => "cancelled without creating a worker",
                "cpu" => "CPU budget terminates worker",
                "managed-memory" => "allocation fails within the job; unknown failures stay WorkerFailed",
                "native-memory" => "native commit is rejected by the job memory limit",
                "stdout" or "stderr" => "output limit terminates worker and discards stdout",
                "descendants" => "deadline terminates worker and its observed descendant",
                "partial-failure" => "nonzero exit discards otherwise valid partial stdout",
                _ => "process creation fails with no remaining processes",
            };
            bool common = result.AllProcessesExited &&
                result.Stderr.Length <= limits.MaxStderrBytes &&
                (result.Reason == WorkerRunReason.Succeeded || result.Stdout.Length == 0) &&
                (name is "pre-cancel" or "start-failure"
                    ? result.PeakWorkerWorkingSetBytes == 0
                    : result.PeakWorkerWorkingSetBytes > 0);
            bool behavior = name switch
            {
                "success" => result.Reason == WorkerRunReason.Succeeded && result.ExitCode == 0 &&
                    result.Stdout.Trim() == "{\"complete\":true}",
                "deadline" => result.Reason == WorkerRunReason.DeadlineExceeded,
                "cancel" or "pre-cancel" => result.Reason == WorkerRunReason.Cancelled,
                "cpu" => result.Reason == WorkerRunReason.CpuLimitExceeded,
                "managed-memory" => result.Reason is WorkerRunReason.MemoryLimitExceeded or
                    WorkerRunReason.WorkerFailed && result.ExitCode is not null and not 0,
                // Ordinary memory notifications may be lost. In that case the explicit
                // synthetic worker's denied-commit evidence proves this fixture's behavior;
                // the runner must still call the unknown failure WorkerFailed.
                "native-memory" => result.Reason == WorkerRunReason.MemoryLimitExceeded ||
                    (result.Reason == WorkerRunReason.WorkerFailed && result.ExitCode == 17 &&
                    result.Stderr.Contains("COMMIT_REJECTED", StringComparison.Ordinal)),
                "stdout" or "stderr" => result.Reason == WorkerRunReason.OutputLimitExceeded,
                "descendants" => result.Reason == WorkerRunReason.DeadlineExceeded &&
                    result.Stderr.Contains("FIXTURE_CHILD_PID=", StringComparison.Ordinal),
                "partial-failure" => result.Reason == WorkerRunReason.WorkerFailed && result.ExitCode == 27,
                _ => result.Reason == WorkerRunReason.WorkerFailed && result.ExitCode is null &&
                    result.Error is not null,
            };
            bool passed = common && behavior;
            if (!passed) failures++;
            checks.Add(new
            {
                name,
                expected,
                observed = new
                {
                    reason = result.Reason.ToString(),
                    result.ExitCode,
                    result.Stdout,
                    result.Stderr,
                    wallMilliseconds = result.WallTime.TotalMilliseconds,
                    cpuMilliseconds = result.CpuTime.TotalMilliseconds,
                    result.PeakJobMemoryBytes,
                    result.PeakWorkerWorkingSetBytes,
                    result.AllProcessesExited,
                    result.Error,
                },
                passed,
            });
        }
        Console.WriteLine(JsonSerializer.Serialize(new { checks, passed = failures == 0 },
            new JsonSerializerOptions(JsonSerializerDefaults.Web) { WriteIndented = true }));
        return failures == 0 ? 0 : 1;
    }

    internal static int RunFixture(string name)
    {
        switch (name)
        {
            case "stderr":
                Console.Error.Write(new string('x', 32768));
                return 0;
            case "partial-fail":
                Console.WriteLine("{\"partial\":true}");
                return 27;
            case "native-allocate":
            {
                nint previous = 0;
                Console.Error.WriteLine("NATIVE_COMMIT_STARTED");
                for (int index = 0; index < 64; index++)
                {
                    nint allocation = VirtualAlloc(0, 8 * 1024 * 1024, 0x3000, 4);
                    if (allocation == 0)
                    {
                        // Make room for the fixture's diagnostic after the failed commit.
                        // PeakJobMemoryUsed is reported unchanged; it is not a proof that
                        // every attempted allocation succeeded or a substitute for RSS.
                        if (previous != 0 && !VirtualFree(previous, 0, 0x8000)) return 18;
                        Console.Error.WriteLine($"COMMIT_REJECTED successfulBytes={index * 8L * 1024 * 1024}");
                        return 17;
                    }
                    previous = allocation;
                }
                return 2;
            }
            default:
                throw new ArgumentException("Unknown process-boundary fixture.", nameof(name));
        }
    }

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern nint VirtualAlloc(nint address, nuint size, uint allocationType, uint protection);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool VirtualFree(nint address, nuint size, uint freeType);
}
