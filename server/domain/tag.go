package domain

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/loft/internal/ent"
	"github.com/lesomnus/loft/internal/ent/asset"
	"github.com/lesomnus/loft/internal/ent/tagcounter"
	"github.com/lesomnus/loft/server/pd"
)

// A tag nobody gave is made up (D52): the prefix of the asset's kind and the
// next number of it, "A-00007". The workspace's counter of the prefix
// (`TagCounter`) says where the numbers have come to, and an operation that
// makes one up holds it until the operation ends -- as does one that writes a
// tag of the shape a made-up one has, whoever wrote it. So two in one
// workspace at once number one after the other: the second waits for the
// first, and then sees the tags it wrote. Counted afresh each time, they found
// the same number free, and the second was refused on the tag's index -- on
// PostgreSQL, where they run at once; SQLite has one writer anyway.

// tagPrefixes is what a made-up tag begins with, by the kind of the asset it
// is made up for.
var tagPrefixes = map[string]string{"item": "A", "space": "S", "kit": "K", "group": "G"}

// tagPrefix is the prefix of a tag made up for an asset of `kind`: an item's,
// for a kind that is none -- one a bulk registration says is wrong.
func tagPrefix(kind string) string { return or(tagPrefixes[kind], "A") }

// madeUpTag is the tag numbered `n` of `prefix`.
func madeUpTag(prefix string, n uint64) string { return fmt.Sprintf("%s-%05d", prefix, n) }

