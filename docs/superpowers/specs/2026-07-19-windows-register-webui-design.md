# Windows 内置注册机 WebUI 融合设计

**日期**: 2026-07-19  
**分支**: `codex/windows-registration`  
**状态**: 已确认目标，待实现  
**来源引擎**: `C:\Users\Administrator\Documents\Codex\2026-07-16\j\outputs\grok-windows-bundle\grok-free-register-oss`  
**参考集成**: `grok-windows-bundle` / `grokcli-2api` 的 `windows_register` 生命周期管理

## 1. 目标与非目标

### 1.1 目标

把 `grok-free-register-oss` 的 Windows CloakBrowser 注册能力融合进当前 `grok2api`，做到：

1. **完整 WebUI 内置**：管理后台 Registration 页可启停注册机、查看进度/脱敏日志、一键导入账号池。
2. **Go 启停 Python 子进程**：Go 后端是唯一管理面；Python 引擎作为附属组件随仓库分发。
3. **仅 Windows**：非 Windows 隐藏/禁用面板，API 返回不可用。
4. **首期范围 = 注册 + 导入**：注册产出 SSO 后导入 Grok Web + Grok Console。

成功标准：

- 在 Windows 上完成引擎就绪检查 → 启动注册 → 看到实时进度/日志 → 停止或完成后导入 → 账号出现在 Web/Console 池中。
- 日志不泄露完整 token、密码、完整邮箱、代理凭据。
- 同时最多一个注册进程；重复 start 返回冲突。
- 现有手动导入、Device OAuth、官方注册引导继续可用。

### 1.2 非目标（首期明确不做）

- `xai_enroller` 认证/OAuth 出货流水线
- 独立 email-service 面板与 Cloudflare Worker 管理 UI
- Linux/macOS 同等打包与依赖安装
- 把注册逻辑重写为 Go
- 自动把 SSO 转 Build / 自动 Device Flow 出货
- 把注册机做成公网开放接口（仅管理员鉴权）

## 2. 背景

当前分支已有：

- Registration 页面：官方注册、Device OAuth、粘贴/文件 SSO、选择输出目录导入
- Windows 打包/部署脚本
- 现成 Web/Console 批量导入 API

缺失：

- 真正运行注册机的后端能力
- 引擎源码与 Python/CloakBrowser 依赖分发
- 进程生命周期、进度、脱敏日志

外部 `grok-free-register-oss` 已有成熟 Python 注册引擎；`grok-windows-bundle` 已验证“管理服务启停子进程 + WebUI 导入”模式。本设计把该模式适配到 Go + React 架构的 `grok2api`。

## 3. 架构

```text
Admin React Registration Page
        │  现有管理鉴权（Cookie/JWT）
        ▼
Go Admin API  /api/admin/v1/accounts/windows-register/*
        │
        ├─ application/windowsregister   参数校验、状态编排、导入编排
        ├─ infra/windowsregister         子进程、路径探测、日志脱敏、accounts 解析
        ├─ application/account           复用 ImportWeb / ImportConsole
        └─ data/windows-register/        运行时输出（gitignore）

tools/windows-register/                  裁剪后的 Python 引擎（入库）
  grok_register/
  requirements.txt
  setup.ps1 / setup.cmd / README.md
```

### 3.1 边界原则

1. **Go 是唯一管理面**：前端不直接 spawn Python。
2. **单进程**：全局最多一个注册 worker。
3. **日志必须脱敏**。
4. **非 Windows 不可用**。
5. **导入复用现有账号导入链路**，不另起一套账号写入逻辑。
6. **引擎输出目录由 Go 注入**，不依赖用户手工整理 `keys/`。

### 3.2 组件职责

| 组件 | 职责 | 不做 |
| --- | --- | --- |
| `infra/windowsregister.Service` | 探测 Python/引擎/浏览器；启停子进程；读 stdout 脱敏；解析 `accounts.txt`；提供状态快照 | 不写账号库 |
| `application/windowsregister.Service` | start 参数校验；调用 infra；把结果编排到 Web/Console import | 不直接 `exec` |
| `transport/http/account` handlers | 路由、DTO、错误映射、管理鉴权下的审计 | 不解析业务状态机细节 |
| Registration 前端 | 表单、轮询、进度/日志、导入按钮 | 不本地执行引擎 |
| `tools/windows-register` | 纯注册引擎与 Windows 安装脚本 | 不提供管理 UI |

