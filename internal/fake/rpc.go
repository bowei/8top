package fake

import (
	"context"
	"strconv"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// page returns the page of items selected by opts.
func (s *Server) page(n int, opts *ateapi.ListOptions) (start, end int, next string, err error) {
	size := int(opts.GetPageSize())
	if size <= 0 {
		size = s.pageSize
	}
	size = min(size, s.maxPage)
	if t := opts.GetPageToken(); t != "" {
		start, err = strconv.Atoi(t)
		if err != nil || start < 0 || start > n {
			return 0, 0, "", status.Errorf(codes.InvalidArgument, "invalid page_token %q", t)
		}
	}
	end = min(start+size, n)
	if end < n {
		next = strconv.Itoa(end)
	}
	return start, end, next, nil
}

// listPage filters m (sorted by key) with keep and returns one page of clones.
func listPage[T proto.Message](s *Server, m map[string]T, keep func(T) bool, opts *ateapi.ListOptions) ([]T, string, error) {
	var all []T
	for _, k := range sortedKeys(m) {
		if keep == nil || keep(m[k]) {
			all = append(all, m[k])
		}
	}
	start, end, next, err := s.page(len(all), opts)
	if err != nil {
		return nil, "", err
	}
	out := make([]T, 0, end-start)
	for _, v := range all[start:end] {
		out = append(out, clone(v))
	}
	return out, next, nil
}

func notFound(kind, k string) error {
	return status.Errorf(codes.NotFound, "%s %q not found", kind, k)
}

func (s *Server) checkAtespace(name string) error {
	if name == "" {
		return status.Error(codes.InvalidArgument, "atespace is required")
	}
	if _, ok := s.atespaces[name]; !ok {
		return notFound("atespace", name)
	}
	return nil
}

// checkUpdate enforces the uid/version preconditions of an update.
func checkUpdate(cur, req *ateapi.ResourceMetadata) error {
	if req.GetUid() == "" || req.GetVersion() == 0 {
		return status.Error(codes.InvalidArgument, "metadata.uid and metadata.version are required")
	}
	if req.GetUid() != cur.GetUid() {
		return status.Errorf(codes.Aborted, "uid %q does not match current uid %q", req.GetUid(), cur.GetUid())
	}
	if req.GetVersion() != cur.GetVersion() {
		return status.Errorf(codes.Aborted, "version %d does not match current version %d; the object was modified", req.GetVersion(), cur.GetVersion())
	}
	return nil
}

// checkDelete enforces optional DeleteOptions.
func checkDelete(cur *ateapi.ResourceMetadata, o *ateapi.DeleteOptions) error {
	if o.GetUid() != "" && o.GetUid() != cur.GetUid() {
		return status.Errorf(codes.Aborted, "uid %q does not match current uid %q", o.GetUid(), cur.GetUid())
	}
	if o.GetVersion() != 0 && o.GetVersion() != cur.GetVersion() {
		return status.Errorf(codes.Aborted, "version %d does not match current version %d", o.GetVersion(), cur.GetVersion())
	}
	return nil
}

func checkSingletonName(name string) error {
	if name != "default" {
		return status.Errorf(codes.InvalidArgument, "name must be \"default\", got %q", name)
	}
	return nil
}

// --- atespaces -------------------------------------------------------------

func (s *Server) ListAtespaces(_ context.Context, req *ateapi.ListAtespacesRequest) (*ateapi.ListAtespacesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, next, err := listPage(s, s.atespaces, nil, req.GetOptions())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListAtespacesResponse{Atespaces: items, NextPageToken: next}, nil
}

func (s *Server) GetAtespace(_ context.Context, req *ateapi.GetAtespaceRequest) (*ateapi.Atespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.atespaces[req.GetAtespace().GetName()]
	if !ok {
		return nil, notFound("atespace", req.GetAtespace().GetName())
	}
	return clone(a), nil
}

func (s *Server) CreateAtespace(_ context.Context, req *ateapi.CreateAtespaceRequest) (*ateapi.Atespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := req.GetAtespace().GetMetadata().GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "metadata.name is required")
	}
	if _, ok := s.atespaces[name]; ok {
		return nil, status.Errorf(codes.AlreadyExists, "atespace %q already exists", name)
	}
	a := &ateapi.Atespace{Metadata: s.newMeta("", name)}
	s.atespaces[name] = a
	return clone(a), nil
}

