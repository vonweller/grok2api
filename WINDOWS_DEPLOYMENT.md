# Windows 一键打包与部署

本方案面向 64 位 Windows 10/11 和 Windows Server 2016 及以上版本。发布包是原生、自包含的 Windows 程序，服务器不需要安装 Go、Node.js、pnpm、SQLite 或 VC++ 运行库。

> Linux 服务器仍建议使用项目已有的 Docker Compose 部署，不要在 Linux 上运行 BAT。

## 一键打包

在仓库根目录双击 `package.bat`，或在 PowerShell/CMD 中运行：

```bat
package.bat
```

脚本会完成以下工作：

1. 检测 Go 1.26+、Node.js 22.12+ 和 pnpm 11.5.2+。
2. 缺少工具时，从 Go、Node.js、pnpm 官方发布源下载，校验固定的 SHA-256/SHA-512，并安装到仓库的 `.tools` 目录，不修改系统环境。
3. 安装锁定的前端依赖，执行前端 lint、后端 test/vet，并构建前后端。
4. 默认生成 `windows/amd64` 和 `windows/arm64` 两个 ZIP。
5. 在 `release/SHA256SUMS.txt` 中写入发布包校验值。

只打包常见的 x64 服务器版本：

```bat
package.bat amd64
```

开发阶段需要跳过 lint/test/vet 时可使用：

```bat
package.bat amd64 -SkipChecks
```

正式发布不建议使用 `-SkipChecks`。

发布包采用白名单复制，不包含本机的 `config.yaml`、`.env`、数据库、媒体、日志、账号数据、源码、`node_modules` 或构建缓存。

## 一键部署

> 仓库根目录中的 `deploy.bat` 不能直接启动源码，因为源码目录还没有 `grok2api.exe`。必须先运行 `package.bat`，再解压 `release` 中对应架构的 ZIP，并运行解压目录里的 `deploy.bat`。直接双击 BAT 时窗口会在结束后保留，便于查看成功信息或错误原因。

1. 将与服务器架构匹配的 ZIP 上传到服务器并解压到本地 NTFS 磁盘，例如 `D:\Services\grok2api`。
2. 通过可信渠道同时取得 `SHA256SUMS.txt`，在解压前核对 ZIP 的 SHA-256；不要把包内清单当作发布者身份签名。
3. 双击解压目录中的 `deploy.bat`，接受一次 UAC 管理员授权。
4. 脚本会检测包内逐文件完整性和架构，生成首次安全配置，注册以 `Grok2API-` 开头且与部署目录绑定的开机启动任务并启动程序。
5. 启动任务使用 Windows 内置的低权限 `LOCAL SERVICE` 账户。程序、脚本、前端和配置对该账户只读，只有 `data` 与 `logs` 可写；首次密码文件不向该账户授权。
6. 打开解压目录中的 `FIRST_RUN_CREDENTIALS.txt` 获取首次管理员密码。登录并修改密码后删除该文件。

可使用系统自带命令核对 ZIP：

```bat
certutil -hashfile grok2api-3.0.4-windows-amd64.zip SHA256
```

默认监听所有网卡的 `8000` 端口，但脚本不会静默修改 Windows 防火墙。指定其他端口：

```bat
deploy.bat install 8080
```

也可以提前设置 `GROK2API_PORT` 环境变量。端口会保存到 `data/grok2api.port`，后续维护命令会继续使用该端口。

更改已安装实例的端口应使用 `deploy.bat restart 新端口` 或重新执行 `deploy.bat install 新端口`；不要在实例仍运行时只执行 `start 新端口`。

## 运维命令

```bat
deploy.bat status
deploy.bat logs
deploy.bat stop
deploy.bat start
deploy.bat restart
deploy.bat uninstall
```

`start` 只启动已经安装的低权限计划任务；如果任务尚未安装或已经卸载，请先执行 `deploy.bat install [端口]`。`uninstall` 只删除开机启动任务，保留 `config.yaml`、`data` 和日志。排障时可在当前控制台运行：

```bat
deploy.bat run
```

