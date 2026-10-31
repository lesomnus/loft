package admin

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/loft/cmd"
)

// TestACertificateIsAskedForAtARate: the ask says yes to each host as often
// as its own rate allows, and to all hosts together as often as theirs does
// (`admin.ask`). A yes one of them refuses spends neither, and each comes back
// as time passes.
func TestACertificateIsAskedForAtARate(t *testing.T) {
	x := require.New(t)
	// One a host an hour, and two for all of them, then one a minute.
	i := NewIssuance(cmd.AskConfig{HostBurst: 1, HostEvery: time.Hour, Burst: 2, Every: time.Minute})
	now := time.Unix(1_800_000_000, 0)

	x.True(i.allow("a.example", now))
	x.False(i.allow("a.example", now), "a's own rate")
	x.True(i.allow("b.example", now))
	x.False(i.allow("c.example", now), "every host's rate, which a and b spent")

	// A minute on, one more for all of them -- and c's own, which every
	// host's refusing did not spend, is there to take it.
	now = now.Add(time.Minute)
	x.True(i.allow("c.example", now), "c's own was not spent when every host's refused it")
	now = now.Add(time.Minute)
	x.False(i.allow("b.example", now), "b's own, an hour away")
	x.True(i.allow("d.example", now), "and every host's yes there for another, which b's refusal did not spend")

	// The defaults: ten at once for a host, three hundred for all.
	d := NewIssuance(cmd.AskConfig{})
	for n := range 10 {
		x.True(d.allow("d.example", now), "the %d. at once", n+1)
	}
	x.False(d.allow("d.example", now))
	for n := range 290 {
		x.True(d.allow(fmt.Sprintf("h%d.example", n), now), "the %d. host at once", n+1)
	}
	x.False(d.allow("last.example", now), "the 301st")
}
