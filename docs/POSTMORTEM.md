# Postmortem — tick-latency SLO breach (incident drill)

> Blameless postmortem. This one documents the deliberate chaos drill from
> [RUNBOOK.md](RUNBOOK.md#incident-drill-practise-the-above-safely); the same
> template is what a real incident would use.

- **Status:** resolved
- **Severity:** SEV-2 (SLO fast burn, no data loss)
- **Duration:** ~30 min (drill)
- **Authors:** on-call
- **SLO affected:** Tick latency < 50 ms @ 99.9% (see [SLO.md](SLO.md))

## Impact

For the duration, ~100% of simulation ticks exceeded the 50 ms latency budget
(normal is <1 ms). Players experienced jerky movement and delayed input
feedback. The tick-latency error budget burned at >1000× the sustainable rate;
`ArenaTickLatencyFastBurn` paged within 2 minutes.

## Timeline (UTC)

| Time | Event |
|------|-------|
| T+0  | Server started with an 80 ms-per-tick fault injected (`-chaos-tick-ms 80`). |
| T+1m | `loadtest` drove 50 clients; `arena_slow_ticks_total` began climbing. |
| T+2m | `ArenaTickLatencyFastBurn` paged; `/slo` showed `burnRate` ≈ 1600, `budgetRemaining` < 0. |
| T+5m | On-call confirmed every tick > 50 ms via `arena_tick_duration_seconds`; load was nominal, so cause was per-tick cost, not volume. |
| T+8m | Process found to be running with `-chaos-tick-ms`; restarted without it. |
| T+9m | `burnRate` returned to 0; alert resolved. |

## Root cause

Artificial per-tick latency (80 ms) was injected via the `-chaos-tick-ms` flag,
pushing every tick past the 50 ms budget. Because the fault was per-tick and not
load-dependent, throughput looked normal while latency was uniformly bad — a
useful reminder that **throughput SLIs alone hide latency regressions**.

## What went well

- The dual-window burn-rate alert paged quickly and did not flap.
- `/metrics` + `/slo` made it obvious within minutes that this was a per-tick
  latency problem, not a load problem.

## What to improve (action items)

- [ ] Add a startup guard/log banner when `-chaos-tick-ms` is set (done: the
      server now logs `CHAOS MODE` at boot).
- [ ] Add a `histogram_quantile` panel for tick p99 to the default dashboard.
- [ ] Add a CI check that the production image never sets a chaos flag.

## Lessons

Measure latency as an SLI, not just throughput. A single uniform-latency fault
was invisible to request-rate graphs but immediately visible to the latency
histogram and the SLO burn rate.
