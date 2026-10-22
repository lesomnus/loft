package cli

// The witnesses `audit.checkpoints.witnesses` may name: where the newest
// checkpoint of the trail's chain is heard outside the database and the
// archive (payday's `trail.Witness`, payday#115), so that whoever can write
// both cannot cut the chain short at its head and wind the count back without
// something they do not hold saying so. `docs/runbook/trail-integrity.md` is
// what each finding of theirs means and what to do.
//
// Blank-imported here, beside the drivers and for their reason: a process
// hears checkpoints, and the page, which imports `cmd` and not this, has no
// archive to hear. Both, because a configuration naming a scheme this binary
// does not open is refused where the process comes up. roster's `cli`, which
// the roster in this process is migrated with, links the same two today; this
// says what rove's own configuration may name rather than leaning on that.
//
// `log:` writes a line of the log and the gauge `payday.trail.checkpoint` for
// every checkpoint, and is a witness only with the alerts on it the runbook
// gives. `store:` and a directory keeps what it hears on a disk of its own,
// mounted with credentials neither the database's nor the archive's, best one
// that refuses deletion. payday's third, a transparency log (`rekor:`), is not
// linked: nothing here names one.
import (
	_ "github.com/lesomnus/payday/config/witnesslog"
	_ "github.com/lesomnus/payday/config/witnessstore"
)