func (s *Server) DeleteAtespace(_ context.Context, req *ateapi.DeleteAtespaceRequest) (*ateapi.Atespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := req.GetAtespace().GetName()
	a, ok := s.atespaces[name]
	if !ok {
		return nil, notFound("atespace", name)
	}
	if err := checkDelete(a.GetMetadata(), req.GetOptions()); err != nil {
		return nil, err
	}
	for _, x := range s.actors {
		if x.GetMetadata().GetAtespace() == name {
			return nil, status.Errorf(codes.FailedPrecondition, "atespace %q still has actors", name)
		}
	}
	for _, x := range s.tags {
		if x.GetMetadata().GetAtespace() == name {
			return nil, status.Errorf(codes.FailedPrecondition, "atespace %q still has tags", name)
		}
	}
	for k, t := range s.templates {
		if t.GetMetadata().GetAtespace() == name {
			delete(s.templates, k)
		}
	}
	delete(s.atespaceAP, name)
	delete(s.atespaces, name)
	return clone(a), nil
}

// --- actor templates -------------------------------------------------------

func (s *Server) ListActorTemplates(_ context.Context, req *ateapi.ListActorTemplatesRequest) (*ateapi.ListActorTemplatesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns := req.GetAtespace()
	if ns != "" {
		if err := s.checkAtespace(ns); err != nil {
			return nil, err
		}
	}
	items, next, err := listPage(s, s.templates, func(t *ateapi.ActorTemplate) bool {
		return ns == "" || t.GetMetadata().GetAtespace() == ns
	}, req.GetOptions())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListActorTemplatesResponse{ActorTemplates: items, NextPageToken: next}, nil
}

func (s *Server) GetActorTemplate(_ context.Context, req *ateapi.GetActorTemplateRequest) (*ateapi.ActorTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(req.GetActorTemplate().GetAtespace(), req.GetActorTemplate().GetName())
	t, ok := s.templates[k]
	if !ok {
		return nil, notFound("actor template", k)
	}
	return clone(t), nil
}

func (s *Server) CreateActorTemplate(_ context.Context, req *ateapi.CreateActorTemplateRequest) (*ateapi.ActorTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := clone(req.GetActorTemplate())
	md := t.GetMetadata()
	if err := s.checkAtespace(md.GetAtespace()); err != nil {
		return nil, err
	}
	k := key(md.GetAtespace(), md.GetName())
	if _, ok := s.templates[k]; ok {
		return nil, status.Errorf(codes.AlreadyExists, "actor template %q already exists", k)
	}
	t.Metadata = s.newMeta(md.GetAtespace(), md.GetName())
	t.Status = nil
	s.templates[k] = t
	return clone(t), nil
}

func (s *Server) DeleteActorTemplate(_ context.Context, req *ateapi.DeleteActorTemplateRequest) (*ateapi.ActorTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(req.GetActorTemplate().GetAtespace(), req.GetActorTemplate().GetName())
	t, ok := s.templates[k]
	if !ok {
		return nil, notFound("actor template", k)
	}
	if err := checkDelete(t.GetMetadata(), req.GetOptions()); err != nil {
		return nil, err
	}
	delete(s.templates, k)
	return clone(t), nil
}

// --- actors ----------------------------------------------------------------

