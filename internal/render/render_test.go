package render

import (
	"slices"
	"sort"
	"testing"
	"time"

	"go.ate.dev/8top/internal/model"
	"go.ate.dev/substrate/api/ateapi/v1"
	atev1 "go.ate.dev/substrate/api/infra/k8s/ate/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) *timestamppb.Timestamp { return timestamppb.New(now.Add(-d)) }

// renderCase is one renderer test: render obj and compare the row.
type renderCase struct {
	name       string
	obj        model.Object
	wantCells  []string
	wantStatus Status
	// wantSort, if set, is compared to Row.SortKeys.
	wantSort []string
}

func runRenderCases(t *testing.T, kind model.Kind, cases []renderCase) {
	t.Helper()
	r := For(kind)
	if r == nil {
		t.Fatalf("For(%q) = nil", kind)
	}
	cols := r.Columns()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.obj.Kind = kind
			if tc.obj.Key == "" {
				tc.obj.Key = "key/" + tc.name
			}
			row := r.Render(tc.obj, now)
			if row.Key != tc.obj.Key {
				t.Errorf("Key = %q, want %q", row.Key, tc.obj.Key)
			}
			if len(row.Cells) != len(cols) {
				t.Errorf("len(Cells) = %d, want %d (one per column)", len(row.Cells), len(cols))
			}
			if len(row.SortKeys) != len(cols) {
				t.Errorf("len(SortKeys) = %d, want %d (one per column)", len(row.SortKeys), len(cols))
			}
			if !slices.Equal(row.Cells, tc.wantCells) {
				t.Errorf("Cells = %q, want %q", row.Cells, tc.wantCells)
			}
			if row.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", row.Status, tc.wantStatus)
			}
			if tc.wantSort != nil && !slices.Equal(row.SortKeys, tc.wantSort) {
				t.Errorf("SortKeys = %q, want %q", row.SortKeys, tc.wantSort)
			}
		})
	}
}

func TestForAllKinds(t *testing.T) {
	kinds := []model.Kind{
		model.KindAtespace, model.KindActor, model.KindActorTemplate, model.KindTag,
		model.KindEgressPolicy, model.KindWorker, model.KindActorAssignment,
		model.KindAccessPolicy, model.KindWorkerPool,
	}
	for _, k := range kinds {
		r := For(k)
		if r == nil {
			t.Errorf("For(%q) = nil, want a renderer", k)
			continue
		}
		if len(r.Columns()) == 0 {
			t.Errorf("For(%q).Columns() is empty", k)
		}
		// Nil and wrongly typed Raw values must not panic.
		for _, raw := range []any{nil, "not a proto"} {
			row := r.Render(model.Object{Kind: k, Key: "k", Raw: raw}, now)
			if row.Key != "k" {
				t.Errorf("For(%q).Render(Raw=%v).Key = %q, want %q", k, raw, row.Key, "k")
			}
			if len(row.Cells) != len(r.Columns()) {
				t.Errorf("For(%q).Render(Raw=%v) has %d cells, want %d", k, raw, len(row.Cells), len(r.Columns()))
			}
		}
	}
	if r := For("bogus"); r != nil {
		t.Errorf("For(bogus) = %v, want nil", r)
	}
}

func TestAge(t *testing.T) {
	cases := []struct {
		name     string
		created  time.Time
		wantCell string
		wantKey  string
	}{
		{"seconds", now.Add(-45 * time.Second), "45s", "00000000000000000045"},
		{"under two minutes", now.Add(-90 * time.Second), "90s", "00000000000000000090"},
		{"minutes", now.Add(-12 * time.Minute), "12m", "00000000000000000720"},
		{"one hour", now.Add(-time.Hour), "60m", "00000000000000003600"},
		{"minutes and seconds", now.Add(-(3*time.Minute + 20*time.Second)), "3m20s", "00000000000000000200"},
		{"hours", now.Add(-3 * time.Hour), "3h", "00000000000000010800"},
		{"hours and minutes", now.Add(-(3*time.Hour + 5*time.Minute)), "3h5m", "00000000000000011100"},
		{"days", now.Add(-5 * 24 * time.Hour), "5d", "00000000000000432000"},
		{"years", now.Add(-800 * 24 * time.Hour), "2y70d", "00000000000069120000"},
		{"zero", time.Time{}, "<unknown>", "~"},
		{"future clamps sort key", now.Add(time.Hour), "<invalid>", "00000000000000000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cell, key := age(tc.created, now)
			if cell != tc.wantCell {
				t.Errorf("age cell = %q, want %q", cell, tc.wantCell)
			}
			if key != tc.wantKey {
				t.Errorf("age sort key = %q, want %q", key, tc.wantKey)
			}
		})
	}
}

