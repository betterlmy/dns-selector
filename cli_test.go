package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/betterlmy/dns-selector/selector"
)

func TestBuildSelector(t *testing.T) {
	sel, usingCustomServers, err := buildSelector(
		"global",
		"dot:1.1.1.1@cloudflare-dns.com,doh:https://1.1.1.1/dns-query@cloudflare-dns.com",
		"example.com,github.com",
		5,
		2,
		3,
		time.Second,
	)
	if err != nil {
		t.Fatalf("buildSelector returned error: %v", err)
	}
	if !usingCustomServers {
		t.Fatal("expected custom servers flag")
	}
	if sel.GetQueries() != 5 || sel.GetWarmupQueries() != 2 || sel.GetConcurrency() != 3 {
		t.Fatalf("unexpected selector config: %+v", sel)
	}
	servers := sel.GetServers()
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	if servers[0].TLSServerName != "cloudflare-dns.com" || servers[1].TLSServerName != "cloudflare-dns.com" {
		t.Fatalf("expected TLS overrides to be parsed, got %+v", servers)
	}
}

func TestPrintJSONResults(t *testing.T) {
	sel := selector.NewSelector(
		[]selector.DNSServer{{Name: "dns", Address: "1.1.1.1", Protocol: selector.UDP}},
		[]string{"example.com"},
		5,
		1,
		2,
		time.Second,
	)

	results := []selector.BenchmarkResult{{
		Name:             "dns",
		Address:          "1.1.1.1",
		Protocol:         selector.UDP,
		MedianTime:       12 * time.Millisecond,
		P95Time:          20 * time.Millisecond,
		SuccessRate:      1,
		Score:            80,
		Successes:        5,
		RawSuccesses:     5,
		Total:            5,
		AnswerMismatches: 0,
	}}

	var buf bytes.Buffer
	if err := printJSONResults(&buf, "global", true, sel, results); err != nil {
		t.Fatalf("printJSONResults returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("failed to unmarshal JSON payload: %v", err)
	}

	if payload["preset"] != "global" {
		t.Fatalf("unexpected preset: %v", payload["preset"])
	}
	if payload["warmup_queries"] != float64(1) {
		t.Fatalf("unexpected warmup queries: %v", payload["warmup_queries"])
	}
	resultsJSON, ok := payload["results"].([]any)
	if !ok || len(resultsJSON) != 1 {
		t.Fatalf("unexpected results payload: %#v", payload["results"])
	}
}
