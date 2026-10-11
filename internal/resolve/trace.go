package resolve

import (
	"fmt"
	"os"
	"time"
)

// traceEnv enables per-phase timing of incremental resolution on stderr
// (GRAPHMASTER_TRACE_RESOLVE=1); a debugging aid, unset in normal use.
const traceEnv = "GRAPHMASTER_TRACE_RESOLVE"

// phaseTracer prints, for each phase of a run, its duration and the
// statistics it added.
type phaseTracer struct {
	on    bool
	start time.Time
	prev  Stats
}

func newPhaseTracer() *phaseTracer {
	return &phaseTracer{on: os.Getenv(traceEnv) != "", start: time.Now()}
}

func (t *phaseTracer) phase(name string, st *Stats) {
	if !t.on {
		return
	}
	now := time.Now()
	fmt.Fprintf(os.Stderr, "resolve: %-18s %8s names=%d files=%d refs=%d deps=%d depsSkipped=%d batches=%d edges=%d\n",
		name, now.Sub(t.start).Round(time.Millisecond), st.Names-t.prev.Names, st.Files-t.prev.Files,
		st.Refs-t.prev.Refs, st.Dependents-t.prev.Dependents, st.DependentsSkipped-t.prev.DependentsSkipped,
		st.Batches-t.prev.Batches, st.Edges-t.prev.Edges)
	t.start, t.prev = now, *st
}
