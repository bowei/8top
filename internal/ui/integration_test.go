package ui

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	poolsfake "go.ate.dev/substrate/pkg/client/clientset/versioned/fake"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"go.ate.dev/8top/internal/edit"
	"go.ate.dev/8top/internal/fake"
	"go.ate.dev/8top/internal/model"
	"go.ate.dev/8top/internal/render"
)

// Integration tests run the UI against the real Store, renderers and edit
// package, talking gRPC to the in-memory fake API server.

const (
	running   = ateapi.ActorState_ACTOR_STATE_RUNNING
	suspended = ateapi.ActorState_ACTOR_STATE_SUSPENDED
)

// newIntegration seeds srv with a small cluster and returns a harness wired
// to it. The harness's fakeAPI is unused.
func newIntegration(t *testing.T, srv *fake.Server, opts ...option) *harness {
	t.Helper()
	srv.AddWorker("ate", "pool-a", "pool-a-0")
	srv.AddWorker("ate", "pool-a", "pool-a-1")
	srv.AddTemplate("default", "counter")
	srv.AddActor("default", "counter-1", "counter", running, "pool-a-0")
	srv.AddActor("default", "counter-2", "counter", suspended, "")
	srv.AddEgressPolicy("default", "counter-1", fake.HTTPSRule("api.anthropic.com"))

	store := model.NewStore(srv.Start(t), kubefake.NewSimpleClientset(), poolsfake.NewSimpleClientset())
	real := func(c *Config) {
		c.API = store
		c.Renderer = render.For
		c.Marshal = edit.Marshal
		c.EditFile = edit.EditFile
		c.Editable = edit.Editable
		c.Apply = edit.Apply
		c.Annotate = edit.Annotate
	}
	return newHarness(t, newFakeAPI(), append([]option{real}, opts...)...)
}

// cell returns the selected row's cell in the named column.
func (h *harness) cell(col string) string {
	h.t.Helper()
	tv := h.table()
	cols := render.For(tv.kind).Columns()
	i := slices.IndexFunc(cols, func(c render.Column) bool { return c.Name == col })
	if i < 0 {
		h.t.Fatalf("no column %q in %v", col, tv.kind)
	}
	if tv.cursor < 0 || tv.cursor >= len(tv.visible) {
		h.t.Fatalf("no selected row")
	}
	return tv.visible[tv.cursor].Cells[i]
}

func TestIntegrationListAndPaging(t *testing.T) {
	srv := fake.New(fake.WithPageSize(1))
	h := newIntegration(t, srv, withAtespace("default"))
	if got, want := h.visibleRowNames(), []string{"counter-1", "counter-2"}; !slices.Equal(got, want) {
		t.Errorf("actors = %v, want %v", got, want)
	}
	if got := srv.Methods(); !slices.Contains(got, "ListActors") {
		t.Errorf("fake calls = %v, want ListActors", got)
	}
	if got := h.cell("STATE"); got != "RUNNING" {
		t.Errorf("STATE = %q, want RUNNING", got)
	}
	if got := h.cell("WORKER POD"); got != "ate/pool-a-0" {
		t.Errorf("WORKER POD = %q, want ate/pool-a-0", got)
	}
}

func TestIntegrationSuspend(t *testing.T) {
	srv := fake.New()
	h := newIntegration(t, srv, withAtespace("default"))
	h.keys("sy")
	if got := srv.Actor("default", "counter-1").GetStatus().GetState(); got != suspended {
		t.Fatalf("fake actor state = %v, want SUSPENDED", got)
	}
	if got := h.cell("STATE"); got != "SUSPENDED" {
		t.Errorf("STATE after refresh = %q, want SUSPENDED", got)
	}
	if got := h.cell("WORKER POD"); got != "" {
		t.Errorf("WORKER POD = %q, want empty", got)
	}
}

