package render

import (
	"strconv"
	"strings"
	"time"

	"go.ate.dev/8top/internal/model"
	"go.ate.dev/substrate/api/ateapi/v1"
	atev1 "go.ate.dev/substrate/api/infra/k8s/ate/v1"
)

// Every renderer accepts a Raw of the wrong type or a nil pointer: protobuf
// getters are nil-safe, so missing messages render as empty cells, and
// identity falls back to the model.Object's Atespace and Name.

// metaStatus is StatusDeleting for an object whose metadata carries a delete
// time, else StatusNone.
func metaStatus(m *ateapi.ResourceMetadata) Status {
	if m.GetDeleteTime() != nil {
		return StatusDeleting
	}
	return StatusNone
}

// --- Atespaces ---------------------------------------------------------

type atespaceRenderer struct{}

func (atespaceRenderer) Columns() []Column {
	return []Column{{"NAME", 30}, {"AGE", 8}}
}

func (atespaceRenderer) Render(o model.Object, now time.Time) Row {
	a, _ := o.Raw.(*ateapi.Atespace)
	m := a.GetMetadata()
	ageCell, ageKey := protoAge(m.GetCreateTime(), now)
	return Row{
		Key:      o.Key,
		Cells:    []string{firstNonEmpty(m.GetName(), o.Name), ageCell},
		Status:   metaStatus(m),
		SortKeys: []string{"", ageKey},
	}
}

// --- Actors ------------------------------------------------------------

type actorRenderer struct{}

func (actorRenderer) Columns() []Column {
	return []Column{
		{"ATESPACE", 16}, {"NAME", 24}, {"TEMPLATE", 28}, {"STATE", 11},
		{"WORKER POD", 28}, {"WORKER IP", 16}, {"VERSION", 8}, {"AGE", 8},
	}
}

// ActorStateStatus maps an actor state to the status used to color it.
func ActorStateStatus(s ateapi.ActorState) Status {
	switch s {
	case ateapi.ActorState_ACTOR_STATE_RUNNING:
		return StatusOK
	case ateapi.ActorState_ACTOR_STATE_RESUMING,
		ateapi.ActorState_ACTOR_STATE_SUSPENDING,
		ateapi.ActorState_ACTOR_STATE_REVERTING:
		return StatusPending
	case ateapi.ActorState_ACTOR_STATE_SUSPENDED:
		return StatusIdle
	case ateapi.ActorState_ACTOR_STATE_CRASHED:
		return StatusError
	case ateapi.ActorState_ACTOR_STATE_DELETING:
		return StatusDeleting
	}
	return StatusNone
}

// ActorStateName is the actor state without its ACTOR_STATE_ prefix.
func ActorStateName(s ateapi.ActorState) string {
	return enumName(s.String(), "ACTOR_STATE_")
}

func (actorRenderer) Render(o model.Object, now time.Time) Row {
	a, _ := o.Raw.(*ateapi.Actor)
	m := a.GetMetadata()
	state := a.GetStatus().GetState()

	// A suspended actor's status may still carry its last assignment; it is
	// not on a worker, so show nothing.
	var pod, ips string
	if as := a.GetStatus().GetWorkerAssignment(); as != nil && state != ateapi.ActorState_ACTOR_STATE_SUSPENDED {
		if as.GetWorkerPod() != "" {
			pod = ref(as.GetWorkerNamespace(), as.GetWorkerPod())
		}
		ips = strings.Join(as.GetWorkerPodIps(), ",")
	}

	status := ActorStateStatus(state)
	if status == StatusNone {
		status = metaStatus(m)
	}
	ageCell, ageKey := protoAge(m.GetCreateTime(), now)
	return Row{
		Key: o.Key,
		Cells: []string{
			firstNonEmpty(m.GetAtespace(), o.Atespace),
			firstNonEmpty(m.GetName(), o.Name),
			ref(a.GetActorTemplate().GetAtespace(), a.GetActorTemplate().GetName()),
			ActorStateName(state),
			pod,
			ips,
			strconv.FormatInt(m.GetVersion(), 10),
			ageCell,
		},
		Status:   status,
		SortKeys: []string{"", "", "", "", "", "", padInt(m.GetVersion()), ageKey},
	}
}

