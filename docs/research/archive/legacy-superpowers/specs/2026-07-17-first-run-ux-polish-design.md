> 历史设计归档；不属于当前产品实现。

# 首次启动体验打磨：校验可见性 + 底部状态栏 + 字号收敛

**日期：** 2026-07-17
**状态：** 待评审
**关联反馈：**
1. 初始化失败后下次应走完整流程（含 npm 包校验），并让校验过程可见。
2. 进入系统后等待后端启动时要有说明/日志，而不是空白。
3. 顶部 "Ready" 状态条的作用不清晰；建议移到底部、不加粗、字号缩小。
4. 窗口标题字号过大。

---

## 背景与现状

- 失败恢复时 `DirectusPackageManager.EnsureInstalledAsync`（`desktop/src/VibeTable.Infrastructure/Directus/DirectusPackageManager.cs:143-158`）**确实会**调用 `VerifyAsync`（结构 + isolated-vm 原生加载 + lock hash 三项），只是校验通过时不重新 `npm ci`。逻辑正确，问题在**可见性**：当前 `VerifyingPackages` 阶段把"安装后校验"和"失败恢复时的强制校验"合并为同一步骤标题，只在细节行区分，用户感知不到这是一次完整复检。
- 顶部状态条位于 `MainWindow.xaml:24-39`（`Grid.Row="0"`，`FontSize=14`，`FontWeight=SemiBold`），仅反映 WPF 宿主的后端+WebView 启动状态（`MainWindowViewModel.StatusText`，`MainWindowViewModel.cs:95-102`），不反映 Directus 状态。启动期间主内容区为空白 WebView。
- 后端/Directus 日志（`OnBackendLogReceived`、`OnDirectusProgressChanged`、`TraceProcessLog`，`MainWindow.xaml.cs:300-378`）只走 `System.Diagnostics.Trace`，主窗口没有任何可见输出口。
- 欢迎窗口、启动窗口横幅、登录窗口标题均为 `FontSize=24`。
- web-grid 自身的状态行（`web-grid/src/styles.css:38-41`，`font-size: 12px`，muted）独立存在，不在本次修改范围。

## 决策记录

| 决策 | 原因 |
|---|---|
| 不改 npm 校验逻辑，只改可见性 | 现有逻辑（VerifyAsync + 校验失败才 npm ci 自愈）已经满足"失败后强制校验"语义；重装只在必要时发生 |
| 把"失败恢复强制校验"独立成一步 | 用户反馈"看不到校验发生过"；与普通安装后校验区分后会更清晰 |
| 把顶部状态条移到底部并合并日志职责 | 用户明确要求移到底部、不加粗、字号缩小；同时承担"等待时的说明/日志"职责，避免新增独立日志面板 |
| 状态文本统一为中文 | 其他窗口（欢迎、启动、登录）均为中文，主窗口应一致 |
| 只缩小窗口标题字号到 18 | 标题加粗保留；24 在桌面端偏大 |

## 变更范围

### 1. npm 包校验可见化（失败恢复路径）

**`DirectusStartupProgress.cs`：** 在 `DirectusStartupStage` 枚举中新增 `RecheckingPackages`（语义：之前初始化未完成，正在对已存在安装做强制完整性复检）。位于 `VerifyingPackages` 之后、`InitializingDatabase` 之前。

**`DirectusPackageManager.cs:143-158`：** 把"强制校验已存在安装"分支发出的进度事件从
```
VerifyingPackages / "The previous initialization did not finish; rechecking..."
```
改为
```
RecheckingPackages / "检测到上次初始化未完成，正在重新校验 Directus 依赖完整性（结构、原生模块、lock hash）。"
```
同时在 `VerifyAsync` 三个子步骤前可选地 `progress?.Invoke(...)` 各发一条 detail，让日志框能看到"结构 → 原生模块 → lock hash"三段，而不是一个黑盒。

**`DirectusStartupWindow.xaml.cs:54-83`：** `UpdateProgress` 的 stage→step / title 映射里：
- `RecheckingPackages` 映射到 step 1（仍归"安装并校验 Directus"这一步位置），但**标题独立**："检测到上次未完成，正在复检 Directus 依赖"。
- 重试时的 `HintText`（`ResetForRetry`，`DirectusStartupWindow.xaml.cs:123`）改为说明"已安装文件会被重新校验（结构、原生模块、lock hash）；校验失败才会重装"，让用户清楚恢复行为。

**测试：** `DirectusPackageManagerTests.cs` 中
- `EnsureInstalled_DoesNotTrustFreshMarker_WhenFirstRunIncomplete`（L154-186）的断言更新为期望 `RecheckingPackages` 阶段事件（而非 `VerifyingPackages`）。
- 新增一个测试断言：失败恢复时如果 `VerifyAsync` 通过，**不会**发出 `InstallingPackages`，但**会**发出 `RecheckingPackages`。

### 2. 顶部状态条 → 底部状态栏（合并启动日志）