func (s *Server) ListActors(_ context.Context, req *ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns := req.GetAtespace()
	if ns != "" {
		if err := s.checkAtespace(ns); err != nil {
			return nil, err
		}
	}
	items, next, err := listPage(s, s.actors, func(a *ateapi.Actor) bool {
		return ns == "" || a.GetMetadata().GetAtespace() == ns
	}, req.GetOptions())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListActorsResponse{Actors: items, NextPageToken: next}, nil
}

func (s *Server) actor(ref *ateapi.ObjectRef) (*ateapi.Actor, error) {
	k := key(ref.GetAtespace(), ref.GetName())
	a, ok := s.actors[k]
	if !ok {
		return nil, notFound("actor", k)
	}
	return a, nil
}

func (s *Server) GetActor(_ context.Context, req *ateapi.GetActorRequest) (*ateapi.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	return clone(a), nil
}

func (s *Server) CreateActor(_ context.Context, req *ateapi.CreateActorRequest) (*ateapi.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := req.GetActor()
	md := in.GetMetadata()
	if err := s.checkAtespace(md.GetAtespace()); err != nil {
		return nil, err
	}
	k := key(md.GetAtespace(), md.GetName())
	if _, ok := s.actors[k]; ok {
		return nil, status.Errorf(codes.AlreadyExists, "actor %q already exists", k)
	}
	if ref := in.GetActorTemplate(); ref != nil {
		if _, ok := s.templates[key(ref.GetAtespace(), ref.GetName())]; !ok {
			return nil, notFound("actor template", key(ref.GetAtespace(), ref.GetName()))
		}
	}
	a := &ateapi.Actor{
		Metadata:       s.newMeta(md.GetAtespace(), md.GetName()),
		ActorTemplate:  clone(in.GetActorTemplate()),
		WorkerSelector: clone(in.GetWorkerSelector()),
		SourceTag:      clone(in.GetSourceTag()),
		Status:         &ateapi.ActorStatus{State: ateapi.ActorState_ACTOR_STATE_SUSPENDED},
	}
	s.actors[k] = a
	return clone(a), nil
}

func (s *Server) UpdateActor(_ context.Context, req *ateapi.UpdateActorRequest) (*ateapi.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := req.GetActor()
	md := in.GetMetadata()
	a, err := s.actor(&ateapi.ObjectRef{Atespace: md.GetAtespace(), Name: md.GetName()})
	if err != nil {
		return nil, err
	}
	if err := checkUpdate(a.GetMetadata(), md); err != nil {
		return nil, err
	}
	oldT, newT := a.GetActorTemplate(), in.GetActorTemplate()
	if oldT.GetAtespace() != newT.GetAtespace() || oldT.GetName() != newT.GetName() {
		if st := a.GetStatus().GetState(); st != ateapi.ActorState_ACTOR_STATE_SUSPENDED {
			return nil, status.Errorf(codes.FailedPrecondition, "actor must be SUSPENDED to change its template (it is %s)", stateName(st))
		}
		if newT != nil {
			if _, ok := s.templates[key(newT.GetAtespace(), newT.GetName())]; !ok {
				return nil, notFound("actor template", key(newT.GetAtespace(), newT.GetName()))
			}
		}
		a.ActorTemplate = clone(newT)
	}
	a.WorkerSelector = clone(in.GetWorkerSelector())
	s.touch(a.Metadata)
	return clone(a), nil
}

func stateName(st ateapi.ActorState) string {
	n := st.String()
	if len(n) > len("ACTOR_STATE_") {
		return n[len("ACTOR_STATE_"):]
	}
	return n
}

// transition moves a to its final state, or in async mode to intermediate
// with finish deferred to Settle.
func (s *Server) transition(a *ateapi.Actor, intermediate ateapi.ActorState, finish func(*ateapi.Actor)) {
	k := key(a.GetMetadata().GetAtespace(), a.GetMetadata().GetName())
	if s.async {
		a.Status.State = intermediate
		s.pending[k] = finish
	} else {
		finish(a)
	}
	s.touch(a.Metadata)
}

