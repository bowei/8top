package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
)

var errForwarderClosed = errors.New("port-forward closed")

// forwarder is a gRPC context dialer that tunnels to a ready pod behind a
// Service. The Kubernetes port-forward API is pod-scoped, so the tunnel is
// pinned to one pod. Before each dial the forwarder checks that the tunnel is
// still up and its pod still ready; otherwise it opens a new tunnel to
// another ready pod. gRPC redials whenever its connection breaks, so a dead
// pod is replaced on the next reconnect.
type forwarder struct {
	config             *rest.Config
	kube               kubernetes.Interface
	namespace, service string
	port               int32

	mu     sync.Mutex
	cur    *tunnel
	closed bool
}

// tunnel is one running port-forward.
type tunnel struct {
	pod      string
	addr     string        // local host:port
	done     chan struct{} // closed when ForwardPorts returns
	stopOnce sync.Once
	stopCh   chan struct{}
}

func (t *tunnel) stop() { t.stopOnce.Do(func() { close(t.stopCh) }) }

func (t *tunnel) alive() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

func newForwarder(config *rest.Config, kube kubernetes.Interface, namespace, service string, port int32) *forwarder {
	return &forwarder{config: config, kube: kube, namespace: namespace, service: service, port: port}
}

// start opens the first tunnel and returns the pod it points at.
func (f *forwarder) start(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.ensureLocked(ctx)
	if err != nil {
		return "", err
	}
	return t.pod, nil
}

// dial is used with grpc.WithContextDialer. The address is ignored.
func (f *forwarder) dial(ctx context.Context, _ string) (net.Conn, error) {
	f.mu.Lock()
	t, err := f.ensureLocked(ctx)
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", t.addr)
	if err != nil {
		// The local listener is gone; drop the tunnel so the next dial
		// opens a fresh one.
		t.stop()
		return nil, err
	}
	return conn, nil
}

func (f *forwarder) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.cur != nil {
		f.cur.stop()
		f.cur = nil
	}
}

// ensureLocked returns the current tunnel if it is usable, otherwise opens a
// new one.
func (f *forwarder) ensureLocked(ctx context.Context) (*tunnel, error) {
	if f.closed {
		return nil, errForwarderClosed
	}
	if f.cur != nil && f.cur.alive() && f.podReady(ctx, f.cur.pod) {
		return f.cur, nil
	}
	if f.cur != nil {
		f.cur.stop()
		f.cur = nil
	}
	pod, svc, err := readyPodForService(ctx, f.kube, f.namespace, f.service)
	if err != nil {
		return nil, err
	}
	targetPort, err := resolveTargetPort(svc, pod, f.port)
	if err != nil {
		return nil, err
	}
	t, err := f.open(ctx, pod.Name, targetPort)
	if err != nil {
		return nil, err
	}
	f.cur = t
	return t, nil
}

func (f *forwarder) podReady(ctx context.Context, name string) bool {
	pod, err := f.kube.CoreV1().Pods(f.namespace).Get(ctx, name, metav1.GetOptions{})
	return err == nil && isPodReady(pod)
}

// open forwards a random local port to targetPort on pod. ctx bounds only the
// setup; the tunnel runs until stopped or the connection to the pod is lost.
func (f *forwarder) open(ctx context.Context, pod string, targetPort int32) (*tunnel, error) {
	req := f.kube.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(f.namespace).
		Name(pod).
		SubResource("portforward")
	dialer, err := portforward.NewSPDYOverWebsocketDialer(req.URL(), f.config)
	if err != nil {
		return nil, fmt.Errorf("creating websocket dialer: %w", err)
	}

	t := &tunnel{pod: pod, done: make(chan struct{}), stopCh: make(chan struct{})}
	readyCh := make(chan struct{})
	fw, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", targetPort)}, t.stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("creating port forwarder: %w", err)
	}
	errCh := make(chan error, 1)
	go func() {
		defer close(t.done)
		errCh <- fw.ForwardPorts()
	}()
	select {
	case <-readyCh:
	case err := <-errCh:
		return nil, fmt.Errorf("port-forward to %s/%s: %w", f.namespace, pod, err)
	case <-ctx.Done():
		t.stop()
		return nil, ctx.Err()
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		t.stop()
		return nil, fmt.Errorf("getting forwarded ports: %w", err)
	}
	t.addr = fmt.Sprintf("127.0.0.1:%d", ports[0].Local)
	return t, nil
}

// readyPodForService returns the first ready pod selected by the Service.
func readyPodForService(ctx context.Context, kube kubernetes.Interface, namespace, service string) (*corev1.Pod, *corev1.Service, error) {
	svc, err := kube.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("getting %s service: %w", service, err)
	}
	if len(svc.Spec.Selector) == 0 {
		return nil, nil, fmt.Errorf("service %s has no selector", service)
	}
	selector := labels.SelectorFromSet(svc.Spec.Selector).String()
	pods, err := kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, nil, fmt.Errorf("listing %s pods: %w", service, err)
	}
	for i := range pods.Items {
		if isPodReady(&pods.Items[i]) {
			return &pods.Items[i], svc, nil
		}
	}
	return nil, nil, fmt.Errorf("no ready %s pods in %s", service, namespace)
}

// resolveTargetPort maps servicePort to the pod's container port, resolving
// named targetPorts against the pod's container ports.
func resolveTargetPort(svc *corev1.Service, pod *corev1.Pod, servicePort int32) (int32, error) {
	for _, sp := range svc.Spec.Ports {
		if sp.Port != servicePort {
			continue
		}
		switch sp.TargetPort.Type {
		case intstr.Int:
			if sp.TargetPort.IntVal == 0 {
				// targetPort defaults to port when omitted.
				return sp.Port, nil
			}
			return sp.TargetPort.IntVal, nil
		case intstr.String:
			for _, c := range pod.Spec.Containers {
				for _, cp := range c.Ports {
					if cp.Name == sp.TargetPort.StrVal {
						return cp.ContainerPort, nil
					}
				}
			}
			return 0, fmt.Errorf("named targetPort %q not found on pod %s", sp.TargetPort.StrVal, pod.Name)
		}
	}
	return 0, fmt.Errorf("service %s has no port %d", svc.Name, servicePort)
}

// isPodReady reports whether pod is running, not terminating, and Ready.
func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
