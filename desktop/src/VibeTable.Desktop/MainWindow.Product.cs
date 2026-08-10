using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Runtime.InteropServices;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using Microsoft.Win32;
using VibeTable.Contracts;
using VibeTable.Desktop.Services;
using VibeTable.Desktop.ViewModels;
using VibeTable.DocumentDiff.OpenXml;
using VibeTable.Infrastructure.Backend;
using VibeTable.Infrastructure.PocketBase;
using VibeTable.Infrastructure.Rpc;
using VibeTable.Infrastructure.Workspace;

namespace VibeTable.Desktop;

/// <summary>
/// PocketBase-only desktop composition root. Provider lifecycle, credentials,
/// and filesystem capabilities remain in native services; the renderer sees
/// only the closed product contracts.
/// </summary>
public partial class MainWindow : Window
{
    private readonly ProductionWorkspaceRuntimeFactory _runtime;
    private readonly WebMessageRouter _router;
    private readonly ProductWebViewBridge _webBridge;
    private readonly ShellBootstrap _shellBootstrap;
    private readonly WorkspaceRegistry _workspaceRegistry;
    private readonly WorkspaceSessionManager _workspaceSessions;
    private readonly WorkspaceSessionEnvelopeFilter _workspaceSessionFilter;
    private readonly WorkspaceReplicaStatusMonitor _replicaStatusMonitor;
    private readonly WorkspaceProviderPolicy _providerPolicy;
    private readonly WorkspaceRepositoryOnboardingService _repositoryOnboarding;
    private readonly WorkspaceReplicaRecoveryService _replicaRecovery;
    private readonly IWorkspaceRepositoryRecoveryUi _repositoryRecoveryUi;
    private readonly WorkspacePathGrantStore _workspacePathGrants;
    private readonly SnapshotPackageBroker _snapshotPackages;
    private readonly WorkspaceStorageBroker _workspaceStorage;
    private readonly IDatabasePicker _databasePicker;
    private readonly LazyProductTableGateway _tableGateway;
    private readonly TableWorkspaceService _workspace;
    private readonly GridStateCoordinator _coordinator;
    private readonly WorkspaceRequestDispatcher _dispatcher;
    private readonly PluginSurfaceSessionManager _pluginSurfaces;
    private readonly PluginWebViewResourceHost _pluginResources;
    private readonly PluginRequestDispatcher _pluginDispatcher;
    private readonly DailyQuoteHostClient _dailyQuotes;
    private readonly ILocalDocumentPreview _attachmentPreview;
    private readonly string _attachmentPreviewRoot;
    private readonly string _productDataRoot;
    private readonly string _activityRootBase;
    private readonly DashboardFeatureOptions _dashboardFeatures;
    private readonly AutoDateFeatureOptions _autoDateFeatures;
    private readonly MainWindowViewModel _viewModel;
    private readonly TestModeReadinessWriter? _readiness;
    private readonly string? _e2eControlsDir;
    private readonly AppPreferencesService _appPreferencesService;
    private readonly ReleaseUpdateCoordinator _releaseUpdateCoordinator;
    private readonly SemaphoreSlim _workspaceOpenGate = new(1, 1);
    private readonly CancellationTokenSource _session = new();

    private JsonRpcProductDataGateway? _productGateway;
    private JsonRpcPluginGateway? _pluginGateway;
    private WorkspaceDocumentOsAdapter? _documentWorkspace;
    private DatabaseOpenResult? _workspaceSnapshot;
    private ShellBootstrapResult? _shellBootstrapResult;
    private readonly Dictionary<Guid, WorkspaceDeletePlan> _workspaceDeletePlans = [];
    private AppPreferences _appPreferences = AppPreferences.Default;
    private TrayIconController? _trayIcon;
    private Timer? _testModeCloseControlTimer;
    private bool _explicitExitRequested;
    private int _closing;
    private bool _startHidden;

    /// <summary>
    /// True when this instance was launched via <c>--autostart</c> by a user
    /// who has enabled tray behavior, so <see cref="App"/> should hide the
    /// window into the tray right after showing it. The composition root
    /// derives this once from <see cref="HostStartupOptions"/> and the
    /// startup preferences; it never changes for a given instance.
    /// </summary>
    internal bool StartHidden => _startHidden;

    internal static IWorkspacePathPicker CreateWorkspacePathPicker(
        HostStartupOptions startup) =>
        startup.TestMode && !string.IsNullOrWhiteSpace(startup.E2eControlsDir)
            ? new TestModeWorkspacePathPicker(startup.E2eControlsDir)
            : new WindowsWorkspacePathPicker();

    public MainWindow()
    {
        InitializeComponent();

        HostStartupOptions startup = HostStartupOptions.Current();
        _e2eControlsDir = startup.TestMode
            && !string.IsNullOrWhiteSpace(startup.E2eControlsDir)
                ? Path.GetFullPath(startup.E2eControlsDir)
                : null;
        _readiness = startup.TestMode
            ? new TestModeReadinessWriter(startup.ReadinessDir)
            : null;
        _dashboardFeatures = DashboardFeatureOptions.FromEnvironment();
        _autoDateFeatures = AutoDateFeatureOptions.FromEnvironment();
        string? runtimeDataRoot = null;
        bool developmentDataRootRequested =
            !string.IsNullOrWhiteSpace(startup.DevelopmentDataRoot);
        if (startup.TestMode && developmentDataRootRequested)
        {
            throw new InvalidOperationException(
                "--dev-data-root cannot be combined with --test-mode.");
        }
        if (startup.TestMode && !string.IsNullOrWhiteSpace(startup.ReadinessDir))
        {
            runtimeDataRoot = Path.GetFullPath(
                Path.Combine(startup.ReadinessDir, "local-data"));
        }
        else if (developmentDataRootRequested)
        {
            runtimeDataRoot = Path.GetFullPath(startup.DevelopmentDataRoot!);
        }
        string localAppData = Environment.GetFolderPath(
            Environment.SpecialFolder.LocalApplicationData);
        _productDataRoot = runtimeDataRoot
            ?? Path.Combine(localAppData, "VibeTable", "shell");
        WindowsStartupRegistration? windowsStartupRegistration = null;
        _appPreferencesService = new AppPreferencesService(
            new JsonAppPreferencesStore(
                Path.Combine(_productDataRoot, "app-preferences.json")),
            startup.TestMode
                ? (IStartupRegistration)new InMemoryStartupRegistration()
                : (windowsStartupRegistration = WindowsStartupRegistration.ForCurrentProcess()));
        _appPreferences = _appPreferencesService.ReadForStartup();
        if (startup.TestModeTrayLifecycle)
        {
            // Persist inside the isolated test-mode data root so the renderer's
            // normal appPreferences.get refresh cannot undo the tray policy
            // before the black-box close request arrives.
            _appPreferences = _appPreferencesService.Update(
                new AppPreferencesPatch(true, null));
        }
        // Only an auto-started launch reconciles the startup value: a stale
        // pointer left after moving/reinstalling is cleaned so the next boot
        // re-points at the current process (or drops the value entirely).
        // ReconcileForCurrentProcess swallows registry failures itself.
        if (startup.AutoStart && windowsStartupRegistration is not null)
        {
            windowsStartupRegistration.ReconcileForCurrentProcess();
        }
        _releaseUpdateCoordinator = new ReleaseUpdateCoordinator(
            AppContext.BaseDirectory,
            ApplicationVersion.FromAssembly(typeof(MainWindow).Assembly),
            installationEnabled: !developmentDataRootRequested && !startup.TestMode);
        _activityRootBase = runtimeDataRoot is null
            ? Path.Combine(localAppData, "VibeTable", "activity")
            : Path.Combine(runtimeDataRoot, "activity");
        _attachmentPreviewRoot = Path.Combine(
            _productDataRoot,
            "attachment-preview",
            $"p{Environment.ProcessId}-{Guid.NewGuid():N}");
        Directory.CreateDirectory(_attachmentPreviewRoot);
        _attachmentPreview = new ShellDocumentPreview();

        _pluginSurfaces = new PluginSurfaceSessionManager();
        _pluginResources = new PluginWebViewResourceHost(
            new PluginResourceHost(),
            _pluginSurfaces);
        _router = new WebMessageRouter(OnRoutedWebRequest);
        _webBridge = new ProductWebViewBridge(
            this,
            AppWebView,
            _router,
            _pluginResources,
            _readiness,
            OnRendererProcessFailed,
            runtimeDataRoot is not null
                ? Path.Combine(runtimeDataRoot, "webview2-user-data")
                : null,
            stableIsolatedUserDataRoot: runtimeDataRoot is not null);
        _workspaceRegistry = new WorkspaceRegistry(
            startup.TestMode ? runtimeDataRoot : null);
        IReadOnlyList<WorkspaceRegistryEntryV2> knownWorkspaces;
        try
        {
            knownWorkspaces = _workspaceRegistry.List();
        }
        catch (WorkspaceRegistryException)
        {
            knownWorkspaces = [];
        }
        Func<PocketBaseLaunchOptions> sidecarOptionsFactory =
            () => PocketBaseHostOptions.Resolve(
                AppContext.BaseDirectory,
                Environment.GetFolderPath(
                    Environment.SpecialFolder.LocalApplicationData));
        _runtime = new ProductionWorkspaceRuntimeFactory(
            sidecarOptionsFactory,
            () => BackendLaunchOptions.ResolveForHost(),
            knownWorkspaces);
        _repositoryOnboarding = new WorkspaceRepositoryOnboardingService(
            sidecarOptionsFactory,
            _runtime.PrepareRepositoryOnboarding);
        _replicaRecovery = new WorkspaceReplicaRecoveryService(
            sidecarOptionsFactory,
            _runtime.PrepareRepositoryOnboarding);
        _repositoryRecoveryUi = new WorkspaceRepositoryRecoveryUi();
        _providerPolicy = WorkspaceProviderPolicy.Load(AppContext.BaseDirectory);
        _workspacePathGrants = new WorkspacePathGrantStore(
            CreateWorkspacePathPicker(startup));
        _shellBootstrap = new ShellBootstrap(_workspaceRegistry, _webBridge);
        WorkspaceSessionEnvelopeFilter? productionEnvelopeFilter = null;
        _workspaceSessions = new WorkspaceSessionManager(
            _workspaceRegistry,
            _runtime,
            new SidecarWorkspaceProtectionHook(
                _runtime,
                (workspaceId, sessionEpoch) =>
                    productionEnvelopeFilter?.ReserveHostSequence(
                        workspaceId,
                        sessionEpoch)
                    ?? throw new InvalidOperationException(
                        "Workspace request envelope filter is not bound.")),
            new WorkspaceCoordinationLeaseHook(),
            new WorkspaceReplicaPreOpenHook(
                _replicaRecovery,
                _repositoryOnboarding,
                _repositoryRecoveryUi));
        _workspaceSessionFilter = new WorkspaceSessionEnvelopeFilter(
            _workspaceSessions);
        productionEnvelopeFilter = _workspaceSessionFilter;
        _workspaceSessions.SetRequestDrainHook(_workspaceSessionFilter);
        _replicaStatusMonitor = new WorkspaceReplicaStatusMonitor(
            RefreshReplicaStatusAsync);
        _snapshotPackages = new SnapshotPackageBroker(
            sidecarOptionsFactory,
            () => BackendLaunchOptions.ResolveForHost(),
            _providerPolicy,
            _workspaceRegistry,
            _workspaceSessions,
            _productDataRoot);
        _workspaceStorage = new WorkspaceStorageBroker(
            _workspaceRegistry,
            _workspaceSessions,
            _providerPolicy,
            _productDataRoot,
            replicas: _replicaRecovery);
        _databasePicker = new SessionProductSourcePicker(_workspaceSessions);

        IPluginPackageSourcePicker pluginPackagePicker =
            _e2eControlsDir is null
                ? new WindowsPluginPackageSourcePicker()
                : new TestModePluginPackageSourcePicker(_e2eControlsDir);
        _pluginDispatcher = new PluginRequestDispatcher(
            _webBridge,
            _pluginSurfaces,
            pluginPackagePicker,
            _pluginResources,
            new WindowsPluginFilePicker(),
            new GitHubPluginPackageSource(
                Path.Combine(_productDataRoot, "plugin-downloads"),
                () => _appPreferencesService.Read()));
        _dailyQuotes = new DailyQuoteHostClient();
        _tableGateway = new LazyProductTableGateway();
        _workspace = new TableWorkspaceService(_tableGateway);
        _workspace.Notification += OnWorkspaceNotification;
        _coordinator = new GridStateCoordinator(
            _tableGateway,
            OnWorkspaceNotification);
        _dispatcher = new WorkspaceRequestDispatcher(
            _workspace,
            _databasePicker,
            _webBridge,
            _coordinator,
            _dashboardFeatures,
            autoDateFeatures: _autoDateFeatures,
            sessionEnvelopeFilter: _workspaceSessionFilter);

        _runtime.ClientReady += OnRuntimeClientReady;
        _runtime.RecoveryFailed += OnRuntimeRecoveryFailed;
        _runtime.BindingChanged += OnRuntimeBindingChanged;
        _workspaceSessions.Changed += OnWorkspaceSessionChanged;
        _viewModel = new MainWindowViewModel(_runtime, _webBridge);
        _viewModel.PropertyChanged += OnViewModelPropertyChanged;
        DataContext = _viewModel;

        Loaded += OnLoaded;
        Closing += OnClosing;
        Closed += OnClosed;
        Application.Current.SessionEnding += OnSessionEnding;
        ApplyAppPreferences(_appPreferences);
        // UI-driven E2E stays visible. The explicit tray-lifecycle submode is
        // allowed to exercise the production auto-start visibility policy;
        // its harness observes fixed host state files instead of clicking UI.
        _startHidden = (!startup.TestMode || startup.TestModeTrayLifecycle)
            && StartupVisibilityPolicy.ShouldStartHidden(
                startup.AutoStart,
                _appPreferences.MinimizeToTrayOnClose);
        if (_e2eControlsDir is not null)
        {
            _testModeCloseControlTimer = new Timer(
                _ => CheckTestModeHostControls(),
                null,
                TimeSpan.FromMilliseconds(100),
                TimeSpan.FromMilliseconds(100));
        }
    }

