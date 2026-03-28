package selector

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	UDP = "udp"
	DOT = "dot"
	DOH = "doh"
)

const (
	defaultConcurrent    = 20
	defaultQueries       = 10
	defaultWarmupQueries = 1
	minSamplesForP95     = 5
)

// DNSServer describes a DNS server endpoint.
type DNSServer struct {
	Name          string
	Address       string
	Protocol      string
	TLSServerName string
	BootstrapIPs  []string
}

// QueryResult holds the outcome of a single DNS query.
type QueryResult struct {
	Server      DNSServer
	ServerKey   string
	Domain      string
	Duration    time.Duration
	Err         error
	Fingerprint string
	Warmup      bool
}

// BenchmarkResult holds aggregated statistics for one DNS server.
type BenchmarkResult struct {
	Name, Address, Protocol string
	MedianTime              time.Duration
	P95Time                 time.Duration
	SuccessRate, Score      float64
	Successes, Total        int
	RawSuccesses            int
	AnswerMismatches        int
}

type serverStats struct {
	server           DNSServer
	successes        int
	rawSuccesses     int
	answerMismatches int
	total            int
	durations        []time.Duration
}

// Selector holds the benchmark configuration.
// Use NewCNSelector or NewGlobalSelector to start from a preset,
// then adjust fields as needed before calling Benchmark.
type Selector struct {
	Servers       []DNSServer
	Domains       []string
	Queries       int
	WarmupQueries int
	Concurrency   int
	Timeout       time.Duration
}

// NewSelector returns a Selector with the provided configuration.
// Slice inputs are copied to avoid sharing mutable backing arrays.
func NewSelector(servers []DNSServer, domains []string, queries, warmupQueries, concurrency int, timeout time.Duration) *Selector {
	return &Selector{
		Servers:       cloneServers(servers),
		Domains:       cloneDomains(domains),
		Queries:       queries,
		WarmupQueries: warmupQueries,
		Concurrency:   concurrency,
		Timeout:       timeout,
	}
}

// NewCNSelector returns a Selector pre-configured with the CN preset.
func NewCNSelector() *Selector {
	return NewSelector(cnDNSServers, cnDefaultDomains, defaultQueries, defaultWarmupQueries, defaultConcurrent, 2*time.Second)
}

// NewGlobalSelector returns a Selector pre-configured with the Global preset.
func NewGlobalSelector() *Selector {
	return NewSelector(globalDNSServers, globalDefaultDomains, defaultQueries, defaultWarmupQueries, defaultConcurrent, 2*time.Second)
}

// NewSelectorForPreset returns a Selector initialized from a built-in preset.
func NewSelectorForPreset(name string) (*Selector, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cn":
		return NewCNSelector(), nil
	case "global":
		return NewGlobalSelector(), nil
	default:
		return nil, fmt.Errorf("unsupported preset: %s, available values: cn, global", name)
	}
}