// --- Actor templates ---------------------------------------------------

type templateRenderer struct{}

func (templateRenderer) Columns() []Column {
	return []Column{{"ATESPACE", 16}, {"NAME", 24}, {"GOLDEN TAG", 24}, {"ERROR", 6}, {"AGE", 8}}
}

func (templateRenderer) Render(o model.Object, now time.Time) Row {
	t, _ := o.Raw.(*ateapi.ActorTemplate)
	m := t.GetMetadata()
	gss := t.GetStatus().GetGoldenSnapshotStatus()
	disabled := t.GetSnapshotConfig().GetGoldenSnapshotConfig().GetMode() == ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_DISABLED

	goldenTag := gss.GetGoldenTag().GetName()
	status := metaStatus(m)
	if disabled {
		goldenTag = "DISABLED"
	}
	// The error message is too long for a cell; the YAML view shows it.
	errFlag := ""
	switch {
	case gss.GetErrorMessage() != "":
		errFlag = "ERROR"
		status = StatusError
	case status == StatusNone && !disabled && goldenTag == "":
		// Golden snapshot still being built.
		status = StatusPending
	}
	ageCell, ageKey := protoAge(m.GetCreateTime(), now)
	return Row{
		Key: o.Key,
		Cells: []string{
			firstNonEmpty(m.GetAtespace(), o.Atespace),
			firstNonEmpty(m.GetName(), o.Name),
			goldenTag, errFlag, ageCell,
		},
		Status:   status,
		SortKeys: []string{"", "", "", "", ageKey},
	}
}

// --- Tags --------------------------------------------------------------

type tagRenderer struct{}

func (tagRenderer) Columns() []Column {
	return []Column{
		{"ATESPACE", 16}, {"NAME", 24}, {"SOURCE ACTOR", 28}, {"SCOPE", 9},
		{"STATE", 7}, {"AGE", 8},
	}
}

// tagReady reports whether the copy of a tag's snapshot has landed in
// durable storage, as kubectl-ate's tag STATE column does.
func tagReady(t *ateapi.Tag) bool {
	st := t.GetStatus().GetSnapshot().GetDurableSnapshot()
	return st.GetState() == ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED &&
		st.GetObject().GetSnapshotUri() != ""
}

func (tagRenderer) Render(o model.Object, now time.Time) Row {
	t, _ := o.Raw.(*ateapi.Tag)
	m := t.GetMetadata()
	state, status := "Pending", StatusPending
	if tagReady(t) {
		state, status = "Ready", StatusOK
	}
	if s := metaStatus(m); s != StatusNone {
		status = s
	}
	scope := ""
	if t.GetScope() != ateapi.TagScope_TAG_SCOPE_UNSPECIFIED {
		scope = enumName(t.GetScope().String(), "TAG_SCOPE_")
	}
	ageCell, ageKey := protoAge(m.GetCreateTime(), now)
	return Row{
		Key: o.Key,
		Cells: []string{
			firstNonEmpty(m.GetAtespace(), o.Atespace),
			firstNonEmpty(m.GetName(), o.Name),
			ref(t.GetSourceActor().GetAtespace(), t.GetSourceActor().GetName()),
			scope, state, ageCell,
		},
		Status:   status,
		SortKeys: []string{"", "", "", "", "", ageKey},
	}
}

// --- Egress policies ---------------------------------------------------

type egressRenderer struct{}

func (egressRenderer) Columns() []Column {
	return []Column{{"ATESPACE", 16}, {"ACTOR", 24}, {"RULES", 6}, {"VERSION", 8}, {"AGE", 8}}
}

