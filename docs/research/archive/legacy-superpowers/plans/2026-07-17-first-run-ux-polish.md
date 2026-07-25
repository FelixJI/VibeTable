> 历史实施计划归档；不属于当前产品实现。

# 首次启动体验打磨 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让失败恢复时的 npm 包校验在 UI 上明确可见；把顶部"Ready"状态条移到底部并合并启动日志职责；缩小窗口标题字号。

**Architecture:** 三处独立、可分别提交的改动：(1) 新增 `RecheckingPackages` 阶段枚举值，把"失败恢复强制校验"从普通 `VerifyingPackages` 中区分出来，让它在启动窗口有独立标题；(2) 把 `MainWindow` 顶部状态条移到 `Grid.Row="1"`（底部），字号缩到 11、不加粗、灰字、中文文案；ViewModel 新增 `DetailMessage` 属性，宿主把后端/Directus 的最新进度行路由进来；(3) 三个窗口标题字号从 24 缩到 18。

**Tech Stack:** .NET 10 / WPF / XAML / MSTest。仓库根：`C:\Users\felji\PycharmProjects\vibetable`。桌面 sln：`desktop/VibeTable.Desktop.sln`。本机 `dotnet` 在 `C:\Users\felji\.dotnet\dotnet.exe`（注意 Program Files (x86) 下的是错的、找不到 SDK）。

## Global Constraints

- **dotnet 路径：** 必须用 `C:\Users\felji\.dotnet\dotnet.exe`（简记 `$DOTNET`）。Program Files (x86) 下的 `dotnet` 指向 x86 host，找不到 SDK，会失败。
- **文案语言：** 新增/修改的用户可见字符串全部用中文（与既有欢迎/启动/登录窗口一致）。
- **构建目标：** 每个 .csproj 都是 `net10.0-windows`，构建命令 `dotnet build`。Windows SDK 元数据读取在本机沙箱可能受限；如果 `dotnet build` 因权限失败，记录失败但继续（静态改动已可人工核验），不要尝试提权绕过。
- **TDD：** 每个有逻辑改动的任务先写失败测试，再改实现。纯 XAML 字号/布局改动无逻辑，按"改 → build → 提交"。
- **提交粒度：** 一个任务一个提交。提交信息前缀用 `feat(desktop)` / `feat(infra)` / `style(desktop)`。
- **默认分支：** 当前在 `main`。计划每个任务单独提交到 `main`（与既有工作流一致；不新建分支）。
- **不要改：** Directus 启动顺序、后端启动时机、npm 校验判定逻辑（保持 VerifyAsync + 失败才 npm ci）、web-grid 内部的 `#status` 行、`DirectusStartupWindow` 的整体步骤/日志框。

---

## File Structure

| 文件 | 责任 | 本次改动 |
|---|---|---|
| `desktop/src/VibeTable.Infrastructure/Directus/DirectusStartupProgress.cs` | 启动阶段枚举 + 进度记录 | 枚举新增 `RecheckingPackages` |
| `desktop/src/VibeTable.Infrastructure/Directus/DirectusPackageManager.cs` | npm 安装/校验/标记 | 失败恢复强制校验分支改发 `RecheckingPackages` |
| `desktop/tests/VibeTable.Infrastructure.Tests/Directus/DirectusPackageManagerTests.cs` | 包管理器测试 | 更新断言期望 `RecheckingPackages`；新增可见性测试 |
| `desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml.cs` | 启动窗口 step/title 映射 + 重试 hint | 映射 `RecheckingPackages` 到独立标题；改重试 hint |
| `desktop/src/VibeTable.Desktop/ViewModels/MainWindowViewModel.cs` | 主窗口 VM 状态/文案 | `StatusText` 中文化；新增 `DetailMessage` |
| `desktop/src/VibeTable.Desktop/MainWindow.xaml` | 主窗口布局 | 顶部状态条移到底部、字号 11、不加粗、灰字、显示 DetailMessage |
| `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs` | 主窗口 code-behind、宿主进度路由 | 把后端/Directus/宿主进度写入 `_viewModel.DetailMessage` |
| `desktop/tests/VibeTable.Desktop.Tests/MainWindowViewModelTests.cs` | VM 测试 | 中文化断言更新；新增 DetailMessage 测试 |
| `desktop/src/VibeTable.Desktop/DirectusFirstRunWindow.xaml` | 欢迎窗口 | 标题 24 → 18 |
| `desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml` | 启动窗口 | 横幅主标题 24 → 18；副标题 15 → 13 |
| `desktop/src/VibeTable.Desktop/DirectusLoginWindow.xaml` | 登录窗口 | 标题 24 → 18 |

---

### Task 1: 新增 `RecheckingPackages` 阶段枚举值

**Files:**
- Modify: `desktop/src/VibeTable.Infrastructure/Directus/DirectusStartupProgress.cs`

**Interfaces:**
- Produces: 枚举值 `DirectusStartupStage.RecheckingPackages`（数值位于 `RepairingPackages`(4) 之后、`InitializingDatabase`(5) 之前）。任务 2、3、5 依赖它存在。

**注意：** 这是纯枚举新增，既有代码无 `switch` 穷尽性检查会因此报错（C# 枚举不强制穷尽），所以这一步只需添加枚举值。后续任务会消费它。

- [ ] **Step 1: 添加枚举值**

打开 `desktop/src/VibeTable.Infrastructure/Directus/DirectusStartupProgress.cs`，把枚举改成（在 `RepairingPackages = 4` 和 `InitializingDatabase = 5` 之间插入新值，并把后续数值重排）：

