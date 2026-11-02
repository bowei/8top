// Package fake is an in-memory implementation of the Substrate ateapi Control
// service for tests. It is served over bufconn, so tests run the real gRPC
// client against it.
//
// The fake keeps every resource in maps guarded by one mutex and enforces the
// rules 8top depends on:
//
//   - uid/version preconditions on Update* (Aborted on mismatch,
//     InvalidArgument when missing), and on Delete* when DeleteOptions are set;
//   - version bump and update_time on every mutation;
//   - changing an actor's template requires SUSPENDED (FailedPrecondition);
//   - paging through List* via page_size / page_token (see WithPageSize);
//   - the atespace (and parent actor / worker) must exist.
//
// Uids come from a counter and timestamps from a fake clock that only moves
// when told to (Advance), so output is deterministic.
//
// Typical use:
//
//	srv := fake.New(fake.WithPageSize(2))
//	srv.AddWorker("ate", "pool-a", "pool-a-0")
//	srv.AddTemplate("default", "counter")
//	srv.AddActor("default", "counter-1", "counter", ateapi.ActorState_ACTOR_STATE_RUNNING, "pool-a-0")
//	srv.AddActor("default", "counter-2", "counter", ateapi.ActorState_ACTOR_STATE_SUSPENDED, "")
//	client := srv.Start(t) // ateapi.ControlClient, closed by t.Cleanup
//
//	srv.FailNext("ListActors", status.Error(codes.Unavailable, "boom"))
//	srv.SetAsync(true) // ResumeActor leaves RESUMING until srv.Settle()
package fake

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Epoch is the fake clock's starting time.
var Epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	// DefaultPageSize is used when a List request does not set page_size.
	DefaultPageSize = 100
	// MaxPageSize caps page_size, as the real server does.
	MaxPageSize = 1000
)

// Server is the in-memory Control service. Create it with New, seed it with
// the Add* helpers, and serve it with Start.
type Server struct {
	ateapi.UnimplementedControlServer

	mu sync.Mutex

	now      time.Time
	uidSeq   int
	ipSeq    int
	pageSize int
	maxPage  int
	async    bool

	atespaces   map[string]*ateapi.Atespace      // name
	templates   map[string]*ateapi.ActorTemplate // atespace/name
	actors      map[string]*ateapi.Actor         // atespace/name
	egress      map[string]*ateapi.EgressPolicy  // atespace/actor
	tags        map[string]*ateapi.Tag           // atespace/name
	workers     map[string]*ateapi.Worker        // name
	assignments map[string]map[string]*ateapi.ActorAssignment
	globalAP    *ateapi.AccessPolicy
	atespaceAP  map[string]*ateapi.AccessPolicy // atespace

	// pending holds the second half of actor transitions in async mode,
	// keyed by actor key.
	pending map[string]func(*ateapi.Actor)

	failNext map[string][]error
	failAll  map[string]error
	latency  map[string]time.Duration
	calls    []Call
}

// Call is one RPC the server received.
type Call struct {
	// Method is the short method name, e.g. "SuspendActor".
	Method string
	// Request is a copy of the request message.
	Request proto.Message
}

// Option configures a Server.
type Option func(*Server)

// WithPageSize makes List* return at most n items per page, whatever the
// request asks for, so paging is exercised with small fixtures.
func WithPageSize(n int) Option {
	return func(s *Server) { s.pageSize, s.maxPage = n, n }
}

// WithAsync starts the server in async mode; see SetAsync.
func WithAsync() Option { return func(s *Server) { s.async = true } }

// New returns an empty server.
func New(opts ...Option) *Server {
	s := &Server{
		now:         Epoch,
		pageSize:    DefaultPageSize,
		maxPage:     MaxPageSize,
		atespaces:   map[string]*ateapi.Atespace{},
		templates:   map[string]*ateapi.ActorTemplate{},
		actors:      map[string]*ateapi.Actor{},
		egress:      map[string]*ateapi.EgressPolicy{},
		tags:        map[string]*ateapi.Tag{},
		workers:     map[string]*ateapi.Worker{},
		assignments: map[string]map[string]*ateapi.ActorAssignment{},
		atespaceAP:  map[string]*ateapi.AccessPolicy{},
		pending:     map[string]func(*ateapi.Actor){},
		failNext:    map[string][]error{},
		failAll:     map[string]error{},
		latency:     map[string]time.Duration{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Start serves s over bufconn and returns a client connected to it. The
// server and connection are torn down by t.Cleanup.
func (s *Server) Start(t testing.TB) ateapi.ControlClient {
	t.Helper()
	conn := s.Dial(t)
	return ateapi.NewControlClient(conn)
}

// Dial serves s over bufconn and returns the client connection, for callers
// that need the raw *grpc.ClientConn. Torn down by t.Cleanup.
func (s *Server) Dial(t testing.TB) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(s.intercept))
	ateapi.RegisterControlServer(gs, s)
	go gs.Serve(lis)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("fake: dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		gs.Stop()
	})
	return conn
}