    private async void OnLoaded(object sender, RoutedEventArgs args)
    {
        try
        {
            _readiness?.Trace("MainWindow: starting global shell");
            _shellBootstrapResult = await _shellBootstrap.StartAsync();
            _viewModel.MarkShellLoaded();
            _readiness?.Trace("MainWindow: loading shell renderer");
            await _viewModel.StartAsync();
            if (_viewModel.State == StartupState.Faulted)
            {
                Exception? error = _viewModel.LastStartupError;
                _webBridge.PostNotification(
                    "host.startupStateChanged",
                    new
                    {
                        phase = "faulted",
                        stage = "runtime",
                        detail = "全局界面加载失败，可重试。",
                        canRetry = true,
                        canCancel = false,
                    });
                _readiness?.WriteError(
                    error is null
                        ? "Product runtime startup failed."
                        : $"Product runtime startup failed: {error.GetType().Name}: {error.Message}");
            }
        }
        catch (Exception exception)
        {
            _readiness?.WriteError(
                $"Product runtime startup failed: {exception.GetType().Name}");
        }
    }

    private void OnRuntimeClientReady()
    {
        if (Volatile.Read(ref _closing) != 0) return;
        Dispatcher.Invoke(ConfigureRpcGateways);
    }

    private void ConfigureRpcGateways()
    {
        PythonBackendSupervisor backend = _runtime.CurrentBackend
            ?? throw new InvalidOperationException(
                "No workspace runtime is bound.");
        _tableGateway.Bind(backend);
        JsonRpcClient client = backend.Client
            ?? throw new InvalidOperationException(
                "The product RPC client is not ready.");

        if (_productGateway is not null)
        {
            _dispatcher.ClearProductDataGateway(_productGateway);
            _productGateway.DataChanged -= OnProductDataChanged;
            _productGateway.TaskChanged -= OnProductTaskChanged;
            _productGateway.Dispose();
        }
        _productGateway = new JsonRpcProductDataGateway(client);
        _productGateway.DataChanged += OnProductDataChanged;
        _productGateway.TaskChanged += OnProductTaskChanged;
        _dispatcher.SetProductDataGateway(_productGateway);

        _dispatcher.SetDashboardGateway(
            new JsonRpcDashboardGateway(client),
            _session.Token);

        _pluginGateway?.Dispose();
        _pluginGateway = new JsonRpcPluginGateway(client);
        _pluginDispatcher.SetGateway(_pluginGateway);

        _documentWorkspace?.Dispose();
        _documentWorkspace = new WorkspaceDocumentOsAdapter(
            CurrentWorkspaceDocumentBinding,
            new DocumentCapabilityStore(),
            new WindowsLocalDocumentActions(),
            new ShellDocumentPreview(),
            _e2eControlsDir is null
                ? new WindowsLocalDocumentFilePicker()
                : new TestModeLocalDocumentFilePicker(_e2eControlsDir),
            _workspaceSessionFilter,
            new OpenXmlDocumentDiffEngine(),
            Path.Combine(_productDataRoot, "document-diff"));
        _dispatcher.SetDocumentWorkspace(_documentWorkspace);

        _workspaceSnapshot = null;
        if (_router.IsReady)
        {
            PostRuntimeReady();
            OpenProductWorkspaceWhenReady();
        }
    }