func (egressRenderer) Render(o model.Object, now time.Time) Row {
	p, _ := o.Raw.(*ateapi.EgressPolicy)
	m := p.GetMetadata()
	n := int64(len(p.GetRules()))
	ageCell, ageKey := protoAge(m.GetCreateTime(), now)
	return Row{
		Key: o.Key,
		Cells: []string{
			firstNonEmpty(m.GetAtespace(), o.Atespace),
			// The policy does not carry the actor's name; the model does.
			o.Name,
			strconv.FormatInt(n, 10),
			strconv.FormatInt(m.GetVersion(), 10),
			ageCell,
		},
		Status:   metaStatus(m),
		SortKeys: []string{"", "", padInt(n), padInt(m.GetVersion()), ageKey},
	}
}

// --- Workers -----------------------------------------------------------

type workerRenderer struct{}

func (workerRenderer) Columns() []Column {
	return []Column{
		{"NAMESPACE", 14}, {"POOL", 18}, {"POD", 28}, {"STATE", 11},
		{"STATUS", 16}, {"CPU", 11}, {"MEMORY", 13}, {"AGE", 8},
	}
}

// WorkerOccupancy is "FREE" for a worker hosting no actors, else
// "ASSIGNED(<hosted>/<capacity>)", as kubectl-ate prints it.
func WorkerOccupancy(w *ateapi.Worker) string {
	hosted := w.GetStatus().GetAllocated().GetActors()
	if hosted == 0 {
		return "FREE"
	}
	return "ASSIGNED(" + strconv.Itoa(int(hosted)) + "/" + strconv.Itoa(int(w.GetStatus().GetCapacity().GetActors())) + ")"
}

// usage renders allocated/capacity, or "-" when there is no capacity to
// compare against.
func usage(allocated, capacity int64, format func(int64) string) string {
	if capacity == 0 {
		return "-"
	}
	return format(allocated) + "/" + format(capacity)
}

func formatInt(n int64) string { return strconv.FormatInt(n, 10) }

func (workerRenderer) Render(o model.Object, now time.Time) Row {
	w, _ := o.Raw.(*ateapi.Worker)
	m := w.GetMetadata()
	st := w.GetStatus()
	state := st.GetState()
	stateCell := ""
	if state != ateapi.WorkerState_WORKER_STATE_UNSPECIFIED {
		stateCell = enumName(state.String(), "WORKER_STATE_")
	}
	hosted := st.GetAllocated().GetActors()

	var status Status
	switch {
	case m.GetDeleteTime() != nil:
		status = StatusDeleting
	case state == ateapi.WorkerState_WORKER_STATE_DRAINING:
		status = StatusPending
	case state == ateapi.WorkerState_WORKER_STATE_UNAVAILABLE:
		status = StatusIdle
	case hosted == 0:
		status = StatusOK
	}
	ageCell, ageKey := protoAge(m.GetCreateTime(), now)
	return Row{
		Key: o.Key,
		Cells: []string{
			w.GetWorkerNamespace(),
			w.GetWorkerPool(),
			firstNonEmpty(w.GetWorkerPod(), m.GetName(), o.Name),
			stateCell,
			WorkerOccupancy(w),
			usage(st.GetAllocated().GetCpuMillicores(), st.GetCapacity().GetCpuMillicores(), formatInt),
			usage(st.GetAllocated().GetMemoryBytes(), st.GetCapacity().GetMemoryBytes(), formatBytes),
			ageCell,
		},
		Status: status,
		SortKeys: []string{
			"", "", "", "",
			padInt(int64(hosted)),
			padInt(st.GetAllocated().GetCpuMillicores()),
			padInt(st.GetAllocated().GetMemoryBytes()),
			ageKey,
		},
	}
}

// --- Actor assignments -------------------------------------------------

type assignmentRenderer struct{}

