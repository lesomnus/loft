package domain

import (
	"fmt"
	"regexp"
	"time"

	// The zones a workspace may name, whatever the image it runs in carries:
	// a workspace's dates are told in its own (#144).
	_ "time/tzdata"
)

// Locale is what a workspace that says nothing of its own is told in
// (`app.locale`, #144): its time zone, an IANA name, and its currency, ISO
// 4217. Empty is the pilot's: Asia/Seoul and KRW.
type Locale struct {
	Timezone string
	Currency string
}

// DefaultZone and DefaultCurrency are a deployment's when it names none.
var (
	DefaultZone     = mustZone("Asia/Seoul")
	DefaultCurrency = "KRW"
)

func mustZone(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}

	return l
}

var currency = regexp.MustCompile(`^[A-Z]{3}$`)

// maxNoShowMinutes is the longest a room waits for whoever booked it: a day.
const maxNoShowMinutes = 24 * 60

// Check refuses a locale nothing could be told in.
func (l Locale) Check() error {
	if err := checkZone(l.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	if err := checkCurrency(l.Currency); err != nil {
		return fmt.Errorf("currency: %w", err)
	}

	return nil
}

func checkZone(v string) error {
	if v == "" {
		return nil
	}
	if _, err := time.LoadLocation(v); err != nil {
		return fmt.Errorf("%q is not a zone this knows, such as Asia/Seoul", v)
	}

	return nil
}

func checkCurrency(v string) error {
	if v == "" || currency.MatchString(v) {
		return nil
	}

	return fmt.Errorf("%q is not a currency's three capital letters, such as KRW", v)
}

// priced refuses a currency an amount could not be told in.
func priced(currency string) error {
	if err := checkCurrency(currency); err != nil {
		return invalid("currency", "통화는 ISO 4217의 세 글자 대문자입니다 (예: KRW, USD)")
	}

	return nil
}

func (l Locale) zone() *time.Location {
	if l.Timezone == "" {
		return DefaultZone
	}
	if v, err := time.LoadLocation(l.Timezone); err == nil {
		return v
	}

	return DefaultZone
}

func (l Locale) currency() string {
	if l.Currency == "" {
		return DefaultCurrency
	}

	return l.Currency
}

// workspaceSettings is what the workspace says of where it is, or the
// deployment does for it: the zone its dates are told in, what a new amount is
// priced in, and how long after a room's reservation begins it is freed when
// nobody checked in -- zero holding nobody to it.
type workspaceSettings struct {
	zone     *time.Location
	currency string
	noShow   time.Duration
}

// settings is the workspace's, read once an operation asks. A workspace that
// cannot be read is told in the deployment's.
func (t *Tx) settings() *workspaceSettings {
	if t.own != nil {
		return t.own
	}

	p := &workspaceSettings{zone: t.deps.Locale.zone(), currency: t.deps.Locale.currency(), noShow: t.deps.NoShowAfter}
	if v, err := t.db.Tenant.Get(t.ctx, t.tenant.Uuid()); err == nil {
		if v.Timezone != "" {
			if l, err := time.LoadLocation(v.Timezone); err == nil {
				p.zone = l
			}
		}
		if v.Currency != "" {
			p.currency = v.Currency
		}
		switch {
		case v.NoShowMinutes > 0:
			p.noShow = time.Duration(v.NoShowMinutes) * time.Minute
		case v.NoShowMinutes < 0:
			p.noShow = 0
		}
	}

	t.own = p
	return p
}

// zone is where a date said without one is, in this workspace.
func (t *Tx) zone() *time.Location { return t.settings().zone }

// when is a moment as a sentence tells it, in this workspace's zone.
func (t *Tx) when(at time.Time) string { return at.In(t.zone()).Format("1월 2일 15:04") }
