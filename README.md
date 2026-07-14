# GROK-GO

[简体中文](#zh-cn) | [English](#english)

<a id="zh-cn"></a>

## 简体中文

GROK-GO 是一个可自托管的 Grok 网关，提供 OpenAI 和 Anthropic 兼容接口。它将
Go 控制/数据平面与内嵌的 Geist 风格管理控制台整合到单个 Linux 可执行文件中。

GROK-GO 支持 CLI OAuth、Console SSO 和 grok.com SSO 账号池；命名的下游 API
密钥；感知健康状态的调度；HTTP/SOCKS5 出站代理；文本、图片、图片编辑和异步视频
请求；以及感知 usage 的请求计量。管理控制台包含账号探活与批量操作、代理检查、
模型路由、媒体生命周期操作、请求/审计日志、运行时设置、TOTP 和 API 调试器。

### 运行要求

- Linux amd64
- PostgreSQL 14 或更高版本
- Redis 7 或更高版本
- 用于缓存媒体的可写数据目录

Node.js 和 pnpm 仅为构建时依赖。本项目有意不包含容器文件和镜像。

### 配置

将 `.env.example` 中的配置复制到服务环境，并设置以下必填值：

| 变量 | 用途 |
| --- | --- |
| `GROK_GO_DATABASE_URL` | PostgreSQL 连接 URL |
| `GROK_GO_REDIS_URL` | Redis 连接 URL |
| `GROK_GO_MASTER_KEY` | Base64 编码的 32 字节凭证加密密钥 |
| `GROK_GO_PUBLIC_URL` | 用于 OAuth 回调和媒体 URL 的公开源站地址 |
| `GROK_GO_BOOTSTRAP_TOKEN` | 用于创建首位管理员的一次性令牌 |

部署多个实例时，请将 `GROK_GO_INSTANCE_ID` 设置为一个在同时运行的副本之间唯一、
且重启后保持稳定的标识符。若省略，GROK-GO 会根据主机名和监听地址生成该标识符。

使用 `openssl rand -base64 32` 生成加密密钥。首位管理员创建后，引导端点将被禁用。

### 构建

```bash
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
pwsh ./scripts/build-web.ps1
go test ./...
go build -trimpath -o bin/grok-go ./cmd/grok-go
```

在 Windows 上，`make release` 或 `scripts/build-release.ps1` 会先构建并暂存 Web
控制台，再在 `bin/` 中生成静态 `linux/amd64` 构建产物。

### 首次启动

```bash
export GROK_GO_DATABASE_URL='postgres://grok_go:secret@127.0.0.1:5432/grok_go?sslmode=disable'
export GROK_GO_REDIS_URL='redis://127.0.0.1:6379/0'
export GROK_GO_MASTER_KEY='BASE64_KEY'
export GROK_GO_PUBLIC_URL='https://grok.example.com'
export GROK_GO_BOOTSTRAP_TOKEN='ONE_TIME_TOKEN'
./grok-go
```

打开配置的公开 URL，创建首位管理员，然后添加上游账号和命名的客户端 API 密钥。
客户端密钥会加密保存以供后续复制；请求路径仅使用带密钥的校验摘要。

### API

```bash
curl https://grok.example.com/v1/chat/completions \
  -H 'Authorization: Bearer gg_live_...' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-4.5",
    "stream": true,
    "messages": [{"role":"user","content":"Hello"}]
  }'
```

主要端点：

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/messages`
- `POST /v1/images/generations`
- `POST /v1/images/edits`
- `POST /v1/videos` 和 `POST /v1/videos/generations`
- `GET /v1/videos/{id}` 和 `GET /v1/videos/{id}/content`

Chat Completions、Responses 和 Anthropic Messages 共用一条标准化事件管线。该管线
保留 reasoning 增量、函数调用、usage、缓存输入 token、SSE 心跳以及各协议的终止
事件结构。系统会生成两个稳定标识：租户隔离的会话亲和键优先采用显式会话、
`prompt_cache_key`、metadata 或 Anthropic cache anchor，否则从稳定的对话前缀派生；
全局上游 prompt-cache 键则从模型、system/instructions、developer 消息和 tools 等静态
前缀派生，不包含下游 API Key，缺少静态前缀时才纳入首条 user 输入。会话亲和键用于
保持兼容的上游账号；生成的 prompt-cache 键会发送给 CLI OAuth 和 Console SSO 路由，
Grok SSO Web 私有协议不注入该字段。xAI 仍根据实际 prompt 前缀判定缓存命中。

图片生成通过 grok.com SSO 账号使用 Grok Imagine。图片编辑会先上传引用并创建 Grok
所需的媒体帖子，然后以流式方式执行编辑。视频创建是异步的：生成期间始终固定到选中
的账号；客户端轮询返回的任务 ID 后，最终资源会被缓存，并通过短期签名 URL 提供。

支持范围请参阅 [API 兼容性](docs/api-compatibility.md)。可复现的并行网关基准和测量
范围请参阅[性能基线](docs/performance.md)。发布变更记录在[更新日志](CHANGELOG.md)中。

### 账号备份兼容性

账号页面和管理 API 可导入 GROK-GO 原生备份、sub2api Grok OAuth 备份、grok2api
token 池，以及 CLIProxyAPI（CPA）使用的 xAI OAuth 记录。支持 JSON、纯文本和多个
multipart 文件。重复导入具有幂等性：系统通过带密钥的凭证指纹识别同一上游身份，
无需保存明文查找值。

管理 API 还保留了兼容 grok2api 的 token 导入路由：

```bash
curl https://grok.example.com/admin/api/tokens/add \
  -b admin-cookies.txt \
  -H 'X-CSRF-Token: CSRF_TOKEN' \
  -H 'Content-Type: application/json' \
  -d '{"tokens":["SSO_TOKEN"],"pool":"basic","tags":["imported"]}'
```

通过 `POST /admin/api/accounts/export` 创建导出。选择 1 至 500 个账号 ID，并选择
`native`、`sub2api`、`grok2api` 或 `cpa`；然后确认当前管理员密码，启用 TOTP 时还需
提供 TOTP 验证码。响应是禁止缓存的附件。导出文件包含可用的上游凭证，必须按密钥
保管。

| 格式 | 导入 | 导出限制 |
| --- | --- | --- |
| GROK-GO 原生 | 版本 1 账号备份 | 支持所有凭证类型；保留调度元数据 |
| sub2api | 版本 1 或旧版 bundle 中的 Grok/xAI OAuth 账号 | 带有 access token 和 refresh token 的 CLI OAuth 账号 |
| grok2api | `basic`、`super`、`heavy`、`auto` 和 `sso*` token 池，或 `/tokens` 列表导出 | 按 basic/super/heavy 池分组的 Grok SSO 账号 |
| CPA / CLIProxyAPI | xAI OAuth JSON，包括旧版最小记录 | CLI OAuth；单账号导出为 JSON，多账号导出为 ZIP |

导入的密钥在持久化前会被加密，且不会由列表 API 返回。OAuth 导入仅接受完整的
Bearer 凭证，并会将上游 URL 规范化为支持的 xAI CLI 端点；导入的端点和 header
覆盖配置不会被信任。管理别名与控制台其他接口使用相同的管理员会话和 CSRF 防护。
请求格式和往返转换细节请参阅
[API 兼容性](docs/api-compatibility.md#account-import-and-export)。

导入控制参数可通过 JSON 字段、multipart 字段或查询参数传递。`initial_status`
（`status` 是其别名）会显式覆盖导入账号的状态；设为 `active` 时也会清除导入的
cooldown。可选的 `post_import_action` 支持 `none`（API 默认值）、`refresh` 和
`refresh_probe`。导入后操作仅对本次新建的 CLI OAuth 账号执行，使用有界并发和
整个请求范围的超时；响应中的 `post_action` 摘要会包含每个已处理账号的结果。除非
通过初始状态覆盖显式激活账号，否则导入文件中明确禁用的账号仍保持禁用。

对 Build OAuth multipart 上传，控制台默认发送以下控制参数。刷新和探活均成功前，
账号不会进入常规调度：

```bash
curl -X POST 'https://HOST/admin/api/accounts/import' \
  -H 'X-CSRF-Token: CSRF_TOKEN' \
  -F 'initial_status=active' \
  -F 'post_import_action=refresh_probe' \
  -F 'files=@xai-ACCOUNT.json;type=application/json'
```

需要保留历史“仅导入”行为的 API 客户端可以发送 `post_import_action=none`。当请求体
为一个原始 Build OAuth JSON 对象时，也可通过查询参数传递同样的控制项。使用
`POST /admin/api/accounts/{id}/probe` 可重新检查单个已有账号；批量端点为
`POST /admin/api/accounts/probe`，请求体是 `{ "ids": ["ACCOUNT_ID"] }`。

内嵌模型目录包含受支持的 Grok 文本和媒体预设、对应的凭证路由、账号等级要求以及
最佳等级偏好。目录迁移会更新托管预设而不覆盖管理员自定义项；可在控制台添加和删除
自定义模型。

### 运维

- `/healthz` 报告进程存活状态。
- `/readyz` 检查 PostgreSQL、Redis、迁移和加密配置。
- 运行时设置和账号调度策略变更通过 Redis 发布，并由每个实例重新加载。账号 CRUD、
  探活反馈、cooldown、配额和凭证状态变更使用相同的传播路径。重新连接的实例会先从
  PostgreSQL 对账，再接受后续通知。
- 账号可用性同时考量请求配额和 token 配额。已耗尽的账号会持续 cooldown，直到
  提供方的重置时间；如果提供方未给出重置时间，则使用与凭证类型对应的回退窗口。
  选择评分采用请求与 token 剩余比例中较低者；较早完成的并发成功不会清除较新的
  限速 cooldown。CLI OAuth 在响应提交前出现 401 时，会先执行一次协调的凭证刷新，
  再进行常规故障转移。
- CLI OAuth 凭证会在到期前通过分布式 Redis 锁刷新。账号列表会显示到期时间，管理员
  也可请求立即刷新单个账号。
- 分布式账号并发控制使用每账号一个原子 Redis sorted-set lease。同一 owner 的重复
  获取具有幂等性，混合 lease TTL 会保留最新的有效到期时间。只要绑定仍由当前请求
  持有，活跃的缓存亲和性就会刷新其 TTL，使长对话保持稳定账号，同时避免过期释放。
- Dashboard 缓存报告分别展示缓存 token 复用、请求级缓存命中、缓存预热候选、
  亲和复用未命中率以及已解析 usage 覆盖率。只有成功的 Chat Completions、Responses
  和 Messages 请求进入缓存分母；失败请求和媒体请求不会扭曲这些比率。
- 仅含元数据的请求日志使用有界异步队列。管理员变更另行记录 actor、action、resource、
  status、request ID 和来源地址；请求体、Cookie 和凭证均被省略。
- 随附的 `deploy/grok-go.service` 是经过加固的 systemd unit 模板。
- PostgreSQL 与配置的媒体目录应一并备份。Redis 存储的是可重建的协调状态，不能替代
  PostgreSQL 备份。

### 开发

```bash
go test -race ./...
pnpm --dir web lint
pnpm --dir web test
pnpm --dir web build
```

前端是静态 Next.js 导出。它使用同源 REST 端点，不使用 Server Actions 或 Next API
路由。

### 默认安全设置

- 上游凭证使用 AES-256-GCM 静态加密。
- 管理员密码使用 Argon2id；TOTP 为可选项。
- API 密钥认证使用带密钥的摘要；可恢复副本使用 AES-256-GCM 加密，仅通过经过认证的
  管理 API 暴露。
- 请求日志不包含请求体和响应体。
- 远程媒体获取会拒绝私有网络和本地网络目标。
- 缓存媒体通过不透明 ID 和过期签名寻址。
- 远程媒体在进入本地缓存前，会经由公开地址验证以及重定向、内容类型和大小检查获取。
- 长时间 SSE 响应会清除服务器绝对写入 deadline，同时保留上游请求超时和心跳。由已
  重启实例拥有的中断视频任务会被标记为失败，不会无限期保持 active。

### 致谢

GROK-GO 是独立实现。`langrenjh-alt/grok2api-haochi` 和 `Wei-Shaw/sub2api` 的公开
行为为兼容性范围提供了参考。Vercel Geist 文档定义了 UI 设计目标。许可证详情请参阅
`NOTICE`。

### 许可证

MIT

---

<a id="english"></a>

## English

GROK-GO is a self-hosted Grok gateway with OpenAI and Anthropic compatible
interfaces. It combines a Go control/data plane with an embedded Geist-style
administration console in one Linux executable.

GROK-GO supports CLI OAuth, Console SSO, and grok.com SSO account
pools; named downstream API keys; health-aware scheduling; HTTP/SOCKS5 egress
proxies; text, image, image-edit, and asynchronous video requests; and
usage-aware request accounting. The administration console includes account
probing and batch operations, proxy checks, model routing, media lifecycle
operations, request/audit logs, runtime settings, TOTP, and an API debugger.

### Runtime requirements

- Linux amd64
- PostgreSQL 14 or newer
- Redis 7 or newer
- A writable data directory for cached media

Node.js and pnpm are build-time dependencies only. Container files and images
are intentionally not part of this project.

### Configuration

Copy `.env.example` into your service environment and set these required values:

| Variable | Purpose |
| --- | --- |
| `GROK_GO_DATABASE_URL` | PostgreSQL connection URL |
| `GROK_GO_REDIS_URL` | Redis connection URL |
| `GROK_GO_MASTER_KEY` | Base64-encoded 32-byte credential encryption key |
| `GROK_GO_PUBLIC_URL` | Public origin used for OAuth callbacks and media URLs |
| `GROK_GO_BOOTSTRAP_TOKEN` | One-time token used to create the first administrator |

For multiple instances, set `GROK_GO_INSTANCE_ID` to an identifier that is
unique among concurrently running replicas and stable across restarts. If it is
omitted, GROK-GO derives one from the host name and listen address.

Generate the encryption key with `openssl rand -base64 32`. The bootstrap
endpoint is disabled after the first administrator exists.

### Build

```bash
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
pwsh ./scripts/build-web.ps1
go test ./...
go build -trimpath -o bin/grok-go ./cmd/grok-go
```

On Windows, `make release` or `scripts/build-release.ps1` builds and stages the
web console before creating the static `linux/amd64` artifact in `bin/`.

### First start

```bash
export GROK_GO_DATABASE_URL='postgres://grok_go:secret@127.0.0.1:5432/grok_go?sslmode=disable'
export GROK_GO_REDIS_URL='redis://127.0.0.1:6379/0'
export GROK_GO_MASTER_KEY='BASE64_KEY'
export GROK_GO_PUBLIC_URL='https://grok.example.com'
export GROK_GO_BOOTSTRAP_TOKEN='ONE_TIME_TOKEN'
./grok-go
```

Open the configured public URL, create the first administrator, then add an
upstream account and a named client API key. Client secrets are encrypted for
later copying; only keyed verification digests are used on the request path.

### API

```bash
curl https://grok.example.com/v1/chat/completions \
  -H 'Authorization: Bearer gg_live_...' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-4.5",
    "stream": true,
    "messages": [{"role":"user","content":"Hello"}]
  }'
