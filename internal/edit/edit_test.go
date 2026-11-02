package edit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.ate.dev/8top/internal/model"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	atev1 "go.ate.dev/substrate/api/infra/k8s/ate/v1"
)

// Environment variables that turn the test binary into a scripted editor.
// See TestMain.
const (
	fakeEditorEnv    = "EDIT_TEST_FAKE_EDITOR"
	fakeEditorOldEnv = "EDIT_TEST_FAKE_EDITOR_OLD"
	fakeEditorNewEnv = "EDIT_TEST_FAKE_EDITOR_NEW"
)

// TestMain lets the test binary act as $EDITOR. When fakeEditorEnv is set,
// it replaces the text in fakeEditorOldEnv with fakeEditorNewEnv in the file
// named by its last argument and exits, instead of running the tests.
func TestMain(m *testing.M) {
	if os.Getenv(fakeEditorEnv) != "" {
		os.Exit(fakeEditor(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeEditor(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fake editor: no file")
		return 2
	}
	path := args[len(args)-1]
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake editor:", err)
		return 2
	}
	old, repl := os.Getenv(fakeEditorOldEnv), os.Getenv(fakeEditorNewEnv)
	if !bytes.Contains(b, []byte(old)) {
		fmt.Fprintf(os.Stderr, "fake editor: %q not in %s\n", old, path)
		return 3
	}
	b = bytes.Replace(b, []byte(old), []byte(repl), 1)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "fake editor:", err)
		return 2
	}
	return 0
}

// fakeAPI records Update calls. Other methods panic (nil embedded API).
type fakeAPI struct {
	model.API
	calls []model.Object
	err   error
}

func (f *fakeAPI) Update(_ context.Context, o model.Object) (model.Object, error) {
	f.calls = append(f.calls, o)
	if f.err != nil {
		return model.Object{}, f.err
	}
	return o, nil
}

func meta(atespace, name string) *ateapi.ResourceMetadata {
	return &ateapi.ResourceMetadata{Atespace: atespace, Name: name, Uid: "uid-1", Version: 7}
}

func actorObj() model.Object {
	return model.Object{
		Kind: model.KindActor, Key: "default/counter", Atespace: "default", Name: "counter",
		Raw: &ateapi.Actor{
			Metadata:       meta("default", "counter"),
			ActorTemplate:  &ateapi.ObjectRef{Atespace: "default", Name: "tmpl-v1"},
			WorkerSelector: &ateapi.Selector{MatchLabels: map[string]string{"pool": "a"}},
			SourceTag:      &ateapi.ObjectRef{Atespace: "default", Name: "tag-1"},
			Status:         &ateapi.ActorStatus{State: ateapi.ActorState_ACTOR_STATE_SUSPENDED},
		},
	}
}

func tagObj() model.Object {
	return model.Object{
		Kind: model.KindTag, Key: "default/tag-1", Atespace: "default", Name: "tag-1",
		Raw: &ateapi.Tag{
			Metadata:    meta("default", "tag-1"),
			Scope:       ateapi.TagScope_TAG_SCOPE_ATESPACE,
			SourceActor: &ateapi.ObjectRef{Atespace: "default", Name: "counter"},
		},
	}
}

func egressObj() model.Object {
	return model.Object{
		Kind: model.KindEgressPolicy, Key: "default/counter", Atespace: "default", Name: "counter",
		Raw: &ateapi.EgressPolicy{
			Metadata: meta("default", "counter"),
			Rules:    []*ateapi.EgressRule{{Http: &ateapi.HttpRule{Hostnames: []string{"example.com"}}}},
		},
	}
}

func accessObj() model.Object {
	return model.Object{
		Kind: model.KindAccessPolicy, Key: "default", Atespace: "default", Name: "default",
		Raw: &ateapi.AccessPolicy{
			Metadata: meta("default", "default"),
			Bindings: []*ateapi.Binding{{Role: "viewer", Members: []string{"alice"}}},
		},
	}
}

// editFile returns EditFile(o) with old replaced by repl, failing if old is
// not present.
func editFile(t *testing.T, o model.Object, old, repl string) []byte {
	t.Helper()
	b, err := EditFile(o)
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	if old == "" {
		return b
	}
	if !bytes.Contains(b, []byte(old)) {
		t.Fatalf("EditFile() output has no %q:\n%s", old, b)
	}
	return bytes.Replace(b, []byte(old), []byte(repl), 1)
}

