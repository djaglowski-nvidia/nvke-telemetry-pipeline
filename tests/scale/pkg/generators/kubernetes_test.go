package generators

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

// kubernetesTestSpec is a small native node group reused across generator tests.
const kubernetesTestSpec = `duration: 10ms
nodeGroups:
  - name: workers
    replicas: 3
    nodeTemplate:
      metadata:
        labels:
          run_id: example-run
      status:
        capacity:
          cpu: "4"
          memory: 16Gi
          pods: "40"
`

// TestNewKubernetesGenerator checks parsing and the default replica count.
func TestNewKubernetesGenerator(t *testing.T) {
	for _, tc := range []struct {
		name      string
		spec      string
		wantCount int
	}{
		{name: "configured", spec: kubernetesTestSpec, wantCount: 3},
		{name: "default replicas", spec: strings.ReplaceAll(kubernetesTestSpec, "    replicas: 3\n", ""), wantCount: 1000},
		{name: "no maximum replicas", spec: strings.ReplaceAll(kubernetesTestSpec, "replicas: 3", "replicas: 5001"), wantCount: 5001},
		{name: "numeric quantities", spec: strings.ReplaceAll(strings.ReplaceAll(kubernetesTestSpec, `cpu: "4"`, "cpu: 4.0"), `pods: "40"`, "pods: 40"), wantCount: 3},
		{name: "fractional cores", spec: strings.ReplaceAll(strings.ReplaceAll(kubernetesTestSpec, `cpu: "4"`, "cpu: 1500m"), `pods: "40"`, `pods: "15"`), wantCount: 3},
		{name: "optional run label", spec: strings.ReplaceAll(kubernetesTestSpec, "          run_id: example-run\n", ""), wantCount: 3},
		{name: "extended resources", spec: strings.ReplaceAll(kubernetesTestSpec, "memory: 16Gi", "memory: 16Gi\n          nvidia.com/gpu: 8"), wantCount: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := NewKubernetesGenerator(kubernetesSpec(t, tc.spec), "/does-not-exist/kubeconfig")
			require.NoError(t, err)
			assert.Equal(t, tc.wantCount, *g.config.NodeGroups[0].Replicas)
		})
	}
}

// TestNewKubernetesGeneratorRejectsInvalidConfig checks run settings and parsing errors.
func TestNewKubernetesGeneratorRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{name: "zero replicas", old: "replicas: 3", replacement: "replicas: 0"},
		{name: "negative replicas", old: "replicas: 3", replacement: "replicas: -1"},
		{name: "string replicas", old: "replicas: 3", replacement: "replicas: '3'"},
		{name: "fractional replicas", old: "replicas: 3", replacement: "replicas: 1.5"},
		{name: "missing group name", old: "name: workers", replacement: "name: ''"},
		{name: "missing duration", old: "duration: 10ms\n", replacement: ""},
		{name: "zero duration", old: "duration: 10ms", replacement: "duration: 0s"},
		{name: "negative duration", old: "duration: 10ms", replacement: "duration: -1s"},
		{name: "invalid duration", old: "duration: 10ms", replacement: "duration: eventually"},
		{name: "unknown field", old: "duration: 10ms", replacement: "duration: 10ms\npods: {}"},
		{name: "removed context", old: "duration: 10ms", replacement: "duration: 10ms\ncontext: scale"},
		{name: "removed hold_for", old: "duration: 10ms", replacement: "hold_for: 10ms"},
		{name: "removed run_id", old: "duration: 10ms", replacement: "duration: 10ms\nrun_id: example-run"},
		{name: "unknown group field", old: "replicas: 3", replacement: "replicas: 3\n    unknown: value"},
		{name: "duplicate field", old: "replicas: 3", replacement: "replicas: 3\n    replicas: 4"},
		{name: "unknown native field", old: "      status:", replacement: "      specc: {}\n      status:"},
		{name: "invalid quantity", old: "memory: 16Gi", replacement: "memory: invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := kubernetesSpec(t, strings.Replace(kubernetesTestSpec, tc.old, tc.replacement, 1))
			_, err := NewKubernetesGenerator(spec, "/example/kubeconfig")
			assert.Error(t, err)
		})
	}
	// Reuse the native template to isolate group-level configuration errors.
	group := kubernetesTestSpec[strings.Index(kubernetesTestSpec, "  - name:"):]
	for _, tc := range []struct{ name, spec string }{
		{name: "missing groups", spec: "duration: 1s"},
		{name: "empty groups", spec: "duration: 1s\nnodeGroups: []"},
		{name: "null groups", spec: "duration: 1s\nnodeGroups: null"},
		{name: "duplicate names", spec: kubernetesTestSpec + group},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewKubernetesGenerator(kubernetesSpec(t, tc.spec), "/example/kubeconfig")
			assert.Error(t, err, "invalid node groups: %s", tc.spec)
		})
	}
	_, err := NewKubernetesGenerator(yaml.Node{}, "/example/config")
	assert.Error(t, err, "omitted specification")
}

