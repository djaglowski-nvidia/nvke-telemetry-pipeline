package generators

import (
	"context"
	"errors"
	"fmt"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	ktesting "k8s.io/client-go/testing"
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

func TestKubernetesGeneratorRun(t *testing.T) {
	createErr := errors.New("create rejected")
	readErr := errors.New("list denied")
	cleanupErr := errors.New("delete denied")
	for _, test := range []struct {
		name         string
		createErr    error
		lostResponse bool
		cancelAt     string
		neverReady   bool
		missing      bool
		replaced     bool
		foreignOwner bool
		readErr      error
		cleanupErr   error
		wantError    string
		wantLeft     int
	}{
		{name: "complete"},
		{name: "partial create", createErr: createErr, wantError: "create Node"},
		{name: "lost create response", createErr: createErr, lostResponse: true, wantError: "create Node"},
		{name: "cancel during create", cancelAt: "create", wantError: "context canceled"},
		{name: "cancel during hold", cancelAt: "hold", wantError: "context canceled"},
		{name: "never ready", neverReady: true, wantError: "context deadline exceeded"},
		{name: "missing node", missing: true, wantError: "owned Nodes"},
		{name: "replaced node", replaced: true, wantError: "refuse to delete unowned Node", wantLeft: 1},
		{name: "ownership changed", foreignOwner: true, wantError: "refuse to delete unowned Node", wantLeft: 1},
		{name: "cleanup denied", cleanupErr: cleanupErr, wantError: "delete Node", wantLeft: 1},
		{name: "cancel and cleanup denied", cancelAt: "hold", cleanupErr: cleanupErr, wantError: "delete Node", wantLeft: 1},
		{name: "list denied", readErr: readErr, wantError: "wait for emulated Nodes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g, err := NewKubernetesGenerator(kubernetesSpec(t, kubernetesTestSpec), "/does/not/exist")
				require.NoError(t, err)
				client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "existing"}})
				nodes := corev1.SchemeGroupVersion.WithResource("nodes")
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				var created []*corev1.Node
				client.PrependReactor("create", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
					node := action.(ktesting.CreateAction).GetObject().(*corev1.Node)
					if test.createErr != nil && !test.lostResponse && len(created) == 1 {
						return true, nil, test.createErr
					}
					node.UID = types.UID(node.Name)
					node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
					created = append(created, node.DeepCopy())
					if test.lostResponse || test.cancelAt == "create" {
						require.NoError(t, client.Tracker().Create(nodes, node, ""))
						if test.cancelAt == "create" {
							cancel()
							return true, nil, ctx.Err()
						}
						return true, nil, test.createErr
					}
					return false, nil, nil
				})
				polls := 0
				var readyAt time.Time
				client.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
					polls++
					assert.Equal(t, kubernetesOwnerLabel+"="+created[0].Labels[kubernetesOwnerLabel], action.(ktesting.ListAction).GetListRestrictions().Labels.String())
					if test.readErr != nil {
						return true, nil, test.readErr
					}
					node := created[0].DeepCopy()
					if test.missing {
						require.NoError(t, client.Tracker().Delete(nodes, node.Namespace, node.Name))
						return false, nil, nil
					}
					if test.replaced {
						node.UID = "replacement"
					}
					if test.foreignOwner {
						node.Labels[kubernetesOwnerLabel] = "foreign"
					}
					if polls == 1 || test.neverReady {
						node.Status.Conditions[0].Status = corev1.ConditionFalse
					} else {
						readyAt = time.Now()
						if test.cancelAt == "hold" {
							time.AfterFunc(g.config.Duration.Duration/2, cancel)
						}
					}
					require.NoError(t, client.Tracker().Update(nodes, node, ""))
					return false, nil, nil
				})
				client.PrependReactor("delete", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
					deletion := action.(ktesting.DeleteAction)
					assert.Equal(t, &metav1.Preconditions{UID: new(types.UID(deletion.GetName()))}, deletion.GetDeleteOptions().Preconditions)
					if test.cleanupErr != nil && deletion.GetName() == created[0].Name {
						return true, nil, test.cleanupErr
					}
					if test.wantError == "" {
						assert.GreaterOrEqual(t, time.Since(readyAt), g.config.Duration.Duration, "duration starts when all nodes are ready")
					}
					return false, nil, nil
				})
				err = g.run(ctx, client.CoreV1().Nodes())
				if test.wantError == "" {
					assert.NoError(t, err)
					assert.Len(t, created, 3)
					assert.Equal(t, 2, polls, "wait until every node is ready")
				} else {
					assert.ErrorContains(t, err, test.wantError)
				}
				if ctx.Err() != nil {
					assert.ErrorIs(t, err, ctx.Err())
				}
				if test.cleanupErr != nil {
					assert.ErrorIs(t, err, test.cleanupErr)
				}
				remaining, err := client.Tracker().List(nodes, corev1.SchemeGroupVersion.WithKind("Node"), "")
				require.NoError(t, err)
				assert.Len(t, remaining.(*corev1.NodeList).Items, test.wantLeft+1)
				_, err = client.CoreV1().Nodes().Get(t.Context(), "existing", metav1.GetOptions{})
				assert.NoError(t, err)
			})
		})
	}
}

func TestKubernetesGeneratorNodeGroups(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, err := NewKubernetesGenerator(kubernetesSpec(t, kubernetesTestSpec), "/does/not/exist")
		require.NoError(t, err)
		g.config.NodeGroups = append(g.config.NodeGroups, kubernetesNodeGroup{
			Name: "gpu", Replicas: new(2),
			NodeTemplate: corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"run_id": "gpu-run"}}},
		})
		client := fake.NewClientset()
		counts := make(map[string]int)
		client.PrependReactor("create", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			node := action.(ktesting.CreateAction).GetObject().(*corev1.Node)
			counts[node.Labels["run_id"]]++
			node.UID = types.UID(node.Name)
			node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
			return false, nil, nil
		})
		require.NoError(t, g.run(t.Context(), client.CoreV1().Nodes()))
		assert.Equal(t, map[string]int{"example-run": 3, "gpu-run": 2}, counts)
	})
}