```

Primary endpoints:

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/messages`
- `POST /v1/images/generations`
- `POST /v1/images/edits`
- `POST /v1/videos` and `POST /v1/videos/generations`
- `GET /v1/videos/{id}` and `GET /v1/videos/{id}/content`

Chat Completions, Responses, and Anthropic Messages share one normalized event
pipeline. It preserves reasoning deltas, function calls, usage, cached-input
tokens, SSE heartbeats, and each protocol's terminal event shape. GROK-GO
derives two stable identities. The tenant-isolated session-affinity key prefers
an explicit session, `prompt_cache_key`, metadata, or an Anthropic cache anchor,
then falls back to a stable conversation prefix. A separate upstream
prompt-cache key is global across downstream API keys and derived from the
static prefix: model, system/instructions, developer messages, and tools; the
first user input is included only when no static prefix exists. The affinity key
keeps requests on a compatible upstream account. The generated prompt-cache
key is sent on CLI OAuth and Console SSO routes, but is not injected into the
private Grok SSO Web schema. xAI still
determines cache hits from the actual prompt prefix.

Image generation uses Grok Imagine for grok.com SSO accounts. Image editing
uploads references and creates the required Grok media post before streaming
the edit. Video creation is asynchronous: generation remains pinned to the
selected account while clients poll the returned job ID, then the final asset
is cached behind a short-lived signed URL.