// intercept applies the injected latency and errors, and records the call.
func (s *Server) intercept(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	method := info.FullMethod[strings.LastIndex(info.FullMethod, "/")+1:]
	s.mu.Lock()
	c := Call{Method: method}
	if m, ok := req.(proto.Message); ok {
		c.Request = proto.Clone(m)
	}
	s.calls = append(s.calls, c)
	delay := s.latency[method] + s.latency["*"]
	var err error
	for _, k := range []string{method, "*"} {
		if q := s.failNext[k]; len(q) > 0 && err == nil {
			err, s.failNext[k] = q[0], q[1:]
		}
	}
	if err == nil {
		if e, ok := s.failAll[method]; ok {
			err = e
		} else if e, ok := s.failAll["*"]; ok {
			err = e
		}
	}
	s.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// FailNext makes the next call to method (short name, e.g. "ListActors", or
// "*" for any method) return err. Calls queue: FailNext twice fails the next
// two calls.
func (s *Server) FailNext(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[method] = append(s.failNext[method], err)
}

// Fail makes every call to method (or "*") return err until Fail(method, nil).
func (s *Server) Fail(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.failAll, method)
		return
	}
	s.failAll[method] = err
}

// SetLatency delays every call to method (or "*") by d. Zero removes it.
func (s *Server) SetLatency(method string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency[method] = d
}

// Calls returns the RPCs received so far, oldest first.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// Methods returns the method names of Calls, oldest first.
func (s *Server) Methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		out = append(out, c.Method)
	}
	return out
}

// LastRequest returns the most recent request to method, or nil.
func (s *Server) LastRequest(method string) proto.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.calls) - 1; i >= 0; i-- {
		if s.calls[i].Method == method {
			return s.calls[i].Request
		}
	}
	return nil
}

// ResetCalls forgets the recorded calls.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// SetAsync controls how actor actions complete. In sync mode (the default)
// ResumeActor, SuspendActor, RevertActor and DeleteActor reach their final
// state before returning. In async mode they leave the actor RESUMING,
// SUSPENDING, REVERTING or DELETING until Settle is called.
func (s *Server) SetAsync(async bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.async = async
}

// Settle completes every pending async transition.
func (s *Server) Settle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := sortedKeys(s.pending)
	for _, k := range keys {
		finish := s.pending[k]
		delete(s.pending, k)
		if a, ok := s.actors[k]; ok {
			finish(a)
			s.touch(a.Metadata)
		}
	}
}

// Now returns the fake clock's time.
func (s *Server) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

// Advance moves the fake clock forward by d.
func (s *Server) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
}

// SetNow sets the fake clock.
func (s *Server) SetNow(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = t
}

// --- seeding helpers -------------------------------------------------------
//
// The Add* helpers insert objects directly, bypassing the RPC rules, and
// return a copy of what was stored. They create missing atespaces
// implicitly. Each takes optional mutators applied just before the object is
// stored, for fields the helper does not cover. They panic on fixtures that
// cannot be built (for example an unknown worker).

// AddAtespace adds an atespace if it does not exist.
func (s *Server) AddAtespace(name string, mods ...func(*ateapi.Atespace)) *ateapi.Atespace {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.atespaces[name]; ok && len(mods) == 0 {
		return clone(a)
	}
	a := &ateapi.Atespace{Metadata: s.newMeta("", name)}
	apply(a, mods)
	s.atespaces[name] = a
	return clone(a)
}

func (s *Server) ensureAtespace(name string) {
	if _, ok := s.atespaces[name]; !ok && name != "" {
		s.atespaces[name] = &ateapi.Atespace{Metadata: s.newMeta("", name)}
	}
}

// AddTemplate adds an actor template with one container.
func (s *Server) AddTemplate(atespace, name string, mods ...func(*ateapi.ActorTemplate)) *ateapi.ActorTemplate {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAtespace(atespace)
	t := &ateapi.ActorTemplate{
		Metadata:   s.newMeta(atespace, name),
		Containers: []*ateapi.Container{{Name: "main", Image: "example.com/" + name + ":latest"}},
	}
	apply(t, mods)
	s.templates[key(atespace, name)] = t
	return clone(t)
}

