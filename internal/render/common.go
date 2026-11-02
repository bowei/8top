package render

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/duration"
)

// unknownAge is shown in the AGE column when an object has no create time.
const unknownAge = "<unknown>"

// age renders the time since created, kubectl-style ("45s", "12m", "3h5m",
// "5d"), and a sort key that orders rows by age: zero-padded seconds, so
// younger objects sort first. A zero created time renders as "<unknown>" and
// sorts after everything else.
func age(created, now time.Time) (cell, sortKey string) {
	if created.IsZero() {
		return unknownAge, "~"
	}
	d := now.Sub(created)
	return duration.HumanDuration(d), padInt(int64(d / time.Second))
}

// protoAge is age for a protobuf timestamp. A nil timestamp is unknown.
func protoAge(ts *timestamppb.Timestamp, now time.Time) (cell, sortKey string) {
	if ts == nil {
		return age(time.Time{}, now)
	}
	return age(ts.AsTime(), now)
}

// padInt renders n as a fixed-width string that sorts numerically. Every
// value it is used for (versions, counts, seconds) is non-negative, so
// negative values are clamped to zero.
func padInt(n int64) string {
	return fmt.Sprintf("%020d", max(n, 0))
}

// enumName strips prefix from a protobuf enum name, e.g.
// "ACTOR_STATE_RUNNING" -> "RUNNING".
func enumName(s, prefix string) string {
	return strings.TrimPrefix(s, prefix)
}

// ref renders an object reference as "<atespace>/<name>", or "" if both are
// empty. A reference without an atespace renders as just the name.
func ref(atespace, name string) string {
	switch {
	case atespace == "" && name == "":
		return ""
	case atespace == "":
		return name
	}
	return atespace + "/" + name
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// formatBytes renders bytes with binary suffixes, e.g. 2<<30 as "2Gi".
func formatBytes(n int64) string {
	return resource.NewQuantity(n, resource.BinarySI).String()
}

// AtespaceColumn returns the index of r's ATESPACE column, or -1 if it has
// none. The UI can hide that column when a view is scoped to one atespace,
// since every row would repeat the same value.
func AtespaceColumn(r Renderer) int {
	if r == nil {
		return -1
	}
	for i, c := range r.Columns() {
		if c.Name == "ATESPACE" {
			return i
		}
	}
	return -1
}
