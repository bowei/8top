// Package render turns API objects into table rows.
package render

import (
	"time"

	"go.ate.dev/8top/internal/model"
)

// Status classifies a row for coloring.
type Status int

const (
	StatusNone     Status = iota // no particular state
	StatusOK                     // e.g. RUNNING, FREE
	StatusPending                // e.g. RESUMING, SUSPENDING, REVERTING
	StatusIdle                   // e.g. SUSPENDED
	StatusError                  // e.g. CRASHED, template build error
	StatusDeleting               // e.g. DELETING
)

// Column describes one table column.
type Column struct {
	Name string
	// Width is the preferred width in cells. Columns may be truncated on
	// narrow terminals.
	Width int
}

// Row is one rendered object.
type Row struct {
	Key    string
	Cells  []string
	Status Status
	// SortKeys holds a sortable value per column where the display text does
	// not sort correctly (e.g. AGE). Empty entries sort by the cell text.
	SortKeys []string
}

// Renderer renders one Kind.
type Renderer interface {
	Columns() []Column
	Render(o model.Object, now time.Time) Row
}