See [API compatibility](docs/api-compatibility.md) for the supported surface.
See [Performance baseline](docs/performance.md) for reproducible parallel
gateway benchmarks and measurement scope. Release changes are recorded in the
[changelog](CHANGELOG.md).

### Account backup compatibility

The Accounts page and administration API import native GROK-GO backups,
sub2api Grok OAuth backups, grok2api token pools, and xAI OAuth records used by
CLIProxyAPI (CPA). JSON, plain text, and multiple multipart files are accepted.
Repeated imports are idempotent: a keyed credential fingerprint identifies the
same upstream identity without storing a plaintext lookup value.

The administration API also retains grok2api-compatible token import routes:

```bash
curl https://grok.example.com/admin/api/tokens/add \
  -b admin-cookies.txt \
  -H 'X-CSRF-Token: CSRF_TOKEN' \
  -H 'Content-Type: application/json' \
  -d '{"tokens":["SSO_TOKEN"],"pool":"basic","tags":["imported"]}'
```

Exports are created with `POST /admin/api/accounts/export`. Select between one
and 500 account IDs, choose `native`, `sub2api`, `grok2api`, or `cpa`, and
confirm the current administrator password plus the TOTP code when TOTP is
enabled. The response is a no-store attachment. Export files contain usable
upstream credentials and must be handled as secrets.