// AddWorker adds an ACTIVE worker named pod, with a deterministic pod uid,
// node and IP.
func (s *Server) AddWorker(namespace, pool, pod string, mods ...func(*ateapi.Worker)) *ateapi.Worker {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ipSeq++
	w := &ateapi.Worker{
		Metadata:        s.newMeta("", pod),
		WorkerNamespace: namespace,
		WorkerPool:      pool,
		WorkerPod:       pod,
		WorkerPodUid:    s.nextUID(),
		NodeName:        fmt.Sprintf("node-%d", s.ipSeq),
		Labels:          map[string]string{"pool": pool},
		Status: &ateapi.WorkerStatus{
			State: ateapi.WorkerState_WORKER_STATE_ACTIVE,
			Ips:   []string{fmt.Sprintf("10.0.0.%d", s.ipSeq)},
		},
	}
	apply(w, mods)
	s.workers[w.GetMetadata().GetName()] = w
	return clone(w)
}

// AddActor adds an actor. template names a template in the same atespace
// ("" for none; it need not exist). If worker is not empty the actor is
// placed on that worker (which must exist) and an ActorAssignment is
// created; state should then be RUNNING. A SUSPENDED actor gets a snapshot,
// so it can be tagged.
func (s *Server) AddActor(atespace, name, template string, state ateapi.ActorState, worker string, mods ...func(*ateapi.Actor)) *ateapi.Actor {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAtespace(atespace)
	a := &ateapi.Actor{
		Metadata: s.newMeta(atespace, name),
		Status:   &ateapi.ActorStatus{State: state},
	}
	if template != "" {
		a.ActorTemplate = &ateapi.ObjectRef{Atespace: atespace, Name: template}
	}
	if state == ateapi.ActorState_ACTOR_STATE_SUSPENDED {
		s.addSnapshot(a, ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED)
	}
	if state == ateapi.ActorState_ACTOR_STATE_CRASHED {
		a.Status.Crash = &ateapi.ActorCrash{Message: "fake crash", CrashTime: timestamppb.New(s.now)}
	}
	if worker != "" {
		w, ok := s.workers[worker]
		if !ok {
			panic(fmt.Sprintf("fake.AddActor: unknown worker %q", worker))
		}
		s.assign(a, w)
	}
	apply(a, mods)
	s.actors[key(atespace, name)] = a
	return clone(a)
}

// AddTag adds a tag of sourceActor's snapshot. sourceActor need not exist.
func (s *Server) AddTag(atespace, name, sourceActor string, scope ateapi.TagScope, mods ...func(*ateapi.Tag)) *ateapi.Tag {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAtespace(atespace)
	t := &ateapi.Tag{
		Metadata: s.newMeta(atespace, name),
		Scope:    scope,
		Status: &ateapi.TagStatus{Snapshot: &ateapi.Snapshot{
			Uuid:  s.nextUID(),
			Owner: ateapi.SnapshotOwner_SNAPSHOT_OWNER_TAG,
		}},
	}
	if sourceActor != "" {
		t.SourceActor = &ateapi.ObjectRef{Atespace: atespace, Name: sourceActor}
	}
	apply(t, mods)
	s.tags[key(atespace, name)] = t
	return clone(t)
}

// AddEgressPolicy sets the egress policy of an actor.
func (s *Server) AddEgressPolicy(atespace, actor string, rules ...*ateapi.EgressRule) *ateapi.EgressPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAtespace(atespace)
	p := &ateapi.EgressPolicy{Metadata: s.newMeta(atespace, "default"), Rules: rules}
	s.egress[key(atespace, actor)] = p
	return clone(p)
}

// HTTPSRule is a convenience egress rule allowing HTTPS to hostnames on all
// ports.
func HTTPSRule(hostnames ...string) *ateapi.EgressRule {
	return &ateapi.EgressRule{Https: &ateapi.HttpsRule{
		Hostnames: hostnames,
		Ports:     &ateapi.Ports{All: &ateapi.AllPorts{}},
	}}
}

// SetAccessPolicy sets the access policy of atespace, or the global policy
// if atespace is empty.
func (s *Server) SetAccessPolicy(atespace string, bindings ...*ateapi.Binding) *ateapi.AccessPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAtespace(atespace)
	p := &ateapi.AccessPolicy{Metadata: s.newMeta(atespace, "default"), Bindings: bindings}
	if atespace == "" {
		s.globalAP = p
	} else {
		s.atespaceAP[atespace] = p
	}
	return clone(p)
}

// --- inspection helpers ----------------------------------------------------
//
// These return copies of the stored objects, or nil if not found.

// Actor returns the stored actor.
func (s *Server) Actor(atespace, name string) *ateapi.Actor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.actors[key(atespace, name)])
}

// Worker returns the stored worker.
func (s *Server) Worker(name string) *ateapi.Worker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.workers[name])
}

