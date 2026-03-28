package selector

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type preparedServer struct {
	server        DNSServer
	key           string
	dialAddress   string
	tlsServerName string
	requestURL    string
	requestHost   string
	httpClient    *http.Client
	prepareErr    error
}

type workerState struct {
	timeout     time.Duration
	connections map[string]*dnsConnection
}

type dnsConnection struct {
	client *dns.Client
	conn   *dns.Conn
}

type answerSummary struct {
	fingerprint string
}

func newWorkerState(timeout time.Duration) *workerState {
	return &workerState{
		timeout:     timeout,
		connections: make(map[string]*dnsConnection),
	}
}

func (w *workerState) Close() {
	for key, conn := range w.connections {
		_ = conn.conn.Close()
		delete(w.connections, key)
	}
}

func prepareServers(ctx context.Context, servers []DNSServer, timeout time.Duration) []preparedServer {
	prepared := make([]preparedServer, 0, len(servers))
	for _, server := range servers {
		prepared = append(prepared, prepareServer(ctx, server, timeout))
	}
	return prepared
}

func prepareServer(ctx context.Context, server DNSServer, timeout time.Duration) preparedServer {
	prepared := preparedServer{
		server:        server,
		key:           serverKey(server),
		tlsServerName: server.TLSServerName,
	}

	switch server.Protocol {
	case UDP:
		host, port, err := splitHostPortDefault(server.Address, "53")
		if err != nil {
			prepared.prepareErr = err
			return prepared
		}
		dialHost, err := resolveDialHost(ctx, host, server.BootstrapIPs, timeout)
		if err != nil {
			prepared.prepareErr = err
			return prepared
		}
		prepared.dialAddress = net.JoinHostPort(dialHost, port)
	case DOT:
		host, port, err := splitHostPortDefault(server.Address, "853")
		if err != nil {
			prepared.prepareErr = err
			return prepared
		}
		if prepared.tlsServerName == "" && net.ParseIP(strings.Trim(host, "[]")) == nil {
			prepared.tlsServerName = host
		}
		dialHost, err := resolveDialHost(ctx, host, server.BootstrapIPs, timeout)
		if err != nil {
			prepared.prepareErr = err
			return prepared
		}
		prepared.dialAddress = net.JoinHostPort(dialHost, port)
	case DOH:
		parsedURL, err := url.Parse(server.Address)
		if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
			prepared.prepareErr = fmt.Errorf("invalid doh address: %s", server.Address)
			return prepared
		}

		host := parsedURL.Hostname()
		port := parsedURL.Port()
		if port == "" {
			port = defaultPortForScheme(parsedURL.Scheme)
		}
		if prepared.tlsServerName == "" && parsedURL.Scheme == "https" && net.ParseIP(host) == nil {
			prepared.tlsServerName = host
		}

		dialHost, err := resolveDialHost(ctx, host, server.BootstrapIPs, timeout)
		if err != nil {
			prepared.prepareErr = err
			return prepared
		}

		prepared.dialAddress = net.JoinHostPort(dialHost, port)
		prepared.requestURL = parsedURL.String()
		if net.ParseIP(host) != nil && prepared.tlsServerName != "" {
			prepared.requestHost = prepared.tlsServerName
			if parsedURL.Port() != "" && parsedURL.Port() != defaultPortForScheme(parsedURL.Scheme) {
				prepared.requestHost = net.JoinHostPort(prepared.tlsServerName, parsedURL.Port())
			}
		}
		prepared.httpClient = buildHTTPClient(prepared, timeout, parsedURL.Scheme)
	default:
		prepared.prepareErr = fmt.Errorf("unsupported protocol: %s", server.Protocol)
	}

	return prepared
}

func buildHTTPClient(server preparedServer, timeout time.Duration, scheme string) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          defaultConcurrent,
		MaxIdleConnsPerHost:   defaultConcurrent,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
	}

	if server.dialAddress != "" {
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: timeout}
			return dialer.DialContext(ctx, network, server.dialAddress)
		}
	}

	if scheme == "https" {
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		if server.tlsServerName != "" {
			transport.TLSClientConfig.ServerName = server.tlsServerName
		}
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func query(ctx context.Context, worker *workerState, server preparedServer, domain string, timeout time.Duration, warmup bool) QueryResult {
	result := QueryResult{
		Server:    server.server,
		ServerKey: server.key,
		Domain:    domain,
		Warmup:    warmup,
	}

	if server.prepareErr != nil {
		result.Err = server.prepareErr
		return result
	}

	qCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	fingerprint, err := performQuery(qCtx, worker, server, domain)
	result.Duration = time.Since(start)
	result.Err = err
	result.Fingerprint = fingerprint
	return result
}

func performQuery(ctx context.Context, worker *workerState, server preparedServer, domain string) (string, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(domain), dns.TypeA)
	msg.RecursionDesired = true

	var (
		resp *dns.Msg
		err  error
	)

	switch server.server.Protocol {
	case UDP, DOT:
		resp, err = worker.exchangeDNSQuery(ctx, server, msg)
	case DOH:
		resp, err = exchangeHTTPSQuery(ctx, server, msg)
	default:
		return "", fmt.Errorf("unsupported protocol: %s", server.server.Protocol)
	}
	if err != nil {
		return "", err
	}

	summary, err := validateDNSResponse(resp, domain)
	if err != nil {
		return "", err
	}
	return summary.fingerprint, nil
}

