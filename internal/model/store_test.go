package model

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"go.ate.dev/8top/internal/fake"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	atev1 "go.ate.dev/substrate/api/infra/k8s/ate/v1"
	poolsfake "go.ate.dev/substrate/pkg/client/clientset/versioned/fake"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const (
	running   = ateapi.ActorState_ACTOR_STATE_RUNNING
	suspended = ateapi.ActorState_ACTOR_STATE_SUSPENDED
)

// newFixture seeds a small cluster with a page size of 2, so every List
// with more than two items pages.
func newFixture(t *testing.T) (*fake.Server, *Store) {
	t.Helper()
	srv := fake.New(fake.WithPageSize(2))
	srv.AddWorker("ate", "pool-a", "pool-a-0")
	srv.AddWorker("ate", "pool-a", "pool-a-1")
	srv.AddWorker("ate", "pool-b", "pool-b-0")
	srv.AddTemplate("default", "counter")
	srv.AddTemplate("default", "counter-v2")
	srv.AddTemplate("other", "web")
	srv.AddActor("default", "counter-1", "counter", running, "pool-a-0")
	srv.AddActor("default", "counter-2", "counter", suspended, "")
	srv.AddActor("default", "counter-3", "counter", running, "pool-a-0")
	srv.AddActor("other", "web-1", "web", running, "pool-a-1")
	srv.AddTag("default", "golden", "counter-2", ateapi.TagScope_TAG_SCOPE_ATESPACE)
	srv.AddTag("other", "web-golden", "", ateapi.TagScope_TAG_SCOPE_PUBLISHED)
	srv.AddEgressPolicy("default", "counter-1", fake.HTTPSRule("example.com"))
	srv.AddEgressPolicy("default", "counter-3", fake.HTTPSRule("example.org"))
	srv.AddEgressPolicy("other", "web-1")
	srv.SetAccessPolicy("other", &ateapi.Binding{Role: "viewer", Members: []string{"user:b"}})
	srv.AddAtespace("empty")

	pools := poolsfake.NewSimpleClientset(
		&atev1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Namespace: "ate", Name: "pool-a"}, Spec: atev1.WorkerPoolSpec{Replicas: 2}},
		&atev1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Namespace: "ate", Name: "pool-b"}, Spec: atev1.WorkerPoolSpec{Replicas: 1}},
		&atev1.WorkerPool{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "pool-c"}},
	)
	return srv, NewStore(srv.Start(t), kubefake.NewSimpleClientset(), pools)
}

func keys(objs []Object) []string {
	var out []string
	for _, o := range objs {
		out = append(out, o.Key)
	}
	return out
}

func TestStoreList(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		kind  Kind
		scope Scope
		want  []string
	}{
		{KindAtespace, Scope{}, []string{"default", "empty", "other"}},
		{KindActor, Scope{}, []string{"default/counter-1", "default/counter-2", "default/counter-3", "other/web-1"}},
		{KindActor, Scope{Atespace: "default"}, []string{"default/counter-1", "default/counter-2", "default/counter-3"}},
		{KindActor, Scope{Atespace: "empty"}, nil},
		{KindActorTemplate, Scope{}, []string{"default/counter", "default/counter-v2", "other/web"}},
		{KindActorTemplate, Scope{Atespace: "other"}, []string{"other/web"}},
		{KindTag, Scope{}, []string{"default/golden", "other/web-golden"}},
		{KindTag, Scope{Atespace: "default"}, []string{"default/golden"}},
		{KindEgressPolicy, Scope{}, []string{"default/counter-1", "default/counter-3", "other/web-1"}},
		{KindEgressPolicy, Scope{Atespace: "default"}, []string{"default/counter-1", "default/counter-3"}},
		{KindEgressPolicy, Scope{Atespace: "default", Actor: "counter-3"}, []string{"default/counter-3"}},
		{KindEgressPolicy, Scope{Atespace: "default", Actor: "counter-2"}, nil},
		{KindWorker, Scope{}, []string{"pool-a-0", "pool-a-1", "pool-b-0"}},
		{KindActorAssignment, Scope{Worker: "pool-a-0"}, nil}, // checked by name below
		{KindActorAssignment, Scope{Worker: "pool-b-0"}, nil},
		{KindAccessPolicy, Scope{}, nil},
		{KindAccessPolicy, Scope{Atespace: "default"}, nil},
		{KindAccessPolicy, Scope{Atespace: "other"}, []string{"other"}},
		{KindWorkerPool, Scope{}, []string{"ate/pool-a", "ate/pool-b", "team/pool-c"}},
		{KindWorkerPool, Scope{Namespace: "ate"}, []string{"ate/pool-a", "ate/pool-b"}},
	} {
		got, err := s.List(ctx, tc.kind, tc.scope)
		if err != nil {
			t.Errorf("List(%s, %+v): %v", tc.kind, tc.scope, err)
			continue
		}
		for _, o := range got {
			if o.Kind != tc.kind || o.Raw == nil {
				t.Errorf("List(%s, %+v): bad object %+v", tc.kind, tc.scope, o)
			}
		}
		if tc.kind == KindActorAssignment {
			continue
		}
		if k := keys(got); !slices.Equal(k, tc.want) {
			t.Errorf("List(%s, %+v) = %v, want %v", tc.kind, tc.scope, k, tc.want)
		}
	}
}

