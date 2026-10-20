// Command server runs the authoritative Snake-arena game server: it serves the
// static browser client, upgrades /ws connections, ticks each match room, and
// exposes a tiny /stats endpoint. Everything runs locally with no external
// dependencies.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/kushino/realtime-arena/internal/replay"
	"github.com/kushino/realtime-arena/internal/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	webDir := flag.String("web", "web", "directory of static client files")
	capacity := flag.Int("cap", 8, "max players per room")
	tickRate := flag.Int("tick", 10, "simulation ticks per second")
	width := flag.Int("w", 40, "arena width in cells")
	height := flag.Int("h", 30, "arena height in cells")
	chaosTickMs := flag.Int("chaos-tick-ms", 0, "inject N ms of latency per tick (incident drill; 0 = off)")
	replayDir := flag.String("replay-dir", "replays", "directory for recorded matches (\"\" = memory only)")
	replayMax := flag.Int("replay-max", 50, "number of recorded matches to keep")
	origins := flag.String("origins", "", "comma-separated extra origins allowed to open /ws (same-origin is always allowed)")
	flag.Parse()

	replays, err := replay.NewStore(*replayDir, *replayMax)
	if err != nil {
		log.Fatalf("replay store: %v", err)
	}

	hub := server.NewHub(server.HubConfig{
		Capacity:    *capacity,
		Width:       *width,
		Height:      *height,
		TickRate:    *tickRate,
		ChaosTickMs: *chaosTickMs,
		Replays:     replays,

		AllowedOrigins: splitList(*origins),
	})
	if *chaosTickMs > 0 {
		log.Printf("CHAOS MODE: injecting %dms latency per tick", *chaosTickMs)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.ServeWS)
	mux.HandleFunc("/stats", hub.ServeStats)
	mux.HandleFunc("/metrics", hub.ServeMetrics)  // Prometheus exposition
	mux.HandleFunc("/slo", hub.ServeSLO)          // live tick-latency SLO
	mux.Handle("/api/replays", replays.Handler()) // recorded matches
	mux.Handle("/api/replays/", replays.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", http.FileServer(http.Dir(*webDir)))

	log.Printf("realtime-arena server on %s  (tick=%d/s, room cap=%d, arena=%dx%d)",
		*addr, *tickRate, *capacity, *width, *height)
	log.Printf("open %s in a browser (or several tabs)", browserURL(*addr))
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

// browserURL turns a listen address into a URL to open locally: ":8080" and
// "0.0.0.0:8080" become "http://localhost:8080".
func browserURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// splitList parses a comma-separated flag value, ignoring blanks.
func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