// TestNewKubernetesGeneratorLeavesNodeValidationToAPI checks that semantic validation stays with Kubernetes.
func TestNewKubernetesGeneratorLeavesNodeValidationToAPI(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{name: "invalid label", old: "run_id: example-run", replacement: "run_id: invalid/id"},
		{name: "negative capacity", old: "memory: 16Gi", replacement: "memory: -16Gi"},
		{name: "missing capacity", old: "          memory: 16Gi\n", replacement: ""},
		{name: "pod capacity", old: `pods: "40"`, replacement: "pods: '250'"},
		{name: "allocatable", old: "      status:", replacement: "      status:\n        allocatable:\n          cpu: '5'"},
		{name: "metadata", old: "      metadata:", replacement: "      metadata:\n        annotations:\n          example.com/note: custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := strings.Replace(kubernetesTestSpec, tc.old, tc.replacement, 1)
			_, err := NewKubernetesGenerator(kubernetesSpec(t, spec), "/nonexistent/config")
			assert.NoError(t, err, "parsing without template validation")
		})
	}
}

// kubernetesSpec parses test YAML and returns its root mapping.
// It reports invalid fixture YAML as a fatal setup error.
func kubernetesSpec(t *testing.T, data string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(data), &doc))
	return *doc.Content[0]
}

// TestKubernetesGeneratorRun exercises the lifecycle and cleanup failure paths.
func TestKubernetesGeneratorRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wantError string
		wantLeft  int
	}{
		{name: "complete"},
		{name: "partial create", wantError: "create Node"},
		{name: "invalid node", wantError: "API rejected Node"},
		{name: "lost create response", wantError: "create Node"},
		{name: "cancel during create", wantError: "context canceled"},
		{name: "cancel during hold", wantError: "context canceled"},
		{name: "never ready", wantError: "context deadline exceeded"},
		{name: "missing node", wantError: "owned Nodes"},
		{name: "replaced node", wantError: "refuse to delete unowned Node", wantLeft: 1},
		{name: "ownership changed", wantError: "refuse to delete unowned Node", wantLeft: 1},
		{name: "cleanup denied", wantError: "delete Node", wantLeft: 1},
		{name: "cancel and cleanup denied", wantError: "context canceled", wantLeft: 1},
		{name: "list denied", wantError: "wait for emulated Nodes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			timeout := 5 * time.Second
			if tc.name == "never ready" {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), timeout)
			defer cancel()
			api, config := newNodeTestAPI(t, tc.name, cancel)
			spec := kubernetesTestSpec
			if tc.name == "invalid node" {
				spec = strings.Replace(spec, "run_id: example-run", "run_id: invalid/id", 1)
			}
			if tc.name == "cancel during hold" || tc.name == "cancel and cleanup denied" {
				spec = strings.Replace(spec, "duration: 10ms", "duration: 1s", 1)
			}
			g, err := NewKubernetesGenerator(kubernetesSpec(t, spec), config)
			require.NoError(t, err)
			err = g.Run(ctx)
			if tc.wantError == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.wantError)
			}
			if tc.name == "cancel and cleanup denied" {
				assert.ErrorIs(t, err, context.Canceled)
				assert.ErrorContains(t, err, "delete Node")
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			assert.Len(t, api.nodes, tc.wantLeft+1)
			assert.Contains(t, api.nodes, "existing")
			if tc.wantError == "" {
				for _, node := range api.createdNodes {
					assert.Equal(t, "example-run", node.Labels["run_id"])
					for name, want := range map[corev1.ResourceName]string{corev1.ResourceCPU: "4", corev1.ResourceMemory: "16Gi", corev1.ResourcePods: "40"} {
						got, allocatable := node.Status.Capacity[name], node.Status.Allocatable[name]
						assert.Zero(t, got.Cmp(resource.MustParse(want)), "capacity %s", name)
						assert.Zero(t, allocatable.Cmp(got), "allocatable %s", name)
					}
				}
				assert.Len(t, api.created, 3)
				assert.GreaterOrEqual(t, api.deletedAt.Sub(api.readyAt), g.config.Duration.Duration)
			}
		})
	}
}