func TestAgeSortKeysOrderByAge(t *testing.T) {
	// Text order would put "12m" before "45s" and "5d" after "3h"; sort keys
	// must order by actual age.
	ages := []time.Duration{5 * 24 * time.Hour, 45 * time.Second, 3 * time.Hour, 12 * time.Minute, 90 * time.Second}
	var keys []string
	for _, d := range ages {
		_, k := age(now.Add(-d), now)
		keys = append(keys, k)
	}
	_, unknown := age(time.Time{}, now)
	keys = append(keys, unknown)
	sort.Strings(keys)
	got := keys
	want := []string{
		padInt(45), padInt(90), padInt(12 * 60), padInt(3 * 3600), padInt(5 * 24 * 3600), "~",
	}
	if !slices.Equal(got, want) {
		t.Errorf("sorted age keys = %q, want %q", got, want)
	}
}

func TestPadIntSortsNumerically(t *testing.T) {
	vals := []int64{10, 9, 100, 2, 0}
	var keys []string
	for _, v := range vals {
		keys = append(keys, padInt(v))
	}
	sort.Strings(keys)
	want := []string{padInt(0), padInt(2), padInt(9), padInt(10), padInt(100)}
	if !slices.Equal(keys, want) {
		t.Errorf("sorted = %q, want %q", keys, want)
	}
	if got, want := padInt(-3), padInt(0); got != want {
		t.Errorf("padInt(-3) = %q, want %q", got, want)
	}
}

func TestAtespaceRenderer(t *testing.T) {
	runRenderCases(t, model.KindAtespace, []renderCase{
		{
			name: "basic",
			obj: model.Object{Raw: &ateapi.Atespace{Metadata: &ateapi.ResourceMetadata{
				Name: "default", CreateTime: ago(5 * time.Minute),
			}}},
			wantCells: []string{"default", "5m"},
			wantSort:  []string{"", padInt(300)},
		},
		{
			name: "deleting",
			obj: model.Object{Raw: &ateapi.Atespace{Metadata: &ateapi.ResourceMetadata{
				Name: "old", CreateTime: ago(48 * time.Hour), DeleteTime: ago(time.Second),
			}}},
			wantCells:  []string{"old", "2d"},
			wantStatus: StatusDeleting,
		},
		{
			name:      "no metadata falls back to object name",
			obj:       model.Object{Name: "fallback", Raw: &ateapi.Atespace{}},
			wantCells: []string{"fallback", "<unknown>"},
			wantSort:  []string{"", "~"},
		},
	})
}

func actor(state ateapi.ActorState, as *ateapi.WorkerAssignment) *ateapi.Actor {
	return &ateapi.Actor{
		Metadata: &ateapi.ResourceMetadata{
			Atespace: "team-a", Name: "counter-1", Version: 7, CreateTime: ago(5 * time.Minute),
		},
		ActorTemplate: &ateapi.ObjectRef{Atespace: "default", Name: "counter"},
		Status:        &ateapi.ActorStatus{State: state, WorkerAssignment: as},
	}
}

var assignment = &ateapi.WorkerAssignment{
	WorkerNamespace: "ate", WorkerPod: "pool-a-0", WorkerPodIps: []string{"10.0.0.5", "fd00::5"},
}

