package style

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"go.ate.dev/8top/internal/render"
)

func TestForStatus(t *testing.T) {
	tests := []struct {
		status render.Status
		want   string // ANSI 16-color SGR the color downsamples to
	}{
		{render.StatusOK, "\x1b[92m"},       // green
		{render.StatusPending, "\x1b[93m"},  // yellow
		{render.StatusIdle, "\x1b[37m"},     // gray
		{render.StatusError, "\x1b[91m"},    // red
		{render.StatusDeleting, "\x1b[35m"}, // magenta
	}
	for _, tc := range tests {
		c := ForStatus(tc.status)
		if c == nil {
			t.Errorf("ForStatus(%d) = nil", tc.status)
			continue
		}
		got := Downsample(lipgloss.NewStyle().Foreground(c).Render("x"), colorprofile.ANSI)
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("ForStatus(%d) renders %q, want prefix %q", tc.status, got, tc.want)
		}
	}
	if c := ForStatus(render.StatusNone); c != nil {
		t.Errorf("ForStatus(StatusNone) = %v, want nil", c)
	}
}

func TestStatusColorsAreDistinct(t *testing.T) {
	seen := map[string]render.Status{}
	for _, s := range []render.Status{render.StatusOK, render.StatusPending, render.StatusIdle, render.StatusError, render.StatusDeleting} {
		got := Downsample(Default().Status(s).Render("x"), colorprofile.ANSI)
		if prev, ok := seen[got]; ok {
			t.Errorf("status %d and %d render the same: %q", prev, s, got)
		}
		seen[got] = s
	}
}

func TestPlainHasNoColor(t *testing.T) {
	p := Plain()
	for _, s := range []render.Status{render.StatusOK, render.StatusError} {
		if got := p.Status(s).Render("x"); got != "x" {
			t.Errorf("Plain().Status(%d).Render = %q, want %q", s, got, "x")
		}
	}
	if got := Downsample(p.Selected.Render("x"), colorprofile.ANSI); got != "\x1b[7mx\x1b[m" {
		t.Errorf("Plain().Selected = %q, want reverse video", got)
	}
}

func TestDownsample(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Bold(true).Render("hi")
	tests := []struct {
		profile colorprofile.Profile
		want    string
	}{
		{colorprofile.NoTTY, "hi"},
		{colorprofile.ASCII, "\x1b[1mhi\x1b[m"},
		{colorprofile.ANSI, "\x1b[1;91mhi\x1b[m"},
		{colorprofile.TrueColor, styled},
	}
	for _, tc := range tests {
		if got := Downsample(styled, tc.profile); got != tc.want {
			t.Errorf("Downsample(%v) = %q, want %q", tc.profile, got, tc.want)
		}
	}
}
