using System.Diagnostics;
using System.Reflection;
using System.Text.Json;

namespace PdfAdapterQualification;

internal static class Program
{
    internal static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);

    public static async Task<int> Main(string[] args)
    {
        if (args.Length == 1 && args[0] == "--check-process-boundary")
            return await ProcessBoundaryChecks.RunAsync();
        if (args.Length == 2 && args[0] == "--boundary-fixture")
            return ProcessBoundaryChecks.RunFixture(args[1]);
        if (args.Length == 4 && args[0] == "--run" && args[2] == "--memory-mib")
        {
            if (!int.TryParse(args[3], out int memoryMiB) || memoryMiB <= 0)
                return 2;
            await RunObserved(args[1], checked((long)memoryMiB * 1024 * 1024));
            return 0;
        }
        if (args.Length == 2 && args[0] == "--worker")
        {
            Console.WriteLine(JsonSerializer.Serialize(PdfWorker.Extract(args[1]), JsonOptions));
            return 0;
        }
        if (args.Length == 2 && args[0] == "--fixture-worker")
            return RunFixture(args[1]);
        Console.Error.WriteLine("PDF adapter qualification: --run <source> --memory-mib <budget> or --check-process-boundary");
        return 2;
    }

    private static async Task RunObserved(string source, long memoryBytes)
    {
        using var cancelled = new CancellationTokenSource();
        ConsoleCancelEventHandler cancelHandler = (_, signal) =>
        {
            signal.Cancel = true;
            cancelled.Cancel();
        };
        Console.CancelKeyPress += cancelHandler;
        WorkerRunResult run;
        try
        {
            string executable = Environment.ProcessPath
                ?? throw new InvalidOperationException("Missing process executable.");
            var arguments = new List<string>();
            if (Path.GetFileNameWithoutExtension(executable).Equals("dotnet", StringComparison.OrdinalIgnoreCase))
                arguments.Add(Assembly.GetExecutingAssembly().Location);
            arguments.Add("--worker");
            arguments.Add(Path.GetFullPath(source));
            run = await BoundedProcessRunner.RunAsync(executable, arguments,
                new WorkerLimits(memoryBytes, TimeSpan.FromSeconds(30), TimeSpan.FromSeconds(30),
                    32 * 1024 * 1024, 64 * 1024), cancelled.Token);
        }
        finally
        {
            Console.CancelKeyPress -= cancelHandler;
        }
        PdfResult result = ResultFromRun(run);
        Console.WriteLine(JsonSerializer.Serialize(new
        {
            file = Path.GetFileName(source),
            result = new { result.Status, result.Text, result.ErrorCode },
            elapsedMilliseconds = (long)run.WallTime.TotalMilliseconds,
            cpuMilliseconds = run.CpuTime.TotalMilliseconds,
            peakJobMemoryBytes = run.PeakJobMemoryBytes,
            peakWorkerWorkingSetBytes = run.PeakWorkerWorkingSetBytes,
            allProcessesExited = run.AllProcessesExited,
            workerReason = run.Reason.ToString(),
        }, JsonOptions));
    }

    private static PdfResult ResultFromRun(WorkerRunResult run)
    {
        if (run.AllProcessesExited && run.Reason == WorkerRunReason.Succeeded)
        {
            try
            {
                var result = JsonSerializer.Deserialize<PdfResult>(run.Stdout, JsonOptions);
                if (result is not null && result.Status is not null && result.Text is not null)
                    return result;
            }
            catch (JsonException) { }
            return new("failed", "", "extract.worker_response_invalid", 0, 0);
        }
        var (status, code) = run.Reason switch
        {
            WorkerRunReason.Cancelled => ("cancelled", "extract.cancelled"),
            WorkerRunReason.DeadlineExceeded => ("resourceLimited", "extract.timeout"),
            WorkerRunReason.CpuLimitExceeded => ("resourceLimited", "extract.cpu_limit"),
            WorkerRunReason.MemoryLimitExceeded => ("resourceLimited", "extract.memory_limit"),
            WorkerRunReason.OutputLimitExceeded => ("failed", "extract.worker_output_limit"),
            WorkerRunReason.CleanupFailed => ("failed", "extract.worker_cleanup_failed"),
            _ => ("failed", "extract.worker_failed"),
        };
        return new(status, "", code, 0, 0);
    }

    private static int RunFixture(string name)
    {
        Console.Error.WriteLine("FIXTURE_STARTED");
        switch (name)
        {
            case "success":
                Console.WriteLine("{\"complete\":true}");
                return 0;
            case "spin":
            {
                var timer = Stopwatch.StartNew();
                while (timer.Elapsed < TimeSpan.FromSeconds(60)) Thread.SpinWait(100_000);
                return 2;
            }
            case "sleep":
                Thread.Sleep(TimeSpan.FromSeconds(60));
                return 2;
            case "allocate":
            {
                var retained = new List<byte[]>();
                for (int index = 0; index < 64; index++)
                {
                    var block = new byte[8 * 1024 * 1024];
                    Array.Fill(block, (byte)index);
                    retained.Add(block);
                }
                GC.KeepAlive(retained);
                return 2;
            }
            case "stdout":
                for (int index = 0; index < 1024; index++)
                    Console.Write(new string('x', 64 * 1024));
                return 0;
            case "spawn":
            {
                string executable = Environment.ProcessPath
                    ?? throw new InvalidOperationException("Missing process executable.");
                var start = new ProcessStartInfo(executable)
                {
                    UseShellExecute = false,
                    CreateNoWindow = true,
                };
                if (Path.GetFileNameWithoutExtension(executable).Equals("dotnet", StringComparison.OrdinalIgnoreCase))
                    start.ArgumentList.Add(Assembly.GetExecutingAssembly().Location);
                start.ArgumentList.Add("--fixture-worker");
                start.ArgumentList.Add("sleep");
                using var child = Process.Start(start)
                    ?? throw new InvalidOperationException("Fixture child did not start.");
                Console.Error.WriteLine($"FIXTURE_CHILD_PID={child.Id}");
                Thread.Sleep(TimeSpan.FromSeconds(60));
                return 2;
            }
            default:
                return 2;
        }
    }
}