func transitional(st ateapi.ActorState) bool {
	switch st {
	case ateapi.ActorState_ACTOR_STATE_RESUMING, ateapi.ActorState_ACTOR_STATE_SUSPENDING,
		ateapi.ActorState_ACTOR_STATE_REVERTING, ateapi.ActorState_ACTOR_STATE_DELETING:
		return true
	}
	return false
}

func (s *Server) ResumeActor(_ context.Context, req *ateapi.ResumeActorRequest) (*ateapi.ResumeActorResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	if a.Status == nil {
		a.Status = &ateapi.ActorStatus{}
	}
	st := a.GetStatus().GetState()
	if st == ateapi.ActorState_ACTOR_STATE_RUNNING {
		return &ateapi.ResumeActorResponse{Actor: clone(a), Resumed: false}, nil
	}
	if transitional(st) {
		return nil, status.Errorf(codes.FailedPrecondition, "actor is %s", stateName(st))
	}
	s.transition(a, ateapi.ActorState_ACTOR_STATE_RESUMING, func(a *ateapi.Actor) {
		a.Status.State = ateapi.ActorState_ACTOR_STATE_RUNNING
		a.Status.Crash = nil
		if w := s.pickWorker(); w != nil {
			s.assign(a, w)
		}
	})
	return &ateapi.ResumeActorResponse{Actor: clone(a), Resumed: true}, nil
}

func (s *Server) SuspendActor(_ context.Context, req *ateapi.SuspendActorRequest) (*ateapi.SuspendActorResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	switch st := a.GetStatus().GetState(); st {
	case ateapi.ActorState_ACTOR_STATE_SUSPENDED:
		return &ateapi.SuspendActorResponse{Actor: clone(a)}, nil
	case ateapi.ActorState_ACTOR_STATE_RUNNING:
	default:
		return nil, status.Errorf(codes.FailedPrecondition, "actor must be RUNNING to suspend (it is %s)", stateName(st))
	}
	storage := ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_PENDING
	if req.GetOptions().GetMode() == ateapi.DurabilityMode_DURABILITY_MODE_SYNCHRONOUS {
		storage = ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED
	}
	s.transition(a, ateapi.ActorState_ACTOR_STATE_SUSPENDING, func(a *ateapi.Actor) {
		a.Status.State = ateapi.ActorState_ACTOR_STATE_SUSPENDED
		s.unassign(a)
		s.addSnapshot(a, storage)
	})
	return &ateapi.SuspendActorResponse{Actor: clone(a)}, nil
}

func (s *Server) RevertActor(_ context.Context, req *ateapi.RevertActorRequest) (*ateapi.RevertActorResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	switch st := a.GetStatus().GetState(); st {
	case ateapi.ActorState_ACTOR_STATE_RUNNING, ateapi.ActorState_ACTOR_STATE_CRASHED, ateapi.ActorState_ACTOR_STATE_SUSPENDED:
	default:
		return nil, status.Errorf(codes.FailedPrecondition, "actor cannot be reverted while %s", stateName(st))
	}
	s.transition(a, ateapi.ActorState_ACTOR_STATE_REVERTING, func(a *ateapi.Actor) {
		a.Status.State = ateapi.ActorState_ACTOR_STATE_SUSPENDED
		a.Status.Crash = nil
		s.unassign(a)
	})
	return &ateapi.RevertActorResponse{Actor: clone(a)}, nil
}

func (s *Server) DeleteActor(_ context.Context, req *ateapi.DeleteActorRequest) (*ateapi.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	if err := checkDelete(a.GetMetadata(), req.GetOptions()); err != nil {
		return nil, err
	}
	if a.Status == nil {
		a.Status = &ateapi.ActorStatus{}
	}
	k := key(a.GetMetadata().GetAtespace(), a.GetMetadata().GetName())
	if !s.async {
		s.unassign(a)
		delete(s.actors, k)
		delete(s.egress, k)
		delete(s.pending, k)
		a.Metadata.DeleteTime = timestamppb.New(s.now)
		return clone(a), nil
	}
	a.Status.State = ateapi.ActorState_ACTOR_STATE_DELETING
	a.Metadata.DeleteTime = timestamppb.New(s.now)
	s.touch(a.Metadata)
	s.pending[k] = func(a *ateapi.Actor) {
		s.unassign(a)
		delete(s.actors, k)
		delete(s.egress, k)
	}
	return clone(a), nil
}

