// Command loadtest drives the arena server with many concurrent bot clients to
// measure how it holds up: connection success, snapshot throughput, and
// end-to-end latency (the gap between the server stamping a snapshot and the
// bot receiving it). It is a scaled-down version of the load testing a game
// backend team does before launch.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/kushino/realtime-arena/internal/ws"
)

type botResult struct {
	connected bool
	snapshots int
	latencies []float64 // milliseconds
}

func main() {
	url := flag.String("url", "ws://localhost:8080/ws", "server websocket URL")
	n := flag.Int("n", 200, "number of concurrent bot clients")
	durFlag := flag.Duration("dur", 10*time.Second, "test duration")
	rate := flag.Int("rate", 5, "direction inputs per second per bot")
	flag.Parse()

	fmt.Printf("load test: %d bots -> %s for %s (%d inputs/s each)\n", *n, *url, *durFlag, *rate)

	results := make(chan botResult, *n)
	var wg sync.WaitGroup
	start := time.Now()
	deadline := start.Add(*durFlag)

	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			results <- runBot(id, *url, deadline, *rate)
		}(i)
		// Stagger connections slightly so we don't open all sockets in the
		// same microsecond (which measures the OS, not the server).
		time.Sleep(time.Millisecond)
	}

	wg.Wait()
	close(results)

	report(collect(results), time.Since(start), *n)
}

func runBot(id int, url string, deadline time.Time, rate int) botResult {
	conn, err := ws.Dial(url)
	if err != nil {
		return botResult{}
	}
	defer conn.Close()

	join := fmt.Sprintf(`{"type":"join","name":"bot-%d"}`, id)
	if err := conn.WriteMessage([]byte(join)); err != nil {
		return botResult{}
	}
	_ = conn.SetReadDeadline(deadline)

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second / time.Duration(rate))
		defer t.Stop()
		r := rand.New(rand.NewSource(int64(id) + 1))
		dirs := []string{"up", "down", "left", "right"}
		for {
			select {
			case <-done:
				return
			case <-t.C:
				msg := fmt.Sprintf(`{"type":"input","dir":%q}`, dirs[r.Intn(4)])
				if err := conn.WriteMessage([]byte(msg)); err != nil {
					return
				}
			}
		}
	}()

	res := botResult{connected: true}
	for {
		raw, err := conn.ReadMessage()
		if err != nil {
			break // deadline reached or peer closed
		}
		var m struct {
			Type         string `json:"type"`
			ServerTimeMs int64  `json:"serverTimeMs"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Type != "snapshot" {
			continue
		}
		res.snapshots++
		nowMs := time.Now().UnixNano() / int64(time.Millisecond)
		res.latencies = append(res.latencies, float64(nowMs-m.ServerTimeMs))
	}
	close(done)
	return res
}

type agg struct {
	connected int
	failed    int
	snapshots int
	latencies []float64
}

func collect(results <-chan botResult) agg {
	var a agg
	for r := range results {
		if r.connected {
			a.connected++
		} else {
			a.failed++
		}
		a.snapshots += r.snapshots
		a.latencies = append(a.latencies, r.latencies...)
	}
	return a
}

func report(a agg, elapsed time.Duration, want int) {
	fmt.Println("\n--- results ---")
	fmt.Printf("connected:        %d / %d\n", a.connected, want)
	fmt.Printf("failed:           %d\n", a.failed)
	fmt.Printf("elapsed:          %.2fs\n", elapsed.Seconds())
	fmt.Printf("snapshots recv:   %d\n", a.snapshots)
	if elapsed.Seconds() > 0 {
		fmt.Printf("snapshot rate:    %.0f msg/s total\n", float64(a.snapshots)/elapsed.Seconds())
	}
	if a.connected > 0 {
		fmt.Printf("per-bot rate:     %.1f msg/s\n",
			float64(a.snapshots)/elapsed.Seconds()/float64(a.connected))
	}

	if len(a.latencies) == 0 {
		fmt.Println("latency:          (no snapshots received)")
		if a.failed > 0 {
			os.Exit(1)
		}
		return
	}
	sort.Float64s(a.latencies)
	fmt.Printf("latency ms:       p50=%.1f  p90=%.1f  p99=%.1f  max=%.1f  mean=%.1f\n",
		pct(a.latencies, 0.50), pct(a.latencies, 0.90), pct(a.latencies, 0.99),
		a.latencies[len(a.latencies)-1], mean(a.latencies))

	if a.failed > 0 {
		os.Exit(1) // non-zero exit so CI / scripts can detect connection loss
	}
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