func TestMarshal(t *testing.T) {
	got, err := Marshal(actorObj())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `actorTemplate:
  atespace: default
  name: tmpl-v1
metadata:
  atespace: default
  name: counter
  uid: uid-1
  version: "7"
sourceTag:
  atespace: default
  name: tag-1
status:
  state: ACTOR_STATE_SUSPENDED
workerSelector:
  matchLabels:
    pool: a
`
	if string(got) != want {
		t.Errorf("Marshal() =\n%s\nwant\n%s", got, want)
	}

	ef, err := EditFile(actorObj())
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	if string(ef) != Header+want {
		t.Errorf("EditFile() =\n%s\nwant Header + Marshal()", ef)
	}
}

func TestMarshalWorkerPool(t *testing.T) {
	o := model.Object{Kind: model.KindWorkerPool, Name: "pool-a", Raw: &atev1.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-a", Namespace: "ate-system"},
	}}
	got, err := Marshal(o)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, want := range []string{"name: pool-a", "namespace: ate-system"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("Marshal() = %s, want it to contain %q", got, want)
		}
	}
}

func TestEditable(t *testing.T) {
	for _, tc := range []struct {
		kind model.Kind
		want bool
	}{
		{model.KindActor, true},
		{model.KindTag, true},
		{model.KindEgressPolicy, true},
		{model.KindAccessPolicy, true},
		{model.KindActorTemplate, false},
		{model.KindAtespace, false},
		{model.KindWorker, false},
		{model.KindActorAssignment, false},
		{model.KindWorkerPool, false},
	} {
		got, reason := Editable(tc.kind)
		if got != tc.want {
			t.Errorf("Editable(%q) = %v, want %v", tc.kind, got, tc.want)
		}
		if !got && reason == "" {
			t.Errorf("Editable(%q) gave no reason", tc.kind)
		}
	}
	if _, reason := Editable(model.KindActorTemplate); !strings.Contains(reason, "kubectl ate update actor --template") {
		t.Errorf("Editable(templates) reason = %q, want a hint to repoint actors", reason)
	}
}

func TestApplyNoChange(t *testing.T) {
	orig := actorObj()
	file, err := EditFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	yml, _ := Marshal(orig)
	for _, tc := range []struct {
		name   string
		edited []byte
	}{
		{"unchanged", file},
		{"header removed", yml},
		{"after error annotation", Annotate(file, errors.New("boom"))},
		{"empty", nil},
		{"whitespace", []byte("\n  \n")},
		{"only comments", []byte(Header + "# more\n")},
		{"reformatted", bytes.Replace(file, []byte("name: counter"), []byte("name: 'counter'"), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			_, changed, err := Apply(context.Background(), api, orig, tc.edited)
			if err != nil || changed {
				t.Errorf("Apply() = changed %v, err %v; want false, nil", changed, err)
			}
			if len(api.calls) != 0 {
				t.Errorf("Update called %d times, want 0", len(api.calls))
			}
		})
	}
}