    private WorkspaceDocumentBinding? CurrentWorkspaceDocumentBinding()
    {
        WorkspaceRegistryEntryV2? workspace = _runtime.CurrentWorkspace;
        WorkspaceV2HttpGateway? gateway = _runtime.CurrentV2Gateway;
        WorkspaceV2SidecarCapabilities? capabilities =
            _runtime.CurrentCapabilities;
        WorkspaceSessionV2 session = _workspaceSessions.Current;
        if (workspace is null || gateway is null || capabilities is null ||
            session.WorkspaceId != workspace.WorkspaceId ||
            session.SessionEpoch == 0)
            return null;
        return new WorkspaceDocumentBinding(
            workspace.WorkspaceId,
            session.SessionEpoch,
            session.Writable,
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace),
            gateway,
            capabilities.RpcMethods);
    }

    private void PostRuntimeReady()
    {
        _webBridge.PostNotification(
            "host.startupStateChanged",
            new
            {
                phase = "ready",
                stage = "runtime",
                detail = "本地数据服务已就绪。",
                canRetry = false,
                canCancel = false,
            });
    }

    private void OnRuntimeRecoveryFailed(Exception exception)
    {
        _readiness?.WriteError(
            $"Local data recovery failed: {exception.GetType().Name}");
        Dispatcher.BeginInvoke(() =>
        {
            _webBridge.PostNotification(
                "host.startupStateChanged",
                new
                {
                    phase = "faulted",
                    stage = "runtime",
                    detail = "本地数据服务恢复失败，可重试或切换工作区。",
                    canRetry = true,
                    canCancel = false,
                });
            if (_viewModel.State is StartupState.LoadingWeb or StartupState.Ready)
            {
                _viewModel.MoveToFaulted("Local data recovery failed.");
            }
        });
    }

    private void OnRuntimeBindingChanged()
    {
        if (Volatile.Read(ref _closing) != 0)
            return;
        Dispatcher.BeginInvoke(() =>
        {
            PythonBackendSupervisor? backend = _runtime.CurrentBackend;
            if (backend is not null)
            {
                _tableGateway.Bind(backend);
                return;
            }
            _tableGateway.Unbind();
            if (_productGateway is not null)
            {
                _dispatcher.ClearProductDataGateway(_productGateway);
                _productGateway.DataChanged -= OnProductDataChanged;
                _productGateway.TaskChanged -= OnProductTaskChanged;
                _productGateway.Dispose();
                _productGateway = null;
            }
            _pluginGateway?.Dispose();
            _pluginGateway = null;
            _documentWorkspace?.Dispose();
            _documentWorkspace = null;
            _workspaceSnapshot = null;
        });
    }

    private void OnRendererProcessFailed(string reason)
    {
        _viewModel.MarkShellUnavailable();
        if (_viewModel.State is StartupState.LoadingWeb or StartupState.Ready)
        {
            _viewModel.MoveToFaulted(reason);
        }
    }

    private void OnRoutedWebRequest(RoutedWebRequest request)
    {
        if (request.Type.StartsWith("file.", StringComparison.Ordinal))
        {
            _readiness?.Trace(
                $"Attachment request routed; type={request.Type}; " +
                $"requestIdPresent={!string.IsNullOrWhiteSpace(request.RequestId)}");
        }
        if (request.Type == "app.ready")
        {
            _router.IsReady = true;
            _dispatcher.RotateDocumentCapabilityEpoch();
            _pluginResources.CloseAllSurfaces();
            PostWorkspaceV2Bootstrap();
            if (_runtime.CurrentBackend?.State == BackendState.Ready
                && _productGateway is not null)
            {
                PostRuntimeReady();
                OpenProductWorkspaceWhenReady();
            }
            else
            {
                _webBridge.PostNotification(
                    "host.startupStateChanged",
                    new
                    {
                        phase = "starting",
                        stage = "shell",
                        detail = _shellBootstrapResult?.RegistryErrorCode is null
                            ? "工作区中心已就绪，请选择或创建工作区。"
                            : "工作区列表需要修复。",
                        canRetry = false,
                        canCancel = false,
                    });
            }
            TryWriteReadiness();
            return;
        }
        if (request.Type == "workspace.v2.request")
        {
            _ = HandleWorkspaceV2RequestAsync(request);
            return;
        }
        if (request.Type == "host.startupCancelRequested")
        {
            _ = Dispatcher.BeginInvoke(RequestExit);
            return;
        }
        if (request.Type == "host.startupRetryRequested")
        {
            Dispatcher.BeginInvoke(() =>
            {
                if (_viewModel.RetryCommand.CanExecute(null))
                {
                    _viewModel.RetryCommand.Execute(null);
                }
            });
            return;
        }
        if (request.Type == "admin.openRequested")
        {
            if (!OpenPocketBaseAdmin())
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "当前版本没有可安全打开的本地管理页面。",
                    "ADMIN_UNAVAILABLE");
            }
            return;
        }
        if (request.Type == "diagnostics.get")
        {
            if (!HasEmptyObjectPayload(request))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "The diagnostics request is invalid.",
                    "DIAGNOSTICS_BAD_PAYLOAD");
                return;
            }
            using Process process = Process.GetCurrentProcess();
            _webBridge.PostResponse(
                request.Type,
                request.RequestId,
                new
                {
                    currentDirectory = Environment.CurrentDirectory,
                    programDirectory = AppContext.BaseDirectory,
                    dataDirectory = _productDataRoot,
                    operatingSystem = RuntimeInformation.OSDescription,
                    programVersion = ApplicationVersion.FromAssembly(
                        typeof(MainWindow).Assembly),
                    dotnetVersion = Environment.Version.ToString(),
                    pocketBaseVersion =
                        _runtime.CurrentPocketBaseVersion ?? "not-started",
                    memoryBytes = process.WorkingSet64,
                    dataServiceState =
                        _runtime.CurrentSidecar?.GetStatus().State.ToString()
                        ?? "Stopped",
                });
            return;
        }
        if (request.Type == "appPreferences.get")
        {
            if (!HasEmptyObjectPayload(request))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "The application preferences request is invalid.",
                    "APP_PREFERENCES_BAD_PAYLOAD");
                return;
            }
            try
            {
                _appPreferences = _appPreferencesService.Read();
                ApplyAppPreferences(_appPreferences);
                PostAppPreferences(request);
            }
            catch (Exception)
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "无法读取桌面应用设置。",
                    "APP_PREFERENCES_READ_FAILED");
            }
            return;
        }
        if (request.Type == "appPreferences.update")
        {
            if (!TryReadAppPreferencesPatch(
                    request.Payload,
                    out AppPreferencesPatch? patch)
                || patch is null)
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "The application preferences update is invalid.",
                    "APP_PREFERENCES_BAD_PAYLOAD");
                return;
            }
            try
            {
                if (patch.MinimizeToTrayOnClose is true)
                {
                    EnsureTrayIcon();
                }
                _appPreferences = _appPreferencesService.Update(patch);
                ApplyAppPreferences(_appPreferences);
                PostAppPreferences(request);
            }
            catch (Exception)
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "无法保存桌面应用设置，请检查当前用户权限后重试。",
                    "APP_PREFERENCES_WRITE_FAILED");
            }
            return;
        }
        if (request.Type == "update.check")
        {
            if (!HasEmptyObjectPayload(request))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "The update check request is invalid.",
                    "UPDATE_BAD_PAYLOAD");
                return;
            }
            _ = CheckForReleaseUpdateAsync(request);
            return;
        }
        if (request.Type == "update.install")
        {
            if (!HasEmptyObjectPayload(request))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "The update install request is invalid.",
                    "UPDATE_BAD_PAYLOAD");
                return;
            }
            _ = InstallReleaseUpdateAsync(request);
            return;
        }
        if (request.Type == "dailyQuote.fetch")
        {
            _ = FetchDailyQuoteAsync(request);
            return;
        }
        if (request.Type == "document.importRequested")
        {
            _ = ImportDocumentsFromPickerAsync(request.Payload);
            return;
        }
        if (request.Type == "data.importSourceRequested")
        {
            PickImportSource(request);
            return;
        }
        if (request.Type == "data.exportTargetRequested")
        {
            PickExportTarget(request);
            return;
        }
        if (request.Type == "document.externalDropRequested")
        {
            IReadOnlyList<string> paths =
                _webBridge.CurrentNativeFilePaths?.ToArray() ?? [];
            if (paths.Count == 0)
            {
                PostDocumentFailure(
                    "拖入请求没有携带有效的原生文件对象。",
                    "DOCUMENT_DROP_OBJECTS_MISSING");
                return;
            }
            _ = ImportDocumentsFromHostPathsAsync(request.Payload, paths);
            return;
        }
        if (request.Type == "document.relinkRequested")
        {
            _ = RelinkDocumentAsync(request.Payload);
            return;
        }
        if (request.Type == "document.dragOutRequested")
        {
            DragDocumentOut(request.Payload);
            return;
        }
        if (request.Type == "file.uploadRequested")
        {
            bool pickerWasShown = false;
            IReadOnlyList<string> paths =
                _webBridge.CurrentNativeFilePaths?.ToArray() ?? [];
            if (paths.Count == 0 && _e2eControlsDir is not null)
            {
                paths =
                [
                    ReadE2eControlPath(
                        "attachment-source.txt",
                        requireExistingFile: true)!
                ];
            }
            if (paths.Count == 0 && _e2eControlsDir is null)
            {
                pickerWasShown = true;
                paths = PickAttachmentPaths(multiselect: true);
            }
            if (paths.Count == 0)
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    pickerWasShown ? "已取消选择附件。" : "上传请求没有携带有效的原生文件对象。",
                    pickerWasShown ? "CANCELLED" : "ATTACHMENT_UPLOAD_OBJECTS_MISSING");
                return;
            }
            _readiness?.Trace(
                $"Attachment upload request accepted; files={paths.Count}; " +
                $"requestIdPresent={!string.IsNullOrWhiteSpace(request.RequestId)}");
            _ = ApplyAttachmentChangeAsync(
                request,
                paths,
                Array.Empty<string>());
            return;
        }
        if (request.Type == "file.replaceRequested")
        {
            bool pickerWasShown = false;
            IReadOnlyList<string> paths =
                _webBridge.CurrentNativeFilePaths?.ToArray() ?? [];
            if (paths.Count == 0 && _e2eControlsDir is not null)
            {
                paths =
                [
                    ReadE2eControlPath(
                        "attachment-replacement-source.txt",
                        requireExistingFile: true)!
                ];
            }
            if (paths.Count == 0 && _e2eControlsDir is null)
            {
                pickerWasShown = true;
                paths = PickAttachmentPaths(multiselect: false);
            }
            string? storedName = ReadString(request.Payload, "storedName");
            if (paths.Count != 1 || string.IsNullOrWhiteSpace(storedName))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    pickerWasShown && paths.Count == 0
                        ? "已取消选择替换文件。"
                        : "附件替换必须携带一个原生文件和已有附件标识。",
                    pickerWasShown && paths.Count == 0
                        ? "CANCELLED"
                        : "ATTACHMENT_REPLACE_INVALID");
                return;
            }
            _ = ApplyAttachmentChangeAsync(
                request,
                paths,
                new[] { storedName });
            return;
        }
        if (request.Type == "file.removeRequested")
        {
            string? storedName = ReadString(request.Payload, "storedName");
            if (string.IsNullOrWhiteSpace(storedName))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "缺少托管附件标识。",
                    "BAD_PAYLOAD");
                return;
            }
            _ = ApplyAttachmentChangeAsync(
                request,
                Array.Empty<string>(),
                new[] { storedName });
            return;
        }
        if (request.Type == "file.previewRequested")
        {
            _ = PreviewAttachmentAsync(request);
            return;
        }
        if (request.Type == "file.downloadRequested")
        {
            SaveAttachment(request);
            return;
        }
        if (request.Type.StartsWith("plugin.", StringComparison.Ordinal))
        {
            _pluginDispatcher.Dispatch(request);
            return;
        }
        _dispatcher.Dispatch(request);
    }

    private async Task HandleWorkspaceV2RequestAsync(RoutedWebRequest request)
    {
        WorkspaceRequestEpochLease? epochLease = null;
        try
        {
            bool lifecycleRequest = request.V2Method is
                "workspace.switch" or "workspace.close";
            bool admitted = request.Scope is null ||
                (lifecycleRequest
                    ? _workspaceSessionFilter.TryAdmitLifecycleRequest(
                        request.Scope)
                    : _workspaceSessionFilter.TryCapture(
                        request.Scope,
                        out epochLease));
            if (!admitted)
            {
                throw new WorkspaceRegistryException(
                    "workspace.session_stale",
                    "The workspace request does not belong to the active session.");
            }
            using CancellationTokenSource? requestCancellation =
                epochLease is null
                    ? null
                    : CancellationTokenSource.CreateLinkedTokenSource(
                        _session.Token,
                        epochLease.CancellationToken);
            CancellationToken requestToken =
                requestCancellation?.Token ?? _session.Token;
            JsonElement parameters = request.Payload.TryGetProperty(
                "params", out JsonElement value)
                    ? value
                    : default;
            Guid operationId = ReadOperationId(request.Wire);
            object result;
            switch (request.V2Method)
            {
                case "workspace.list":
                    result = new
                    {
                        workspaces = _workspaceRegistry.List()
                            .Select(ToWorkspaceProjection)
                            .ToArray(),
                    };
                    break;
                case "workspace.create":
                    result = await CreateWorkspaceAsync(
                        parameters,
                        operationId,
                        requestToken);
                    break;
                case "workspace.register":
                    result = await RegisterWorkspaceAsync(
                        parameters,
                        operationId,
                        requestToken);
                    break;
                case "workspace.relink":
                    result = await RelinkWorkspaceAsync(
                        parameters,
                        operationId,
                        requestToken);
                    break;
                case "workspace.open":
                    result = ToSessionResult(
                        await OpenWorkspaceWithRecoveryAsync(
                            ReadRequiredGuid(parameters, "workspaceId"),
                            ReadOpenMode(parameters),
                            switching: false,
                            requestToken));
                    break;
                case "workspace.switch":
                    result = ToSessionResult(
                        await OpenWorkspaceWithRecoveryAsync(
                            ReadRequiredGuid(parameters, "targetWorkspaceId"),
                            ReadOpenMode(parameters),
                            switching: true,
                            requestToken));
                    break;
                case "workspace.close":
                    result = ToSessionResult(
                        await _workspaceSessions.CloseAsync(
                            ReadString(parameters, "reason") ?? "user",
                            requestToken));
                    break;
                case "workspace.remove":
                    result = RemoveWorkspace(parameters);
                    break;
                case "workspace.planDelete":
                    result = PlanWorkspaceDelete(parameters);
                    break;
                case "workspace.applyDelete":
                    result = ApplyWorkspaceDelete(parameters);
                    break;
                case "snapshot.inspectPackage":
                {
                    JsonElement packageParameters =
                        _workspacePathGrants.MaterializeSentinels(
                            request.V2Method!,
                            operationId,
                            parameters);
                    WorkspaceSidecarPathGrant sourceGrant =
                        _workspacePathGrants.ConsumeForSidecar(
                            packageParameters,
                            request.V2Method!,
                            operationId)
                        ?? throw new WorkspacePathGrantException(
                            "workspace.path_grant_invalid",
                            "Snapshot package inspection requires a native source grant.");
                    result = await _snapshotPackages.InspectAsync(
                        request.RequestId ?? operationId.ToString("D"),
                        request.Wire,
                        packageParameters,
                        sourceGrant,
                        requestToken);
                    break;
                }
                case "snapshot.import":
                    result = await _snapshotPackages.ImportAsync(
                        request.RequestId ?? operationId.ToString("D"),
                        request.Wire,
                        parameters,
                        requestToken);
                    break;
                case "snapshot.openAsNewWorkspace":
                {
                    WorkspaceV2HttpGateway sourceGateway =
                        _runtime.CurrentV2Gateway
                        ?? throw new WorkspaceRegistryException(
                            "workspace.session_required",
                            "Opening a snapshot as a new workspace requires an active source workspace.");
                    if (_runtime.CurrentCapabilities?.RpcMethods.Contains(
                            "snapshot.export",
                            StringComparer.Ordinal) != true)
                    {
                        throw new WorkspaceRegistryException(
                            "workspace.capability_unavailable",
                            "The source runtime cannot export this snapshot.");
                    }
                    SnapshotOpenAsNewPlan plan =
                        await _snapshotPackages.StageOpenAsNewAsync(
                            sourceGateway,
                            request.RequestId ?? operationId.ToString("D"),
                            request.Wire,
                            parameters,
                            requestToken);
                    // The package is now staged independently of the source
                    // epoch. Release this request before switching sessions so
                    // the source drain cannot wait on its own broker request.
                    epochLease?.Dispose();
                    epochLease = null;
                    result = ToSessionResult(
                        await _snapshotPackages.CompleteOpenAsNewAsync(
                            plan,
                            request.RequestId ?? operationId.ToString("D"),
                            _session.Token));
                    break;
                }
                case "workspace.storage.preview":
                {
                    string? action = ReadString(parameters, "action");
                    string? selectedRoot = null;
                    JsonElement storageParameters = parameters;
                    if (action is "relocate" or "convertTopology")
                    {
                        storageParameters =
                            _workspacePathGrants.MaterializeSentinels(
                                request.V2Method!,
                                operationId,
                                parameters);
                        string grantId =
                            ReadString(
                                storageParameters,
                                "selectedRootGrant")
                            ?? throw new WorkspacePathGrantException(
                                "workspace.path_grant_invalid",
                                "Storage relocation requires a native target grant.");
                        selectedRoot = _workspacePathGrants.Consume(
                            grantId,
                            request.V2Method!,
                            operationId,
                            "workspace-root");
                    }
                    result = await _workspaceStorage.PreviewAsync(
                        storageParameters,
                        selectedRoot,
                        requestToken);
                    break;
                }
                case "workspace.storage.apply":
                    result = await _workspaceStorage.ApplyAsync(
                        parameters,
                        requestToken);
                    _ = Dispatcher.BeginInvoke(PostWorkspaceV2Bootstrap);
                    break;
                default:
                    if (IsWorkspaceMutation(request.V2Method!)
                        && !_workspaceSessions.Current.Writable)
                    {
                        throw new WorkspaceRegistryException(
                            "workspace.read_only",
                            "This workspace session is read-only.");
                    }
                    WorkspaceV2HttpGateway gateway =
                        _runtime.CurrentV2Gateway
                        ?? throw new WorkspaceRegistryException(
                            "workspace.session_required",
                            "This operation requires an active workspace session.");
                    if (_runtime.CurrentCapabilities?.RpcMethods.Contains(
                            request.V2Method!,
                            StringComparer.Ordinal) != true)
                    {
                        throw new WorkspaceRegistryException(
                            "workspace.capability_unavailable",
                            "This workspace v2 capability is not connected in this build.");
                    }
                    JsonElement materialized =
                        _workspacePathGrants.MaterializeSentinels(
                            request.V2Method!,
                            operationId,
                            parameters);
                    WorkspaceSidecarPathGrant? sidecarPathGrant =
                        _workspacePathGrants.ConsumeForSidecar(
                            materialized,
                            request.V2Method!,
                            operationId);
                    WorkspaceV2ForwardResult forwarded =
                        await gateway.ForwardAsync(
                            request.RequestId ?? operationId.ToString("D"),
                            request.V2Method!,
                            request.Wire,
                            materialized,
                            sidecarPathGrant,
                            requestToken);
                    if (epochLease is not null &&
                        !_workspaceSessionFilter.IsCurrent(epochLease))
                        return;
                    if (forwarded.Error is not null)
                    {
                        _webBridge.PostWorkspaceV2Response(
                            request.RequestId,
                            new
                            {
                                method = request.V2Method,
                                wire = forwarded.Wire,
                                ok = false,
                                result = (object?)null,
                                error = new
                                {
                                    code = forwarded.Error.Code,
                                    message = forwarded.Error.Message,
                                    retryable = forwarded.Error.Retryable,
                                },
                            },
                            forwarded.Wire);
                        return;
                    }
                    _webBridge.PostWorkspaceV2Response(
                        request.RequestId,
                        new
                        {
                            method = request.V2Method,
                            wire = forwarded.Wire,
                            ok = true,
                            result = forwarded.Result,
                            error = (object?)null,
                        },
                        forwarded.Wire);
                    if (request.V2Method == "replica.forceTakeover" &&
                        request.Scope is not null)
                    {
                        await _replicaStatusMonitor.RefreshNowAsync(
                            request.Scope.WorkspaceId,
                            request.Scope.SessionEpoch,
                            requestToken);
                    }
                    if (request.V2Method == "snapshot.applyRestore"
                        && IsPreparedRestoreResult(forwarded.Result)
                        && request.Scope is not null)
                    {
                        // The prepared response must reach the old epoch
                        // before it is rotated. Go has suspended the workspace
                        // and requested shutdown; Desktop owns the explicit
                        // stop/reopen/verify/bootstrap sequence.
                        epochLease?.Dispose();
                        epochLease = null;
                        try
                        {
                            _ = await _workspaceSessions
                                .RestartAfterRestoreAsync(
                                    request.Scope.WorkspaceId,
                                    request.Scope.SessionEpoch,
                                    _session.Token);
                            PostWorkspaceV2Bootstrap();
                        }
                        catch (Exception restartError)
                        {
                            _readiness?.WriteError(
                                $"Restored workspace restart failed: {restartError.GetType().Name}");
                            PostWorkspaceV2Bootstrap();
                        }
                    }
                    else if (request.V2Method
                                 == "repository.applyKeyRotation"
                             && IsKeyRotationRestartResult(
                                 forwarded.Result)
                             && request.Scope is not null)
                    {
                        epochLease?.Dispose();
                        epochLease = null;
                        try
                        {
                            _ = await _workspaceSessions
                                .RestartAfterHostMaintenanceAsync(
                                    request.Scope.WorkspaceId,
                                    request.Scope.SessionEpoch,
                                    _session.Token);
                            PostWorkspaceV2Bootstrap();
                        }
                        catch (Exception restartError)
                        {
                            _readiness?.WriteError(
                                $"Repository key rotation restart failed: {restartError.GetType().Name}");
                            PostWorkspaceV2Bootstrap();
                        }
                    }
                    return;
            }
            _webBridge.PostWorkspaceV2Response(
                request.RequestId,
                new
                {
                    method = request.V2Method,
                    wire = request.Wire,
                    ok = true,
                    result,
                    error = (object?)null,
                },
                request.Wire);
        }
        catch (OperationCanceledException)
            when (epochLease?.CancellationToken.IsCancellationRequested == true)
        {
            // Draining invalidated this epoch. Never post a late response into
            // a new workspace session.
        }
        catch (Exception exception)
        {
            string code = exception is WorkspaceRegistryException registry
                ? registry.Code
                : exception is WorkspacePathGrantException grant
                    ? grant.Code
                : "workspace.operation_failed";
            _webBridge.PostWorkspaceV2Response(
                request.RequestId,
                new
                {
                    method = request.V2Method,
                    wire = request.Wire,
                    ok = false,
                    result = (object?)null,
                    error = new
                    {
                        code,
                        message = exception.Message,
                        retryable = false,
                    },
                },
                request.Wire);
        }
        finally
        {
            epochLease?.Dispose();
        }
    }

    private async Task<WorkspaceSessionV2> OpenWorkspaceWithRecoveryAsync(
        Guid workspaceId,
        WorkspaceOpenMode mode,
        bool switching,
        CancellationToken cancellationToken)
    {
        try
        {
            return switching
                ? await _workspaceSessions.SwitchAsync(
                    workspaceId,
                    mode,
                    cancellationToken)
                : await _workspaceSessions.OpenAsync(
                    workspaceId,
                    mode,
                    cancellationToken);
        }
        catch (Exception exception) when (RequiresRepositoryRecovery(exception))
        {
            WorkspaceRegistryEntryV2 workspace = _workspaceRegistry.List()
                .Single(entry => entry.WorkspaceId == workspaceId);
            string? recoveryKey = _repositoryRecoveryUi.PromptRecoveryKey(
                workspace.DisplayName);
            if (string.IsNullOrWhiteSpace(recoveryKey))
                throw new WorkspaceRegistryException(
                    "repository.recovery_cancelled",
                    "Workspace recovery was cancelled.");
            await _repositoryOnboarding.UnlockAsync(
                workspace,
                recoveryKey,
                cancellationToken);
            return switching
                ? await _workspaceSessions.SwitchAsync(
                    workspaceId,
                    mode,
                    cancellationToken)
                : await _workspaceSessions.OpenAsync(
                    workspaceId,
                    mode,
                    cancellationToken);
        }
    }

    private static bool RequiresRepositoryRecovery(Exception exception)
    {
        for (Exception? current = exception;
             current is not null;
             current = current.InnerException)
        {
            if (current is WorkspaceRegistryException registry &&
                registry.Code == "repository.key_missing")
                return true;
            if (current.Message.Contains(
                    "repository.key_missing",
                    StringComparison.Ordinal))
                return true;
            if (current is AggregateException aggregate &&
                aggregate.InnerExceptions.Any(RequiresRepositoryRecovery))
                return true;
        }
        return false;
    }

    private static bool IsPreparedRestoreResult(JsonElement? result)
        => result is JsonElement value
            && value.ValueKind == JsonValueKind.Object
            && value.TryGetProperty("state", out JsonElement state)
            && state.ValueKind == JsonValueKind.String
            && state.GetString() == "prepared";

    private static bool IsKeyRotationRestartResult(JsonElement? result)
        => result is JsonElement value
            && value.ValueKind == JsonValueKind.Object
            && value.EnumerateObject().Count() == 3
            && value.TryGetProperty("operationId", out JsonElement operation)
            && operation.ValueKind == JsonValueKind.String
            && Guid.TryParse(operation.GetString(), out Guid operationId)
            && operationId != Guid.Empty
            && value.TryGetProperty("state", out JsonElement state)
            && state.ValueKind == JsonValueKind.String
            && state.GetString() == "hostRestartRequired"
            && value.TryGetProperty(
                "newRecoveryKeyAvailable",
                out JsonElement available)
            && available.ValueKind == JsonValueKind.False;

    private async Task<object> CreateWorkspaceAsync(
        JsonElement parameters,
        Guid operationId,
        CancellationToken cancellationToken)
    {
        Guid workspaceId = Guid.NewGuid();
        string selectedRoot = ResolveWorkspaceCreateRoot(
            parameters,
            operationId,
            _workspacePathGrants,
            _productDataRoot,
            workspaceId);
        bool userMarkedSync = ReadRequiredBoolean(
            parameters,
            "userMarkedSync");
        string displayName = ReadString(parameters, "displayName")?.Trim()
            ?? string.Empty;
        if (displayName.Length is < 1 or > 120)
            throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Workspace display name is invalid.");
        WorkspaceStorageMode storageMode =
            ReadString(parameters, "storageMode") switch
            {
                "direct" => WorkspaceStorageMode.Direct,
                "mirrored" => WorkspaceStorageMode.Mirrored,
                _ => throw new WorkspaceRegistryException(
                    "workspace.request_invalid",
                    "Workspace storage mode is invalid."),
            };
        WorkspaceStorageObservation selectedStorage =
            ProbeCreateTarget(
                _providerPolicy,
                selectedRoot,
                storageMode,
                userMarkedSync);
        WorkspaceEncryptionMode encryptionMode =
            ReadString(parameters, "encryptionMode") switch
            {
                "none" => WorkspaceEncryptionMode.None,
                "convenient" => WorkspaceEncryptionMode.Convenient,
                "protected" => WorkspaceEncryptionMode.Protected,
                _ => throw new WorkspaceRegistryException(
                    "workspace.request_invalid",
                    "Workspace encryption mode is invalid."),
            };
        string? activityRoot = storageMode == WorkspaceStorageMode.Mirrored
            ? ManagedActivityRoot(_activityRootBase, workspaceId)
            : null;
        if (activityRoot is not null)
            _ = ProbeCreateTarget(
                _providerPolicy,
                activityRoot,
                WorkspaceStorageMode.Direct);
        WorkspaceLayoutResult layout = WorkspaceLayout.Create(
            selectedRoot,
            displayName,
            storageMode,
            encryptionMode,
            activityRoot,
            workspaceId);
        var entry = new WorkspaceRegistryEntryV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = layout.Manifest.WorkspaceId,
            DisplayName = layout.Manifest.DisplayName,
            SelectedRoot = layout.SelectedRoot,
            ActivityRoot = storageMode == WorkspaceStorageMode.Mirrored
                ? layout.ActivityRoot
                : null,
            StorageKind = selectedStorage.StorageKind,
            CoordinationStrength = storageMode == WorkspaceStorageMode.Mirrored
                ? WorkspaceCoordinationStrength.Advisory
                : selectedStorage.CoordinationStrength,
            LastOpenedAt = null,
            LastKnownHealth = WorkspaceHealth.Healthy,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = false,
        };
        try
        {
            WorkspaceRepositoryInitialization repository =
                await _repositoryOnboarding.InitializeAsync(
                    entry,
                    cancellationToken);
            if (repository.RecoveryKey is not null)
            {
                _repositoryRecoveryUi.ConfirmRecoveryKey(
                    entry.DisplayName,
                    repository.RecoveryKey);
            }
            if (storageMode == WorkspaceStorageMode.Mirrored)
            {
                WorkspaceReplicaReceipt receipt =
                    await _replicaRecovery.InitializeAsync(
                        entry,
                        cancellationToken);
                entry = entry with
                {
                    LastSnapshotAt = receipt.VerifiedAt,
                    LastSyncAt = receipt.VerifiedAt,
                    PendingSync = false,
                };
            }
            _workspaceRegistry.Register(entry);
        }
        catch
        {
            TryRollbackUnregisteredLayout(layout);
            throw;
        }
        return new
        {
            workspaceId = layout.Manifest.WorkspaceId.ToString("D"),
            status = "created",
        };
    }

    internal static string ResolveWorkspaceCreateRoot(
        JsonElement parameters,
        Guid operationId,
        WorkspacePathGrantStore pathGrants,
        string productDataRoot,
        Guid workspaceId)
    {
        ArgumentNullException.ThrowIfNull(pathGrants);
        ArgumentException.ThrowIfNullOrWhiteSpace(productDataRoot);
        if (operationId == Guid.Empty || workspaceId == Guid.Empty)
            throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Workspace creation requires non-empty operation and workspace ids.");
        if (!SnapshotPackageBroker.HasExactProperties(
                parameters,
                "displayName",
                "locationPolicy",
                "selectedRootGrant",
                "storageMode",
                "encryptionMode",
                "userMarkedSync"))
        {
            throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Workspace create params contain missing or unknown fields.");
        }
        if (!parameters.TryGetProperty(
                "selectedRootGrant",
                out JsonElement selectedRootGrant))
        {
            throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Missing selectedRootGrant.");
        }

        string? locationPolicy = ReadString(parameters, "locationPolicy");
        bool userMarkedSync = ReadRequiredBoolean(
            parameters,
            "userMarkedSync");
        if (locationPolicy == "managedDefault" && userMarkedSync)
        {
            throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "The managed default location cannot be marked as sync-managed.");
        }
        if (locationPolicy == "managedDefault"
            && ReadString(parameters, "storageMode") == "mirrored")
        {
            throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "The managed default location requires direct storage mode.");
        }

        return locationPolicy switch
        {
            "managedDefault" when selectedRootGrant.ValueKind == JsonValueKind.Null =>
                Path.Combine(
                    Path.GetFullPath(productDataRoot),
                    "workspaces",
                    workspaceId.ToString("D")),
            "managedDefault" => throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "The managed default location must not include a path grant."),
            "other" => ResolveGrantedWorkspaceCreateRoot(
                parameters,
                operationId,
                pathGrants),
            _ => throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Workspace locationPolicy is invalid."),
        };
    }

    private static string ResolveGrantedWorkspaceCreateRoot(
        JsonElement parameters,
        Guid operationId,
        WorkspacePathGrantStore pathGrants)
    {
        JsonElement materialized = pathGrants.MaterializeSentinels(
            "workspace.create",
            operationId,
            parameters);
        string grant = ReadString(materialized, "selectedRootGrant")
            ?? throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "The other location requires a selectedRootGrant.");
        return pathGrants.Consume(
            grant,
            "workspace.create",
            operationId,
            "workspace-root");
    }

    private async Task<object> RegisterWorkspaceAsync(
        JsonElement parameters,
        Guid operationId,
        CancellationToken cancellationToken)
    {
        JsonElement materialized = _workspacePathGrants.MaterializeSentinels(
            "workspace.register",
            operationId,
            parameters);
        string grant = ReadString(materialized, "selectedRootGrant")
            ?? throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Missing selectedRootGrant.");
        string selectedRoot = _workspacePathGrants.Consume(
            grant,
            "workspace.register",
            operationId,
            "workspace-root");
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(
            selectedRoot);
        WorkspaceStorageObservation selectedStorage =
            _providerPolicy.ProbeAndEnsureSupported(
                selectedRoot,
                manifest.StorageMode);
        string? activityRoot = null;
        if (manifest.StorageMode == WorkspaceStorageMode.Mirrored)
        {
            activityRoot = ManagedActivityRoot(
                _activityRootBase,
                manifest.WorkspaceId);
            _ = ProbeCreateTarget(
                _providerPolicy,
                activityRoot,
                WorkspaceStorageMode.Direct);
        }
        var entry = new WorkspaceRegistryEntryV2
        {
            ContractVersion = WorkspaceV2Json.ContractVersion,
            WorkspaceId = manifest.WorkspaceId,
            DisplayName = manifest.DisplayName,
            SelectedRoot = Path.GetFullPath(selectedRoot),
            ActivityRoot = activityRoot,
            StorageKind = selectedStorage.StorageKind,
            CoordinationStrength =
                manifest.StorageMode == WorkspaceStorageMode.Mirrored
                    ? WorkspaceCoordinationStrength.Advisory
                    : selectedStorage.CoordinationStrength,
            LastOpenedAt = null,
            LastKnownHealth = WorkspaceHealth.Healthy,
            LastSnapshotAt = null,
            LastSyncAt = null,
            PendingSync = manifest.StorageMode == WorkspaceStorageMode.Mirrored,
        };
        if (manifest.StorageMode == WorkspaceStorageMode.Mirrored)
        {
            WorkspaceReplicaReceipt receipt =
                await _replicaRecovery.RecoverAndPublishAsync(
                    entry,
                    cancellationToken);
            entry = entry with
            {
                LastSnapshotAt = receipt.VerifiedAt,
                LastSyncAt = receipt.VerifiedAt,
                PendingSync = false,
            };
        }
        _workspaceRegistry.Register(entry);
        return new
        {
            workspaceId = manifest.WorkspaceId.ToString("D"),
            status = "registered",
        };
    }

    private async Task<object> RelinkWorkspaceAsync(
        JsonElement parameters,
        Guid operationId,
        CancellationToken cancellationToken)
    {
        Guid workspaceId = ReadRequiredGuid(parameters, "workspaceId");
        if (_workspaceSessions.Current.WorkspaceId == workspaceId)
            throw new WorkspaceRegistryException(
                "workspace.session_open",
                "Close the workspace before changing its location.");
        JsonElement materialized = _workspacePathGrants.MaterializeSentinels(
            "workspace.relink",
            operationId,
            parameters);
        string grant = ReadString(materialized, "selectedRootGrant")
            ?? throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Missing selectedRootGrant.");
        string selectedRoot = _workspacePathGrants.Consume(
            grant,
            "workspace.relink",
            operationId,
            "workspace-root");
        WorkspaceRegistryEntryV2 current = _workspaceRegistry.List()
            .SingleOrDefault(entry => entry.WorkspaceId == workspaceId)
            ?? throw new WorkspaceRegistryException(
                "workspace.not_registered",
                "Workspace is not registered on this device.");
        WorkspaceManifestV2 selectedManifest =
            WorkspaceLayout.ReadManifest(selectedRoot);
        if (selectedManifest.WorkspaceId != workspaceId)
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Selected path contains a different workspace UUID.");
        EnsureRelinkTopology(current, selectedManifest);
        if (selectedManifest.StorageMode == WorkspaceStorageMode.Mirrored)
        {
            WorkspaceStorageObservation selectedStorage =
                _providerPolicy.ProbeAndEnsureSupported(
                    selectedRoot,
                    selectedManifest.StorageMode);
            string activityRoot = string.IsNullOrWhiteSpace(current.ActivityRoot)
                ? ManagedActivityRoot(_activityRootBase, workspaceId)
                : current.ActivityRoot;
            var candidate = current with
            {
                SelectedRoot = Path.GetFullPath(selectedRoot),
                ActivityRoot = activityRoot,
                StorageKind = selectedStorage.StorageKind,
                CoordinationStrength = WorkspaceCoordinationStrength.Advisory,
                PendingSync = true,
            };
            WorkspaceReplicaReceipt receipt =
                _replicaRecovery.RequiresRecovery(candidate)
                    ? await _replicaRecovery.RecoverAndPublishAsync(
                        candidate,
                        cancellationToken)
                    : await _replicaRecovery.VerifyAsync(
                        candidate,
                        cancellationToken);
            _workspaceRegistry.Relink(
                workspaceId,
                selectedRoot,
                activityRoot,
                selectedStorage with
                {
                    CoordinationStrength =
                        WorkspaceCoordinationStrength.Advisory,
                });
            _workspaceRegistry.UpdateHealth(
                workspaceId,
                new WorkspaceHealthObservation(
                    Health: WorkspaceHealth.Healthy,
                    PendingSync: false,
                    LastSnapshotAt: receipt.VerifiedAt,
                    LastSyncAt: receipt.VerifiedAt));
        }
        else
        {
            _ = RelinkWorkspaceEntry(
                _workspaceRegistry,
                _providerPolicy,
                _activityRootBase,
                _workspaceSessions.Current.WorkspaceId,
                workspaceId,
                selectedRoot);
        }
        PostWorkspaceV2Bootstrap();
        return new
        {
            workspaceId = workspaceId.ToString("D"),
            status = "relinked",
        };
    }

    internal static WorkspaceRegistryEntryV2 RelinkWorkspaceEntry(
        WorkspaceRegistry registry,
        WorkspaceProviderPolicy providerPolicy,
        string activityRootBase,
        Guid? activeWorkspaceId,
        Guid workspaceId,
        string selectedRoot)
    {
        ArgumentNullException.ThrowIfNull(registry);
        ArgumentNullException.ThrowIfNull(providerPolicy);
        ArgumentException.ThrowIfNullOrWhiteSpace(activityRootBase);
        ArgumentException.ThrowIfNullOrWhiteSpace(selectedRoot);
        if (activeWorkspaceId == workspaceId)
            throw new WorkspaceRegistryException(
                "workspace.session_open",
                "Close the workspace before changing its location.");

        WorkspaceRegistryEntryV2 current = registry.List()
            .SingleOrDefault(entry => entry.WorkspaceId == workspaceId)
            ?? throw new WorkspaceRegistryException(
                "workspace.not_registered",
                "Workspace is not registered on this device.");
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(
            selectedRoot);
        WorkspaceStorageObservation selectedStorage =
            providerPolicy.ProbeAndEnsureSupported(
                selectedRoot,
                manifest.StorageMode);
        if (manifest.WorkspaceId != workspaceId)
            throw new WorkspaceRegistryException(
                "workspace.identity_mismatch",
                "Selected path contains a different workspace UUID.");
        EnsureRelinkTopology(current, manifest);

        string? activityRoot = null;
        if (manifest.StorageMode == WorkspaceStorageMode.Mirrored)
        {
            activityRoot = string.IsNullOrWhiteSpace(current.ActivityRoot)
                ? ManagedActivityRoot(activityRootBase, workspaceId)
                : current.ActivityRoot;
            if (!Directory.Exists(activityRoot))
            {
                throw new WorkspaceRegistryException(
                    "workspace.replica_recovery_required",
                    "Mirrored relink requires verified activity-root recovery.");
            }
            else
            {
                _ = providerPolicy.ProbeAndEnsureSupported(
                    activityRoot,
                    WorkspaceStorageMode.Direct);
                WorkspaceManifestV2 activityManifest =
                    WorkspaceLayout.ReadManifest(activityRoot);
                if (activityManifest.WorkspaceId != workspaceId)
                    throw new WorkspaceRegistryException(
                        "workspace.identity_mismatch",
                        "Activity path contains a different workspace UUID.");
            }
        }
        return registry.Relink(
            workspaceId,
            selectedRoot,
            activityRoot,
            manifest.StorageMode == WorkspaceStorageMode.Mirrored
                ? selectedStorage with
                {
                    CoordinationStrength =
                        WorkspaceCoordinationStrength.Advisory,
                }
                : selectedStorage);
    }

    internal static string ManagedActivityRoot(
        string activityRootBase,
        Guid workspaceId)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(activityRootBase);
        if (workspaceId == Guid.Empty)
            throw new ArgumentException(
                "Workspace identity is required.",
                nameof(workspaceId));
        return Path.Combine(
            Path.GetFullPath(activityRootBase),
            workspaceId.ToString("D"));
    }

    private static void EnsureRelinkTopology(
        WorkspaceRegistryEntryV2 current,
        WorkspaceManifestV2 manifest)
    {
        WorkspaceStorageMode registeredMode =
            string.IsNullOrWhiteSpace(current.ActivityRoot)
                ? WorkspaceStorageMode.Direct
                : WorkspaceStorageMode.Mirrored;
        if (manifest.StorageMode != registeredMode)
            throw new WorkspaceRegistryException(
                "workspace.storage_topology_mismatch",
                "Relinking cannot change storage topology; use a storage conversion plan.");
    }

    private static void TryRollbackUnregisteredLayout(
        WorkspaceLayoutResult layout)
    {
        foreach (string root in new[] { layout.ActivityRoot, layout.SelectedRoot }
                     .Distinct(StringComparer.OrdinalIgnoreCase))
        {
            try
            {
                WorkspaceLayout.DeleteWorkspaceRoot(
                    root,
                    layout.Manifest.WorkspaceId);
            }
            catch (Exception exception) when (
                exception is IOException
                    or UnauthorizedAccessException
                    or WorkspaceRegistryException)
            {
                // Preserve unexpected state for diagnosis. Registration was
                // never published, so no product path can open it.
            }
        }
    }

    private static WorkspaceStorageObservation ProbeCreateTarget(
        WorkspaceProviderPolicy providerPolicy,
        string root,
        WorkspaceStorageMode storageMode,
        bool userMarkedSync = false)
        => providerPolicy.ProbeCreateTargetAndEnsureSupported(
            root,
            storageMode,
            userMarkedSync);

    private object RemoveWorkspace(JsonElement parameters)
    {
        Guid workspaceId = ReadRequiredGuid(parameters, "workspaceId");
        if (_workspaceSessions.Current.WorkspaceId == workspaceId)
            throw new WorkspaceRegistryException(
                "workspace.session_open",
                "Close the workspace before removing it from this device.");
        _workspaceRegistry.Unregister(workspaceId);
        return new { workspaceId = workspaceId.ToString("D"), status = "removed" };
    }

    private object PlanWorkspaceDelete(JsonElement parameters)
    {
        Guid workspaceId = ReadRequiredGuid(parameters, "workspaceId");
        if (_workspaceSessions.Current.WorkspaceId == workspaceId)
            throw new WorkspaceRegistryException(
                "workspace.session_open",
                "Close the workspace before deleting it.");
        WorkspaceDeletePlan plan =
            _workspaceRegistry.PlanPermanentDelete(workspaceId);
        _workspaceDeletePlans[plan.PlanId] = plan;
        return new
        {
            planId = plan.PlanId.ToString("D"),
            displayName = plan.DisplayName,
            requiresTypedName = true,
        };
    }

    private object ApplyWorkspaceDelete(JsonElement parameters)
    {
        Guid planId = ReadRequiredGuid(parameters, "planId");
        string confirmation = ReadString(parameters, "confirmation")
            ?? string.Empty;
        if (!_workspaceDeletePlans.Remove(
                planId,
                out WorkspaceDeletePlan? plan))
        {
            throw new WorkspaceRegistryException(
                "workspace.delete_plan_stale",
                "Workspace delete plan is missing or expired.");
        }
        _workspaceRegistry.ApplyPermanentDelete(plan, confirmation);
        return new
        {
            workspaceId = plan.WorkspaceId.ToString("D"),
            status = "deleted",
        };
    }

    private static Guid ReadOperationId(JsonElement wire)
    {
        string? raw = ReadString(wire, "operationId");
        return Guid.TryParse(raw, out Guid operationId)
            && operationId != Guid.Empty
                ? operationId
                : throw new WorkspaceRegistryException(
                    "workspace.request_invalid",
                    "Workspace operationId is invalid.");
    }

    private static object ToSessionResult(WorkspaceSessionV2 session) => new
    {
        workspaceId = session.WorkspaceId?.ToString("D"),
        sessionEpoch = session.SessionEpoch,
        state = SessionStateName(session.State),
    };

    private void OnWorkspaceSessionChanged(
        object? sender,
        WorkspaceSessionChangedEventArgs args)
    {
        ConfigureReplicaStatusPolling(args.Session);
        if (Volatile.Read(ref _closing) != 0 || !_router.IsReady)
            return;
        Dispatcher.BeginInvoke(() =>
        {
            if (Volatile.Read(ref _closing) != 0 || !_router.IsReady)
                return;
            PostWorkspaceV2Bootstrap();
            OpenProductWorkspaceWhenReady();
        });
    }

    private void ConfigureReplicaStatusPolling(WorkspaceSessionV2 session)
    {
        bool opened = session.State is
            WorkspaceSessionState.OpenedReadOnly or
            WorkspaceSessionState.OpenedWritable or
            WorkspaceSessionState.OpenedProvisional;
        WorkspaceRegistryEntryV2? workspace = _runtime.CurrentWorkspace;
        bool enabled =
            Volatile.Read(ref _closing) == 0 &&
            opened &&
            session.WorkspaceId is Guid workspaceId &&
            workspaceId != Guid.Empty &&
            session.SessionEpoch > 0 &&
            workspace?.WorkspaceId == workspaceId &&
            !string.IsNullOrWhiteSpace(workspace.ActivityRoot) &&
            _runtime.CurrentCapabilities?.RpcMethods.Contains(
                "replica.status",
                StringComparer.Ordinal) == true;
        _replicaStatusMonitor.Bind(
            enabled ? session.WorkspaceId!.Value : Guid.Empty,
            enabled ? session.SessionEpoch : 0,
            enabled);
    }

    private async Task<WorkspaceReplicaStatus> RefreshReplicaStatusAsync(
        Guid workspaceId,
        ulong sessionEpoch,
        CancellationToken cancellationToken)
    {
        WorkspaceSessionV2 session = _workspaceSessions.Current;
        WorkspaceRegistryEntryV2 workspace =
            _runtime.CurrentWorkspace
            ?? throw new InvalidOperationException(
                "Replica status requires an active workspace runtime.");
        WorkspaceV2HttpGateway gateway =
            _runtime.CurrentV2Gateway
            ?? throw new InvalidOperationException(
                "Replica status requires an active Sidecar gateway.");
        if (session.WorkspaceId != workspaceId ||
            session.SessionEpoch != sessionEpoch ||
            session.State is not (
                WorkspaceSessionState.OpenedReadOnly or
                WorkspaceSessionState.OpenedWritable or
                WorkspaceSessionState.OpenedProvisional) ||
            workspace.WorkspaceId != workspaceId ||
            string.IsNullOrWhiteSpace(workspace.ActivityRoot) ||
            _runtime.CurrentCapabilities?.RpcMethods.Contains(
                "replica.status",
                StringComparer.Ordinal) != true)
            throw new InvalidOperationException(
                "Replica status session is no longer current.");

        Guid operationId = Guid.NewGuid();
        ulong sequence = _workspaceSessionFilter.ReserveHostSequence(
            workspaceId,
            sessionEpoch);
        JsonElement wire = JsonSerializer.SerializeToElement(new
        {
            scope = "workspace",
            workspaceId = workspaceId.ToString("D"),
            sessionEpoch,
            operationId = operationId.ToString("D"),
            sequence,
        });
        WorkspaceV2ForwardResult forwarded = await gateway.ForwardAsync(
            "desktop-replica-status-" + operationId.ToString("N"),
            "replica.status",
            wire,
            JsonSerializer.SerializeToElement(new { }),
            pathGrant: null,
            cancellationToken).ConfigureAwait(false);
        if (forwarded.Error is not null)
            throw new WorkspaceRegistryException(
                forwarded.Error.Code,
                "The Sidecar could not read durable replica status.");
        WorkspaceReplicaStatus status =
            WorkspaceReplicaStatusMonitor.Parse(
                forwarded.Result
                ?? throw new InvalidOperationException(
                    "Sidecar replica.status returned no result."));

        session = _workspaceSessions.Current;
        if (session.WorkspaceId != workspaceId ||
            session.SessionEpoch != sessionEpoch ||
            session.State is not (
                WorkspaceSessionState.OpenedReadOnly or
                WorkspaceSessionState.OpenedWritable or
                WorkspaceSessionState.OpenedProvisional))
            throw new InvalidOperationException(
                "Replica status response belongs to a retired session.");

        WorkspaceRegistryEntryV2 current = _workspaceRegistry.List()
            .SingleOrDefault(entry => entry.WorkspaceId == workspaceId)
            ?? throw new WorkspaceRegistryException(
                "workspace.not_registered",
                "Workspace is not registered on this device.");
        WorkspaceHealthObservation observation =
            WorkspaceReplicaStatusMonitor.ProjectHealth(
                current,
                status,
                DateTimeOffset.UtcNow);
        bool registryChanged =
            current.LastKnownHealth != observation.Health ||
            current.PendingSync != observation.PendingSync ||
            (observation.LastSyncAt is not null &&
             current.LastSyncAt != observation.LastSyncAt);
        if (registryChanged)
            _ = _workspaceRegistry.UpdateHealth(workspaceId, observation);

        var payloadSchema = new
        {
            type = "object",
            additionalProperties = false,
            required = new[] { "syncState", "pendingSync" },
            properties = new
            {
                syncState = new { type = "string" },
                pendingSync = new { type = "boolean" },
            },
        };
        _webBridge.PostWorkspaceV2Event(
            new
            {
                contractVersion = WorkspaceV2Json.ContractVersion,
                topic = "replica.changed",
                wire = forwarded.Wire,
                payloadModel = "ReplicaChangedEvent",
                payloadSchema,
                payload = new
                {
                    syncState = status.SyncState,
                    pendingSync = status.PendingSync,
                },
            },
            forwarded.Wire);
        if (registryChanged &&
            Volatile.Read(ref _closing) == 0 &&
            _router.IsReady)
            _ = Dispatcher.BeginInvoke(PostWorkspaceV2Bootstrap);
        return status;
    }

    private void PostWorkspaceV2Bootstrap()
    {
        if (!_router.IsReady)
            return;
        IReadOnlyList<WorkspaceRegistryEntryV2> workspaces;
        try
        {
            workspaces = _workspaceRegistry.List();
        }
        catch (WorkspaceRegistryException)
        {
            workspaces = [];
        }
        WorkspaceV2SidecarCapabilities? sidecar =
            _runtime.CurrentCapabilities;
        HashSet<string> methods = sidecar?.RpcMethods.ToHashSet(
            StringComparer.Ordinal) ?? [];
        var capabilities = new List<string>
        {
            "workspace.session.v2",
            "snapshot.package.v2",
            "workspace.storage.relocate.v2",
            "workspace.storage.topology.v2",
            "workspace.storage.release-cache.v2",
        };
        if (_providerPolicy.MirroredCreationEnabled)
            capabilities.Add("workspace.storage.mirrored-create.v2");
        // Synchronization is internal-only, but the renderer capability still
        // requires the complete Host-to-Sidecar protection protocol.
        if (ContainsEvery(
                methods,
                "snapshot.request",
                "snapshot.list",
                "snapshot.inspect",
                "snapshot.update",
                "snapshot.previewRestore",
                "snapshot.applyRestore",
                "snapshot.previewExtract",
                "snapshot.applyExtract",
                "snapshot.export",
                "repository.verify"))
        {
            capabilities.Add("snapshot.timeline.v2");
            capabilities.Add("snapshot.open-as-new.v2");
        }
        if (ContainsEvery(
                methods,
                "history.query",
                "history.previewRestore",
                "history.applyRestore"))
            capabilities.Add("history.restore.v2");
        if (ContainsEvery(
                methods,
                "fileHistory.listDocuments",
                "fileHistory.listPendingChanges",
                "fileHistory.import",
                "fileHistory.relink",
                "fileHistory.unlink",
                "fileHistory.readTree",
                "fileHistory.restore",
                "fileHistory.upgrade",
                "fileHistory.activateLeaf",
                "fileHistory.applyPendingChange"))
            capabilities.Add("fileHistory.tree.v2");
        if (_documentWorkspace is not null &&
            ContainsEvery(
                methods,
                WorkspaceDocumentOsAdapter.MaterializeDiffPairMethod,
                WorkspaceDocumentOsAdapter.AssertEffectiveRevisionMethod))
            capabilities.Add("document.diff.v1");
        if (ContainsEvery(
                methods,
                "retention.get",
                "retention.status",
                "retention.update",
                "retention.plan",
                "retention.apply"))
            capabilities.Add("retention.policy.v2");
        if (methods.Contains("repository.verify"))
            capabilities.Add("repository.settings.v2");
        if (ContainsEvery(
                methods,
                "repository.previewKeyRotation",
                "repository.applyKeyRotation"))
        {
            capabilities.Add("repository.key-rotation.v2");
        }
        if (ContainsEvery(
                methods,
                "conflict.list",
                "conflict.inspect",
                "conflict.preview",
                "conflict.apply"))
        {
            capabilities.Add("conflict.center.v2");
        }
        WorkspaceSessionV2 session = _workspaceSessions.Current;
        _webBridge.PostNotification(
            "workspace.v2.bootstrap",
            new
            {
                contractVersion = WorkspaceV2Json.ContractVersion,
                capabilities,
                workspaces = workspaces
                    .Select(ToWorkspaceProjection)
                    .ToArray(),
                session = ToSessionProjection(session),
                snapshots = Array.Empty<object>(),
                storage = BuildStorageProjection(
                    _runtime.CurrentWorkspace,
                    capabilities.Contains(
                        "repository.settings.v2",
                        StringComparer.Ordinal)),
                // The Web layer never owns retention defaults. It stays
                // unhydrated until retention.get returns the authority
                // projection for the active workspace and session epoch.
                retention = (object?)null,
                conflicts = Array.Empty<object>(),
                fileTrees = Array.Empty<object>(),
            });
    }

    private static object ToWorkspaceProjection(
        WorkspaceRegistryEntryV2 workspace) => new
    {
        contractVersion = workspace.ContractVersion,
        workspaceId = workspace.WorkspaceId.ToString("D"),
        displayName = workspace.DisplayName,
        selectedRoot = workspace.SelectedRoot,
        activityRoot = workspace.ActivityRoot,
        storageKind = workspace.StorageKind switch
        {
            WorkspaceStorageKind.Fixed => "fixed",
            WorkspaceStorageKind.Network => "network",
            WorkspaceStorageKind.Removable => "removable",
            WorkspaceStorageKind.RegisteredCloud => "registeredCloud",
            WorkspaceStorageKind.UserMarkedSync => "userMarkedSync",
            _ => throw new ArgumentOutOfRangeException(
                nameof(workspace.StorageKind)),
        },
        coordinationStrength = workspace.CoordinationStrength
            == WorkspaceCoordinationStrength.Strong
                ? "strong"
                : "advisory",
        lastOpenedAt = workspace.LastOpenedAt,
        lastKnownHealth = workspace.LastKnownHealth switch
        {
            WorkspaceHealth.Healthy => "healthy",
            WorkspaceHealth.Offline => "offline",
            WorkspaceHealth.Degraded => "degraded",
            WorkspaceHealth.Corrupt => "corrupt",
            WorkspaceHealth.Unknown => "unknown",
            _ => throw new ArgumentOutOfRangeException(
                nameof(workspace.LastKnownHealth)),
        },
        lastSnapshotAt = workspace.LastSnapshotAt,
        lastSyncAt = workspace.LastSyncAt,
        pendingSync = workspace.PendingSync,
    };

    private static object ToSessionProjection(WorkspaceSessionV2 session)
        => new
        {
            contractVersion = session.ContractVersion,
            workspaceId = session.WorkspaceId?.ToString("D"),
            sessionEpoch = session.SessionEpoch,
            state = SessionStateName(session.State),
            openMode = session.OpenMode switch
            {
                WorkspaceOpenMode.ReadOnly => "readOnly",
                WorkspaceOpenMode.Writable => "writable",
                WorkspaceOpenMode.Provisional => "provisional",
                _ => throw new ArgumentOutOfRangeException(
                    nameof(session.OpenMode)),
            },
            writable = session.Writable,
            provisional = session.Provisional,
            phase = session.Phase switch
            {
                WorkspaceSessionPhase.Idle => "idle",
                WorkspaceSessionPhase.Protecting => "protecting",
                WorkspaceSessionPhase.Draining => "draining",
                WorkspaceSessionPhase.Stopping => "stopping",
                WorkspaceSessionPhase.Starting => "starting",
                WorkspaceSessionPhase.Binding => "binding",
                WorkspaceSessionPhase.Verifying => "verifying",
                WorkspaceSessionPhase.RollingBack => "rollingBack",
                _ => throw new ArgumentOutOfRangeException(
                    nameof(session.Phase)),
            },
            errorCode = session.ErrorCode,
        };

    private static object? BuildStorageProjection(
        WorkspaceRegistryEntryV2? workspace,
        bool enabled)
    {
        if (!enabled || workspace is null)
            return null;
        string root = ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
        WorkspaceManifestV2 manifest = WorkspaceLayout.ReadManifest(root);
        (long logicalSize, long physicalSize) =
            MeasureWorkspaceStorage(workspace);
        return new
        {
            location = workspace.SelectedRoot,
            activityRoot = root,
            mode = manifest.StorageMode == WorkspaceStorageMode.Direct
                ? "direct"
                : "mirrored",
            provider = workspace.StorageKind switch
            {
                WorkspaceStorageKind.Fixed => "fixed",
                WorkspaceStorageKind.Network => "network",
                WorkspaceStorageKind.Removable => "removable",
                WorkspaceStorageKind.RegisteredCloud => "registeredCloud",
                WorkspaceStorageKind.UserMarkedSync => "userMarkedSync",
                _ => throw new ArgumentOutOfRangeException(
                    nameof(workspace.StorageKind)),
            },
            health = workspace.LastKnownHealth switch
            {
                WorkspaceHealth.Healthy => "healthy",
                WorkspaceHealth.Offline => "offline",
                _ => "attention",
            },
            logicalSize,
            physicalSize,
            // Reclaimability is an authority-owned retention-plan result.
            // Bootstrap never guesses it; the retention UI replaces this
            // neutral value after retention.plan completes.
            reclaimableSize = 0L,
            encryption = manifest.EncryptionMode switch
            {
                WorkspaceEncryptionMode.None => "none",
                WorkspaceEncryptionMode.Convenient => "convenient",
                WorkspaceEncryptionMode.Protected => "protected",
                _ => throw new ArgumentOutOfRangeException(
                    nameof(manifest.EncryptionMode)),
            },
            keyVersion = manifest.EncryptionMode
                == WorkspaceEncryptionMode.None ? 0 : 1,
            pendingSync = workspace.PendingSync,
            replicaVerified =
                manifest.StorageMode == WorkspaceStorageMode.Direct,
        };
    }

    internal static (long LogicalSize, long PhysicalSize)
        MeasureWorkspaceStorage(WorkspaceRegistryEntryV2 workspace)
    {
        ArgumentNullException.ThrowIfNull(workspace);
        string activityRoot =
            ProductionWorkspaceRuntimeFactory.RuntimeRoot(workspace);
        WorkspacePaths activity = WorkspaceLayout.Paths(activityRoot);
        long logical = AddSaturating(
            MeasureDirectoryBytes(activity.Data),
            MeasureDirectoryBytes(activity.Files));

        string selectedRoot = Path.GetFullPath(workspace.SelectedRoot);
        long physical = 0;
        foreach (string root in new[] { selectedRoot, activityRoot }
                     .Distinct(StringComparer.OrdinalIgnoreCase))
        {
            physical = AddSaturating(
                physical,
                MeasureDirectoryBytes(root));
        }
        return (logical, physical);
    }

    private static long MeasureDirectoryBytes(string root)
    {
        if (!Directory.Exists(root))
            return 0;
        var options = new EnumerationOptions
        {
            RecurseSubdirectories = true,
            IgnoreInaccessible = true,
            ReturnSpecialDirectories = false,
            AttributesToSkip = FileAttributes.ReparsePoint,
        };
        long total = 0;
        try
        {
            foreach (string path in Directory.EnumerateFiles(
                         root,
                         "*",
                         options))
            {
                try
                {
                    total = AddSaturating(
                        total,
                        new FileInfo(path).Length);
                }
                catch (Exception exception) when (
                    exception is IOException or
                        UnauthorizedAccessException)
                {
                    // The live workspace may rotate files while metrics are
                    // sampled. Skip only that file and keep the projection.
                }
            }
        }
        catch (Exception exception) when (
            exception is IOException or
                UnauthorizedAccessException)
        {
            // A provider may disconnect during bootstrap. Health remains the
            // authoritative signal; never follow a fallback path.
        }
        return total;
    }

    private static long AddSaturating(long left, long right)
        => left > long.MaxValue - right
            ? long.MaxValue
            : left + right;

    private static string SessionStateName(
        WorkspaceSessionState state) => state switch
    {
        WorkspaceSessionState.Closed => "closed",
        WorkspaceSessionState.Opening => "opening",
        WorkspaceSessionState.OpenedReadOnly => "openedReadOnly",
        WorkspaceSessionState.OpenedWritable => "openedWritable",
        WorkspaceSessionState.OpenedProvisional => "openedProvisional",
        WorkspaceSessionState.Switching => "switching",
        WorkspaceSessionState.Failed => "failed",
        _ => throw new ArgumentOutOfRangeException(nameof(state)),
    };

    internal static bool IsWorkspaceMutation(string method)
        => method is
            "snapshot.request"
            or "snapshot.update"
            or "snapshot.applyRestore"
            or "snapshot.import"
            or "history.applyRestore"
            or "repository.applyKeyRotation"
            or "fileHistory.import"
            or "fileHistory.relink"
            or "fileHistory.unlink"
            or "fileHistory.restore"
            or "fileHistory.upgrade"
            or "fileHistory.activateLeaf"
            or "fileHistory.applyPendingChange"
            or "retention.update"
            or "retention.apply"
            or "replica.forceTakeover"
            or "conflict.apply";

    private static bool ContainsEvery(
        HashSet<string> methods,
        params string[] required)
        => required.All(methods.Contains);

    private static Guid ReadRequiredGuid(JsonElement value, string name)
    {
        string? raw = ReadString(value, name);
        return Guid.TryParse(raw, out Guid parsed) && parsed != Guid.Empty
            ? parsed
            : throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                $"Missing or invalid '{name}'.");
    }

    private static WorkspaceOpenMode ReadOpenMode(JsonElement value)
    {
        string? raw = ReadString(value, "openMode");
        return raw switch
        {
            "writable" => WorkspaceOpenMode.Writable,
            "readOnly" => WorkspaceOpenMode.ReadOnly,
            "provisional" => WorkspaceOpenMode.Provisional,
            _ => throw new WorkspaceRegistryException(
                "workspace.request_invalid",
                "Missing or invalid 'openMode'."),
        };
    }

    private async Task ApplyAttachmentChangeAsync(
        RoutedWebRequest request,
        IReadOnlyList<string> hostPaths,
        IReadOnlyList<string> removeStoredNames)
    {
        _readiness?.Trace(
            $"Attachment change starting; type={request.Type}; " +
            $"uploads={hostPaths.Count}; removals={removeStoredNames.Count}");
        JsonRpcProductDataGateway? gateway = _productGateway;
        if (gateway is null
            || !TryReadAttachmentContext(
                request.Payload,
                requireDigest: true,
                out Dictionary<string, object?> context))
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "缺少最新附件行版本，请刷新后重试。",
                "ATTACHMENT_CONTEXT_INVALID");
            return;
        }
        context["hostPaths"] = hostPaths;
        context["removeStoredNames"] = removeStoredNames;
        try
        {
            JsonElement result = await gateway.ApplyHostAttachmentChangeAsync(
                JsonSerializer.SerializeToElement(context),
                _session.Token);
            _readiness?.Trace(
                $"Attachment change completed; type={request.Type}");
            _webBridge.PostResponse(request.Type, request.RequestId, result);
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
        }
        catch (Exception exception)
        {
            _readiness?.Trace(
                $"Attachment change failed; type={request.Type}; " +
                $"exception={exception.GetType().Name}");
            _webBridge.PostOperationFailed(
                request.RequestId,
                "托管附件变更失败，请刷新记录后重试。",
                "ATTACHMENT_CHANGE_FAILED");
        }
    }

    private async Task FetchDailyQuoteAsync(RoutedWebRequest request)
    {
        if (!DailyQuoteHostClient.TryParseRequest(
                request.Payload,
                out DailyQuoteHostRequest? quoteRequest)
            || quoteRequest is null)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "The daily quote request is invalid.",
                "DAILY_QUOTE_BAD_PAYLOAD");
            return;
        }
        try
        {
            DailyQuoteHostResult result = await _dailyQuotes.FetchAsync(
                quoteRequest,
                _session.Token).ConfigureAwait(false);
            _webBridge.PostResponse("dailyQuote.fetch", request.RequestId, result);
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
        }
        catch (DailyQuoteHostException exception)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                exception.Message,
                exception.Code);
        }
        catch (Exception exception)
        {
            _readiness?.Trace(
                $"Daily quote request failed; exception={exception.GetType().Name}");
            _webBridge.PostOperationFailed(
                request.RequestId,
                "The daily quote provider is unavailable.",
                "DAILY_QUOTE_UNAVAILABLE");
        }
    }

    private static bool HasEmptyObjectPayload(RoutedWebRequest request)
        => request.Payload.ValueKind == JsonValueKind.Object
            && !request.Payload.EnumerateObject().Any();

    private async Task CheckForReleaseUpdateAsync(RoutedWebRequest request)
    {
        try
        {
            _appPreferences = _appPreferencesService.Read();
            ReleaseUpdateCheckResult result = await _releaseUpdateCoordinator.CheckAsync(
                _appPreferences,
                _session.Token);
            _webBridge.PostResponse(
                request.Type,
                request.RequestId,
                new
                {
                    currentVersion = result.CurrentVersion,
                    latestVersion = result.LatestVersion,
                    updateAvailable = result.UpdateAvailable,
                    canInstall = result.CanInstall,
                    installUnavailableReason = result.InstallUnavailableReason,
                    downloadBytes = result.DownloadBytes,
                    releaseUrl = result.ReleaseUrl,
                    notesTruncated = result.NotesTruncated,
                    releases = result.Releases.Select(note => new
                    {
                        version = note.Version,
                        title = note.Title,
                        body = note.Body,
                        publishedAt = note.PublishedAt,
                        releaseUrl = note.ReleaseUrl,
                    }),
                });
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
            // The application is already shutting down.
        }
        catch (ReleaseUpdateException exception)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                exception.Message,
                exception.Code);
        }
        catch (Exception)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "无法连接 GitHub 检查更新，请稍后重试。",
                "UPDATE_CHECK_FAILED");
        }
    }

    private async Task InstallReleaseUpdateAsync(RoutedWebRequest request)
    {
        try
        {
            await _releaseUpdateCoordinator.LaunchUpdateAsync(_session.Token);
            _webBridge.PostResponse(
                request.Type,
                request.RequestId,
                new { status = "restarting" });
            _ = Dispatcher.BeginInvoke(RequestExit);
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
            // The application is already shutting down.
        }
        catch (ReleaseUpdateException exception)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                exception.Message,
                exception.Code);
        }
        catch (Exception)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "更新包下载或暂存失败，现有程序与用户数据均未更改。",
                "UPDATE_INSTALL_FAILED");
        }
    }

    internal static bool TryReadAppPreferencesPatch(
        JsonElement payload,
        out AppPreferencesPatch? patch)
    {
        patch = null;
        if (payload.ValueKind != JsonValueKind.Object) return false;

        bool? minimizeToTrayOnClose = null;
        bool? startWithWindows = null;
        string? updateProxy = null;
        string? customUpdateProxyUrl = null;
        bool sawMinimize = false;
        bool sawStartup = false;
        bool sawUpdateProxy = false;
        bool sawCustomUpdateProxyUrl = false;
        foreach (JsonProperty property in payload.EnumerateObject())
        {
            if (property.NameEquals("minimizeToTrayOnClose"))
            {
                if (sawMinimize || property.Value.ValueKind is not
                        (JsonValueKind.True or JsonValueKind.False))
                {
                    return false;
                }
                sawMinimize = true;
                minimizeToTrayOnClose = property.Value.GetBoolean();
                continue;
            }
            if (property.NameEquals("startWithWindows"))
            {
                if (sawStartup || property.Value.ValueKind is not
                        (JsonValueKind.True or JsonValueKind.False))
                {
                    return false;
                }
                sawStartup = true;
                startWithWindows = property.Value.GetBoolean();
                continue;
            }
            if (property.NameEquals("updateProxy"))
            {
                if (sawUpdateProxy || property.Value.ValueKind != JsonValueKind.String)
                {
                    return false;
                }
                sawUpdateProxy = true;
                updateProxy = property.Value.GetString();
                if (updateProxy is null || !UpdateProxyOptions.IsKnown(updateProxy))
                {
                    return false;
                }
                continue;
            }
            if (property.NameEquals("customUpdateProxyUrl"))
            {
                if (sawCustomUpdateProxyUrl
                    || property.Value.ValueKind != JsonValueKind.String)
                {
                    return false;
                }
                sawCustomUpdateProxyUrl = true;
                customUpdateProxyUrl = property.Value.GetString();
                if (customUpdateProxyUrl is null || customUpdateProxyUrl.Length > 2048)
                {
                    return false;
                }
                continue;
            }
            return false;
        }

        if (!sawMinimize && !sawStartup && !sawUpdateProxy && !sawCustomUpdateProxyUrl)
        {
            return false;
        }
        patch = new AppPreferencesPatch(
            minimizeToTrayOnClose,
            startWithWindows,
            updateProxy,
            customUpdateProxyUrl,
            sawCustomUpdateProxyUrl);
        return true;
    }

    private void PostAppPreferences(RoutedWebRequest request)
    {
        _webBridge.PostResponse(
            request.Type,
            request.RequestId,
            new
            {
                minimizeToTrayOnClose = _appPreferences.MinimizeToTrayOnClose,
                startWithWindows = _appPreferences.StartWithWindows,
                updateProxy = _appPreferences.UpdateProxy,
                customUpdateProxyUrl = _appPreferences.CustomUpdateProxyUrl ?? "",
            });
    }

    private void ApplyAppPreferences(AppPreferences preferences)
    {
        if (!preferences.MinimizeToTrayOnClose)
        {
            if (_trayIcon is not null) _trayIcon.Visible = false;
            return;
        }

        EnsureTrayIcon().Visible = true;
    }

    private TrayIconController EnsureTrayIcon() =>
        _trayIcon ??= new TrayIconController(RestoreFromTray, RequestExit);

    private void OnClosing(object? sender, CancelEventArgs args)
    {
        if (!WindowClosePolicy.ShouldMinimizeToTray(
                _appPreferences,
                _explicitExitRequested))
        {
            return;
        }

        ApplyAppPreferences(_appPreferences);
        args.Cancel = true;
        Hide();
    }

    private void RestoreFromTray()
    {
        Dispatcher.BeginInvoke(() =>
        {
            Show();
            if (WindowState == WindowState.Minimized)
            {
                WindowState = WindowState.Normal;
            }
            Activate();
        });
    }

    private void RequestExit()
    {
        Dispatcher.BeginInvoke(() =>
        {
            _explicitExitRequested = true;
            Close();
        });
    }

    private void CheckTestModeHostControls()
    {
        if (_e2eControlsDir is null
            || Volatile.Read(ref _closing) != 0
            || Dispatcher.HasShutdownStarted
            || Dispatcher.HasShutdownFinished)
        {
            return;
        }
        if (TryConsumeTestModeControl("host-normal-close.request"))
        {
            DispatchTestModeControl("normal close", RequestExit);
            return;
        }
        if (TryConsumeTestModeControl("host-window-close.request"))
        {
            DispatchTestModeControl(
                "window close",
                () =>
                {
                    Close();
                    WriteTestModeHostState("close-to-tray");
                });
            return;
        }
        if (TryConsumeTestModeControl("host-tray-exit.request"))
        {
            DispatchTestModeControl(
                "tray exit",
                () =>
                {
                    WriteTestModeHostState("tray-exit-requested");
                    RequestExit();
                });
            return;
        }
        string openControl = Path.Combine(_e2eControlsDir, "host-open-workspace.request");
        if (!File.Exists(openControl)) return;
        string workspaceText;
        try
        {
            workspaceText = File.ReadAllText(openControl).Trim();
            File.Delete(openControl);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            _readiness?.Trace(
                $"MainWindow: test-mode workspace open control rejected: {exception.GetType().Name}");
            return;
        }
        if (!Guid.TryParse(workspaceText, out Guid workspaceId))
        {
            WriteTestModeHostState(
                "workspace-open-failed",
                error: "workspace ID control is invalid");
            return;
        }
        DispatchTestModeControl(
            "workspace open",
            async () =>
            {
                try
                {
                    _ = await OpenWorkspaceWithRecoveryAsync(
                        workspaceId,
                        WorkspaceOpenMode.Writable,
                        switching: false,
                        _session.Token);
                    WriteTestModeHostState("workspace-opened");
                }
                catch (Exception exception)
                {
                    WriteTestModeHostState(
                        "workspace-open-failed",
                        error: $"{exception.GetType().Name}: {exception.Message}");
                }
            });
    }

    private bool TryConsumeTestModeControl(string controlName)
    {
        if (_e2eControlsDir is null) return false;
        string controlPath = Path.Combine(_e2eControlsDir, controlName);
        if (!File.Exists(controlPath)) return false;
        try
        {
            File.Delete(controlPath);
            return true;
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            _readiness?.Trace(
                $"MainWindow: test-mode {controlName} rejected: {exception.GetType().Name}");
            return false;
        }
    }

    private void DispatchTestModeControl(string action, Action callback)
    {
        _readiness?.Trace($"MainWindow: test-mode {action} requested");
        try
        {
            Dispatcher.BeginInvoke(callback);
        }
        catch (InvalidOperationException exception)
        {
            _readiness?.Trace(
                $"MainWindow: test-mode {action} dispatch rejected: {exception.GetType().Name}");
        }
    }

    internal void ReportTestModeStartupVisibility()
    {
        if (_e2eControlsDir is null) return;
        WriteTestModeHostState(StartHidden ? "silent-startup" : "visible-startup");
    }

    private void WriteTestModeHostState(
        string action,
        string? error = null)
    {
        if (_e2eControlsDir is null) return;
        WorkspaceSessionV2 session = _workspaceSessions.Current;
        bool hasWorkspaceSession = session.WorkspaceId is not null;
        var state = new
        {
            evidenceKind = "packaged-host-control",
            action,
            hostExecutable = Path.GetFileName(Environment.ProcessPath),
            hostProcessId = Environment.ProcessId,
            windowVisible = IsVisible,
            trayVisible = _trayIcon?.Visible == true,
            workspaceId = session.WorkspaceId,
            sessionEpoch = hasWorkspaceSession ? session.SessionEpoch : (ulong?)null,
            sessionState = !hasWorkspaceSession
                ? null
                : JsonNamingPolicy.CamelCase.ConvertName(session.State.ToString()),
            error,
        };
        string destination = Path.Combine(_e2eControlsDir, "host-lifecycle-state.json");
        string temporary = destination + $".{Guid.NewGuid():N}.tmp";
        try
        {
            File.WriteAllText(
                temporary,
                JsonSerializer.Serialize(state, WorkspaceV2Json.StrictOptions));
            File.Move(temporary, destination, overwrite: true);
        }
        catch (Exception exception) when (
            exception is IOException or UnauthorizedAccessException)
        {
            try { File.Delete(temporary); } catch { }
            _readiness?.Trace(
                $"MainWindow: test-mode host state write rejected: {exception.GetType().Name}");
        }
    }

    private void OnSessionEnding(object? sender, SessionEndingCancelEventArgs args)
    {
        _explicitExitRequested = true;
    }

    private IReadOnlyList<string> PickAttachmentPaths(bool multiselect)
    {
        var dialog = new OpenFileDialog
        {
            CheckFileExists = true,
            Multiselect = multiselect,
            Title = multiselect ? "Add attachments" : "Replace attachment",
            Filter = "All files|*.*",
        };
        return dialog.ShowDialog(this) == true
            ? dialog.FileNames
            : Array.Empty<string>();
    }

    private void PickImportSource(RoutedWebRequest request)
    {
        JsonRpcProductDataGateway? gateway = _productGateway;
        if (gateway is null)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "Local data service is unavailable.",
                "BACKEND_UNAVAILABLE");
            return;
        }
        string? selectedPath = ReadE2eControlPath(
            "import-source.txt",
            requireExistingFile: true);
        if (selectedPath is null)
        {
            var dialog = new OpenFileDialog
            {
                CheckFileExists = true,
                Multiselect = false,
                Title = "Import table data",
                Filter = "Supported data|*.xlsx;*.xls;*.csv|Excel workbook|*.xlsx;*.xls|CSV file|*.csv",
            };
            if (dialog.ShowDialog(this) != true)
            {
                _webBridge.PostOperationFailed(request.RequestId, "Import cancelled.", "CANCELLED");
                return;
            }
            selectedPath = dialog.FileName;
        }
        var info = new FileInfo(selectedPath);
        _ = RegisterPickedPathAsync(
            gateway.RegisterImportSourceAsync,
            request,
            new { path = info.FullName, sizeBytes = info.Length, mimeType = (string?)null });
    }

    private string? ReadE2eControlPath(
        string controlName,
        bool requireExistingFile)
    {
        if (_e2eControlsDir is null)
        {
            return null;
        }
        string controlPath = Path.Combine(_e2eControlsDir, controlName);
        if (!File.Exists(controlPath))
        {
            throw new InvalidOperationException(
                $"Missing test-mode control file: {controlName}");
        }
        string value = File.ReadAllText(controlPath).Trim();
        if (string.IsNullOrWhiteSpace(value))
        {
            throw new InvalidOperationException(
                $"Empty test-mode control file: {controlName}");
        }
        string fullPath = Path.GetFullPath(value);
        if (requireExistingFile && !File.Exists(fullPath))
        {
            throw new FileNotFoundException(
                $"Test-mode selected file does not exist: {controlName}",
                fullPath);
        }
        return fullPath;
    }

    private void PickExportTarget(RoutedWebRequest request)
    {
        JsonRpcProductDataGateway? gateway = _productGateway;
        if (gateway is null)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "Local data service is unavailable.",
                "BACKEND_UNAVAILABLE");
            return;
        }
        string format = ReadString(request.Payload, "format") == "xlsx" ? "xlsx" : "csv";
        string defaultName = SafeFileName(ReadString(request.Payload, "defaultName"))
            ?? $"vibetable-export.{format}";
        string? selectedPath = ReadE2eControlPath(
            "export-target.txt",
            requireExistingFile: false);
        if (selectedPath is null)
        {
            var dialog = new SaveFileDialog
            {
                FileName = defaultName,
                AddExtension = true,
                DefaultExt = $".{format}",
                OverwritePrompt = true,
                Title = "Export table data",
                Filter = format == "xlsx" ? "Excel workbook|*.xlsx" : "CSV file|*.csv",
            };
            if (dialog.ShowDialog(this) != true)
            {
                _webBridge.PostOperationFailed(request.RequestId, "Export cancelled.", "CANCELLED");
                return;
            }
            selectedPath = dialog.FileName;
        }
        _ = RegisterPickedPathAsync(
            gateway.RegisterExportTargetAsync,
            request,
            new { path = selectedPath });
    }

    private async Task RegisterPickedPathAsync(
        Func<JsonElement, CancellationToken, Task<JsonElement>> register,
        RoutedWebRequest request,
        object parameters)
    {
        try
        {
            JsonElement grant = await register(
                JsonSerializer.SerializeToElement(parameters),
                _session.Token);
            _webBridge.PostResponse(request.Type, request.RequestId, grant);
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
        }
        catch (Exception exception)
        {
            _readiness?.Trace(
                $"Path grant failed; type={request.Type}; " +
                $"exception={exception.GetType().Name}");
            _webBridge.PostOperationFailed(
                request.RequestId,
                "无法使用所选位置，请重新选择后重试。",
                "PATH_GRANT_FAILED");
        }
    }

    private void SaveAttachment(RoutedWebRequest request)
    {
        JsonRpcProductDataGateway? gateway = _productGateway;
        if (gateway is null
            || !TryReadAttachmentContext(
                request.Payload,
                requireDigest: false,
                out Dictionary<string, object?> context))
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "附件下载参数无效。",
                "ATTACHMENT_CONTEXT_INVALID");
            return;
        }
        string? storedName = ReadString(request.Payload, "storedName");
        string? originalName = ReadString(request.Payload, "originalName");
        if (string.IsNullOrWhiteSpace(storedName))
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "缺少托管附件标识。",
                "BAD_PAYLOAD");
            return;
        }
        string suggested = SafeFileName(originalName)
            ?? SafeFileName(storedName)
            ?? "attachment.bin";
        var dialog = new SaveFileDialog
        {
            FileName = suggested,
            AddExtension = false,
            CheckPathExists = true,
            OverwritePrompt = true,
            Title = "保存托管附件",
            Filter = "所有文件|*.*",
        };
        if (dialog.ShowDialog(this) != true)
        {
            return;
        }
        context["storedName"] = storedName;
        context["outputPath"] = dialog.FileName;
        _ = SaveAttachmentCoreAsync(
            gateway,
            request.RequestId,
            JsonSerializer.SerializeToElement(context));
    }

    private async Task PreviewAttachmentAsync(RoutedWebRequest request)
    {
        JsonRpcProductDataGateway? gateway = _productGateway;
        if (gateway is null
            || !TryReadAttachmentContext(
                request.Payload,
                requireDigest: false,
                out Dictionary<string, object?> context))
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "附件预览参数无效。",
                "ATTACHMENT_CONTEXT_INVALID");
            return;
        }
        string? storedName = ReadString(request.Payload, "storedName");
        string? originalName = ReadString(request.Payload, "originalName");
        if (string.IsNullOrWhiteSpace(storedName))
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "缺少托管附件标识。",
                "BAD_PAYLOAD");
            return;
        }
        string suggested = SafeFileName(originalName)
            ?? SafeFileName(storedName)
            ?? "attachment.bin";
        string previewPath = Path.Combine(
            _attachmentPreviewRoot,
            $"{Guid.NewGuid():N}-{suggested}");
        context["storedName"] = storedName;
        context["outputPath"] = previewPath;
        try
        {
            await gateway.SaveAttachmentToHostAsync(
                JsonSerializer.SerializeToElement(context),
                _session.Token);
            await Dispatcher.InvokeAsync(() =>
            {
                if (!_attachmentPreview.CanPreview(previewPath))
                {
                    throw new DocumentPreviewException(
                        "系统没有为此文件类型注册安全预览器。",
                        "PREVIEW_HANDLER_UNAVAILABLE");
                }
                _attachmentPreview.Show(previewPath);
            });
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
        }
        catch (DocumentPreviewException exception)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                exception.Message,
                exception.Code);
        }
        catch (Exception)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "托管附件预览失败，请稍后重试。",
                "ATTACHMENT_PREVIEW_FAILED");
        }
    }

    private async Task SaveAttachmentCoreAsync(
        JsonRpcProductDataGateway gateway,
        string? requestId,
        JsonElement parameters)
    {
        try
        {
            await gateway.SaveAttachmentToHostAsync(parameters, _session.Token);
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
        }
        catch
        {
            _webBridge.PostOperationFailed(
                requestId,
                "托管附件下载失败，请重试。",
                "ATTACHMENT_DOWNLOAD_FAILED");
        }
    }

    private static bool TryReadAttachmentContext(
        JsonElement payload,
        bool requireDigest,
        out Dictionary<string, object?> context)
    {
        context = new Dictionary<string, object?>(StringComparer.Ordinal);
        foreach (string name in new[] { "tableId", "recordId", "fieldId" })
        {
            string? value = ReadString(payload, name);
            if (string.IsNullOrWhiteSpace(value))
            {
                context.Clear();
                return false;
            }
            context[name] = value;
        }
        if (!requireDigest)
        {
            return true;
        }
        string? schemaRevision = ReadString(payload, "schemaRevision");
        string? expectedDigest = ReadString(payload, "expectedDigest");
        if (string.IsNullOrWhiteSpace(schemaRevision)
            || !IsRowDigest(expectedDigest))
        {
            context.Clear();
            return false;
        }
        context["schemaRevision"] = schemaRevision;
        context["expectedDigest"] = expectedDigest;
        return true;
    }

    private static bool IsRowDigest(string? value)
        => value is { Length: 71 }
            && value.StartsWith("sha256:", StringComparison.Ordinal)
            && value.AsSpan(7).IndexOfAnyExcept(
                "0123456789abcdef".AsSpan()) < 0;

    private static string? SafeFileName(string? value)
    {
        if (string.IsNullOrWhiteSpace(value)) return null;
        string name = Path.GetFileName(value);
        return name.Length is > 0 and <= 255
            && name.IndexOfAny(Path.GetInvalidFileNameChars()) < 0
                ? name
                : null;
    }

    private void OpenProductWorkspaceWhenReady()
    {
        WorkspaceSessionV2 session = _workspaceSessions.Current;
        if (Volatile.Read(ref _closing) != 0 ||
            !_router.IsReady ||
            _runtime.CurrentBackend?.State != BackendState.Ready ||
            _productGateway is null ||
            _runtime.CurrentWorkspace?.WorkspaceId != session.WorkspaceId ||
            !ProductWorkspaceOpenPolicy.CanProject(session))
            return;
        _ = OpenProductWorkspaceAsync();
    }

    private async Task OpenProductWorkspaceAsync()
    {
        if (!await _workspaceOpenGate.WaitAsync(0)) return;
        try
        {
            string? source = await _databasePicker.PickDatabaseAsync();
            if (source is null)
                return;
            DatabaseOpenResult result = _workspaceSnapshot
                ?? await _workspace.OpenDatabaseAsync(source);
            _workspaceSnapshot = result;
            _coordinator.SetDatabase("local");
            _webBridge.PostNotification(
                "database.opened",
                new
                {
                    tables = result.Tables,
                    views = result.Views,
                    displayNames = result.DisplayNames,
                    projectKey = "local:default",
                    projectRevision = "1",
                    currentUser = new
                    {
                        id = "local-user",
                        displayName = "本地用户",
                    },
                    hostVersion = typeof(MainWindow).Assembly
                        .GetName().Version?.ToString() ?? "unknown",
                    features = new HostFeatureFlags(
                        _dashboardFeatures.Enabled,
                        _autoDateFeatures.Enabled),
                });
        }
        catch (Exception exception)
        {
            _readiness?.Trace(
                $"Product workspace open failed; " +
                $"exception={exception.GetType().Name}");
            _webBridge.PostOperationFailed(
                null,
                "本地工作区启动失败，请重试。",
                "PRODUCT_WORKSPACE_OPEN_FAILED");
        }
        finally
        {
            _workspaceOpenGate.Release();
        }
    }

    private void OnProductDataChanged(DataChangedEvent change)
    {
        _webBridge.PostNotification("data.changed", change);
    }

    private void OnProductTaskChanged(JsonElement change)
    {
        _webBridge.PostNotification("task.changed", change);
    }

    private void OnWorkspaceNotification(TableNotification notification)
    {
        if (notification.MutationResult is not null)
        {
            object? mutationPayload = notification.MutationResult.Success
                ? notification.MutationResult.Result
                : ToWebMutationError(notification.MutationResult.Error);
            if (string.IsNullOrWhiteSpace(notification.RequestId))
            {
                _webBridge.PostNotification(notification.Type, mutationPayload);
            }
            else
            {
                _webBridge.PostResponse(
                    notification.Type,
                    notification.RequestId,
                    mutationPayload);
            }
            return;
        }
        _webBridge.PostNotification(
            notification.Type,
            new
            {
                table = notification.Page?.Table,
                columns = notification.Page?.Columns,
                rows = notification.Page?.Rows,
                offset = notification.Page?.Offset,
                limit = notification.Page?.Limit,
                totalRows = notification.Page?.TotalRows,
                mode = notification.Page?.Mode,
                filteredRows = notification.Page?.FilteredRows,
                querySnapshot = notification.Page?.QuerySnapshot,
                revision = notification.Page?.Revision,
                groupRows = notification.Page?.GroupRows,
                groupOffset = notification.Page?.GroupOffset,
                groupLimit = notification.Page?.GroupLimit,
                hasMoreGroups = notification.Page?.HasMoreGroups,
                loadedRows = notification.LoadedRows,
            });
    }

    private static object? ToWebMutationError(MutationError? error)
    {
        if (error is null)
        {
            return null;
        }
        return new
        {
            kind = MutationErrorMapper.ToWireKind(error.Value.Kind),
            message = error.Value.Message,
            currentRow = error.Value.CurrentRow,
            conflictingRowKeys = error.Value.ConflictingRowKeys,
            fieldErrors = error.Value.FieldErrors,
        };
    }

    private async Task ImportDocumentsFromPickerAsync(JsonElement payload)
    {
        if (_documentWorkspace is null)
        {
            PostDocumentFailure(
                "文档工作区尚未连接。",
                "DOCUMENT_WORKSPACE_UNAVAILABLE");
            return;
        }
        try
        {
            WorkspaceDocumentImportResult? result = await _documentWorkspace
                .ImportFromPickerAsync(_session.Token);
            if (result is not null)
            {
                PostDocumentWorkspaceChanged("import", 1);
            }
        }
        catch (OperationCanceledException) { }
        catch (Exception exception)
        {
            PostDocumentFailure(exception, "DOCUMENT_IMPORT_FAILED");
        }
    }

    private async Task ImportDocumentsFromHostPathsAsync(
        JsonElement payload,
        IReadOnlyList<string> paths)
    {
        if (_documentWorkspace is null)
        {
            PostDocumentFailure(
                "文档工作区尚未连接。",
                "DOCUMENT_WORKSPACE_UNAVAILABLE");
            return;
        }
        int imported = 0;
        try
        {
            foreach (string path in paths.Take(100))
            {
                await _documentWorkspace.ImportFromHostPathAsync(
                    path,
                    _session.Token);
                imported++;
            }
            if (imported > 0)
            {
                PostDocumentWorkspaceChanged("import", imported);
            }
        }
        catch (OperationCanceledException) { }
        catch (Exception exception)
        {
            if (imported > 0)
            {
                PostDocumentWorkspaceChanged("import", imported);
            }
            PostDocumentFailure(exception, "DOCUMENT_IMPORT_FAILED");
        }
    }

    private async Task RelinkDocumentAsync(JsonElement payload)
    {
        if (_documentWorkspace is null)
        {
            PostDocumentFailure(
                "文档工作区尚未连接。",
                "DOCUMENT_WORKSPACE_UNAVAILABLE");
            return;
        }
        string? handle = ReadString(payload, "handle");
        if (string.IsNullOrWhiteSpace(handle))
        {
            PostDocumentFailure("缺少文档授权。", "BAD_PAYLOAD");
            return;
        }
        try
        {
            WorkspaceDocumentRelinkResult? result = await _documentWorkspace
                .RelinkFromPickerAsync(handle, _session.Token);
            if (result is not null)
            {
                PostDocumentWorkspaceChanged("relink", 1);
            }
        }
        catch (OperationCanceledException) { }
        catch (Exception exception)
        {
            PostDocumentFailure(exception, "DOCUMENT_RELINK_FAILED");
        }
    }

    private void DragDocumentOut(JsonElement payload)
    {
        if (_documentWorkspace is null)
        {
            PostDocumentFailure(
                "文档工作区尚未连接。",
                "DOCUMENT_WORKSPACE_UNAVAILABLE");
            return;
        }
        string? handle = ReadString(payload, "handle");
        if (string.IsNullOrWhiteSpace(handle))
        {
            PostDocumentFailure("缺少文档授权。", "BAD_PAYLOAD");
            return;
        }
        try
        {
            string path = _documentWorkspace.ResolveDragOutPath(handle);
            var data = new DataObject();
            data.SetData(DataFormats.FileDrop, new[] { path });
            DragDrop.DoDragDrop(AppWebView, data, DragDropEffects.Copy);
        }
        catch (Exception exception)
        {
            PostDocumentFailure(exception, "DOCUMENT_DRAG_OUT_FAILED");
        }
    }

    private void PostDocumentWorkspaceChanged(string reason, int affectedCount)
        => _webBridge.PostNotification(
            "document.workspaceChanged",
            new { reason, affectedCount });

    private void PostDocumentFailure(Exception exception, string fallbackCode)
    {
        string message = exception switch
        {
            DocumentFileOperationException value => value.Message,
            DocumentCapabilityException value => value.Message,
            _ => "文件操作未完成，请重试。",
        };
        string code = exception switch
        {
            DocumentFileOperationException value => value.Code,
            DocumentCapabilityException value => value.Code,
            _ => fallbackCode,
        };
        PostDocumentFailure(message, code);
    }

    private void PostDocumentFailure(string message, string code)
        => _webBridge.PostNotification(
            "document.operationFailed",
            new DocumentOperationFailedPayload(message, code));

    internal static DocumentOperationFailedPayload? ValidateExternalDropPaths(
        IReadOnlyList<string>? nativePaths)
        => nativePaths is null || nativePaths.Count == 0
            ? new DocumentOperationFailedPayload(
                "拖入请求没有携带原生文件对象，请使用“导入文件”按钮。",
                "DOCUMENT_DROP_OBJECTS_MISSING")
            : null;

    private static string? ReadString(JsonElement value, string name)
        => value.ValueKind == JsonValueKind.Object
            && value.TryGetProperty(name, out JsonElement item)
            && item.ValueKind == JsonValueKind.String
                ? item.GetString()
                : null;

    private static bool ReadRequiredBoolean(
        JsonElement value,
        string name)
    {
        if (value.ValueKind == JsonValueKind.Object
            && value.TryGetProperty(name, out JsonElement item)
            && item.ValueKind is JsonValueKind.True or JsonValueKind.False)
        {
            return item.GetBoolean();
        }
        throw new WorkspaceRegistryException(
            "workspace.request_invalid",
            $"{name} must be a boolean.");
    }

    private static string? ReadScalar(JsonElement value, string name)
    {
        if (value.ValueKind != JsonValueKind.Object
            || !value.TryGetProperty(name, out JsonElement item))
        {
            return null;
        }
        return item.ValueKind switch
        {
            JsonValueKind.String => item.GetString(),
            JsonValueKind.Number => item.GetRawText(),
            _ => null,
        };
    }

    private void OnViewModelPropertyChanged(
        object? sender,
        PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(MainWindowViewModel.State))
        {
            if (_viewModel.State == StartupState.Ready)
            {
                _viewModel.DetailMessage = string.Empty;
            }
            TryWriteReadiness();
        }
    }

    private void TryWriteReadiness()
    {
        if (_viewModel.State == StartupState.Ready
            && _router.IsReady)
        {
            _readiness?.WriteShellReady();
        }
    }

    private void OnClosed(object? sender, EventArgs args)
    {
        if (Interlocked.Exchange(ref _closing, 1) != 0) return;
        _testModeCloseControlTimer?.Dispose();
        _testModeCloseControlTimer = null;
        Application.Current.SessionEnding -= OnSessionEnding;
        _trayIcon?.Dispose();
        _session.Cancel();
        _router.IsReady = false;
        _runtime.ClientReady -= OnRuntimeClientReady;
        _runtime.RecoveryFailed -= OnRuntimeRecoveryFailed;
        _runtime.BindingChanged -= OnRuntimeBindingChanged;
        _workspaceSessions.Changed -= OnWorkspaceSessionChanged;
        _viewModel.PropertyChanged -= OnViewModelPropertyChanged;
        if (_productGateway is not null)
        {
            _dispatcher.ClearProductDataGateway(_productGateway);
            _productGateway.DataChanged -= OnProductDataChanged;
            _productGateway.TaskChanged -= OnProductTaskChanged;
            _productGateway.Dispose();
        }
        _pluginDispatcher.Dispose();
        _dailyQuotes.Dispose();
        _pluginGateway?.Dispose();
        _pluginResources.Dispose();
        _attachmentPreview.Dispose();
        _documentWorkspace?.Dispose();
        _tableGateway.Dispose();
        try
        {
            _replicaStatusMonitor.DisposeAsync().AsTask()
                .Wait(TimeSpan.FromSeconds(2));
        }
        catch
        {
            // Session-bound status polling is best-effort at shutdown.
        }
        _workspaceSessionFilter.Dispose();
        try
        {
            _snapshotPackages.DisposeAsync().AsTask().Wait(TimeSpan.FromSeconds(8));
        }
        catch
        {
            // Transient package runtime cleanup is best-effort at shutdown.
        }
        try
        {
            _workspaceSessions.DisposeAsync().AsTask().Wait(TimeSpan.FromSeconds(2));
        }
        catch
        {
            // Session teardown is best-effort during WPF shutdown.
        }
        try
        {
            _runtime.DisposeAsync().AsTask().Wait(TimeSpan.FromSeconds(8));
        }
        catch
        {
            // Process-tree cleanup is best-effort during WPF shutdown.
        }
        _workspaceOpenGate.Dispose();
        _session.Dispose();
        AppWebView.Dispose();
        try
        {
            Directory.Delete(_attachmentPreviewRoot, recursive: true);
        }
        catch (IOException)
        {
            // A preview handler may briefly retain a file handle during exit.
        }
        catch (UnauthorizedAccessException)
        {
            // Best-effort cleanup of the per-process preview cache.
        }
    }

    private sealed class SessionProductSourcePicker(
        WorkspaceSessionManager sessions) : IDatabasePicker
    {
        public Task<string?> PickDatabaseAsync()
            => Task.FromResult(
                sessions.Current.WorkspaceId is Guid workspaceId
                    ? $"local://workspace/{workspaceId:D}"
                    : null);
    }
}
