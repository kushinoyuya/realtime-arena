// Command server runs the authoritative Snake-arena game server: it serves the
// static browser client, upgrades /ws connections, ticks each match room, and
// exposes a tiny /stats endpoint. Everything runs locally with no external
// dependencies.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/kushino/realtime-arena/internal/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	webDir := flag.String("web", "web", "directory of static client files")
	capacity := flag.Int("cap", 8, "max players per room")
	tickRate := flag.Int("tick", 10, "simulation ticks per second")
	width := flag.Int("w", 40, "arena width in cells")
	height := flag.Int("h", 30, "arena height in cells")
	flag.Parse()

	hub := server.NewHub(server.HubConfig{
		Capacity: *capacity,
		Width:    *width,
		Height:   *height,
		TickRate: *tickRate,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.ServeWS)
	mux.HandleFunc("/stats", hub.ServeStats)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", http.FileServer(http.Dir(*webDir)))

	log.Printf("realtime-arena server on %s  (tick=%d/s, room cap=%d, arena=%dx%d)",
		*addr, *tickRate, *capacity, *width, *height)
	log.Printf("open http://localhost%s in a browser (or several tabs)", normalizeAddr(*addr))
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

// normalizeAddr turns ":8080" into "localhost:8080" for the convenience log line.
func normalizeAddr(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return addr
	}
	return addr
}
