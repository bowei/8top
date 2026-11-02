package ui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.ate.dev/8top/internal/style"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

func noTick(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }

type screen struct {
	name  string
	opts  []option
	setup func(h *harness)
}

var screens = []screen{
	{name: "actors", opts: []option{withAtespace("default")}},
	{name: "actors_all", opts: []option{}},
	{name: "actors_readonly", opts: []option{withAtespace("default"), readOnly}},
	{name: "actors_sorted_filtered", opts: []option{withAtespace("default")}, setup: func(h *harness) {
		h.keys("S/!crashed").enter().keys("j")
	}},
	{name: "atespaces", opts: []option{withCommand("atespaces")}},
	{name: "workers", opts: []option{withCommand("workers")}},
	{name: "templates", opts: []option{withCommand("templates default")}},
	{name: "drill_egress", opts: []option{withCommand("atespaces")}, setup: func(h *harness) {
		h.enter().enter()
	}},
	{name: "yaml", opts: []option{withAtespace("default")}, setup: func(h *harness) { h.keys("y") }},
	{name: "describe", opts: []option{withAtespace("default")}, setup: func(h *harness) { h.keys("d") }},
	{name: "logs", opts: []option{withAtespace("default")}, setup: func(h *harness) {
		h.api.logs = "2026-10-30T12:00:00Z starting counter\n2026-10-30T12:00:01Z count=1\n2026-10-30T12:00:02Z count=2\n"
		h.keys("l")
	}},
	{name: "help", opts: []option{withAtespace("default")}, setup: func(h *harness) { h.keys("?") }},
	{name: "confirm_suspend", opts: []option{withAtespace("default")}, setup: func(h *harness) { h.keys("s") }},
	{name: "confirm_delete", opts: []option{withAtespace("default")}, setup: func(h *harness) {
		h.press('d', tea.ModCtrl)
	}},
	{name: "command_prompt", opts: []option{withAtespace("default")}, setup: func(h *harness) { h.keys(":wor") }},
	{name: "filter_prompt", opts: []option{withAtespace("default")}, setup: func(h *harness) { h.keys("/counter") }},
	{name: "tag_prompt", opts: []option{withAtespace("default")}, setup: func(h *harness) { h.keys("t") }},
	{name: "flash_error", opts: []option{withAtespace("default")}, setup: func(h *harness) {
		h.api.errs["ResumeActor"] = status.Error(codes.FailedPrecondition, "actor must be SUSPENDED to resume")
		h.keys("a")
	}},
	{name: "empty", opts: []option{withCommand("tags nowhere")}},
}

// colorScreens also get a 16-color ANSI snapshot to check state colors.
var colorScreens = map[string]bool{"actors": true, "workers": true, "confirm_suspend": true}

var sizes = []struct{ w, h int }{{80, 24}, {120, 40}, {60, 20}}

func TestGolden(t *testing.T) {
	for _, sc := range screens {
		for _, sz := range sizes {
			name := fmt.Sprintf("%s_%dx%d", sc.name, sz.w, sz.h)
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, newFakeAPI(defaultObjects()...), sc.opts...)
				h.send(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
				if sc.setup != nil {
					sc.setup(h)
				}
				out := h.app.render()
				checkSize(t, style.Downsample(out, colorprofile.NoTTY), sz.w, sz.h)
				golden(t, name, style.Downsample(out, colorprofile.NoTTY))
				if colorScreens[sc.name] && sz.w == 120 {
					golden(t, name+"_ansi", style.Downsample(out, colorprofile.ANSI))
				}
			})
		}
	}
}

// TestGoldenPlainTheme renders the no-color theme, which uses reverse video
// for the selection.
func TestGoldenPlainTheme(t *testing.T) {
	plain := style.Plain()
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"), func(c *Config) { c.Theme = &plain })
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	golden(t, "plain_theme_80x24_ansi", style.Downsample(h.app.render(), colorprofile.ANSI))
}

func checkSize(t *testing.T, s string, w, h int) {
	t.Helper()
	lines := strings.Split(s, "\n")
	if len(lines) != h {
		t.Errorf("screen has %d lines, want %d", len(lines), h)
	}
	for i, l := range lines {
		if n := len([]rune(l)); n != w {
			t.Errorf("line %d is %d cells wide, want %d: %q", i, n, w, l)
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file (run go test ./internal/ui/... -update and review the diff)\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}
