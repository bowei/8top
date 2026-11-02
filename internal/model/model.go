// Package model is 8top's view of a Substrate cluster: the resource kinds it
// shows, the objects it lists, and the operations it can run on them.
package model

import (
	"context"
	"io"
)

// Kind identifies a resource type 8top can show.
type Kind string

const (
	KindAtespace        Kind = "atespaces"
	KindActor           Kind = "actors"
	KindActorTemplate   Kind = "templates"
	KindTag             Kind = "tags"
	KindEgressPolicy    Kind = "egress"
	KindWorker          Kind = "workers"
	KindActorAssignment Kind = "assignments"
	KindAccessPolicy    Kind = "accesspolicy"
	KindWorkerPool      Kind = "workerpools"
)

// Scope narrows a List call. Fields that do not apply to a Kind are ignored.
type Scope struct {
	// Atespace limits atespace-scoped kinds to one atespace. Empty means all
	// atespaces. For KindAccessPolicy, empty means the global policy.
	Atespace string
	// Actor selects the actor whose egress policy is listed (KindEgressPolicy).
	// Empty lists the egress policies of every actor in scope.
	Actor string
	// Worker selects the worker whose assignments are listed
	// (KindActorAssignment).
	Worker string
	// Namespace limits KindWorkerPool to one Kubernetes namespace. Empty means
	// all namespaces.
	Namespace string
}

// Object is one resource as returned by the API.
type Object struct {
	Kind Kind
	// Key is unique within a List result and stable across refreshes, so the
	// UI can keep the selection on the same row.
	Key string
	// Atespace and Name identify the object. Atespace is empty for global
	// resources. For KindEgressPolicy, Name is the actor's name.
	Atespace string
	Name     string
	// Raw is the API object: a *ateapi.<Message> for Substrate resources and
	// a *atev1.WorkerPool for worker pools.
	Raw any
}

// API is everything the UI needs from the cluster. *Store implements it
// against a live Substrate API server; tests use fakes.
type API interface {
	// List returns the objects of kind in scope, following page tokens.
	List(ctx context.Context, kind Kind, scope Scope) ([]Object, error)
	// Get re-reads one object.
	Get(ctx context.Context, o Object) (Object, error)
	// Update writes o.Raw back with the matching Update* call. o.Raw must
	// carry the metadata uid/version it was read with.
	Update(ctx context.Context, o Object) (Object, error)
	// Delete deletes o with the matching Delete* call.
	Delete(ctx context.Context, o Object) error

	ResumeActor(ctx context.Context, atespace, name string) error
	// SuspendActor suspends an actor. If wait is true it waits for the
	// snapshot to reach storage (--mode=sync).
	SuspendActor(ctx context.Context, atespace, name string, wait bool) error
	RevertActor(ctx context.Context, atespace, name string) error
	// CreateTag tags the snapshot held by a suspended actor.
	CreateTag(ctx context.Context, atespace, tag, actor string) error

	DrainWorker(ctx context.Context, worker string) error
	SetWorkerAvailable(ctx context.Context, worker string, available bool) error

	// StreamLogs streams the logs of a running actor from its worker pod.
	// The caller closes the reader.
	StreamLogs(ctx context.Context, atespace, actor string, follow bool) (io.ReadCloser, error)
}
