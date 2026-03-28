# DNS Selector

[English README](./README.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)

跨平台 DNS 基准测试工具，帮你找出当前网络环境下最快、最稳定的 DNS 服务器。

## 功能特性

- 提供 Windows、macOS、Linux 的 `amd64` / `arm64` 预编译版本
- 支持 UDP、DNS-over-TLS (DoT)、DNS-over-HTTPS (DoH)
- 使用全局打散的任务队列，降低调度顺序偏差
- 在单次 benchmark 内复用 UDP/DoT/DoH 连接，而不是每次查询都强制冷启动
- 对响应做结构校验，并用多服务器共识剔除离群答案
- 内置 `cn` 和 `global` 两套预设
- 支持自定义服务器列表、测试域名、正式查询次数、预热、并发数和 JSON 输出

## 安装

在 [Releases](https://github.com/betterlmy/dns-selector/releases) 页面下载适合你操作系统和架构的预编译版本，解压后直接运行。

## 快速开始

```bash
# 使用默认预设（cn）运行
./dns-selector

# 使用全球预设
./dns-selector -preset global

# 自定义域名、预热和正式查询次数
./dns-selector -queries 10 -warmup 1 -domains "google.com,github.com,youtube.com"

# 自定义 DNS 服务器
./dns-selector -servers "udp:1.1.1.1,udp:8.8.8.8,dot:dns.google,doh:https://dns.google/dns-query"

# 自定义 DoT/DoH 服务器并显式指定 TLS 域名
./dns-selector -servers "dot:1.1.1.1@cloudflare-dns.com,doh:https://1.1.1.1/dns-query@cloudflare-dns.com"

# 输出 JSON
./dns-selector -preset global -json
```

CLI 输出语言跟随预设切换：`cn` 输出中文，`global` 输出英文。

## 参数说明

| 参数 | 描述 | 默认值 |
|---|---|---|
| `-preset` | 预设：`cn` 或 `global` | `cn` |
| `-servers` | 自定义 DNS 服务器（`协议:地址`，逗号分隔）；覆盖预设服务器 | 预设 |
| `-domains` | 测试域名（逗号分隔）；仅覆盖预设域名 | 预设 |
| `-queries` | 每个域名的正式查询次数 | `10` |
| `-warmup` | 每个服务器在正式测试前的预热查询次数 | `1` |
| `-concurrency` | 最大并发查询数 | `20` |
| `-timeout` | 每次 DNS 查询的超时时间 | `2s` |
| `-json` | 输出机器可读的 JSON，而不是交互式 CLI 视图 | `false` |

## 自定义服务器格式

```text
udp:1.1.1.1,udp:8.8.8.8,dot:dns.google,doh:https://dns.google/dns-query
```

支持协议：`udp`、`dot`、`doh`

对于需要显式 TLS 域名的 IP 形式 DoT/DoH 地址，可使用：

```text
dot:1.1.1.1@cloudflare-dns.com
doh:https://1.1.1.1/dns-query@cloudflare-dns.com
```

## 基准测试方法

- 内置 DoT/DoH 预设会尽量使用 bootstrap IP；使用域名的端点会在 benchmark 开始前解析一次，并固定到单一拨号地址。
- 响应必须包含对请求域名可用的 `A`/`AAAA` 结果；如果有 CNAME，会沿链继续校验。
- 同一域名若不同服务器返回的答案不一致，只有多服务器重复出现的答案指纹会被视为有效，单点离群答案会被剔除。
- 只有当某台服务器至少拿到 5 个有效样本时，才会展示 `P95`。

## 评分公式

```text
Score = (1 / 中位延迟秒数) × (成功率²) × (中位延迟 / P95延迟)
```

这里的成功率是“校验后成功率”；成功率平方会显著惩罚不稳定或答案不一致的服务器。样本不足时不计算 `P95` 惩罚项。

## SDK 用法

将包引入到你的项目中，即可以编程方式执行基准测试：

```go
import "github.com/betterlmy/dns-selector/selector"
```

### 基础用法

```go
// 使用预设初始化
sel := selector.NewCNSelector() // 或 selector.NewGlobalSelector()

// 按需自定义参数
sel.Queries = 10
sel.WarmupQueries = 1
sel.Timeout = 3 * time.Second
sel.Domains = []string{"google.com", "github.com"}

// 执行基准测试
results, err := sel.Benchmark(ctx, nil)
if err != nil {
    log.Fatal(err)
}
for _, r := range results {
    fmt.Printf("%s: 评分=%.2f 中位延迟=%s\n", r.Name, r.Score, r.MedianTime)
}
```

### Selector 字段说明

| 字段 | 类型 | 描述 | 默认值（CN） |
|---|---|---|---|
| `Servers` | `[]DNSServer` | 参与测试的 DNS 服务器列表 | 预设列表 |
| `Domains` | `[]string` | 测试域名列表 | 预设列表 |
| `Queries` | `int` | 每个域名的正式查询次数 | `10` |
| `WarmupQueries` | `int` | 每个服务器在正式测试前的预热次数 | `1` |
| `Concurrency` | `int` | 最大并发查询数；`0` 使用默认（20） | `20` |
| `Timeout` | `time.Duration` | 每次查询超时时间 | `2s` |

`Benchmark` 的第二个参数为可选的 `func()` 回调，每次正式查询完成后调用，可用于驱动进度条，传 `nil` 则忽略。

### 预设数据访问

```go
servers := selector.GetDefaultCNServers()      // []DNSServer — 内置 CN 服务器列表
domains := selector.GetDefaultGlobalDomains()  // []string — 内置 Global 域名列表
```

### BenchmarkResult 字段说明

| 字段 | 类型 | 描述 |
|---|---|---|
| `Name` | `string` | 服务器名称 |
| `Address` | `string` | 配置中的服务器地址 |
| `Protocol` | `string` | `udp`、`dot` 或 `doh` |
| `MedianTime` | `time.Duration` | 有效样本的中位查询延迟 |
| `P95Time` | `time.Duration` | 有效样本的 P95；少于 5 个样本时为 0 |
| `SuccessRate` | `float64` | 校验后成功率（0–1） |
| `Score` | `float64` | 综合评分 |
| `Successes` | `int` | 校验后成功查询次数 |
| `RawSuccesses` | `int` | 共识过滤前的结构合法响应次数 |
| `AnswerMismatches` | `int` | 被判定为离群答案的响应次数 |
| `Total` | `int` | 总正式查询次数 |

## 从源码构建

```bash
git clone https://github.com/betterlmy/dns-selector.git
cd dns-selector
go mod tidy
go build -o dns-selector .
```

## 测试

```bash
go test ./...
go vet ./...
```

## 许可

MIT 协议，详见 [LICENSE](./LICENSE)。
本项目基于 [palemoky/dns-optimizer](https://github.com/palemoky/dns-optimizer) 二次开发，原项目同样采用 MIT 协议。