func TestActorRenderer(t *testing.T) {
	cases := []renderCase{
		{
			name:       "running",
			obj:        model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_RUNNING, assignment)},
			wantCells:  []string{"team-a", "counter-1", "default/counter", "RUNNING", "ate/pool-a-0", "10.0.0.5,fd00::5", "7", "5m"},
			wantStatus: StatusOK,
			wantSort:   []string{"", "", "", "", "", "", padInt(7), padInt(300)},
		},
		{
			name:       "suspended with no assignment",
			obj:        model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_SUSPENDED, nil)},
			wantCells:  []string{"team-a", "counter-1", "default/counter", "SUSPENDED", "", "", "7", "5m"},
			wantStatus: StatusIdle,
		},
		{
			name:       "suspended with stale assignment shows no worker",
			obj:        model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_SUSPENDED, assignment)},
			wantCells:  []string{"team-a", "counter-1", "default/counter", "SUSPENDED", "", "", "7", "5m"},
			wantStatus: StatusIdle,
		},
		{
			name:       "crashed",
			obj:        model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_CRASHED, nil)},
			wantCells:  []string{"team-a", "counter-1", "default/counter", "CRASHED", "", "", "7", "5m"},
			wantStatus: StatusError,
		},
		{
			name:       "resuming",
			obj:        model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_RESUMING, nil)},
			wantCells:  []string{"team-a", "counter-1", "default/counter", "RESUMING", "", "", "7", "5m"},
			wantStatus: StatusPending,
		},
		{
			name:       "suspending keeps worker",
			obj:        model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_SUSPENDING, assignment)},
			wantCells:  []string{"team-a", "counter-1", "default/counter", "SUSPENDING", "ate/pool-a-0", "10.0.0.5,fd00::5", "7", "5m"},
			wantStatus: StatusPending,
		},
		{
			name:       "reverting",
			obj:        model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_REVERTING, nil)},
			wantCells:  []string{"team-a", "counter-1", "default/counter", "REVERTING", "", "", "7", "5m"},
			wantStatus: StatusPending,
		},
		{
			name:       "deleting",
			obj:        model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_DELETING, nil)},
			wantCells:  []string{"team-a", "counter-1", "default/counter", "DELETING", "", "", "7", "5m"},
			wantStatus: StatusDeleting,
		},
		{
			name:      "unspecified state",
			obj:       model.Object{Raw: actor(ateapi.ActorState_ACTOR_STATE_UNSPECIFIED, nil)},
			wantCells: []string{"team-a", "counter-1", "default/counter", "UNSPECIFIED", "", "", "7", "5m"},
		},
		{
			name:      "no metadata or status",
			obj:       model.Object{Atespace: "team-b", Name: "bare", Raw: &ateapi.Actor{}},
			wantCells: []string{"team-b", "bare", "", "UNSPECIFIED", "", "", "0", "<unknown>"},
			wantSort:  []string{"", "", "", "", "", "", padInt(0), "~"},
		},
		{
			name:      "nil actor pointer",
			obj:       model.Object{Atespace: "team-b", Name: "nil", Raw: (*ateapi.Actor)(nil)},
			wantCells: []string{"team-b", "nil", "", "UNSPECIFIED", "", "", "0", "<unknown>"},
		},
		{
			name: "large version sorts numerically",
			obj: model.Object{Raw: &ateapi.Actor{Metadata: &ateapi.ResourceMetadata{
				Atespace: "a", Name: "n", Version: 12, CreateTime: ago(3 * time.Hour),
			}}},
			wantCells: []string{"a", "n", "", "UNSPECIFIED", "", "", "12", "3h"},
			wantSort:  []string{"", "", "", "", "", "", "00000000000000000012", padInt(3 * 3600)},
		},
	}
	runRenderCases(t, model.KindActor, cases)
}

func template(mode ateapi.GoldenSnapshotMode, gss *ateapi.GoldenSnapshotStatus) *ateapi.ActorTemplate {
	t := &ateapi.ActorTemplate{
		Metadata: &ateapi.ResourceMetadata{Atespace: "default", Name: "counter", CreateTime: ago(3 * time.Hour)},
		Status:   &ateapi.ActorTemplateStatus{GoldenSnapshotStatus: gss},
	}
	if mode != ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_UNSPECIFIED {
		t.SnapshotConfig = &ateapi.SnapshotConfig{GoldenSnapshotConfig: &ateapi.GoldenSnapshotConfig{Mode: mode}}
	}
	return t
}

