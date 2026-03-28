package selector

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestBenchmarkRejectsConsensusOutlier(t *testing.T) {
	goodAddr1 := startUDPDNSServer(t, "1.1.1.1", 0)
	goodAddr2 := startUDPDNSServer(t, "1.1.1.1", 5*time.Millisecond)
	badAddr := startUDPDNSServer(t, "9.9.9.9", 0)

	sel := NewSelector(
		[]DNSServer{
			{Name: "good-1", Address: goodAddr1, Protocol: UDP},
			{Name: "good-2", Address: goodAddr2, Protocol: UDP},
			{Name: "bad", Address: badAddr, Protocol: UDP},
		},
		[]string{"example.com"},
		3,
		0,
		3,
		time.Second,
	)

	results, err := sel.Benchmark(context.Background(), nil)
	if err != nil {
		t.Fatalf("benchmark returned error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	foundBad := false
	for _, result := range results {
		if result.Name != "bad" {
			if result.Successes != 3 || result.AnswerMismatches != 0 {
				t.Fatalf("expected good server to fully pass consensus, got %+v", result)
			}
			continue
		}

		foundBad = true
		if result.RawSuccesses != 3 || result.Successes != 0 || result.AnswerMismatches != 3 {
			t.Fatalf("expected outlier server to be rejected by consensus, got %+v", result)
		}
	}

	if !foundBad {
		t.Fatal("expected bad server result")
	}
}

func TestBenchmarkSupportsDoHOverHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := ioReadAllLimit(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req dns.Msg
		if err := req.Unpack(body); err != nil {
			t.Fatalf("failed to unpack request: %v", err)
		}

		resp := new(dns.Msg)
		resp.SetReply(&req)
		resp.Answer = []dns.RR{mustARecord(t, "example.com. 60 IN A 1.1.1.1")}

		wire, err := resp.Pack()
		if err != nil {
			t.Fatalf("failed to pack response: %v", err)
		}

		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	}))
	defer server.Close()

	sel := NewSelector(
		[]DNSServer{{Name: "local-doh", Address: server.URL, Protocol: DOH}},
		[]string{"example.com"},
		2,
		1,
		1,
		time.Second,
	)

	results, err := sel.Benchmark(context.Background(), nil)
	if err != nil {
		t.Fatalf("benchmark returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Successes != 2 || results[0].RawSuccesses != 2 {
		t.Fatalf("expected DoH server to succeed, got %+v", results[0])
	}
}

func startUDPDNSServer(t *testing.T, answerIP string, delay time.Duration) string {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on udp socket: %v", err)
	}

	server := &dns.Server{
		PacketConn: pc,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
			if delay > 0 {
				time.Sleep(delay)
			}
			resp := new(dns.Msg)
			resp.SetReply(r)
			resp.Answer = []dns.RR{mustARecord(t, fmt.Sprintf("%s 60 IN A %s", r.Question[0].Name, answerIP))}
			_ = w.WriteMsg(resp)
		}),
	}

	go func() {
		_ = server.ActivateAndServe()
	}()

	t.Cleanup(func() {
		_ = server.Shutdown()
	})

	return pc.LocalAddr().String()
}

func ioReadAllLimit(body io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(body, 64*1024))
}