// TestKubernetesGeneratorNodeGroups checks distinct templates and their preservation.
func TestKubernetesGeneratorNodeGroups(t *testing.T) {
	api, config := newNodeTestAPI(t, "complete", func() {})
	spec := kubernetesTestSpec + `  - name: gpu
    replicas: 2
    nodeTemplate:
      apiVersion: v1
      kind: Node
      metadata:
        labels:
          run_id: gpu-run
          kubernetes.io/arch: arm64
          topology.kubernetes.io/zone: zone-a
        annotations:
          example.com/note: custom
      spec:
        providerID: example://gpu
        unschedulable: true
        taints:
          - key: nvidia.com/gpu
            value: present
            effect: NoSchedule
          - key: kwok.x-k8s.io/node
            value: fake
            effect: NoSchedule
      status:
        capacity:
          cpu: "8"
          memory: 32Gi
          pods: "80"
          nvidia.com/gpu: "2"
        allocatable:
          cpu: "7"
          memory: 30Gi
          pods: "70"
          nvidia.com/gpu: "2"
`
	g, err := NewKubernetesGenerator(kubernetesSpec(t, spec), config)
	require.NoError(t, err)
	original := g.config.NodeGroups[1].NodeTemplate.DeepCopy()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	assert.NoError(t, g.Run(ctx))
	api.mu.Lock()
	defer api.mu.Unlock()
	counts := make(map[string]int)
	for _, node := range api.createdNodes {
		counts[node.Labels["run_id"]]++
		if node.Labels["run_id"] != "gpu-run" {
			continue
		}
		for key, want := range original.Labels {
			assert.Equal(t, want, node.Labels[key], "label %s", key)
		}
		assert.Equal(t, "custom", node.Annotations["example.com/note"])
		assert.Equal(t, original.Spec, node.Spec)
		for key, want := range original.Status.Capacity {
			got := node.Status.Capacity[key]
			assert.Zero(t, got.Cmp(want), "capacity %s", key)
		}
		for key, want := range original.Status.Allocatable {
			got := node.Status.Allocatable[key]
			assert.Zero(t, got.Cmp(want), "allocatable %s", key)
		}
	}
	assert.Equal(t, map[string]int{"example-run": 3, "gpu-run": 2}, counts)
	assert.Len(t, api.createdNodes, 5)
	assert.Len(t, api.nodes, 1)
	assert.NotNil(t, api.nodes["existing"])
	assert.Equal(t, original, &g.config.NodeGroups[1].NodeTemplate, "unchanged node template")
}

// TestKubernetesGeneratorReplicaCounts checks exact creation counts with existing nodes.
func TestKubernetesGeneratorReplicaCounts(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			api, config := newNodeTestAPI(t, "complete", func() {})
			// Exceed the former cluster limit without including these nodes in the run.
			for i := range 5000 {
				name := fmt.Sprintf("real-%04d", i)
				api.nodes[name] = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
			}
			spec := strings.Replace(kubernetesTestSpec, "replicas: 3", fmt.Sprintf("replicas: %d", count), 1)
			g, err := NewKubernetesGenerator(kubernetesSpec(t, spec), config)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			assert.NoError(t, g.Run(ctx))
			api.mu.Lock()
			defer api.mu.Unlock()
			assert.Len(t, api.created, count)
			for i, name := range api.created {
				assert.True(t, strings.HasSuffix(name, fmt.Sprintf("-%04d", i)), "node %d: %s", i, name)
			}
			assert.Len(t, api.nodes, 5001)
		})
	}
}

