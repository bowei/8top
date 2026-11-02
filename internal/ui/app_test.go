package ui

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.ate.dev/8top/internal/model"
	"go.ate.dev/8top/internal/style"
)

func TestInitialView(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
	tv := h.table()
	if tv.kind != model.KindActor || tv.scope.Atespace != "default" {
		t.Errorf("initial view = %s %+v, want actors in default", tv.kind, tv.scope)
	}
	if got, want := h.visibleRowNames(), []string{"counter-1", "counter-2", "counter-3"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if got, want := style.Downsample(tv.title(h.app.theme), 0), "Actors(default)[3]"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	if got, want := h.crumbs(), []string{"actors", "default"}; !slices.Equal(got, want) {
		t.Errorf("crumbs = %v, want %v", got, want)
	}
	if got, want := len(h.ticks), 1; got != want {
		t.Errorf("scheduled ticks = %d, want %d", got, want)
	}
}

func TestInitialCommand(t *testing.T) {
	tests := []struct {
		cmd, atespace string
		wantKind      model.Kind
		wantScope     model.Scope
		wantFlash     string
	}{
		{"", "", model.KindActor, model.Scope{}, ""},
		{"workers", "default", model.KindWorker, model.Scope{}, ""},
		{"actors myspace", "", model.KindActor, model.Scope{Atespace: "myspace"}, ""},
		{"bogus", "", model.KindActor, model.Scope{}, `unknown command "bogus"`},
	}
	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			h := newHarness(t, newFakeAPI(defaultObjects()...), withCommand(tc.cmd), withAtespace(tc.atespace))
			tv := h.table()
			if tv.kind != tc.wantKind || tv.scope != tc.wantScope {
				t.Errorf("view = %s %+v, want %s %+v", tv.kind, tv.scope, tc.wantKind, tc.wantScope)
			}
			if got := h.flash(); got != tc.wantFlash {
				t.Errorf("flash = %q, want %q", got, tc.wantFlash)
			}
		})
	}
}

func TestCommands(t *testing.T) {
	tests := []struct {
		line       string
		wantKind   model.Kind
		wantScope  model.Scope
		wantCrumbs []string
		wantFlash  string
	}{
		{"workers", model.KindWorker, model.Scope{}, []string{"workers"}, ""},
		{"w", model.KindWorker, model.Scope{}, []string{"workers"}, ""},
		{"as", model.KindAtespace, model.Scope{}, []string{"atespaces"}, ""},
		{"actors myspace", model.KindActor, model.Scope{Atespace: "myspace"}, []string{"actors", "myspace"}, ""},
		{"a all", model.KindActor, model.Scope{}, []string{"actors", "all"}, ""},
		{"a", model.KindActor, model.Scope{Atespace: "default"}, []string{"actors", "default"}, ""},
		{"at", model.KindActorTemplate, model.Scope{Atespace: "default"}, []string{"templates", "default"}, ""},
		{"t", model.KindTag, model.Scope{Atespace: "default"}, []string{"tags", "default"}, ""},
		{"ep default/counter-1", model.KindEgressPolicy, model.Scope{Atespace: "default", Actor: "counter-1"}, []string{"egress", "default/counter-1"}, ""},
		{"ap", model.KindAccessPolicy, model.Scope{Atespace: "default"}, []string{"accesspolicy", "default"}, ""},
		{"wp", model.KindWorkerPool, model.Scope{}, []string{"workerpools", "all"}, ""},
		{"bogus", model.KindActor, model.Scope{Atespace: "default"}, []string{"actors", "default"}, `unknown command "bogus"`},
		{"workers extra", model.KindActor, model.Scope{Atespace: "default"}, []string{"actors", "default"}, `"workers" takes no arguments`},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
			h.keys(":" + tc.line).enter()
			tv := h.table()
			if tv.kind != tc.wantKind || tv.scope != tc.wantScope {
				t.Errorf("view = %s %+v, want %s %+v", tv.kind, tv.scope, tc.wantKind, tc.wantScope)
			}
			if got := h.crumbs(); !slices.Equal(got, tc.wantCrumbs) {
				t.Errorf("crumbs = %v, want %v", got, tc.wantCrumbs)
			}
			if got := h.flash(); got != tc.wantFlash {
				t.Errorf("flash = %q, want %q", got, tc.wantFlash)
			}
			if h.app.prompt != nil {
				t.Errorf("prompt still open")
			}
		})
	}
}

func TestQuit(t *testing.T) {
	for _, keys := range []string{":q", ":quit"} {
		h := newHarness(t, newFakeAPI(defaultObjects()...))
		h.keys(keys).enter()
		if !h.quit {
			t.Errorf("%s did not quit", keys)
		}
	}
	h := newHarness(t, newFakeAPI(defaultObjects()...))
	h.press('c', tea.ModCtrl)
	if !h.quit {
		t.Errorf("ctrl-c did not quit")
	}
}

