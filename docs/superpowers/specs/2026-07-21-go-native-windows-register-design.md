# Go 原生 Windows 注册引擎设计

日期：2026-07-21
状态：已确认，待实现计划

## 背景

当前 Windows 注册能力由 Go 后端管理一个 Python 子进程。Python 引擎依赖 Playwright、CloakBrowser、HTTP 客户端和虚拟环境，`deploy.ps1` 需要在目标机发现 Python、创建 `.venv`、安装 PyPI 依赖并下载浏览器。这个链路增加了首次部署失败面，也使低权限 `LOCAL SERVICE` 的路径发现和 ACL 处理更复杂。

现有发布 ZIP 不包含 `.venv` 或浏览器二进制，`tools/windows-register` 源码约 0.15 MiB，因此改写不会显著缩小 ZIP。主要收益是删除 Python/venv/PyPI 运行时依赖；真实 Chromium 仍然是注册流程的必要外部组件和主要磁盘占用。

本设计将注册引擎迁入现有 Go 进程，通过 Chrome DevTools Protocol（CDP）控制 Chromium，同时保持管理端 API、页面和导入语义兼容。

## 目标

- Windows 注册流程不再依赖 Python、venv、pip、Playwright 或 CloakBrowser Python 包。
- 最终发布包删除整个 `tools/windows-register` 目录。
- 不增加第二个 Go 可执行文件，注册引擎编译进 `grok2api.exe`。
- 使用包管理或系统已有的 Chromium、Chrome 或 Edge，通过 CDP 完成页面自动化。
- 保持现有注册状态、启动、停止、日志和导入 API 契约不变。
- 保持管理端注册页面不变；仅在缺失运行时提示中移除 Python 相关项。
- 保留当前并发调度、限流熔断、日志脱敏和“本次/全部”导入能力。
- Windows 打包和部署不再执行 Python 探测或 PyPI 安装。

## 非目标

- 不尝试用纯 HTTP 完全替代浏览器；Turnstile、首方 Cookie 和动态 Next.js Server Action 仍使用真实浏览器上下文。
- 不把 Chromium 二进制直接塞进发布 ZIP。
- 不同时重做注册管理页面或账号导入业务。
- 不改变 Web/Console 账号池的凭据格式。
- 不承诺规避所有上游风控。上游页面、挑战和指纹策略变化仍可能需要适配。

## 方案比较

### 方案 A：集成式 Go 引擎（采用）

注册引擎作为 `internal/infra/windowsregister` 内部组件运行，使用 goroutine、context 和 CDP 驱动浏览器。它复用现有 Go 状态机，无需额外进程或可执行文件。

优点：部署最简单；API 无需跨进程通信；不重复链接 Go 运行时代码；现有状态和日志模型可直接复用。
缺点：引擎错误发生在主进程内，需要严格隔离 panic、阻塞和资源泄漏。

### 方案 B：独立 Go helper

构建单独的 `grok-register.exe`，由后端继续按子进程管理。

优点：进程级隔离好。
缺点：额外可执行文件重复包含 Go 运行时和依赖，ZIP 可能增大数 MiB；仍需处理子进程协议和生命周期，不符合单主程序目标。

### 方案 C：纯 HTTP

直接用 Go HTTP 客户端调用注册接口。

优点：最小运行时。
缺点：无法可靠处理 Turnstile、浏览器绑定状态、首方导航 Cookie 和动态页面参数，不能达到现有功能等价性。

## 总体架构

```text
Admin HTTP API
      |
Application Windows Register Service
      |
Native Go Registration Engine
  |-- Browser Manager (Chromium + CDP)
  |-- Dynamic Config Discovery
  |-- Email Providers
  |-- Turnstile Flow
  |-- Registration Client
  |-- Pipeline Scheduler
  |-- Result Store
      |
Existing Web / Console Account Importer
```

应用层和 HTTP 层继续依赖一个小型 `TokenSource/Worker` 接口。基础设施实现从“启动 Python 子进程并解析 stdout”改为“在进程内运行原生 Go Engine”。前端无需感知实现变化。

## 组件设计

### Engine 与状态机

现有 `Service` 的公开方法保持不变：

```go
Status() Status
Start(StartOptions) (Status, error)
Stop(context.Context) (Status, error)
ImportTokens(scope string) ([]string, error)
Close()
```

`Start` 创建一次运行级 context，进入 `starting`，完成浏览器与动态配置准备后进入 `running`。同一进程最多运行一个注册任务。`Stop` 取消 context，关闭页面、BrowserContext 和浏览器进程，并在 15 秒内转为 `stopped`。未捕获的 worker panic 必须在运行边界恢复，转为脱敏的 `error` 状态，不能终止主服务。