var (
	cnDNSServers = []DNSServer{
		{Name: "AliDNS 1 (UDP)", Address: "223.5.5.5", Protocol: UDP},
		{Name: "AliDNS 2 (UDP)", Address: "223.6.6.6", Protocol: UDP},
		{Name: "BaiduDNS (UDP)", Address: "180.76.76.76", Protocol: UDP},
		{Name: "DNSPod 1 (UDP)", Address: "119.28.28.28", Protocol: UDP},
		{Name: "DNSPod 2 (UDP)", Address: "119.29.29.29", Protocol: UDP},
		{Name: "114DNS 1 (UDP)", Address: "114.114.114.114", Protocol: UDP},
		{Name: "114DNS 2 (UDP)", Address: "114.114.115.115", Protocol: UDP},
		{Name: "114DNS Safe 1 (UDP)", Address: "114.114.114.119", Protocol: UDP},
		{Name: "114DNS Safe 2 (UDP)", Address: "114.114.115.119", Protocol: UDP},
		{Name: "114DNS Family 1 (UDP)", Address: "114.114.114.110", Protocol: UDP},
		{Name: "114DNS Family 2 (UDP)", Address: "114.114.115.110", Protocol: UDP},
		{Name: "Bytedance 1 (UDP)", Address: "180.184.1.1", Protocol: UDP},
		{Name: "Bytedance 2 (UDP)", Address: "180.184.2.2", Protocol: UDP},
		{Name: "Google 1 (UDP)", Address: "8.8.8.8", Protocol: UDP},
		{Name: "Google 2 (UDP)", Address: "8.8.4.4", Protocol: UDP},
		{Name: "Cloudflare 1 (UDP)", Address: "1.1.1.1", Protocol: UDP},
		{Name: "Cloudflare 2 (UDP)", Address: "1.0.0.1", Protocol: UDP},
		{Name: "Freenom 1 (UDP)", Address: "80.80.80.80", Protocol: UDP},
		{Name: "Freenom 2 (UDP)", Address: "80.80.81.81", Protocol: UDP},

		{Name: "AliDNS (DoT)", Address: "dns.alidns.com", Protocol: DOT, TLSServerName: "dns.alidns.com", BootstrapIPs: []string{"223.5.5.5", "223.6.6.6"}},
		{Name: "DNSPod (DoT)", Address: "dot.pub", Protocol: DOT, TLSServerName: "dot.pub"},
		{Name: "Google (DoT)", Address: "dns.google", Protocol: DOT, TLSServerName: "dns.google", BootstrapIPs: []string{"8.8.8.8", "8.8.4.4"}},
		{Name: "Cloudflare 1 (DoT)", Address: "1.1.1.1", Protocol: DOT, TLSServerName: "cloudflare-dns.com"},
		{Name: "Cloudflare 2 (DoT)", Address: "one.one.one.one", Protocol: DOT, TLSServerName: "one.one.one.one", BootstrapIPs: []string{"1.1.1.1", "1.0.0.1"}},

		{Name: "AliDNS 1 (DoH)", Address: "https://dns.alidns.com/dns-query", Protocol: DOH, BootstrapIPs: []string{"223.5.5.5", "223.6.6.6"}},
		{Name: "AliDNS 2 (DoH)", Address: "https://223.5.5.5/dns-query", Protocol: DOH, TLSServerName: "dns.alidns.com"},
		{Name: "AliDNS 3 (DoH)", Address: "https://223.6.6.6/dns-query", Protocol: DOH, TLSServerName: "dns.alidns.com"},
		{Name: "DNSPod (DoH)", Address: "https://doh.pub/dns-query", Protocol: DOH},
		{Name: "Cloudflare 1 (DoH)", Address: "https://cloudflare-dns.com/dns-query", Protocol: DOH, BootstrapIPs: []string{"1.1.1.1", "1.0.0.1"}},
		{Name: "Cloudflare 2 (DoH)", Address: "https://1.1.1.1/dns-query", Protocol: DOH, TLSServerName: "cloudflare-dns.com"},
		{Name: "Cloudflare 3 (DoH)", Address: "https://1.0.0.1/dns-query", Protocol: DOH, TLSServerName: "cloudflare-dns.com"},
		{Name: "Google (DoH)", Address: "https://dns.google/dns-query", Protocol: DOH, BootstrapIPs: []string{"8.8.8.8", "8.8.4.4"}},
	}

	globalDNSServers = []DNSServer{
		{Name: "Google 1 (UDP)", Address: "8.8.8.8", Protocol: UDP},
		{Name: "Google 2 (UDP)", Address: "8.8.4.4", Protocol: UDP},
		{Name: "Cloudflare 1 (UDP)", Address: "1.1.1.1", Protocol: UDP},
		{Name: "Cloudflare 2 (UDP)", Address: "1.0.0.1", Protocol: UDP},
		{Name: "Quad9 1 (UDP)", Address: "9.9.9.9", Protocol: UDP},
		{Name: "Quad9 2 (UDP)", Address: "149.112.112.112", Protocol: UDP},
		{Name: "OpenDNS 1 (UDP)", Address: "208.67.222.222", Protocol: UDP},
		{Name: "OpenDNS 2 (UDP)", Address: "208.67.220.220", Protocol: UDP},

		{Name: "Google (DoT)", Address: "dns.google", Protocol: DOT, TLSServerName: "dns.google", BootstrapIPs: []string{"8.8.8.8", "8.8.4.4"}},
		{Name: "Cloudflare (DoT)", Address: "one.one.one.one", Protocol: DOT, TLSServerName: "one.one.one.one", BootstrapIPs: []string{"1.1.1.1", "1.0.0.1"}},
		{Name: "Quad9 (DoT)", Address: "dns.quad9.net", Protocol: DOT, TLSServerName: "dns.quad9.net", BootstrapIPs: []string{"9.9.9.9", "149.112.112.112"}},

		{Name: "Google (DoH)", Address: "https://dns.google/dns-query", Protocol: DOH, BootstrapIPs: []string{"8.8.8.8", "8.8.4.4"}},
		{Name: "Cloudflare (DoH)", Address: "https://cloudflare-dns.com/dns-query", Protocol: DOH, BootstrapIPs: []string{"1.1.1.1", "1.0.0.1"}},
		{Name: "Quad9 (DoH)", Address: "https://dns.quad9.net/dns-query", Protocol: DOH, BootstrapIPs: []string{"9.9.9.9", "149.112.112.112"}},
	}

	cnDefaultDomains = []string{
		"douyin.com", "kuaishou.com", "baidu.com", "taobao.com", "mi.com", "aliyun.com",
		"bilibili.com", "jd.com", "qq.com", "ithome.com", "hupu.com", "feishu.cn",
		"sohu.com", "163.com", "sina.com", "weibo.com", "xiaohongshu.com", "douban.com", "zhihu.com",
		"youku.com", "youdao.com", "mp.weixin.qq.com", "iqiyi.com", "v.qq.com", "y.qq.com",
		"www.ctrip.com", "autohome.com.cn", "apple.com", "github.com", "bing.com",
	}

	globalDefaultDomains = []string{
		"google.com", "youtube.com", "github.com", "wikipedia.org", "cloudflare.com", "amazon.com",
		"openai.com", "chatgpt.com", "apple.com", "microsoft.com", "reddit.com", "netflix.com",
		"bbc.com", "nytimes.com", "linkedin.com", "instagram.com", "x.com", "tiktok.com",
		"discord.com", "zoom.us", "dropbox.com", "ubuntu.com", "mozilla.org", "stackoverflow.com",
	}
)