## 4. 目录与分发

### 4.1 仓库新增/修改

```text
tools/windows-register/                 # 新增：裁剪后的引擎
backend/internal/application/windowsregister/
backend/internal/infra/windowsregister/
backend/internal/transport/http/account/  # 增加 windows-register 路由
backend/internal/infra/config/            # 可选 windowsRegister 配置段
frontend/src/features/registration/       # API 客户端 + 页面面板
docs/superpowers/specs/                   # 本设计
WINDOWS_DEPLOYMENT.md / README*.md        # Windows 注册机说明
.gitignore                                # 放行 docs/superpowers；忽略引擎 venv/keys
scripts/windows/package.ps1               # 打包时纳入引擎源码，排除 venv/keys
```

### 4.2 引擎裁剪规则

从 `grok-free-register-oss` 并入时：

**保留**

- `grok_register/`（含 `core/`、`register.py`、`clearance.py`、`email_server.py` 源码文件；首期可不暴露 email server UI）
- `requirements.txt`
- Windows `setup.ps1` / `setup.cmd` / `start.ps1` / `start.cmd`（可精简为 setup + 说明）
- `README.md` / `README_WINDOWS.md` / `LICENSE`（如适用）
- 必要的最小测试或 smoke（可选）

**排除**

- `.venv/`、`keys/`、`logs/`、`.env`、缓存、`__pycache__`
- `xai_enroller/`
- `tools/GrokSession2CPAAndSub2API`
- 与认证出货强绑定的脚本（如 `push_keys_to_auth.sh`、`reset_pipeline.sh` 中 auth 部分）

**必须适配**

上游独立包当前把结果写到 `keys/accounts.txt`。并入后必须支持 Go 注入：

```text
REGISTER_OUTPUT_DIR=<abs path to data/windows-register>
```

实现方式：以 `grok-windows-bundle` 中已支持 `REGISTER_OUTPUT_DIR` 的 `register.py` 为基准合并/补丁，确保：

- `accounts.txt` / `grok.txt` 写到 `REGISTER_OUTPUT_DIR`
- 目录不存在时自动创建

### 4.3 运行时路径

相对 `config.yaml` 所在目录（与 `data/backend.db`、`data/media` 一致）：

| 用途 | 默认路径 |
| --- | --- |
| 引擎根 | `./tools/windows-register` |
| 输出目录 | `./data/windows-register` |
| 账号结果 | `./data/windows-register/accounts.txt` |
| SSO 列表 | `./data/windows-register/grok.txt` |
| 推荐 venv Python | `./tools/windows-register/.venv/Scripts/python.exe` |

### 4.4 配置

首期支持环境变量，并在 `config.example.yaml` 增加可选段：

```yaml
windowsRegister:
  # [按需修改] 仅 Windows 生效；其他平台强制不可用。
  enabled: true
  enginePath: "./tools/windows-register"
  outputDir: "./data/windows-register"
  # 空=自动探测：GROK2API_REGISTER_PYTHON > 引擎 .venv > py -3 > python
  pythonPath: ""
```

环境变量覆盖：

- `GROK2API_WINDOWS_REGISTER_DIR` → outputDir
- `GROK2API_REGISTER_ENGINE_PATH` → enginePath
- `GROK2API_REGISTER_PYTHON` → pythonPath
- `CLOAKBROWSER_EXECUTABLE_PATH` → 浏览器可执行文件（传给子进程环境）

### 4.5 Python 与浏览器探测顺序

**Python**

1. 配置/环境变量显式路径且文件存在
2. `enginePath/.venv/Scripts/python.exe`
3. `py -3`（Windows launcher）
4. `python` / `python3`（PATH）

**浏览器**

1. `CLOAKBROWSER_EXECUTABLE_PATH` / `XAI_ENROLLER_BROWSER_EXECUTABLE`
2. `%USERPROFILE%\.cloakbrowser\chromium-*\chrome.exe`
3. `%LOCALAPPDATA%\cloakbrowser\**\chrome.exe`