func TestTemplateRenderer(t *testing.T) {
	ready := &ateapi.GoldenSnapshotStatus{GoldenTag: &ateapi.ObjectRef{Atespace: "default", Name: "counter-golden"}}
	failed := &ateapi.GoldenSnapshotStatus{ErrorMessage: "BuildFailed: image pull"}
	runRenderCases(t, model.KindActorTemplate, []renderCase{
		{
			name:      "golden tag ready",
			obj:       model.Object{Raw: template(ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_ENABLED, ready)},
			wantCells: []string{"default", "counter", "counter-golden", "", "3h"},
			wantSort:  []string{"", "", "", "", padInt(3 * 3600)},
		},
		{
			name:      "default mode is enabled",
			obj:       model.Object{Raw: template(ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_UNSPECIFIED, ready)},
			wantCells: []string{"default", "counter", "counter-golden", "", "3h"},
		},
		{
			name:      "disabled",
			obj:       model.Object{Raw: template(ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_DISABLED, nil)},
			wantCells: []string{"default", "counter", "DISABLED", "", "3h"},
		},
		{
			name:       "build error",
			obj:        model.Object{Raw: template(ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_ENABLED, failed)},
			wantCells:  []string{"default", "counter", "", "ERROR", "3h"},
			wantStatus: StatusError,
		},
		{
			name:       "golden build in progress",
			obj:        model.Object{Raw: template(ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_UNSPECIFIED, nil)},
			wantCells:  []string{"default", "counter", "", "", "3h"},
			wantStatus: StatusPending,
		},
		{
			name:       "no metadata",
			obj:        model.Object{Atespace: "x", Name: "y", Raw: &ateapi.ActorTemplate{}},
			wantCells:  []string{"x", "y", "", "", "<unknown>"},
			wantStatus: StatusPending,
		},
	})
}

func TestTagRenderer(t *testing.T) {
	durable := &ateapi.TagStatus{Snapshot: &ateapi.Snapshot{DurableSnapshot: &ateapi.SnapshotStorage{
		State:  ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED,
		Object: &ateapi.ObjectSnapshot{SnapshotUri: "gs://b/snap"},
	}}}
	uploading := &ateapi.TagStatus{Snapshot: &ateapi.Snapshot{DurableSnapshot: &ateapi.SnapshotStorage{
		State: ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_PENDING,
	}}}
	md := func() *ateapi.ResourceMetadata {
		return &ateapi.ResourceMetadata{Atespace: "team-a", Name: "v1", CreateTime: ago(12 * time.Minute)}
	}
	src := &ateapi.ObjectRef{Atespace: "team-a", Name: "counter-1"}
	runRenderCases(t, model.KindTag, []renderCase{
		{
			name: "ready atespace scope",
			obj: model.Object{Raw: &ateapi.Tag{
				Metadata: md(), SourceActor: src, Scope: ateapi.TagScope_TAG_SCOPE_ATESPACE, Status: durable,
			}},
			wantCells:  []string{"team-a", "v1", "team-a/counter-1", "ATESPACE", "Ready", "12m"},
			wantStatus: StatusOK,
			wantSort:   []string{"", "", "", "", "", padInt(720)},
		},
		{
			name: "pending published",
			obj: model.Object{Raw: &ateapi.Tag{
				Metadata: md(), SourceActor: src, Scope: ateapi.TagScope_TAG_SCOPE_PUBLISHED, Status: uploading,
			}},
			wantCells:  []string{"team-a", "v1", "team-a/counter-1", "PUBLISHED", "Pending", "12m"},
			wantStatus: StatusPending,
		},
		{
			name:       "no status or source",
			obj:        model.Object{Atespace: "a", Name: "t", Raw: &ateapi.Tag{}},
			wantCells:  []string{"a", "t", "", "", "Pending", "<unknown>"},
			wantStatus: StatusPending,
		},
	})
}

func TestEgressRenderer(t *testing.T) {
	runRenderCases(t, model.KindEgressPolicy, []renderCase{
		{
			name: "two rules",
			obj: model.Object{Name: "c1", Raw: &ateapi.EgressPolicy{
				Metadata: &ateapi.ResourceMetadata{Atespace: "team-a", Name: "default", Version: 12, CreateTime: ago(5 * time.Minute)},
				Rules: []*ateapi.EgressRule{
					{Http: &ateapi.HttpRule{Hostnames: []string{"api.example.com"}}},
					{Https: &ateapi.HttpsRule{Hostnames: []string{"www.example.com"}}},
				},
			}},
			wantCells: []string{"team-a", "c1", "2", "12", "5m"},
			wantSort:  []string{"", "", padInt(2), padInt(12), padInt(300)},
		},
		{
			name: "no rules",
			obj: model.Object{Name: "c2", Raw: &ateapi.EgressPolicy{
				Metadata: &ateapi.ResourceMetadata{Atespace: "team-a", Version: 1, CreateTime: ago(30 * time.Second)},
			}},
			wantCells: []string{"team-a", "c2", "0", "1", "30s"},
		},
		{
			name:      "no metadata",
			obj:       model.Object{Atespace: "team-b", Name: "c3", Raw: &ateapi.EgressPolicy{}},
			wantCells: []string{"team-b", "c3", "0", "0", "<unknown>"},
		},
	})
}