// --- egress policies -------------------------------------------------------

func (s *Server) GetActorEgressPolicy(_ context.Context, req *ateapi.GetActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkSingletonName(req.GetName()); err != nil {
		return nil, err
	}
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	k := key(a.GetMetadata().GetAtespace(), a.GetMetadata().GetName())
	p, ok := s.egress[k]
	if !ok {
		return nil, notFound("egress policy of actor", k)
	}
	return clone(p), nil
}

func (s *Server) CreateActorEgressPolicy(_ context.Context, req *ateapi.CreateActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	in := req.GetEgressPolicy()
	if err := checkSingletonName(in.GetMetadata().GetName()); err != nil {
		return nil, err
	}
	ns := a.GetMetadata().GetAtespace()
	if in.GetMetadata().GetAtespace() != ns {
		return nil, status.Errorf(codes.InvalidArgument, "metadata.atespace %q must match the actor's atespace %q", in.GetMetadata().GetAtespace(), ns)
	}
	k := key(ns, a.GetMetadata().GetName())
	if _, ok := s.egress[k]; ok {
		return nil, status.Errorf(codes.AlreadyExists, "egress policy of actor %q already exists", k)
	}
	p := &ateapi.EgressPolicy{Metadata: s.newMeta(ns, "default"), Rules: clone(in).GetRules()}
	s.egress[k] = p
	return clone(p), nil
}

func (s *Server) UpdateActorEgressPolicy(_ context.Context, req *ateapi.UpdateActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	k := key(a.GetMetadata().GetAtespace(), a.GetMetadata().GetName())
	p, ok := s.egress[k]
	if !ok {
		return nil, notFound("egress policy of actor", k)
	}
	in := req.GetEgressPolicy()
	if err := checkUpdate(p.GetMetadata(), in.GetMetadata()); err != nil {
		return nil, err
	}
	if in.GetMetadata().GetName() != p.GetMetadata().GetName() || in.GetMetadata().GetAtespace() != p.GetMetadata().GetAtespace() {
		return nil, status.Error(codes.InvalidArgument, "metadata.atespace and metadata.name are immutable")
	}
	p.Rules = clone(in).GetRules()
	s.touch(p.Metadata)
	return clone(p), nil
}

func (s *Server) DeleteActorEgressPolicy(_ context.Context, req *ateapi.DeleteActorEgressPolicyRequest) (*ateapi.EgressPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkSingletonName(req.GetName()); err != nil {
		return nil, err
	}
	a, err := s.actor(req.GetActor())
	if err != nil {
		return nil, err
	}
	k := key(a.GetMetadata().GetAtespace(), a.GetMetadata().GetName())
	p, ok := s.egress[k]
	if !ok {
		return nil, notFound("egress policy of actor", k)
	}
	if err := checkDelete(p.GetMetadata(), req.GetOptions()); err != nil {
		return nil, err
	}
	delete(s.egress, k)
	return clone(p), nil
}

// --- tags ------------------------------------------------------------------

func (s *Server) ListTags(_ context.Context, req *ateapi.ListTagsRequest) (*ateapi.ListTagsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns := req.GetAtespace()
	if ns != "" {
		if err := s.checkAtespace(ns); err != nil {
			return nil, err
		}
	}
	items, next, err := listPage(s, s.tags, func(t *ateapi.Tag) bool {
		return ns == "" || t.GetMetadata().GetAtespace() == ns
	}, req.GetOptions())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListTagsResponse{Tags: items, NextPageToken: next}, nil
}

