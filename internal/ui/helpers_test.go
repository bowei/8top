package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.ate.dev/8top/internal/model"
	"go.ate.dev/8top/internal/render"
	"go.ate.dev/8top/internal/style"
)

// testNow is the fixed clock of every test.
var testNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// testObj is the Raw of fake objects: the cells before AGE.
type testObj struct {
	cells   []string
	status  render.Status
	created time.Time
	worker  string // for assignments
}

// --- stub renderers ---

var testColumns = map[model.Kind][]string{
	model.KindAtespace:        {"NAME", "AGE"},
	model.KindActor:           {"ATESPACE", "NAME", "TEMPLATE", "STATE", "WORKER POD", "WORKER IP", "VERSION", "AGE"},
	model.KindActorTemplate:   {"ATESPACE", "NAME", "GOLDEN TAG", "ERROR", "AGE"},
	model.KindTag:             {"ATESPACE", "NAME", "SOURCE ACTOR", "SCOPE", "STATE", "AGE"},
	model.KindEgressPolicy:    {"ATESPACE", "ACTOR", "RULES", "VERSION", "AGE"},
	model.KindWorker:          {"NAMESPACE", "POOL", "POD", "STATE", "STATUS", "CPU", "MEMORY", "AGE"},
	model.KindActorAssignment: {"ATESPACE", "ACTOR", "ACTOR UID", "CPU", "MEMORY", "AGE"},
	model.KindAccessPolicy:    {"SCOPE", "BINDINGS", "ROLES", "VERSION", "AGE"},
	model.KindWorkerPool:      {"NAMESPACE", "NAME", "READY", "DESIRED", "AGE"},
}

type stubRenderer struct{ kind model.Kind }

func (r stubRenderer) Columns() []render.Column {
	var cols []render.Column
	for _, n := range testColumns[r.kind] {
		cols = append(cols, render.Column{Name: n})
	}
	return cols
}

func (r stubRenderer) Render(o model.Object, now time.Time) render.Row {
	t := o.Raw.(testObj)
	cells := slices.Clone(t.cells)
	var sortKeys []string
	if cols := testColumns[r.kind]; cols[len(cols)-1] == "AGE" {
		age := now.Sub(t.created)
		cells = append(cells, fmtAge(age))
		sortKeys = make([]string, len(cells))
		sortKeys[len(cells)-1] = fmt.Sprintf("%020d", int64(age))
	}
	return render.Row{Key: o.Key, Cells: cells, Status: t.status, SortKeys: sortKeys}
}

func fmtAge(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func stubRendererFor(k model.Kind) render.Renderer { return stubRenderer{k} }

// --- fixtures ---

func actor(ns, name, state string, ageMin int) model.Object {
	st := map[string]render.Status{
		"RUNNING": render.StatusOK, "RESUMING": render.StatusPending, "SUSPENDING": render.StatusPending,
		"SUSPENDED": render.StatusIdle, "CRASHED": render.StatusError, "DELETING": render.StatusDeleting,
	}[state]
	pod, ip := "", ""
	if state == "RUNNING" {
		pod, ip = "ate/pool-a-"+name[len(name)-1:], "10.0.0."+name[len(name)-1:]
	}
	return model.Object{
		Kind: model.KindActor, Key: ns + "/" + name, Atespace: ns, Name: name,
		Raw: testObj{
			cells:   []string{ns, name, ns + "/counter", state, pod, ip, "7"},
			status:  st,
			created: testNow.Add(-time.Duration(ageMin) * time.Minute),
		},
	}
}

func atespace(name string, ageMin int) model.Object {
	return model.Object{Kind: model.KindAtespace, Key: name, Name: name,
		Raw: testObj{cells: []string{name}, created: testNow.Add(-time.Duration(ageMin) * time.Minute)}}
}

func worker(name, state, statusText string) model.Object {
	st := render.StatusOK
	if state != "READY" {
		st = render.StatusError
	}
	return model.Object{Kind: model.KindWorker, Key: name, Name: name,
		Raw: testObj{cells: []string{"ate", "pool-a", name, state, statusText, "250m", "512Mi"}, status: st,
			created: testNow.Add(-26 * time.Hour)}}
}

func egress(ns, actorName string) model.Object {
	return model.Object{Kind: model.KindEgressPolicy, Key: ns + "/" + actorName, Atespace: ns, Name: actorName,
		Raw: testObj{cells: []string{ns, actorName, "2", "3"}, created: testNow.Add(-time.Hour)}}
}

func assignment(workerName, ns, actorName string) model.Object {
	return model.Object{Kind: model.KindActorAssignment, Key: workerName + "/" + ns + "/" + actorName, Atespace: ns, Name: actorName,
		Raw: testObj{cells: []string{ns, actorName, "6f1c2a9e", "100m", "128Mi"}, created: testNow.Add(-time.Minute), worker: workerName}}
}

func template(ns, name string) model.Object {
	return model.Object{Kind: model.KindActorTemplate, Key: ns + "/" + name, Atespace: ns, Name: name,
		Raw: testObj{cells: []string{ns, name, "golden", ""}, created: testNow.Add(-48 * time.Hour)}}
}

func defaultObjects() []model.Object {
	return []model.Object{
		atespace("default", 60*24*3), atespace("myspace", 90),
		actor("default", "counter-1", "RUNNING", 120),
		actor("default", "counter-2", "SUSPENDED", 60),
		actor("default", "counter-3", "CRASHED", 5),
		actor("myspace", "web-1", "RUNNING", 30),
		worker("pool-a-1", "READY", "ASSIGNED"),
		worker("pool-a-2", "READY", "FREE"),
		worker("pool-a-3", "LOST", "UNAVAILABLE"),
		egress("default", "counter-1"),
		assignment("pool-a-1", "default", "counter-1"),
		template("default", "counter"),
	}
}

// --- fake API ---

type fakeAPI struct {
	mu    sync.Mutex
	objs  []model.Object
	calls []string
	// errs makes the named method fail.
	errs map[string]error
	logs string
}

func newFakeAPI(objs ...model.Object) *fakeAPI {
	return &fakeAPI{objs: objs, errs: map[string]error{}}
}

func (f *fakeAPI) record(format string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := fmt.Sprintf(format, args...)
	f.calls = append(f.calls, c)
	name, _, _ := strings.Cut(c, " ")
	return f.errs[name]
}

func (f *fakeAPI) callsTo(method string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c == method || strings.HasPrefix(c, method+" ") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeAPI) set(objs ...model.Object) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs = objs
}