**包就绪**

- 能用选定 Python import `grok_register.register`、`playwright`、`cloakbrowser`
- 或至少确认 `enginePath/grok_register/register.py` 存在且 requirements 已安装（实现时优先真实 import 探测）

## 5. 状态机与 API

### 5.1 状态

```text
idle → starting → running → completed
                     │
                     ├→ stopping → stopped
                     └→ error
```

| 状态 | 含义 |
| --- | --- |
| `idle` | 从未启动或已重置 |
| `starting` | 已接受 start，进程尚未确认 running |
| `running` | 子进程存活 |
| `stopping` | 已请求停止 |
| `stopped` | 用户停止完成 |
| `completed` | 进程 exit 0 |
| `error` | 启动失败或非 0 退出 |

约束：

- `running` / `starting` / `stopping` 时再次 `start` → `409`
- 进程异常退出 → `error`，保留 `lastError` 与日志
- 服务重启后：不恢复旧子进程；根据 `accounts.txt` 仍可 `import all`；状态回到 `idle`（或 `completed` 若需要展示上次文件计数，首期选 `idle` + 文件计数）

### 5.2 路由

均挂在现有管理保护组：`/api/admin/v1`

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/accounts/windows-register/status` | 就绪、状态、计数、脱敏日志 |
| POST | `/accounts/windows-register/start` | 启动 |
| POST | `/accounts/windows-register/stop` | 停止 |
| POST | `/accounts/windows-register/import` | 导入注册结果到 Web/Console |

### 5.3 DTO

#### status 响应

```json
{
  "platformSupported": true,
  "ready": false,
  "missing": ["python", "cloakbrowser", "browser"],
  "browserInstalled": false,
  "state": "idle",
  "running": false,
  "target": 0,
  "success": 0,
  "failed": 0,
  "rateLimited": 0,
  "percent": 0,
  "generatedThisRun": 0,
  "generatedTotal": 0,
  "canImportCurrent": false,
  "canImportAll": false,
  "startedAt": null,
  "finishedAt": null,
  "elapsedSec": 0,
  "exitCode": null,
  "lastError": "",
  "logs": []
}
```

说明：

- `success` = max(本次 `accounts.txt` 新增条数, 日志中解析到的成功事件数)
- `generatedThisRun` = 当前文件总条数 − start 时 baseline
- `logs` 为环形缓冲最近 N 条（建议 300），已脱敏
- 非 Windows：`platformSupported=false`，`ready=false`，`missing` 含 `windows`

#### start 请求

```json
{
  "target": 10,
  "emailMode": "tempmail",
  "emailApi": "",
  "emailDomain": "",
  "proxy": "",
  "maxMem": "",
  "debug": false
}
```

校验：

- `target`：1..10000
- `emailMode`：`tempmail` | `custom`
- `custom` 时 `emailApi`、`emailDomain` 必填
- `proxy` / `maxMem` 可选；服务端做长度与明显非法字符校验，不在日志中回显完整 proxy 凭据

启动命令（概念）：

```text
<python> -u -m grok_register.register --target <n> [--max-mem ...] [--debug]
cwd = enginePath
env:
  PYTHONUTF8=1
  PYTHONUNBUFFERED=1
  REGISTER_OUTPUT_DIR=<abs outputDir>
  REGISTER_LOG_MODE=user|debug
  EMAIL_MODE=...
  EMAIL_API=...            # optional
  EMAIL_DOMAIN=...         # optional
  REGISTER_PROXY=...       # optional
  CLOAKBROWSER_EXECUTABLE_PATH=... # if known
