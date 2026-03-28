package selector

import (
	"context"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

type workItem struct {
	server preparedServer
	domain string
	warmup bool
}

// Benchmark runs DNS benchmarks and returns results sorted by score.
// onProgress is called after each measured query completes and may be nil.
// It produces no output; display is the caller's responsibility.
func (s *Selector) Benchmark(ctx context.Context, onProgress func()) ([]BenchmarkResult, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	prepared := prepareServers(ctx, s.Servers, s.Timeout)
	concurrency := s.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrent
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	warmupItems := buildWarmupItems(prepared, s.Domains, s.WarmupQueries)
	shuffleWorkItems(rng, warmupItems)
	if _, err := executeWorkItems(ctx, warmupItems, concurrency, s.Timeout, nil); err != nil {
		return nil, err
	}

	items := buildMeasurementItems(prepared, s.Domains, s.Queries)
	shuffleWorkItems(rng, items)
	results, err := executeWorkItems(ctx, items, concurrency, s.Timeout, onProgress)
	if err != nil && len(results) == 0 {
		return nil, err
	}

	scored := calculateScores(aggregateResults(results))
	if err != nil {
		return scored, err
	}
	return scored, nil
}

func buildWarmupItems(servers []preparedServer, domains []string, repeat int) []workItem {
	if repeat <= 0 || len(domains) == 0 {
		return nil
	}

	items := make([]workItem, 0, len(servers)*repeat)
	for _, server := range servers {
		for i := 0; i < repeat; i++ {
			items = append(items, workItem{
				server: server,
				domain: domains[i%len(domains)],
				warmup: true,
			})
		}
	}
	return items
}

func buildMeasurementItems(servers []preparedServer, domains []string, repeat int) []workItem {
	domainItems := buildWorkItems(domains, repeat)
	items := make([]workItem, 0, len(servers)*len(domainItems))
	for _, server := range servers {
		for _, domain := range domainItems {
			items = append(items, workItem{
				server: server,
				domain: domain,
			})
		}
	}
	return items
}

func shuffleWorkItems(rng *rand.Rand, items []workItem) {
	rng.Shuffle(len(items), func(i, j int) {
		items[i], items[j] = items[j], items[i]
	})
}

func executeWorkItems(ctx context.Context, items []workItem, concurrency int, timeout time.Duration, onProgress func()) ([]QueryResult, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if concurrency <= 0 {
		concurrency = defaultConcurrent
	}

	tasks := make(chan workItem)
	results := make(chan QueryResult, concurrency)

	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		state := newWorkerState(timeout)
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer state.Close()
			for item := range tasks {
				results <- query(ctx, state, item.server, item.domain, timeout, item.warmup)
			}
		}()
	}

	go func() {
		defer close(tasks)
		for _, item := range items {
			select {
			case <-ctx.Done():
				return
			case tasks <- item:
			}
		}
	}()

	go func() {
		workers.Wait()
		close(results)
	}()

	out := make([]QueryResult, 0, len(items))
	for result := range results {
		out = append(out, result)
		if !result.Warmup && onProgress != nil {
			onProgress()
		}
	}

	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func aggregateResults(results []QueryResult) map[string]*serverStats {
	statsMap := make(map[string]*serverStats)
	acceptedFingerprints := buildConsensusFingerprints(results)

	for _, result := range results {
		if result.Warmup {
			continue
		}
		if _, ok := statsMap[result.ServerKey]; !ok {
			statsMap[result.ServerKey] = &serverStats{server: result.Server}
		}
		stats := statsMap[result.ServerKey]
		stats.total++

		if result.Err != nil {
			continue
		}

		stats.rawSuccesses++
		if isAcceptedFingerprint(acceptedFingerprints[result.Domain], result.Fingerprint) {
			stats.successes++
			stats.durations = append(stats.durations, result.Duration)
			continue
		}

		stats.answerMismatches++
	}

	return statsMap
}

func buildConsensusFingerprints(results []QueryResult) map[string]map[string]struct{} {
	fingerprintsByDomain := make(map[string]map[string]map[string]struct{})
	for _, result := range results {
		if result.Warmup || result.Err != nil || result.Fingerprint == "" {
			continue
		}
		if _, ok := fingerprintsByDomain[result.Domain]; !ok {
			fingerprintsByDomain[result.Domain] = make(map[string]map[string]struct{})
		}
		if _, ok := fingerprintsByDomain[result.Domain][result.ServerKey]; !ok {
			fingerprintsByDomain[result.Domain][result.ServerKey] = make(map[string]struct{})
		}
		fingerprintsByDomain[result.Domain][result.ServerKey][result.Fingerprint] = struct{}{}
	}

	acceptedByDomain := make(map[string]map[string]struct{}, len(fingerprintsByDomain))
	for domain, serverFingerprints := range fingerprintsByDomain {
		counts := make(map[string]int)
		for _, fingerprints := range serverFingerprints {
			for fingerprint := range fingerprints {
				counts[fingerprint]++
			}
		}

		hasRepeatedFingerprint := false
		for _, count := range counts {
			if count > 1 {
				hasRepeatedFingerprint = true
				break
			}
		}

		accepted := make(map[string]struct{}, len(counts))
		for fingerprint, count := range counts {
			if !hasRepeatedFingerprint || count > 1 {
				accepted[fingerprint] = struct{}{}
			}
		}
		acceptedByDomain[domain] = accepted
	}

	return acceptedByDomain
}

func isAcceptedFingerprint(accepted map[string]struct{}, fingerprint string) bool {
	if len(accepted) == 0 {
		return true
	}
	_, ok := accepted[fingerprint]
	return ok
}

func calculateScores(statsMap map[string]*serverStats) []BenchmarkResult {
	results := make([]BenchmarkResult, 0, len(statsMap))
	for _, stats := range statsMap {
		res := BenchmarkResult{
			Name:             stats.server.Name,
			Address:          stats.server.Address,
			Protocol:         stats.server.Protocol,
			Successes:        stats.successes,
			Total:            stats.total,
			RawSuccesses:     stats.rawSuccesses,
			AnswerMismatches: stats.answerMismatches,
		}

		if stats.total > 0 {
			res.SuccessRate = float64(stats.successes) / float64(stats.total)
		}

		if len(stats.durations) > 0 {
			sort.Slice(stats.durations, func(i, j int) bool {
				return stats.durations[i] < stats.durations[j]
			})
			res.MedianTime = percentile(stats.durations, 0.5)
			if len(stats.durations) >= minSamplesForP95 {
				res.P95Time = percentile(stats.durations, 0.95)
			}

			medianSeconds := res.MedianTime.Seconds()
			if medianSeconds > 0 {
				jitterPenalty := 1.0
				if res.P95Time > res.MedianTime {
					jitterPenalty = res.MedianTime.Seconds() / res.P95Time.Seconds()
				}
				res.Score = (1.0 / medianSeconds) * math.Pow(res.SuccessRate, 2) * jitterPenalty
			}
		}

		results = append(results, res)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			if results[i].SuccessRate == results[j].SuccessRate {
				return results[i].MedianTime < results[j].MedianTime
			}
			return results[i].SuccessRate > results[j].SuccessRate
		}
		return results[i].Score > results[j].Score
	})

	return results
}

func percentile(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	if len(durations) == 1 {
		return durations[0]
	}
	index := int(math.Ceil(float64(len(durations))*p)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(durations) {
		index = len(durations) - 1
	}
	return durations[index]
}