func (s *Server) GetTag(_ context.Context, req *ateapi.GetTagRequest) (*ateapi.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(req.GetTag().GetAtespace(), req.GetTag().GetName())
	t, ok := s.tags[k]
	if !ok {
		return nil, notFound("tag", k)
	}
	return clone(t), nil
}

func (s *Server) CreateTag(_ context.Context, req *ateapi.CreateTagRequest) (*ateapi.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := req.GetTag()
	md := in.GetMetadata()
	if err := s.checkAtespace(md.GetAtespace()); err != nil {
		return nil, err
	}
	src := in.GetSourceActor()
	if src.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_actor is required")
	}
	if src.GetAtespace() != md.GetAtespace() {
		return nil, status.Errorf(codes.InvalidArgument, "metadata.atespace %q must match source_actor's atespace %q", md.GetAtespace(), src.GetAtespace())
	}
	k := key(md.GetAtespace(), md.GetName())
	if _, ok := s.tags[k]; ok {
		return nil, status.Errorf(codes.AlreadyExists, "tag %q already exists", k)
	}
	a, err := s.actor(src)
	if err != nil {
		return nil, err
	}
	if st := a.GetStatus().GetState(); st != ateapi.ActorState_ACTOR_STATE_SUSPENDED {
		return nil, status.Errorf(codes.FailedPrecondition, "actor must be SUSPENDED to tag its snapshot (it is %s)", stateName(st))
	}
	snaps := a.GetStatus().GetSnapshots()
	if len(snaps) == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "actor %q holds no snapshot", key(src.GetAtespace(), src.GetName()))
	}
	snap := clone(snaps[len(snaps)-1])
	snap.Uuid = s.nextUID()
	snap.Owner = ateapi.SnapshotOwner_SNAPSHOT_OWNER_TAG
	scope := in.GetScope()
	if scope == ateapi.TagScope_TAG_SCOPE_UNSPECIFIED {
		scope = ateapi.TagScope_TAG_SCOPE_ATESPACE
	}
	t := &ateapi.Tag{
		Metadata:    s.newMeta(md.GetAtespace(), md.GetName()),
		Scope:       scope,
		SourceActor: clone(src),
		Status:      &ateapi.TagStatus{Snapshot: snap, ActorTemplateUid: snap.GetActorTemplateUid()},
	}
	s.tags[k] = t
	return clone(t), nil
}

func (s *Server) UpdateTag(_ context.Context, req *ateapi.UpdateTagRequest) (*ateapi.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	md := req.GetTag().GetMetadata()
	k := key(md.GetAtespace(), md.GetName())
	t, ok := s.tags[k]
	if !ok {
		return nil, notFound("tag", k)
	}
	if err := checkUpdate(t.GetMetadata(), md); err != nil {
		return nil, err
	}
	switch sc := req.GetTag().GetScope(); sc {
	case ateapi.TagScope_TAG_SCOPE_ATESPACE, ateapi.TagScope_TAG_SCOPE_PUBLISHED:
		t.Scope = sc
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid scope %s", sc)
	}
	s.touch(t.Metadata)
	return clone(t), nil
}

func (s *Server) DeleteTag(_ context.Context, req *ateapi.DeleteTagRequest) (*ateapi.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(req.GetTag().GetAtespace(), req.GetTag().GetName())
	t, ok := s.tags[k]
	if !ok {
		return nil, notFound("tag", k)
	}
	if err := checkDelete(t.GetMetadata(), req.GetOptions()); err != nil {
		return nil, err
	}
	delete(s.tags, k)
	return clone(t), nil
}

// --- workers ---------------------------------------------------------------

func (s *Server) ListWorkers(_ context.Context, req *ateapi.ListWorkersRequest) (*ateapi.ListWorkersResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, next, err := listPage(s, s.workers, nil, req.GetOptions())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListWorkersResponse{Workers: items, NextPageToken: next}, nil
}