func (w *workerState) exchangeDNSQuery(ctx context.Context, server preparedServer, msg *dns.Msg) (*dns.Msg, error) {
	state, err := w.ensureConnection(ctx, server)
	if err != nil {
		return nil, err
	}

	resp, _, err := state.client.ExchangeWithConnContext(ctx, msg, state.conn)
	if err == nil {
		return resp, nil
	}

	_ = state.conn.Close()
	delete(w.connections, server.key)

	state, err = w.ensureConnection(ctx, server)
	if err != nil {
		return nil, err
	}
	resp, _, err = state.client.ExchangeWithConnContext(ctx, msg, state.conn)
	if err != nil {
		_ = state.conn.Close()
		delete(w.connections, server.key)
	}
	return resp, err
}

func (w *workerState) ensureConnection(ctx context.Context, server preparedServer) (*dnsConnection, error) {
	if state, ok := w.connections[server.key]; ok {
		return state, nil
	}

	client := &dns.Client{
		Net:     dnsNetwork(server.server.Protocol),
		Timeout: w.timeout,
		Dialer:  &net.Dialer{Timeout: w.timeout},
	}
	if server.server.Protocol == DOT {
		client.TLSConfig = &tls.Config{
			ServerName: server.tlsServerName,
			MinVersion: tls.VersionTLS12,
		}
	}

	conn, err := client.DialContext(ctx, server.dialAddress)
	if err != nil {
		return nil, err
	}

	state := &dnsConnection{
		client: client,
		conn:   conn,
	}
	w.connections[server.key] = state
	return state, nil
}

func exchangeHTTPSQuery(ctx context.Context, server preparedServer, msg *dns.Msg) (*dns.Msg, error) {
	wire, err := msg.Pack()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.requestURL, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-message")
	req.Header.Set("Content-Type", "application/dns-message")
	if server.requestHost != "" {
		req.Host = server.requestHost
	}

	resp, err := server.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	var answer dns.Msg
	if err := answer.Unpack(body); err != nil {
		return nil, err
	}
	return &answer, nil
}

func validateDNSResponse(resp *dns.Msg, domain string) (answerSummary, error) {
	if resp == nil {
		return answerSummary{}, errors.New("empty response")
	}
	if resp.Truncated {
		return answerSummary{}, errors.New("truncated response")
	}
	if resp.Rcode != dns.RcodeSuccess {
		return answerSummary{}, fmt.Errorf("dns rcode %s", dns.RcodeToString[resp.Rcode])
	}

	fingerprint, err := fingerprintDNSAnswer(resp, domain)
	if err != nil {
		return answerSummary{}, err
	}
	return answerSummary{fingerprint: fingerprint}, nil
}

func fingerprintDNSAnswer(resp *dns.Msg, domain string) (string, error) {
	target := dns.Fqdn(domain)
	relevantNames := map[string]struct{}{target: {}}
	addresses := make(map[string]struct{})
	cnames := make(map[string]struct{})

	for step := 0; step < 8; step++ {
		expanded := false
		for _, rr := range resp.Answer {
			switch record := rr.(type) {
			case *dns.CNAME:
				if _, ok := relevantNames[record.Hdr.Name]; !ok {
					continue
				}
				targetName := dns.Fqdn(record.Target)
				cnames[strings.TrimSuffix(targetName, ".")] = struct{}{}
				if _, seen := relevantNames[targetName]; !seen {
					relevantNames[targetName] = struct{}{}
					expanded = true
				}
			case *dns.A:
				if _, ok := relevantNames[record.Hdr.Name]; ok {
					addresses[record.A.String()] = struct{}{}
				}
			case *dns.AAAA:
				if _, ok := relevantNames[record.Hdr.Name]; ok {
					addresses[record.AAAA.String()] = struct{}{}
				}
			}
		}
		if len(addresses) > 0 || !expanded {
			break
		}
	}

	if len(addresses) == 0 {
		return "", errors.New("no usable A/AAAA answer")
	}

	parts := make([]string, 0, len(cnames)+len(addresses))
	if len(cnames) > 0 {
		cnameList := make([]string, 0, len(cnames))
		for cname := range cnames {
			cnameList = append(cnameList, cname)
		}
		sort.Strings(cnameList)
		parts = append(parts, "cname="+strings.Join(cnameList, ","))
	}

	addressList := make([]string, 0, len(addresses))
	for address := range addresses {
		addressList = append(addressList, address)
	}
	sort.Strings(addressList)
	parts = append(parts, "addr="+strings.Join(addressList, ","))

	return strings.Join(parts, "|"), nil
}

func resolveDialHost(ctx context.Context, host string, bootstrapIPs []string, timeout time.Duration) (string, error) {
	trimmedHost := strings.Trim(host, "[]")
	if trimmedHost == "" {
		return "", errors.New("empty host")
	}
	if ip := net.ParseIP(trimmedHost); ip != nil {
		return ip.String(), nil
	}

	for _, bootstrap := range bootstrapIPs {
		if ip := net.ParseIP(strings.TrimSpace(bootstrap)); ip != nil {
			return ip.String(), nil
		}
	}

	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, trimmedHost)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no ip addresses found for %s", trimmedHost)
	}

	ips := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP.String())
	}
	sort.Strings(ips)
	return ips[0], nil
}

func splitHostPortDefault(address, defaultPort string) (string, string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", "", errors.New("empty address")
	}

	if host, port, err := net.SplitHostPort(address); err == nil {
		return strings.Trim(host, "[]"), port, nil
	}

	return strings.Trim(address, "[]"), defaultPort, nil
}

func serverKey(server DNSServer) string {
	return strings.Join([]string{
		server.Protocol,
		server.Address,
		server.TLSServerName,
		server.Name,
	}, "|")
}

func dnsNetwork(protocol string) string {
	switch protocol {
	case UDP:
		return "udp"
	case DOT:
		return "tcp-tls"
	default:
		return ""
	}
}

func defaultPortForScheme(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}
