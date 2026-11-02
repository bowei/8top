package model

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	atev1 "go.ate.dev/substrate/api/infra/k8s/ate/v1"
	"go.ate.dev/substrate/pkg/client/clientset/versioned"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// singletonName is the name of every singleton sub-resource (egress and
// access policies).
const singletonName = "default"

// listPageSize is the page size Store asks for. The server may return fewer.
const listPageSize = 500

// Store implements API against a live Substrate API server, the Kubernetes
// API (for worker pod logs) and the WorkerPool CRD clientset.
//
// Object conventions:
//
//   - Atespace-scoped kinds: Key "atespace/name".
//   - KindAtespace, KindWorker: Key = Name, Atespace empty.
//   - KindEgressPolicy: Key "atespace/actor", Name = actor name.
//   - KindAccessPolicy: Name "default"; Atespace empty for the global policy.
//     Key "global" or the atespace name.
//   - KindActorAssignment: Atespace/Name are the assigned actor's; Key is
//     "worker/actorUID".
//   - KindWorkerPool: Key "namespace/name", Name = pool name, Atespace empty.
//     The namespace is read from Raw.
type Store struct {
	ate   ateapi.ControlClient
	kube  kubernetes.Interface
	pools versioned.Interface
}

var _ API = (*Store)(nil)

// NewStore returns a Store. kube and pools may be nil; StreamLogs and
// KindWorkerPool then return an error.
func NewStore(ate ateapi.ControlClient, kube kubernetes.Interface, pools versioned.Interface) *Store {
	return &Store{ate: ate, kube: kube, pools: pools}
}

// apiError renders a gRPC status as "Code: message", one line, suitable for
// the flash line. It keeps the status, so status.Code(err) still works.
type apiError struct{ st *status.Status }

func (e *apiError) Error() string              { return e.st.Code().String() + ": " + e.st.Message() }
func (e *apiError) GRPCStatus() *status.Status { return e.st }

// wrap turns err into an apiError when it carries a gRPC status.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return err
	}
	if st, ok := status.FromError(err); ok {
		return &apiError{st}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &apiError{status.FromContextError(err)}
	}
	return err
}

func errorf(c codes.Code, format string, args ...any) error {
	return &apiError{status.Newf(c, format, args...)}
}

func isNotFound(err error) bool { return status.Code(err) == codes.NotFound }

func ref(atespace, name string) *ateapi.ObjectRef {
	return &ateapi.ObjectRef{Atespace: atespace, Name: name}
}

// collect follows page tokens. fetch returns one page and the next token.
func collect[T any](ctx context.Context, fetch func(ctx context.Context, opts *ateapi.ListOptions) ([]T, string, error)) ([]T, error) {
	var out []T
	token := ""
	seen := map[string]bool{}
	for {
		items, next, err := fetch(ctx, &ateapi.ListOptions{PageSize: listPageSize, PageToken: token})
		if err != nil {
			return nil, wrap(err)
		}
		out = append(out, items...)
		if next == "" {
			return out, nil
		}
		if seen[next] {
			return nil, errorf(codes.Internal, "server repeated page token %q", next)
		}
		seen[next] = true
		token = next
	}
}

// --- object constructors ---------------------------------------------------

func atespacedKey(atespace, name string) string { return atespace + "/" + name }

func objAtespace(a *ateapi.Atespace) Object {
	n := a.GetMetadata().GetName()
	return Object{Kind: KindAtespace, Key: n, Name: n, Raw: a}
}

func objActor(a *ateapi.Actor) Object {
	md := a.GetMetadata()
	return Object{Kind: KindActor, Key: atespacedKey(md.GetAtespace(), md.GetName()), Atespace: md.GetAtespace(), Name: md.GetName(), Raw: a}
}

func objTemplate(t *ateapi.ActorTemplate) Object {
	md := t.GetMetadata()
	return Object{Kind: KindActorTemplate, Key: atespacedKey(md.GetAtespace(), md.GetName()), Atespace: md.GetAtespace(), Name: md.GetName(), Raw: t}
}

func objTag(t *ateapi.Tag) Object {
	md := t.GetMetadata()
	return Object{Kind: KindTag, Key: atespacedKey(md.GetAtespace(), md.GetName()), Atespace: md.GetAtespace(), Name: md.GetName(), Raw: t}
}

