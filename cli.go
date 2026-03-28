package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/betterlmy/dns-selector/selector"
	"github.com/briandowns/spinner"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/schollz/progressbar/v3"
)

type messages struct {
	AppStart             string
	UsingPreset          string
	TestDomains          string
	QuerySummary         string
	WarmupSummary        string
	Aggregating          string
	ResultsTitle         string
	RecommendationsTitle string
	NoGoodServers        string
	ScoreLabel           string
	MedianLabel          string
	P95Label             string
	SuccessRateLabel     string
	TableServer          string
	TableAddress         string
	TableMedian          string
	TableP95             string
	TableSuccessRate     string
	TableMismatch        string
	TableScore           string
	UsingCustomServers   string
}

var messagesByLang = map[string]messages{
	"cn": {
		AppStart:             "DNS 优选器: 开始对 %d 个 DNS 服务器进行综合基准测试...",
		UsingPreset:          "使用预设: %s\n",
		TestDomains:          "测试域名 (%d个): %s\n",
		QuerySummary:         "每个域名正式查询 %d 次, 最大并发 %d, 总计 %d 次正式查询。\n",
		WarmupSummary:        "每个服务器预热 %d 次, 总计 %d 次预热查询。\n\n",
		Aggregating:          " 正在聚合和计算评分...",
		ResultsTitle:         "--- 综合测试结果 ---",
		RecommendationsTitle: "\n--- 最佳DNS推荐 (Top 3) ---",
		NoGoodServers:        "没有找到表现足够好的DNS服务器，请检查网络连接。",
		ScoreLabel:           "综合评分",
		MedianLabel:          "中位延迟",
		P95Label:             "P95延迟",
		SuccessRateLabel:     "成功率",
		TableServer:          "DNS服务器",
		TableAddress:         "地址",
		TableMedian:          "中位延迟",
		TableP95:             "P95延迟",
		TableSuccessRate:     "成功率",
		TableMismatch:        "答案不一致",
		TableScore:           "综合评分",
		UsingCustomServers:   "使用自定义 DNS 服务器 (%d个)\n",
	},
	"global": {
		AppStart:             "DNS Selector: starting benchmark across %d DNS servers...",
		UsingPreset:          "Preset: %s\n",
		TestDomains:          "Test domains (%d): %s\n",
		QuerySummary:         "%d measured queries per domain, max concurrency %d, total %d measured queries.\n",
		WarmupSummary:        "%d warmup queries per server, total %d warmup queries.\n\n",
		Aggregating:          " Aggregating results and calculating scores...",
		ResultsTitle:         "--- Benchmark Results ---",
		RecommendationsTitle: "\n--- Best DNS Recommendations (Top 3) ---",
		NoGoodServers:        "no DNS server met the recommendation threshold; check your network connection",
		ScoreLabel:           "Score",
		MedianLabel:          "Median",
		P95Label:             "P95",
		SuccessRateLabel:     "Success Rate",
		TableServer:          "DNS Server",
		TableAddress:         "Address",
		TableMedian:          "Median Latency",
		TableP95:             "P95 Latency",
		TableSuccessRate:     "Success Rate",
		TableMismatch:        "Answer Mismatches",
		TableScore:           "Score",
		UsingCustomServers:   "Using custom DNS servers (%d)\n",
	},
}

func getMessages(lang string) messages {
	if m, ok := messagesByLang[strings.ToLower(strings.TrimSpace(lang))]; ok {
		return m
	}
	return messagesByLang["cn"]
}