func (f *fakeAPI) List(_ context.Context, kind model.Kind, scope model.Scope) ([]model.Object, error) {
	if err := f.record("List %s %+v", kind, scope); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Object
	for _, o := range f.objs {
		if o.Kind != kind {
			continue
		}
		if scope.Atespace != "" && o.Atespace != "" && o.Atespace != scope.Atespace {
			continue
		}
		if kind == model.KindEgressPolicy && scope.Actor != "" && o.Name != scope.Actor {
			continue
		}
		if kind == model.KindActorAssignment && o.Raw.(testObj).worker != scope.Worker {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

func (f *fakeAPI) Get(_ context.Context, o model.Object) (model.Object, error) {
	if err := f.record("Get %s %s", o.Kind, o.Key); err != nil {
		return model.Object{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.objs {
		if x.Kind == o.Kind && x.Key == o.Key {
			return x, nil
		}
	}
	return model.Object{}, status.Error(codes.NotFound, "not found")
}

func (f *fakeAPI) Update(_ context.Context, o model.Object) (model.Object, error) {
	return o, f.record("Update %s %s", o.Kind, o.Key)
}

func (f *fakeAPI) Delete(_ context.Context, o model.Object) error {
	return f.record("Delete %s %s", o.Kind, o.Key)
}

func (f *fakeAPI) ResumeActor(_ context.Context, ns, name string) error {
	return f.record("ResumeActor %s/%s", ns, name)
}

func (f *fakeAPI) SuspendActor(_ context.Context, ns, name string, wait bool) error {
	return f.record("SuspendActor %s/%s wait=%v", ns, name, wait)
}

func (f *fakeAPI) RevertActor(_ context.Context, ns, name string) error {
	return f.record("RevertActor %s/%s", ns, name)
}

func (f *fakeAPI) CreateTag(_ context.Context, ns, tag, actorName string) error {
	return f.record("CreateTag %s %s %s", ns, tag, actorName)
}

func (f *fakeAPI) DrainWorker(_ context.Context, w string) error {
	return f.record("DrainWorker %s", w)
}

func (f *fakeAPI) SetWorkerAvailable(_ context.Context, w string, available bool) error {
	return f.record("SetWorkerAvailable %s %v", w, available)
}

func (f *fakeAPI) StreamLogs(_ context.Context, ns, name string, follow bool) (io.ReadCloser, error) {
	if err := f.record("StreamLogs %s/%s follow=%v", ns, name, follow); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(f.logs)), nil
}

// --- edit hooks ---

func testMarshal(o model.Object) ([]byte, error) {
	t := o.Raw.(testObj)
	return []byte(fmt.Sprintf("kind: %s\nmetadata:\n  atespace: %s\n  name: %s\nspec:\n  cells: [%s]\n",
		o.Kind, o.Atespace, o.Name, strings.Join(t.cells, ", "))), nil
}

func testEditable(k model.Kind) (bool, string) {
	if k == model.KindActorTemplate {
		return false, "Templates are immutable. Create a new template and repoint suspended actors at it."
	}
	return true, ""
}

// testApply fails with Aborted if the file contains "conflict", and reports
// a change if the content differs from the marshaled original.
func testApply(ctx context.Context, api model.API, orig model.Object, edited []byte) (model.Object, bool, error) {
	edited = stripAnnotation(edited)
	want, _ := testMarshal(orig)
	if len(bytes.TrimSpace(edited)) == 0 || bytes.Equal(edited, want) {
		return orig, false, nil
	}
	if bytes.Contains(edited, []byte("conflict")) {
		return orig, false, status.Error(codes.Aborted, "the object has been modified")
	}
	o, err := api.Update(ctx, orig)
	return o, err == nil, err
}

func testAnnotate(content []byte, err error) []byte {
	return append([]byte("# error: "+errText(err)+"\n"), stripAnnotation(content)...)
}

func stripAnnotation(b []byte) []byte {
	for bytes.HasPrefix(b, []byte("# error: ")) {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			b = nil
		}
	}
	return b
}

// --- harness ---

type harness struct {
	t     *testing.T
	app   *App
	api   *fakeAPI
	ticks []time.Duration
	quit  bool
	// editor is run instead of $EDITOR on the edit file.
	editor   func(path string, n int) error
	editorN  int
	editSeen [][]byte // file contents each time the editor opened
}

type option func(*Config)

func readOnly(c *Config) { c.ReadOnly = true }

func withCommand(cmd string) option { return func(c *Config) { c.Command = cmd } }

func withAtespace(ns string) option { return func(c *Config) { c.Atespace = ns } }

func newHarness(t *testing.T, api *fakeAPI, opts ...option) *harness {
	t.Helper()
	h := &harness{t: t, api: api}
	theme := style.Default()
	cfg := Config{
		API:      api,
		Renderer: stubRendererFor,
		Context:  "kind-ate",
		Endpoint: "port-fwd",
		Version:  "v0.1.0",
		Refresh:  2 * time.Second,
		Now:      func() time.Time { return testNow },
		Tick: func(d time.Duration, _ func(time.Time) tea.Msg) tea.Cmd {
			h.ticks = append(h.ticks, d)
			return nil
		},
		Theme:         &theme,
		Marshal:       testMarshal,
		Editable:      testEditable,
		EditFile:      testMarshal,
		Apply:         testApply,
		Annotate:      testAnnotate,
		EditorCommand: func(path string) *exec.Cmd { return exec.Command("fake-editor", path) },
		Exec: func(c *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
			return func() tea.Msg {
				path := c.Args[len(c.Args)-1]
				b, _ := os.ReadFile(path)
				h.editSeen = append(h.editSeen, b)
				h.editorN++
				var err error
				if h.editor != nil {
					err = h.editor(path, h.editorN)
				}
				return cb(err)
			}
		},
		TempDir: t.TempDir(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	h.app = New(cfg)
	h.run(h.app.Init())
	h.send(tea.WindowSizeMsg{Width: 120, Height: 40})
	return h
}

// send feeds messages to the app and runs the returned commands.
func (h *harness) send(msgs ...tea.Msg) *harness {
	h.t.Helper()
	for _, m := range msgs {
		_, cmd := h.app.Update(m)
		h.run(cmd)
	}
	return h
}

// run executes cmd and every command that follows from it.
func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; n++ {
		if n > 10000 {
			h.t.Fatal("command loop did not settle")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.QuitMsg:
			h.quit = true
		default:
			_, next := h.app.Update(msg)
			queue = append(queue, next)
		}
	}
}

func (h *harness) keys(s string) *harness {
	h.t.Helper()
	for _, r := range s {
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return h
}

func (h *harness) press(code rune, mod ...tea.KeyMod) *harness {
	h.t.Helper()
	k := tea.KeyPressMsg{Code: code}
	for _, m := range mod {
		k.Mod |= m
	}
	return h.send(k)
}

func (h *harness) enter() *harness { return h.press(tea.KeyEnter) }
func (h *harness) esc() *harness   { return h.press(tea.KeyEscape) }

// tick fires the pending poll of the active view.
func (h *harness) tick() *harness {
	h.t.Helper()
	return h.send(tickMsg{view: h.app.top().id(), gen: h.app.pollGen})
}

func (h *harness) table() *tableView {
	h.t.Helper()
	tv, ok := h.app.top().(*tableView)
	if !ok {
		h.t.Fatalf("top view is %T, want *tableView", h.app.top())
	}
	return tv
}

func (h *harness) visibleRowNames() []string {
	h.t.Helper()
	tv := h.table()
	nameCol := slices.Index(testColumns[tv.kind], "NAME")
	if nameCol < 0 {
		nameCol = 0
	}
	var out []string
	for _, r := range tv.visible {
		out = append(out, r.Cells[nameCol])
	}
	return out
}

func (h *harness) selectedName() string {
	h.t.Helper()
	o, _ := h.table().selected()
	return o.Name
}

func (h *harness) crumbs() []string {
	var out []string
	for _, v := range h.app.stack {
		out = append(out, v.crumbs()...)
	}
	return out
}

func (h *harness) flash() string { return h.app.flash.text }

func (h *harness) screen() string {
	return style.Downsample(h.app.render(), colorprofile.NoTTY)
}

func (h *harness) hintKeys() []string {
	var out []string
	for _, b := range h.app.headerHints() {
		out = append(out, b.Help().Key)
	}
	return out
}
