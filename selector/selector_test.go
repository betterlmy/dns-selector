package selector

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestNormalizeDomains(t *testing.T) {
	got := normalizeDomains(" Example.com,example.com., GitHub.com , ,github.com")
	want := []string{"example.com", "github.com"}

	if len(got) != len(want) {
		t.Fatalf("unexpected domain count: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected domain at %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestResolvePreset(t *testing.T) {
	servers, domains, err := resolvePreset("global")
	if err != nil {
		t.Fatalf("resolvePreset returned error: %v", err)
	}
	if len(servers) == 0 || len(domains) == 0 {
		t.Fatalf("expected populated preset, got %d servers and %d domains", len(servers), len(domains))
	}
	servers[0].Name = "mutated"
	if globalDNSServers[0].Name == "mutated" {
		t.Fatal("resolvePreset should return copies")
	}
}

func TestResolvePresetInvalid(t *testing.T) {
	_, _, err := resolvePreset("unknown")
	if err == nil {
		t.Fatal("expected error for invalid preset")
	}
}

func TestParseServers(t *testing.T) {
	servers, err := parseServers("udp:1.1.1.1,dot:1.1.1.1@cloudflare-dns.com,doh:https://1.1.1.1/dns-query@cloudflare-dns.com")
	if err != nil {
		t.Fatalf("parseServers returned error: %v", err)
	}

	if len(servers) != 3 {
		t.Fatalf("unexpected server count: got %d want 3", len(servers))
	}
	if servers[0].Protocol != UDP || servers[0].Address != "1.1.1.1" {
		t.Fatalf("unexpected UDP server: %+v", servers[0])
	}
	if servers[1].Protocol != DOT || servers[1].TLSServerName != "cloudflare-dns.com" {
		t.Fatalf("unexpected DoT server: %+v", servers[1])
	}
	if servers[2].Protocol != DOH || servers[2].TLSServerName != "cloudflare-dns.com" {
		t.Fatalf("unexpected DoH server: %+v", servers[2])
	}
}

func TestParseServersInvalid(t *testing.T) {
	tests := []string{
		"invalid-entry",
		"udp:",
		"udp:1.1.1.1@cloudflare-dns.com",
		"doh:https://",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := parseServers(input); err == nil {
				t.Fatalf("expected parseServers to fail for %q", input)
			}
		})
	}
}

