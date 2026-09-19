# CPA Codex Ticket

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

适用于 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的**实验性原生动态库插件**：按账号与模型采集、缓存 Codex turn-state，并在 HTTP/SSE 请求中注入；附带中文采票代理设置页。

> **不是 OpenAI、CLIProxyAPI 或 Sub2API 的官方产品，不保证“满血”或推理质量。**
> `292` 是候选响应头的预期字符串长度，**不是 HTTP 状态码**。本地 TTL 不代表上游真实有效期。跨轮复用 turn-state 可能与上游路由语义冲突，请自行评估风险。

## 功能

- 自定义 **HTTP / HTTPS / SOCKS5 / SOCKS5h** 采票代理，支持认证。
- 下拉选择 CPA 已配置的**全局代理、provider 配置代理、文件型账号专用代理**。
- 只改变采票出口，不修改 CPA 核心业务代理或账号凭据。
- 保存后取消旧探测、清除旧候选票据并重新采集，无需重启 CPA。
- 内存缓存按账号索引、身份、实际模型隔离；账号改绑会失效。
- 有界并发、取消、指数退避与 `Retry-After`；代理失效不会偷偷直连。
- 默认保留客户端已有的 turn-state；没有可用票据时放行原请求。
- 状态页、连接测试、手动刷新、暂停/恢复；不回显票据或代理密码。

## 兼容范围与限制

- 已对 CLIProxyAPI **v7.3.8** 测试，C ABI **1**、RPC schema **6**。其他版本需自行验证。
- 当前源码/发布目标为 **Linux amd64 + glibc**；未验证 ARM、Windows、macOS、musl/Alpine。
- 仅支持 **HTTP/SSE**，跳过 WebSocket；不提供调度器覆盖或严格 fail-closed。
- 仅从有效、文件型 Codex OAuth 账号采票；runtime-only 账号不支持。
- SOCKS5 与 SOCKS5h 在当前实现中都将目标域名交给代理解析。
- 模型列表是精确的上游模型名称；请按自己的账号权限修改 `models`。默认名称不代表账号一定具备访问权限。
- 账号代理列表最多检查 256 条账号，超出时显示部分加载提示；不影响全局代理选择。账号运行信息缓存最多两秒。
- 配置数组中的代理按非代理内容身份匹配，而非数组位置；删除、身份修改或重复歧义需重新选择。
- “测试代理”只做不带账号凭据的固定 HTTPS 请求；HTTP 401 可证明目标可达，**不能证明采票或模型质量**。

## 安装

### 1. 获取动态库

