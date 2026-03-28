# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目简介

dns-selector 是一个跨平台 DNS 基准测试工具，帮助用户找出更快、更稳定的 DNS 服务商。支持 UDP、DoT（DNS-over-TLS）、DoH（DNS-over-HTTPS）三种协议，内置 CN 和 Global 两套预设。

## 常用命令

```bash
# 构建
go build -o dns-selector .

# 测试
go test ./...

# 静态检查
go vet ./...

# 查看覆盖率
go test ./... -coverprofile=coverage.out -covermode=atomic
go tool cover -func=coverage.out

# 格式化（提交前必须通过）
gofmt -w .
```

## 架构概览

```text
main.go                            # CLI 入口：flag 注册、main()
cli.go                             # CLI 组装层：参数解析、表格输出、JSON 输出
selector/selector.go               # 对外类型、预设、参数校验、解析辅助
selector/benchmark.go              # benchmark 调度、全局任务队列、聚合与评分
selector/query.go                  # 协议实现、连接复用、答案校验、端点预解析
selector/selector_test.go          # 单元测试
selector/benchmark_integration_test.go # 本地 UDP/DoH 集成测试
```

## 核心数据流

```text
CLI flags
  → buildSelector()
  → Selector.Benchmark()
  → prepareServers()：解析预设、预解析 DoT/DoH 目标、固定拨号地址
  → buildWarmupItems() + buildMeasurementItems()
  → executeWorkItems()：全局打散任务 + worker pool
  → query() / performQuery()
  → aggregateResults()：结构合法 + 多服务器共识过滤
  → calculateScores()
  → CLI table / recommendations / JSON
```

## 查询模型

- UDP 与 DoT 通过 worker 级别连接缓存复用连接。
- DoH 通过复用 `http.Client` / `http.Transport` 保持连接池。
- 内置 DoT/DoH 预设尽量使用 bootstrap IP；其余域名端点会在 benchmark 开始前解析一次并固定。
- 响应必须能为请求域名提供可用的 `A` / `AAAA` 结果，必要时会沿 CNAME 链继续校验。
- 同一域名若不同服务器答案不一致，单点离群答案会被共识过滤掉，不计入最终成功率。

## 评分与输出

- 默认正式查询次数：`10`
- 默认预热次数：`1`
- 默认最大并发：`20`
- 默认超时：`2s`
- `P95` 仅在有效样本数不少于 5 时展示
- Score 公式：

```text
Score = (1 / median_seconds) × (success_rate²) × (median / P95)
```

这里的 `success_rate` 是共识过滤后的有效成功率。

## 开发约束

- `README.md` 与 `README.zh-CN.md` 需要保持结构和能力说明同步。
- 若修改 CLI 参数、SDK 公开字段、评分逻辑、预热/共识规则，需同步更新 README 和本文件。
- 若修改 benchmark 主流程，至少更新一条单元测试或集成测试覆盖变更路径。

## 当前测试重点

- 纯函数：域名归一化、服务器解析、聚合与评分
- 关键协议路径：本地 UDP benchmark、本地 DoH benchmark
- CLI 组装：selector 构建、JSON 输出