func TestStoreListAssignments(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		worker string
		want   []string
	}{
		{"pool-a-0", []string{"default/counter-1", "default/counter-3"}},
		{"pool-b-0", nil},
		{"", []string{"default/counter-1", "default/counter-3", "other/web-1"}},
	} {
		objs, err := s.List(ctx, KindActorAssignment, Scope{Worker: tc.worker})
		if err != nil {
			t.Fatalf("List(assignments, %q): %v", tc.worker, err)
		}
		var got []string
		for _, o := range objs {
			got = append(got, o.Atespace+"/"+o.Name)
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("List(assignments, %q) = %v, want %v", tc.worker, got, tc.want)
		}
	}
	_, err := s.List(ctx, KindActorAssignment, Scope{Worker: "nope"})
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("unknown worker: code = %v, want %v", got, want)
	}
}

func TestStoreListPagesAndErrors(t *testing.T) {
	srv, s := newFixture(t)
	ctx := context.Background()
	srv.ResetCalls()
	if _, err := s.List(ctx, KindActor, Scope{}); err != nil {
		t.Fatal(err)
	}
	if got, want := srv.Methods(), []string{"ListActors", "ListActors"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}

	srv.FailNext("ListActors", status.Error(codes.Unavailable, "connection refused"))
	_, err := s.List(ctx, KindActor, Scope{})
	if got, want := err.Error(), "Unavailable: connection refused"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	if got, want := status.Code(err), codes.Unavailable; got != want {
		t.Errorf("code = %v, want %v", got, want)
	}

	_, err = s.List(ctx, KindActor, Scope{Atespace: "nope"})
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("unknown atespace: code = %v, want %v", got, want)
	}
}

func TestStoreGet(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	for _, kind := range []Kind{KindAtespace, KindActor, KindActorTemplate, KindTag, KindEgressPolicy, KindWorker, KindActorAssignment, KindAccessPolicy, KindWorkerPool} {
		scope := Scope{Atespace: "other"}
		if kind == KindAtespace || kind == KindWorker || kind == KindWorkerPool {
			scope = Scope{}
		}
		objs, err := s.List(ctx, kind, scope)
		if err != nil || len(objs) == 0 {
			t.Fatalf("List(%s): %v, %d objects", kind, err, len(objs))
		}
		got, err := s.Get(ctx, objs[0])
		if err != nil {
			t.Errorf("Get(%s %s): %v", kind, objs[0].Key, err)
			continue
		}
		if got.Key != objs[0].Key || got.Name != objs[0].Name || got.Atespace != objs[0].Atespace {
			t.Errorf("Get(%s) = %+v, want %+v", kind, got, objs[0])
		}
		if m, ok := got.Raw.(proto.Message); ok && !proto.Equal(m, objs[0].Raw.(proto.Message)) {
			t.Errorf("Get(%s) raw = %v, want %v", kind, m, objs[0].Raw)
		}
	}
	_, err := s.Get(ctx, Object{Kind: KindActor, Atespace: "default", Name: "nope"})
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("Get missing: code = %v, want %v", got, want)
	}
}