// GetDefaultCNServers returns a copy of the built-in CN preset server list.
func GetDefaultCNServers() []DNSServer {
	return cloneServers(cnDNSServers)
}

// GetDefaultGlobalServers returns a copy of the built-in Global preset server list.
func GetDefaultGlobalServers() []DNSServer {
	return cloneServers(globalDNSServers)
}

// GetDefaultCNDomains returns a copy of the built-in CN preset test domain list.
func GetDefaultCNDomains() []string {
	return cloneDomains(cnDefaultDomains)
}

// GetDefaultGlobalDomains returns a copy of the built-in Global preset test domain list.
func GetDefaultGlobalDomains() []string {
	return cloneDomains(globalDefaultDomains)
}

// ParseServers converts CLI-style protocol:address entries into DNS servers.
func ParseServers(raw string) ([]DNSServer, error) {
	return parseServers(raw)
}

// NormalizeDomains normalizes and de-duplicates domains from comma-separated input.
func NormalizeDomains(raw string) []string {
	return normalizeDomains(raw)
}

// GetServers returns a copy of the selector's current server list.
func (s *Selector) GetServers() []DNSServer {
	return cloneServers(s.Servers)
}

// SetServers replaces the selector's server list using a defensive copy.
func (s *Selector) SetServers(servers []DNSServer) {
	s.Servers = cloneServers(servers)
}

// GetDomains returns a copy of the selector's current domain list.
func (s *Selector) GetDomains() []string {
	return cloneDomains(s.Domains)
}

