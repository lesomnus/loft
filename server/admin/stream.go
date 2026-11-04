package admin

import (
	"context"
	"fmt"
	"time"
)

// Heartbeat is how often a long call says it is still at it when the work
// says nothing: often enough that nothing between the operator and the entry
// point -- a `kubectl port-forward`, a tunnel -- takes the connection for an
// idle one.
var Heartbeat = 15 * time.Second

// progress is what a long operation says of itself as it goes. A word it says
// while the one before is still waiting to be sent is dropped rather than
// waited for: the work does not wait on the wire.
type progress chan string

func (p progress) say(format string, args ...any) {
	select {
	case p <- fmt.Sprintf(format, args...):
	default:
	}
}

// working runs `f` and, while it does, hands `say` what it says and, every
// [Heartbeat] it says nothing, that it is still at it. `say` is called from
// this goroutine alone, which is what a stream's Send asks.
//
// `f` runs on the call's context: a call that is gone ends it at its next
// step, and so does one `say` could not answer. Every long operation the
// entry point runs is one to run again after that (design 9.15).
func working(ctx context.Context, what string, say func(string) error, f func(ctx context.Context, p progress) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p := make(progress, 16)
	done := make(chan error, 1)
	go func() { done <- f(ctx, p) }()

	began := time.Now()
	tick := time.NewTicker(Heartbeat)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case v := <-p:
			if err := say(v); err != nil {
				cancel()
				<-done
				return err
			}
		case <-tick.C:
			if err := say(fmt.Sprintf("%s: %s", what, time.Since(began).Round(time.Second))); err != nil {
				cancel()
				<-done
				return err
			}
		}
	}
}