func worker(state ateapi.WorkerState, hosted int32) *ateapi.Worker {
	return &ateapi.Worker{
		Metadata:        &ateapi.ResourceMetadata{Name: "w-uid", CreateTime: ago(72 * time.Hour)},
		WorkerNamespace: "ate",
		WorkerPool:      "pool-a",
		WorkerPod:       "pool-a-0",
		Status: &ateapi.WorkerStatus{
			State:     state,
			Capacity:  &ateapi.WorkerResources{Actors: 10, CpuMillicores: 2000, MemoryBytes: 4 << 30},
			Allocated: &ateapi.WorkerResources{Actors: hosted, CpuMillicores: 500 * int64(hosted), MemoryBytes: int64(hosted) << 30},
		},
	}
}

func TestWorkerRenderer(t *testing.T) {
	runRenderCases(t, model.KindWorker, []renderCase{
		{
			name:       "free",
			obj:        model.Object{Raw: worker(ateapi.WorkerState_WORKER_STATE_ACTIVE, 0)},
			wantCells:  []string{"ate", "pool-a", "pool-a-0", "ACTIVE", "FREE", "0/2000", "0/4Gi", "3d"},
			wantStatus: StatusOK,
			wantSort:   []string{"", "", "", "", padInt(0), padInt(0), padInt(0), padInt(72 * 3600)},
		},
		{
			name:      "assigned",
			obj:       model.Object{Raw: worker(ateapi.WorkerState_WORKER_STATE_ACTIVE, 2)},
			wantCells: []string{"ate", "pool-a", "pool-a-0", "ACTIVE", "ASSIGNED(2/10)", "1000/2000", "2Gi/4Gi", "3d"},
			wantSort:  []string{"", "", "", "", padInt(2), padInt(1000), padInt(2 << 30), padInt(72 * 3600)},
		},
		{
			name:       "draining",
			obj:        model.Object{Raw: worker(ateapi.WorkerState_WORKER_STATE_DRAINING, 1)},
			wantCells:  []string{"ate", "pool-a", "pool-a-0", "DRAINING", "ASSIGNED(1/10)", "500/2000", "1Gi/4Gi", "3d"},
			wantStatus: StatusPending,
		},
		{
			name:       "unavailable",
			obj:        model.Object{Raw: worker(ateapi.WorkerState_WORKER_STATE_UNAVAILABLE, 0)},
			wantCells:  []string{"ate", "pool-a", "pool-a-0", "UNAVAILABLE", "FREE", "0/2000", "0/4Gi", "3d"},
			wantStatus: StatusIdle,
		},
		{
			name:       "no status",
			obj:        model.Object{Name: "w", Raw: &ateapi.Worker{}},
			wantCells:  []string{"", "", "w", "", "FREE", "-", "-", "<unknown>"},
			wantStatus: StatusOK,
		},
	})
}

func TestAssignmentRenderer(t *testing.T) {
	runRenderCases(t, model.KindActorAssignment, []renderCase{
		{
			name: "basic",
			obj: model.Object{Raw: &ateapi.ActorAssignment{
				Metadata:  &ateapi.ResourceMetadata{Name: "uid-1", CreateTime: ago(90 * time.Second)},
				Actor:     &ateapi.ObjectRef{Atespace: "team-a", Name: "counter-1"},
				ActorUid:  "uid-1",
				Resources: &ateapi.Resources{CpuMillicores: 250, MemoryBytes: 512 << 20},
			}},
			wantCells: []string{"team-a", "counter-1", "uid-1", "250m", "512Mi", "90s"},
			wantSort:  []string{"", "", "", padInt(250), padInt(512 << 20), padInt(90)},
		},
		{
			name: "uid from metadata, no resources",
			obj: model.Object{Raw: &ateapi.ActorAssignment{
				Metadata: &ateapi.ResourceMetadata{Name: "uid-2"},
			}},
			wantCells: []string{"", "", "uid-2", "-", "-", "<unknown>"},
		},
	})
}