// SetDomains replaces the selector's domain list using a defensive copy.
func (s *Selector) SetDomains(domains []string) {
	s.Domains = cloneDomains(domains)
}

// GetQueries returns the selector's query count per domain.
func (s *Selector) GetQueries() int {
	return s.Queries
}

// SetQueries updates the selector's query count per domain.
func (s *Selector) SetQueries(queries int) {
	s.Queries = queries
}

// GetWarmupQueries returns the selector's warmup query count per server.
func (s *Selector) GetWarmupQueries() int {
	return s.WarmupQueries
}

// SetWarmupQueries updates the selector's warmup query count per server.
func (s *Selector) SetWarmupQueries(warmupQueries int) {
	s.WarmupQueries = warmupQueries
}

// GetConcurrency returns the selector's maximum concurrency.
func (s *Selector) GetConcurrency() int {
	return s.Concurrency
}

// SetConcurrency updates the selector's maximum concurrency.
func (s *Selector) SetConcurrency(concurrency int) {
	s.Concurrency = concurrency
}

// GetTimeout returns the selector's per-query timeout.
func (s *Selector) GetTimeout() time.Duration {
	return s.Timeout
}

// SetTimeout updates the selector's per-query timeout.
func (s *Selector) SetTimeout(timeout time.Duration) {
	s.Timeout = timeout
}

// Print renders the selector's current configuration without executing a benchmark.
func (s *Selector) Print(lang string) {
	labels := selectorPrintLabels(lang)
	concurrency := s.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrent
	}

	serverItems := make([]string, 0, len(s.Servers))
	for _, server := range s.Servers {
		serverItems = append(serverItems, fmt.Sprintf("%s [%s:%s]", server.Name, server.Protocol, server.Address))
	}

	fmt.Println(labels.title)
	fmt.Printf("%s (%d): %s\n", labels.servers, len(s.Servers), strings.Join(serverItems, ", "))
	fmt.Printf("%s (%d): %s\n", labels.domains, len(s.Domains), strings.Join(s.Domains, ", "))
	fmt.Printf("%s: %d\n", labels.queries, s.Queries)
	fmt.Printf("%s: %d\n", labels.warmup, s.WarmupQueries)
	fmt.Printf("%s: %d\n", labels.concurrency, concurrency)
	fmt.Printf("%s: %s\n", labels.timeout, s.Timeout)
}

type printLabels struct {
	title       string
	servers     string
	domains     string
	queries     string
	warmup      string
	concurrency string
	timeout     string
}

func selectorPrintLabels(lang string) printLabels {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "global":
		return printLabels{
			title:       "--- Current Selector Configuration ---",
			servers:     "DNS servers",
			domains:     "Test domains",
			queries:     "Queries per domain",
			warmup:      "Warmup queries per server",
			concurrency: "Max concurrency",
			timeout:     "Per-query timeout",
		}
	default:
		return printLabels{
			title:       "--- 当前 Selector 配置 ---",
			servers:     "DNS 服务器",
			domains:     "测试域名",
			queries:     "每域名查询次数",
			warmup:      "每服务器预热查询次数",
			concurrency: "最大并发",
			timeout:     "单次查询超时",
		}
	}
}

// Validate checks whether the selector contains a runnable benchmark configuration.
func (s *Selector) Validate() error {
	if len(s.Servers) == 0 {
		return errors.New("no servers configured")
	}
	if len(s.Domains) == 0 {
		return errors.New("no valid test domains")
	}
	if s.Queries <= 0 {
		return errors.New("queries must be greater than 0")
	}
	if s.WarmupQueries < 0 {
		return errors.New("warmup queries must be greater than or equal to 0")
	}
	if s.Timeout <= 0 {
		return errors.New("timeout must be greater than 0")
	}
	return nil
}

func resolvePreset(name string) ([]DNSServer, []string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cn":
		return cloneServers(cnDNSServers), cloneDomains(cnDefaultDomains), nil
	case "global":
		return cloneServers(globalDNSServers), cloneDomains(globalDefaultDomains), nil
	default:
		return nil, nil, fmt.Errorf("unsupported preset: %s, available values: cn, global", name)
	}
}