| Format | Import | Export constraints |
| --- | --- | --- |
| GROK-GO native | Version 1 account backup | All supported credential kinds; preserves scheduling metadata |
| sub2api | Grok/xAI OAuth accounts from version 1 or legacy bundles | CLI OAuth accounts with access and refresh tokens |
| grok2api | `basic`, `super`, `heavy`, `auto`, and `sso*` token pools, or a `/tokens` list export | Grok SSO accounts grouped into basic/super/heavy pools |
| CPA / CLIProxyAPI | xAI OAuth JSON, including legacy minimal records | CLI OAuth; one account is JSON, multiple accounts are a ZIP |

Imported secrets are encrypted before persistence and are never returned by
list APIs. OAuth imports accept only complete Bearer credentials and normalize
the upstream URL to the supported xAI CLI endpoint; imported endpoint and
header overrides are not trusted. Management aliases use the same
administrator session and CSRF protection as the rest of the console. See
[API compatibility](docs/api-compatibility.md#account-import-and-export) for
the request schema and round-trip details.

Import controls may be supplied as JSON fields, multipart fields, or query
parameters. `initial_status` (with `status` as an alias) explicitly overrides
the imported account state; `active` also clears an imported cooldown. The
optional `post_import_action` accepts `none` (the API default), `refresh`, or
`refresh_probe`. Post-import actions run only for newly created CLI OAuth
accounts, use bounded concurrency and a request-wide timeout, and return a
`post_action` summary with one result per processed account. An explicitly
disabled account is preserved unless an initial status override activates it.

For a Build OAuth multipart upload, the console sends the following controls by
default. The account remains outside normal scheduling until both refresh and
probe complete successfully:

```bash
curl -X POST 'https://HOST/admin/api/accounts/import' \
  -H 'X-CSRF-Token: CSRF_TOKEN' \
  -F 'initial_status=active' \
  -F 'post_import_action=refresh_probe' \
  -F 'files=@xai-ACCOUNT.json;type=application/json'
```

API clients that need the historical import-only behavior can send
`post_import_action=none`. The same controls can be query parameters when the
request body is one raw Build OAuth JSON object. A single existing account can
be rechecked with `POST /admin/api/accounts/{id}/probe`; the batch endpoint is
`POST /admin/api/accounts/probe` with `{ "ids": ["ACCOUNT_ID"] }`.

The embedded model catalog contains the supported Grok text and media presets,
their credential routes, account-tier requirements, and best-tier preference.
Catalog migrations update managed presets without overwriting administrator
customizations; custom models can be added and removed from the console.

### Operations

- `/healthz` reports process liveness.
- `/readyz` checks PostgreSQL, Redis, migrations, and encryption configuration.
- Runtime settings and account scheduling policy changes are published through
  Redis and reloaded by every instance. Account CRUD, probe feedback, cooldown,
  quota, and credential-state changes use the same propagation path.
  Reconnecting instances reconcile from PostgreSQL before accepting subsequent
  notifications.
- Account eligibility observes both request and token quota. Exhausted accounts
  remain cooling until the provider reset time, or a credential-kind fallback
  window when the provider omits a reset timestamp. Selection scores the lower
  remaining request/token ratio, and an older concurrent success cannot erase
  a newer rate-limit cooldown. A pre-commit CLI OAuth 401 triggers one
  coordinated credential refresh before normal failover.
- CLI OAuth credentials are refreshed before expiry under a distributed Redis
  lock. Expiry is visible in the account list, and administrators can also
  request an immediate refresh for one account.
- Distributed account concurrency uses an atomic Redis sorted-set lease per
  account. Replayed acquisition by the same owner is idempotent and mixed lease
  TTLs retain the latest live expiry. Active cache affinity refreshes its TTL
  while the binding is still owned, so long conversations retain a stable
  account without stale releases.
- Dashboard cache reporting separates cached-token reuse, request-level cache
  hits, warmup candidates, the affinity-reuse miss rate, and parsed-usage coverage.
  Only successful Chat Completions, Responses, and Messages requests enter
  cache denominators; failed and media requests do not distort the rates.
- Metadata-only request logs use a bounded asynchronous queue. Administrator
  mutations are recorded separately with actor, action, resource, status,
  request ID, and source address; bodies, cookies, and credentials are omitted.
- The included `deploy/grok-go.service` is a hardened systemd unit template.
- Back up PostgreSQL and the configured media directory together. Redis stores
  reconstructable coordination state and does not replace PostgreSQL backups.

### Development

```bash
go test -race ./...
pnpm --dir web lint
pnpm --dir web test
pnpm --dir web build
```

The frontend is a static Next.js export. It uses same-origin REST endpoints and
does not use Server Actions or Next API routes.

### Security defaults

- Upstream credentials use AES-256-GCM at rest.
- Administrator passwords use Argon2id; TOTP is optional.
- API key authentication uses keyed digests; recoverable copies are encrypted
  with AES-256-GCM and exposed only through the authenticated admin API.
- Request and response bodies are excluded from request logs.
- Remote media fetches reject private and local network targets.
- Cached media is addressed by opaque IDs and expiring signatures.
- Remote media is fetched through public-address validation with redirect,
  content-type, and size checks before it enters the local cache.
- Long SSE responses clear the server's absolute write deadline while retaining
  the upstream request timeout and heartbeat. Interrupted video jobs owned by a
  restarted instance are marked failed instead of remaining indefinitely active.

### Attribution

GROK-GO is an independent implementation. Public behavior from
`langrenjh-alt/grok2api-haochi` and `Wei-Shaw/sub2api` informed the compatibility
surface. Vercel Geist documentation defines the UI design target. See `NOTICE`
for license details.

### License

MIT
