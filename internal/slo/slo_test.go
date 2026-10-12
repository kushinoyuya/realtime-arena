package slo

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestErrorBudgetAndRemaining(t *testing.T) {
	s := SLO{Name: "availability", Target: 0.999}
	if !approx(s.ErrorBudget(), 0.001) {
		t.Fatalf("error budget = %v, want 0.001", s.ErrorBudget())
	}
	// 100000 requests, budget allows 100 failures. 50 bad => half spent.
	w := Window{Total: 100000, Bad: 50}
	if !approx(s.BudgetRemaining(w), 0.5) {
		t.Fatalf("remaining = %v, want 0.5", s.BudgetRemaining(w))
	}
	// Exactly on budget.
	if !approx(s.BudgetRemaining(Window{Total: 100000, Bad: 100}), 0.0) {
		t.Fatalf("remaining should be 0 at budget")
	}
	// Overspent => negative.
	if s.BudgetRemaining(Window{Total: 100000, Bad: 200}) >= 0 {
		t.Fatal("remaining should be negative when overspent")
	}
}

func TestBurnRate(t *testing.T) {
	s := SLO{Target: 0.999} // budget 0.001
	// failure ratio 0.001 => burn rate 1.0 (exactly on budget).
	if got := s.BurnRate(Window{Total: 10000, Bad: 10}); !approx(got, 1.0) {
		t.Fatalf("burn rate = %v, want 1.0", got)
	}
	// failure ratio 0.0144 => burn rate 14.4.
	if got := s.BurnRate(Window{Total: 10000, Bad: 144}); !approx(got, 14.4) {
		t.Fatalf("burn rate = %v, want 14.4", got)
	}
	// no traffic => no burn.
	if got := s.BurnRate(Window{}); got != 0 {
		t.Fatalf("burn rate with no traffic = %v, want 0", got)
	}
}

func TestAlertSeverity(t *testing.T) {
	s := SLO{Target: 0.999}

	healthy := Window{Total: 100000, Bad: 50} // burn 0.5
	if got := s.AlertSeverity(healthy, healthy); got != SeverityNone {
		t.Fatalf("healthy => %v, want none", got)
	}

	// Sustained burn ~2x over both windows => ticket, not page.
	slowBurn := Window{Total: 100000, Bad: 200} // burn 2.0
	if got := s.AlertSeverity(slowBurn, slowBurn); got != SeverityTicket {
		t.Fatalf("2x burn => %v, want ticket", got)
	}

	// Fast burn over both windows => page.
	fastBurn := Window{Total: 10000, Bad: 200} // ratio .02 => burn 20
	if got := s.AlertSeverity(fastBurn, fastBurn); got != SeverityPage {
		t.Fatalf("20x burn => %v, want page", got)
	}

	// Fast short spike but long window calm => suppressed (no page).
	if got := s.AlertSeverity(fastBurn, healthy); got == SeverityPage {
		t.Fatal("short-only spike should not page")
	}
}

func TestSeverityString(t *testing.T) {
	if SeverityPage.String() != "page" || SeverityTicket.String() != "ticket" || SeverityNone.String() != "none" {
		t.Fatal("severity strings wrong")
	}
}