func objEgress(atespace, actor string, p *ateapi.EgressPolicy) Object {
	return Object{Kind: KindEgressPolicy, Key: atespacedKey(atespace, actor), Atespace: atespace, Name: actor, Raw: p}
}

func objWorker(w *ateapi.Worker) Object {
	n := w.GetMetadata().GetName()
	return Object{Kind: KindWorker, Key: n, Name: n, Raw: w}
}

func objAssignment(worker string, a *ateapi.ActorAssignment) Object {
	return Object{
		Kind:     KindActorAssignment,
		Key:      worker + "/" + a.GetMetadata().GetName(),
		Atespace: a.GetActor().GetAtespace(),
		Name:     a.GetActor().GetName(),
		Raw:      a,
	}
}

func objAccessPolicy(atespace string, p *ateapi.AccessPolicy) Object {
	k := atespace
	if k == "" {
		k = "global"
	}
	return Object{Kind: KindAccessPolicy, Key: k, Atespace: atespace, Name: singletonName, Raw: p}
}

func objWorkerPool(p *atev1.WorkerPool) Object {
	return Object{Kind: KindWorkerPool, Key: p.Namespace + "/" + p.Name, Name: p.Name, Raw: p}
}

// --- List ------------------------------------------------------------------

// List implements API.
func (s *Store) List(ctx context.Context, kind Kind, scope Scope) ([]Object, error) {
	switch kind {
	case KindAtespace:
		items, err := collect(ctx, func(ctx context.Context, o *ateapi.ListOptions) ([]*ateapi.Atespace, string, error) {
			r, err := s.ate.ListAtespaces(ctx, &ateapi.ListAtespacesRequest{Options: o})
			return r.GetAtespaces(), r.GetNextPageToken(), err
		})
		return mapObjs(items, objAtespace), err
	case KindActor:
		items, err := s.listActors(ctx, scope.Atespace)
		return mapObjs(items, objActor), err
	case KindActorTemplate:
		items, err := collect(ctx, func(ctx context.Context, o *ateapi.ListOptions) ([]*ateapi.ActorTemplate, string, error) {
			r, err := s.ate.ListActorTemplates(ctx, &ateapi.ListActorTemplatesRequest{Atespace: scope.Atespace, Options: o})
			return r.GetActorTemplates(), r.GetNextPageToken(), err
		})
		return mapObjs(items, objTemplate), err
	case KindTag:
		items, err := collect(ctx, func(ctx context.Context, o *ateapi.ListOptions) ([]*ateapi.Tag, string, error) {
			r, err := s.ate.ListTags(ctx, &ateapi.ListTagsRequest{Atespace: scope.Atespace, Options: o})
			return r.GetTags(), r.GetNextPageToken(), err
		})
		return mapObjs(items, objTag), err
	case KindEgressPolicy:
		return s.listEgress(ctx, scope)
	case KindWorker:
		items, err := s.listWorkers(ctx)
		return mapObjs(items, objWorker), err
	case KindActorAssignment:
		return s.listAssignments(ctx, scope.Worker)
	case KindAccessPolicy:
		p, err := s.getAccessPolicy(ctx, scope.Atespace)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []Object{objAccessPolicy(scope.Atespace, p)}, nil
	case KindWorkerPool:
		if s.pools == nil {
			return nil, errors.New("worker pools: no Kubernetes client")
		}
		var out []Object
		opts := metav1.ListOptions{Limit: listPageSize}
		for {
			l, err := s.pools.AteV1().WorkerPools(scope.Namespace).List(ctx, opts)
			if err != nil {
				return nil, err
			}
			for i := range l.Items {
				out = append(out, objWorkerPool(&l.Items[i]))
			}
			if l.Continue == "" {
				return out, nil
			}
			opts.Continue = l.Continue
		}
	}
	return nil, errorf(codes.InvalidArgument, "unknown kind %q", kind)
}

func mapObjs[T any](items []T, f func(T) Object) []Object {
	if items == nil {
		return nil
	}
	out := make([]Object, len(items))
	for i, it := range items {
		out[i] = f(it)
	}
	return out
}

func (s *Store) listActors(ctx context.Context, atespace string) ([]*ateapi.Actor, error) {
	return collect(ctx, func(ctx context.Context, o *ateapi.ListOptions) ([]*ateapi.Actor, string, error) {
		r, err := s.ate.ListActors(ctx, &ateapi.ListActorsRequest{Atespace: atespace, Options: o})
		return r.GetActors(), r.GetNextPageToken(), err
	})
}

