package generators

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

// statefulSetTestSpec includes native fields that must survive construction.
const statefulSetTestSpec = `replicas: 3
duration: 1s
podTemplate:
  metadata:
    namespace: workloads
    labels:
      run_id: example
    annotations:
      example.com/note: preserved
  spec:
    terminationGracePeriodSeconds: 7
    nodeSelector:
      pool: load
    containers:
    - name: load
      image: example:test
      command: [/load]
      args: ["--rate", "10"]
      resources:
        requests:
          cpu: 100m
          memory: 128Mi
`

// TestNewStatefulSetGenerator checks offline parsing and only generator defaults.
func TestNewStatefulSetGenerator(t *testing.T) {
	// tests distinguishes generator settings from fields validated by Kubernetes.
	tests := []struct {
		// name identifies the configuration case.
		name string
		// spec contains the constructor's YAML input.
		spec string
		// wantReplicas checks the default and explicit replica count.
		wantReplicas int32
		// wantErr identifies invalid generator configuration.
		wantErr bool
	}{
		{name: "defaults", spec: "duration: 1s", wantReplicas: 1},
		{name: "zero", spec: "duration: 1s\nreplicas: 0"},
		{name: "no scale ceiling", spec: "duration: 1s\nreplicas: 100000", wantReplicas: 100000},
		{name: "Kubernetes validates Pod fields", spec: "duration: 1s\npodTemplate: {spec: {restartPolicy: invalid}}", wantReplicas: 1},
		{name: "negative replicas", spec: "duration: 1s\nreplicas: -1", wantErr: true},
		{name: "missing duration", spec: "{}", wantErr: true},
		{name: "zero duration", spec: "duration: 0s", wantErr: true},
		{name: "negative duration", spec: "duration: -1s", wantErr: true},
		{name: "unknown field", spec: "duration: 1s\nimage: invalid", wantErr: true},
		{name: "unknown Pod field", spec: "duration: 1s\npodTemplate: {spec: {imag: typo}}", wantErr: true},
		{name: "duplicate field", spec: "duration: 1s\nreplicas: 1\nreplicas: 2", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// g must parse without reading the nonexistent kubeconfig.
			g, err := NewStatefulSetGenerator(podSpec(t, test.spec), "/does/not/exist")
			if test.wantErr {
				if err == nil {
					t.Error("NewStatefulSetGenerator() succeeded, want configuration error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewStatefulSetGenerator(): %v", err)
			}
			workload := newStatefulSet(g.config)
			assert.Equal(t, test.wantReplicas, g.config.Replicas, "replicas")
			assert.Equal(t, "default", workload.Namespace, "namespace")
			if test.wantReplicas == 0 {
				err = g.Run(t.Context())
				if err != nil {
					t.Errorf("zero-replica Run(): %v", err)
				}
			}
			// ctx verifies cancellation before credentials are read.
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err = g.Run(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("canceled Run() = %v, want context.Canceled", err)
			}
		})
	}
}

