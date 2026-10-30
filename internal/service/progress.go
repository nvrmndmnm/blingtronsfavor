package service

import (
	"sync"

	"github.com/itp5b5/blessforge/internal/client"
	"github.com/itp5b5/blessforge/internal/github"
)

// Phase names one step of a unit's progress through a run.
type Phase string

// The phases a unit passes through: a live unit resolves, downloads and
// installs before it ends in exactly one terminal phase — done, failed or
// skipped. Flows report only the phases their work actually has ("bfctl
// adopt" resolves and completes units but never downloads or installs).
const (
	PhaseResolving   Phase = "resolving"
	PhaseDownloading Phase = "downloading"
	PhaseInstalling  Phase = "installing"
	PhaseDone        Phase = "done"
	PhaseFailed      Phase = "failed"
	PhaseSkipped     Phase = "skipped"
)

// Unit identifies the flavor/addon pair one progress event reports about.
// AddonID is the addon's manifest key; before "bfctl adopt" has proposed a
// binding the unit is named by its primary addon directory instead.
type Unit struct {
	Flavor  client.Flavor
	AddonID string
}

// Detail carries the phase-specific context of one event: the version or
// ref being worked on, a human-readable reason (skip reasons, rate-limit
// waits) and the error behind a failure.
type Detail struct {
	Version string
	Ref     string
	Reason  string
	Err     error
}

// ProgressEvent is one renderer-agnostic progress report: the phase one
// unit of work just entered, its context and the unit's slot in the
// workload counter (Index of Total, 1-based). Index names a unit's place
// in the run; flows process one unit at a time, so events arrive in unit
// order.
type ProgressEvent struct {
	Unit   Unit
	Phase  Phase
	Detail Detail
	Index  int // 1-based position of the unit in the workload
	Total  int // number of units in the workload
}

// ProgressFunc receives progress events as the work happens. It may be
// nil: flows treat that as "no reporting" and never special-case it.
//
// Delivery contract: the service layer serializes delivery, so the
// callback is never invoked concurrently and need not be goroutine-safe.
// It is called inline on the goroutine doing the work and should return
// quickly; it must not call back into the running flow.
type ProgressFunc func(ProgressEvent)

// progressSink serializes progress delivery across goroutines (risk R3):
// every emission takes the same lock, so the callback sees one event at a
// time even when a flow reports from a worker goroutine.
type progressSink struct {
	mu sync.Mutex
	fn ProgressFunc
}

func newProgressSink(fn ProgressFunc) *progressSink {
	return &progressSink{fn: fn}
}

// emit delivers one event, dropping it when nothing is listening. The nil
// receiver is a no-op so unconfigured call paths stay branch-free.
func (p *progressSink) emit(ev ProgressEvent) {
	if p == nil || p.fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fn(ev)
}

// progressSlot is one unit's reporting context — identity plus workload
// position — so flow code emits events without rebuilding the frame.
type progressSlot struct {
	p     *progressSink
	unit  Unit
	index int
	total int
}

func (s progressSlot) emit(phase Phase, d Detail) {
	s.p.emit(ProgressEvent{Unit: s.unit, Phase: phase, Detail: d, Index: s.index, Total: s.total})
}

// rateLimited reports a GitHub rate-limit wait as an explicit event in the
// unit's current phase (risk R6) instead of falling silent: the reason
// carries the wait guidance ("rate limited, retry after …" / "quota
// resets at …") and the error stays inspectable. It reports whether err
// was a rate limit at all.
func (s progressSlot) rateLimited(phase Phase, err error) bool {
	hint, ok := github.RateLimitHint(err)
	if !ok {
		return false
	}
	s.emit(phase, Detail{Reason: "rate limited, " + hint, Err: err})
	return true
}