// TestNewKubernetesGeneratorMultipleLargeGroups checks that replicas have no aggregate cap.
func TestNewKubernetesGeneratorMultipleLargeGroups(t *testing.T) {
	group := kubernetesTestSpec[strings.Index(kubernetesTestSpec, "  - name:"):]
	secondGroup := strings.Replace(group, "name: workers", "name: more-workers", 1)
	spec := strings.ReplaceAll(kubernetesTestSpec+secondGroup, "replicas: 3", "replicas: 3000")
	g, err := NewKubernetesGenerator(kubernetesSpec(t, spec), "/nonexistent/config")
	require.NoError(t, err)
	require.Len(t, g.config.NodeGroups, 2)
	for _, group := range g.config.NodeGroups {
		assert.Equal(t, 3000, *group.Replicas, "group %s", group.Name)
	}
}

// TestKubernetesGeneratorRunCanceled checks cancellation before credential access.
func TestKubernetesGeneratorRunCanceled(t *testing.T) {
	g, err := NewKubernetesGenerator(kubernetesSpec(t, kubernetesTestSpec), "/nonexistent/config")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, g.Run(ctx), context.Canceled)
}

// nodeTestAPI serves the real client's node endpoints. It models KWOK readiness,
// API failures and concurrent replacement without substituting a client interface.
type nodeTestAPI struct {
	// t reports protocol and lifecycle violations from HTTP handlers.
	t *testing.T
	// mu protects state shared between request handlers and test assertions.
	mu sync.Mutex
	// mode selects the failure or lifecycle event injected by the server.
	mode string
	// cancel interrupts the generator in cancellation scenarios.
	cancel context.CancelFunc
	// nodes holds the server's current objects, including the pre-existing node.
	nodes map[string]*corev1.Node
	// created records accepted node names in creation order.
	created []string
	// createdNodes preserves request bodies before simulated controller updates.
	createdNodes []corev1.Node
	// changed prevents repeating an injected deletion or ownership change.
	changed bool
	// deleteDenied records whether the single forbidden deletion was injected.
	deleteDenied bool
	// readyAt records the first readiness poll for duration assertions.
	readyAt time.Time
	// deletedAt records the first successful deletion for duration assertions.
	deletedAt time.Time
}