func TestIntegrationResumeAcrossRefreshes(t *testing.T) {
	srv := fake.New(fake.WithAsync())
	h := newIntegration(t, srv, withAtespace("default"))
	h.keys("j") // counter-2, suspended
	if got := h.selectedName(); got != "counter-2" {
		t.Fatalf("selected = %q, want counter-2", got)
	}
	h.keys("a")
	if got := h.cell("STATE"); got != "RESUMING" {
		t.Errorf("STATE after resume = %q, want RESUMING", got)
	}
	srv.Settle()
	h.tick()
	if got := h.cell("STATE"); got != "RUNNING" {
		t.Errorf("STATE after settle = %q, want RUNNING", got)
	}
	if got := h.selectedName(); got != "counter-2" {
		t.Errorf("selection moved to %q", got)
	}
}

func TestIntegrationListErrorRecovers(t *testing.T) {
	srv := fake.New()
	h := newIntegration(t, srv, withAtespace("default"))
	srv.FailNext("ListActors", status.Error(codes.Unavailable, "connection refused"))
	h.tick()
	if got := h.flash(); !strings.Contains(got, "Unavailable: connection refused") {
		t.Errorf("flash = %q, want the Unavailable error", got)
	}
	if got := len(h.visibleRowNames()); got != 2 {
		t.Errorf("rows after failed refresh = %d, want the previous 2", got)
	}
	srv.AddActor("default", "counter-3", "counter", suspended, "")
	h.tick()
	if got := len(h.visibleRowNames()); got != 3 {
		t.Errorf("rows after recovery = %d, want 3", got)
	}
}

func TestIntegrationWorkerDrillDown(t *testing.T) {
	srv := fake.New()
	h := newIntegration(t, srv, withCommand("workers"))
	h.enter() // pool-a-0 hosts counter-1
	if got, want := h.table().kind, model.KindActorAssignment; got != want {
		t.Fatalf("view = %v, want %v", got, want)
	}
	if got := h.cell("ACTOR"); got != "counter-1" {
		t.Errorf("assigned actor = %q, want counter-1", got)
	}
}

func TestIntegrationEditEgressPolicy(t *testing.T) {
	srv := fake.New()
	h := newIntegration(t, srv, withAtespace("default"))
	h.editor = func(p string, _ int) error {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(p, bytes.ReplaceAll(b, []byte("api.anthropic.com"), []byte("example.com")), 0o600)
	}
	h.keys("e") // counter-1's egress policy
	if got, want := h.table().kind, model.KindEgressPolicy; got != want {
		t.Fatalf("view = %v, want %v", got, want)
	}
	h.keys("E")
	if got := h.editorN; got != 1 {
		t.Fatalf("editor opened %d times, want 1", got)
	}
	got := srv.EgressPolicy("default", "counter-1").GetRules()[0].GetHttps().GetHostnames()
	if want := []string{"example.com"}; !slices.Equal(got, want) {
		t.Errorf("hostnames = %v, want %v (flash %q)", got, want, h.flash())
	}
	if v := srv.EgressPolicy("default", "counter-1").GetMetadata().GetVersion(); v < 2 {
		t.Errorf("version = %d, want bumped", v)
	}
}

func TestIntegrationEditConflict(t *testing.T) {
	srv := fake.New()
	h := newIntegration(t, srv, withAtespace("default"))
	h.editor = func(p string, n int) error {
		if n > 1 {
			return nil // give up after seeing the error
		}
		// Someone else replaces the policy while the editor is open.
		srv.AddEgressPolicy("default", "counter-1", fake.HTTPSRule("other.example"))
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(p, bytes.ReplaceAll(b, []byte("api.anthropic.com"), []byte("example.com")), 0o600)
	}
	h.keys("eE")
	if got := h.editorN; got != 2 {
		t.Fatalf("editor opened %d times, want 2 (reopen after conflict)", got)
	}
	if got := string(h.editSeen[1]); !strings.Contains(got, "# error: ") || !strings.Contains(got, "Aborted") {
		t.Errorf("reopened file has no Aborted error comment:\n%s", got)
	}
	if got := h.flash(); !strings.Contains(got, "Aborted") {
		t.Errorf("flash = %q, want Aborted", got)
	}
	got := srv.EgressPolicy("default", "counter-1").GetRules()[0].GetHttps().GetHostnames()
	if want := []string{"other.example"}; !slices.Equal(got, want) {
		t.Errorf("hostnames = %v, want the concurrent writer's %v", got, want)
	}
}
