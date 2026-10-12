// Package slo turns raw success/failure counts into the numbers an SRE actually
// reasons about: error budget, budget remaining, and burn rate — plus a
// multi-window burn-rate alert decision in the style of the Google SRE
// workbook. It is pure arithmetic with no dependencies, so it is trivially
// unit-tested and can back a /slo endpoint, a dashboard, or an alert rule.
package slo

// SLO is an availability objective, e.g. {"api availability", 0.999}.
type SLO struct {
	Name   string
	Target float64 // desired success ratio in (0,1], e.g. 0.999 for "three nines"
}

// ErrorBudget is the fraction of requests that may fail and still meet the SLO.
func (s SLO) ErrorBudget() float64 { return 1 - s.Target }

// Window is a count of requests and how many of them were "bad" (errors, or
// too-slow responses) over some time window.
type Window struct {
	Total uint64
	Bad   uint64
}

// FailureRatio is bad/total (0 when there is no traffic).
func (w Window) FailureRatio() float64 {
	if w.Total == 0 {
		return 0
	}
	return float64(w.Bad) / float64(w.Total)
}

// BudgetRemaining is the fraction of the error budget left for this window:
// 1.0 means untouched, 0.0 means exactly spent, negative means overspent.
func (s SLO) BudgetRemaining(w Window) float64 {
	budget := s.ErrorBudget()
	if budget <= 0 {
		return 0
	}
	allowed := budget * float64(w.Total)
	if allowed == 0 {
		return 1
	}
	return 1 - float64(w.Bad)/allowed
}

// BurnRate is how fast the budget is being consumed relative to the sustainable
// rate. 1.0 means exactly on budget; 14.4 is the classic "burning a month's
// budget in ~2 hours" threshold.
func (s SLO) BurnRate(w Window) float64 {
	budget := s.ErrorBudget()
	if budget <= 0 {
		return 0
	}
	return w.FailureRatio() / budget
}

// Severity is the action a burn-rate alert recommends.
type Severity int

const (
	// SeverityNone: budget burn is within normal bounds.
	SeverityNone Severity = iota
	// SeverityTicket: sustained slow burn — open a ticket, investigate soon.
	SeverityTicket
	// SeverityPage: fast burn — page on-call now.
	SeverityPage
)

func (s Severity) String() string {
	switch s {
	case SeverityPage:
		return "page"
	case SeverityTicket:
		return "ticket"
	default:
		return "none"
	}
}

// Standard multi-window burn-rate thresholds (Google SRE workbook).
const (
	PageBurnRate   = 14.4 // ~2% of a 30d budget in 1h
	TicketBurnRate = 1.0
)

// AlertSeverity decides the alert level from a short ("fast") and long ("slow")
// window. Requiring both windows to exceed the threshold is what suppresses
// flapping: the long window proves the problem is sustained, the short window
// proves it is still happening right now.
func (s SLO) AlertSeverity(fast, slow Window) Severity {
	fb, sb := s.BurnRate(fast), s.BurnRate(slow)
	if fb >= PageBurnRate && sb >= PageBurnRate {
		return SeverityPage
	}
	if fb >= TicketBurnRate && sb >= TicketBurnRate {
		return SeverityTicket
	}
	return SeverityNone
}
