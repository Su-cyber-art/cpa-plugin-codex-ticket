# Agent / 维护者工作指南

本文件用于后续开发、排障与发布交接，适用于整个仓库。维护前先阅读 [README.md](README.md)、[SECURITY.md](SECURITY.md) 和本文件，再核对当前源码与远端状态。若工具不会自动加载小写 `agent.md`，请显式将本文件加入上下文。

## 1. 项目定位与权限边界

- 仓库：<https://github.com/Su-cyber-art/cpa-plugin-codex-ticket>。
- 实验性 CLIProxyAPI（CPA）原生动态库插件，独立维护，不修改 CPA 核心；MIT 许可，保留上游声明。
- 功能是采集、缓存和注入 Codex turn-state，不保证模型质量、推理额度或所谓“满血”。`292` 是候选头长度，不是 HTTP 状态码。
- 发布源码、发布 Release、提交官方商店收录、部署线上是四项独立操作。只完成当前明确授权的范围；文档修改不需要重启服务。
- 不把某台机器的域名、SSH 别名、IP、账号索引、绝对工作区路径或生产数据当成项目默认配置。先发现实际环境，再执行运维操作。
- 不修改无关服务、账号文件、核心业务代理、用量数据库或其他插件。线上改动前说明范围，保留必要恢复手段，完成后读回验证。

## 2. 当前兼容基线

以代码、依赖和 Release 为准，不把本节当成永不变化的版本承诺：

- 插件基线 `0.2.0`；CPA SDK 锁定 `v7.3.8`；C ABI 1 / RPC schema 6。
- 已验证 Linux amd64 + glibc，使用 Go 1.26+、CGO/C 编译器；Python 3 用于 ABI/打包脚本；Node.js 24+ 用于 UI 测试。
- 只支持 HTTP/SSE，明确跳过 WebSocket；仅支持有效的文件型 Codex OAuth 账号。
- `codex-ticket.so` 的文件名对应插件 ID，不得随意改名。安装位置由 CPA 插件目录决定，示例为 `plugins/linux/amd64/codex-ticket.so`。
- glibc 最低要求取决于实际构建产物。对每次新发布运行 `readelf --version-info`，不要从旧发布照抄；未测试的平台不可写“支持”。
- 当前仓库没有 GitHub Actions 工作流。只有本地测试时，应明确说“本地通过”，不能说远端 CI 通过。

## 3. 代码导航

| 路径 | 职责 / 修改关注点 |
| --- | --- |
| `cmd/codex-ticket/main.go` | C ABI 导出、内存释放、host callback 桥接；不能只跑 Go 单测而跳过动态库 smoke |
| `internal/ticket/rpc.go` | 注册元数据、能力、RPC 分发、生命周期与管理路由；版本号在 `registration()` |
| `internal/ticket/config.go` | 默认值、边界校验、有限文件读取、核心配置与日志安全门控 |
| `internal/ticket/auth.go` | 账号资格、OAuth 凭据解析及身份隔离；不得输出原始凭据/错误 |
| `internal/ticket/engine.go` | worker、扫描、并发、内存缓存、注入、完成清理及状态快照 |
| `internal/ticket/probe.go` | 固定上游探测、强制代理、候选头验证、退避 |
| `internal/ticket/proxy_settings.go` | 自定义/核心代理引用、稳定身份、脱敏列表、私密文件原子保存、连接测试 |
| `internal/ticket/page.go` | 嵌入静态页面、脚本哈希 CSP 和安全响应头 |
| `internal/ticket/ui.html` | 无外部依赖的中文页面；管理密钥只在内存保存 |
| `internal/ticket/*_test.go` | 生命周期、隔离、安全、代理引用、协议与注册回归 |
| `internal/ticket/ui.browser.test.cjs` | jsdom 交互测试，不等同于真实浏览器 E2E |
| `scripts/abi_smoke.py` | 实际加载动态库并调用 ABI 的 smoke 测试 |
| `scripts/package_release.py` | 检查 ELF 平台，生成 ZIP 与 SHA-256 清单；版本参数不自动修改源码版本 |
| `config.example.yaml` | 可公开配置示例，不含真实凭据；默认不启用采票/注入 |

## 4. 必须保留的行为与安全约束

### 生命周期、缓存与业务请求

