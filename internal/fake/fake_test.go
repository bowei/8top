package fake_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"go.ate.dev/8top/internal/fake"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	running   = ateapi.ActorState_ACTOR_STATE_RUNNING
	suspended = ateapi.ActorState_ACTOR_STATE_SUSPENDED
	crashed   = ateapi.ActorState_ACTOR_STATE_CRASHED
)

func ref(ns, name string) *ateapi.ObjectRef { return &ateapi.ObjectRef{Atespace: ns, Name: name} }

func TestPaging(t *testing.T) {
	srv := fake.New(fake.WithPageSize(2))
	for _, n := range []string{"e", "d", "c", "b", "a"} {
		srv.AddActor("default", n, "", suspended, "")
	}
	srv.AddActor("other", "z", "", suspended, "")
	c := srv.Start(t)
	ctx := context.Background()

	for _, tc := range []struct {
		atespace string
		want     []string
		pages    int
	}{
		{"default", []string{"a", "b", "c", "d", "e"}, 3},
		{"", []string{"a", "b", "c", "d", "e", "z"}, 3},
		{"other", []string{"z"}, 1},
	} {
		var got []string
		token, pages := "", 0
		for {
			r, err := c.ListActors(ctx, &ateapi.ListActorsRequest{Atespace: tc.atespace, Options: &ateapi.ListOptions{PageSize: 100, PageToken: token}})
			if err != nil {
				t.Fatalf("ListActors(%q): %v", tc.atespace, err)
			}
			pages++
			if len(r.GetActors()) > 2 {
				t.Errorf("page has %d items, want <= 2", len(r.GetActors()))
			}
			for _, a := range r.GetActors() {
				got = append(got, a.GetMetadata().GetName())
			}
			if token = r.GetNextPageToken(); token == "" {
				break
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("ListActors(%q) = %v, want %v", tc.atespace, got, tc.want)
		}
		if pages != tc.pages {
			t.Errorf("ListActors(%q) took %d pages, want %d", tc.atespace, pages, tc.pages)
		}
	}

	_, err := c.ListActors(ctx, &ateapi.ListActorsRequest{Options: &ateapi.ListOptions{PageToken: "bogus"}})
	if got, want := status.Code(err), codes.InvalidArgument; got != want {
		t.Errorf("bad token: code = %v, want %v", got, want)
	}
}

func TestAtespaceMustExist(t *testing.T) {
	srv := fake.New()
	srv.AddAtespace("default")
	c := srv.Start(t)
	ctx := context.Background()

	_, err := c.ListActors(ctx, &ateapi.ListActorsRequest{Atespace: "nope"})
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("ListActors(nope) code = %v, want %v", got, want)
	}
	_, err = c.CreateActor(ctx, &ateapi.CreateActorRequest{Actor: &ateapi.Actor{Metadata: &ateapi.ResourceMetadata{Atespace: "nope", Name: "a"}}})
	if got, want := status.Code(err), codes.NotFound; got != want {
		t.Errorf("CreateActor(nope/a) code = %v, want %v", got, want)
	}
	a, err := c.CreateActor(ctx, &ateapi.CreateActorRequest{Actor: &ateapi.Actor{Metadata: &ateapi.ResourceMetadata{Atespace: "default", Name: "a"}}})
	if err != nil {
		t.Fatalf("CreateActor(default/a): %v", err)
	}
	if got, want := a.GetStatus().GetState(), suspended; got != want {
		t.Errorf("new actor state = %v, want %v", got, want)
	}
}