func (assignmentRenderer) Columns() []Column {
	return []Column{
		{"ATESPACE", 16}, {"ACTOR", 24}, {"ACTOR UID", 36}, {"CPU", 6}, {"MEMORY", 8}, {"AGE", 8},
	}
}

func (assignmentRenderer) Render(o model.Object, now time.Time) Row {
	a, _ := o.Raw.(*ateapi.ActorAssignment)
	m := a.GetMetadata()
	res := a.GetResources()
	cpu, mem := "-", "-"
	if c := res.GetCpuMillicores(); c != 0 {
		cpu = strconv.FormatInt(c, 10) + "m"
	}
	if b := res.GetMemoryBytes(); b != 0 {
		mem = formatBytes(b)
	}
	ageCell, ageKey := protoAge(m.GetCreateTime(), now)
	return Row{
		Key: o.Key,
		Cells: []string{
			a.GetActor().GetAtespace(),
			a.GetActor().GetName(),
			// The assignment's name is the actor's UID.
			firstNonEmpty(a.GetActorUid(), m.GetName()),
			cpu, mem, ageCell,
		},
		Status:   metaStatus(m),
		SortKeys: []string{"", "", "", padInt(res.GetCpuMillicores()), padInt(res.GetMemoryBytes()), ageKey},
	}
}

// --- Access policies ---------------------------------------------------

type accessPolicyRenderer struct{}

func (accessPolicyRenderer) Columns() []Column {
	return []Column{{"SCOPE", 16}, {"BINDINGS", 8}, {"ROLES", 30}, {"VERSION", 8}, {"AGE", 8}}
}

// GlobalScope is the SCOPE shown for the cluster-wide access policy.
const GlobalScope = "<global>"

func (accessPolicyRenderer) Render(o model.Object, now time.Time) Row {
	p, _ := o.Raw.(*ateapi.AccessPolicy)
	m := p.GetMetadata()
	scope := firstNonEmpty(o.Atespace, m.GetAtespace(), GlobalScope)
	roles := make([]string, 0, len(p.GetBindings()))
	for _, b := range p.GetBindings() {
		roles = append(roles, b.GetRole())
	}
	n := int64(len(p.GetBindings()))
	ageCell, ageKey := protoAge(m.GetCreateTime(), now)
	return Row{
		Key: o.Key,
		Cells: []string{
			scope,
			strconv.FormatInt(n, 10),
			strings.Join(roles, ","),
			strconv.FormatInt(m.GetVersion(), 10),
			ageCell,
		},
		Status:   metaStatus(m),
		SortKeys: []string{"", padInt(n), "", padInt(m.GetVersion()), ageKey},
	}
}

// --- Worker pools ------------------------------------------------------

type workerPoolRenderer struct{}

func (workerPoolRenderer) Columns() []Column {
	return []Column{{"NAMESPACE", 14}, {"NAME", 24}, {"READY", 6}, {"DESIRED", 8}, {"AGE", 8}}
}

func (workerPoolRenderer) Render(o model.Object, now time.Time) Row {
	p, _ := o.Raw.(*atev1.WorkerPool)
	if p == nil {
		p = &atev1.WorkerPool{}
	}
	ready, desired := int64(p.Status.ReadyReplicas), int64(p.Spec.Replicas)
	var status Status
	switch {
	case p.DeletionTimestamp != nil:
		status = StatusDeleting
	case ready < desired:
		status = StatusPending
	case desired > 0:
		status = StatusOK
	}
	ageCell, ageKey := age(p.CreationTimestamp.Time, now)
	return Row{
		Key: o.Key,
		Cells: []string{
			firstNonEmpty(p.Namespace, o.Atespace),
			firstNonEmpty(p.Name, o.Name),
			strconv.FormatInt(ready, 10),
			strconv.FormatInt(desired, 10),
			ageCell,
		},
		Status:   status,
		SortKeys: []string{"", "", padInt(ready), padInt(desired), ageKey},
	}
}