- 配置变更/代理保存需要串行处理生命周期，取消并等待旧 worker；旧 generation、已删除或改绑账号的探测结果不得进入新缓存。
- 缓存按账号索引 + 身份 + 实际上游模型隔离。不要按邮箱或模型单独共用票据；不要把完整 OAuth token 当公开标识。
- 缓存仅在内存中；CPA 重启、重新配置、代理变化可能使缓存及其计数失效。`pause` 不跨重启，持久停用依赖配置开关。
- 没有候选票据时保持原业务请求，不能阻塞业务去同步采票。默认 `replace_existing: false`，保留客户端已有的 turn-state。
- 只移除本插件拥有的 HTTP/SSE 响应头，不改正文、SSE payload 或用量数据，也不承诺剔除了上游正文中的所有原生元数据。
- CPA host callbacks 是同步调用；Go context 不能强制打断正在运行的 C callback。不要声称设置超时就解决了所有卡顿。

### 代理与凭据

- 采票必须使用显式选择的代理；失效、删除、停用或解析失败时，不自动换代理或回退直连。
- 核心代理选择仅引用/读取核心配置，不写回业务代理。数组条目按非代理内容的身份识别，不依赖下标；删除、身份变化或歧义必须要求重选。
- 精确选择解析与下拉选项发现分开；保留有界账号发现和部分结果提示。不要为获取全局代理遍历所有账号 JSON，避免 SDK 整表复制造成放大开销。
- 代理账号列表最多检查 256 条，列表缓存最多两秒；部分发现不等同于当前选择无效。
- 私密目录 0700、文件 0600，归 CPA 运行用户所有；拒绝不安全文件类型/末级符号链接。明文凭据依靠文件权限保护，不可称为加密存储。
- 保留临时文件 + fsync + 原子替换；目录同步失败需区分“已提交但持久化未确认”，不能假装完全成功或悄悄覆盖旧配置。
- 不记录完整代理 URL、用户名/密码、OAuth token、管理密钥或票据正文。错误返回用固定原因码，不能直接回显网络库原始错误。

### 管理页面、API 与日志

- 公开资源 `/v0/resource/plugins/codex-ticket/settings` 只提供静态 HTML。状态、选项、保存、测试、刷新、暂停/恢复全部通过 CPA Management API 鉴权。
- 管理密钥只保留在页面内存；不写 URL、Cookie、浏览器持久存储、截图或 console。刷新/断开后重新连接。
- 保持同源固定 API、禁止重定向、HTTPS/loopback 限制、CSP；不引入第三方脚本或内联事件处理器。
- “代理测试”不携带账号 token，只验证固定目标可达性；测试结果中的上游 401 与管理 API 自身的鉴权失败 401 必须区分。
- CPA v7.3.8 的 `request-log: false` 不能单独阻止失败请求的原始头记录。必须在启动时设置 `commercial-mode: true` 并重启 CPA；不能把编辑 YAML 当成运行中间件已经更新。
- 不为排障打开含真实票据的原始请求日志。注入的日志安全门控要有回归测试。

## 5. 最近采票排障与可观测性缺口

**当前 `0.2.0` 没有逐次采票历史、持久化事件记录或全周期成功/失败计数。** `GET /v0/management/codex-ticket/status` 是各账号/模型的当前快照，不是日志列表。运行日志中的 `/refresh` 或 `/proxy-test` 管理请求，也不是采票上游结果。

只读排障顺序：

1. 确认实际 CPA 容器/进程、插件版本、启动时间与时区，不只看本地源码版本。
2. 通过受保护的 `/status` 获取 `harvest_active`、`inject_active`、各 reason、`cached_count`、`inflight_count` 和 entries；只输出必要脱敏字段。
3. 对照只读配置中的模型、TTL、提前刷新量、扫描周期和超时；不要打印整份配置或代理私密文件。
4. 查看现有运行日志，区分插件加载、页面操作、模型业务请求与采票探测；不要把业务请求的 504 自动归因于采票。
5. `candidate_cached` 表示最近一次通过候选头校验；`injected_count` 只表示对应缓存条目的注入累计，不代表模型质量，也不是历史采票次数。
6. `expires_at - ttl_seconds` 只能在已确认配置的前提下推算最近成功缓存时间，并明确标注“推算”；失败后的 last result 可能与仍保留的旧票据并存。
7. 自动刷新在 `expires_at - refresh_before_seconds` 附近开始进入资格窗口，实际时间还受扫描、排队和退避影响。`POST /refresh` 返回 202 仅唤醒扫描，保留有效缓存和退避，不等于强制网络采票。
8. 历史不足就直接说明，不能编造“最近 N 次”、成功率或精确采票耗时。未经授权不手动刷新、换代理、重启或发付费模型测试。

### 后续可选改进（尚未实现，不是本文件授予的开发任务）