// TestStatefulSetGeneratorRun exercises real clients against controlled API responses.
func TestStatefulSetGeneratorRun(t *testing.T) {
	// tests covers startup, controller recovery and cleanup ownership boundaries.
	tests := []struct {
		// name selects an API scenario and labels its subtest.
		name string
		// wantErr is a diagnostic fragment, empty on success.
		wantErr string
		// wantDelete records whether this run should delete the controller.
		wantDelete bool
	}{
		{name: "ready", wantDelete: true},
		{name: "replica recovery", wantDelete: true},
		{name: "cancel startup", wantErr: "context canceled", wantDelete: true},
		{name: "cancel duration", wantErr: "context canceled", wantDelete: true},
		{name: "create rejected", wantErr: "create StatefulSet"},
		{name: "lost create response", wantErr: "create StatefulSet", wantDelete: true},
		{name: "service rejected", wantErr: "create StatefulSet Service", wantDelete: true},
		{name: "lost service response", wantErr: "create StatefulSet Service", wantDelete: true},
		{name: "replaced", wantErr: "refuse to delete unowned"},
		{name: "foreign owner", wantErr: "refuse to delete unowned"},
		{name: "delete rejected", wantErr: "delete StatefulSet", wantDelete: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// ctx lets the server cancel startup or the duration wait deterministically.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// api models only controller and governing Service operations.
			api := &statefulSetTestAPI{t: t, mode: test.name, cancel: cancel}
			// server keeps the real client serialization and context handling in use.
			server := httptest.NewServer(http.HandlerFunc(api.serveHTTP))
			t.Cleanup(server.Close)
			// path contains credentials valid only for this HTTP fixture.
			path := filepath.Join(t.TempDir(), "kubeconfig")
			// config deliberately selects a different namespace from the template.
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: scale\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: scale\n  context:\n    cluster: test\n    namespace: wrong\n", server.URL)
			err := os.WriteFile(path, []byte(config), 0600)
			if err != nil {
				t.Fatalf("write kubeconfig: %v", err)
			}
			// g uses native template parsing before acquiring resources in Run.
			g, err := NewStatefulSetGenerator(podSpec(t, statefulSetTestSpec), path)
			if err != nil {
				t.Fatalf("NewStatefulSetGenerator(): %v", err)
			}
			api.template = g.config.PodTemplate.DeepCopy()
			err = g.Run(ctx)
			if test.wantErr == "" && err != nil {
				t.Errorf("Run(): %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Errorf("Run() = %v, want error containing %q", err, test.wantErr)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.deleted != test.wantDelete {
				t.Errorf("Run() deleted controller = %v, want %v", api.deleted, test.wantDelete)
			}
			if test.wantDelete && test.name != "delete rejected" && api.workload != nil {
				t.Error("Run() returned before controller cleanup completed")
			}
			if test.name == "ready" && api.polls < 4 {
				t.Errorf("Run() readiness polls = %d, want at least 4 to include duration after readiness", api.polls)
			}
			if test.name == "replica recovery" && api.polls < 5 {
				t.Errorf("Run() readiness polls = %d, want at least 5 to include replica recovery", api.polls)
			}
			// diff detects changes to the caller's reusable native template.
			diff := cmp.Diff(*api.template, g.config.PodTemplate)
			if diff != "" {
				t.Errorf("Run() mutated template (-want +got):\n%s", diff)
			}
		})
	}
}

// statefulSetTestAPI models the endpoints needed by one generator invocation.
type statefulSetTestAPI struct {
	// t reports malformed requests from the server goroutine without aborting it.
	t *testing.T
	// mu protects state shared between HTTP handlers and the test goroutine.
	mu sync.Mutex
	// mode selects the run or cleanup failure to simulate.
	mode string
	// cancel ends the caller's context while leaving cleanup usable.
	cancel context.CancelFunc
	// template is the unmodified configuration expected in the submitted workload.
	template *corev1.PodTemplateSpec
	// workload retains the API object until simulated foreground deletion completes.
	workload *appsv1.StatefulSet
	// polls counts status reads before deletion for readiness sequencing.
	polls int
	// deleted records an attempted controller deletion.
	deleted bool
	// cleanupPolls keeps the object present for one observation after deletion.
	cleanupPolls int
}

