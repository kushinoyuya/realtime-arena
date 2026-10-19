# Service Level Objectives

SLIs/SLOs for the realtime-arena server. The arithmetic behind these lives,
unit-tested, in [`internal/slo`](../internal/slo); the live numbers are at
`/slo` and the raw signals at `/metrics`.

## SLIs (what we measure)

| SLI | Definition | Source metric |
|-----|------------|---------------|
| Tick latency | fraction of simulation ticks completing within 50 ms | `arena_slow_ticks_total` / `arena_ticks_total` |
| Availability | fraction of successful WebSocket upgrades | `arena_disconnects_total`, upgrade failures (log) |
| Snapshot delivery | snapshots fanned out per second | `arena_snapshots_sent_total` |

## SLOs (the targets)

| Objective | Target | Window | Error budget |
|-----------|--------|--------|--------------|
| **Tick latency < 50 ms** | 99.9% | 30 days | 0.1% of ticks |
| WebSocket availability | 99.9% | 30 days | 0.1% of connections |

At 10 ticks/s a 30-day budget is ~25,900 "slow" ticks. Spending it faster than
that is a *burn*.

## Error-budget policy

- **Budget remaining > 0:** ship features normally.
- **Budget exhausted (remaining ≤ 0):** feature freeze; reliability work only
  until the 30-day window recovers.
- **Fast burn (≥ 14.4× over 1h and 5m):** page on-call — see
  [RUNBOOK](RUNBOOK.md#tick-latency-slo-burn).
- **Slow burn (≥ 1× sustained over 6h and 30m):** open a ticket.

The burn-rate thresholds (14.4× page, 1× ticket) and the dual-window
confirmation come straight from the Google SRE workbook and are implemented in
`slo.SLO.AlertSeverity`; the Prometheus rules are in
[`deploy/alerts.yml`](../deploy/alerts.yml).