func (s *Store) listWorkers(ctx context.Context) ([]*ateapi.Worker, error) {
	return collect(ctx, func(ctx context.Context, o *ateapi.ListOptions) ([]*ateapi.Worker, string, error) {
		r, err := s.ate.ListWorkers(ctx, &ateapi.ListWorkersRequest{Options: o})
		return r.GetWorkers(), r.GetNextPageToken(), err
	})
}

// listEgress lists the egress policy of scope.Actor, or of every actor in
// scope.Atespace when Actor is empty. Actors without a policy are skipped.
func (s *Store) listEgress(ctx context.Context, scope Scope) ([]Object, error) {
	if scope.Actor != "" {
		if scope.Atespace == "" {
			return nil, errorf(codes.InvalidArgument, "an atespace is required to show the egress policy of actor %q", scope.Actor)
		}
		p, err := s.getEgress(ctx, scope.Atespace, scope.Actor)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []Object{objEgress(scope.Atespace, scope.Actor, p)}, nil
	}
	actors, err := s.listActors(ctx, scope.Atespace)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, a := range actors {
		ns, name := a.GetMetadata().GetAtespace(), a.GetMetadata().GetName()
		p, err := s.getEgress(ctx, ns, name)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, objEgress(ns, name, p))
	}
	return out, nil
}

// listAssignments lists the assignments of worker, or of every worker when
// worker is empty.
func (s *Store) listAssignments(ctx context.Context, worker string) ([]Object, error) {
	workers := []string{worker}
	if worker == "" {
		ws, err := s.listWorkers(ctx)
		if err != nil {
			return nil, err
		}
		workers = workers[:0]
		for _, w := range ws {
			workers = append(workers, w.GetMetadata().GetName())
		}
	}
	var out []Object
	for _, w := range workers {
		items, err := collect(ctx, func(ctx context.Context, o *ateapi.ListOptions) ([]*ateapi.ActorAssignment, string, error) {
			r, err := s.ate.ListWorkerActorAssignments(ctx, &ateapi.ListWorkerActorAssignmentsRequest{Worker: ref("", w), Options: o})
			return r.GetActorAssignments(), r.GetNextPageToken(), err
		})
		if err != nil {
			return nil, err
		}
		for _, a := range items {
			out = append(out, objAssignment(w, a))
		}
	}
	return out, nil
}

func (s *Store) getEgress(ctx context.Context, atespace, actor string) (*ateapi.EgressPolicy, error) {
	p, err := s.ate.GetActorEgressPolicy(ctx, &ateapi.GetActorEgressPolicyRequest{Actor: ref(atespace, actor), Name: singletonName})
	return p, wrap(err)
}

func (s *Store) getAccessPolicy(ctx context.Context, atespace string) (*ateapi.AccessPolicy, error) {
	if atespace == "" {
		p, err := s.ate.GetGlobalAccessPolicy(ctx, &ateapi.GetGlobalAccessPolicyRequest{Name: singletonName})
		return p, wrap(err)
	}
	p, err := s.ate.GetAtespaceAccessPolicy(ctx, &ateapi.GetAtespaceAccessPolicyRequest{Atespace: ref("", atespace), Name: singletonName})
	return p, wrap(err)
}

// --- Get / Update / Delete -------------------------------------------------

// workerPoolRef returns the namespace and name of a worker pool object.
func workerPoolRef(o Object) (ns, name string) {
	if p, ok := o.Raw.(*atev1.WorkerPool); ok && p != nil {
		return p.Namespace, p.Name
	}
	if ns, name, ok := strings.Cut(o.Key, "/"); ok {
		return ns, name
	}
	return "", o.Name
}