### Browser Manager

Browser Manager 负责发现、启动、重启和关闭浏览器。路径解析顺序：

1. `GROK2API_REGISTER_BROWSER` 环境变量；
2. `windowsRegister.browserPath`；
3. 部署脚本管理的 `data/windows-register/browser`；
4. 系统 Chrome；
5. 系统 Edge。

Go CDP 驱动采用 Rod。每次注册运行拥有一个 Browser 进程；邮箱验证码、Turnstile 和账号创建使用隔离的 BrowserContext/Page。静态资源拦截、代理、User-Agent、Cookie 注入和首方导航通过可测试的小接口封装。

如果浏览器意外退出，Engine 终止当前受影响任务，最多重启浏览器一次；第二次退出将整次运行标记为错误，避免无限重启。

### 浏览器部署

发布 ZIP 不携带浏览器。`deploy.ps1` 首先查找系统 Chrome/Edge。找不到时，下载固定版本的 Chrome for Testing 到 `data/windows-register/browser`，并在解压前校验仓库中固定的 SHA-256。浏览器版本、下载 URL 和校验值集中声明，禁止使用未固定的 latest URL。

下载失败不影响核心 API 启动；注册状态返回 `ready=false` 和 `missing=["browser"]`。部署脚本不再要求 Python，也不访问 PyPI。

### Dynamic Config Discovery

该组件打开 `accounts.x.ai/sign-up`，提取：

- Turnstile Site Key；
- Next.js Server Action ID；
- Router State Tree。

页面 HTML 和静态 JS 解析逻辑从 Python 等价迁移。提取结果仅在一次运行内缓存；注册响应表明参数失效时允许重新发现一次。

### Email Providers

定义统一接口：

```go
type EmailProvider interface {
    Create(context.Context) (Mailbox, error)
    PollCode(context.Context, Mailbox) (string, error)
}
```

实现现有公共临时邮箱与自定义邮箱 API。所有 HTTP 请求设置有限超时、响应体上限和可取消 context。邮箱、密码、验证码和 SSO 不写入日志。

### Turnstile Flow

Turnstile 组件在真实浏览器页面中注入或等待挑战组件，通过 DOM/CDP 读取响应 token。鼠标交互、轮询间隔、硬超时和页面复用保持可配置。失败分为超时、页面变化、浏览器退出和上游拒绝，供调度器决定是否重试。

此组件不实现第三方验证码服务，也不保证绕过所有上游挑战。它只迁移当前本地浏览器行为。

### Registration Client

验证码创建、验证码验证和账号创建仍在页面的第一方浏览器上下文内完成，以保留 Cookie、Origin 和浏览器网络行为。gRPC-Web frame 编码迁移为纯 Go 函数；动态注册请求通过页面 JavaScript fetch 或 CDP 网络调用执行。

账号创建成功后访问响应中的 `set-cookie` URL，再从 BrowserContext 提取 `sso`/`sso-rw` Cookie。解析器必须限制 URL scheme/host，防止上游响应诱导访问非预期地址。

### Pipeline Scheduler

保留现有 S/P/C 三阶段思想，但使用 Go channel 和有界 worker pool：

- S：生产 Turnstile token；
- P：创建邮箱、发送验证码并等待验证码；
- C：组合 token 与验证码完成注册。

所有队列必须有明确容量，生产者在取消时立即退出。目标数量达到后停止生产新任务，等待已进入 C 阶段的任务安全结算。限流响应触发现有恢复窗口和熔断逻辑。

### Result Store

首个版本保持 `data/windows-register/accounts.txt` 与当前解析格式兼容，以避免同时修改应用层导入逻辑。写入使用进程内互斥、追加打开、显式 flush；Windows 下尽可能限制文件 ACL。日志和状态响应不得包含原始凭据。

后续可单独设计加密数据库存储，本次迁移不扩大范围。

## 配置兼容

新增：

```yaml
windowsRegister:
  enabled: true
  browserPath: ""
  outputDir: "./data/windows-register"
```

`enginePath` 与 `pythonPath` 在一个兼容周期内继续被配置解析器接受，但不再使用，并从示例配置和管理说明删除。这样旧 `config.yaml` 不会因严格 YAML 解码而启动失败。后续大版本再删除字段。

## 错误处理与可观测性