// newNodeTestAPI starts a server for mode and returns its isolated kubeconfig path.
// The server closes when the test finishes. cancel controls the active generator.
func newNodeTestAPI(t *testing.T, mode string, cancel context.CancelFunc) (*nodeTestAPI, string) {
	t.Helper()
	api := &nodeTestAPI{
		t: t, mode: mode, cancel: cancel,
		nodes: map[string]*corev1.Node{
			"existing": {ObjectMeta: metav1.ObjectMeta{Name: "existing", UID: "real-node"}},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(api.serveHTTP))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	config := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: scale
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: scale
  context:
    cluster: test
`, server.URL)
	require.NoError(t, os.WriteFile(path, []byte(config), 0600))
	return api, path
}

// serveHTTP handles Node requests through the real client transport and injects
// the selected scenario. It serializes access to the simulated cluster state.
func (a *nodeTestAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	// write emits JSON responses and reports transport failures to the test.
	write := func(value any) {
		assert.NoError(a.t, json.NewEncoder(w).Encode(value), "encode API response")
	}
	// decode accepts the wire formats used by client-go and reports malformed requests.
	decode := func(object runtime.Object) bool {
		data, err := io.ReadAll(r.Body)
		if err == nil {
			err = runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), data, object)
		}
		if !assert.NoError(a.t, err, "decode API request") {
			http.Error(w, "invalid request", http.StatusBadRequest)
		}
		return err == nil
	}
	// path is the collection endpoint shared by list and create requests.
	const path = "/api/v1/nodes"
	if r.Method == http.MethodPost && r.URL.Path == path {
		if a.mode == "partial create" && len(a.created) == 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var node corev1.Node
		if !decode(&node) {
			return
		}
		if a.mode == "invalid node" {
			assert.Equal(a.t, "invalid/id", node.Labels["run_id"])
			w.WriteHeader(http.StatusUnprocessableEntity)
			write(metav1.Status{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
				Status:   "Failure",
				Reason:   metav1.StatusReasonInvalid,
				Code:     http.StatusUnprocessableEntity,
				Message:  "API rejected Node: invalid label",
			})
			return
		}
		assert.Equal(a.t, "fake", node.Annotations[kwokNodeKey])
		assert.NotEmpty(a.t, node.Labels[kubernetesOwnerLabel])
		assert.True(a.t, slices.ContainsFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
			return taint.Key == kwokNodeKey && taint.Value == "fake" && taint.Effect == corev1.TaintEffectNoSchedule
		}), "KWOK NoSchedule taint")
		a.createdNodes = append(a.createdNodes, *node.DeepCopy())
		assert.NotContains(a.t, a.nodes, node.Name, "unique node name")
		a.created = append(a.created, node.Name)
		node.UID = types.UID(fmt.Sprintf("uid-%d", len(a.created)))
		node.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}
		if a.mode != "never ready" {
			node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
		}
		a.nodes[node.Name] = &node
		if a.mode == "lost create response" {
			http.Error(w, "response lost after persistence", http.StatusInternalServerError)
			return
		}
		if a.mode == "cancel during create" {
			a.cancel()
		}
		write(node)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == path {
		if a.mode == "list denied" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		selector, err := labels.Parse(r.URL.Query().Get("labelSelector"))
		if !assert.NoError(a.t, err, "parse label selector") {
			http.Error(w, "invalid selector", http.StatusBadRequest)
			return
		}
		if !selector.Empty() {
			if a.mode == "cancel during hold" || a.mode == "cancel and cleanup denied" {
				time.AfterFunc(10*time.Millisecond, a.cancel)
			}
			if !a.changed && len(a.created) > 0 {
				node := a.nodes[a.created[0]]
				switch a.mode {
				case "replaced node":
					node.UID = "replacement"
					a.changed = true
				case "ownership changed":
					node.Labels[kubernetesOwnerLabel] = "someone-else"
					a.changed = true
				case "missing node":
					delete(a.nodes, node.Name)
					a.changed = true
				}
			}
			if a.readyAt.IsZero() {
				a.readyAt = time.Now()
			}
		}
		list := corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}}
		for _, node := range a.nodes {
			if selector.Matches(labels.Set(node.Labels)) {
				list.Items = append(list.Items, *node)
			}
		}
		write(list)
		return
	}
	if strings.HasPrefix(r.URL.Path, path+"/") {
		name := strings.TrimPrefix(r.URL.Path, path+"/")
		node := a.nodes[name]
		if node == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			write(node)
		case http.MethodDelete:
			var options metav1.DeleteOptions
			if !decode(&options) {
				return
			}
			if !assert.NotNil(a.t, options.Preconditions) || !assert.NotNil(a.t, options.Preconditions.UID) || !assert.Equal(a.t, node.UID, *options.Preconditions.UID) {
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
			assert.NotEqual(a.t, "existing", name, "preserve pre-existing node")
			if (a.mode == "cleanup denied" || a.mode == "cancel and cleanup denied") && !a.deleteDenied {
				a.deleteDenied = true
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if a.deletedAt.IsZero() {
				a.deletedAt = time.Now()
			}
			delete(a.nodes, name)
			write(metav1.Status{Status: "Success"})
		default:
			assert.Fail(a.t, "unexpected method", "%s", r.Method)
		}
		return
	}
	assert.Fail(a.t, "unexpected request", "%s %s", r.Method, r.URL.Path)
	http.Error(w, "unexpected request", http.StatusNotFound)
}

// TestKubernetesNodeCleanupBudget checks minimum and scaled cleanup allowances.
func TestKubernetesNodeCleanupBudget(t *testing.T) {
	for _, tc := range []struct {
		count int
		want  time.Duration
	}{
		{count: 0, want: 5 * time.Minute},
		{count: 1, want: 5 * time.Minute},
		{count: 32, want: 5 * time.Minute},
		{count: 33, want: 5 * time.Minute},
		{count: 1000, want: 16*time.Minute + 30*time.Second},
		{count: 6000, want: 97 * time.Minute},
	} {
		t.Run(strconv.Itoa(tc.count), func(t *testing.T) {
			assert.Equal(t, tc.want, kubernetesNodeCleanupBudget(tc.count))
		})
	}
}

// TestCleanupKubernetesNodesLargeBatch exercises the real client and its rate
// limiter with simulated latency. Virtual time makes the scale case inexpensive.
func TestCleanupKubernetesNodesLargeBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api, owned := cleanupTestNodes(t, 6000)
		var mu sync.Mutex
		active, peak, requests := 0, 0, 0
		client := cleanupTestClient(t, func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			active++
			peak = max(peak, active)
			requests++
			mu.Unlock()
			defer func() {
				mu.Lock()
				active--
				mu.Unlock()
			}()
			timer := time.NewTimer(20 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-timer.C:
			}
			w := httptest.NewRecorder()
			api.serveHTTP(w, r)
			return w.Result(), nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), kubernetesNodeCleanupBudget(len(owned)))
		defer cancel()
		start := time.Now()
		require.NoError(t, cleanupKubernetesNodes(ctx, client, owned))
		assert.Greater(t, peak, 1, "cleanup must overlap requests")
		assert.LessOrEqual(t, peak, kubernetesNodeCleanupWorkers)
		assert.Equal(t, 3*len(owned), requests)
		assert.Less(t, time.Since(start), 5*time.Minute, "serial requests would take six minutes")
		assert.Len(t, api.nodes, 1)
		assert.Contains(t, api.nodes, "existing")
	})
}

// TestCleanupKubernetesNodesStalled checks that node deadlines release workers
// and that each error is retained while later nodes still get deleted.
func TestCleanupKubernetesNodesStalled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api, owned := cleanupTestNodes(t, 2*kubernetesNodeCleanupWorkers+1)
		client := cleanupTestClient(t, func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, owned[0].Name) {
				// Accept deletion without removing the node, as with a stuck finalizer.
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.NewEncoder(w).Encode(metav1.Status{Status: "Success"}))
			} else if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, owned[1].Name) {
				http.Error(w, "forbidden", http.StatusForbidden)
			} else {
				api.serveHTTP(w, r)
			}
			return w.Result(), nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), kubernetesNodeCleanupBudget(len(owned)))
		defer cancel()
		err := cleanupKubernetesNodes(ctx, client, owned)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.ErrorContains(t, err, "wait for Node "+owned[0].Name)
		assert.ErrorContains(t, err, "delete Node "+owned[1].Name)
		assert.NoError(t, ctx.Err(), "per-node expiry must not cancel other workers")
		assert.Len(t, api.nodes, 3)
		assert.Contains(t, api.nodes, "existing")
		assert.Contains(t, api.nodes, owned[0].Name)
		assert.Contains(t, api.nodes, owned[1].Name)
	})
}

// TestCleanupKubernetesNodesDeadline checks that the overall deadline stops and
// joins all workers even when every in-flight API request is blocked.
func TestCleanupKubernetesNodesDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, owned := cleanupTestNodes(t, 2*kubernetesNodeCleanupWorkers)
		client := cleanupTestClient(t, func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		assert.ErrorIs(t, cleanupKubernetesNodes(ctx, client, owned), context.DeadlineExceeded)
		assert.Equal(t, time.Second, time.Since(start))
	})
}

// cleanupTestNodes seeds owned nodes plus one unrelated node for cleanup tests.
func cleanupTestNodes(t *testing.T, count int) (*nodeTestAPI, []*corev1.Node) {
	t.Helper()
	api := &nodeTestAPI{
		t:     t,
		nodes: map[string]*corev1.Node{"existing": {ObjectMeta: metav1.ObjectMeta{Name: "existing"}}},
	}
	owned := make([]*corev1.Node, count)
	for i := range count {
		name := fmt.Sprintf("node-%04d", i)
		node := &corev1.Node{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				UID:    types.UID(name),
				Labels: map[string]string{kubernetesOwnerLabel: "test-run"},
			},
		}
		owned[i] = node.DeepCopy()
		api.nodes[name] = node
	}
	return api, owned
}

// cleanupTestTransport runs API requests inside the virtual-time test bubble.
type cleanupTestTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper without real network I/O.
func (f cleanupTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	return f(r)
}

// cleanupTestClient preserves client-go serialization and throttling while
// replacing the wire transport so request delays can use virtual time.
func cleanupTestClient(t *testing.T, transport cleanupTestTransport) coreclient.NodeInterface {
	t.Helper()
	client, err := coreclient.NewForConfig(&rest.Config{
		Host:      "http://nodes.test",
		QPS:       kubernetesNodeQPS,
		Burst:     kubernetesNodeQPS,
		Timeout:   10 * time.Second,
		Transport: transport,
	})
	require.NoError(t, err)
	return client.Nodes()
}