func TestApplyValid(t *testing.T) {
	for _, tc := range []struct {
		name      string
		orig      model.Object
		old, repl string
		check     func(t *testing.T, raw any)
	}{
		{
			name: "actor template", orig: actorObj(), old: "name: tmpl-v1", repl: "name: tmpl-v2",
			check: func(t *testing.T, raw any) {
				if got, want := raw.(*ateapi.Actor).GetActorTemplate().GetName(), "tmpl-v2"; got != want {
					t.Errorf("actor_template.name = %q, want %q", got, want)
				}
			},
		},
		{
			name: "actor worker selector", orig: actorObj(), old: "pool: a", repl: "pool: b",
			check: func(t *testing.T, raw any) {
				got := raw.(*ateapi.Actor).GetWorkerSelector().GetMatchLabels()
				if want := map[string]string{"pool": "b"}; !reflect.DeepEqual(got, want) {
					t.Errorf("worker_selector.match_labels = %v, want %v", got, want)
				}
			},
		},
		{
			name: "tag scope", orig: tagObj(), old: "TAG_SCOPE_ATESPACE", repl: "TAG_SCOPE_PUBLISHED",
			check: func(t *testing.T, raw any) {
				if got, want := raw.(*ateapi.Tag).GetScope(), ateapi.TagScope_TAG_SCOPE_PUBLISHED; got != want {
					t.Errorf("scope = %v, want %v", got, want)
				}
			},
		},
		{
			name: "egress rules", orig: egressObj(), old: "- example.com", repl: "- example.com\n    - api.example.com",
			check: func(t *testing.T, raw any) {
				got := raw.(*ateapi.EgressPolicy).GetRules()[0].GetHttp().GetHostnames()
				if want := []string{"example.com", "api.example.com"}; !reflect.DeepEqual(got, want) {
					t.Errorf("hostnames = %v, want %v", got, want)
				}
			},
		},
		{
			name: "access bindings", orig: accessObj(), old: "role: viewer", repl: "role: editor",
			check: func(t *testing.T, raw any) {
				if got, want := raw.(*ateapi.AccessPolicy).GetBindings()[0].GetRole(), "editor"; got != want {
					t.Errorf("role = %q, want %q", got, want)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			origRaw := proto.Clone(tc.orig.Raw.(proto.Message))
			got, changed, err := Apply(context.Background(), api, tc.orig, editFile(t, tc.orig, tc.old, tc.repl))
			if err != nil || !changed {
				t.Fatalf("Apply() = changed %v, err %v; want true, nil", changed, err)
			}
			if len(api.calls) != 1 {
				t.Fatalf("Update called %d times, want 1", len(api.calls))
			}
			call := api.calls[0]
			if call.Kind != tc.orig.Kind || call.Key != tc.orig.Key || call.Name != tc.orig.Name {
				t.Errorf("Update object = %+v, want identity of %+v", call, tc.orig)
			}
			if m := call.Raw.(interface {
				GetMetadata() *ateapi.ResourceMetadata
			}).GetMetadata(); m.GetUid() != "uid-1" || m.GetVersion() != 7 {
				t.Errorf("Update metadata = %v, want uid-1 version 7", m)
			}
			tc.check(t, call.Raw)
			if got.Raw != call.Raw {
				t.Errorf("Apply() did not return the Update result")
			}
			if !proto.Equal(tc.orig.Raw.(proto.Message), origRaw) {
				t.Errorf("Apply() modified the original object")
			}
		})
	}
}

func TestApplyErrors(t *testing.T) {
	aborted := status.Error(codes.Aborted, "version mismatch")
	for _, tc := range []struct {
		name      string
		orig      model.Object
		old, repl string
		raw       string // if set, the edited file, instead of EditFile with old replaced
		apiErr    error
		wantErr   string // substring
		wantCalls int
	}{
		{name: "actor status", orig: actorObj(), old: "ACTOR_STATE_SUSPENDED", repl: "ACTOR_STATE_RUNNING", wantErr: `"status.state" is read-only`},
		{name: "actor source tag", orig: actorObj(), old: "name: tag-1", repl: "name: tag-2", wantErr: `"sourceTag.name" is read-only`},
		{name: "metadata name", orig: actorObj(), old: "name: counter", repl: "name: other", wantErr: `"metadata.name" is read-only`},
		{name: "metadata atespace", orig: tagObj(), old: "  atespace: default\n  name: tag-1", repl: "  atespace: prod\n  name: tag-1", wantErr: `"metadata.atespace" is read-only`},
		{name: "metadata uid", orig: egressObj(), old: "uid: uid-1", repl: "uid: uid-2", wantErr: `"metadata.uid" is read-only`},
		{name: "metadata version", orig: accessObj(), old: `version: "7"`, repl: `version: "8"`, wantErr: `"metadata.version" is read-only`},
		{name: "metadata removed", orig: egressObj(), old: "metadata:", repl: "xmetadata:", wantErr: `unknown field "xmetadata"`},
		{name: "tag source actor", orig: tagObj(), old: "name: counter", repl: "name: c2", wantErr: `"sourceActor.name" is read-only`},
		{name: "invalid YAML", orig: actorObj(), old: "pool: a", repl: "pool: [a", wantErr: "invalid YAML"},
		{name: "not a mapping", orig: actorObj(), raw: "- a\n- b\n", wantErr: "expected a YAML mapping"},
		{name: "unknown field", orig: actorObj(), old: "workerSelector:", repl: "workerSelecter:", wantErr: `unknown field "workerSelecter"`},
		{name: "bad enum", orig: tagObj(), old: "TAG_SCOPE_ATESPACE", repl: "TAG_SCOPE_GLOBAL", wantErr: "invalid Tag"},
		{name: "api aborted", orig: actorObj(), old: "pool: a", repl: "pool: b", apiErr: aborted, wantErr: "version mismatch", wantCalls: 1},
		{name: "template not editable", orig: model.Object{Kind: model.KindActorTemplate, Raw: &ateapi.ActorTemplate{}}, old: "", repl: "", wantErr: "immutable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{err: tc.apiErr}
			edited := []byte(tc.raw)
			if tc.raw == "" {
				edited = editFile(t, tc.orig, tc.old, tc.repl)
			}
			_, changed, err := Apply(context.Background(), api, tc.orig, edited)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Apply() error = %v, want it to contain %q", err, tc.wantErr)
			}
			if changed {
				t.Errorf("Apply() changed = true, want false")
			}
			if len(api.calls) != tc.wantCalls {
				t.Errorf("Update called %d times, want %d", len(api.calls), tc.wantCalls)
			}
			if tc.apiErr != nil {
				if !errors.Is(err, tc.apiErr) || status.Code(err) != codes.Aborted {
					t.Errorf("Apply() error = %v, want it to wrap %v with code Aborted", err, tc.apiErr)
				}
			}
		})
	}
}