// Get implements API.
func (s *Store) Get(ctx context.Context, o Object) (Object, error) {
	switch o.Kind {
	case KindAtespace:
		a, err := s.ate.GetAtespace(ctx, &ateapi.GetAtespaceRequest{Atespace: ref("", o.Name)})
		if err != nil {
			return Object{}, wrap(err)
		}
		return objAtespace(a), nil
	case KindActor:
		a, err := s.ate.GetActor(ctx, &ateapi.GetActorRequest{Actor: ref(o.Atespace, o.Name)})
		if err != nil {
			return Object{}, wrap(err)
		}
		return objActor(a), nil
	case KindActorTemplate:
		t, err := s.ate.GetActorTemplate(ctx, &ateapi.GetActorTemplateRequest{ActorTemplate: ref(o.Atespace, o.Name)})
		if err != nil {
			return Object{}, wrap(err)
		}
		return objTemplate(t), nil
	case KindTag:
		t, err := s.ate.GetTag(ctx, &ateapi.GetTagRequest{Tag: ref(o.Atespace, o.Name)})
		if err != nil {
			return Object{}, wrap(err)
		}
		return objTag(t), nil
	case KindEgressPolicy:
		p, err := s.getEgress(ctx, o.Atespace, o.Name)
		if err != nil {
			return Object{}, err
		}
		return objEgress(o.Atespace, o.Name, p), nil
	case KindWorker:
		w, err := s.ate.GetWorker(ctx, &ateapi.GetWorkerRequest{Worker: ref("", o.Name)})
		if err != nil {
			return Object{}, wrap(err)
		}
		return objWorker(w), nil
	case KindActorAssignment:
		// There is no GetActorAssignment; re-list the worker's assignments.
		worker, _, _ := strings.Cut(o.Key, "/")
		objs, err := s.listAssignments(ctx, worker)
		if err != nil {
			return Object{}, err
		}
		for _, x := range objs {
			if x.Key == o.Key {
				return x, nil
			}
		}
		return Object{}, errorf(codes.NotFound, "assignment %q not found", o.Key)
	case KindAccessPolicy:
		p, err := s.getAccessPolicy(ctx, o.Atespace)
		if err != nil {
			return Object{}, err
		}
		return objAccessPolicy(o.Atespace, p), nil
	case KindWorkerPool:
		if s.pools == nil {
			return Object{}, errors.New("worker pools: no Kubernetes client")
		}
		ns, name := workerPoolRef(o)
		p, err := s.pools.AteV1().WorkerPools(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Object{}, err
		}
		return objWorkerPool(p), nil
	}
	return Object{}, errorf(codes.InvalidArgument, "unknown kind %q", o.Kind)
}

// Update implements API. o.Raw is sent as-is: the Update* requests replace
// the resource and carry no field mask, and the server ignores read-only
// fields (and checks metadata.uid/version, failing with Aborted on a
// conflict).
func (s *Store) Update(ctx context.Context, o Object) (Object, error) {
	switch o.Kind {
	case KindActor:
		a, ok := o.Raw.(*ateapi.Actor)
		if !ok {
			return Object{}, rawErr(o)
		}
		got, err := s.ate.UpdateActor(ctx, &ateapi.UpdateActorRequest{Actor: a})
		if err != nil {
			return Object{}, wrap(err)
		}
		return objActor(got), nil
	case KindTag:
		t, ok := o.Raw.(*ateapi.Tag)
		if !ok {
			return Object{}, rawErr(o)
		}
		got, err := s.ate.UpdateTag(ctx, &ateapi.UpdateTagRequest{Tag: t})
		if err != nil {
			return Object{}, wrap(err)
		}
		return objTag(got), nil
	case KindEgressPolicy:
		p, ok := o.Raw.(*ateapi.EgressPolicy)
		if !ok {
			return Object{}, rawErr(o)
		}
		got, err := s.ate.UpdateActorEgressPolicy(ctx, &ateapi.UpdateActorEgressPolicyRequest{Actor: ref(o.Atespace, o.Name), EgressPolicy: p})
		if err != nil {
			return Object{}, wrap(err)
		}
		return objEgress(o.Atespace, o.Name, got), nil
	case KindAccessPolicy:
		p, ok := o.Raw.(*ateapi.AccessPolicy)
		if !ok {
			return Object{}, rawErr(o)
		}
		var got *ateapi.AccessPolicy
		var err error
		if o.Atespace == "" {
			got, err = s.ate.UpdateGlobalAccessPolicy(ctx, &ateapi.UpdateGlobalAccessPolicyRequest{AccessPolicy: p})
		} else {
			got, err = s.ate.UpdateAtespaceAccessPolicy(ctx, &ateapi.UpdateAtespaceAccessPolicyRequest{Atespace: ref("", o.Atespace), AccessPolicy: p})
		}
		if err != nil {
			return Object{}, wrap(err)
		}
		return objAccessPolicy(o.Atespace, got), nil
	case KindWorkerPool:
		p, ok := o.Raw.(*atev1.WorkerPool)
		if !ok {
			return Object{}, rawErr(o)
		}
		if s.pools == nil {
			return Object{}, errors.New("worker pools: no Kubernetes client")
		}
		got, err := s.pools.AteV1().WorkerPools(p.Namespace).Update(ctx, p, metav1.UpdateOptions{})
		if err != nil {
			return Object{}, err
		}
		return objWorkerPool(got), nil
	}
	return Object{}, errorf(codes.InvalidArgument, "%s cannot be edited", o.Kind)
}

