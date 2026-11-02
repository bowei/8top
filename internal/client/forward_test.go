package client

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func testPod(name string, ready bool, mods ...func(*corev1.Pod)) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ate-system", Labels: map[string]string{"app": "ate-api-server"}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
	for _, m := range mods {
		m(p)
	}
	return p
}

func TestIsPodReady(t *testing.T) {
	now := metav1.Now()
	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{name: "ready", pod: testPod("p", true), want: true},
		{name: "not ready", pod: testPod("p", false)},
		{name: "pending", pod: testPod("p", true, func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending })},
		{name: "terminating", pod: testPod("p", true, func(p *corev1.Pod) { p.DeletionTimestamp = &now })},
		{name: "no conditions", pod: testPod("p", true, func(p *corev1.Pod) { p.Status.Conditions = nil })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPodReady(tc.pod); got != tc.want {
				t.Errorf("isPodReady() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReadyPodForService(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ate-system"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "ate-api-server"}},
	}
	noSelector := svc.DeepCopy()
	noSelector.Spec.Selector = nil
	other := testPod("other", true, func(p *corev1.Pod) { p.Labels = map[string]string{"app": "other"} })
	otherNS := testPod("other-ns", true, func(p *corev1.Pod) { p.Namespace = "default" })

	for _, tc := range []struct {
		name    string
		objs    []runtime.Object
		want    string
		wantErr bool
	}{
		{name: "one ready", objs: []runtime.Object{svc, testPod("a", true)}, want: "a"},
		{name: "skips unready", objs: []runtime.Object{svc, testPod("a", false), testPod("b", true)}, want: "b"},
		{name: "none ready", objs: []runtime.Object{svc, testPod("a", false)}, wantErr: true},
		{name: "ignores unselected", objs: []runtime.Object{svc, other, otherNS}, wantErr: true},
		{name: "no service", objs: []runtime.Object{testPod("a", true)}, wantErr: true},
		{name: "no selector", objs: []runtime.Object{noSelector, testPod("a", true)}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kube := fake.NewClientset(tc.objs...)
			pod, gotSvc, err := readyPodForService(context.Background(), kube, "ate-system", "api")
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("readyPodForService() err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if pod.Name != tc.want {
				t.Errorf("pod = %q, want %q", pod.Name, tc.want)
			}
			if gotSvc.Name != "api" {
				t.Errorf("service = %q, want %q", gotSvc.Name, "api")
			}
		})
	}
}

func TestResolveTargetPort(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar", Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9090}}},
			{Name: "api", Ports: []corev1.ContainerPort{{Name: "grpc", ContainerPort: 8443}}},
		}},
	}
	svc := func(ports ...corev1.ServicePort) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api"}, Spec: corev1.ServiceSpec{Ports: ports}}
	}
	for _, tc := range []struct {
		name    string
		svc     *corev1.Service
		want    int32
		wantErr bool
	}{
		{name: "int target", svc: svc(corev1.ServicePort{Port: 443, TargetPort: intstr.FromInt32(8443)}), want: 8443},
		{name: "omitted target", svc: svc(corev1.ServicePort{Port: 443}), want: 443},
		{name: "named target", svc: svc(corev1.ServicePort{Port: 443, TargetPort: intstr.FromString("grpc")}), want: 8443},
		{name: "named target missing", svc: svc(corev1.ServicePort{Port: 443, TargetPort: intstr.FromString("nope")}), wantErr: true},
		{
			name: "picks matching port",
			svc: svc(
				corev1.ServicePort{Port: 9090, TargetPort: intstr.FromString("metrics")},
				corev1.ServicePort{Port: 443, TargetPort: intstr.FromString("grpc")},
			),
			want: 8443,
		},
		{name: "no such port", svc: svc(corev1.ServicePort{Port: 80}), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTargetPort(tc.svc, pod, 443)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("resolveTargetPort() err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("resolveTargetPort() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestForwarderClosed(t *testing.T) {
	f := newForwarder(nil, fake.NewClientset(), "ate-system", "api", 443)
	f.close()
	if _, err := f.dial(context.Background(), "ignored"); err != errForwarderClosed {
		t.Errorf("dial() after close err = %v, want %v", err, errForwarderClosed)
	}
}