```csharp
public enum DirectusStartupStage
{
    PreparingRuntime = 0,
    CheckingPackages = 1,
    InstallingPackages = 2,
    VerifyingPackages = 3,
    RepairingPackages = 4,
    /// <summary>
    /// A previous first-run attempt did not complete; the existing install on
    /// disk is being fully re-verified (structure + native modules + lock
    /// hash) before reuse. Distinct from <see cref="VerifyingPackages"/> so the
    /// UI can surface that a forced recheck happened after a failed init.
    /// </summary>
    RecheckingPackages = 5,
    InitializingDatabase = 6,
    StartingService = 7,
    WaitingForService = 8,
    ApplyingSchema = 9,
    Ready = 10,
}
```

- [ ] **Step 2: 构建确认编译通过**

Run: `$DOTNET build desktop/src/VibeTable.Infrastructure/VibeTable.Infrastructure.csproj`
Expected: 成功（0 错误）。可能有已存在的警告，无需处理。

- [ ] **Step 3: 提交**

```bash
git add desktop/src/VibeTable.Infrastructure/Directus/DirectusStartupProgress.cs
git commit -m "feat(infra): add RecheckingPackages startup stage enum"
```

---

### Task 2: 失败恢复强制校验改发 `RecheckingPackages`（TDD）

**Files:**
- Modify: `desktop/tests/VibeTable.Infrastructure.Tests/Directus/DirectusPackageManagerTests.cs:154-186`
- Modify: `desktop/src/VibeTable.Infrastructure/Directus/DirectusPackageManager.cs:143-158`

**Interfaces:**
- Consumes: `DirectusStartupStage.RecheckingPackages`（任务 1）
- Produces: `DirectusPackageManager.EnsureInstalledAsync` 在 `forceFullVerification && hasExistingInstall` 分支发出 `RecheckingPackages` 事件（而非 `VerifyingPackages`）。

**背景：** 当前 `DirectusPackageManager.cs:143-158` 在"上次未完成、对已存在安装做强制复检"分支里，发出的是 `VerifyingPackages`，detail 是 `"The previous initialization did not finish; rechecking all package files and native modules."`。这与普通"安装后校验"用的是同一个 stage，用户在 UI 上区分不出。改发新的 `RecheckingPackages`，detail 改成中文（窗口已有 `TranslateDetail`，但 detail 是英文 key，翻译表在任务 3 同步更新；这里先写英文 key，任务 3 加翻译）。

测试改动逻辑：`EnsureInstalled_DoesNotTrustFreshMarker_WhenFirstRunIncomplete`（L154-186）当前断言进度列表包含 `InstallingPackages`（因为测试环境没有 `node_modules/directus`，所以走不到 L143-158 的 verify-existing 分支，而是落到 L160 的 npm ci）。**这个测试不会被本次改动影响**（它本就走 install 分支）。所以我们**不改这个测试**，而是**新增**两个测试覆盖 verify-existing 分支。

- [ ] **Step 1: 新增失败测试——强制校验已存在安装时发出 RecheckingPackages**

在 `DirectusPackageManagerTests.cs` 的 `EnsureInstalled_DoesNotTrustFreshMarker_WhenFirstRunIncomplete` 测试方法之后（L186 之后、`EnsureInstalled_DoesNotSkip_WhenMarkerExpired` 之前），新增以下两个测试方法。注意复用文件已有的 helper：`BundledNodePath()`、`ComputeLockHashFor(dir)`、`WriteMarker(dir, payload)`、`WithTemporaryDirectory(body)`。

```csharp
/// <summary>
/// When forceFullVerification is set AND an existing install is present
/// (node_modules/directus exists), EnsureInstalled must emit the
/// RecheckingPackages stage (not VerifyingPackages) so the UI can show the
/// user that a forced re-verification happened after a failed first run.
/// The existing install here is a stub directory; VerifyAsync will fail
/// (no real node_modules contents), so we only assert the stage event was
/// raised before the method threw.
/// </summary>
[TestMethod]
public async Task EnsureInstalled_EmitsRecheckingPackages_WhenForcedAndInstallExists()
{
    WithTemporaryDirectory(dir =>
    {
        File.WriteAllText(Path.Combine(dir, "package-lock.json"), "{\"name\":\"x\"}");
        // Stub an existing install so the verify-existing branch is taken.
        Directory.CreateDirectory(Path.Combine(dir, "node_modules", "directus"));
        var progress = new List<DirectusStartupProgress>();
        var manager = new DirectusPackageManager(npmTimeout: TimeSpan.FromSeconds(5));

        // The stub install will fail verification (no real contents) and fall
        // through to npm ci, which also fails in the unit fixture. We expect a
        // throw, but BEFORE throwing, RecheckingPackages must have been emitted.
        Assert.Throws<InvalidOperationException>(() =>
            manager.EnsureInstalledAsync(
                    BundledNodePath(),
                    dir,
                    CancellationToken.None,
                    progress.Add,
                    logLine: null,
                    forceFullVerification: true)
                .GetAwaiter().GetResult());

        CollectionAssert.Contains(
            progress.ConvertAll(item => item.Stage),
            DirectusStartupStage.RecheckingPackages,
            "a forced recheck of an existing install must emit RecheckingPackages, " +
            "not the ordinary VerifyingPackages stage");
    });
    await Task.CompletedTask;
}

/// <summary>
/// Sanity: when forceFullVerification is set but NO existing install is on
/// disk, the verify-existing branch is skipped and RecheckingPackages is NOT
/// emitted (the install path runs instead). This guards against accidentally
/// emitting RecheckingPackages from the wrong branch.
/// </summary>
[TestMethod]
public async Task EnsureInstalled_DoesNotEmitRecheckingPackages_WhenNoExistingInstall()
{
    WithTemporaryDirectory(dir =>
    {
        File.WriteAllText(Path.Combine(dir, "package-lock.json"), "{\"name\":\"x\"}");
        var progress = new List<DirectusStartupProgress>();
        var manager = new DirectusPackageManager(npmTimeout: TimeSpan.FromSeconds(5));

        Assert.Throws<InvalidOperationException>(() =>
            manager.EnsureInstalledAsync(
                    BundledNodePath(),
                    dir,
                    CancellationToken.None,
                    progress.Add,
                    logLine: null,
                    forceFullVerification: true)
                .GetAwaiter().GetResult());

        CollectionAssert.DoesNotContain(
            progress.ConvertAll(item => item.Stage),
            DirectusStartupStage.RecheckingPackages,
            "RecheckingPackages must only fire when an existing install is being " +
            "force-rechecked, not on a fresh install path");
    });
    await Task.CompletedTask;
}
```