// madeUpPrefix is the prefix of a tag a counter could make up, whoever wrote
// it: "A-00007" is one, and "A-7" and "NB-00007" are none.
func madeUpPrefix(tag string) (string, bool) {
	p, digits, ok := strings.Cut(tag, "-")
	if !ok || !slices.Contains(slices.Collect(maps.Values(tagPrefixes)), p) {
		return "", false
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || n == 0 || madeUpTag(p, n) != tag {
		return "", false
	}
	return p, true
}

// nextTag makes up a tag for an asset of `kind` nobody gave one, holding its
// counter.
func (t *Tx) nextTag(kind string) (string, error) {
	p := tagPrefix(kind)
	held, err := t.holdTags(p)
	if err != nil {
		return "", err
	}
	tags, err := t.makeUpTags(p, held[p], 1, nil)
	if err != nil {
		return "", err
	}
	return tags[0], nil
}

// writesTags holds the counter of each prefix one of `tags` has the shape of,
// before the rows that hold them are written: a tag somebody wrote that a
// counter could have made up is written after what the counter is making up
// now, or before what it makes up next, which then passes it over. Any other
// tag no counter makes, and holds nothing.
func (t *Tx) writesTags(tags ...string) error {
	ps := []string{}
	for _, v := range tags {
		if p, ok := madeUpPrefix(v); ok {
			ps = append(ps, p)
		}
	}
	_, err := t.holdTags(ps...)
	return err
}

// holdTags takes the workspace's counter of each prefix and holds it until the
// transaction ends, answering each as it is then. They are taken in the order
// of the prefixes, so that two operations that hold more than one never each
// wait for the other.
//
// The first time a prefix is asked for in the workspace its counter is made,
// at as many tags of the prefix as the workspace ever had, erased ones too --
// which is where numbering began before there were counters. Two made at once
// are one: the second waits for the first, and then holds it.
func (t *Tx) holdTags(prefixes ...string) (map[string]*ent.TagCounter, error) {
	ps := slices.Compact(slices.Sorted(slices.Values(prefixes)))
	held := map[string]*ent.TagCounter{}
	for _, p := range ps {
		c, err := t.tagCounter(p, true)
		if err != nil {
			return nil, err
		}
		if c == nil {
			if err := t.makeTagCounter(p); err != nil {
				return nil, err
			}
			if c, err = t.tagCounter(p, true); err != nil {
				return nil, err
			}
			if c == nil {
				return nil, fmt.Errorf("domain: the counter of %s- was made, and is not there", p)
			}
		}
		held[p] = c
	}
	return held, nil
}

// tagCounter reads the workspace's counter of `prefix` -- held until the
// transaction ends, when `hold` says so -- and nil when there is none yet.
// SQLite runs one writer at a time, which holds the whole database from its
// first statement on.
func (t *Tx) tagCounter(prefix string, hold bool) (*ent.TagCounter, error) {
	q := t.db.TagCounter.Query().Where(tagcounter.TenantId(t.tenant.Uuid()), tagcounter.Prefix(prefix))
	if hold && t.postgres() {
		q.Modify(func(s *sql.Selector) { s.ForUpdate() })
	}
	c, err := q.Only(t.ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return c, err
}

// makeTagCounter makes the workspace's counter of `prefix`, at the number it
// would begin at ([Tx.tagsBegin]), unless one is there already ([Tx.insertOnce]).
// Through ent, as every write of it is: the counter is bookkeeping, and no
// server writes it, so it is no row of the trail either (payday#108).
func (t *Tx) makeTagCounter(prefix string) error {
	last, err := t.tagsBegin(prefix)
	if err != nil {
		return err
	}
	now := t.deps.now()
	return t.insertOnce(tagcounter.Table, []string{tagcounter.FieldTenantId, tagcounter.FieldPrefix},
		[]string{tagcounter.FieldId, tagcounter.FieldTenantId, tagcounter.FieldPrefix, tagcounter.FieldLast, tagcounter.FieldDateUpdated, tagcounter.FieldDateCreated},
		pdid.New(pd.TagCounterDomain).Uuid(), t.tenant.Uuid(), prefix, last, now, now,
	)
}

// tagsBegin is the number a counter of `prefix` made now begins after: as
// many as the tags of the prefix the workspace ever had.
func (t *Tx) tagsBegin(prefix string) (uint64, error) {
	n, err := t.db.Asset.Query().Where(asset.TenantId(t.tenant.Uuid()), asset.TagHasPrefix(prefix+"-")).Count(t.ctx)
	return uint64(n), err
}

// makeUpTags makes up `n` tags of `prefix`: the numbers after the last its
// counter gave that no live asset has, nor `skip` names -- the tags given
// beside them, in the same operation. An erased asset's tag is free again
// (design 3.4).
//
// `c` is the counter, which the operation holds ([Tx.holdTags]) and moves on
// to the last number it looked at, in the operation's transaction: what it
// gave is given once, and given back if the operation is refused. A dry run
// passes nil, and is answered what would be made up now, holding and writing
// nothing.
func (t *Tx) makeUpTags(prefix string, c *ent.TagCounter, n int, skip map[string]int) ([]string, error) {
	var last uint64
	switch {
	case c != nil:
		last = c.Last
	default:
		v, err := t.tagCounter(prefix, false)
		switch {
		case err != nil:
			return nil, err
		case v != nil:
			last = v.Last
		default:
			if last, err = t.tagsBegin(prefix); err != nil {
				return nil, err
			}
		}
	}

	out := make([]string, 0, n)
	for len(out) < n {
		want := make([]string, 0, n-len(out))
		for len(want) < n-len(out) {
			last++
			tag := madeUpTag(prefix, last)
			if _, ok := skip[tag]; !ok {
				want = append(want, tag)
			}
		}
		vs, err := t.db.Asset.Query().
			Where(asset.TenantId(t.tenant.Uuid()), asset.TagIn(want...), asset.DateErasedIsNil()).
			Select(asset.FieldTag).
			Strings(t.ctx)
		if err != nil {
			return nil, err
		}
		here := map[string]bool{}
		for _, v := range vs {
			here[v] = true
		}
		for _, tag := range want {
			if !here[tag] {
				out = append(out, tag)
			}
		}
	}

	if c != nil {
		if err := t.db.TagCounter.UpdateOneId(c.Id).SetLast(last).SetDateUpdated(t.deps.now()).Exec(t.ctx); err != nil {
			return nil, err
		}
		c.Last = last
	}
	return out, nil
}