`/healthz` 成功表示程序已启动。首次部署时 `/readyz` 可能在尚未添加上游账号、模型路由之前返回未就绪，这是正常状态。

Windows 后台进程当前不实现 Service Control Manager 的优雅关闭协议，`stop`、`restart` 和 `uninstall` 会强制结束进程。SQLite WAL 通常可以恢复，但正式备份仍应先停止实例，确认 `deploy.bat status` 显示已停止，再复制配置、数据库与媒体。每次启动或重启会先归档非空的 stdout、stderr 与任务错误日志，并为每类日志保留最近 5 份；运行中的日志不会按大小实时切割。

## 配置、数据与升级

首次运行仅在 `config.yaml` 不存在时生成：

- 64 个 hex 字符（256 位）的 JWT 密钥；
- Base64 编码的 32 字节凭据加密密钥；
- 随机管理员密码。

脚本发现已有 `config.yaml` 时绝不会覆盖或重新生成密钥。若配置缺失但 `data` 中已有任何运行数据，脚本会拒绝初始化并要求恢复原配置。`credentialEncryptionKey` 与数据库中的加密凭据绑定，丢失或更换后已有上游凭据将无法解密。

升级前执行：

```bat
deploy.bat stop
```

完整备份以下内容，然后替换程序文件并重新执行 `deploy.bat install`：

- `config.yaml`
- `data/backend.db`、可能存在的 `backend.db-wal` 与 `backend.db-shm`
- `data/media`

不要把 SQLite 数据库或媒体目录放在 FAT/exFAT、SUBST、映射盘或普通网络共享中。项目使用 SQLite WAL、本地硬链接和 Windows ACL，部署脚本只接受本机固定 NTFS 磁盘上的普通目录，也拒绝目录联接和符号链接。

## 公网安全

- 不要直接把管理端暴露到公网；使用 Nginx、Caddy 或 IIS 反向代理并启用 HTTPS。
- `LOCAL SERVICE` 是 Windows 内置的整机共享身份，并非每个应用独占账号；不要在同一服务器上以该身份运行不可信服务。需要强实例隔离时应改用专用服务账号或容器。
- HTTPS 部署应在 `config.yaml` 中设置 `auth.secureCookies: true`。
- 只开放确实需要的 Windows 防火墙端口，并限制管理端来源。
- 定期备份 `config.yaml`、数据库与媒体目录。
- `FIRST_RUN_CREDENTIALS.txt` 在修改首次密码后应立即删除。

## Windows 浏览器注册机（可选）

发布包会附带 `tools/windows-register` 引擎源码，但**不会**捆绑 Python 运行时、`.venv` 或浏览器。注册机会由 Go 后端作为子进程管理，仅管理员可在 WebUI 中操作。

### 首次准备

1. 在目标 Windows 机器安装 Python 3.10+（可加入 PATH，或稍后配置 `pythonPath`）。
2. 在部署目录打开 PowerShell：

```powershell
cd tools\windows-register
powershell -ExecutionPolicy Bypass -File .\setup.ps1 -SmokeTest
```

3. 启动/重启 grok2api。
4. 登录管理后台 → **注册** 页面 → **Windows 浏览器注册机**。
5. 确认状态显示“运行环境就绪”后，设置目标数量并开始注册。
6. 注册完成后点击“导入本次结果”或“导入全部结果”，写入 Web / Console 账号池。

### 运行时路径

| 用途 | 默认路径 |
| --- | --- |
| 引擎 | `tools/windows-register` |
| 输出 | `data/windows-register/accounts.txt` |
| 推荐 venv | `tools/windows-register/.venv/Scripts/python.exe` |

可用环境变量覆盖：

- `GROK2API_REGISTER_ENGINE_PATH`
- `GROK2API_WINDOWS_REGISTER_DIR`
- `GROK2API_REGISTER_PYTHON`
- `CLOAKBROWSER_EXECUTABLE_PATH`

### 安全注意

- `data/windows-register` 含真实账号凭据，勿上传到 Git 或共享盘。
- 管理 API 日志已脱敏；仍不要把完整日志发到公开渠道。
- 注册机会启动本机浏览器并访问上游站点，只在你有权操作的受控环境使用。