- [ ] **Step 2: 运行新测试确认失败**

Run: `$DOTNET test desktop/tests/VibeTable.Infrastructure.Tests/VibeTable.Infrastructure.Tests.csproj --filter "FullyQualifiedName~EnsureInstalled_EmitsRecheckingPackages_WhenForcedAndInstallExists|FullyQualifiedName~EnsureInstalled_DoesNotEmitRecheckingPackages_WhenNoExistingInstall"`
Expected: 第一个测试 FAIL（`CollectionAssert.Contains` 失败，因为当前发的是 `VerifyingPackages` 不是 `RecheckingPackages`）。第二个测试 PASS（因为当前根本不发 `RecheckingPackages`）。

- [ ] **Step 3: 改实现——把强制校验分支改发 RecheckingPackages**

打开 `desktop/src/VibeTable.Infrastructure/Directus/DirectusPackageManager.cs`，定位 L143-158（`if (forceFullVerification && hasExistingInstall)` 块）。把块内的 progress 调用从：

```csharp
progress?.Invoke(new DirectusStartupProgress(
    DirectusStartupStage.VerifyingPackages,
    "The previous initialization did not finish; rechecking all package files and native modules."));
```

改为：

```csharp
progress?.Invoke(new DirectusStartupProgress(
    DirectusStartupStage.RecheckingPackages,
    "The previous initialization did not finish; rechecking all package files and native modules."));
```

（只改 `DirectusStartupStage.VerifyingPackages` → `DirectusStartupStage.RecheckingPackages`，detail 字符串保持不变，任务 3 会在翻译表里加中文映射。）

- [ ] **Step 4: 运行测试确认通过**

Run: `$DOTNET test desktop/tests/VibeTable.Infrastructure.Tests/VibeTable.Infrastructure.Tests.csproj --filter "FullyQualifiedName~EnsureInstalled_EmitsRecheckingPackages_WhenForcedAndInstallExists|FullyQualifiedName~EnsureInstalled_DoesNotEmitRecheckingPackages_WhenNoExistingInstall"`
Expected: 两个测试都 PASS。

- [ ] **Step 5: 运行整个 Infrastructure 测试套件确认无回归**

Run: `$DOTNET test desktop/tests/VibeTable.Infrastructure.Tests/VibeTable.Infrastructure.Tests.csproj`
Expected: 全绿（之前的 `EnsureInstalled_DoesNotTrustFreshMarker_WhenFirstRunIncomplete` 仍 PASS，因为它走 install 分支不涉及本次改动）。

- [ ] **Step 6: 提交**

```bash
git add desktop/src/VibeTable.Infrastructure/Directus/DirectusPackageManager.cs desktop/tests/VibeTable.Infrastructure.Tests/Directus/DirectusPackageManagerTests.cs
git commit -m "feat(infra): emit RecheckingPackages stage on forced re-verify of existing install"
```

---

### Task 3: 启动窗口映射 `RecheckingPackages` 并更新重试提示文案

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml.cs:54-83,123`

**Interfaces:**
- Consumes: `DirectusStartupStage.RecheckingPackages`（任务 1）
- Produces: 启动窗口在收到 `RecheckingPackages` 时显示独立中文标题"检测到上次未完成，正在复检 Directus 依赖"；重试 hint 说明"已安装文件会被重新校验，校验失败才会重装"。

**背景：** `DirectusStartupWindow.xaml.cs:54-83` 的 `UpdateProgress` 里有两个 switch：一个 `step`（映射到步骤列表的哪一行）、一个 `title`（横幅副标题）。当前 `CheckingPackages/InstallingPackages/VerifyingPackages/RepairingPackages` 都映射到 step 1。`RecheckingPackages` 也应映射到 step 1（仍是"安装并校验 Directus"这一步位置），但 title 独立。`TranslateDetail`（L235-269）是 detail 文案的中文翻译表，需要加一条新 key。

- [ ] **Step 1: 在 `UpdateProgress` 的两个 switch 中加 `RecheckingPackages` 分支**

打开 `desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml.cs`，定位 `UpdateProgress` 方法（L48-85）。在第一个 switch（`progress.Stage switch { ... }` 算 step，L54-67）里，当前 `CheckingPackages or InstallingPackages or VerifyingPackages or RepairingPackages => 1`。把 `RecheckingPackages` 也加入这一组（它仍归 step 1）。改为：

```csharp
int step = progress.Stage switch
{
    DirectusStartupStage.PreparingRuntime => 0,
    DirectusStartupStage.CheckingPackages
        or DirectusStartupStage.InstallingPackages
        or DirectusStartupStage.VerifyingPackages
        or DirectusStartupStage.RepairingPackages
        or DirectusStartupStage.RecheckingPackages => 1,
    DirectusStartupStage.InitializingDatabase => 2,
    DirectusStartupStage.StartingService
        or DirectusStartupStage.WaitingForService => 3,
    DirectusStartupStage.ApplyingSchema
        or DirectusStartupStage.Ready => 4,
    _ => _currentStep,
};
```

在第二个 switch（`progress.Stage switch { ... }` 算 title，L68-83）里，在 `RepairingPackages` 分支之后插入新分支：

```csharp
string title = progress.Stage switch
{
    DirectusStartupStage.PreparingRuntime => "准备本地运行环境",
    DirectusStartupStage.CheckingPackages => "检查 Directus 依赖",
    DirectusStartupStage.InstallingPackages => "安装 Directus 依赖",
    DirectusStartupStage.VerifyingPackages => progress.UsedFastPath
        ? "依赖已安装，无需重复安装"
        : "校验 Directus 依赖",
    DirectusStartupStage.RepairingPackages => "修复 Directus 依赖",
    DirectusStartupStage.RecheckingPackages => "检测到上次未完成，正在复检 Directus 依赖",
    DirectusStartupStage.InitializingDatabase => "初始化本地数据库",
    DirectusStartupStage.StartingService => "启动 Directus 服务",
    DirectusStartupStage.WaitingForService => "等待 Directus 就绪",
    DirectusStartupStage.ApplyingSchema => "创建 VibeTable 数据结构",
    DirectusStartupStage.Ready => "Directus 已就绪",
    _ => "正在初始化",
};
```

- [ ] **Step 2: 在 `TranslateDetail` 翻译表加新 key**

同一文件，定位 `TranslateDetail`（L235-269）。在 `"The previous initialization did not finish; rechecking all package files and native modules."` 那条（L253-254）的翻译改成更清晰的说明：

```csharp
"The previous initialization did not finish; rechecking all package files and native modules." =>
    "检测到上次初始化未完成，正在重新校验全部包文件和原生模块（结构、原生模块、lock hash）。",