从 [Releases](https://github.com/Su-cyber-art/cpa-plugin-codex-ticket/releases) 下载 Linux amd64 ZIP 与 `checksums.txt`，或按下文从源码构建。

```bash
sha256sum -c checksums.txt
unzip codex-ticket_0.2.0_linux_amd64.zip
```

将 `codex-ticket.so` 放进 CPA 配置的插件目录，例如：

```text
plugins/linux/amd64/codex-ticket.so
```

必须以 **`codex-ticket.so`** 命名，插件 ID 来自动态库文件名。动态库包含原生代码，安装前请审查来源。

### 2. 准备私密目录

选择 CPA 进程可读写、容器内可见的持久目录。以下是容器内路径示例，请按实际挂载调整：

```bash
install -d -m 0700 /CLIProxyAPI/plugins/codex-ticket-private
```

目录及代理文件必须归 CPA 运行用户所有。无需先在文件中手写代理密码，设置页可以创建文件。代理文件是 **0600 权限的明文文件，不是加密保险库**；请同时保护备份。

### 3. 合并配置并重启 CPA

参考 [`config.example.yaml`](config.example.yaml)，保留原有其他设置。首次配置建议只启用插件页面，不立即采票：

```yaml
commercial-mode: true
request-log: false
plugins:
  enabled: true
  dir: plugins
  configs:
    codex-ticket:
      enabled: true
      priority: 10
      harvest_enabled: false
      inject_enabled: false
      proxy_file: /CLIProxyAPI/plugins/codex-ticket-private/harvest-proxy.url
      host_config_file: /CLIProxyAPI/config.yaml
      models: gpt-6-astra,gpt-5.6-sol # 按实际模型权限修改
      target_length: 292
      ttl_seconds: 3600
      refresh_before_seconds: 600
      scan_interval_seconds: 30
      timeout_seconds: 25
      max_concurrency: 1
      retry_base_seconds: 60
      retry_max_seconds: 1800
      replace_existing: false
```

**日志安全前提：** CPA v7.3.8 在 `request-log: false` 时仍可能保存失败请求的原始上游头。必须在 CPA **启动时**启用 `commercial-mode: true`，以去掉该请求日志中间件。修改后只重载配置不够，需要重启 CPA。普通运行日志和用量统计仍保留。

### 4. 在页面中设置代理

在支持插件菜单的管理面板打开 **Codex 采票代理**，或直接访问 CPA 同源路径：

```text
https://<你的-CPA-域名>/v0/resource/plugins/codex-ticket/settings
```

- 输入 **CPA Management Key**，不是模型 API Key，也不是 CPAMP 管理密钥。
- 选择自定义代理并输入完整 URL，或选择已有核心代理。
- 点击“保存并立即生效”，再点击“测试已保存代理”。
- 确认模型和代理后，将插件的 `harvest_enabled`、`inject_enabled` 设为 `true`。

页面只在内存保存管理密钥，不写入 URL、Cookie、localStorage 或 sessionStorage。刷新页面后需重新连接。非本机明文 HTTP 会被阻止，请使用 HTTPS 或 localhost SSH 隧道。

### 5. 观察状态

候选票据需要上游 **HTTP 200 + 指定长度 + `gAAAAA` 前缀**。采集器收到头后立即关闭响应流，不读取完整回答；采票仍可能消耗账号额度。

查看缓存数量、最近 HTTP、剩余时间、退避和注入次数。`injected_count` 只能证明插件钩子进行了注入，不能证明上游接纳了某种质量权限。

## 安全与代理行为

- 自定义代理支持 `http://`、`https://`、`socks5://`、`socks5h://`；拒绝任意路径、查询参数、片段、重定向和直连回退。
- 核心代理以引用保存，读取当前值；不会复制到公开插件配置，也不会改核心代理。
- 旧版本的单行 URL 私密文件仍可读取；设置页保存后使用 JSON 格式。
- 自定义输入留空仅在已有自定义代理时表示保持不变。存量用户名与密码从不回填。
- 私密文件写入采用临时文件、fsync 和原子替换；目录同步失败会报告“已提交但持久化未确认”。
- 公开资源仅提供静态 HTML；代理选项、状态、写入和测试接口均走 CPA 管理鉴权。
- CPA host callbacks 是同步调用，无法在 C 调用内部强制取消；网络探测设置了独立超时。
- 上游响应正文可能包含原生不透明元数据；插件不会重写响应正文或用量数据库，不能声称所有原生元数据都已脱敏。

更多说明见 [SECURITY.md](SECURITY.md)。

## 管理接口

以下接口使用 CPA Management API 的 `Authorization: Bearer <management-key>`：

- `GET /v0/management/codex-ticket/status`：状态与脱敏统计。
- `GET /v0/management/codex-ticket/proxy-options`：当前选择及核心代理选项。
- `POST /v0/management/codex-ticket/proxy`：保存自定义代理或核心引用。
- `POST /v0/management/codex-ticket/proxy-test`：测试已保存的代理。
- `POST /v0/management/codex-ticket/refresh`：请求扫描，保留已有退避。
- `POST /v0/management/codex-ticket/pause`：暂停当前进程的采票与注入。
- `POST /v0/management/codex-ticket/resume`：按持久开关恢复。

`pause` 状态不跨 CPA 重启。要持久停用，请关闭 `harvest_enabled`、`inject_enabled` 或插件的 `enabled`。

## 开发与测试

后续开发与排障请先阅读 [agent.md — Agent / 维护者工作指南](agent.md)，包含架构导航、安全约束、采票状态解读、已知缺口和提交发布流程。

依赖：Linux、Go **1.26+**（允许自动工具链）、CGO/C 编译器、Python 3；UI 测试需要 Node.js **24+**。Go 依赖锁定 CPA SDK v7.3.8，无本机路径 replace。

```bash
git clone https://github.com/Su-cyber-art/cpa-plugin-codex-ticket.git
cd cpa-plugin-codex-ticket
make test
make build
python3 scripts/abi_smoke.py dist/codex-ticket.so
npm ci
npm test
```

输出为 `dist/codex-ticket.so`。运行 `python3 scripts/package_release.py 0.2.0` 可生成商店命名格式的 ZIP 和 SHA-256 清单。ZIP 根目录包含同名动态库及许可证声明；源码仓库不提交二进制、私密配置或生产验收记录。

测试覆盖账号/模型隔离、代理格式与切换、失效回退、配置条目重排/删除、部分发现、生命周期、错误日志门控、ABI 和 UI 鉴权/脱敏交互。单元测试使用合成凭据与合成票据，不代表真实上游质量。

GLIBC 符号要求依赖构建环境；请检查发布说明或用 `readelf --version-info dist/codex-ticket.so` 核对。不要把 glibc 产物直接当作 musl 产物使用。

## 停用与移除

先在 CPA 配置中停用插件，重启核心，再删除动态库、插件配置块和插件专用私密目录。保留 CPA 账号、全局代理、其他插件与用量数据库。不要删除共享目录。

## License

[MIT](LICENSE)，Copyright © 2026 Yuesaki。上游 SDK/示例归属见 [NOTICE](NOTICE)。本项目独立维护，未默认提交至官方插件商店。