- 所有外部操作使用 context 和有限超时。
- 错误按 `browser_unavailable`、`browser_crashed`、`config_discovery_failed`、`email_failed`、`turnstile_failed`、`rate_limited`、`registration_rejected` 分类。
- 状态中的 `LastError` 和环形日志继续使用统一脱敏器。
- 每个阶段维护成功、失败、耗时和在途数量，但不记录邮箱或 token。
- 浏览器页面和 Context 必须在每条退出路径关闭。
- 运行结束后清理本次创建的浏览器进程，不扫描或杀死用户的其他 Chrome/Edge 进程。

## 安全边界

- 仅管理员 API 可以启动、停止或导入注册结果。
- 浏览器下载必须固定版本并验证 SHA-256。
- 浏览器启动参数和代理凭据不进入日志。
- 注册响应中的跳转 URL仅允许预期的 HTTPS host。
- CDP 调试端口只绑定回环地址，并使用进程随机端口。
- 不向普通管理端响应暴露浏览器命令行、系统路径或内部堆栈。

## 测试策略

### 单元测试

- gRPC-Web frame 编码与 JWT/Cookie 解析；
- 动态页面参数提取；
- 注册响应与 set-cookie URL 校验；
- 邮箱验证码解析和轮询超时；
- 调度容量、取消、目标停止和限流熔断；
- 状态百分比、日志环形缓冲和脱敏；
- 浏览器路径发现顺序。

### 组件测试

Browser、Page 和 EmailProvider 均通过小接口注入 fake，覆盖浏览器退出、页面超时、挑战失败、重试和停止。使用 `httptest.Server` 模拟邮箱与上游响应，不依赖公网。

### 浏览器集成测试

提供 opt-in Windows 测试，使用本机 Chromium 打开仓库内静态 fixture 页面，验证 CDP 启动、Context 隔离、JavaScript 执行、Cookie 获取和关闭清理。默认 CI 不访问真实注册站点。

### 手工验收

在授权环境中执行一次 `target=1` 的真实 smoke test，确认：

1. 状态从 starting 到 running 再到 completed；
2. 日志无凭据泄漏；
3. 可以导入本次结果到 Web/Console；
4. 停止操作不遗留由本次任务创建的浏览器；
5. 不安装 Python也能完成流程。

### 打包验收

- 完整运行 `package.bat amd64`；
- 后端全部测试和 vet 通过，前端 lint/build 通过；
- ZIP 不包含 `.py`、`.pyc`、`.venv`、requirements 或 `tools/windows-register`；
- 解压后在无 Python 的 Windows 环境可启动核心服务；
- 浏览器存在时注册状态 ready，缺失时只报告 `missing=browser`。

## 迁移顺序

1. 提取浏览器、邮箱、编码、解析和存储接口及纯函数测试。
2. 实现 Go Browser Manager 与本地 fixture 集成测试。
3. 迁移动态配置、Turnstile、验证码和注册步骤。
4. 实现 Go pipeline，并接入现有 Service 状态/API。
5. 在开发分支保留 Python 作为行为对照，完成 opt-in smoke test。
6. 切换到 Go 引擎，删除 Python 子进程、探测和配置逻辑。
7. 删除 `tools/windows-register`，简化 package/deploy 脚本与文档。
8. 执行完整测试、Windows 打包和解压后 smoke 验收。

最终提交不保留用户可选择的双引擎开关，避免长期维护两套实现。

## 风险与缓解

- **CDP 驱动与 CloakBrowser 指纹不完全等价**：通过固定 Chromium 版本、真实 BrowserContext 和小范围启动配置降低差异；真实 smoke 是删除 Python 前的硬门槛。
- **上游页面结构变化**：动态参数解析集中在独立组件并使用 fixture/golden 测试。
- **浏览器影响主服务稳定性**：所有 worker 设恢复边界，浏览器进程失败不允许 panic 穿透；重启次数有界。
- **本地明文结果文件**：本次保持兼容但强化 ACL 和日志边界；加密持久化作为后续独立项目。
- **浏览器仍占磁盘**：优先复用系统 Chrome/Edge，只有缺失时才下载包管理浏览器。

## 完成标准

- 管理端现有 Windows 注册操作无需改用法。
- Go 引擎通过单元、组件和浏览器 fixture 测试。
- 授权环境 `target=1` smoke 成功且可导入。
- `package.bat amd64` 完整检查通过。
- 发布 ZIP 和部署脚本不存在 Python、venv、pip、Playwright、CloakBrowser 或 `tools/windows-register` 依赖。
- 核心服务在无 Python 环境正常启动，注册功能仅要求可用 Chromium。
