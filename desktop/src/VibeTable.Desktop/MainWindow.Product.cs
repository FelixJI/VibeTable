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
    private readonly BackendLaunchOptions _backendOptions;
    private readonly PocketBaseSupervisor _sidecar;
    private readonly LocalDataService _localData;
    private readonly ProductRuntimeService _runtime;
    private readonly WebMessageRouter _router;
    private readonly ProductWebViewBridge _webBridge;
    private readonly LazyProductTableGateway _tableGateway;
    private readonly TableWorkspaceService _workspace;
    private readonly GridStateCoordinator _coordinator;
    private readonly WorkspaceRequestDispatcher _dispatcher;
    private readonly PluginSurfaceSessionManager _pluginSurfaces;
    private readonly PluginWebViewResourceHost _pluginResources;
    private readonly PluginRequestDispatcher _pluginDispatcher;
    private readonly DailyQuoteHostClient _dailyQuotes;
    private readonly WorkspaceMountStore _workspaceMounts;
    private readonly ILocalDocumentFilePicker _documentFilePicker;
    private readonly ILocalDocumentPreview _attachmentPreview;
    private readonly string _attachmentPreviewRoot;
    private readonly string _productDataRoot;
    private readonly string _documentsDirectory;
    private readonly string _pocketBaseDataDirectory;
    private readonly string _pocketBaseVersion;
    private readonly DashboardFeatureOptions _dashboardFeatures;
    private readonly AutoDateFeatureOptions _autoDateFeatures;
    private readonly MainWindowViewModel _viewModel;
    private readonly TestModeReadinessWriter? _readiness;
    private readonly string? _e2eControlsDir;
    private readonly SemaphoreSlim _workspaceOpenGate = new(1, 1);
    private readonly CancellationTokenSource _session = new();

    private JsonRpcProductDataGateway? _productGateway;
    private JsonRpcPluginGateway? _pluginGateway;
    private DocumentWorkspaceHostService? _documentWorkspace;
    private DatabaseOpenResult? _workspaceSnapshot;
    private int _closing;

    public MainWindow()
    {
        InitializeComponent();

        HostStartupOptions startup = HostStartupOptions.Current();
        string localAppData = Environment.GetFolderPath(
            Environment.SpecialFolder.LocalApplicationData);
        _documentsDirectory = Environment.GetFolderPath(
            Environment.SpecialFolder.MyDocuments);
        if (string.IsNullOrWhiteSpace(_documentsDirectory))
        {
            _documentsDirectory = Path.Combine(
                Environment.GetFolderPath(
                    Environment.SpecialFolder.UserProfile),
                "Documents");
        }
        _e2eControlsDir = startup.TestMode
            && !string.IsNullOrWhiteSpace(startup.E2eControlsDir)
                ? Path.GetFullPath(startup.E2eControlsDir)
                : null;
        _readiness = startup.TestMode
            ? new TestModeReadinessWriter(startup.ReadinessDir)
            : null;
        _dashboardFeatures = DashboardFeatureOptions.FromEnvironment();
        _autoDateFeatures = AutoDateFeatureOptions.FromEnvironment();
        _backendOptions = BackendLaunchOptions.ResolveForHost();
        string? runtimeDataRoot = null;
        bool developmentDataRootRequested =
            !string.IsNullOrWhiteSpace(startup.DevelopmentDataRoot);
        if (startup.TestMode && developmentDataRootRequested)
        {
            throw new InvalidOperationException(
                "--dev-data-root cannot be combined with --test-mode.");
        }
        PocketBaseLaunchOptions sidecarOptions = PocketBaseHostOptions.Resolve(
            AppContext.BaseDirectory,
            localAppData);
        if (startup.TestMode && !string.IsNullOrWhiteSpace(startup.ReadinessDir))
        {
            runtimeDataRoot = Path.GetFullPath(
                Path.Combine(startup.ReadinessDir, "local-data"));
        }
        else if (developmentDataRootRequested)
        {
            if (!sidecarOptions.DevelopmentMode)
            {
                throw new InvalidOperationException(
                    "--dev-data-root is available only from a source layout.");
            }
            runtimeDataRoot = Path.GetFullPath(startup.DevelopmentDataRoot!);
        }
        else if (!startup.TestMode)
        {
            runtimeDataRoot = ProductDataRootManager.Resolve(
                _documentsDirectory,
                localAppData);
        }
        bool productionSession =
            !startup.TestMode && !developmentDataRootRequested;
        if (runtimeDataRoot is not null)
        {
            sidecarOptions = PocketBaseHostOptions.WithRuntimeDataRoot(
                sidecarOptions,
                runtimeDataRoot,
                productionSession
                    ? ProductDataRootManager.ResolvePocketBaseLogPath(
                        AppContext.BaseDirectory)
                    : null);
            ProductDataRootManager.ConfigureProcessEnvironment(
                _backendOptions.Environment,
                productionSession
                    ? ProductDataRootManager.ResolveRuntimeRoot(localAppData)
                    : runtimeDataRoot);
            _backendOptions.LogPath = ProductDataRootManager.ResolveSidecarLogPath(
                productionSession
                    ? AppContext.BaseDirectory
                    : runtimeDataRoot);
        }
        _productDataRoot = runtimeDataRoot
            ?? Path.Combine(
                Environment.GetFolderPath(
                    Environment.SpecialFolder.LocalApplicationData),
                "VibeTable");
        _pocketBaseDataDirectory = sidecarOptions.DataDirectory;
        _pocketBaseVersion = sidecarOptions.ExpectedIdentity?.PocketBaseVersion
            ?? "unknown";
        string attachmentCacheBase = productionSession
            ? ProductDataRootManager.ResolveCacheRoot(localAppData)
            : _productDataRoot;
        string attachmentPreviewSession =
            $"p{Environment.ProcessId:x}-{Guid.NewGuid():N}"[..17];
        _attachmentPreviewRoot = Path.Combine(
            attachmentCacheBase,
            "attachment-preview",
            attachmentPreviewSession);
        Directory.CreateDirectory(_attachmentPreviewRoot);
        _attachmentPreview = new ShellDocumentPreview();
        _sidecar = new PocketBaseSupervisor(sidecarOptions);
        _localData = new LocalDataService(_sidecar);
        var backend = new PythonBackendSupervisor(_backendOptions);
        _runtime = new ProductRuntimeService(
            _localData,
            _sidecar,
            backend,
            _backendOptions);

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
            productionSession
                ? Path.Combine(
                    ProductDataRootManager.ResolveCacheRoot(localAppData),
                    "webview2-user-data")
                : runtimeDataRoot is not null
                    ? Path.Combine(runtimeDataRoot, "webview2-user-data")
                    : null,
            stableIsolatedUserDataRoot:
                productionSession || runtimeDataRoot is not null);

        IPluginPackageSourcePicker pluginPackagePicker =
            _e2eControlsDir is null
                ? new WindowsPluginPackageSourcePicker()
                : new TestModePluginPackageSourcePicker(_e2eControlsDir);
        _pluginDispatcher = new PluginRequestDispatcher(
            _webBridge,
            _pluginSurfaces,
            pluginPackagePicker,
            _pluginResources,
            new WindowsPluginFilePicker());
        _dailyQuotes = new DailyQuoteHostClient();
        _workspaceMounts = new WorkspaceMountStore(runtimeDataRoot);
        _documentFilePicker = new WindowsLocalDocumentFilePicker();

        _tableGateway = new LazyProductTableGateway(backend);
        _workspace = new TableWorkspaceService(_tableGateway);
        _workspace.Notification += OnWorkspaceNotification;
        _coordinator = new GridStateCoordinator(
            _tableGateway,
            OnWorkspaceNotification);
        _dispatcher = new WorkspaceRequestDispatcher(
            _workspace,
            new FixedProductSourcePicker(),
            _webBridge,
            _coordinator,
            _dashboardFeatures,
            autoDateFeatures: _autoDateFeatures);

        _runtime.ClientReady += OnRuntimeClientReady;
        _runtime.RecoveryFailed += OnRuntimeRecoveryFailed;
        _viewModel = new MainWindowViewModel(_runtime, _webBridge);
        _viewModel.PropertyChanged += OnViewModelPropertyChanged;
        DataContext = _viewModel;

        Loaded += OnLoaded;
        Closed += OnClosed;
    }

    private async void OnLoaded(object sender, RoutedEventArgs args)
    {
        try
        {
            _readiness?.Trace("MainWindow: starting product runtime");
            await _viewModel.StartAsync();
            if (_viewModel.State == StartupState.Faulted)
            {
                Exception? error = _viewModel.LastStartupError;
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
        JsonRpcClient client = _runtime.Backend.Client
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
        _documentWorkspace = new DocumentWorkspaceHostService(
            new JsonRpcDocumentWorkspaceGateway(client),
            _workspaceMounts,
            new DocumentCapabilityStore(),
            new WindowsLocalDocumentActions(),
            filePicker: _documentFilePicker,
            partitionKey: "local:default|user:local");
        _dispatcher.SetDocumentWorkspace(_documentWorkspace);

        _workspaceSnapshot = null;
        if (_router.IsReady)
        {
            _ = OpenProductWorkspaceAsync();
        }
    }

    private void OnRuntimeRecoveryFailed(Exception exception)
    {
        _readiness?.WriteError(
            $"Local data recovery failed: {exception.GetType().Name}");
        Dispatcher.BeginInvoke(() =>
        {
            if (_viewModel.State is StartupState.LoadingWeb or StartupState.Ready)
            {
                _viewModel.MoveToFaulted("Local data recovery failed.");
            }
        });
    }

    private void OnRendererProcessFailed(string reason)
    {
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
            _webBridge.PostNotification(
                "host.startupStateChanged",
                new
                {
                    phase = "ready",
                    title = "已就绪",
                    detail = "本地数据服务已就绪。",
                    canRetry = false,
                    canCancel = false,
                });
            _ = OpenProductWorkspaceAsync();
            TryWriteReadiness();
            return;
        }
        if (request.Type == "host.startupCancelRequested")
        {
            Dispatcher.BeginInvoke(Close);
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
        if (request.Type == "backup.openFolder")
        {
            if (!HasEmptyObjectPayload(request))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "The backup-folder request is invalid.",
                    "BACKUP_FOLDER_BAD_PAYLOAD");
                return;
            }
            try
            {
                string backupDirectory = Path.Combine(
                    _pocketBaseDataDirectory,
                    "backups");
                Directory.CreateDirectory(backupDirectory);
                Process.Start(new ProcessStartInfo(backupDirectory)
                {
                    UseShellExecute = true,
                });
                _webBridge.PostResponse(
                    request.Type,
                    request.RequestId,
                    new { status = "opened" });
            }
            catch (Exception exception)
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    exception.Message,
                    "BACKUP_FOLDER_OPEN_FAILED");
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
                    pocketBaseVersion = _pocketBaseVersion,
                    memoryBytes = process.WorkingSet64,
                    dataServiceState = _localData.GetStatus().State.ToString(),
                });
            return;
        }
        if (request.Type == "dailyQuote.fetch")
        {
            _ = FetchDailyQuoteAsync(request);
            return;
        }
        if (request.Type == "dataRoot.get")
        {
            if (!HasEmptyObjectPayload(request))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "The data-root status request is invalid.",
                    "DATA_ROOT_BAD_PAYLOAD");
                return;
            }
            _webBridge.PostResponse(
                request.Type,
                request.RequestId,
                ProductDataRootManager.GetStatus(
                    _productDataRoot,
                    _documentsDirectory));
            return;
        }
        if (request.Type == "dataRoot.chooseMigrationRequested")
        {
            if (!HasEmptyObjectPayload(request))
            {
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    "The data-root migration request is invalid.",
                    "DATA_ROOT_BAD_PAYLOAD");
                return;
            }
            try
            {
                _webBridge.PostResponse(
                    request.Type,
                    request.RequestId,
                    ProductDataRootManager.ChooseAndScheduleMigration(
                        _productDataRoot));
            }
            catch (Exception exception)
            {
                _readiness?.Trace(
                    $"Data-root migration selection failed; " +
                    $"exception={exception.GetType().Name}");
                _webBridge.PostOperationFailed(
                    request.RequestId,
                    exception.Message,
                    "DATA_ROOT_MIGRATION_REJECTED");
            }
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
        if (request.Type is
            "document.commitRevisionRequested"
            or "document.promoteVersionRequested"
            or "document.revisionPreviewRequested"
            or "document.revisionRestoreRequested"
            or "document.schemeListRequested"
            or "document.schemeCreateRequested"
            or "document.schemeRenameRequested"
            or "document.schemeArchiveRequested"
            or "document.schemeActivateRequested")
        {
            _ = HandleDocumentVersionRequestAsync(request);
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

    private async Task HandleDocumentVersionRequestAsync(RoutedWebRequest request)
    {
        if (_documentWorkspace is null)
        {
            _webBridge.PostOperationFailed(
                request.RequestId,
                "The document workspace is not connected.",
                "DOCUMENT_WORKSPACE_UNAVAILABLE");
            return;
        }

        try
        {
            object result;
            string responseType;
            string? changedReason = null;
            switch (request.Type)
            {
                case "document.commitRevisionRequested":
                {
                    var payload = ReadDocumentPayload<DocumentCommitRevisionRequestedPayload>(
                        request.Payload);
                    result = await _documentWorkspace.CommitRevisionAsync(
                        payload.EntryHandle,
                        payload.Note,
                        payload.SchemeHandle,
                        _session.Token).ConfigureAwait(false);
                    responseType = "document.versionCommitted";
                    changedReason = "revision";
                    break;
                }
                case "document.promoteVersionRequested":
                {
                    var payload = ReadDocumentPayload<DocumentPromoteVersionRequestedPayload>(
                        request.Payload);
                    result = await _documentWorkspace.PromoteVersionAsync(
                        payload.EntryHandle,
                        payload.VersionLabel,
                        payload.Note,
                        payload.SchemeHandle,
                        _session.Token).ConfigureAwait(false);
                    responseType = "document.versionCommitted";
                    changedReason = "revision";
                    break;
                }
                case "document.revisionPreviewRequested":
                {
                    var payload = ReadDocumentPayload<DocumentRevisionHandleRequest>(
                        request.Payload);
                    result = _documentWorkspace.PreviewRevision(
                        payload.EntryHandle,
                        payload.RevisionHandle);
                    responseType = "document.revisionPreviewCompleted";
                    break;
                }
                case "document.revisionRestoreRequested":
                {
                    var payload = ReadDocumentPayload<DocumentRevisionHandleRequest>(
                        request.Payload);
                    result = await _documentWorkspace.RestoreRevisionAsync(
                        payload.EntryHandle,
                        payload.RevisionHandle,
                        _session.Token).ConfigureAwait(false);
                    responseType = "document.versionCommitted";
                    changedReason = "restore";
                    break;
                }
                case "document.schemeListRequested":
                {
                    var payload = ReadDocumentPayload<DocumentEntryHandleRequest>(
                        request.Payload);
                    result = _documentWorkspace.ListSchemes(payload.EntryHandle);
                    responseType = "document.schemeListLoaded";
                    break;
                }
                case "document.schemeCreateRequested":
                {
                    var payload = ReadDocumentPayload<DocumentSchemeCreateRequestedPayload>(
                        request.Payload);
                    result = await _documentWorkspace.CreateSchemeAsync(
                        payload.EntryHandle,
                        payload.Name,
                        payload.BaseRevisionHandle,
                        _session.Token).ConfigureAwait(false);
                    responseType = "document.schemeMutationCompleted";
                    changedReason = "scheme";
                    break;
                }
                case "document.schemeRenameRequested":
                {
                    var payload = ReadDocumentPayload<DocumentSchemeRenameRequestedPayload>(
                        request.Payload);
                    result = await _documentWorkspace.RenameSchemeAsync(
                        payload.EntryHandle,
                        payload.SchemeHandle,
                        payload.Name,
                        _session.Token).ConfigureAwait(false);
                    responseType = "document.schemeMutationCompleted";
                    changedReason = "scheme";
                    break;
                }
                case "document.schemeArchiveRequested":
                {
                    var payload = ReadDocumentPayload<DocumentSchemeHandleRequest>(
                        request.Payload);
                    result = await _documentWorkspace.ArchiveSchemeAsync(
                        payload.EntryHandle,
                        payload.SchemeHandle,
                        _session.Token).ConfigureAwait(false);
                    responseType = "document.schemeMutationCompleted";
                    changedReason = "scheme";
                    break;
                }
                case "document.schemeActivateRequested":
                    _ = ReadDocumentPayload<DocumentSchemeHandleRequest>(
                        request.Payload);
                    throw new DocumentFileOperationException(
                        "Activating a scheme is not supported because the workspace has no persistent active-scheme model.",
                        "NOT_SUPPORTED");
                default:
                    return;
            }

            _webBridge.PostResponse(responseType, request.RequestId, result);
            if (changedReason is not null)
                PostDocumentWorkspaceChanged(changedReason, 1);
        }
        catch (OperationCanceledException) when (_session.IsCancellationRequested)
        {
        }
        catch (Exception exception)
        {
            DocumentOperationFailedPayload failure = exception switch
            {
                DocumentFileOperationException value =>
                    new DocumentOperationFailedPayload(value.Message, value.Code),
                DocumentCapabilityException value =>
                    new DocumentOperationFailedPayload(value.Message, value.Code),
                _ => new DocumentOperationFailedPayload(
                    "The document version operation did not complete.",
                    "DOCUMENT_VERSION_OPERATION_FAILED"),
            };
            _webBridge.PostOperationFailed(
                request.RequestId,
                failure.Message,
                failure.Code);
            PostDocumentFailure(
                failure.Message,
                failure.Code ?? "DOCUMENT_VERSION_OPERATION_FAILED");
        }
    }

    private static T ReadDocumentPayload<T>(JsonElement payload)
        where T : class
    {
        try
        {
            return payload.Deserialize<T>()
                ?? throw new JsonException("Document request payload is empty.");
        }
        catch (JsonException)
        {
            throw new DocumentFileOperationException(
                "The document request payload is invalid.",
                "BAD_PAYLOAD");
        }
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
        // The preview host only needs the extension to select a handler. Do
        // not carry the original name into the temporary path: evidence roots
        // and user filenames can otherwise cross Windows' legacy MAX_PATH
        // boundary in the packaged Python process.
        string previewExtension = Path.GetExtension(suggested);
        if (previewExtension.Length > 16)
        {
            previewExtension = string.Empty;
        }
        string previewPath = Path.Combine(
            _attachmentPreviewRoot,
            $"{Guid.NewGuid():N}{previewExtension}");
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
        catch (Exception exception)
        {
            _readiness?.Trace(
                $"Attachment preview failed; type={exception.GetType().Name}; message={exception.Message}");
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

    private async Task OpenProductWorkspaceAsync()
    {
        if (!await _workspaceOpenGate.WaitAsync(0)) return;
        try
        {
            DatabaseOpenResult result = _workspaceSnapshot
                ?? await _workspace.OpenDatabaseAsync("local://default");
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
            DocumentImportRequest request = CreateDocumentImportRequest(payload);
            DocumentImportResult? result = await _documentWorkspace
                .ImportFromPickerAsync(request, _session.Token);
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
            DocumentImportRequest request = CreateDocumentImportRequest(payload);
            foreach (string path in paths.Take(100))
            {
                await _documentWorkspace.ImportFromHostPathAsync(
                    request,
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
            DocumentRelinkResult? result = await _documentWorkspace
                .RelinkMissingFromPickerAsync(handle, _session.Token);
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

    private DocumentImportRequest CreateDocumentImportRequest(JsonElement payload)
    {
        var selection = new ManagedWorkspaceProvisioner(
            _workspaceMounts,
            "local:default|user:local").EnsurePreferred();
        JsonElement scope = payload.ValueKind == JsonValueKind.Object
            && payload.TryGetProperty("scope", out JsonElement value)
                ? value
                : default;
        string kind = ReadString(scope, "kind") ?? "global";
        if (kind == "global")
        {
            return new DocumentImportRequest(selection.WorkspaceId, FolderId: null);
        }
        if (kind != "record")
        {
            throw new DocumentFileOperationException(
                "未知文件范围。",
                "BAD_PAYLOAD");
        }
        string? collection = ReadString(scope, "collection");
        string? itemId = ReadScalar(scope, "itemId");
        if (string.IsNullOrWhiteSpace(collection)
            || string.IsNullOrWhiteSpace(itemId))
        {
            throw new DocumentFileOperationException(
                "记录文件范围缺少必要信息。",
                "BAD_PAYLOAD");
        }
        return new DocumentImportRequest(
            selection.WorkspaceId,
            FolderId: null,
            ItemCollection: collection,
            ItemId: itemId,
            LinkType: "attachment");
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
        if (_runtime.Backend.State == BackendState.Ready
            && _viewModel.State == StartupState.Ready
            && _router.IsReady)
        {
            _readiness?.WriteShellReady();
        }
    }

    private void OnClosed(object? sender, EventArgs args)
    {
        if (Interlocked.Exchange(ref _closing, 1) != 0) return;
        _session.Cancel();
        _router.IsReady = false;
        _runtime.ClientReady -= OnRuntimeClientReady;
        _runtime.RecoveryFailed -= OnRuntimeRecoveryFailed;
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

    private sealed class FixedProductSourcePicker : IDatabasePicker
    {
        public Task<string?> PickDatabaseAsync()
            => Task.FromResult<string?>("local://default");
    }
}