```

Windows 进程创建：

- 新进程组，便于 CTRL_BREAK / taskkill 树杀
- 隐藏控制台窗口
- stdout+stderr 合并为管道，按行读取

#### stop

- 先 CTRL_BREAK / terminate
- 超时后 `taskkill /PID /T /F`
- 返回最新 status

#### import 请求

```json
{
  "scope": "current",
  "destinations": ["grok_web", "grok_console"]
}
```

| 字段 | 规则 |
| --- | --- |
| `scope` | `current`（本次 baseline 之后）或 `all` |
| `destinations` | 默认 `["grok_web","grok_console"]`；允许子集 |

行为：

1. 从 `accounts.txt` 读取 `email:password:sso` 行，去重 SSO。
2. 按 scope 切片。
3. 为空 → `400`
4. 将 SSO token 列表编码为现有导入可识别的文本/JSON 文档。
5. 依次调用现有 `ImportWebCredentials*` / `ImportConsoleCredentials*`（或 documents 变体）。
6. 返回汇总：

```json
{
  "scope": "current",
  "sourceCount": 12,
  "results": [
    {"provider": "grok_web", "imported": 10, "skipped": 2, "syncFailed": 0, "accountIds": ["..."]},
    {"provider": "grok_console", "imported": 10, "skipped": 2, "syncFailed": 0, "accountIds": ["..."]}
  ]
}
```

导入格式兼容：

- 现有前端 `credential-parser` 与后端 Web/Console import 已支持纯 SSO token 行，以及 `email:password:sso` / `email----password----sso`。
- 服务端 import 优先提交 **仅 SSO token 列表**（每行一个），避免把密码写入导入审计面；密码仍保留在本地 `accounts.txt` 供排障，但不进入账号库导入路径。

## 6. 日志脱敏

对子进程每一行 stdout 做：

1. 去 ANSI / NUL
2. JWT 形态 → `[token hidden]`
3. `sso|password|passwd|cookie|authorization|token = value` → `key=[hidden]`
4. `scheme://user:pass@host` → `scheme://***:***@host`
5. 邮箱 → `a***@domain`
6. 截断到 2000 字符

计数启发式（与 bundle 一致，可中英）：

- 成功：`注册成功` / `registration succeeded`
- 失败：`注册失败` / `registration failed`
- 限流：`触发限流` / `rate limit`

监控只读日志与文件计数，不修改引擎内部队列。

## 7. 前端设计

### 7.1 页面结构（Registration）

在现有页面顶部安全说明之后、或 step 1/2 之前，增加 **Windows 注册机** 主面板：

1. 就绪状态条：平台/Python/依赖/浏览器缺失项
2. 参数区：
   - 目标数量
   - 邮箱模式 tempmail/custom（custom 显示 API/域名）
   - 代理（可选）
   - 内存上限（可选）
   - Debug 开关（可选，默认关）
3. 操作：开始 / 停止
4. 进度：success/failed/rateLimited/percent/elapsed
5. 日志窗口：只读、自动滚底、展示脱敏 logs
6. 导入区：
   - 导入本次结果
   - 导入全部结果
   - 导入中复用现有进度展示风格

保留现有：

- 官方注册引导
- Device OAuth
- 手动粘贴/文件/输出目录导入

非 Windows 或 `platformSupported=false`：

- 面板显示不可用说明，不渲染 start 表单；手动导入仍可用。

### 7.2 前端 API

新增 `frontend/src/features/registration/windows-register-api.ts`：

- `getWindowsRegisterStatus()`
- `startWindowsRegister(body)`
- `stopWindowsRegister()`
- `importWindowsRegister(body)`

轮询：

- `running|starting|stopping` 时每 1–2s 拉 status
- 空闲时进入页面拉一次；用户操作后立即刷新

### 7.3 i18n

解决当前 `frontend/src/shared/i18n/index.ts` 未合并冲突时，一并加入：

- nav.registration（已有草稿）
- registration.* 文案
- windowsRegister.*：就绪、缺失组件、start/stop/import、状态名、错误

中英文都要有。

## 8. 打包与部署

### 8.1 源码开发

- `tools/windows-register/setup.ps1`：创建 `.venv`、装 requirements、安装/检测 CloakBrowser、可选 smoke（data: URL，不访问 xAI）
- 文档写清：先 setup 引擎，再启动 grok2api，再在 WebUI 点注册

### 8.2 `package.ps1` / 发布 ZIP