func TestUpdateActor(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		state    ateapi.ActorState
		mutate   func(a *ateapi.Actor)
		wantCode codes.Code
	}{
		{"selector ok while running", running, func(a *ateapi.Actor) {
			a.WorkerSelector = &ateapi.Selector{MatchLabels: map[string]string{"pool": "b"}}
		}, codes.OK},
		{"template while suspended", suspended, func(a *ateapi.Actor) { a.ActorTemplate = ref("default", "t2") }, codes.OK},
		{"template while running", running, func(a *ateapi.Actor) { a.ActorTemplate = ref("default", "t2") }, codes.FailedPrecondition},
		{"unknown template", suspended, func(a *ateapi.Actor) { a.ActorTemplate = ref("default", "nope") }, codes.NotFound},
		{"stale version", suspended, func(a *ateapi.Actor) { a.Metadata.Version += 5 }, codes.Aborted},
		{"wrong uid", suspended, func(a *ateapi.Actor) { a.Metadata.Uid = "x" }, codes.Aborted},
		{"missing preconditions", suspended, func(a *ateapi.Actor) { a.Metadata.Uid, a.Metadata.Version = "", 0 }, codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fake.New()
			srv.AddWorker("ate", "pool-a", "w0")
			srv.AddTemplate("default", "t1")
			srv.AddTemplate("default", "t2")
			worker := ""
			if tc.state == running {
				worker = "w0"
			}
			srv.AddActor("default", "a", "t1", tc.state, worker)
			c := srv.Start(t)
			a, err := c.GetActor(ctx, &ateapi.GetActorRequest{Actor: ref("default", "a")})
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(a)
			got, err := c.UpdateActor(ctx, &ateapi.UpdateActorRequest{Actor: a})
			if code := status.Code(err); code != tc.wantCode {
				t.Fatalf("UpdateActor code = %v (%v), want %v", code, err, tc.wantCode)
			}
			if err != nil {
				return
			}
			if got, want := got.GetMetadata().GetVersion(), int64(2); got != want {
				t.Errorf("version = %d, want %d", got, want)
			}
		})
	}
}

