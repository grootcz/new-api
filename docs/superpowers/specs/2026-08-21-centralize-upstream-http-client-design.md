# 设计文档：归并上游渠道 HTTP Client 并集中配置超时

- 日期：2026-08-21
- 分支：`feat/centralize-upstream-http-client`（基于 `release/groot`）
- 状态：已确认，待实现

## 背景与目标

仓库中请求上游 AI 渠道的 HTTP client 已经**大部分**集中在 `service/http_client.go`，统一出口为
`GetHttpClient()` / `GetHttpClientWithProxy()` / `GetHttpClientWithProxySettings()`，底层由
`newRelayHTTPTransport()` 构建 transport（已支持代理、SSRF、HTTP/2 分片、TLS、连接池复用）。

本次改造有两个目标：

1. **归并**：消除 `relay/` 下仍绕过中心、直接 `&http.Client{}` 的调用点，使所有上游渠道请求统一走中心。
2. **配置超时**：为中心 transport 补齐建连、TLS 握手、响应头、每主机连接数、读写缓冲等参数，全部
   通过环境变量可配置，并在 `docker-compose.yml` / `docker-compose.dev.yml` 中给出默认值。

## 范围界定

**纳入**（relay 中继到上游 AI 渠道，经全仓扫描仅这两处绕过中心）：

- `relay/channel/ollama/relay-ollama.go`：6 处裸 `&http.Client{}`
  （`FetchOllamaModels`、`PullOllamaModel`、`PullOllamaModelStream`、`DeleteOllamaModel`、
  `FetchOllamaVersion`，以及其中重复的空 client）。
- `relay/channel/ali/image.go:207`：`updateTask` 轮询上游任务状态。

**不纳入**（用途不是上游渠道推理）：

- `oauth/*`（OAuth 登录）、`controller/topup_creem.go`（支付）、`controller/uptime_kuma.go`（监控）、
  `controller/ratio_sync.go` / `controller/model_sync.go`（定价/模型列表同步）、
  `controller/custom_oauth.go`、`middleware/turnstile-check.go`（人机验证）。
- `pkg/ionet/client.go`：独立包，维持现状。

## 一、中心 transport 增强

改造点：`service/http_client.go` 的 `newRelayHTTPTransport()`。该函数从 `http.DefaultTransport.Clone()`
起步，需**显式覆盖**以下字段（Clone 会带入标准库默认值，必须覆盖才生效）：

- `DialContext`：用带 `Timeout = RELAY_DIAL_TIMEOUT` 的 `net.Dialer`（保留现有 `KeepAlive`）。
- `TLSHandshakeTimeout = RELAY_TLS_HANDSHAKE_TIMEOUT`
- `ResponseHeaderTimeout = RELAY_RESPONSE_HEADER_TIMEOUT`
- `MaxConnsPerHost = RELAY_MAX_CONNS_PER_HOST`
- `WriteBufferSize = RELAY_WRITE_BUFFER_SIZE`
- `ReadBufferSize = RELAY_READ_BUFFER_SIZE`
- 现有的 `MaxIdleConns` / `MaxIdleConnsPerHost` / `IdleConnTimeout` / `ForceAttemptHTTP2` 保留。

### 参数表

| 参数 | env 变量 | 单位 | 默认值 | 来源 |
|---|---|---|---|---|
| 建连超时 | `RELAY_DIAL_TIMEOUT` | 秒 | 5 | 新增 |
| TLS 握手超时 | `RELAY_TLS_HANDSHAKE_TIMEOUT` | 秒 | 7 | 新增 |
| 响应头超时 | `RELAY_RESPONSE_HEADER_TIMEOUT` | 秒 | 900（15min） | 新增 |
| 每主机最大连接 | `RELAY_MAX_CONNS_PER_HOST` | 个 | 1024 | 新增 |
| 写缓冲 | `RELAY_WRITE_BUFFER_SIZE` | 字节 | 65536（64KB） | 新增 |
| 读缓冲 | `RELAY_READ_BUFFER_SIZE` | 字节 | 65536（64KB） | 新增 |
| 空闲连接超时 | `RELAY_IDLE_CONN_TIMEOUT` | 秒 | 90 | 已有，保留 |
| 最大空闲连接 | `RELAY_MAX_IDLE_CONNS` | 个 | 500 | 已有，保留 |
| 每主机最大空闲 | `RELAY_MAX_IDLE_CONNS_PER_HOST` | 个 | 100 | 已有，保留 |
| 总超时 | `RELAY_TIMEOUT` | 秒 | 0（不设） | 已有，保留 |

### 关键的偏离示例说明

- **读写 buffer 用 64KB 而非示例的 64MB**：示例 `64 * 1024 * 1024` = 64MB 是**每连接**分配，
  配合 `MaxConnsPerHost=1024` 潜在占用达数十 GB，会导致 OOM。64KB 已是 Go 默认值（4KB）的 16 倍，
  是合理的大 buffer。
- **`MaxIdleConns=500` / `MaxIdleConnsPerHost=100` 保留现有值**（不采用示例的 256/8）：
  对高并发 AI 网关，示例的 8 会导致连接反复重建，现有值更优。
- **不设总 `Timeout`**（`RELAY_TIMEOUT` 默认 0）：与示例注释一致——总超时是从发起到读完整个
  response body 的硬性墙钟，会误杀长时流式响应；靠连接级超时与空闲控制即可。

## 二、常量声明与初始化

- `common/constants.go`：声明新增的 6 个 `var Relay* int`（单位注释：超时为秒，buffer 为字节）。
- `common/init.go`：用 `GetEnvOrDefault(...)` 读取，默认值同上表。

## 三、归并绕过点

- **ollama（6 处）**：全部改用 `service.GetHttpClient()`，删除各自的 `Timeout`（含 Pull 的 30/60min）。
  - **行为变化**：非流式 `PullOllamaModel` 大模型改由 `RELAY_RESPONSE_HEADER_TIMEOUT`（默认 15min）约束，
    超大模型拉取如需更久，可调大该 env。流式 `PullOllamaModelStream` 持续有数据，响应头即时返回，不受影响。
  - `ollama` 包已 import `service`，无循环依赖。
- **ali `updateTask`**：改用 `service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)`，
  与同目录其他 task adaptor 一致（实现时确认 `info.ChannelSetting` 字段可用）。

## 四、docker-compose 默认值

- `docker-compose.yml`：沿用现有注释惯例 `#      - VAR=default # 中英说明`，列出全部新增 `RELAY_*` 变量。
- `docker-compose.dev.yml`：用**生效行**列出全部新增 `RELAY_*` 变量及默认值。

## 五、验证

- `go build ./...` 编译通过。
- `newRelayHTTPTransport` 与 `relaykit/` 无关联，本次不触及 relaykit 模块；如构建提示受影响，
  执行 `cd relaykit && GOWORK=off go build ./...`。
- 关注 socks5 代理路径：`configureProxyTransport` 会为 socks5 重设 `DialContext`（用其自带 30s dialer），
  这是既有行为，本次不改；新的 dial timeout 主要作用于直连与 http/https 代理路径。

## 非目标

- 不重写现有代理/SSRF/分片/缓存体系。
- 不改动范围外的 OAuth/支付/监控/同步类 client。
- 不引入新的 client 抽象层——继续复用现有 `GetHttpClient*` 出口。