func TestKubernetesNode(t *testing.T) {
	g, err := NewKubernetesGenerator(kubernetesSpec(t, kubernetesTestSpec), "/does/not/exist")
	require.NoError(t, err)
	group := g.config.NodeGroups[0]
	group.NodeTemplate.Annotations = map[string]string{"example.com/note": "preserved"}
	group.NodeTemplate.Spec = corev1.NodeSpec{
		ProviderID: "example://gpu", Unschedulable: true,
		Taints: []corev1.Taint{{Key: "nvidia.com/gpu", Effect: corev1.TaintEffectNoSchedule}, {Key: kwokNodeKey, Value: "old"}},
	}
	group.NodeTemplate.Status.Capacity["nvidia.com/gpu"] = resource.MustParse("2")
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit allocatable=%t", explicit), func(t *testing.T) {
			if explicit {
				group.NodeTemplate.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}
			}
			original := group.NodeTemplate.DeepCopy()
			node := kubernetesNode(group, "owner", 0)
			want := original.DeepCopy()
			want.Name = "workers-owner-0000"
			want.Labels[kubernetesOwnerLabel] = "owner"
			want.Annotations[kwokNodeKey] = "fake"
			want.Spec.Taints[1] = corev1.Taint{Key: kwokNodeKey, Value: "fake", Effect: corev1.TaintEffectNoSchedule}
			if !explicit {
				want.Status.Allocatable = want.Status.Capacity.DeepCopy()
			}
			assert.Equal(t, want, node)
			other := kubernetesNode(group, "owner", 1)
			assert.NotEqual(t, node.Name, other.Name)
			node.Labels["run_id"] = "changed"
			node.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("1")
			assert.Equal(t, *original, group.NodeTemplate)
			assert.Equal(t, "example-run", other.Labels["run_id"])
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

func TestCleanupKubernetesNodesWorkers(t *testing.T) {
	for _, mode := range []string{"complete", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, owned := cleanupTestNodes(kubernetesNodeCleanupWorkers + 1)
				var mu sync.Mutex
				active, peak := 0, 0
				nodes := &cleanupNodeClient{NodeInterface: client.CoreV1().Nodes()}
				nodes.get = func(ctx context.Context, name string, options metav1.GetOptions) (*corev1.Node, error) {
					mu.Lock()
					active++
					peak = max(peak, active)
					mu.Unlock()
					defer func() {
						mu.Lock()
						active--
						mu.Unlock()
					}()
					if mode == "deadline" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					// Blocking outside the fake's reactors lets requests overlap.
					time.Sleep(20 * time.Millisecond)
					return nodes.NodeInterface.Get(ctx, name, options)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				err := cleanupKubernetesNodes(ctx, nodes, owned)
				if mode == "deadline" {
					assert.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					assert.NoError(t, err)
					remaining, err := client.CoreV1().Nodes().List(t.Context(), metav1.ListOptions{})
					require.NoError(t, err)
					require.Len(t, remaining.Items, 1)
					assert.Equal(t, "existing", remaining.Items[0].Name)
				}
				mu.Lock()
				defer mu.Unlock()
				assert.Equal(t, kubernetesNodeCleanupWorkers, peak, "cleanup uses its worker limit")
				assert.Zero(t, active, "all workers stop before cleanup returns")
			})
		})
	}
}

func TestCleanupKubernetesNodesStalled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, owned := cleanupTestNodes(kubernetesNodeCleanupWorkers + 1)
		denied := errors.New("delete denied")
		client.PrependReactor("delete", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
			switch action.(ktesting.DeleteAction).GetName() {
			case owned[0].Name:
				return true, nil, nil // Keep the node present as if a finalizer blocked deletion.
			case owned[1].Name:
				return true, nil, denied
			}
			return false, nil, nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), kubernetesNodeCleanupBudget(len(owned)))
		defer cancel()
		err := cleanupKubernetesNodes(ctx, client.CoreV1().Nodes(), owned)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.ErrorIs(t, err, denied)
		assert.NoError(t, ctx.Err(), "per-node expiry leaves time for later nodes")
		remaining, err := client.CoreV1().Nodes().List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		names := make([]string, 0, len(remaining.Items))
		for _, node := range remaining.Items {
			names = append(names, node.Name)
		}
		assert.ElementsMatch(t, []string{"existing", owned[0].Name, owned[1].Name}, names)
	})
}

func cleanupTestNodes(count int) (*fake.Clientset, []*corev1.Node) {
	objects := []runtime.Object{&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "existing"}}}
	owned := make([]*corev1.Node, count)
	for i := range count {
		name := fmt.Sprintf("node-%04d", i)
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: name, UID: types.UID(name), Labels: map[string]string{kubernetesOwnerLabel: "test-run"},
		}}
		owned[i] = node.DeepCopy()
		objects = append(objects, node)
	}
	return fake.NewClientset(objects...), owned
}

// cleanupNodeClient blocks reads outside client-go's serialized fake reactors.
type cleanupNodeClient struct {
	coreclient.NodeInterface
	get func(context.Context, string, metav1.GetOptions) (*corev1.Node, error)
}

func (c *cleanupNodeClient) Get(ctx context.Context, name string, options metav1.GetOptions) (*corev1.Node, error) {
	return c.get(ctx, name, options)
}