func TestStoreUpdate(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		kind     Kind
		scope    Scope
		key      string
		edit     func(raw any)
		wantCode codes.Code
		wantMsg  string
	}{
		{"actor template while suspended", KindActor, Scope{Atespace: "default"}, "default/counter-2", func(raw any) {
			raw.(*ateapi.Actor).ActorTemplate.Name = "counter-v2"
		}, codes.OK, ""},
		{"actor template while running", KindActor, Scope{Atespace: "default"}, "default/counter-1", func(raw any) {
			raw.(*ateapi.Actor).ActorTemplate.Name = "counter-v2"
		}, codes.FailedPrecondition, "FailedPrecondition: actor must be SUSPENDED to change its template (it is RUNNING)"},
		{"actor conflict", KindActor, Scope{Atespace: "default"}, "default/counter-1", func(raw any) {
			raw.(*ateapi.Actor).Metadata.Version++
		}, codes.Aborted, ""},
		{"tag scope", KindTag, Scope{Atespace: "default"}, "default/golden", func(raw any) {
			raw.(*ateapi.Tag).Scope = ateapi.TagScope_TAG_SCOPE_PUBLISHED
		}, codes.OK, ""},
		{"egress rules", KindEgressPolicy, Scope{Atespace: "default"}, "default/counter-1", func(raw any) {
			raw.(*ateapi.EgressPolicy).Rules = nil
		}, codes.OK, ""},
		{"egress conflict", KindEgressPolicy, Scope{Atespace: "default"}, "default/counter-1", func(raw any) {
			raw.(*ateapi.EgressPolicy).Metadata.Uid = "stale"
		}, codes.Aborted, ""},
		{"atespace access policy", KindAccessPolicy, Scope{Atespace: "other"}, "other", func(raw any) {
			raw.(*ateapi.AccessPolicy).Bindings = nil
		}, codes.OK, ""},
		{"worker not editable", KindWorker, Scope{}, "pool-a-0", func(any) {}, codes.InvalidArgument, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := newFixture(t)
			objs, err := s.List(ctx, tc.kind, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(objs, func(o Object) bool { return o.Key == tc.key })
			if i < 0 {
				t.Fatalf("%s not in %v", tc.key, keys(objs))
			}
			o := objs[i]
			tc.edit(o.Raw)
			got, err := s.Update(ctx, o)
			if code := status.Code(err); code != tc.wantCode {
				t.Fatalf("Update code = %v (%v), want %v", code, err, tc.wantCode)
			}
			if err != nil {
				if !strings.HasPrefix(err.Error(), tc.wantCode.String()+": ") {
					t.Errorf("error %q lacks code prefix", err)
				}
				if tc.wantMsg != "" && err.Error() != tc.wantMsg {
					t.Errorf("error = %q, want %q", err, tc.wantMsg)
				}
				return
			}
			if got.Key != o.Key {
				t.Errorf("key = %q, want %q", got.Key, o.Key)
			}
			if v := got.Raw.(interface {
				GetMetadata() *ateapi.ResourceMetadata
			}).GetMetadata().GetVersion(); v != 2 {
				t.Errorf("version = %d, want 2", v)
			}
		})
	}
}

func TestStoreGlobalAccessPolicy(t *testing.T) {
	srv, s := newFixture(t)
	ctx := context.Background()
	srv.SetAccessPolicy("", &ateapi.Binding{Role: "owner", Members: []string{"user:a"}})
	objs, err := s.List(ctx, KindAccessPolicy, Scope{})
	if err != nil || len(objs) != 1 {
		t.Fatalf("List: %v, %d objects", err, len(objs))
	}
	o := objs[0]
	if o.Key != "global" || o.Atespace != "" || o.Name != "default" {
		t.Errorf("object = %+v", o)
	}
	o.Raw.(*ateapi.AccessPolicy).Bindings = append(o.Raw.(*ateapi.AccessPolicy).Bindings, &ateapi.Binding{Role: "viewer", Members: []string{"user:c"}})
	if _, err := s.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	if got, want := len(srv.AccessPolicy("").GetBindings()), 2; got != want {
		t.Errorf("bindings = %d, want %d", got, want)
	}
	if err := s.Delete(ctx, o); status.Code(err) != codes.InvalidArgument {
		t.Errorf("Delete global policy: err = %v, want InvalidArgument", err)
	}
}

