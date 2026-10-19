# Runbook

Operational guide for the realtime-arena server.

## Endpoints

| Path | Purpose |
|------|---------|
| `/` | game client |
| `/ws` | player WebSocket |
| `/healthz` | liveness probe (always `ok` while serving) |
| `/stats` | rooms / players (JSON) |
| `/metrics` | Prometheus exposition |
| `/slo` | live tick-latency SLO: budget remaining + burn rate (JSON) |

## Run it

```bash
go run ./cmd/server                 # local
docker compose up --build           # with Prometheus on :9090
cd deploy/terraform && terraform apply   # as a managed container
```

## Dashboards / queries

```promql
# snapshot throughput
rate(arena_snapshots_sent_total[1m])
# tick p99 (approx, from the histogram)
histogram_quantile(0.99, rate(arena_tick_duration_seconds_bucket[5m]))
# live error-budget burn for the tick-latency SLO
(rate(arena_slow_ticks_total[5m]) / rate(arena_ticks_total[5m])) / 0.001
```

## Alerts

### Tick latency SLO burn

**Symptom:** `ArenaTickLatencyFastBurn` (page) or `...SlowBurn` (ticket) firing;
`/slo` shows `burnRate` > 1 and `budgetRemaining` falling.

**Triage**
1. Check `rate(arena_slow_ticks_total[5m])` — are ticks actually exceeding 50 ms?
2. Check `arena_active_players` and `arena_ws_connections` — load spike?
3. Look at `arena_tick_duration_seconds` histogram for where time is going.

**Likely causes & mitigation**
- **Load beyond capacity:** players per room too high → lower room `-cap`, or
  scale out (more replicas behind the hub). Rooms are independent, so horizontal
  scaling is linear.
- **A slow/stuck room goroutine:** restart the instance (rooms are in-memory and
  rebuild as clients reconnect via their tokens).
- **Chaos drill left on:** confirm the process was not started with
  `-chaos-tick-ms` (see below).

**Verify recovery:** `/slo` `burnRate` back under 1; alert resolves.

## Incident drill (practise the above safely)

Inject latency, watch the SLO burn, then mitigate:

```bash
# 1. start the server in "degraded" mode (80 ms per tick >> 50 ms budget)
go run ./cmd/server -chaos-tick-ms 80
# 2. generate traffic
go run ./cmd/loadtest -n 50 -dur 30s
# 3. observe the burn
watch -n1 'curl -s localhost:8080/slo'
#    -> slowTicks climbs, budgetRemaining drops, burnRate >> 14.4
# 4. "mitigate": restart without chaos
go run ./cmd/server
#    -> burnRate returns to 0
```

Write up the result as a blameless postmortem — see
[POSTMORTEM.md](POSTMORTEM.md) for the template filled in from this exact drill.
