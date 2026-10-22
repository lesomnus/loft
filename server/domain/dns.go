package domain

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"time"
)

// heard is what DNS said of a domain: that it is here, that it is not, or
// nothing either way.
type heard int

const (
	// heardNothing is no answer: a timeout, a server that failed, a
	// deployment that cannot read DNS. It says nothing of the domain.
	heardNothing heard = iota
	heardNo
	heardYes
)

// The recheck of an active custom domain's DNS (rove#155).
//
// Its TXT record and where it points are asked about every [RecheckEvery],
// and a domain lapses once [RecheckFailures] answers in a row, over at least
// [RecheckSpan], said no. An answer that says nothing -- a timeout, a server
// failure, which is what an outage at a DNS host looks like -- is not
// counted, and one that says yes starts the count again.
//
// Why six an hour apart, which is five hours of the same no: a record a DNS
// host's migration dropped and put back, or a name cached as missing for a
// moment, is gone for about one TTL, and the TTLs DNS hosts give records and
// missing names by default are an hour to a few; six in a row outlast that,
// so a blip comes back before the count does. And a domain that somebody else
// now points stops being handed sign-ins the same working day, which is how
// long a code on its way there can be read by whoever holds it.
const (
	RecheckEvery    = time.Hour
	RecheckFailures = 6
	RecheckSpan     = (RecheckFailures - 1) * RecheckEvery
)

// told is what DNS said, read off an error: a name or a record it says is not
// there is a no, and anything else is no answer at all.
func told(err error) heard {
	var e *net.DNSError
	if errors.As(err, &e) && e.IsNotFound {
		return heardNo
	}
	return heardNothing
}

// txt answers whether `host`'s challenge record holds `token`.
func (d *Deps) txt(ctx context.Context, host, token string) heard {
	if d.LookupTXT == nil {
		return heardNothing
	}
	vs, err := d.LookupTXT(ctx, "_rove-challenge."+host)
	if err != nil {
		return told(err)
	}
	if slices.Contains(vs, token) {
		return heardYes
	}
	return heardNo
}

// pointsHere answers whether `host` reaches this deployment: its CNAME
// chain ends at `app.labels.target`, or it has an address the target has --
// an apex flattened by its DNS host, an ALIAS record. A deployment that names
// no target asks nothing, and is told yes.
func (d *Deps) pointsHere(ctx context.Context, host string) heard {
	target := dnsName(d.LabelTarget)
	if target == "" {
		return heardYes
	}
	if d.LookupCNAME != nil {
		if v, err := d.LookupCNAME(ctx, host); err == nil && dnsName(v) == target {
			return heardYes
		}
	}
	if d.LookupHost == nil {
		return heardNothing
	}
	at, err := d.LookupHost(ctx, host)
	if err != nil {
		return told(err)
	}
	// What the target is, asked here: one that cannot be told says nothing
	// of the domain.
	here, err := d.LookupHost(ctx, target)
	if err != nil {
		return heardNothing
	}
	for _, a := range at {
		if slices.Contains(here, a) {
			return heardYes
		}
	}
	return heardNo
}

// isHere answers whether `host` is still this deployment's for the
// workspace that verified it: its TXT record holds `token`, and it points
// here. Either no is a no; yes is both.
func (d *Deps) isHere(ctx context.Context, host, token string) heard {
	a, b := d.txt(ctx, host, token), d.pointsHere(ctx, host)
	switch {
	case a == heardNo || b == heardNo:
		return heardNo
	case a == heardYes && b == heardYes:
		return heardYes
	default:
		return heardNothing
	}
}

func dnsName(v string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
}

// domainRedirect is how long a legacy app domain sends its pages on.
func (d *Deps) domainRedirect() time.Duration {
	if d.DomainRedirect > 0 {
		return d.DomainRedirect
	}
	return 90 * 24 * time.Hour
}
