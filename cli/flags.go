package cli

import (
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
)

// flag is a string flag of a command, as it was given, without the spaces
// around it.
func flag(self *xli.Command, name string) string {
	v, _ := flg.Find[string](self, name)
	return strings.TrimSpace(v)
}

// or is v, or what to say where there is none.
func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}