func TestActorLifecycleAsync(t *testing.T) {
	srv := fake.New(fake.WithAsync())
	srv.AddWorker("ate", "pool-a", "w0")
	srv.AddActor("default", "a", "", suspended, "")
	c := srv.Start(t)
	ctx := context.Background()
	state := func() ateapi.ActorState { return srv.Actor("default", "a").GetStatus().GetState() }

	r, err := c.ResumeActor(ctx, &ateapi.ResumeActorRequest{Actor: ref("default", "a")})
	if err != nil {
		t.Fatal(err)
	}
	if !r.GetResumed() {
		t.Errorf("resumed = false, want true")
	}
	if got, want := state(), ateapi.ActorState_ACTOR_STATE_RESUMING; got != want {
		t.Errorf("after resume: state = %v, want %v", got, want)
	}
	_, err = c.SuspendActor(ctx, &ateapi.SuspendActorRequest{Actor: ref("default", "a")})
	if got, want := status.Code(err), codes.FailedPrecondition; got != want {
		t.Errorf("suspend while RESUMING: code = %v, want %v", got, want)
	}
	srv.Settle()
	a := srv.Actor("default", "a")
	if got, want := a.GetStatus().GetState(), running; got != want {
		t.Errorf("after settle: state = %v, want %v", got, want)
	}
	if got, want := a.GetStatus().GetWorkerAssignment().GetWorkerPod(), "w0"; got != want {
		t.Errorf("worker pod = %q, want %q", got, want)
	}
	if got, want := len(srv.Assignments("w0")), 1; got != want {
		t.Errorf("assignments = %d, want %d", got, want)
	}

	_, err = c.SuspendActor(ctx, &ateapi.SuspendActorRequest{Actor: ref("default", "a"), Options: &ateapi.SuspendOptions{Mode: ateapi.DurabilityMode_DURABILITY_MODE_SYNCHRONOUS}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := state(), ateapi.ActorState_ACTOR_STATE_SUSPENDING; got != want {
		t.Errorf("after suspend: state = %v, want %v", got, want)
	}
	srv.Settle()
	a = srv.Actor("default", "a")
	if got, want := a.GetStatus().GetState(), suspended; got != want {
		t.Errorf("after settle: state = %v, want %v", got, want)
	}
	if a.GetStatus().GetWorkerAssignment() != nil {
		t.Errorf("suspended actor still assigned: %v", a.GetStatus().GetWorkerAssignment())
	}
	if got, want := a.GetStatus().GetSnapshots()[0].GetDurableSnapshot().GetState(), ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED; got != want {
		t.Errorf("snapshot storage = %v, want %v", got, want)
	}
	if got, want := len(srv.Assignments("w0")), 0; got != want {
		t.Errorf("assignments = %d, want %d", got, want)
	}

	_, err = c.DeleteActor(ctx, &ateapi.DeleteActorRequest{Actor: ref("default", "a")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := state(), ateapi.ActorState_ACTOR_STATE_DELETING; got != want {
		t.Errorf("after delete: state = %v, want %v", got, want)
	}
	srv.Settle()
	if a := srv.Actor("default", "a"); a != nil {
		t.Errorf("actor still exists after settle: %v", a)
	}
}

func TestActorTransitionsSync(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		from     ateapi.ActorState
		call     func(c ateapi.ControlClient) error
		want     ateapi.ActorState
		wantCode codes.Code
	}{
		{"resume suspended", suspended, func(c ateapi.ControlClient) error {
			_, err := c.ResumeActor(ctx, &ateapi.ResumeActorRequest{Actor: ref("default", "a")})
			return err
		}, running, codes.OK},
		{"resume crashed", crashed, func(c ateapi.ControlClient) error {
			_, err := c.ResumeActor(ctx, &ateapi.ResumeActorRequest{Actor: ref("default", "a")})
			return err
		}, running, codes.OK},
		{"suspend crashed", crashed, func(c ateapi.ControlClient) error {
			_, err := c.SuspendActor(ctx, &ateapi.SuspendActorRequest{Actor: ref("default", "a")})
			return err
		}, crashed, codes.FailedPrecondition},
		{"revert crashed", crashed, func(c ateapi.ControlClient) error {
			_, err := c.RevertActor(ctx, &ateapi.RevertActorRequest{Actor: ref("default", "a")})
			return err
		}, suspended, codes.OK},
		{"resume missing", suspended, func(c ateapi.ControlClient) error {
			_, err := c.ResumeActor(ctx, &ateapi.ResumeActorRequest{Actor: ref("default", "nope")})
			return err
		}, suspended, codes.NotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fake.New()
			srv.AddWorker("ate", "pool-a", "w0")
			srv.AddActor("default", "a", "", tc.from, "")
			c := srv.Start(t)
			err := tc.call(c)
			if got := status.Code(err); got != tc.wantCode {
				t.Fatalf("code = %v (%v), want %v", got, err, tc.wantCode)
			}
			if got := srv.Actor("default", "a").GetStatus().GetState(); got != tc.want {
				t.Errorf("state = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTags(t *testing.T) {
	srv := fake.New()
	srv.AddWorker("ate", "pool-a", "w0")
	srv.AddActor("default", "s", "", suspended, "")
	srv.AddActor("default", "r", "", running, "w0")
	c := srv.Start(t)
	ctx := context.Background()
	tag := func(name, actor string) *ateapi.Tag {
		return &ateapi.Tag{Metadata: &ateapi.ResourceMetadata{Atespace: "default", Name: name}, SourceActor: ref("default", actor)}
	}

	if _, err := c.CreateTag(ctx, &ateapi.CreateTagRequest{Tag: tag("t1", "r")}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("tag running actor: err = %v, want FailedPrecondition", err)
	}
	got, err := c.CreateTag(ctx, &ateapi.CreateTagRequest{Tag: tag("t1", "s")})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetScope() != ateapi.TagScope_TAG_SCOPE_ATESPACE {
		t.Errorf("scope = %v, want ATESPACE", got.GetScope())
	}
	if _, err := c.CreateTag(ctx, &ateapi.CreateTagRequest{Tag: tag("t1", "s")}); status.Code(err) != codes.AlreadyExists {
		t.Errorf("duplicate tag: err = %v, want AlreadyExists", err)
	}
	got.Scope = ateapi.TagScope_TAG_SCOPE_PUBLISHED
	up, err := c.UpdateTag(ctx, &ateapi.UpdateTagRequest{Tag: got})
	if err != nil {
		t.Fatal(err)
	}
	if up.GetScope() != ateapi.TagScope_TAG_SCOPE_PUBLISHED || up.GetMetadata().GetVersion() != 2 {
		t.Errorf("updated tag = %v", up)
	}
	if _, err := c.UpdateTag(ctx, &ateapi.UpdateTagRequest{Tag: got}); status.Code(err) != codes.Aborted {
		t.Errorf("stale update: err = %v, want Aborted", err)
	}
}

func TestWorkers(t *testing.T) {
	srv := fake.New()
	srv.AddWorker("ate", "pool-a", "w0")
	c := srv.Start(t)
	ctx := context.Background()
	w := ref("", "w0")
	st := func() ateapi.WorkerState { return srv.Worker("w0").GetStatus().GetState() }

	if _, err := c.MarkWorkerUnavailable(ctx, &ateapi.MarkWorkerUnavailableRequest{Worker: w}); err != nil {
		t.Fatal(err)
	}
	if got, want := st(), ateapi.WorkerState_WORKER_STATE_UNAVAILABLE; got != want {
		t.Errorf("state = %v, want %v", got, want)
	}
	if _, err := c.MarkWorkerAvailable(ctx, &ateapi.MarkWorkerAvailableRequest{Worker: w}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DrainWorker(ctx, &ateapi.DrainWorkerRequest{Worker: w}); err != nil {
		t.Fatal(err)
	}
	if got, want := st(), ateapi.WorkerState_WORKER_STATE_DRAINING; got != want {
		t.Errorf("state = %v, want %v", got, want)
	}
	if _, err := c.MarkWorkerAvailable(ctx, &ateapi.MarkWorkerAvailableRequest{Worker: w}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("mark draining available: err = %v, want FailedPrecondition", err)
	}
	if got, want := srv.Worker("w0").GetMetadata().GetVersion(), int64(4); got != want {
		t.Errorf("version = %d, want %d", got, want)
	}
	if _, err := c.ListWorkerActorAssignments(ctx, &ateapi.ListWorkerActorAssignmentsRequest{Worker: ref("", "nope")}); status.Code(err) != codes.NotFound {
		t.Errorf("assignments of unknown worker: err = %v, want NotFound", err)
	}
}

func TestPolicies(t *testing.T) {
	srv := fake.New()
	srv.AddActor("default", "a", "", suspended, "")
	c := srv.Start(t)
	ctx := context.Background()

	_, err := c.GetActorEgressPolicy(ctx, &ateapi.GetActorEgressPolicyRequest{Actor: ref("default", "a"), Name: "default"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("get missing egress: err = %v, want NotFound", err)
	}
	p, err := c.CreateActorEgressPolicy(ctx, &ateapi.CreateActorEgressPolicyRequest{
		Actor:        ref("default", "a"),
		EgressPolicy: &ateapi.EgressPolicy{Metadata: &ateapi.ResourceMetadata{Atespace: "default", Name: "default"}, Rules: []*ateapi.EgressRule{fake.HTTPSRule("example.com")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Rules = append(p.Rules, fake.HTTPSRule("example.org"))
	if _, err := c.UpdateActorEgressPolicy(ctx, &ateapi.UpdateActorEgressPolicyRequest{Actor: ref("default", "a"), EgressPolicy: p}); err != nil {
		t.Fatal(err)
	}
	if got, want := len(srv.EgressPolicy("default", "a").GetRules()), 2; got != want {
		t.Errorf("rules = %d, want %d", got, want)
	}
	if _, err := c.UpdateActorEgressPolicy(ctx, &ateapi.UpdateActorEgressPolicyRequest{Actor: ref("default", "a"), EgressPolicy: p}); status.Code(err) != codes.Aborted {
		t.Errorf("stale egress update: err = %v, want Aborted", err)
	}

	if _, err := c.GetGlobalAccessPolicy(ctx, &ateapi.GetGlobalAccessPolicyRequest{Name: "default"}); status.Code(err) != codes.NotFound {
		t.Errorf("get missing global policy: err = %v, want NotFound", err)
	}
	srv.SetAccessPolicy("", &ateapi.Binding{Role: "owner", Members: []string{"user:a"}})
	g, err := c.GetGlobalAccessPolicy(ctx, &ateapi.GetGlobalAccessPolicyRequest{Name: "default"})
	if err != nil {
		t.Fatal(err)
	}
	g.Bindings = nil
	if _, err := c.UpdateGlobalAccessPolicy(ctx, &ateapi.UpdateGlobalAccessPolicyRequest{AccessPolicy: g}); err != nil {
		t.Fatal(err)
	}
	if got := srv.AccessPolicy("").GetBindings(); len(got) != 0 {
		t.Errorf("bindings = %v, want none", got)
	}
	if _, err := c.GetAtespaceAccessPolicy(ctx, &ateapi.GetAtespaceAccessPolicyRequest{Atespace: ref("", "nope"), Name: "default"}); status.Code(err) != codes.NotFound {
		t.Errorf("policy of unknown atespace: err = %v, want NotFound", err)
	}
}

func TestErrorInjection(t *testing.T) {
	srv := fake.New()
	srv.AddAtespace("default")
	c := srv.Start(t)
	ctx := context.Background()
	list := func() error {
		_, err := c.ListAtespaces(ctx, &ateapi.ListAtespacesRequest{})
		return err
	}

	srv.FailNext("ListAtespaces", status.Error(codes.Unavailable, "boom"))
	srv.FailNext("*", status.Error(codes.Internal, "any"))
	if got, want := status.Code(list()), codes.Unavailable; got != want {
		t.Errorf("1st call: code = %v, want %v", got, want)
	}
	if got, want := status.Code(list()), codes.Internal; got != want {
		t.Errorf("2nd call: code = %v, want %v", got, want)
	}
	if err := list(); err != nil {
		t.Errorf("3rd call: %v", err)
	}

	srv.Fail("ListAtespaces", status.Error(codes.PermissionDenied, "no"))
	for range 2 {
		if got, want := status.Code(list()), codes.PermissionDenied; got != want {
			t.Errorf("with Fail: code = %v, want %v", got, want)
		}
	}
	srv.Fail("ListAtespaces", nil)
	if err := list(); err != nil {
		t.Errorf("after clearing Fail: %v", err)
	}

	srv.SetLatency("ListAtespaces", time.Second)
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err := c.ListAtespaces(tctx, &ateapi.ListAtespacesRequest{})
	if got, want := status.Code(err), codes.DeadlineExceeded; got != want {
		t.Errorf("with latency: code = %v, want %v", got, want)
	}
	srv.SetLatency("ListAtespaces", 0)

	methods := srv.Methods()
	if got, want := len(methods), 7; got != want {
		t.Errorf("recorded %d calls (%v), want %d", got, methods, want)
	}
	if srv.LastRequest("ListAtespaces") == nil {
		t.Errorf("LastRequest(ListAtespaces) = nil")
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("unexpected cancel")
	}
}

func TestDeterministic(t *testing.T) {
	build := func() *ateapi.Actor {
		srv := fake.New()
		srv.AddWorker("ate", "pool-a", "w0")
		return srv.AddActor("default", "a", "t", running, "w0")
	}
	a, b := build(), build()
	if a.GetMetadata().GetUid() != b.GetMetadata().GetUid() || !a.GetMetadata().GetCreateTime().AsTime().Equal(fake.Epoch) {
		t.Errorf("not deterministic: %v vs %v", a.GetMetadata(), b.GetMetadata())
	}
	if got, want := a.GetStatus().GetWorkerAssignment().GetWorkerPodIps(), []string{"10.0.0.1"}; !slices.Equal(got, want) {
		t.Errorf("pod ips = %v, want %v", got, want)
	}
}