func (s *Server) worker(ref *ateapi.ObjectRef) (*ateapi.Worker, error) {
	w, ok := s.workers[ref.GetName()]
	if !ok {
		return nil, notFound("worker", ref.GetName())
	}
	return w, nil
}

func (s *Server) GetWorker(_ context.Context, req *ateapi.GetWorkerRequest) (*ateapi.Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.worker(req.GetWorker())
	if err != nil {
		return nil, err
	}
	return clone(w), nil
}

func (s *Server) DrainWorker(_ context.Context, req *ateapi.DrainWorkerRequest) (*ateapi.DrainWorkerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.worker(req.GetWorker())
	if err != nil {
		return nil, err
	}
	if w.Status == nil {
		w.Status = &ateapi.WorkerStatus{}
	}
	if w.Status.State != ateapi.WorkerState_WORKER_STATE_DRAINING {
		w.Status.State = ateapi.WorkerState_WORKER_STATE_DRAINING
		s.touch(w.Metadata)
	}
	return &ateapi.DrainWorkerResponse{Worker: clone(w)}, nil
}

func (s *Server) setWorkerState(ref *ateapi.ObjectRef, state ateapi.WorkerState) (*ateapi.Worker, error) {
	w, err := s.worker(ref)
	if err != nil {
		return nil, err
	}
	if w.Status == nil {
		w.Status = &ateapi.WorkerStatus{}
	}
	if w.Status.State == ateapi.WorkerState_WORKER_STATE_DRAINING {
		return nil, status.Errorf(codes.FailedPrecondition, "worker %q is DRAINING", ref.GetName())
	}
	if w.Status.State != state {
		w.Status.State = state
		s.touch(w.Metadata)
	}
	return clone(w), nil
}

func (s *Server) MarkWorkerAvailable(_ context.Context, req *ateapi.MarkWorkerAvailableRequest) (*ateapi.MarkWorkerAvailableResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.setWorkerState(req.GetWorker(), ateapi.WorkerState_WORKER_STATE_ACTIVE)
	if err != nil {
		return nil, err
	}
	return &ateapi.MarkWorkerAvailableResponse{Worker: w}, nil
}

func (s *Server) MarkWorkerUnavailable(_ context.Context, req *ateapi.MarkWorkerUnavailableRequest) (*ateapi.MarkWorkerUnavailableResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.setWorkerState(req.GetWorker(), ateapi.WorkerState_WORKER_STATE_UNAVAILABLE)
	if err != nil {
		return nil, err
	}
	return &ateapi.MarkWorkerUnavailableResponse{Worker: w}, nil
}

func (s *Server) ListWorkerActorAssignments(_ context.Context, req *ateapi.ListWorkerActorAssignmentsRequest) (*ateapi.ListWorkerActorAssignmentsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.worker(req.GetWorker())
	if err != nil {
		return nil, err
	}
	items, next, err := listPage(s, s.assignments[w.GetMetadata().GetName()], nil, req.GetOptions())
	if err != nil {
		return nil, err
	}
	return &ateapi.ListWorkerActorAssignmentsResponse{ActorAssignments: items, NextPageToken: next}, nil
}

// --- access policies -------------------------------------------------------

func (s *Server) GetGlobalAccessPolicy(_ context.Context, req *ateapi.GetGlobalAccessPolicyRequest) (*ateapi.AccessPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkSingletonName(req.GetName()); err != nil {
		return nil, err
	}
	if s.globalAP == nil {
		return nil, status.Error(codes.NotFound, "global access policy not found")
	}
	return clone(s.globalAP), nil
}