func rawErr(o Object) error {
	return errorf(codes.InvalidArgument, "%s %s: unexpected object type %T", o.Kind, o.Key, o.Raw)
}

// deleteOpts guards a delete with the uid the object was read with, so a
// delete never hits a different object recreated under the same name. The
// version is not checked: status changes bump it between refreshes.
func deleteOpts(raw any) *ateapi.DeleteOptions {
	type withMeta interface {
		GetMetadata() *ateapi.ResourceMetadata
	}
	if m, ok := raw.(withMeta); ok && m.GetMetadata().GetUid() != "" {
		return &ateapi.DeleteOptions{Uid: m.GetMetadata().GetUid()}
	}
	return nil
}

// Delete implements API.
func (s *Store) Delete(ctx context.Context, o Object) error {
	var err error
	opts := deleteOpts(o.Raw)
	switch o.Kind {
	case KindAtespace:
		_, err = s.ate.DeleteAtespace(ctx, &ateapi.DeleteAtespaceRequest{Atespace: ref("", o.Name), Options: opts})
	case KindActor:
		_, err = s.ate.DeleteActor(ctx, &ateapi.DeleteActorRequest{Actor: ref(o.Atespace, o.Name), Options: opts})
	case KindActorTemplate:
		_, err = s.ate.DeleteActorTemplate(ctx, &ateapi.DeleteActorTemplateRequest{ActorTemplate: ref(o.Atespace, o.Name), Options: opts})
	case KindTag:
		_, err = s.ate.DeleteTag(ctx, &ateapi.DeleteTagRequest{Tag: ref(o.Atespace, o.Name), Options: opts})
	case KindEgressPolicy:
		_, err = s.ate.DeleteActorEgressPolicy(ctx, &ateapi.DeleteActorEgressPolicyRequest{Actor: ref(o.Atespace, o.Name), Name: singletonName, Options: opts})
	case KindAccessPolicy:
		if o.Atespace == "" {
			return errorf(codes.InvalidArgument, "the global access policy cannot be deleted")
		}
		_, err = s.ate.DeleteAtespaceAccessPolicy(ctx, &ateapi.DeleteAtespaceAccessPolicyRequest{Atespace: ref("", o.Atespace), Name: singletonName, Options: opts})
	case KindWorkerPool:
		if s.pools == nil {
			return errors.New("worker pools: no Kubernetes client")
		}
		ns, name := workerPoolRef(o)
		return s.pools.AteV1().WorkerPools(ns).Delete(ctx, name, metav1.DeleteOptions{})
	default:
		return errorf(codes.InvalidArgument, "%s cannot be deleted", o.Kind)
	}
	return wrap(err)
}

// --- actions ---------------------------------------------------------------

// ResumeActor implements API.
func (s *Store) ResumeActor(ctx context.Context, atespace, name string) error {
	_, err := s.ate.ResumeActor(ctx, &ateapi.ResumeActorRequest{Actor: ref(atespace, name)})
	return wrap(err)
}

// SuspendActor implements API.
func (s *Store) SuspendActor(ctx context.Context, atespace, name string, wait bool) error {
	mode := ateapi.DurabilityMode_DURABILITY_MODE_ASYNCHRONOUS
	if wait {
		mode = ateapi.DurabilityMode_DURABILITY_MODE_SYNCHRONOUS
	}
	_, err := s.ate.SuspendActor(ctx, &ateapi.SuspendActorRequest{Actor: ref(atespace, name), Options: &ateapi.SuspendOptions{Mode: mode}})
	return wrap(err)
}

