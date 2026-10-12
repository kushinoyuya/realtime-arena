# Realtime Arena

A small **authoritative multiplayer game server** — a browser Snake arena where
all game logic runs on the server and clients only send input. Written in Go
with **zero external dependencies** (the WebSocket layer is implemented from the
standard library) and a build-free browser client, so the whole thing runs
locally with `go run`.

The point of the project is not the game. It is to demonstrate, end to end, the
things a real-time game *backend* is made of:

- an **authoritative server** that is the single source of truth,
- a **deterministic, fully-tested simulation** (the basis for replays and
  anti-cheat),
- **matchmaking**, **reconnection**, and a fixed-rate **tick loop**,
- a **load-testing harness** that measures how it behaves under many clients,
- correct **concurrency** (each match is owned by one goroutine; no locks on
  game state), and
- a hand-written **WebSocket** implementation, to show the protocol is
  understood rather than imported.

```
┌──────────┐  input (JSON)   ┌───────────────────────────────┐
│ browser  │ ───────────────▶│ Room goroutine (authoritative)│
│ client   │                 │  tick loop @ N Hz             │
│ (canvas) │ ◀───────────────│  world.Step(inputs) -- pure   │
└──────────┘  snapshots      │  broadcast snapshot           │
                             └───────────────────────────────┘
       many bots ──▶ cmd/loadtest ──▶ p50/p99 latency, throughput
```

## Quick start

Requires Go (1.13+). No `npm`, no modules to download.

```bash
# 1. run the server
go run ./cmd/server
# open http://localhost:8080 in two or more browser tabs

# 2. (optional) hammer it with 200 bot clients for 10s
go run ./cmd/loadtest -n 200 -dur 10s

# tests
go test ./...
# with the race detector (needs a C toolchain)
go test -race ./...
```

Move with the arrow keys or `WASD`. Eat food to grow; hitting a wall, another
snake, or yourself ends your run. When everyone has died the round restarts
automatically.

## Why it is built this way

**Authoritative server.** The client never computes positions — it sends only
intent (`join`, `input`). The server validates every input (you cannot reverse
into your own neck, you cannot move another player) and answers with snapshots.
A cheating or buggy client therefore cannot move anyone; it can only *request* a
move the server may reject. This is the core property every competitive game
backend needs.

**Deterministic simulation.** The whole world advances through one pure
function, `game.World.Step(Input)`. It uses a seeded xorshift PRNG (never
`math/rand`'s global state) and always iterates players in sorted order, so a
match is a pure function of `(seed, []Input)`. That is what makes
`internal/game/replay.go` trivially correct: re-feeding the recorded inputs
reproduces the match bit-for-bit, which `TestReplayReproducesMatch` asserts via
a state fingerprint. The same property is what real systems lean on for replays,
spectating, and server-side verification of client-reported results.

**Concurrency by message passing.** Each `Room` is a single goroutine that
exclusively owns its world state; clients communicate with it only over
channels (`internal/server/room.go`). There are no mutexes around the game
state and nothing to race. Slow clients are handled by dropping snapshots for
them, never by blocking the room.

**Hand-written WebSocket.** `internal/ws` implements the RFC 6455 handshake,
masked/unmasked framing, fragmentation reassembly, and ping/pong/close, for both
server (`Upgrade`) and client (`Dial`) roles — enough for this app, and no more.
Keeping it in-tree is why the project has no third-party dependencies.

**Load testing.** `cmd/loadtest` dials many bot clients, drives random input,
and reports connection success, snapshot throughput, and end-to-end latency
(p50/p90/p99) measured from a server timestamp embedded in each snapshot — a
scaled-down version of the pre-launch load testing a game-services team does.

## Layout

```
cmd/
  server/     HTTP + WebSocket server; serves the client, ticks the rooms
  loadtest/   concurrent bot-client load generator
internal/
  game/       pure deterministic simulation + replay (no I/O)  ← most tests
  protocol/   JSON messages shared by client and server
  ws/         standard-library-only WebSocket (server Upgrade + client Dial)
  server/     hub (matchmaking), room (tick loop), client (connection pumps)
web/          build-free browser client (HTML + ES-module JS + canvas)
```

## Tests

| Area | What is covered |
|------|-----------------|
| `internal/game` | determinism, replay reproduction, wall/self/head-to-head collisions, growth, input validation, food refill |
| `internal/ws`   | RFC 6455 accept-key vector, frame round-trip, extended length, fragmentation, ping→pong, close |
| `internal/server` | tick loop & broadcast, input application, reconnection within grace, matchmaking capacity/spill |

## Possible next steps

Client-side interpolation/prediction with server reconciliation; snapshot delta
compression; a persisted leaderboard; a `/metrics` endpoint in Prometheus
format; containerisation. These are deliberately left out to keep the project
dependency-free and locally runnable.

## License

MIT — see [LICENSE](LICENSE).