```

（这条 key 字符串没变，只是把右边的中文译文写得更明确，列出三项校验内容。）

- [ ] **Step 3: 改重试 hint 文案**

同一文件，定位 `ResetForRetry`（L110-127）。把 L123 的：

```csharp
HintText.Text = "正在重试；已完成的安装文件会继续复用。";
```

改为：

```csharp
HintText.Text = "正在重试。已安装的依赖会被重新校验（结构、原生模块、lock hash）；仅在校验失败时才会重装。";
```

- [ ] **Step 4: 构建确认编译通过**

Run: `$DOTNET build desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj`
Expected: 成功（0 错误）。

- [ ] **Step 5: 提交**

```bash
git add desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml.cs
git commit -m "feat(desktop): surface RecheckingPackages stage with dedicated title and clearer retry hint"
```

---

### Task 4: ViewModel `StatusText` 中文化 + 新增 `DetailMessage`（TDD）

**Files:**
- Modify: `desktop/tests/VibeTable.Desktop.Tests/MainWindowViewModelTests.cs:52,66,97-98,99`
- Modify: `desktop/src/VibeTable.Desktop/ViewModels/MainWindowViewModel.cs:83-102`

**Interfaces:**
- Produces: `MainWindowViewModel.DetailMessage`（string，可读写，set 时 `RaisePropertyChanged(nameof(DetailMessage))`）；`StatusText` 改中文文案。任务 5 的 XAML 绑定 `DetailMessage`，任务 6 的 code-behind 写入 `DetailMessage`。

**背景：** 现有测试 L41、L52、L66 硬编码了英文 `"Loading web"` / `"Ready"` / `"Faulted"`，本次中文化必须同步改这些断言。`DetailMessage` 是一个简单的可绑定 string 属性，不参与状态机转换（外部任意赋值，Ready 时由 code-behind 清空——见任务 6；VM 本身不在 `TransitionTo` 里清空，因为 VM 不知道"宿主进度"语义）。

- [ ] **Step 1: 改测试断言为中文字符串**

打开 `desktop/tests/VibeTable.Desktop.Tests/MainWindowViewModelTests.cs`。

L41（`Ctor_StartsInStartingBackend_AndTransitionsToReady_HappyPath`，LoadingWeb 阶段）：
```csharp
Assert.AreEqual("Loading web", vm.StatusText);
```
改为：
```csharp
Assert.AreEqual("正在加载界面…", vm.StatusText);
```

L52（同一测试，Ready 阶段）：
```csharp
Assert.AreEqual("Ready", vm.StatusText);
```
改为：
```csharp
Assert.AreEqual("就绪", vm.StatusText);
```

L66（`BackendFailure_InStarting_MovesToFaulted`）：
```csharp
Assert.AreEqual("Faulted", vm.StatusText);
```
改为：
```csharp
Assert.AreEqual("出现错误", vm.StatusText);
```

- [ ] **Step 2: 新增 DetailMessage 失败测试**

在同一测试文件末尾（`RetryCommand_IsNull_OrDisabled_WhenNotFaulted` 测试之后、`CreateAndStartAsync` helper 之前，约 L163 处），新增测试：

```csharp
[TestMethod]
public void DetailMessage_RaisesPropertyChanged_WhenSet()
{
    var backend = new FakeBackendLifecycle();
    var web = new FakeWebViewBridge();
    var vm = new MainWindowViewModel(backend, web);

    var changed = new List<string?>();
    vm.PropertyChanged += (_, e) => changed.Add(e.PropertyName);

    vm.DetailMessage = "正在创建 VibeTable 数据结构…";

    Assert.AreEqual("正在创建 VibeTable 数据结构…", vm.DetailMessage);
    CollectionAssert.Contains(changed, nameof(MainWindowViewModel.DetailMessage));
}

