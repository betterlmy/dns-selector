package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	preset := flag.String("preset", "cn", "network preset: cn or global")
	servers := flag.String("servers", "", "custom DNS servers in protocol:address format, comma-separated")
	domains := flag.String("domains", "", "comma-separated list of test domains; defaults to preset domains")
	queries := flag.Int("queries", 10, "number of measured queries per domain")
	warmup := flag.Int("warmup", 1, "number of warmup queries per server before measurement")
	concurrency := flag.Int("concurrency", 20, "maximum concurrent queries")
	timeout := flag.Duration("timeout", 2*time.Second, "timeout per DNS query")
	jsonOutput := flag.Bool("json", false, "emit machine-readable JSON output")

	flag.Parse()

	if err := runBenchmarkCLI(context.Background(), *preset, *servers, *domains, *queries, *warmup, *concurrency, *timeout, *jsonOutput); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