// RevertActor implements API.
func (s *Store) RevertActor(ctx context.Context, atespace, name string) error {
	_, err := s.ate.RevertActor(ctx, &ateapi.RevertActorRequest{Actor: ref(atespace, name)})
	return wrap(err)
}

// CreateTag implements API. The tag is created atespace-scoped.
func (s *Store) CreateTag(ctx context.Context, atespace, tag, actor string) error {
	_, err := s.ate.CreateTag(ctx, &ateapi.CreateTagRequest{Tag: &ateapi.Tag{
		Metadata:    &ateapi.ResourceMetadata{Atespace: atespace, Name: tag},
		Scope:       ateapi.TagScope_TAG_SCOPE_ATESPACE,
		SourceActor: ref(atespace, actor),
	}})
	return wrap(err)
}

// DrainWorker implements API.
func (s *Store) DrainWorker(ctx context.Context, worker string) error {
	_, err := s.ate.DrainWorker(ctx, &ateapi.DrainWorkerRequest{Worker: ref("", worker)})
	return wrap(err)
}

// SetWorkerAvailable implements API.
func (s *Store) SetWorkerAvailable(ctx context.Context, worker string, available bool) error {
	var err error
	if available {
		_, err = s.ate.MarkWorkerAvailable(ctx, &ateapi.MarkWorkerAvailableRequest{Worker: ref("", worker)})
	} else {
		_, err = s.ate.MarkWorkerUnavailable(ctx, &ateapi.MarkWorkerUnavailableRequest{Worker: ref("", worker)})
	}
	return wrap(err)
}

// --- logs ------------------------------------------------------------------

// StreamLogs implements API. Like `kubectl ate logs actor`, it reads the
// actor's worker pod logs and keeps only the JSON lines labeled with this
// actor (a worker pod can host several actors over time). Lines are passed
// through unchanged.
//
// Unlike kubectl ate, follow mode does not reattach when the actor moves to
// another worker; the stream ends and the caller can call StreamLogs again.
func (s *Store) StreamLogs(ctx context.Context, atespace, actor string, follow bool) (io.ReadCloser, error) {
	if s.kube == nil {
		return nil, errors.New("logs: no Kubernetes client")
	}
	a, err := s.ate.GetActor(ctx, &ateapi.GetActorRequest{Actor: ref(atespace, actor)})
	if err != nil {
		return nil, wrap(err)
	}
	wa := a.GetStatus().GetWorkerAssignment()
	if a.GetStatus().GetState() != ateapi.ActorState_ACTOR_STATE_RUNNING || wa.GetWorkerPod() == "" || wa.GetWorkerNamespace() == "" {
		return nil, errorf(codes.FailedPrecondition, "actor %s/%s is not running on any worker pod", atespace, actor)
	}
	rc, err := s.kube.CoreV1().Pods(wa.GetWorkerNamespace()).GetLogs(wa.GetWorkerPod(), &corev1.PodLogOptions{Follow: follow}).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("logs of pod %s/%s: %w", wa.GetWorkerNamespace(), wa.GetWorkerPod(), err)
	}
	return filterLogs(rc, atespace, actor), nil
}

// Log label keys, as set by Substrate (internal/ateattr).
const (
	logAtespaceKey = "ate.atespace"
	logActorKey    = "ate.actor.name"
)

// filterLogs returns a reader of the lines of rc that belong to the actor.
func filterLogs(rc io.ReadCloser, atespace, actor string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if !logLineMatches(line, atespace, actor) {
				continue
			}
			if _, err := pw.Write(append(slices.Clone(line), '\n')); err != nil {
				rc.Close()
				return
			}
		}
		pw.CloseWithError(sc.Err())
	}()
	return &filteredLogs{PipeReader: pr, src: rc}
}

type filteredLogs struct {
	*io.PipeReader
	src io.Closer
}

func (f *filteredLogs) Close() error {
	f.PipeReader.Close()
	return f.src.Close()
}

// logLineMatches reports whether a JSON log line is labeled with the actor.
func logLineMatches(line []byte, atespace, actor string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return false
	}
	for _, k := range []string{"logging.googleapis.com/labels", "labels"} {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var labels map[string]any
		if json.Unmarshal(raw, &labels) != nil {
			continue
		}
		if name, _ := labels[logActorKey].(string); name != "" {
			ns, _ := labels[logAtespaceKey].(string)
			return name == actor && ns == atespace
		}
	}
	return false
}