func (s *Server) CreateGlobalAccessPolicy(_ context.Context, req *ateapi.CreateGlobalAccessPolicyRequest) (*ateapi.AccessPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := req.GetAccessPolicy()
	if err := checkSingletonName(in.GetMetadata().GetName()); err != nil {
		return nil, err
	}
	if in.GetMetadata().GetAtespace() != "" {
		return nil, status.Error(codes.InvalidArgument, "metadata.atespace must be empty")
	}
	if s.globalAP != nil {
		return nil, status.Error(codes.AlreadyExists, "global access policy already exists")
	}
	s.globalAP = &ateapi.AccessPolicy{Metadata: s.newMeta("", "default"), Bindings: clone(in).GetBindings()}
	return clone(s.globalAP), nil
}

func (s *Server) UpdateGlobalAccessPolicy(_ context.Context, req *ateapi.UpdateGlobalAccessPolicyRequest) (*ateapi.AccessPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.globalAP == nil {
		return nil, status.Error(codes.NotFound, "global access policy not found")
	}
	in := req.GetAccessPolicy()
	if err := checkUpdate(s.globalAP.GetMetadata(), in.GetMetadata()); err != nil {
		return nil, err
	}
	s.globalAP.Bindings = clone(in).GetBindings()
	s.touch(s.globalAP.Metadata)
	return clone(s.globalAP), nil
}

func (s *Server) GetAtespaceAccessPolicy(_ context.Context, req *ateapi.GetAtespaceAccessPolicyRequest) (*ateapi.AccessPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkSingletonName(req.GetName()); err != nil {
		return nil, err
	}
	ns := req.GetAtespace().GetName()
	if err := s.checkAtespace(ns); err != nil {
		return nil, err
	}
	p, ok := s.atespaceAP[ns]
	if !ok {
		return nil, notFound("access policy of atespace", ns)
	}
	return clone(p), nil
}

func (s *Server) CreateAtespaceAccessPolicy(_ context.Context, req *ateapi.CreateAtespaceAccessPolicyRequest) (*ateapi.AccessPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns := req.GetAtespace().GetName()
	if err := s.checkAtespace(ns); err != nil {
		return nil, err
	}
	in := req.GetAccessPolicy()
	if err := checkSingletonName(in.GetMetadata().GetName()); err != nil {
		return nil, err
	}
	if in.GetMetadata().GetAtespace() != ns {
		return nil, status.Errorf(codes.InvalidArgument, "metadata.atespace %q must match %q", in.GetMetadata().GetAtespace(), ns)
	}
	if _, ok := s.atespaceAP[ns]; ok {
		return nil, status.Errorf(codes.AlreadyExists, "access policy of atespace %q already exists", ns)
	}
	p := &ateapi.AccessPolicy{Metadata: s.newMeta(ns, "default"), Bindings: clone(in).GetBindings()}
	s.atespaceAP[ns] = p
	return clone(p), nil
}

func (s *Server) UpdateAtespaceAccessPolicy(_ context.Context, req *ateapi.UpdateAtespaceAccessPolicyRequest) (*ateapi.AccessPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns := req.GetAtespace().GetName()
	if err := s.checkAtespace(ns); err != nil {
		return nil, err
	}
	p, ok := s.atespaceAP[ns]
	if !ok {
		return nil, notFound("access policy of atespace", ns)
	}
	in := req.GetAccessPolicy()
	if err := checkUpdate(p.GetMetadata(), in.GetMetadata()); err != nil {
		return nil, err
	}
	p.Bindings = clone(in).GetBindings()
	s.touch(p.Metadata)
	return clone(p), nil
}

func (s *Server) DeleteAtespaceAccessPolicy(_ context.Context, req *ateapi.DeleteAtespaceAccessPolicyRequest) (*ateapi.AccessPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkSingletonName(req.GetName()); err != nil {
		return nil, err
	}
	ns := req.GetAtespace().GetName()
	if err := s.checkAtespace(ns); err != nil {
		return nil, err
	}
	p, ok := s.atespaceAP[ns]
	if !ok {
		return nil, notFound("access policy of atespace", ns)
	}
	if err := checkDelete(p.GetMetadata(), req.GetOptions()); err != nil {
		return nil, err
	}
	delete(s.atespaceAP, ns)
	return clone(p), nil
}
