# DNS Selector

[中文文档](./README.zh-CN.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)

A cross-platform DNS benchmarking tool that helps you find the fastest and most stable DNS servers for your network environment.

## Features

- Prebuilt binaries for Windows, macOS, and Linux on `amd64` / `arm64`
- Supports UDP, DNS-over-TLS (DoT), and DNS-over-HTTPS (DoH)
- Uses a global shuffled work queue to reduce scheduling bias
- Reuses UDP/DoT/DoH transports during a run instead of forcing a cold connection for every query
- Validates answers structurally and rejects outlier responses by multi-server consensus
- Built-in `cn` and `global` presets
- Configurable server list, domain list, measured query count, warmup, concurrency, and JSON output

## Installation

Download the prebuilt binary for your OS and architecture from the [Releases](https://github.com/betterlmy/dns-selector/releases) page, then extract and run directly.

## Quick Start

```bash
# Run with default preset (cn)
./dns-selector

# Use the global preset
./dns-selector -preset global

# Custom domains, warmup, and measured queries
./dns-selector -queries 10 -warmup 1 -domains "google.com,github.com,youtube.com"

# Custom DNS servers
./dns-selector -servers "udp:1.1.1.1,udp:8.8.8.8,dot:dns.google,doh:https://dns.google/dns-query"

# Custom DoT/DoH endpoints with explicit TLS server names
./dns-selector -servers "dot:1.1.1.1@cloudflare-dns.com,doh:https://1.1.1.1/dns-query@cloudflare-dns.com"

# Machine-readable output
./dns-selector -preset global -json
```

CLI output language follows the preset: `cn` prints Chinese, `global` prints English.

## Flags

| Flag | Description | Default |
|---|---|---|
| `-preset` | Preset: `cn` or `global` | `cn` |
| `-servers` | Custom DNS servers (`protocol:address`, comma-separated); overrides preset servers | preset |
| `-domains` | Test domains (comma-separated); overrides preset domains only | preset |
| `-queries` | Measured queries per domain | `10` |
| `-warmup` | Warmup queries per server before measurement | `1` |
| `-concurrency` | Maximum concurrent queries | `20` |
| `-timeout` | Timeout per DNS query | `2s` |
| `-json` | Emit JSON output instead of the interactive CLI view | `false` |

## Custom Server Format

```text
udp:1.1.1.1,udp:8.8.8.8,dot:dns.google,doh:https://dns.google/dns-query
```

Supported protocols: `udp`, `dot`, `doh`

For IP-based DoT/DoH endpoints that need an explicit TLS server name:

```text
dot:1.1.1.1@cloudflare-dns.com
doh:https://1.1.1.1/dns-query@cloudflare-dns.com
```

## Benchmark Method

- Built-in DoT/DoH presets use bootstrap IPs where possible; hostname endpoints are resolved once before the run and then pinned to a fixed dial target.
- Responses must contain a usable `A`/`AAAA` answer for the requested name, following CNAME chains when present.
- If multiple servers disagree for the same domain, one-off answer fingerprints are treated as outliers and excluded from the validated success rate.
- `P95` is only reported when a server has at least 5 validated samples.

## Scoring

```text
Score = (1 / median_seconds) × (success_rate²) × (median / P95)
```

The success rate here is the validated success rate. Squaring it penalizes unstable or inconsistent resolvers; the `median / P95` ratio penalizes high jitter when enough samples are available.

## SDK Usage

Import the package to run benchmarks programmatically:

```go
import "github.com/betterlmy/dns-selector/selector"
```

### Basic usage

```go
// Initialize from a preset
sel := selector.NewCNSelector() // or selector.NewGlobalSelector()

// Customize as needed
sel.Queries = 10
sel.WarmupQueries = 1
sel.Timeout = 3 * time.Second
sel.Domains = []string{"google.com", "github.com"}

// Run benchmark
results, err := sel.Benchmark(ctx, nil)
if err != nil {
    log.Fatal(err)
}
for _, r := range results {
    fmt.Printf("%s: score=%.2f median=%s\n", r.Name, r.Score, r.MedianTime)
}
```

### Selector fields

| Field | Type | Description | Default (CN) |
|---|---|---|---|
| `Servers` | `[]DNSServer` | DNS servers to benchmark | preset list |
| `Domains` | `[]string` | Test domains | preset list |
| `Queries` | `int` | Measured queries per domain | `10` |
| `WarmupQueries` | `int` | Warmup queries per server before measurement | `1` |
| `Concurrency` | `int` | Maximum concurrent queries; `0` uses default (20) | `20` |
| `Timeout` | `time.Duration` | Timeout per query | `2s` |

The second argument to `Benchmark` is an optional `func()` called after each measured query completes, useful for driving a progress bar.

### Preset data

```go
servers := selector.GetDefaultCNServers()      // []DNSServer — built-in CN server list
domains := selector.GetDefaultGlobalDomains()  // []string — built-in Global domain list
```

### BenchmarkResult fields

| Field | Type | Description |
|---|---|---|
| `Name` | `string` | Server name |
| `Address` | `string` | Configured server address |
| `Protocol` | `string` | `udp`, `dot`, or `doh` |
| `MedianTime` | `time.Duration` | Median validated query latency |
| `P95Time` | `time.Duration` | P95 validated latency; zero when sample count is below 5 |
| `SuccessRate` | `float64` | Validated success rate (0–1) |
| `Score` | `float64` | Composite score |
| `Successes` | `int` | Validated successful query count |
| `RawSuccesses` | `int` | Structurally valid responses before consensus filtering |
| `AnswerMismatches` | `int` | Responses rejected as answer outliers |
| `Total` | `int` | Total measured query count |

## Build from Source

```bash
git clone https://github.com/betterlmy/dns-selector.git
cd dns-selector
go mod tidy
go build -o dns-selector .
```

## Testing

```bash
go test ./...
go vet ./...
```

## License

MIT — see [LICENSE](./LICENSE).
This project is a derivative of [palemoky/dns-optimizer](https://github.com/palemoky/dns-optimizer), which is also released under the MIT License.