func TestStoreDelete(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		kind     Kind
		scope    Scope
		key      string
		wantCode codes.Code
	}{
		{KindActor, Scope{Atespace: "default"}, "default/counter-2", codes.OK},
		{KindTag, Scope{Atespace: "default"}, "default/golden", codes.OK},
		{KindEgressPolicy, Scope{Atespace: "default"}, "default/counter-1", codes.OK},
		{KindActorTemplate, Scope{Atespace: "default"}, "default/counter-v2", codes.OK},
		{KindAccessPolicy, Scope{Atespace: "other"}, "other", codes.OK},
		{KindAtespace, Scope{}, "empty", codes.OK},
		{KindAtespace, Scope{}, "default", codes.FailedPrecondition},
		{KindWorkerPool, Scope{}, "team/pool-c", codes.OK},
		{KindWorker, Scope{}, "pool-a-0", codes.InvalidArgument},
	} {
		t.Run(string(tc.kind)+"/"+tc.key, func(t *testing.T) {
			_, s := newFixture(t)
			objs, err := s.List(ctx, tc.kind, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(objs, func(o Object) bool { return o.Key == tc.key })
			if i < 0 {
				t.Fatalf("%s not in %v", tc.key, keys(objs))
			}
			err = s.Delete(ctx, objs[i])
			if code := status.Code(err); code != tc.wantCode {
				t.Fatalf("Delete code = %v (%v), want %v", code, err, tc.wantCode)
			}
			after, err := s.List(ctx, tc.kind, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			if gone := !slices.Contains(keys(after), tc.key); gone != (tc.wantCode == codes.OK) {
				t.Errorf("after Delete: keys = %v", keys(after))
			}
		})
	}
}

func TestStoreActions(t *testing.T) {
	ctx := context.Background()
	state := func(srv *fake.Server, name string) ateapi.ActorState {
		return srv.Actor("default", name).GetStatus().GetState()
	}

	t.Run("resume", func(t *testing.T) {
		srv, s := newFixture(t)
		if err := s.ResumeActor(ctx, "default", "counter-2"); err != nil {
			t.Fatal(err)
		}
		if got := state(srv, "counter-2"); got != running {
			t.Errorf("state = %v, want RUNNING", got)
		}
	})
	t.Run("resume async settles across refreshes", func(t *testing.T) {
		srv, s := newFixture(t)
		srv.SetAsync(true)
		if err := s.ResumeActor(ctx, "default", "counter-2"); err != nil {
			t.Fatal(err)
		}
		o, err := s.Get(ctx, Object{Kind: KindActor, Atespace: "default", Name: "counter-2"})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := o.Raw.(*ateapi.Actor).GetStatus().GetState(), ateapi.ActorState_ACTOR_STATE_RESUMING; got != want {
			t.Errorf("state = %v, want %v", got, want)
		}
		srv.Settle()
		o, err = s.Get(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if got := o.Raw.(*ateapi.Actor).GetStatus().GetState(); got != running {
			t.Errorf("state = %v, want RUNNING", got)
		}
	})
	for _, wait := range []bool{false, true} {
		t.Run("suspend", func(t *testing.T) {
			srv, s := newFixture(t)
			if err := s.SuspendActor(ctx, "default", "counter-1", wait); err != nil {
				t.Fatal(err)
			}
			if got := state(srv, "counter-1"); got != suspended {
				t.Errorf("state = %v, want SUSPENDED", got)
			}
			req := srv.LastRequest("SuspendActor").(*ateapi.SuspendActorRequest)
			want := ateapi.DurabilityMode_DURABILITY_MODE_ASYNCHRONOUS
			if wait {
				want = ateapi.DurabilityMode_DURABILITY_MODE_SYNCHRONOUS
			}
			if got := req.GetOptions().GetMode(); got != want {
				t.Errorf("mode = %v, want %v", got, want)
			}
		})
	}
	t.Run("revert", func(t *testing.T) {
		srv, s := newFixture(t)
		if err := s.RevertActor(ctx, "default", "counter-3"); err != nil {
			t.Fatal(err)
		}
		if got := state(srv, "counter-3"); got != suspended {
			t.Errorf("state = %v, want SUSPENDED", got)
		}
	})
	t.Run("create tag", func(t *testing.T) {
		srv, s := newFixture(t)
		if err := s.CreateTag(ctx, "default", "snap", "counter-2"); err != nil {
			t.Fatal(err)
		}
		if tag := srv.Tag("default", "snap"); tag.GetSourceActor().GetName() != "counter-2" {
			t.Errorf("tag = %v", tag)
		}
		err := s.CreateTag(ctx, "default", "snap2", "counter-1")
		if got, want := status.Code(err), codes.FailedPrecondition; got != want {
			t.Errorf("tag running actor: code = %v, want %v", got, want)
		}
	})
	t.Run("workers", func(t *testing.T) {
		srv, s := newFixture(t)
		ws := func() ateapi.WorkerState { return srv.Worker("pool-b-0").GetStatus().GetState() }
		if err := s.SetWorkerAvailable(ctx, "pool-b-0", false); err != nil {
			t.Fatal(err)
		}
		if got, want := ws(), ateapi.WorkerState_WORKER_STATE_UNAVAILABLE; got != want {
			t.Errorf("state = %v, want %v", got, want)
		}
		if err := s.SetWorkerAvailable(ctx, "pool-b-0", true); err != nil {
			t.Fatal(err)
		}
		if got, want := ws(), ateapi.WorkerState_WORKER_STATE_ACTIVE; got != want {
			t.Errorf("state = %v, want %v", got, want)
		}
		if err := s.DrainWorker(ctx, "pool-b-0"); err != nil {
			t.Fatal(err)
		}
		if got, want := ws(), ateapi.WorkerState_WORKER_STATE_DRAINING; got != want {
			t.Errorf("state = %v, want %v", got, want)
		}
		err := s.SetWorkerAvailable(ctx, "pool-b-0", true)
		if got, want := status.Code(err), codes.FailedPrecondition; got != want {
			t.Errorf("mark draining available: code = %v, want %v", got, want)
		}
	})
}

func TestStoreStreamLogs(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()

	_, err := s.StreamLogs(ctx, "default", "counter-2", false)
	if got, want := status.Code(err), codes.FailedPrecondition; got != want {
		t.Errorf("suspended actor: code = %v (%v), want %v", got, err, want)
	}
	if err != nil && !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want it to say the actor is not running", err)
	}

	// The fake clientset serves "fake logs", which is not an actor's JSON
	// line, so the filtered stream is empty.
	rc, err := s.StreamLogs(ctx, "default", "counter-1", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || len(b) != 0 {
		t.Errorf("logs = %q, %v; want empty", b, err)
	}
}

func TestFilterLogs(t *testing.T) {
	lines := []string{
		`{"message":"mine","labels":{"ate.atespace":"default","ate.actor.name":"a"}}`,
		`{"message":"other actor","labels":{"ate.atespace":"default","ate.actor.name":"b"}}`,
		`{"message":"other atespace","labels":{"ate.atespace":"x","ate.actor.name":"a"}}`,
		`{"message":"gcp","logging.googleapis.com/labels":{"ate.atespace":"default","ate.actor.name":"a"}}`,
		`{"message":"no labels"}`,
		`not json`,
	}
	rc := filterLogs(io.NopCloser(strings.NewReader(strings.Join(lines, "\n"))), "default", "a")
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{lines[0], lines[3]}
	if !slices.Equal(got, want) {
		t.Errorf("filtered = %q, want %q", got, want)
	}
}