func TestAnnotate(t *testing.T) {
	body := "metadata:\n  name: x\n"
	for _, tc := range []struct {
		name    string
		content string
		err     error
		want    string
	}{
		{
			name: "plain YAML", content: body, err: errors.New("boom"),
			want: Header + "# error: boom\n#\n" + body,
		},
		{
			name: "replaces header", content: Header + body, err: errors.New("boom"),
			want: Header + "# error: boom\n#\n" + body,
		},
		{
			name: "replaces previous error", content: Header + "# error: old\n#\n" + body, err: errors.New("new"),
			want: Header + "# error: new\n#\n" + body,
		},
		{
			name: "multi-line error", content: body, err: errors.New("one\ntwo"),
			want: Header + "# error: one\n#   two\n#\n" + body,
		},
		{
			name: "nil error clears", content: Header + "# error: old\n#\n" + body, err: nil,
			want: Header + body,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(Annotate([]byte(tc.content), tc.err))
			if got != tc.want {
				t.Errorf("Annotate() =\n%s\nwant\n%s", got, tc.want)
			}
			if again := string(Annotate([]byte(got), tc.err)); again != got {
				t.Errorf("Annotate() is not idempotent:\n%s\nthen\n%s", got, again)
			}
		})
	}
}

func TestEditorCommand(t *testing.T) {
	for _, tc := range []struct {
		name          string
		visual, edtor string
		want          []string
	}{
		{name: "default", want: []string{"vi", "/tmp/f.yaml"}},
		{name: "EDITOR", edtor: "nano", want: []string{"nano", "/tmp/f.yaml"}},
		{name: "VISUAL wins", visual: "emacs", edtor: "nano", want: []string{"emacs", "/tmp/f.yaml"}},
		{name: "blank VISUAL ignored", visual: "  ", edtor: "nano", want: []string{"nano", "/tmp/f.yaml"}},
		{name: "args", edtor: "code -w  --new-window", want: []string{"code", "-w", "--new-window", "/tmp/f.yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VISUAL", tc.visual)
			t.Setenv("EDITOR", tc.edtor)
			cmd := EditorCommand("/tmp/f.yaml")
			if !reflect.DeepEqual(cmd.Args, tc.want) {
				t.Errorf("EditorCommand().Args = %q, want %q", cmd.Args, tc.want)
			}
			if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
				t.Errorf("EditorCommand() set stdio")
			}
		})
	}
}

// TestEditorRoundTrip runs the whole flow with the test binary as $EDITOR:
// write the file, edit it, apply, and on error annotate and edit again.
func TestEditorRoundTrip(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", exe+" -fake-flag")
	t.Setenv(fakeEditorEnv, "1")

	orig := actorObj()
	path := filepath.Join(t.TempDir(), "actor.yaml")
	content, err := EditFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	runEditor := func(old, repl string) []byte {
		t.Helper()
		t.Setenv(fakeEditorOldEnv, old)
		t.Setenv(fakeEditorNewEnv, repl)
		if out, err := EditorCommand(path).CombinedOutput(); err != nil {
			t.Fatalf("editor: %v: %s", err, out)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// First attempt edits a read-only field.
	api := &fakeAPI{}
	edited := runEditor("ACTOR_STATE_SUSPENDED", "ACTOR_STATE_RUNNING")
	_, _, err = Apply(context.Background(), api, orig, edited)
	if err == nil {
		t.Fatal("Apply() of read-only edit succeeded")
	}
	if err := os.WriteFile(path, Annotate(edited, err), 0o600); err != nil {
		t.Fatal(err)
	}

	// Second attempt fixes it and makes a valid edit.
	edited = runEditor("ACTOR_STATE_RUNNING", "ACTOR_STATE_SUSPENDED")
	if !bytes.Contains(edited, []byte(`# error: field "status.state" is read-only`)) {
		t.Errorf("reopened file has no error comment:\n%s", edited)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	edited = runEditor("pool: a", "pool: b")
	_, changed, err := Apply(context.Background(), api, orig, edited)
	if err != nil || !changed {
		t.Fatalf("Apply() = changed %v, err %v; want true, nil", changed, err)
	}
	if len(api.calls) != 1 {
		t.Fatalf("Update called %d times, want 1", len(api.calls))
	}
	got := api.calls[0].Raw.(*ateapi.Actor).GetWorkerSelector().GetMatchLabels()["pool"]
	if got != "b" {
		t.Errorf("updated pool = %q, want %q", got, "b")
	}
}
