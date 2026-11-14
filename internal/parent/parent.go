// Package parent is the toolkit port. Commit 1 tests use a fake.
// The systemapi agent implements it in commit 3.
package parent

import "errors"

// Kind is the shape of a stage result.
type Kind int

const (
	// KindBytes is a text stream. The "|" pipe renders rows into this shape.
	KindBytes Kind = iota
	// KindRows is a table. The "|>" pipe accepts only this shape.
	KindRows
)

// Row is one record. Keys are field names.
type Row map[string]string

// Result is what a stage or a parent verb returns.
type Result struct {
	Kind  Kind
	Bytes []byte
	Rows  []Row
	Cols  []string
	Exit  int
}

// Verb is one entitled call. Path is set for file verbs. Body is the text for file.write.
// Mode is "" or a file.write mode ("0600"); sash sets it only when a write
// needs one, because an agent older than the mode refuses any frame with it.
type Verb struct {
	Key  string
	Argv []string
	Path string
	Body []byte
	Mode string
}

// ErrModeUnsupported is a file.write with a mode that the agent refused as
// an unknown frame field: an agent older than file.write mode. The write is
// never retried without the mode.
var ErrModeUnsupported = errors.New("agent too old for file.write mode")

// Parent fulfills toolkit verbs. sash does not exec host binaries.
type Parent interface {
	Do(v Verb) (Result, error)
}
