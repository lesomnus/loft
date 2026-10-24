package domain

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	app "github.com/lesomnus/rove"
)

// An asset's history is its facts: from a moment on, one of what it is -- its
// name, its status, an attribute -- held a value (design 3.3, D5). Rove writes
// each under a key of its own, `status` or `attr.<key>`, and the value as the
// text a request said it in. payday's past keeps the same thing by the field
// (payday#151): the asset's field by its number, the entry of a map by its
// key, and the value as the field's canonical JSON (`past.Encode`). What is
// here is the one beside the other: which fields are facts, and what a fact
// written under a key is as payday's past writes it.

// assetDesc is what a fact is a field of.
var assetDesc = (&app.Asset{}).ProtoReflect().Descriptor()

// factKeys is each field of an asset that is a fact of it, by the key Rove
// writes it under, which is the field's name. Its attributes are the one map,
// an entry each, under [attrPrefix].
var factKeys = []string{"name", "desc", "type", "model", "tag", "serial", "status", "condition"}

// attrPrefix is what the key of a fact of one of an asset's attributes begins
// with, and attributes the field they are entries of.
const (
	attrPrefix = "attr."
	attributes = "attributes"
)

// assetFacts is `a` with only what is a fact of it: what payday's past says an
// asset made starts as (`past.Changes(nil, assetFacts(a))`), which is what Add
// starts one with. Not its kind, its labels, where it is or who holds it, nor
// the rest of the row: those are kept by rows of their own, or are no part of
// what it was.
func assetFacts(a *app.Asset) *app.Asset {
	from := proto.Clone(a).ProtoReflect()
	out := &app.Asset{}
	to := out.ProtoReflect()
	for _, k := range append(slices.Clone(factKeys), attributes) {
		fd := assetDesc.Fields().ByName(protoreflect.Name(k))
		if from.Has(fd) {
			to.Set(fd, from.Get(fd))
		}
	}
	return out
}

// SlotOf is the field of an asset a fact written under `key` is of, and the
// entry for one of its attributes. A key that is no fact of an asset is
// refused.
func SlotOf(key string) (past.Slot, error) {
	if k, ok := strings.CutPrefix(key, attrPrefix); ok {
		if !utf8.ValidString(k) {
			return past.Slot{}, fmt.Errorf("fact %q: the attribute's key is not UTF-8", key)
		}
		return past.Slot{Field: assetDesc.Fields().ByName(attributes).Number(), Key: k}, nil
	}
	if !slices.Contains(factKeys, key) {
		return past.Slot{}, fmt.Errorf("fact %q: no field of an asset is written under this key", key)
	}
	return past.Slot{Field: assetDesc.Fields().ByName(protoreflect.Name(key)).Number()}, nil
}

// KeyOf is the key Rove writes a fact of an asset's field under, [SlotOf]
// read backwards: an attribute's is `attr.` and its key. False for a slot of
// no fact.
func KeyOf(s past.Slot) (string, bool) {
	fd := assetDesc.Fields().ByNumber(s.Field)
	if fd == nil {
		return "", false
	}
	if string(fd.Name()) == attributes {
		return attrPrefix + s.Key, true
	}
	if s.Key != "" || !slices.Contains(factKeys, string(fd.Name())) {
		return "", false
	}
	return string(fd.Name()), true
}

// LegacyFact is a fact Rove wrote under `key`, saying `value` or `cleared`, as
// payday's past writes the same fact: the field, the entry of a map -- empty
// for any other field -- and the value's canonical JSON, `null` for one
// cleared.
//
// A type and a model are edges, which the past writes by the identifier of
// the row they name. Rove kept the identifier as text, in whatever form the
// request wrote it -- a uuid is read in upper case, in braces, as a URN -- so
// it is read as an identifier and written as its canonical string. A key that
// is no fact of an asset is refused, and so is a value the past cannot write:
// text that is not UTF-8, or an identifier that does not read as one.
func LegacyFact(key, value string, cleared bool) (field protoreflect.FieldNumber, entry, canonical string, err error) {
	s, err := SlotOf(key)
	if err != nil {
		return 0, "", "", err
	}
	if cleared {
		return s.Field, s.Key, past.Null, nil
	}

	fd := assetDesc.Fields().ByNumber(s.Field)
	v := protoreflect.ValueOfString(value)
	switch {
	case fd.IsMap():
		fd = fd.MapValue()
	case fd.Message() != nil:
		id, err := pdid.Parse(value)
		if err != nil {
			return 0, "", "", fmt.Errorf("fact %q: %q is not an identifier: %w", key, value, err)
		}
		row := (&app.Asset{}).ProtoReflect().NewField(fd).Message()
		row.Set(row.Descriptor().Fields().ByName("id"), protoreflect.ValueOfBytes(id.Bytes()))
		v = protoreflect.ValueOfMessage(row)
	}

	j, err := past.Encode(fd, v)
	if err != nil {
		return 0, "", "", fmt.Errorf("fact %q: %w", key, err)
	}
	return s.Field, s.Key, j, nil
}

// valueOf is a fact Rove says under `key` -- `value`, or cleared for nil -- as
// the value of the asset's field payday's past writes it as: an attribute an
// entry of the map, gone when it is cleared, and a type or a model the edge to
// the row the identifier names.
func valueOf(key string, value *string) (past.Value, error) {
	s, err := SlotOf(key)
	if err != nil {
		return past.Value{}, err
	}
	fd := assetDesc.Fields().ByNumber(s.Field)
	switch {
	case fd.IsMap():
		k := protoreflect.ValueOfString(s.Key).MapKey()
		if value == nil {
			return past.Entry(fd, k, protoreflect.Value{}), nil
		}
		return past.Entry(fd, k, protoreflect.ValueOfString(*value)), nil
	case value == nil:
		return past.Cleared(fd), nil
	case fd.Message() != nil:
		id, err := pdid.Parse(*value)
		if err != nil {
			return past.Value{}, fmt.Errorf("fact %q: %q is not an identifier: %w", key, *value, err)
		}
		row := (&app.Asset{}).ProtoReflect().NewField(fd).Message()
		row.Set(row.Descriptor().Fields().ByName("id"), protoreflect.ValueOfBytes(id.Bytes()))
		return past.To(fd, protoreflect.ValueOfMessage(row)), nil
	}
	return past.To(fd, protoreflect.ValueOfString(*value)), nil
}

// textOf is a fact of an asset's field as Rove reads it, the text it was said
// in: a string as it is, an edge as the identifier of the row it names, and
// nothing for one cleared.
func textOf(s past.Slot, value string) (string, error) {
	fd := assetDesc.Fields().ByNumber(s.Field)
	if fd == nil {
		return "", fmt.Errorf("field %d is no field of an asset", s.Field)
	}
	if fd.IsMap() {
		fd = fd.MapValue()
	}
	v, err := past.Decode(fd, value)
	if err != nil || !v.IsValid() {
		return "", err
	}
	if md := fd.Message(); md != nil {
		id, err := pdid.From(v.Message().Get(md.Fields().ByName("id")).Bytes())
		if err != nil {
			return "", err
		}
		return id.String(), nil
	}
	return v.String(), nil
}