// serveHTTP validates native templates, ownership and foreground deletion requests.
func (a *statefulSetTestAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	// base rejects operations in the kubeconfig's unrelated default namespace.
	const base = "/apis/apps/v1/namespaces/workloads/statefulsets"
	// write preserves API response encoding errors in the test output.
	write := func(value any) {
		// err reports an HTTP fixture failure without terminating the server goroutine.
		err := json.NewEncoder(w).Encode(value)
		if err != nil {
			a.t.Errorf("encode response: %v", err)
		}
	}
	if r.Method == http.MethodPost && r.URL.Path == base {
		if a.mode == "create rejected" {
			http.Error(w, "rejected", http.StatusUnprocessableEntity)
			return
		}
		// workload contains the submitted native controller and Pod template.
		var workload appsv1.StatefulSet
		if !a.decode(w, r, &workload) {
			return
		}
		// want preserves every user field with only the reserved label assigned.
		want := a.template.DeepCopy()
		want.Labels[generatorOwnerLabel] = workload.Name
		// diff catches changes to native Pod settings and labels.
		diff := cmp.Diff(*want, workload.Spec.Template)
		if diff != "" {
			a.t.Errorf("submitted StatefulSet template mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(a.t, workload.Name, workload.Spec.ServiceName, "StatefulSet Service name")
		assert.Equal(a.t, workload.Name, workload.Labels[generatorOwnerLabel], "StatefulSet ownership label")
		if assert.NotNil(a.t, workload.Spec.Selector, "StatefulSet selector") {
			assert.Equal(a.t, workload.Name, workload.Spec.Selector.MatchLabels[generatorOwnerLabel], "StatefulSet ownership selector")
		}
		if workload.Spec.Replicas == nil || *workload.Spec.Replicas != 3 || workload.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
			a.t.Errorf("StatefulSet spec = %+v, want three parallel replicas", workload.Spec)
		}
		workload.UID = "owned"
		workload.Generation = 1
		workload.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"}
		a.workload = &workload
		if a.mode == "lost create response" {
			http.Error(w, "response lost", http.StatusInternalServerError)
			return
		}
		write(workload)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/workloads/services" {
		// service must refer to the controller UID so cleanup includes it.
		var service corev1.Service
		if !a.decode(w, r, &service) {
			return
		}
		assert.Equal(a.t, a.workload.Name, service.Name, "Service name")
		assert.Equal(a.t, corev1.ClusterIPNone, service.Spec.ClusterIP, "headless Service")
		assert.Equal(a.t, a.workload.Name, service.Spec.Selector[generatorOwnerLabel], "Service ownership selector")
		if len(service.OwnerReferences) != 1 || service.OwnerReferences[0].UID != a.workload.UID || service.OwnerReferences[0].BlockOwnerDeletion == nil || !*service.OwnerReferences[0].BlockOwnerDeletion {
			a.t.Errorf("Service owner references = %v, want controller UID with deletion blocked", service.OwnerReferences)
		}
		if a.mode == "service rejected" || a.mode == "lost service response" {
			http.Error(w, "Service create failed", http.StatusInternalServerError)
			return
		}
		service.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}
		write(service)
		return
	}
	if strings.HasPrefix(r.URL.Path, base+"/") {
		if a.workload == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			// options must target the known UID and include dependent resources.
			var options metav1.DeleteOptions
			if !a.decode(w, r, &options) {
				return
			}
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "owned" || options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationForeground {
				a.t.Errorf("Delete options = %+v, want UID precondition and foreground propagation", options)
			}
			a.deleted = true
			if a.mode == "delete rejected" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			write(metav1.Status{Status: "Success"})
			return
		}
		if r.Method == http.MethodGet {
			if a.deleted {
				a.cleanupPolls++
				if a.cleanupPolls > 1 {
					a.workload = nil
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
			} else {
				a.polls++
				a.workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 3}
				if a.polls == 1 || (a.mode == "replica recovery" && a.polls == 4) {
					a.workload.Status.ReadyReplicas = 2
				}
				if a.polls == 2 {
					a.workload.Status.ObservedGeneration = 0
				}
				if (a.mode == "cancel startup" && a.polls == 1) || (a.mode == "cancel duration" && a.polls == 4) {
					a.cancel()
				}
				if a.mode == "replaced" {
					a.workload.UID = "replacement"
				}
				if a.mode == "foreign owner" {
					a.workload.Labels[generatorOwnerLabel] = "another-run"
				}
			}
			write(a.workload)
			return
		}
	}
	a.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	http.Error(w, "unexpected request", http.StatusNotFound)
}

// decode accepts JSON or protobuf requests from the real typed client.
func (a *statefulSetTestAPI) decode(w http.ResponseWriter, r *http.Request, into runtime.Object) bool {
	// data holds the complete API request body for the negotiated serializer.
	data, err := io.ReadAll(r.Body)
	if err == nil {
		err = runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), data, into)
	}
	if err != nil {
		a.t.Errorf("decode request: %v", err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}