**`MainWindow.xaml`：**
- `Grid.RowDefinitions` 增加第三行：`Row 0`=`*`（WebView 占据主体），`Row 1`=`Auto`（底部状态栏）。去掉顶部 `Row 0` 的 `DockPanel`，把 WebView 放到 `Row 0`，状态栏放到 `Row 1`。
- 底部状态栏结构：
  ```xaml
  <DockPanel Grid.Row="1" LastChildFill="True"
             Background="{DynamicResource {x:Static SystemColors.ControlLightBrushKey}}">
      <Button DockPanel.Dock="Right" ... Content="重试" .../>  <!-- 仅 Faulted 可见 -->
      <TextBlock Text="{Binding StatusText}"
                 VerticalAlignment="Center" Margin="8,0"
                 FontSize="11"
                 Foreground="{DynamicResource {x:Static SystemColors.GrayTextBrushKey}}"/>
  </DockPanel>
  ```
- 字号 11、不加粗、GrayText 前景色。

**`MainWindowViewModel.cs:95-102`：** `StatusText` 改为中文：
- `StartingBackend` → "正在启动后端…"
- `LoadingWeb` → "正在加载界面…"
- `Ready` → "就绪"
- `Faulted` → "出现错误"

并新增一个可绑定属性 `DetailMessage`（string，默认空）：启动期间由宿主写入最新一行进度/日志；就绪后清空。`StatusText` 显示状态概要，`DetailMessage` 显示当前正在做的具体事（如"正在创建 VibeTable 数据结构…"）。

**底部栏布局：** 状态概要 + 分隔 + `DetailMessage`（muted，省略号截断）。`DetailMessage` 非空时显示，空时只显示 `StatusText`。

**`MainWindow.xaml.cs`：**
- `OnDirectusProgressChanged`（L340-361）除了路由到 `_directusStartupWindow`，同时把 `TranslateDetail(progress.Detail)` 的结果写到 `_viewModel.DetailMessage`。
- `OnBackendLogReceived`（L300-301）和 `OnBackendStateChanged`（L303-319）：把最后一条非空行摘要写到 `_viewModel.DetailMessage`（例如 `"backend: <line>"` 截断到 80 字符）。
- `OnDirectusLogReceived`（L321-338）：可选地把关键 directus 日志行（如包含 "Listening"、"error"、"started" 的行）刷新到 `DetailMessage`；非关键行仍只进 Trace，避免噪声。
- WebView `NavigationCompleted` 成功转 Ready 时清空 `DetailMessage`。

**注意：** `DirectusStartupWindow`（首次启动模态进度窗）保留不变，仍显示完整步骤和日志框——底部状态栏服务于"模态窗关闭后、主窗口内"的等待场景（如后端启动较慢、Directus 已就绪但 Python 后端还在起）。两者职责不重叠。

**测试：** `MainWindowViewModelTests.cs` 增加：`DetailMessage` 在 `StartingBackend` 可被外部赋值并触发 `RaisePropertyChanged`；`Ready` 转换后 `DetailMessage` 被清空；`StatusText` 中文断言。

### 3. 缩小窗口标题字号

**`DirectusFirstRunWindow.xaml:20-21`** 欢迎标题：`FontSize 24 → 18`。
**`DirectusStartupWindow.xaml:16-17`** 横幅主标题：`FontSize 24 → 18`。
**`DirectusStartupWindow.xaml:18-19`** 横幅副标题 `StageTitleText`：`FontSize 15 → 13`（与主标题缩小比例一致）。
**`DirectusLoginWindow.xaml:19`** 登录标题：`FontSize 24 → 18`。

`FontWeight=SemiBold` 在所有标题上保留（标题加粗合理；反馈中"不需要加粗"针对的是状态条，已在 §2 处理）。

## 非目标（YAGNI）

- 不引入完整可滚动日志面板（用户已确认合并到底部状态栏即可）。
- 不修改 web-grid 内部的 `#status` 行（独立 UX，本次反馈未涉及）。
- 不改 Directus 启动顺序或后端启动时机（白屏根因是 Directus 在 WebView 加载前完成且无可见进度，已由既有 `DirectusStartupWindow` 解决；本次只补主窗口内的可见性）。
- 不重做失败重试对话框（既有 Retry/Exit 决策已可用）。
- 不改 npm 校验的判定逻辑（保持 VerifyAsync + 失败才 npm ci）。

## 风险与回滚

- **风险：** `DetailMessage` 频繁刷新可能造成 UI 抖动。**缓解：** 用 `Dispatcher.BeginInvoke` 合并，并对同一文本去重（仅变化时 `RaisePropertyChanged`）。
- **风险：** 顶部状态条移除后，原有依赖 `StatusText` 绑定的元素只剩底部栏。**缓解：** 已确认仅 `MainWindow.xaml:35` 绑定 `StatusText`。
- **回滚：** 全部为局部 XAML / ViewModel / 单枚举值改动，git revert 单提交即可。

## 验证清单

- [ ] `dotnet build` VibeTable.Desktop + VibeTable.Infrastructure：0 警告 0 错误。
- [ ] `dotnet test` VibeTable.Infrastructure.Tests + VibeTable.Desktop.Tests：全绿（更新后的 `RecheckingPackages` 断言通过）。
- [ ] `git diff --check`：无空白错误。
- [ ] 手工：删除 `.vibetable-initialized`（保留 `.install-verified`）模拟半完成状态，启动应用，确认启动窗口显示"检测到上次未完成，正在复检 Directus 依赖"独立标题，且底部状态栏在主窗口就绪后显示"就绪"（小字、不加粗）。
- [ ] 手工：确认首次启动三个窗口标题字号为 18，视觉上更克制。
