// Package gottytuitest ships the `gotty-tui-test` agent skill as an embedded
// resource, so `gotty skill` always prints the exact skill that matches this
// binary version (the same reason `gotty usage` lives in the binary).
package gottytuitest

import _ "embed"

// Content is skills/gotty-tui-test/SKILL.md verbatim.
//
//go:embed SKILL.md
var Content string