func TestPromptEscCancels(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...))
	h.keys(":workers").esc()
	if h.app.prompt != nil {
		t.Errorf("prompt still open")
	}
	if got := h.table().kind; got != model.KindActor {
		t.Errorf("view = %s, want actors", got)
	}
}

func TestDrillDownAndBack(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withCommand("atespaces"))
	if got, want := h.visibleRowNames(), []string{"default", "myspace"}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	h.enter()
	if got, want := h.crumbs(), []string{"atespaces", "actors", "default"}; !slices.Equal(got, want) {
		t.Errorf("crumbs after enter = %v, want %v", got, want)
	}
	if got, want := h.visibleRowNames(), []string{"counter-1", "counter-2", "counter-3"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	// actor → egress policy
	h.enter()
	tv := h.table()
	if tv.kind != model.KindEgressPolicy || tv.scope != (model.Scope{Atespace: "default", Actor: "counter-1"}) {
		t.Errorf("view = %s %+v, want egress of default/counter-1", tv.kind, tv.scope)
	}
	h.esc()
	if got, want := h.selectedName(), "counter-1"; got != want {
		t.Errorf("selected after esc = %q, want %q", got, want)
	}
	h.esc()
	if got, want := h.crumbs(), []string{"atespaces"}; !slices.Equal(got, want) {
		t.Errorf("crumbs after esc esc = %v, want %v", got, want)
	}
	// esc at the root does nothing.
	h.esc()
	if got := len(h.app.stack); got != 1 {
		t.Errorf("stack depth = %d, want 1", got)
	}
	// The atespace became current and a favorite.
	if got, want := h.app.favorites, []string{"default"}; !slices.Equal(got, want) {
		t.Errorf("favorites = %v, want %v", got, want)
	}
}

func TestDrillDownWorker(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withCommand("workers"))
	h.enter()
	tv := h.table()
	if tv.kind != model.KindActorAssignment || tv.scope.Worker != "pool-a-1" {
		t.Errorf("view = %s %+v, want assignments of pool-a-1", tv.kind, tv.scope)
	}
	if got, want := h.crumbs(), []string{"workers", "assignments", "pool-a-1"}; !slices.Equal(got, want) {
		t.Errorf("crumbs = %v, want %v", got, want)
	}
	if got, want := len(tv.visible), 1; got != want {
		t.Errorf("rows = %d, want %d", got, want)
	}
}

func TestEgressKey(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
	h.keys("e")
	tv := h.table()
	if tv.kind != model.KindEgressPolicy || tv.scope.Actor != "counter-1" {
		t.Errorf("view = %s %+v, want egress of counter-1", tv.kind, tv.scope)
	}
}