func runBenchmarkCLI(ctx context.Context, presetName, serversStr, domainsStr string, queries, warmup, concurrency int, timeout time.Duration, jsonOutput bool) error {
	sel, usingCustomServers, err := buildSelector(presetName, serversStr, domainsStr, queries, warmup, concurrency, timeout)
	if err != nil {
		return err
	}
	if err := sel.Validate(); err != nil {
		return err
	}

	msgs := getMessages(presetName)
	totalQueries := len(sel.GetServers()) * len(sel.GetDomains()) * sel.GetQueries()
	totalWarmups := len(sel.GetServers()) * sel.GetWarmupQueries()

	if jsonOutput {
		results, err := sel.Benchmark(ctx, nil)
		if err != nil {
			return err
		}
		return printJSONResults(os.Stdout, presetName, usingCustomServers, sel, results)
	}

	fmt.Printf(msgs.AppStart+"\n", len(sel.GetServers()))
	if usingCustomServers {
		fmt.Printf(msgs.UsingCustomServers, len(sel.GetServers()))
	} else {
		fmt.Printf(msgs.UsingPreset, presetName)
	}
	fmt.Printf(msgs.TestDomains, len(sel.GetDomains()), strings.Join(sel.GetDomains(), ", "))
	fmt.Printf(msgs.QuerySummary, sel.GetQueries(), effectiveConcurrency(sel), totalQueries)
	fmt.Printf(msgs.WarmupSummary, sel.GetWarmupQueries(), totalWarmups)

	sel.Print(presetName)

	bar := progressbar.NewOptions(totalQueries,
		progressbar.OptionSetWriter(color.Output),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionSetDescription("[cyan]Running queries[reset]"),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[green]=[reset]",
			SaucerHead:    "[green]>[reset]",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	results, err := sel.Benchmark(ctx, func() { _ = bar.Add(1) })
	if err != nil {
		return err
	}

	fmt.Println()
	s := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
	s.Suffix = msgs.Aggregating
	s.Start()
	s.Stop()

	fmt.Println()
	fmt.Println(msgs.ResultsTitle)
	printResultsTable(results, msgs)

	fmt.Println(msgs.RecommendationsTitle)
	printRecommendations(results, msgs)

	return nil
}

func buildSelector(presetName, serversStr, domainsStr string, queries, warmup, concurrency int, timeout time.Duration) (*selector.Selector, bool, error) {
	sel, err := selector.NewSelectorForPreset(presetName)
	if err != nil {
		return nil, false, err
	}

	if strings.TrimSpace(serversStr) != "" {
		servers, err := selector.ParseServers(serversStr)
		if err != nil {
			return nil, false, fmt.Errorf("%w\nservers must use protocol:address format, for example udp:1.1.1.1,dot:dns.google,doh:https://dns.google/dns-query,dot:1.1.1.1@cloudflare-dns.com", err)
		}
		if len(servers) == 0 {
			return nil, false, fmt.Errorf("no valid custom DNS servers provided\nservers must use protocol:address format, for example udp:1.1.1.1,dot:dns.google,doh:https://dns.google/dns-query,dot:1.1.1.1@cloudflare-dns.com")
		}
		sel.SetServers(servers)
	}

	if strings.TrimSpace(domainsStr) != "" {
		domains := selector.NormalizeDomains(domainsStr)
		if len(domains) == 0 {
			return nil, false, fmt.Errorf("no valid test domains")
		}
		sel.SetDomains(domains)
	}

	sel.SetQueries(queries)
	sel.SetWarmupQueries(warmup)
	sel.SetConcurrency(concurrency)
	sel.SetTimeout(timeout)

	return sel, strings.TrimSpace(serversStr) != "", nil
}

func effectiveConcurrency(sel *selector.Selector) int {
	if sel.GetConcurrency() <= 0 {
		return 20
	}
	return sel.GetConcurrency()
}

func printResultsTable(results []selector.BenchmarkResult, msgs messages) {
	table := tablewriter.NewWriter(os.Stdout)
	header := []string{msgs.TableServer, msgs.TableAddress, msgs.TableMedian, msgs.TableP95, msgs.TableSuccessRate, msgs.TableMismatch, msgs.TableScore}

	for _, r := range results {
		rateStr := fmt.Sprintf("%.1f%% (%d/%d, raw %d)", r.SuccessRate*100, r.Successes, r.Total, r.RawSuccesses)
		medianStr := durationString(r.MedianTime)
		p95Str := durationString(r.P95Time)
		mismatchStr := fmt.Sprintf("%d", r.AnswerMismatches)
		scoreStr := fmt.Sprintf("%.2f", r.Score)

		green := color.New(color.FgGreen).SprintFunc()
		red := color.New(color.FgRed).SprintFunc()
		if r.SuccessRate < 1.0 {
			rateStr = red(rateStr)
			if r.AnswerMismatches > 0 {
				mismatchStr = red(mismatchStr)
			}
		} else {
			rateStr = green(rateStr)
		}

		table.Append([]string{
			r.Name,
			r.Address,
			medianStr,
			p95Str,
			rateStr,
			mismatchStr,
			scoreStr,
		})
	}

	table.SetHeader(header)
	table.Render()
}

func durationString(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return d.Round(time.Microsecond).String()
}

func printRecommendations(results []selector.BenchmarkResult, msgs messages) {
	green := color.New(color.FgGreen, color.Bold)
	yellow := color.New(color.FgYellow)
	cyan := color.New(color.FgCyan)
	red := color.New(color.FgRed)

	found := 0
	for _, best := range results {
		if best.SuccessRate < 0.98 || best.MedianTime == 0 {
			continue
		}

		var c *color.Color
		switch found {
		case 0:
			c = green
		case 1:
			c = yellow
		default:
			c = cyan
		}

		c.Printf("#%d: %s (%s)\n", found+1, best.Name, best.Address)
		fmt.Printf("    %s: %.2f, %s: %s, %s: %s, %s: %.1f%%, mismatches: %d\n",
			msgs.ScoreLabel,
			best.Score,
			msgs.MedianLabel,
			durationString(best.MedianTime),
			msgs.P95Label,
			durationString(best.P95Time),
			msgs.SuccessRateLabel,
			best.SuccessRate*100,
			best.AnswerMismatches,
		)

		found++
		if found >= 3 {
			break
		}
	}

	if found == 0 {
		red.Println(msgs.NoGoodServers)
	}
}

type jsonBenchmarkOutput struct {
	GeneratedAt   time.Time             `json:"generated_at"`
	Preset        string                `json:"preset"`
	UsingCustom   bool                  `json:"using_custom_servers"`
	Domains       []string              `json:"domains"`
	Queries       int                   `json:"queries"`
	WarmupQueries int                   `json:"warmup_queries"`
	Concurrency   int                   `json:"concurrency"`
	Timeout       string                `json:"timeout"`
	Results       []jsonBenchmarkResult `json:"results"`
}

type jsonBenchmarkResult struct {
	Name             string  `json:"name"`
	Address          string  `json:"address"`
	Protocol         string  `json:"protocol"`
	MedianLatency    string  `json:"median_latency"`
	P95Latency       string  `json:"p95_latency,omitempty"`
	SuccessRate      float64 `json:"success_rate"`
	Score            float64 `json:"score"`
	Successes        int     `json:"successes"`
	RawSuccesses     int     `json:"raw_successes"`
	Total            int     `json:"total"`
	AnswerMismatches int     `json:"answer_mismatches"`
}

func printJSONResults(out io.Writer, presetName string, usingCustomServers bool, sel *selector.Selector, results []selector.BenchmarkResult) error {
	payload := jsonBenchmarkOutput{
		GeneratedAt:   time.Now().UTC(),
		Preset:        presetName,
		UsingCustom:   usingCustomServers,
		Domains:       sel.GetDomains(),
		Queries:       sel.GetQueries(),
		WarmupQueries: sel.GetWarmupQueries(),
		Concurrency:   effectiveConcurrency(sel),
		Timeout:       sel.GetTimeout().String(),
		Results:       make([]jsonBenchmarkResult, 0, len(results)),
	}

	for _, result := range results {
		item := jsonBenchmarkResult{
			Name:             result.Name,
			Address:          result.Address,
			Protocol:         result.Protocol,
			MedianLatency:    durationString(result.MedianTime),
			SuccessRate:      result.SuccessRate,
			Score:            result.Score,
			Successes:        result.Successes,
			RawSuccesses:     result.RawSuccesses,
			Total:            result.Total,
			AnswerMismatches: result.AnswerMismatches,
		}
		if result.P95Time > 0 {
			item.P95Latency = durationString(result.P95Time)
		}
		payload.Results = append(payload.Results, item)
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}
