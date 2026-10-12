package cmd_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/payday/spin"
)

// TestEveryLoopIsOneTheProcessesAgreeOn: what the server hands spin.Run is
// loops and nothing else -- something that is no spin.Spinner is skipped
// without a word, which is how the roster in this process's housekeeping went
// unrun -- and under a lease every one of them has a name of its own, without
// which spin.Run refuses to start (payday#54).
func TestEveryLoopIsOneTheProcessesAgreeOn(t *testing.T) {
	w := newWorld(t)
	x := w.x

	x.NotEmpty(w.s.Spin)
	for i, v := range w.s.Spin {
		_, ok := v.(spin.Spinner)
		x.True(ok, "s.Spin[%d] is a %T, which spin.Run skips", i, v)
	}

	// Under a lease nothing is held by, so that no loop runs here and what is
	// asked is only whether the processes could agree on them.
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	x.NoError(spin.Run(ctx, slices.Values(w.s.Spin), spin.WithLease(spin.Nowhere())))
}