[TestMethod]
public void DetailMessage_SetToSameValue_DoesNotRaise()
{
    var backend = new FakeBackendLifecycle();
    var web = new FakeWebViewBridge();
    var vm = new MainWindowViewModel(backend, web);
    vm.DetailMessage = "same";

    var changed = new List<string?>();
    vm.PropertyChanged += (_, e) => changed.Add(e.PropertyName);

    vm.DetailMessage = "same"; // identical

    CollectionAssert.DoesNotContain(changed, nameof(MainWindowViewModel.DetailMessage));
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `$DOTNET test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj`
Expected: FAIL——三个中文化断言失败（VM 仍返回英文），两个新测试失败（`DetailMessage` 属性不存在，编译失败或运行时缺失）。

- [ ] **Step 4: 改 ViewModel——StatusText 中文化 + 新增 DetailMessage**

打开 `desktop/src/VibeTable.Desktop/ViewModels/MainWindowViewModel.cs`。

定位 `State` 的 setter（L74-90），在 `RaisePropertyChanged` 调用块里，**保持现有所有 RaisePropertyChanged 不变**，不要加 DetailMessage 到这里（Ready 时清空是 code-behind 的职责，见任务 6）。

定位 `StatusText`（L95-102），改为：

```csharp
public string StatusText => State switch
{
    StartupState.StartingBackend => "正在启动后端…",
    StartupState.LoadingWeb => "正在加载界面…",
    StartupState.Ready => "就绪",
    StartupState.Faulted => "出现错误",
    _ => State.ToString(),
};
```

在 `StatusText` 属性之后（L102 之后、`IsGridVisible` 之前），新增 `DetailMessage` 属性：

```csharp
private string _detailMessage = string.Empty;

/// <summary>
/// The most recent backend / Directus progress line, shown in the bottom
/// status bar while the system is starting or busy. Cleared by the host
/// (MainWindow code-behind) when the WebView reaches Ready. The ViewModel
/// itself does not clear it on state transitions — it has no notion of
/// "host progress" semantics.
/// </summary>
public string DetailMessage
{
    get => _detailMessage;
    set
    {
        if (_detailMessage != value)
        {
            _detailMessage = value;
            RaisePropertyChanged(nameof(DetailMessage));
        }
    }
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `$DOTNET test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj`
Expected: 全绿（中文化断言通过、两个 DetailMessage 新测试通过）。

- [ ] **Step 6: 提交**

```bash
git add desktop/src/VibeTable.Desktop/ViewModels/MainWindowViewModel.cs desktop/tests/VibeTable.Desktop.Tests/MainWindowViewModelTests.cs
git commit -m "feat(desktop): localize StatusText to Chinese and add DetailMessage bindable property"
```

---

### Task 5: 主窗口状态条移到底部、缩小字号、显示 DetailMessage

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/MainWindow.xaml`

**Interfaces:**
- Consumes: `MainWindowViewModel.StatusText`（中文化后）、`MainWindowViewModel.DetailMessage`（任务 4）、`MainWindowViewModel.IsRetryVisible`、`MainWindowViewModel.RetryCommand`。

**背景：** 当前 `MainWindow.xaml` 的 Grid 有两行：`Row 0`=Auto（顶部状态 DockPanel）、`Row 1`=`*`（WebView2）。改动后：`Row 0`=`*`（WebView2，占据主体）、`Row 1`=Auto（底部状态 DockPanel）。WebView2 必须 `Grid.Row="0"`，状态条 `Grid.Row="1"`。状态条字号 11、不加粗、GrayText 前景。Retry 按钮文案改中文"重试"。

底部栏布局：左边 `StatusText`（中文状态概要），中间分隔，右边 `DetailMessage`（muted，省略号截断）+ 最右 Retry 按钮。`DetailMessage` 为空时不占位（用 `StringToVisibility` 或简单绑定 + converter；本项目已有 `BooleanToVisibilityConverter` 但没有 `StringToVisibility`，所以用一个轻量 `TextBlock` 配合 `Binding` 的 `TargetNullValue`/空字符串可见性处理——见下方实现，用 code-behind 不必要，直接用一个简单的多绑定或把 DetailText 放在固定位置让其空时不影响布局）。

**最简方案：** 用两个 TextBlock 放在一个 StackPanel（Orientation=Horizontal）里：第一个绑定 `StatusText`，第二个绑定 `DetailMessage` 并设置 `Visibility` 通过一个内联的值转换器……但项目没有 StringToVisibility。**更简单：** DetailMessage 非空时也只是一段文本，空字符串时 TextBlock 高度为 0 不影响（StackPanel Horizontal 下空 TextBlock 不占宽度）。所以直接绑定，空就空着，不需要 converter。

- [ ] **Step 1: 改 MainWindow.xaml——移行、移位、改样式**

打开 `desktop/src/VibeTable.Desktop/MainWindow.xaml`，把整个 `<Grid>` 内容（L18-54）替换为：

```xaml
    <Grid>
        <Grid.RowDefinitions>
            <RowDefinition Height="*"/>
            <RowDefinition Height="Auto"/>
        </Grid.RowDefinitions>

        <!--
            Hardened WebView2 host. The control is realized (visible) whenever a
            WebView2 instance exists (LoadingWeb + Ready): a Collapsed/Hidden
            WebView2 has a zero-size HWND and its virtual-host navigation can
            fail with success=False http=0. Binding to IsWebViewVisible (NOT
            IsGridVisible) keeps the HWND alive during the LoadingWeb navigate.
            The WebMessageRouter attached in code-behind is the sole path from
            the renderer into the .NET host.
        -->
        <wv2:WebView2
            x:Name="WebView"
            Grid.Row="0"
            Visibility="{Binding IsWebViewVisible, Converter={StaticResource BooleanToVisibilityConverter}}"/>

        <!-- Bottom status bar (always visible): status text + detail + retry. -->
        <DockPanel Grid.Row="1" LastChildFill="True"
                   Background="{DynamicResource {x:Static SystemColors.ControlLightBrushKey}}">
            <Button
                DockPanel.Dock="Right"
                Padding="12,2" Margin="8,0,8,0"
                Content="重试"
                Visibility="{Binding IsRetryVisible, Converter={StaticResource BooleanToVisibilityConverter}}"
                Command="{Binding RetryCommand}"
                ToolTip="重新启动后端并刷新界面"/>
            <StackPanel Orientation="Horizontal" VerticalAlignment="Center" Margin="8,0,0,0">
                <TextBlock
                    Text="{Binding StatusText}"
                    FontSize="11"
                    Foreground="{DynamicResource {x:Static SystemColors.GrayTextBrushKey}}"/>
                <TextBlock
                    Text="{Binding DetailMessage}"
                    FontSize="11"
                    Margin="10,0,0,0"
                    Foreground="{DynamicResource {x:Static SystemColors.GrayTextBrushKey}}"
                    TextTrimming="CharacterEllipsis"
                    MaxWidth="600"/>
            </StackPanel>
        </DockPanel>
    </Grid>
```

要点：
- Row 顺序从 `(Auto, *)` 改为 `(*, Auto)`：WebView 占主体，状态条在底部。
- 状态条 `Grid.Row="1"`（原来是 Row 0）。
- 字号 14 → 11，去掉 `FontWeight="SemiBold"`，前景改 `GrayTextBrushKey`（muted）。
- 新增 `DetailMessage` TextBlock，`MaxWidth="600"` + `TextTrimming="CharacterEllipsis"` 防止过长撑爆。
- Retry 按钮文案 "Retry" → "重试"。
- 保留原有的 WebView2 注释块（已移到 WebView 上方）。

- [ ] **Step 2: 构建确认编译通过**

Run: `$DOTNET build desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj`
Expected: 成功（0 错误）。XAML 编译会校验绑定的属性存在（`StatusText`、`DetailMessage`、`IsWebViewVisible`、`IsRetryVisible`、`RetryCommand` 都已在 ViewModel）。

- [ ] **Step 3: 提交**

```bash
git add desktop/src/VibeTable.Desktop/MainWindow.xaml
git commit -m "feat(desktop): move status bar to bottom, shrink font, show DetailMessage"
```

---

### Task 6: 宿主把后端/Directus/应用进度路由到 `DetailMessage`

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs:186-189,221-224,231-234,300-378`

**Interfaces:**
- Consumes: `MainWindowViewModel.DetailMessage`（任务 4）。

**背景：** 宿主已有三个进度来源：
1. `OnDirectusProgressChanged`（L340-361）：把 `DirectusStartupProgress` 路由到 `_directusStartupWindow`。
2. `OnBackendLogReceived`（L300-301）/ `OnBackendStateChanged`（L303-319）：Python 后端日志。
3. `UpdateStartupHostStage`（L186-189、L221-224、L231-234）：应用级阶段（"启动应用后端"、"建立登录会话"、"首次启动完成"）。

这三处都只在 `_directusStartupWindow`（首次启动模态窗）可见，主窗口底部栏拿不到。改动：在这三处同时写入 `_viewModel.DetailMessage`。用 `Dispatcher.BeginInvoke` 保证线程安全。对日志行做长度截断（80 字符）避免过长。

**注意线程模型：** 这些 handler 可能从非 UI 线程调用（进程 stdout 泵）。已有代码用 `Dispatcher.BeginInvoke`（见 L326、L349），本次沿用同样模式。`_viewModel` 在 UI 线程构造，属性 set 触发 `PropertyChanged`，WPF 绑定会自动 marshal 到 UI 线程，所以理论上直接 `_viewModel.DetailMessage = ...` 也安全；但为与既有风格一致，统一包在 `Dispatcher.BeginInvoke` 里。

- [ ] **Step 1: 新增一个私有 helper 方法 `SetDetailMessage`**

打开 `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`，在 `UpdateStartupHostStage` 方法（L597-598）附近新增：

```csharp
private void SetDetailMessage(string? message)
{
    if (string.IsNullOrWhiteSpace(message))
    {
        return;
    }
    string trimmed = message.Length > 80
        ? message[..77] + "…"
        : message;
    try
    {
        Dispatcher.BeginInvoke(() =>
        {
            // Only update while the system is not yet Ready; once Ready the
            // bottom bar shows just "就绪" and DetailMessage is cleared.
            if (_viewModel.State is StartupState.StartingBackend
                or StartupState.LoadingWeb)
            {
                _viewModel.DetailMessage = trimmed;
            }
        });
    }
    catch
    {
        // Window may be closing; best-effort.
    }
}
```

- [ ] **Step 2: 在 `UpdateStartupHostStage` 里同时写入 DetailMessage**

定位 `UpdateStartupHostStage`（L597-598），当前：

```csharp
private void UpdateStartupHostStage(int step, string title, string detail)
    => _directusStartupWindow?.UpdateHostStage(step, title, detail);
```

改为：

```csharp
private void UpdateStartupHostStage(int step, string title, string detail)
{
    _directusStartupWindow?.UpdateHostStage(step, title, detail);
    SetDetailMessage(detail);
}
```

- [ ] **Step 3: 在 `OnDirectusProgressChanged` 里写入 DetailMessage**

定位 `OnDirectusProgressChanged`（L340-361）。在 `Dispatcher.BeginInvoke` 块内、`_directusStartupWindow?.UpdateProgress(progress);` 之后，加一行。但 detail 是英文 key（需要翻译），而 `DirectusStartupWindow.TranslateDetail` 是 `private static`。**为了复用翻译**，把 `DirectusStartupWindow.TranslateDetail` 改成 `internal static`（同项目内可见）。先改 `DirectusStartupWindow.xaml.cs:235`：

```csharp
internal static string TranslateDetail(string detail)
```

（只把 `private` 改 `internal static`，方法体不变。）

然后在 `MainWindow.xaml.cs` 的 `OnDirectusProgressChanged` 里，把 `Dispatcher.BeginInvoke` 块改成：

```csharp
Dispatcher.BeginInvoke(() =>
{
    if (ReferenceEquals(sender, _directusSupervisor))
    {
        _directusStartupWindow?.UpdateProgress(progress);
        SetDetailMessage(DirectusStartupWindow.TranslateDetail(progress.Detail));
    }
});
```

- [ ] **Step 4: 在 `OnBackendLogReceived` / `OnBackendStateChanged` 里写入 DetailMessage**

定位 `OnBackendLogReceived`（L300-301）：

```csharp
private void OnBackendLogReceived(object? sender, string line)
    => TraceProcessLog("backend", line);
```

改为：

```csharp
private void OnBackendLogReceived(object? sender, string line)
{
    TraceProcessLog("backend", line);
    SetDetailMessage(line);
}
```

定位 `OnBackendStateChanged`（L303-319），在方法开头（`string status = ...` 之后）加一行用 state 生成中文摘要。在 `Trace.WriteLine($"[backend] {status}");` 之前插入：

```csharp
string detail = state switch
{
    BackendState.Starting => "正在启动应用后端…",
    BackendState.Ready => "应用后端已就绪。",
    BackendState.Faulted => "应用后端进程意外退出。",
    _ => null,
};
if (detail is not null)
{
    SetDetailMessage(detail);
}
```

（`BackendState` 枚举值已确认：`Stopped, Starting, Ready, Stopping, Faulted`，定义在 `desktop/src/VibeTable.Infrastructure/Backend/BackendState.cs:25`。）

- [ ] **Step 5: 在 WebView Ready 时清空 DetailMessage**

WebView2 的 `NavigationCompleted` 成功处理在 `WebViewBridge.LoadAsync`（L1048-1129 内联 `core.NavigationCompleted += onNavigationCompleted`，L1097-1113）。ViewModel 的 `LoadingWeb → Ready` 转换由 `DriveWebViewLoadAsync`（`MainWindowViewModel.cs:193-215`）在 `LoadAsync` 完成后触发。

**最简方案：** 不改 `WebViewBridge.LoadAsync`，而是在 ViewModel 的 `TransitionTo` 里，当目标是 `Ready` 时清空 `DetailMessage`。但任务 4 明确说了"VM 本身不在 TransitionTo 里清空"。**改用方案 B：** 在 `MainWindow.xaml.cs` 的 `OnDirectusStateChanged`（L363-372）里，当 Directus 进入 Ready 时（其实 Directus Ready 早于 WebView Ready），或在 WebView 加载完成路径里清空。

**实际最稳妥：** 复用已有的 `_viewModel.PropertyChanged` 订阅。当前在构造函数 L142-155 有一个订阅，但它包在 `if (_shellSmokeMode)` 里（只用于 shell 烟测）。把它**拆出来**变成无条件订阅，在原有 State 变化逻辑基础上加 DetailMessage 清空。

打开 `desktop/src/VibeTable.Desktop/MainWindow.xaml.cs`，定位 L142-156：

```csharp
_viewModel = new MainWindowViewModel(_backendAdapter, _webBridge);
_supervisor.LogReceived += OnBackendLogReceived;
_supervisor.StateChanged += OnBackendStateChanged;
if (_shellSmokeMode)
{
    _viewModel.PropertyChanged += (_, args) =>
    {
        if (string.Equals(args.PropertyName, nameof(MainWindowViewModel.State),
            StringComparison.Ordinal))
        {
            TryWriteShellReadiness();
        }
    };
}
DataContext = _viewModel;
```

把 `if (_shellSmokeMode) { _viewModel.PropertyChanged += ... }` 改成无条件订阅，在 State 变化时既清空 DetailMessage 又（仅 shell 烟测时）写 readiness：

```csharp
_viewModel = new MainWindowViewModel(_backendAdapter, _webBridge);
_supervisor.LogReceived += OnBackendLogReceived;
_supervisor.StateChanged += OnBackendStateChanged;
_viewModel.PropertyChanged += OnViewModelPropertyChanged;
DataContext = _viewModel;
```

并新增方法（放在 `SetDetailMessage` 附近）：

```csharp
private void OnViewModelPropertyChanged(object? sender, System.ComponentModel.PropertyChangedEventArgs e)
{
    if (!string.Equals(e.PropertyName, nameof(MainWindowViewModel.State),
            StringComparison.Ordinal))
    {
        return;
    }
    // Clear the bottom-bar detail line once the system reaches Ready; the
    // bar then shows only the "就绪" status, not stale progress text.
    if (_viewModel.State == StartupState.Ready)
    {
        _viewModel.DetailMessage = string.Empty;
    }
    if (_shellSmokeMode)
    {
        TryWriteShellReadiness();
    }
}
```

这样：原 shell 烟测行为保留（只是从内联 lambda 改成命名方法），新增的 DetailMessage 清空逻辑对**所有**运行模式生效，无论从哪条路径进入 Ready。

- [ ] **Step 6: 构建确认编译通过**

Run: `$DOTNET build desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj`
Expected: 成功（0 错误）。如果 `BackendState` 枚举值名不对，按编译错误修正。

- [ ] **Step 7: 运行桌面测试确认无回归**

Run: `$DOTNET test desktop/tests/VibeTable.Desktop.Tests/VibeTable.Desktop.Tests.csproj`
Expected: 全绿（本任务只改 code-behind，不直接影响 VM 测试，但跑一遍确认编译兼容）。

- [ ] **Step 8: 提交**

```bash
git add desktop/src/VibeTable.Desktop/MainWindow.xaml.cs desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml.cs
git commit -m "feat(desktop): route backend/Directus/host progress into bottom status DetailMessage"
```

---

### Task 7: 缩小三个窗口的标题字号

**Files:**
- Modify: `desktop/src/VibeTable.Desktop/DirectusFirstRunWindow.xaml:20-21`
- Modify: `desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml:16-19`
- Modify: `desktop/src/VibeTable.Desktop/DirectusLoginWindow.xaml:19`

**Interfaces:** 无（纯样式）。

- [ ] **Step 1: 改欢迎窗口标题字号**

打开 `desktop/src/VibeTable.Desktop/DirectusFirstRunWindow.xaml`，L20-21：

```xaml
<TextBlock x:Name="HeadingText" Grid.Row="0" Text="欢迎使用 VibeTable"
           FontSize="24" FontWeight="SemiBold"/>
```
把 `FontSize="24"` 改为 `FontSize="18"`：

```xaml
<TextBlock x:Name="HeadingText" Grid.Row="0" Text="欢迎使用 VibeTable"
           FontSize="18" FontWeight="SemiBold"/>
```

- [ ] **Step 2: 改启动窗口横幅主标题 + 副标题字号**

打开 `desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml`，L16-19：

```xaml
<TextBlock Text="正在准备本地数据服务" Foreground="White"
           FontSize="24" FontWeight="SemiBold"/>
<TextBlock x:Name="StageTitleText" Text="准备运行环境"
           Foreground="#D9E8F5" FontSize="15" Margin="0,8,0,0"/>
```
改为：

```xaml
<TextBlock Text="正在准备本地数据服务" Foreground="White"
           FontSize="18" FontWeight="SemiBold"/>
<TextBlock x:Name="StageTitleText" Text="准备运行环境"
           Foreground="#D9E8F5" FontSize="13" Margin="0,8,0,0"/>
```

- [ ] **Step 3: 改登录窗口标题字号**

打开 `desktop/src/VibeTable.Desktop/DirectusLoginWindow.xaml`，L19：

```xaml
<TextBlock Grid.Row="0" Text="登录 Directus" FontSize="24" FontWeight="SemiBold"/>
```
改为：

```xaml
<TextBlock Grid.Row="0" Text="登录 Directus" FontSize="18" FontWeight="SemiBold"/>
```

- [ ] **Step 4: 构建确认编译通过**

Run: `$DOTNET build desktop/src/VibeTable.Desktop/VibeTable.Desktop.csproj`
Expected: 成功（0 错误）。

- [ ] **Step 5: 提交**

```bash
git add desktop/src/VibeTable.Desktop/DirectusFirstRunWindow.xaml desktop/src/VibeTable.Desktop/DirectusStartupWindow.xaml desktop/src/VibeTable.Desktop/DirectusLoginWindow.xaml
git commit -m "style(desktop): reduce window heading font sizes from 24 to 18"
```

---

### Task 8: 最终验证

**Files:** 无（只跑命令）。

- [ ] **Step 1: 完整构建整个 sln**

Run: `$DOTNET build desktop/VibeTable.Desktop.sln`
Expected: 0 警告 0 错误（或仅有已知的、与本次改动无关的既有警告）。

- [ ] **Step 2: 跑全部测试**

Run: `$DOTNET test desktop/VibeTable.Desktop.sln`
Expected: 全绿。Infrastructure.Tests 和 Desktop.Tests 都通过。

- [ ] **Step 3: `git diff --check`**

Run: `git diff --check`
Expected: 无输出（无空白错误）。

- [ ] **Step 4: 提交计划文件本身（可选）**

如果 `task_plan.md` / `findings.md` / `progress.md` / spec / plan 还没提交，可在本步一并提交；否则跳过。

```bash
git add docs/superpowers/specs/2026-07-17-first-run-ux-polish-design.md docs/superpowers/plans/2026-07-17-first-run-ux-polish.md
git commit -m "docs: first-run UX polish spec and implementation plan"
```

---

## Self-Review

**1. Spec coverage:**
- §1 npm 校验可见化 → 任务 1（枚举）+ 任务 2（发出 RecheckingPackages）+ 任务 3（窗口映射 + hint）。
- §2 底部状态栏（合并日志）→ 任务 4（DetailMessage + 中文化）+ 任务 5（XAML 移位/字号）+ 任务 6（宿主路由）。
- §3 标题字号 → 任务 7。
- §4 Ready 重命名反映系统状态 → 任务 4（StatusText 中文化为"就绪"）+ 任务 5（底部栏）。✓

**2. Placeholder scan:** 无 TBD/TODO；所有代码块完整；测试代码可执行；命令带 `$DOTNET` 明确路径。✓

**3. Type consistency:**
- `RecheckingPackages` 在任务 1 定义、任务 2/3 消费——名称一致。✓
- `DetailMessage` 在任务 4 定义、任务 5 绑定、任务 6 写入、任务 6 Step 5 清空——名称一致。✓
- `StatusText` 中文字符串在任务 4 定义、任务 5 测试断言在任务 4 Step 1 同步更新——一致。✓
- `TranslateDetail` 任务 6 Step 3 改 `internal static`、任务 3 Step 2 改翻译表——两处改的是同一文件同一方法，无冲突（任务 3 改方法体内容，任务 6 改可见性修饰符；若任务 3 先提交则任务 6 的 `internal` 修饰仍作用于被任务 3 改过的内容）。✓

**4. Task 6 Step 3 的依赖：** 任务 6 把 `TranslateDetail` 从 `private` 改 `internal static`。任务 3 改的是 `TranslateDetail` 方法体（翻译表内容）。两任务改同一方法的不同方面，提交顺序（任务 3 在前、任务 6 在后）不会冲突。✓

**5. 风险：** `BackendState` 枚举值名（任务 6 Step 4）未在计划中确认——计划已注明"先 grep 确认"。执行时第一步 grep 即可。