func TestBuildWorkItems(t *testing.T) {
	got := buildWorkItems([]string{"a.com", "b.com"}, 2)
	want := []string{"a.com", "a.com", "b.com", "b.com"}

	if len(got) != len(want) {
		t.Fatalf("unexpected item count: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected work item at %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestBuildConsensusFingerprints(t *testing.T) {
	results := []QueryResult{
		{ServerKey: "good-1", Domain: "example.com", Fingerprint: "addr=1.1.1.1"},
		{ServerKey: "good-2", Domain: "example.com", Fingerprint: "addr=1.1.1.1"},
		{ServerKey: "bad-1", Domain: "example.com", Fingerprint: "addr=9.9.9.9"},
	}

	accepted := buildConsensusFingerprints(results)
	if len(accepted["example.com"]) != 1 {
		t.Fatalf("expected one accepted fingerprint, got %v", accepted["example.com"])
	}
	if _, ok := accepted["example.com"]["addr=1.1.1.1"]; !ok {
		t.Fatalf("expected repeated fingerprint to be accepted: %v", accepted["example.com"])
	}
}

func TestAggregateResults(t *testing.T) {
	good1 := DNSServer{Name: "good-1", Address: "1.1.1.1", Protocol: UDP}
	good2 := DNSServer{Name: "good-2", Address: "8.8.8.8", Protocol: UDP}
	bad := DNSServer{Name: "bad", Address: "9.9.9.9", Protocol: UDP}
	results := []QueryResult{
		{Server: good1, ServerKey: serverKey(good1), Domain: "example.com", Duration: 10 * time.Millisecond, Fingerprint: "addr=1.1.1.1"},
		{Server: good2, ServerKey: serverKey(good2), Domain: "example.com", Duration: 20 * time.Millisecond, Fingerprint: "addr=1.1.1.1"},
		{Server: bad, ServerKey: serverKey(bad), Domain: "example.com", Duration: 30 * time.Millisecond, Fingerprint: "addr=9.9.9.9"},
	}

	stats := aggregateResults(results)
	goodStats := stats[serverKey(good1)]
	if goodStats == nil {
		t.Fatal("expected aggregated stats for good server")
	}
	if goodStats.total != 1 || goodStats.rawSuccesses != 1 || goodStats.successes != 1 {
		t.Fatalf("unexpected aggregate counts: %+v", goodStats)
	}

	badStats := stats[serverKey(bad)]
	if badStats == nil {
		t.Fatal("expected aggregated stats for bad server")
	}
	if badStats.successes != 0 || badStats.answerMismatches != 1 {
		t.Fatalf("unexpected consensus counts: %+v", badStats)
	}
}

func TestCalculateScoresSortsByScore(t *testing.T) {
	stats := map[string]*serverStats{
		"fast": {
			server:    DNSServer{Name: "fast", Address: "1.1.1.1", Protocol: UDP},
			successes: 5,
			total:     5,
			durations: []time.Duration{10 * time.Millisecond, 11 * time.Millisecond, 12 * time.Millisecond, 13 * time.Millisecond, 14 * time.Millisecond},
		},
		"slow": {
			server:    DNSServer{Name: "slow", Address: "8.8.8.8", Protocol: UDP},
			successes: 5,
			total:     5,
			durations: []time.Duration{50 * time.Millisecond, 55 * time.Millisecond, 60 * time.Millisecond, 65 * time.Millisecond, 70 * time.Millisecond},
		},
	}

	results := calculateScores(stats)
	if len(results) != 2 {
		t.Fatalf("unexpected result count: %d", len(results))
	}
	if results[0].Name != "fast" {
		t.Fatalf("expected fast server first, got %s", results[0].Name)
	}
	if results[0].MedianTime != 12*time.Millisecond {
		t.Fatalf("unexpected median: %s", results[0].MedianTime)
	}
	if results[0].P95Time != 14*time.Millisecond {
		t.Fatalf("unexpected p95: %s", results[0].P95Time)
	}
}

func TestCalculateScoresSkipsP95ForSmallSample(t *testing.T) {
	stats := map[string]*serverStats{
		"small": {
			server:    DNSServer{Name: "small", Address: "1.1.1.1", Protocol: UDP},
			successes: 3,
			total:     3,
			durations: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond},
		},
	}

	results := calculateScores(stats)
	if len(results) != 1 {
		t.Fatalf("unexpected result count: %d", len(results))
	}
	if results[0].P95Time != 0 {
		t.Fatalf("expected p95 to be omitted for small sample, got %s", results[0].P95Time)
	}
}

func TestPercentile(t *testing.T) {
	values := []time.Duration{10, 20, 30, 40}
	if got := percentile(values, 0.5); got != 20 {
		t.Fatalf("unexpected p50: %v", got)
	}
	if got := percentile(values, 0.95); got != 40 {
		t.Fatalf("unexpected p95: %v", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Fatalf("unexpected percentile for nil slice: %v", got)
	}
}

func TestNewSelectorCopiesInputs(t *testing.T) {
	servers := []DNSServer{{Name: "custom", Address: "1.1.1.1", Protocol: UDP, BootstrapIPs: []string{"9.9.9.9"}}}
	domains := []string{"example.com"}

	sel := NewSelector(servers, domains, 2, 1, 3, time.Second)
	servers[0].Name = "mutated"
	servers[0].BootstrapIPs[0] = "8.8.8.8"
	domains[0] = "mutated.com"

	if sel.Servers[0].Name != "custom" {
		t.Fatalf("expected servers to be copied, got %+v", sel.Servers[0])
	}
	if sel.Servers[0].BootstrapIPs[0] != "9.9.9.9" {
		t.Fatalf("expected nested slices to be copied, got %+v", sel.Servers[0])
	}
	if sel.Domains[0] != "example.com" {
		t.Fatalf("expected domains to be copied, got %q", sel.Domains[0])
	}
	if sel.Queries != 2 || sel.WarmupQueries != 1 || sel.Concurrency != 3 || sel.Timeout != time.Second {
		t.Fatalf("unexpected selector config: %+v", sel)
	}
}

func TestSelectorValidateErrors(t *testing.T) {
	tests := []struct {
		name string
		sel  *Selector
	}{
		{
			name: "empty servers",
			sel:  &Selector{Domains: []string{"example.com"}, Queries: 1, Timeout: time.Second},
		},
		{
			name: "empty domains",
			sel:  &Selector{Servers: []DNSServer{{Name: "s", Address: "1.1.1.1", Protocol: UDP}}, Queries: 1, Timeout: time.Second},
		},
		{
			name: "invalid queries",
			sel:  &Selector{Servers: []DNSServer{{Name: "s", Address: "1.1.1.1", Protocol: UDP}}, Domains: []string{"example.com"}, Timeout: time.Second},
		},
		{
			name: "invalid warmup",
			sel:  &Selector{Servers: []DNSServer{{Name: "s", Address: "1.1.1.1", Protocol: UDP}}, Domains: []string{"example.com"}, Queries: 1, WarmupQueries: -1, Timeout: time.Second},
		},
		{
			name: "invalid timeout",
			sel:  &Selector{Servers: []DNSServer{{Name: "s", Address: "1.1.1.1", Protocol: UDP}}, Domains: []string{"example.com"}, Queries: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.sel.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSelectorPrint(t *testing.T) {
	sel := NewSelector(
		[]DNSServer{{Name: "Test DNS", Address: "1.1.1.1", Protocol: UDP}},
		[]string{"example.com"},
		10,
		1,
		0,
		2*time.Second,
	)

	output := captureStdout(t, func() {
		sel.Print("global")
	})

	expectedSnippets := []string{
		"Current Selector Configuration",
		"DNS servers (1): Test DNS [udp:1.1.1.1]",
		"Test domains (1): example.com",
		"Queries per domain: 10",
		"Warmup queries per server: 1",
		"Max concurrency: 20",
		"Per-query timeout: 2s",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(output, snippet) {
			t.Fatalf("expected output to contain %q, got:\n%s", snippet, output)
		}
	}
}

func TestGetDefaultCNServersDeepCopy(t *testing.T) {
	servers := GetDefaultCNServers()
	if len(servers) == 0 {
		t.Fatal("expected non-empty CN server list")
	}
	if len(servers[19].BootstrapIPs) == 0 {
		t.Fatal("expected bootstrap IPs on preset DoT server")
	}
	servers[19].BootstrapIPs[0] = "9.9.9.9"
	if GetDefaultCNServers()[19].BootstrapIPs[0] == "9.9.9.9" {
		t.Fatal("expected deep copy for bootstrap IPs")
	}
}

func TestWarmupAccessors(t *testing.T) {
	sel := NewCNSelector()
	sel.SetWarmupQueries(3)
	if got := sel.GetWarmupQueries(); got != 3 {
		t.Fatalf("unexpected warmup queries: %d", got)
	}
}

func TestPrepareServerDoHWithTLSOverride(t *testing.T) {
	server := prepareServer(context.Background(), DNSServer{
		Name:          "test",
		Address:       "https://1.1.1.1/dns-query",
		Protocol:      DOH,
		TLSServerName: "cloudflare-dns.com",
	}, time.Second)

	if server.prepareErr != nil {
		t.Fatalf("unexpected prepare error: %v", server.prepareErr)
	}
	if server.requestHost != "cloudflare-dns.com" {
		t.Fatalf("unexpected request host: %q", server.requestHost)
	}
	if server.dialAddress != "1.1.1.1:443" {
		t.Fatalf("unexpected dial address: %q", server.dialAddress)
	}
}

func TestResolveDialHostUsesBootstrap(t *testing.T) {
	host, err := resolveDialHost(context.Background(), "dns.google", []string{"8.8.8.8", "8.8.4.4"}, time.Second)
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}
	if host != "8.8.8.8" {
		t.Fatalf("unexpected bootstrap host: %s", host)
	}
}

func TestValidateDNSResponse(t *testing.T) {
	if _, err := validateDNSResponse(nil, "example.com"); err == nil {
		t.Fatal("expected nil response error")
	}

	msg := &dns.Msg{}
	msg.Rcode = dns.RcodeServerFailure
	if _, err := validateDNSResponse(msg, "example.com"); err == nil {
		t.Fatal("expected rcode error")
	}

	msg = &dns.Msg{}
	msg.Rcode = dns.RcodeSuccess
	msg.Answer = []dns.RR{mustARecord(t, "example.net. 60 IN A 1.1.1.1")}
	if _, err := validateDNSResponse(msg, "example.com"); err == nil {
		t.Fatal("expected irrelevant answer error")
	}

	msg = &dns.Msg{}
	msg.Rcode = dns.RcodeSuccess
	msg.Answer = []dns.RR{
		mustCNAMERecord(t, "example.com. 60 IN CNAME edge.example.net."),
		mustARecord(t, "edge.example.net. 60 IN A 1.1.1.1"),
	}
	summary, err := validateDNSResponse(msg, "example.com")
	if err != nil {
		t.Fatalf("expected valid response, got %v", err)
	}
	if !strings.Contains(summary.fingerprint, "addr=1.1.1.1") {
		t.Fatalf("unexpected fingerprint: %s", summary.fingerprint)
	}
}

func TestBuildHTTPClient(t *testing.T) {
	client := buildHTTPClient(preparedServer{
		dialAddress:   "1.1.1.1:443",
		tlsServerName: "dns.google",
	}, 2*time.Second, "https")

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.DialContext == nil {
		t.Fatal("expected custom dialer")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.ServerName != "dns.google" {
		t.Fatalf("unexpected TLS config: %+v", transport.TLSClientConfig)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	os.Stdout = writer

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()

	fn()

	_ = writer.Close()
	os.Stdout = original
	output := <-done
	_ = reader.Close()
	return output
}

func mustARecord(t *testing.T, record string) dns.RR {
	t.Helper()
	rr, err := dns.NewRR(record)
	if err != nil {
		t.Fatalf("failed to create rr: %v", err)
	}
	return rr
}

func mustCNAMERecord(t *testing.T, record string) dns.RR {
	t.Helper()
	rr, err := dns.NewRR(record)
	if err != nil {
		t.Fatalf("failed to create rr: %v", err)
	}
	return rr
}
