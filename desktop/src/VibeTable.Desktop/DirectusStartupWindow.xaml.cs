using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Diagnostics;
using System.Linq;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Media;
using System.Windows.Threading;
using VibeTable.Infrastructure.Directus;

namespace VibeTable.Desktop;

public partial class DirectusStartupWindow : Window
{
    private static readonly string[] Steps =
    {
        "准备本地运行环境",
        "安装并校验 Directus",
        "初始化本地数据库",
        "启动 Directus 服务",
        "创建 VibeTable 数据结构",
        "启动应用后端",
        "自动登录并打开工作区",
    };

    private readonly Queue<string> _logLines = new();
    private readonly Stopwatch _elapsed = Stopwatch.StartNew();
    private readonly DispatcherTimer _timer;
    private bool _allowClose;
    private bool _cancelRequested;
    private bool _failed;
    private int _currentStep;
    private TaskCompletionSource<bool>? _failureDecision;

    public DirectusStartupWindow()
    {
        InitializeComponent();
        _timer = new DispatcherTimer(TimeSpan.FromSeconds(1), DispatcherPriority.Background,
            (_, _) => ElapsedText.Text = $"已用时 {(int)_elapsed.Elapsed.TotalSeconds} 秒",
            Dispatcher);
        _timer.Start();
        RenderSteps();
    }

    public event EventHandler? CancelRequested;

    public void UpdateProgress(DirectusStartupProgress progress)
    {
        if (_failed)
        {
            return;
        }
        int step = progress.Stage switch
        {
            DirectusStartupStage.PreparingRuntime => 0,
            DirectusStartupStage.CheckingPackages
                or DirectusStartupStage.InstallingPackages
                or DirectusStartupStage.VerifyingPackages
                or DirectusStartupStage.RepairingPackages => 1,
            DirectusStartupStage.InitializingDatabase => 2,
            DirectusStartupStage.StartingService
                or DirectusStartupStage.WaitingForService => 3,
            DirectusStartupStage.ApplyingSchema
                or DirectusStartupStage.Ready => 4,
            _ => _currentStep,
        };
        string title = progress.Stage switch
        {
            DirectusStartupStage.PreparingRuntime => "准备本地运行环境",
            DirectusStartupStage.CheckingPackages => "检查 Directus 依赖",
            DirectusStartupStage.InstallingPackages => "安装 Directus 依赖",
            DirectusStartupStage.VerifyingPackages => progress.UsedFastPath
                ? "依赖已安装，无需重复安装"
                : "校验 Directus 依赖",
            DirectusStartupStage.RepairingPackages => "修复 Directus 依赖",
            DirectusStartupStage.InitializingDatabase => "初始化本地数据库",
            DirectusStartupStage.StartingService => "启动 Directus 服务",
            DirectusStartupStage.WaitingForService => "等待 Directus 就绪",
            DirectusStartupStage.ApplyingSchema => "创建 VibeTable 数据结构",
            DirectusStartupStage.Ready => "Directus 已就绪",
            _ => "正在初始化",
        };
        UpdateStage(step, title, progress.Detail);
    }

    public void UpdateHostStage(int step, string title, string detail)
    {
        if (!_failed)
        {
            UpdateStage(step, title, detail);
        }
    }

    public void AppendLog(string line)
    {
        if (string.IsNullOrWhiteSpace(line))
        {
            return;
        }
        _logLines.Enqueue(line);
        while (_logLines.Count > 240)
        {
            _logLines.Dequeue();
        }
        LogBox.Text = string.Join(Environment.NewLine, _logLines);
        LogBox.ScrollToEnd();
    }

    public void ResetForRetry()
    {
        _cancelRequested = false;
        _failed = false;
        _failureDecision = null;
        _currentStep = 0;
        StageProgress.IsIndeterminate = true;
        StageProgress.Foreground = new SolidColorBrush(Color.FromRgb(42, 127, 158));
        CancelButton.IsEnabled = true;
        CancelButton.Content = "取消";
        CancelButton.Visibility = Visibility.Visible;
        RetryButton.Visibility = Visibility.Collapsed;
        ExitButton.Visibility = Visibility.Collapsed;
        HintText.Text = "正在重试；已完成的安装文件会继续复用。";
        StageTitleText.Text = "重新开始初始化";
        DetailText.Text = "正在重新检查本地运行环境…";
        RenderSteps();
    }