// Tag returns the stored tag.
func (s *Server) Tag(atespace, name string) *ateapi.Tag {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.tags[key(atespace, name)])
}

// EgressPolicy returns the stored egress policy of an actor.
func (s *Server) EgressPolicy(atespace, actor string) *ateapi.EgressPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.egress[key(atespace, actor)])
}

// AccessPolicy returns the stored access policy of atespace, or the global
// one if atespace is empty.
func (s *Server) AccessPolicy(atespace string) *ateapi.AccessPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	if atespace == "" {
		return clone(s.globalAP)
	}
	return clone(s.atespaceAP[atespace])
}

// Assignments returns the actor assignments of a worker, sorted by name.
func (s *Server) Assignments(worker string) []*ateapi.ActorAssignment {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.assignments[worker]
	var out []*ateapi.ActorAssignment
	for _, k := range sortedKeys(m) {
		out = append(out, clone(m[k]))
	}
	return out
}

// --- internals (callers hold s.mu) -----------------------------------------

func key(atespace, name string) string { return atespace + "/" + name }

func clone[T proto.Message](m T) T {
	var zero T
	if any(m) == any(zero) {
		return zero
	}
	return proto.Clone(m).(T)
}

func apply[T any](v T, mods []func(T)) {
	for _, m := range mods {
		m(v)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (s *Server) nextUID() string {
	s.uidSeq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", s.uidSeq)
}

func (s *Server) newMeta(atespace, name string) *ateapi.ResourceMetadata {
	return &ateapi.ResourceMetadata{
		Atespace:   atespace,
		Name:       name,
		Uid:        s.nextUID(),
		Version:    1,
		CreateTime: timestamppb.New(s.now),
		UpdateTime: timestamppb.New(s.now),
	}
}

func (s *Server) touch(m *ateapi.ResourceMetadata) {
	m.Version++
	m.UpdateTime = timestamppb.New(s.now)
}

func (s *Server) addSnapshot(a *ateapi.Actor, storage ateapi.SnapshotStorageState) {
	gen := int32(1)
	if snaps := a.GetStatus().GetSnapshots(); len(snaps) > 0 {
		gen = snaps[len(snaps)-1].GetGeneration() + 1
	}
	a.Status.Snapshots = []*ateapi.Snapshot{{
		Uuid:            s.nextUID(),
		Generation:      gen,
		Owner:           ateapi.SnapshotOwner_SNAPSHOT_OWNER_ACTOR,
		Fidelity:        ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY,
		DurableSnapshot: &ateapi.SnapshotStorage{State: storage},
	}}
}

// assign places a on w.
func (s *Server) assign(a *ateapi.Actor, w *ateapi.Worker) {
	a.Status.WorkerAssignment = &ateapi.WorkerAssignment{
		Worker:          &ateapi.ObjectRef{Name: w.GetMetadata().GetName()},
		WorkerNamespace: w.GetWorkerNamespace(),
		WorkerPool:      w.GetWorkerPool(),
		WorkerPod:       w.GetWorkerPod(),
		WorkerPodUid:    w.GetWorkerPodUid(),
		WorkerPodIps:    slices.Clone(w.GetStatus().GetIps()),
		NodeName:        w.GetNodeName(),
	}
	a.Status.AssignedNode = w.GetNodeName()
	wn := w.GetMetadata().GetName()
	if s.assignments[wn] == nil {
		s.assignments[wn] = map[string]*ateapi.ActorAssignment{}
	}
	uid := a.GetMetadata().GetUid()
	s.assignments[wn][uid] = &ateapi.ActorAssignment{
		Metadata: s.newMeta("", uid),
		Actor:    &ateapi.ObjectRef{Atespace: a.GetMetadata().GetAtespace(), Name: a.GetMetadata().GetName()},
		ActorUid: uid,
	}
}

// unassign removes a from its worker.
func (s *Server) unassign(a *ateapi.Actor) {
	if wa := a.GetStatus().GetWorkerAssignment(); wa != nil {
		delete(s.assignments[wa.GetWorker().GetName()], a.GetMetadata().GetUid())
	}
	if a.Status != nil {
		a.Status.WorkerAssignment = nil
		a.Status.AssignedNode = ""
	}
}

// pickWorker returns the ACTIVE worker with the fewest assignments, or nil.
func (s *Server) pickWorker() *ateapi.Worker {
	var best *ateapi.Worker
	for _, name := range sortedKeys(s.workers) {
		w := s.workers[name]
		if w.GetStatus().GetState() != ateapi.WorkerState_WORKER_STATE_ACTIVE {
			continue
		}
		if best == nil || len(s.assignments[name]) < len(s.assignments[best.GetMetadata().GetName()]) {
			best = w
		}
	}
	return best
}