若需求明确要求历史采票记录，再设计一个**有界、脱敏**的事件环形缓冲及受保护查询接口：

- 字段考虑时间戳/时区、受保护的账号索引、模型、耗时、HTTP 状态、票据长度、固定原因码和下次重试时间；不保存票据正文、凭据、完整代理 URL 或原始上游正文。
- 内存历史与持久化历史分开声明；若需要落盘，明确开关、权限、轮转、保留上限、清理与重启语义。
- 若提供成功率，定义统计窗口和分母，区分网络尝试、扫描跳过、取消、格式不符与合格候选；不要把管理接口 200 计为采票成功。
- 加入有界容量、并发/race、取消、重启、脱敏、鉴权、分页和 UI 回归测试，并同步 README/CHANGELOG。

## 6. 开发验证

从仓库根目录运行，不依赖某台机器的私有脚本或凭据：

```bash
git status --short
make test
make build
python3 scripts/abi_smoke.py dist/codex-ticket.so
npm ci --ignore-scripts
npm test
git diff --check
```

- `make test` 包含 race-enabled Go 测试和 `go vet`；`make build` 生成 c-shared 动态库。必须看到实际返回结果才能报告通过。
- 修改 UI/API：至少重跑 Go 路由/脱敏测试及 npm 测试。需要验证布局/CSP/真实鉴权时，用隔离环境做桌面及 360px 浏览器检查；jsdom 无法替代这些验证。
- 修改代理引用、worker、门控或网络代码：重点跑 `proxy_identity_test.go`、`proxy_settings_test.go`、`ticket_test.go`、`logging_test.go`、`wire_test.go`；最终仍需完整 race 测试和 ABI smoke。
- 测试使用合成凭据与本地 fixture。线上管理密钥只能从授权的私密来源读取且不输出；不要将带真实密钥的命令写入文档/仓库。
- 默认配置与示例配置不一定相同，例如源码 `MaxConcurrency` 默认 2，而公开示例显式设为 1；不要混淆默认值和部署值。
- 仅文档修改可做链接/路径、敏感信息和 diff 检查；若未重跑运行测试，明确说明，不沿用旧测试结果冒充本次结果。

## 7. 提交、发布与部署

### 提交

1. 核对 `git remote -v`、工作区及远端分支；不覆盖别人未提交的改动，不随意 force-push。
2. 使用明确文件列表暂存并检查 diff；公开说明保持通用，不提交运行日志、数据库、备份、凭据、截图或本机验收记录。
3. 检查 `.gitignore` 并扫描**实际暂存内容**；忽略规则不会移除已经进入 Git 的秘密。若有泄漏，停止推送并处理撤销/轮换，不只删当前文件。
4. 提交推送后比对远端 SHA，并从 GitHub 读回目标文件；工具返回成功不等于内容已核实。

### 新版本 Release（仅明确要求发布时）

1. 同步 `rpc.go` 注册版本、`package.json`/锁文件、CHANGELOG 和需要变动的 README 示例；SDK/ABI 变更另做兼容评估。
2. 完整测试、构建、ABI smoke 后，从该提交构建产物并核对注册元数据；打包脚本不会替你更新版本。
3. 示例打包命令（版本号必须替换为本次实际版本）：

   ```bash
   python3 scripts/package_release.py 0.2.0
   readelf --version-info dist/codex-ticket.so
   (cd release && sha256sum -c checksums.txt)
   ```

4. ZIP 根目录保留同名 `codex-ticket.so`、LICENSE、NOTICE 和 THIRD_PARTY_NOTICES；二进制只上传 Release，不提交源码树。
5. 创建指向已测试提交的版本 tag 与 Release，上传 ZIP/校验清单；不要覆盖已发布 `v0.2.0` 的 tag 或附件冒充原产物。
6. 从公开 Release 重新下载，核对校验值、ZIP 内容及 tag 指向；报告本地测试、远端 CI 与线上验证各自的实际状态。

### 部署（另行授权）

- 先隔离验证，再更新指定 CPA 实例；不要修改账号、统计数据、业务代理或无关容器。
- 避免覆盖正在加载的动态库；使用受控停启/替换，按实际核心能力确认 reload 行为。
- 更新后读回插件版本与状态；需要真实上游测试时事先确认账号/额度与范围。
- 不把“有缓存”当成“已注入”，不把“已注入”当成“质量提升”。完成后清理专用临时测试资源，保留用户要求的恢复材料。

## 8. 交付口径

说明改了什么、实际验证了什么、哪些没有做；附相关 commit/文件/Release 链接。事实与推测分开，未实现功能标为待办，不能把建议写成已上线能力。
