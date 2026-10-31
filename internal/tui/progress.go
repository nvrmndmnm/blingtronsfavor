package tui

import (
	"sort"
	"sync"

	"github.com/itp5b5/blessforge/internal/service"
)

// liveRow is one addon's live status inside a running sync: the phase its
// unit is working through, the detail that phase carries (the rate-limit
// wait reason while one lasts) and — once the outcome is decided — the
// result itself. Rows are keyed by addon id, the table's key, so every
// event lands in the same row and rows never move (risk R2).
type liveRow struct {
	phase  service.Phase
	detail service.Detail
	result *service.Result // set once the outcome is decided
}

// liveState is one tick's snapshot of a running sync: the per-addon live
// rows, the counters the status bar frames ("syncing 2/4 · 1 done · 1
// failed") and the rate-limit wait reason to surface while one lasts.
type liveState struct {
	rows    map[string]liveRow
	total   int
	done    int
	failed  int
	skipped int
	wait    string
}

// syncRun is the live state of one running sync. The service layer's
// ProgressEvent stream and its result callback write it as the work
// happens; only the spinner tick samples it into the model. Nothing else
// touches the model mid-run, so redraws are rate-limited to the spinner
// tick (risk R3): a burst of events can never flicker or spew, and every
// row's live phase stays a pure function of the sampled state and the
// terminal size (risk R1).
type syncRun struct {
	mu      sync.Mutex
	rows    map[string]*liveRow
	total   int
	done    int
	failed  int
	skipped int
}

// newSyncRun starts an empty run state.
func newSyncRun() *syncRun {
	return &syncRun{rows: map[string]*liveRow{}}
}

// record folds one progress event into the run state — the model's
// ProgressFunc. The service layer serializes delivery (see
// service.ProgressFunc); the lock keeps result callbacks out regardless.
func (r *syncRun) record(ev service.ProgressEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.rows[ev.Unit.AddonID]
	if row == nil {
		row = &liveRow{}
		r.rows[ev.Unit.AddonID] = row
	}
	row.phase = ev.Phase
	row.detail = ev.Detail
	if ev.Total > 0 {
		r.total = ev.Total
	}
	switch ev.Phase {
	case service.PhaseDone:
		r.done++
	case service.PhaseFailed:
		r.failed++
	case service.PhaseSkipped:
		r.skipped++
	}
}

// recordResult folds one decided outcome into the run state — the model's
// result callback. The row flips to its outcome the moment the report
// order publishes it, in place, in the CLI live block's words.
func (r *syncRun) recordResult(res service.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.rows[res.AddonID]
	if row == nil {
		row = &liveRow{}
		r.rows[res.AddonID] = row
	}
	cp := res
	row.result = &cp
}

// sample copies the run state for the model to render: the rows as they
// stand, the run counters, and one deterministic rate-limit wait reason
// while several units wait at once.
func (r *syncRun) sample() liveState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := liveState{
		rows:    make(map[string]liveRow, len(r.rows)),
		total:   r.total,
		done:    r.done,
		failed:  r.failed,
		skipped: r.skipped,
	}
	ids := make([]string, 0, len(r.rows))
	for id, row := range r.rows {
		s.rows[id] = *row
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if row := s.rows[id]; livePhase(row.phase) && row.detail.Reason != "" {
			s.wait = row.detail.Reason
			break
		}
	}
	return s
}

// livePhase reports whether a phase is still in flight.
func livePhase(p service.Phase) bool {
	switch p {
	case service.PhaseResolving, service.PhaseDownloading, service.PhaseInstalling:
		return true
	}
	return false
}