func TestAccessPolicyRenderer(t *testing.T) {
	policy := &ateapi.AccessPolicy{
		Metadata: &ateapi.ResourceMetadata{Name: "default", Version: 3, CreateTime: ago(time.Hour)},
		Bindings: []*ateapi.Binding{
			{Role: "owner", Members: []string{"user:alice"}},
			{Role: "viewer", Members: []string{"user:bob"}},
		},
	}
	runRenderCases(t, model.KindAccessPolicy, []renderCase{
		{
			name:      "global",
			obj:       model.Object{Raw: policy},
			wantCells: []string{"<global>", "2", "owner,viewer", "3", "60m"},
			wantSort:  []string{"", padInt(2), "", padInt(3), padInt(3600)},
		},
		{
			name:      "atespace",
			obj:       model.Object{Atespace: "team-a", Raw: policy},
			wantCells: []string{"team-a", "2", "owner,viewer", "3", "60m"},
		},
		{
			name:      "empty",
			obj:       model.Object{Raw: &ateapi.AccessPolicy{}},
			wantCells: []string{"<global>", "0", "", "0", "<unknown>"},
		},
	})
}

func pool(desired, ready int32) *atev1.WorkerPool {
	return &atev1.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ate", Name: "pool-a", CreationTimestamp: metav1.NewTime(now.Add(-26 * time.Hour)),
		},
		Spec:   atev1.WorkerPoolSpec{Replicas: desired},
		Status: atev1.WorkerPoolStatus{ReadyReplicas: ready},
	}
}

func TestWorkerPoolRenderer(t *testing.T) {
	deleting := pool(3, 3)
	ts := metav1.NewTime(now)
	deleting.DeletionTimestamp = &ts
	runRenderCases(t, model.KindWorkerPool, []renderCase{
		{
			name:       "all ready",
			obj:        model.Object{Raw: pool(3, 3)},
			wantCells:  []string{"ate", "pool-a", "3", "3", "26h"},
			wantStatus: StatusOK,
			wantSort:   []string{"", "", padInt(3), padInt(3), padInt(26 * 3600)},
		},
		{
			name:       "scaling up",
			obj:        model.Object{Raw: pool(10, 2)},
			wantCells:  []string{"ate", "pool-a", "2", "10", "26h"},
			wantStatus: StatusPending,
			wantSort:   []string{"", "", padInt(2), padInt(10), padInt(26 * 3600)},
		},
		{
			name:      "scaled to zero",
			obj:       model.Object{Raw: pool(0, 0)},
			wantCells: []string{"ate", "pool-a", "0", "0", "26h"},
		},
		{
			name:       "deleting",
			obj:        model.Object{Raw: deleting},
			wantCells:  []string{"ate", "pool-a", "3", "3", "26h"},
			wantStatus: StatusDeleting,
		},
		{
			name:      "nil pool",
			obj:       model.Object{Name: "p", Raw: (*atev1.WorkerPool)(nil)},
			wantCells: []string{"", "p", "0", "0", "<unknown>"},
		},
	})
}

func TestAtespaceColumn(t *testing.T) {
	cases := []struct {
		kind model.Kind
		want int
	}{
		{model.KindActor, 0},
		{model.KindActorTemplate, 0},
		{model.KindTag, 0},
		{model.KindEgressPolicy, 0},
		{model.KindActorAssignment, 0},
		{model.KindAtespace, -1},
		{model.KindWorker, -1},
		{model.KindAccessPolicy, -1},
		{model.KindWorkerPool, -1},
	}
	for _, tc := range cases {
		if got := AtespaceColumn(For(tc.kind)); got != tc.want {
			t.Errorf("AtespaceColumn(%q) = %d, want %d", tc.kind, got, tc.want)
		}
	}
	if got := AtespaceColumn(nil); got != -1 {
		t.Errorf("AtespaceColumn(nil) = %d, want -1", got)
	}
}