func parseServers(raw string) ([]DNSServer, error) {
	parts := strings.Split(raw, ",")
	servers := make([]DNSServer, 0, len(parts))

	for idx, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}

		protocol, addressWithOptions, ok := strings.Cut(item, ":")
		if !ok {
			return nil, fmt.Errorf("invalid server entry: %s", item)
		}

		protocol = strings.ToLower(strings.TrimSpace(protocol))
		address, tlsServerName, err := splitAddressAndTLSName(protocol, strings.TrimSpace(addressWithOptions))
		if err != nil {
			return nil, err
		}
		if address == "" {
			return nil, fmt.Errorf("invalid server entry: %s", item)
		}

		server, err := buildCustomServer(protocol, address, tlsServerName, idx+1)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}

	return servers, nil
}

func splitAddressAndTLSName(protocol, raw string) (string, string, error) {
	address := strings.TrimSpace(raw)
	if address == "" {
		return "", "", nil
	}

	idx := strings.LastIndex(address, "@")
	if idx == -1 {
		return address, "", nil
	}

	base := strings.TrimSpace(address[:idx])
	tlsServerName := strings.TrimSpace(address[idx+1:])
	if base == "" || tlsServerName == "" {
		return "", "", fmt.Errorf("invalid server entry: %s", raw)
	}
	if protocol == UDP {
		return "", "", errors.New("tls server name is only supported for dot and doh servers")
	}
	if protocol == DOH {
		parsed, err := url.Parse(base)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", "", fmt.Errorf("invalid doh address: %s", base)
		}
	}
	return base, tlsServerName, nil
}

func buildCustomServer(protocol, address, tlsServerName string, index int) (DNSServer, error) {
	switch protocol {
	case UDP:
		return DNSServer{
			Name:     fmt.Sprintf("Custom %d (UDP)", index),
			Address:  address,
			Protocol: UDP,
		}, nil
	case DOT:
		host, _, err := splitHostPortDefault(address, "853")
		if err != nil {
			return DNSServer{}, err
		}
		if tlsServerName == "" && net.ParseIP(strings.Trim(host, "[]")) == nil {
			tlsServerName = host
		}
		return DNSServer{
			Name:          fmt.Sprintf("Custom %d (DoT)", index),
			Address:       address,
			Protocol:      DOT,
			TLSServerName: tlsServerName,
		}, nil
	case DOH:
		parsed, err := url.Parse(address)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return DNSServer{}, fmt.Errorf("invalid doh address: %s", address)
		}
		if tlsServerName == "" && parsed.Scheme == "https" && net.ParseIP(parsed.Hostname()) == nil {
			tlsServerName = parsed.Hostname()
		}
		return DNSServer{
			Name:          fmt.Sprintf("Custom %d (DoH)", index),
			Address:       address,
			Protocol:      DOH,
			TLSServerName: tlsServerName,
		}, nil
	default:
		return DNSServer{}, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

func normalizeDomains(raw string) []string {
	seen := make(map[string]struct{})
	var domains []string

	for _, item := range strings.Split(raw, ",") {
		domain := strings.TrimSpace(strings.ToLower(item))
		if domain == "" {
			continue
		}
		domain = strings.TrimSuffix(domain, ".")
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}

	sort.Strings(domains)
	return domains
}

func buildWorkItems(domains []string, repeat int) []string {
	workItems := make([]string, 0, len(domains)*repeat)
	for _, domain := range domains {
		for i := 0; i < repeat; i++ {
			workItems = append(workItems, domain)
		}
	}
	return workItems
}

func cloneServers(src []DNSServer) []DNSServer {
	out := make([]DNSServer, len(src))
	for i, server := range src {
		out[i] = server
		if len(server.BootstrapIPs) > 0 {
			out[i].BootstrapIPs = append([]string{}, server.BootstrapIPs...)
		}
	}
	return out
}

func cloneDomains(src []string) []string {
	out := make([]string, len(src))
	copy(out, src)
	return out
}