func TestFilter(t *testing.T) {
	tests := []struct {
		filter string
		want   []string
	}{
		{"counter-2", []string{"counter-2"}},
		{"COUNTER-[13]", []string{"counter-1", "counter-3"}},
		{"!counter-2", []string{"counter-1", "counter-3"}},
		{"running", []string{"counter-1"}},
		{"(", nil}, // invalid regex matches literally
		{"", []string{"counter-1", "counter-2", "counter-3"}},
	}
	for _, tc := range tests {
		t.Run(tc.filter, func(t *testing.T) {
			h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
			h.keys("/" + tc.filter).enter()
			if got := h.visibleRowNames(); !slices.Equal(got, tc.want) {
				t.Errorf("visible rows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterEscClearsThenPops(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withCommand("atespaces"))
	h.enter()
	h.keys("/counter-2").enter()
	if got, want := h.visibleRowNames(), []string{"counter-2"}; !slices.Equal(got, want) {
		t.Fatalf("visible rows = %v, want %v", got, want)
	}
	if !strings.Contains(h.screen(), "Actors(default)[1] </counter-2>") {
		t.Errorf("title does not show filter:\n%s", h.screen())
	}
	h.esc()
	if got, want := h.visibleRowNames(), []string{"counter-1", "counter-2", "counter-3"}; !slices.Equal(got, want) {
		t.Errorf("after esc rows = %v, want %v", got, want)
	}
	h.esc()
	if got := h.table().kind; got != model.KindAtespace {
		t.Errorf("after second esc view = %s, want atespaces", got)
	}
}

func TestFilterLiveAndEscInPrompt(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
	h.keys("/counter-3")
	if got, want := h.visibleRowNames(), []string{"counter-3"}; !slices.Equal(got, want) {
		t.Errorf("live filter rows = %v, want %v", got, want)
	}
	h.esc()
	if got := len(h.table().visible); got != 3 {
		t.Errorf("rows after esc in prompt = %d, want 3", got)
	}
}

func TestSort(t *testing.T) {
	tests := []struct {
		keys string
		want []string
	}{
		{"", []string{"counter-1", "counter-2", "counter-3"}},
		{"S", []string{"counter-3", "counter-1", "counter-2"}},  // STATE asc
		{"SS", []string{"counter-2", "counter-1", "counter-3"}}, // STATE desc
		{"A", []string{"counter-3", "counter-2", "counter-1"}},  // AGE asc (youngest first)
		{"AA", []string{"counter-1", "counter-2", "counter-3"}}, // AGE desc
		{"N", []string{"counter-1", "counter-2", "counter-3"}},  // NAME
		{"NN", []string{"counter-3", "counter-2", "counter-1"}}, // NAME desc
		{"Z", []string{"counter-1", "counter-2", "counter-3"}},  // no such column
	}
	for _, tc := range tests {
		t.Run(tc.keys, func(t *testing.T) {
			h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
			h.keys(tc.keys)
			if got := h.visibleRowNames(); !slices.Equal(got, tc.want) {
				t.Errorf("rows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSortCyclesSameLetter(t *testing.T) {
	// W matches WORKER IP and WORKER POD: asc, desc, then the next column.
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
	tv := h.table()
	var got []int
	for range 4 {
		h.keys("W")
		got = append(got, tv.sortCol)
	}
	if want := []int{5, 5, 4, 4}; !slices.Equal(got, want) {
		t.Errorf("sort columns = %v, want %v", got, want)
	}
}

func TestShiftKeyWithoutText(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
	h.press('s', tea.ModShift)
	if got, want := h.table().sortCol, 3; got != want {
		t.Errorf("sortCol = %d, want %d (STATE)", got, want)
	}
}

func TestSelectionKeptAcrossRefresh(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	h := newHarness(t, api, withAtespace("default"))
	h.keys("j")
	if got, want := h.selectedName(), "counter-2"; got != want {
		t.Fatalf("selected = %q, want %q", got, want)
	}
	// Add a row above and reorder.
	api.set(
		actor("default", "counter-0", "RUNNING", 1),
		actor("default", "counter-3", "CRASHED", 5),
		actor("default", "counter-2", "SUSPENDED", 60),
		actor("default", "counter-1", "RUNNING", 120),
	)
	h.tick()
	if got, want := h.selectedName(), "counter-2"; got != want {
		t.Errorf("selected after reorder = %q, want %q", got, want)
	}
	if got, want := h.table().cursor, 2; got != want {
		t.Errorf("cursor = %d, want %d", got, want)
	}
	// Remove the selected row: the cursor stays at the same index.
	api.set(
		actor("default", "counter-0", "RUNNING", 1),
		actor("default", "counter-3", "CRASHED", 5),
		actor("default", "counter-1", "RUNNING", 120),
	)
	h.tick()
	if got, want := h.selectedName(), "counter-1"; got != want {
		t.Errorf("selected after delete = %q, want %q", got, want)
	}
}

func TestScrollKeptAcrossRefresh(t *testing.T) {
	var objs []model.Object
	for i := range 50 {
		objs = append(objs, actor("default", "a-"+string(rune('a'+i/26))+string(rune('a'+i%26)), "RUNNING", i))
	}
	api := newFakeAPI(objs...)
	h := newHarness(t, api, withAtespace("default"))
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	for range 30 {
		h.keys("j")
	}
	tv := h.table()
	sel, screenPos := h.selectedName(), tv.cursor-tv.offset
	// Insert a row at the top.
	api.set(append([]model.Object{actor("default", "a-000", "RUNNING", 0)}, objs...)...)
	h.tick()
	if got := h.selectedName(); got != sel {
		t.Errorf("selected = %q, want %q", got, sel)
	}
	if got := tv.cursor - tv.offset; got != screenPos {
		t.Errorf("screen position = %d, want %d", got, screenPos)
	}
}

func TestChangedRowsHighlight(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	h := newHarness(t, api, withAtespace("default"))
	tv := h.table()
	if len(tv.changed) != 0 {
		t.Errorf("changed on first load = %v, want none", tv.changed)
	}
	api.set(
		actor("default", "counter-1", "RUNNING", 120),
		actor("default", "counter-2", "RESUMING", 60),
		actor("default", "counter-3", "CRASHED", 5),
		actor("default", "counter-4", "RUNNING", 0),
	)
	h.tick()
	var got []string
	for k := range tv.changed {
		got = append(got, k)
	}
	slices.Sort(got)
	if want := []string{"default/counter-2", "default/counter-4"}; !slices.Equal(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
	h.tick()
	if len(tv.changed) != 0 {
		t.Errorf("changed after quiet refresh = %v, want none", tv.changed)
	}
}

func TestPolling(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	h := newHarness(t, api, withAtespace("default"))
	lists := func() int { return len(api.callsTo("List")) }
	n := lists()
	h.tick()
	if got := lists(); got != n+1 {
		t.Errorf("List calls after tick = %d, want %d", got, n+1)
	}
	// A stale tick (older generation) is ignored.
	h.send(tickMsg{view: h.app.top().id(), gen: h.app.pollGen - 1})
	if got := lists(); got != n+1 {
		t.Errorf("List calls after stale tick = %d, want %d", got, n+1)
	}
	// A tick for a view that is not active is ignored.
	actors := h.app.top().id()
	h.keys("y") // push YAML view
	n = lists()
	h.send(tickMsg{view: actors, gen: h.app.pollGen})
	if got := lists(); got != n {
		t.Errorf("List calls after tick for inactive view = %d, want %d", got, n)
	}
	// ctrl-r refreshes now.
	h.esc()
	n = lists()
	h.press('r', tea.ModCtrl)
	if got := lists(); got != n+1 {
		t.Errorf("List calls after ctrl-r = %d, want %d", got, n+1)
	}
}

func TestListErrorFlashes(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	h := newHarness(t, api, withAtespace("default"))
	api.errs["List"] = status.Error(codes.Unavailable, "connection refused")
	h.tick()
	if got, want := h.flash(), "Unavailable: connection refused"; got != want {
		t.Errorf("flash = %q, want %q", got, want)
	}
	if got := len(h.table().visible); got != 3 {
		t.Errorf("rows kept = %d, want 3", got)
	}
}

func TestKeyHints(t *testing.T) {
	tests := []struct {
		cmd      string
		readOnly bool
		want     []string
	}{
		{"actors", false, []string{"a", "s", "r", "e", "l", "p", "t", "y", "d", "E", "ctrl-d", "?"}},
		{"actors", true, []string{"e", "l", "p", "y", "d", "?"}},
		{"atespaces", false, []string{"y", "d", "ctrl-d", "?"}},
		{"atespaces", true, []string{"y", "d", "?"}},
		{"templates", false, []string{"y", "d", "E", "ctrl-d", "?"}},
		{"tags", false, []string{"y", "d", "E", "ctrl-d", "?"}},
		{"egress", false, []string{"y", "d", "E", "ctrl-d", "?"}},
		{"workers", false, []string{"n", "c", "u", "y", "d", "?"}},
		{"workers", true, []string{"y", "d", "?"}},
		{"accesspolicy", false, []string{"y", "d", "E", "?"}},
		{"accesspolicy", true, []string{"y", "d", "?"}},
		{"workerpools", false, []string{"y", "d", "?"}},
		{"assignments pool-a-1", false, []string{"y", "d", "?"}},
	}
	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			opts := []option{withCommand(tc.cmd)}
			if tc.readOnly {
				opts = append(opts, readOnly)
			}
			h := newHarness(t, newFakeAPI(defaultObjects()...), opts...)
			if got := h.hintKeys(); !slices.Equal(got, tc.want) {
				t.Errorf("hints = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeyHintsOtherViews(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
	h.keys("y")
	if got, want := h.hintKeys(), []string{"?"}; !slices.Equal(got, want) {
		t.Errorf("yaml hints = %v, want %v", got, want)
	}
	h.esc().keys("l")
	if got, want := h.hintKeys(), []string{"f", "?"}; !slices.Equal(got, want) {
		t.Errorf("logs hints = %v, want %v", got, want)
	}
}

func TestReadOnlyHeaderHasNoMutatingHints(t *testing.T) {
	for _, k := range kinds {
		h := newHarness(t, newFakeAPI(defaultObjects()...), withCommand(k.aliases[0]+map[bool]string{true: " pool-a-1"}[k.kind == model.KindActorAssignment]), readOnly)
		for _, b := range h.app.headerHints() {
			if b.mutating {
				t.Errorf("%s: read-only header shows mutating key %s", k.kind, b.Help().Key)
			}
		}
	}
}

func TestSuspendConfirm(t *testing.T) {
	tests := []struct {
		name      string
		keys      string
		wantCalls []string
	}{
		{"confirm", "sy", []string{"SuspendActor default/counter-1 wait=false"}},
		{"wait for snapshot", "swy", []string{"SuspendActor default/counter-1 wait=true"}},
		{"cancel", "sn", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(defaultObjects()...)
			h := newHarness(t, api, withAtespace("default"))
			h.keys("s")
			if h.app.confirm == nil {
				t.Fatal("s did not ask for confirmation")
			}
			if got := api.callsTo("SuspendActor"); len(got) != 0 {
				t.Fatalf("SuspendActor called before confirming: %v", got)
			}
			lists := len(api.callsTo("List"))
			h.keys(tc.keys[1:])
			if h.app.confirm != nil {
				t.Errorf("dialog still open")
			}
			if got := api.callsTo("SuspendActor"); !slices.Equal(got, tc.wantCalls) {
				t.Errorf("calls = %v, want %v", got, tc.wantCalls)
			}
			if tc.wantCalls == nil {
				return
			}
			if got, want := h.flash(), "Suspend requested for actor default/counter-1"; got != want {
				t.Errorf("flash = %q, want %q", got, want)
			}
			if got := len(api.callsTo("List")); got != lists+1 {
				t.Errorf("List calls = %d, want %d (refresh after action)", got, lists+1)
			}
		})
	}
}

func TestConfirmButtons(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	h := newHarness(t, api, withAtespace("default"))
	// enter defaults to Cancel.
	h.keys("r").enter()
	if got := api.callsTo("RevertActor"); len(got) != 0 {
		t.Errorf("enter on default button reverted: %v", got)
	}
	// tab moves to OK.
	h.keys("r").press(tea.KeyTab).enter()
	if got, want := api.callsTo("RevertActor"), []string{"RevertActor default/counter-1"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestActions(t *testing.T) {
	tests := []struct {
		name      string
		cmd       string
		keys      []tea.KeyPressMsg
		wantCalls []string
		wantFlash string
	}{
		{"resume", "actors default", keysOf("a"), []string{"ResumeActor default/counter-1"}, "Resume requested for actor default/counter-1"},
		{"revert", "actors default", keysOf("ry"), []string{"RevertActor default/counter-1"}, "Revert requested for actor default/counter-1"},
		{"delete actor", "actors default", append([]tea.KeyPressMsg{{Code: 'd', Mod: tea.ModCtrl}}, keysOf("y")...),
			[]string{"Delete actors default/counter-1"}, "Deleted actor default/counter-1"},
		{"delete atespace", "atespaces", append([]tea.KeyPressMsg{{Code: 'd', Mod: tea.ModCtrl}}, keysOf("y")...),
			[]string{"Delete atespaces default"}, "Deleted atespace default"},
		{"drain", "workers", keysOf("ny"), []string{"DrainWorker pool-a-1"}, "Drain requested for worker pool-a-1"},
		{"available", "workers", keysOf("c"), []string{"SetWorkerAvailable pool-a-1 true"}, "Marked worker pool-a-1 available"},
		{"unavailable", "workers", keysOf("u"), []string{"SetWorkerAvailable pool-a-1 false"}, "Marked worker pool-a-1 unavailable"},
		{"tag", "actors default", append(keysOf("tv1"), tea.KeyPressMsg{Code: tea.KeyEnter}),
			[]string{"CreateTag default v1 counter-1"}, "Tagged the snapshot of default/counter-1 as v1"},
		{"tag empty", "actors default", append(keysOf("t"), tea.KeyPressMsg{Code: tea.KeyEnter}), nil, "Tag cancelled: no name given"},
		{"port-forward", "actors default", keysOf("p"), nil, "Port-forward is not implemented yet"},
		{"action not for kind", "workers", keysOf("a"), nil, ""},
	}
	mutations := []string{"ResumeActor", "SuspendActor", "RevertActor", "Delete", "DrainWorker", "SetWorkerAvailable", "CreateTag", "Update"}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(defaultObjects()...)
			h := newHarness(t, api, withCommand(tc.cmd))
			for _, k := range tc.keys {
				h.send(k)
			}
			var got []string
			for _, m := range mutations {
				got = append(got, api.callsTo(m)...)
			}
			if !slices.Equal(got, tc.wantCalls) {
				t.Errorf("calls = %v, want %v", got, tc.wantCalls)
			}
			if got := h.flash(); got != tc.wantFlash {
				t.Errorf("flash = %q, want %q", got, tc.wantFlash)
			}
			if h.app.inflight != 0 {
				t.Errorf("inflight = %d, want 0", h.app.inflight)
			}
		})
	}
}

func keysOf(s string) []tea.KeyPressMsg {
	var out []tea.KeyPressMsg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

func TestActionErrors(t *testing.T) {
	tests := []struct {
		method, keys string
		err          error
		want         string
	}{
		{"ResumeActor", "a", status.Error(codes.FailedPrecondition, "actor must be SUSPENDED to resume"), "FailedPrecondition: actor must be SUSPENDED to resume"},
		{"SuspendActor", "sy", status.Error(codes.Aborted, "version mismatch"), "Aborted: version mismatch"},
		{"RevertActor", "ry", errors.New("boom"), "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			api := newFakeAPI(defaultObjects()...)
			api.errs[tc.method] = tc.err
			h := newHarness(t, api, withAtespace("default"))
			h.keys(tc.keys)
			if got := h.flash(); got != tc.want {
				t.Errorf("flash = %q, want %q", got, tc.want)
			}
			if h.app.flash.level != flashError {
				t.Errorf("flash level = %v, want error", h.app.flash.level)
			}
		})
	}
}

func TestReadOnlyBlocksMutations(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	h := newHarness(t, api, withAtespace("default"), readOnly)
	for _, k := range []string{"a", "s", "r", "t", "E"} {
		h.keys(k)
		if h.app.confirm != nil || h.app.prompt != nil {
			t.Fatalf("%s opened a dialog in read-only mode", k)
		}
		if got, want := h.flash(), "8top is in read-only mode"; got != want {
			t.Errorf("%s: flash = %q, want %q", k, got, want)
		}
	}
	h.press('d', tea.ModCtrl)
	if h.app.confirm != nil {
		t.Errorf("ctrl-d asked for confirmation in read-only mode")
	}
	if got := len(api.calls) - len(api.callsTo("List")); got != 0 {
		t.Errorf("non-List calls = %v", api.calls)
	}
}

func TestFavorites(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"), func(c *Config) { c.Favorites = []string{"myspace"} })
	if got, want := h.app.favorites, []string{"myspace", "default"}; !slices.Equal(got, want) {
		t.Fatalf("favorites = %v, want %v", got, want)
	}
	h.keys("0")
	if got, want := h.table().scope.Atespace, ""; got != want {
		t.Errorf("after 0 atespace = %q, want %q", got, want)
	}
	if got := len(h.table().visible); got != 4 {
		t.Errorf("rows in all atespaces = %d, want 4", got)
	}
	h.keys("1")
	if got, want := h.visibleRowNames(), []string{"web-1"}; !slices.Equal(got, want) {
		t.Errorf("after 1 rows = %v, want %v", got, want)
	}
	if got, want := len(h.app.stack), 1; got != want {
		t.Errorf("stack depth = %d, want %d (switching replaces the view)", got, want)
	}
	h.keys("5")
	if got, want := h.flash(), "No favorite atespace 5"; got != want {
		t.Errorf("flash = %q, want %q", got, want)
	}
}

func TestYAMLView(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	h := newHarness(t, api, withAtespace("default"))
	h.keys("y")
	v, ok := h.app.top().(*textView)
	if !ok {
		t.Fatalf("top = %T, want *textView", h.app.top())
	}
	if !strings.Contains(v.text, "name: counter-1") {
		t.Errorf("yaml = %q", v.text)
	}
	if got, want := api.callsTo("Get"), []string{"Get actors default/counter-1"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if got, want := h.crumbs(), []string{"actors", "default", "yaml"}; !slices.Equal(got, want) {
		t.Errorf("crumbs = %v, want %v", got, want)
	}
	h.esc()
	if _, ok := h.app.top().(*tableView); !ok {
		t.Errorf("esc did not pop the YAML view")
	}
}

func TestDescribe(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
	h.keys("d")
	v := h.app.top().(*textView)
	for _, want := range []string{"name: counter-1", "# Related Egress(default/counter-1)", "RULES"} {
		if !strings.Contains(v.text, want) {
			t.Errorf("describe does not contain %q:\n%s", want, v.text)
		}
	}
}

func TestEnterOnLeafShowsYAML(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withCommand("templates default"))
	h.enter()
	if _, ok := h.app.top().(*textView); !ok {
		t.Errorf("top = %T, want *textView", h.app.top())
	}
}

func TestLogs(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	api.logs = "line 1\nline 2\nline 3\n"
	h := newHarness(t, api, withAtespace("default"))
	h.keys("l")
	v, ok := h.app.top().(*logsView)
	if !ok {
		t.Fatalf("top = %T, want *logsView", h.app.top())
	}
	if got, want := v.lines, []string{"line 1", "line 2", "line 3"}; !slices.Equal(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
	if !v.ended {
		t.Errorf("stream not marked ended after EOF")
	}
	if got, want := api.callsTo("StreamLogs"), []string{"StreamLogs default/counter-1 follow=true"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	h.keys("f")
	if v.follow {
		t.Errorf("f did not turn follow off")
	}
	h.keys("f")
	if !v.follow {
		t.Errorf("f did not turn follow on")
	}
	h.esc()
	if _, ok := h.app.top().(*tableView); !ok {
		t.Errorf("esc did not pop the logs view")
	}
}

func TestReadBatch(t *testing.T) {
	tests := []struct {
		in        string
		max       int
		wantLines [][]string
	}{
		{"a\nb\nc\n", 10, [][]string{{"a", "b", "c"}}},
		{"a\nb\nc\n", 2, [][]string{{"a", "b"}, {"c"}}},
		{"a\nb", 10, [][]string{{"a"}, {"b"}}}, // a partial line waits for the next read
		{"", 10, [][]string{nil}},
	}
	for _, tc := range tests {
		br := newBufReader(tc.in)
		var got [][]string
		for {
			lines, err := readBatch(br, tc.max)
			got = append(got, lines)
			if err != nil {
				break
			}
			if len(got) > 10 {
				t.Fatal("no EOF")
			}
		}
		// The last batch may be empty when the input ends with a newline.
		if n := len(got); n > 1 && got[n-1] == nil {
			got = got[:n-1]
		}
		if len(got) != len(tc.wantLines) {
			t.Errorf("readBatch(%q, %d) batches = %q, want %q", tc.in, tc.max, got, tc.wantLines)
			continue
		}
		for i := range got {
			if !slices.Equal(got[i], tc.wantLines[i]) {
				t.Errorf("readBatch(%q, %d) batch %d = %q, want %q", tc.in, tc.max, i, got[i], tc.wantLines[i])
			}
		}
	}
}

func TestLogsError(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	api.errs["StreamLogs"] = status.Error(codes.FailedPrecondition, "actor is not running")
	h := newHarness(t, api, withAtespace("default"))
	h.keys("l")
	if got, want := h.flash(), "FailedPrecondition: actor is not running"; got != want {
		t.Errorf("flash = %q, want %q", got, want)
	}
}

func TestHelpView(t *testing.T) {
	h := newHarness(t, newFakeAPI(defaultObjects()...), withAtespace("default"))
	h.keys("?")
	if _, ok := h.app.top().(*helpView); !ok {
		t.Fatalf("top = %T, want *helpView", h.app.top())
	}
	scr := h.screen()
	for _, want := range []string{"RESOURCE", "GENERAL", "Suspend", "shift-<col>", "<1> default"} {
		if !strings.Contains(scr, want) {
			t.Errorf("help screen does not contain %q:\n%s", want, scr)
		}
	}
	h.keys("?") // no nested help
	if got := len(h.app.stack); got != 2 {
		t.Errorf("stack depth = %d, want 2", got)
	}
	h.esc()
	if _, ok := h.app.top().(*tableView); !ok {
		t.Errorf("esc did not close help")
	}
}

func TestEdit(t *testing.T) {
	tests := []struct {
		name string
		// editor rewrites the file on the n-th open.
		editor      func(path string, n int) error
		wantOpens   int
		wantUpdates []string
		wantFlash   string
		wantLastHas string // content of the last editor open
	}{
		{
			name:      "no change",
			editor:    func(string, int) error { return nil },
			wantOpens: 1, wantFlash: "Edit cancelled, no changes made",
		},
		{
			name:      "emptied",
			editor:    func(p string, _ int) error { return os.WriteFile(p, nil, 0o600) },
			wantOpens: 1, wantFlash: "Edit cancelled, no changes made",
		},
		{
			name:        "valid edit",
			editor:      appendLine("  workerSelector: gpu\n"),
			wantOpens:   1,
			wantUpdates: []string{"Update actors default/counter-1"},
			wantFlash:   "Updated actor default/counter-1",
		},
		{
			name: "conflict then fixed",
			editor: func(p string, n int) error {
				if n == 1 {
					return appendLine("  conflict: true\n")(p, n)
				}
				b, _ := os.ReadFile(p)
				return os.WriteFile(p, bytes.ReplaceAll(b, []byte("conflict"), []byte("fixed")), 0o600)
			},
			wantOpens:   2,
			wantUpdates: []string{"Update actors default/counter-1"},
			wantFlash:   "Updated actor default/counter-1",
			wantLastHas: "# error: Aborted: the object has been modified\n",
		},
		{
			name: "conflict then unchanged",
			editor: func(p string, n int) error {
				if n == 1 {
					return appendLine("  conflict: true\n")(p, n)
				}
				return nil
			},
			wantOpens:   2,
			wantFlash:   "Edit cancelled, no changes made after error: Aborted: the object has been modified",
			wantLastHas: "  conflict: true\n",
		},
		{
			name:      "editor fails",
			editor:    func(string, int) error { return errors.New("exit status 1") },
			wantOpens: 1, wantFlash: "editor: exit status 1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(defaultObjects()...)
			h := newHarness(t, api, withAtespace("default"))
			h.editor = tc.editor
			h.keys("E")
			if got := h.editorN; got != tc.wantOpens {
				t.Errorf("editor opened %d times, want %d", got, tc.wantOpens)
			}
			if got := api.callsTo("Update"); !slices.Equal(got, tc.wantUpdates) {
				t.Errorf("updates = %v, want %v", got, tc.wantUpdates)
			}
			if got := h.flash(); got != tc.wantFlash {
				t.Errorf("flash = %q, want %q", got, tc.wantFlash)
			}
			if len(h.editSeen) > 0 {
				first := string(h.editSeen[0])
				if !strings.Contains(first, "name: counter-1") {
					t.Errorf("edit file = %q", first)
				}
				if last := string(h.editSeen[len(h.editSeen)-1]); !strings.Contains(last, tc.wantLastHas) {
					t.Errorf("last edit file = %q, want it to contain %q", last, tc.wantLastHas)
				}
			}
			if h.app.edit != nil {
				t.Errorf("edit session still open")
			}
			entries, _ := os.ReadDir(h.app.cfg.TempDir)
			if len(entries) != 0 {
				t.Errorf("temp files left behind: %v", entries)
			}
		})
	}
}

func appendLine(line string) func(string, int) error {
	return func(p string, _ int) error {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(p, append(b, line...), 0o600)
	}
}

func TestEditTemplateIsReadOnly(t *testing.T) {
	api := newFakeAPI(defaultObjects()...)
	h := newHarness(t, api, withCommand("templates default"))
	h.keys("E")
	if h.editorN != 0 {
		t.Errorf("editor opened for a template")
	}
	v, ok := h.app.top().(*textView)
	if !ok {
		t.Fatalf("top = %T, want *textView", h.app.top())
	}
	if !strings.HasPrefix(v.text, "# Templates are immutable.") {
		t.Errorf("yaml = %q, want the immutable hint on top", v.text)
	}
	if got, want := h.flash(), "Templates are immutable. Create a new template and repoint suspended actors at it."; got != want {
		t.Errorf("flash = %q, want %q", got, want)
	}
}

func TestErrText(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{status.Error(codes.FailedPrecondition, "actor must be SUSPENDED to change its template"), "FailedPrecondition: actor must be SUSPENDED to change its template"},
		{status.Error(codes.Aborted, "conflict"), "Aborted: conflict"},
		{errors.New("plain"), "plain"},
	}
	for _, tc := range tests {
		if got := errText(tc.err); got != tc.want {
			t.Errorf("errText(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		line    string
		want    command
		wantErr bool
	}{
		{"actors", command{kind: model.KindActor, scope: model.Scope{Atespace: "cur"}}, false},
		{":a ns1", command{kind: model.KindActor, scope: model.Scope{Atespace: "ns1"}, setAtespace: true}, false},
		{"a all", command{kind: model.KindActor, setAtespace: true}, false},
		{"a -A", command{kind: model.KindActor, setAtespace: true}, false},
		{"as", command{kind: model.KindAtespace}, false},
		{"ep ns1/act", command{kind: model.KindEgressPolicy, scope: model.Scope{Atespace: "ns1", Actor: "act"}, setAtespace: true}, false},
		{"wp kube-system", command{kind: model.KindWorkerPool, scope: model.Scope{Namespace: "kube-system"}}, false},
		{"assignments w1", command{kind: model.KindActorAssignment, scope: model.Scope{Worker: "w1"}}, false},
		{"q", command{quit: true}, false},
		{"q!", command{quit: true}, false},
		{"", command{}, true},
		{"nope", command{}, true},
		{"as x", command{}, true},
		{"a x y", command{}, true},
		{"assignments", command{}, true},
	}
	for _, tc := range tests {
		got, err := parseCommand(tc.line, "cur")
		if (err != nil) != tc.wantErr {
			t.Errorf("parseCommand(%q) error = %v, wantErr %v", tc.line, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("parseCommand(%q) = %+v, want %+v", tc.line, got, tc.want)
		}
	}
}

func TestReadmeAliases(t *testing.T) {
	// Every alias in the README resource table opens its view.
	for _, row := range readmeTable(t, "View", "Command") {
		cmds := readmeCommands(row["Command"])
		for _, alias := range cmds {
			c, err := parseCommand(alias, "")
			if err != nil && c.kind == "" {
				// Kinds that need an argument.
				c, err = parseCommand(alias+" x", "")
			}
			if err != nil {
				t.Errorf("README command %s: %v", alias, err)
				continue
			}
			if want := strings.TrimPrefix(cmds[0], ":"); infoFor(c.kind).aliases[0] != want {
				t.Errorf("README command %s opens %s, want %s", alias, c.kind, want)
			}
		}
	}
}