- **包含**：`tools/windows-register` 源码与 README/setup 脚本、requirements
- **排除**：`.venv`、`keys`、`.env`、logs、`__pycache__`、本机 accounts
- 发布包说明：目标机需 Python 3.10+；首次在包内运行 setup，或使用文档中的一键步骤
- **不**把系统 Python 打进 ZIP（体积与授权复杂）；与当前“后端自包含 Go 二进制”策略并存：API 自包含，注册机附属需 Python

### 8.3 `deploy.ps1`

- 不默认自动安装 Python/浏览器（避免静默改系统）
- status/logs 中可提示注册机未就绪
- 若未来加 `-WithRegisterRuntime`，必须显式开关；首期不做强制安装

### 8.4 安全

- 仅管理员可访问 windows-register API
- 输出目录 ACL 跟随 `data/`（LOCAL SERVICE 可写 data）
- 引擎目录只读即可
- 审计：`windows_register.start|stop|import`，summary 不含 proxy 明文/token
- 公网部署文档强调：注册机会启动本机浏览器并产生真实账号操作，务必只在受控环境使用

## 9. 错误处理

| 场景 | HTTP | 行为 |
| --- | --- | --- |
| 非 Windows | 503 | status 可 200 且 `platformSupported=false`；start/stop/import → 503 |
| 缺 Python/依赖/浏览器 | 503 | start 失败，status.missing 列出 |
| 已在运行 | 409 | start 拒绝 |
| 参数非法 | 400 | 明确字段错误 |
| 无可导入结果 | 400 | scope 对应文案 |
| 子进程崩溃 | status=error | lastError 脱敏；允许再次 start |
| 导入中部分 provider 失败 | 200 或 207 风格 | 在 results 中分别返回；首期可用 200 + 各 provider 结果/错误字段 |
| 服务关闭 | - | stop 子进程（尽量），避免孤儿浏览器；实现 shutdown hook |

## 10. 测试计划

### 10.1 Go 单测

- 日志脱敏用例（token/邮箱/代理/密码）
- `accounts.txt` 解析与 scope 切片
- 状态机：重复 start、stop 无进程、exit 0/非 0
- 非 Windows 短路
- process factory 可注入 fake process（不真正起浏览器）

### 10.2 HTTP 单测

- 鉴权保护
- start 校验
- import 空结果
- status 形状

### 10.3 前端

- status 轮询启停
- 非 ready 禁用 start
- i18n key 存在（中英）

### 10.4 手工验收（Windows）

1. setup 引擎
2. 启动 grok2api
3. Registration 页看到 ready
4. target=1 tempmail 试跑（需代理/网络按环境）
5. 日志脱敏可见
6. 导入本次结果到 Web+Console
7. stop / 完成后状态正确
8. package 产物含引擎源码、不含 venv/keys

## 11. 实现顺序

1. 解决 `i18n/index.ts` 冲突，保留 registration 文案基础
2. 并入并裁剪 `tools/windows-register`，打上 `REGISTER_OUTPUT_DIR` 支持
3. 实现 Go infra 进程服务 + 单测
4. 实现 application + HTTP 路由 + 接线
5. 前端 API + Registration 面板
6. 配置示例、README / WINDOWS_DEPLOYMENT 说明
7. package.ps1 白名单纳入引擎
8. 本地 Windows 冒烟

## 12. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 上游引擎硬编码 `keys/` | 并入时强制 `REGISTER_OUTPUT_DIR` 补丁，并加单测/文档 |
| CloakBrowser/Playwright 体积与首次安装失败 | setup 脚本明确步骤；status.missing 可操作提示 |
| 孤儿 chrome 进程 | stop 时进程树 kill；start 前可选清理无主进程（若引擎已有则复用） |
| 凭据落盘 | `data/windows-register` gitignore；文档强调勿上传；导入路径不传密码 |
| 与现有 i18n 冲突 | 实现第一步先合并冲突 |
| 发布包用户缺 Python | 文档前置要求；status 明确缺失项，不静默失败 |

## 13. 决策记录

- 融合深度：完整 WebUI 内置
- 运行方式：Go 管理 Python 子进程
- 平台：仅 Windows
- 首期能力：注册 + 导入（Web + Console）
- 实现路径：附属引擎 + Go 生命周期（方案 A）