    public void ShowFailure(string message)
    {
        _failed = true;
        _failureDecision = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        StageTitleText.Text = "初始化未完成";
        DetailText.Text = message;
        DetailText.Foreground = new SolidColorBrush(Color.FromRgb(183, 28, 28));
        StageProgress.IsIndeterminate = false;
        StageProgress.Value = 100;
        StageProgress.Foreground = new SolidColorBrush(Color.FromRgb(198, 40, 40));
        HintText.Text = "初始化已停止。请查看日志，然后选择重试或退出。";
        CancelButton.Visibility = Visibility.Collapsed;
        RetryButton.Visibility = Visibility.Visible;
        RetryButton.IsEnabled = true;
        ExitButton.Visibility = Visibility.Visible;
        ExitButton.IsEnabled = true;
    }

    public Task<bool> WaitForFailureDecisionAsync()
        => _failureDecision?.Task ?? Task.FromResult(false);

    public void CompleteAndClose()
    {
        _currentStep = Steps.Length;
        RenderSteps();
        StageTitleText.Text = "首次启动完成";
        DetailText.Text = "本地数据服务和登录会话均已就绪。";
        DetailText.Foreground = new SolidColorBrush(Color.FromRgb(36, 55, 70));
        StageProgress.IsIndeterminate = false;
        StageProgress.Value = 100;
        _allowClose = true;
        Close();
    }

    public void ForceClose()
    {
        _allowClose = true;
        Close();
    }

    protected override void OnClosing(CancelEventArgs e)
    {
        if (!_allowClose)
        {
            e.Cancel = true;
            RequestCancel();
            return;
        }
        _timer.Stop();
        base.OnClosing(e);
    }

    private void OnCancelClick(object sender, RoutedEventArgs e) => RequestCancel();

    private void OnRetryClick(object sender, RoutedEventArgs e)
    {
        RetryButton.IsEnabled = false;
        ExitButton.IsEnabled = false;
        HintText.Text = "正在停止失败的进程并准备重试…";
        _failureDecision?.TrySetResult(true);
    }

    private void OnExitClick(object sender, RoutedEventArgs e)
    {
        RetryButton.IsEnabled = false;
        ExitButton.IsEnabled = false;
        _failureDecision?.TrySetResult(false);
    }

    private void RequestCancel()
    {
        if (_failed && _failureDecision is not null)
        {
            _failureDecision.TrySetResult(false);
            return;
        }
        if (_cancelRequested)
        {
            return;
        }
        _cancelRequested = true;
        CancelButton.IsEnabled = false;
        CancelButton.Content = "正在取消…";
        HintText.Text = "正在安全停止初始化进程…";
        CancelRequested?.Invoke(this, EventArgs.Empty);
    }

    private void UpdateStage(int step, string title, string detail)
    {
        _currentStep = Math.Max(_currentStep, step);
        StageTitleText.Text = title;
        DetailText.Text = TranslateDetail(detail);
        DetailText.Foreground = new SolidColorBrush(Color.FromRgb(36, 55, 70));
        RenderSteps();
    }

    private void RenderSteps()
    {
        StepsList.ItemsSource = Steps.Select((text, index) =>
        {
            string prefix = index < _currentStep ? "✓" : index == _currentStep ? "›" : "○";
            return $"{prefix}  {text}";
        }).ToArray();
    }

    private static string TranslateDetail(string detail)
    {
        if (detail.StartsWith("Starting the local Directus service on port ",
            StringComparison.Ordinal))
        {
            string port = detail["Starting the local Directus service on port ".Length..]
                .TrimEnd('.');
            return $"正在端口 {port} 启动本地 Directus 服务。";
        }
        return detail switch
        {
            "Preparing the local Directus runtime directory." => "正在准备本地 Directus 运行目录。",
            "Checking the local Directus package cache." => "正在检查已安装的 npm 包和校验缓存。",
            "The cached package verification is current; no reinstall is needed." =>
                "依赖完整性校验仍然有效，本次不会重复安装。",
            "Installing Directus packages. The first run can take several minutes." =>
                "正在安装 Directus npm 包；首次安装可能需要几分钟。",
            "Verifying package structure and native modules." => "正在校验包结构和原生模块。",
            "The previous initialization did not finish; rechecking all package files and native modules." =>
                "检测到上次初始化未完成，正在重新校验全部包文件和原生模块。",
            "Package verification failed; repairing the local installation once." =>
                "完整性校验失败，正在自动修复本地安装。",
            "Verifying the repaired package installation." => "正在校验修复后的依赖。",
            "Creating the Directus database and local administrator." =>
                "正在创建本地数据库和管理员账号。",
            "The Directus database already exists; checking initialization state." =>
                "检测到未完成的本地数据库，正在重新初始化。",
            "Waiting for the Directus health endpoint to respond." =>
                "服务进程已启动，正在等待健康检查通过。",
            "Creating the initial VibeTable collections, relations, and permissions." =>
                "正在创建初始数据表、关系和权限。",
            "Directus initialization is complete." => "Directus 初始化已完成。",
            _ => detail,
        };
    }
}
