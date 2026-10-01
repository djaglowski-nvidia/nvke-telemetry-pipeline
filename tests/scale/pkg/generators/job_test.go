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
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
)

const jobTestSpec = `context: scale
podTemplate:
  metadata:
    namespace: load
    labels:
      scale.nktp.io/workload: avalanche
  spec:
    containers:
    - name: load
      image: example:test
      command: [/binary]
      args: [metrics, '--rate', '10']
`

func TestNewJobGenerator(t *testing.T) {
	for _, test := range []struct {
		name        string
		spec        string
		parallelism int32
		wantError   bool
	}{
		{name: "defaults", spec: "{}", parallelism: 1},
		{name: "native template", spec: jobTestSpec, parallelism: 1},
		{name: "parallelism", spec: jobTestSpec + "parallelism: 3", parallelism: 3},
		{name: "zero parallelism", spec: "parallelism: 0", wantError: true},
		{name: "negative parallelism", spec: "parallelism: -1", wantError: true},
		{name: "overflow parallelism", spec: "parallelism: 2147483648", wantError: true},
		{name: "unknown generator field", spec: "replicas: 2", wantError: true},
		{name: "unknown Pod field", spec: "podTemplate: {spec: {imag: typo}}", wantError: true},
		{name: "controller label", spec: "podTemplate: {metadata: {labels: {batch.kubernetes.io/job-name: foreign}}}", wantError: true},
		{name: "legacy controller label", spec: "podTemplate: {metadata: {labels: {controller-uid: foreign}}}", wantError: true},
		{name: "ownership label", spec: "podTemplate: {metadata: {labels: {scale.nktp.io/generator: foreign}}}", wantError: true},
		{name: "API validates fields", spec: "podTemplate: {spec: {restartPolicy: invalid}}", parallelism: 1},
		{name: "nonmapping", spec: "[]", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, err := NewJobGenerator(jobSpec(t, test.spec), "/does/not/exist")
			if test.wantError {
				assert.Error(t, err, "NewJobGenerator()")
				return
			}
			require.NoError(t, err, "NewJobGenerator()")
			assert.Equal(t, test.parallelism, g.config.Parallelism, "parallelism")
		})
	}
}

// jobSpec decodes a test fixture into the node consumed by the constructor.
func jobSpec(t *testing.T, data string) yaml.Node {
	t.Helper()
	var document yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(data), &document), "decode test spec")
	return *document.Content[0]
}

func TestJobTopologySpread(t *testing.T) {
	g, err := NewJobGenerator(jobSpec(t, jobTestSpec), "/does/not/exist")
	require.NoError(t, err, "NewJobGenerator()")
	first := g.manifest(time.Now().Add(time.Minute))
	second := g.manifest(time.Now().Add(time.Minute))
	require.NotEqual(t, first.Name, second.Name, "independent Job names")
	honor := corev1.NodeInclusionPolicyHonor
	for _, job := range []*batchv1.Job{first, second} {
		want := []corev1.TopologySpreadConstraint{{
			MaxSkew: 1, TopologyKey: "kubernetes.io/hostname", WhenUnsatisfiable: corev1.DoNotSchedule,
			NodeAffinityPolicy: &honor, NodeTaintsPolicy: &honor,
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{generatorOwnerLabel: job.Name}},
		}}
		require.Equal(t, want, job.Spec.Template.Spec.TopologySpreadConstraints, "topology spread")
		assert.Nil(t, job.Spec.Selector, "Job controller assigns its selector")
		selector, err := metav1.LabelSelectorAsSelector(want[0].LabelSelector)
		require.NoError(t, err, "parse selector")
		other := first
		if job == first {
			other = second
		}
		assert.True(t, selector.Matches(labels.Set(job.Spec.Template.Labels)), "count this Job's Pods")
		assert.False(t, selector.Matches(labels.Set(other.Spec.Template.Labels)), "ignore another Job's Pods")
	}
}

func TestJobManifestPreservesTemplate(t *testing.T) {
	g, err := NewJobGenerator(jobSpec(t, jobTestSpec+`    restartPolicy: OnFailure
    automountServiceAccountToken: true
    topologySpreadConstraints: []
    nodeSelector: {pool: load}
    volumes:
    - name: scratch
      emptyDir: {}
`), "/does/not/exist")
	require.NoError(t, err, "NewJobGenerator()")
	source := g.config.PodTemplate.DeepCopy()
	job := g.manifest(time.Now().Add(time.Minute))
	want := source.DeepCopy()
	want.Labels[generatorOwnerLabel] = job.Name
	assert.Equal(t, *want, job.Spec.Template, "native Pod template preserved")
	job.Spec.Template.Labels["scale.nktp.io/workload"] = "changed"
	job.Spec.Template.Spec.Containers[0].Image = "changed"
	assert.Equal(t, *source, g.config.PodTemplate, "manifest does not mutate configuration")
}

// TestJobGeneratorRun checks completion, failures and cleanup through the Kubernetes API.
func TestJobGeneratorRun(t *testing.T) {
	for _, test := range []struct {
		name        string
		wantError   string
		wantDeleted bool
		parallelism int32
	}{
		{name: "complete", wantDeleted: true},
		{name: "parallel complete", wantDeleted: true, parallelism: 3},
		{name: "parallel failed", wantError: "export failed", wantDeleted: true, parallelism: 3},
		{name: "parallel canceled", wantError: "context canceled", wantDeleted: true, parallelism: 3},
		{name: "current context", wantDeleted: true},
		{name: "missing current context", wantError: "configure Kubernetes client"},
		{name: "failed", wantError: "export failed", wantDeleted: true},
		{name: "create rejected", wantError: "create Job"},
		{name: "lost create response", wantError: "create Job", wantDeleted: true},
		{name: "canceled", wantError: "context canceled", wantDeleted: true},
		{name: "deadline", wantError: "context deadline exceeded", wantDeleted: true},
		{name: "unschedulable", wantError: "PodScheduled=False reason=Unschedulable: 0/3 nodes satisfy topology spread", wantDeleted: true, parallelism: 3},
		{name: "get failed", wantError: "wait for Job", wantDeleted: true},
		{name: "replaced", wantError: "refuse to delete unowned Job"},
		{name: "ownership changed", wantError: "refuse to delete unowned Job"},
		{name: "cleanup failed", wantError: "delete Job"},
	} {
		t.Run(test.name, func(t *testing.T) {
			timeout := 2 * time.Second
			if test.parallelism > 1 && test.name != "unschedulable" {
				timeout = 10 * time.Second
			}
			ctx, cancel := context.WithTimeout(t.Context(), timeout)
			defer cancel()
			mode := strings.TrimPrefix(test.name, "parallel ")
			api := &jobTestAPI{t: t, mode: mode, cancel: cancel, parallelism: max(1, test.parallelism)}
			server := httptest.NewServer(http.HandlerFunc(api.serveHTTP))
			t.Cleanup(server.Close)
			path := filepath.Join(t.TempDir(), "kubeconfig")
			// An unusable current-context proves that the explicitly named context wins.
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: wrong\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: scale\n  context:\n    cluster: test\n    namespace: wrong\n", server.URL)
			if test.name == "current context" {
				config = strings.ReplaceAll(config, "current-context: wrong", "current-context: scale")
			}
			require.NoError(t, os.WriteFile(path, []byte(config), 0600), "write kubeconfig")
			spec := jobTestSpec
			if test.name == "current context" || test.name == "missing current context" {
				spec = strings.ReplaceAll(spec, "context: scale\n", "")
			}
			if test.parallelism > 1 {
				spec += fmt.Sprintf("parallelism: %d\n", test.parallelism)
			}
			g, err := NewJobGenerator(jobSpec(t, spec), path)
			require.NoError(t, err, "NewJobGenerator()")
			err = g.Run(ctx)
			if test.wantError == "" {
				assert.NoError(t, err, "Run()")
			} else {
				assert.ErrorContains(t, err, test.wantError, "Run()")
			}
			if mode == "canceled" {
				require.ErrorIs(t, err, context.Canceled, "Run()")
			}
			if test.name == "deadline" || test.name == "unschedulable" {
				require.ErrorIs(t, err, context.DeadlineExceeded, "Run()")
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			assert.Equal(t, test.wantDeleted, api.deleted, "Job deleted")
			if test.name == "unschedulable" {
				assert.False(t, api.logsRead, "unscheduled pods have no container logs")
			}
			if test.name == "failed" {
				if err != nil {
					assert.LessOrEqual(t, len(err.Error()), 9000, "bounded failure logs")
				}
				assert.True(t, api.logsRead, "collect failure logs from the Running pod")
			}
			if api.deleted {
				assert.Nil(t, api.job, "owned Job removed before Run() returns")
				assert.Zero(t, api.remainingPods, "owned pods removed before Run() returns")
			}
			if mode == "complete" && test.parallelism > 1 {
				assert.GreaterOrEqual(t, api.gets, 3, "observe all completions before Run() returns")
			}
		})
	}
}

// jobTestAPI exercises real client serialization and context propagation without
// requiring Kubernetes. It models terminal conditions and deletion, not controllers.
type jobTestAPI struct {
	t        *testing.T
	mu       sync.Mutex
	mode     string
	cancel   context.CancelFunc
	job      *batchv1.Job
	gets     int
	deleted  bool
	logsRead bool
	// parallelism is the expected pod count in submitted Jobs.
	parallelism int32
	// remainingPods models pods that outlive their Job during foreground deletion.
	remainingPods int32
}

// serveHTTP models the Job and pod endpoints used by Run and cleanup.
func (a *jobTestAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	const jobsPath = "/apis/batch/v1/namespaces/load/jobs"
	const podsPath = "/api/v1/namespaces/load/pods"
	write := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			a.t.Errorf("encode API response: %v", err)
		}
	}
	if r.Method == http.MethodPost && r.URL.Path == jobsPath {
		if a.mode == "create rejected" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var job batchv1.Job
		data, err := io.ReadAll(r.Body)
		if err == nil {
			err = runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), data, &job)
		}
		if err != nil {
			a.t.Errorf("decode Job: %v", err)
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		assert.Equal(a.t, "load", job.Namespace, "Job namespace")
		assert.Equal(a.t, job.Name, job.Labels[generatorOwnerLabel], "Job ownership label")
		assert.Equal(a.t, a.parallelism, *job.Spec.Parallelism, "Job parallelism")
		assert.Equal(a.t, a.parallelism, *job.Spec.Completions, "Job completions")
		assert.Zero(a.t, *job.Spec.BackoffLimit, "Job retries")
		assert.Equal(a.t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy, "pod restart policy")
		if a.parallelism > 1 {
			assert.Equal(a.t, "avalanche", job.Spec.Template.Labels["scale.nktp.io/workload"], "workload selection label")
		}
		assert.Equal(a.t, job.Name, job.Spec.Template.Labels[generatorOwnerLabel], "default spread selector label reaches the API")
		assert.Len(a.t, job.Spec.Template.Spec.TopologySpreadConstraints, 1, "default spread constraint reaches the API")
		assert.Positive(a.t, *job.Spec.ActiveDeadlineSeconds, "Job controller deadline")
		assert.Positive(a.t, *job.Spec.TTLSecondsAfterFinished, "Job cleanup fallback")
		container := job.Spec.Template.Spec.Containers[0]
		assert.Equal(a.t, "load", container.Name, "container name")
		assert.Equal(a.t, "example:test", container.Image, "container image")
		assert.Equal(a.t, []string{"/binary"}, container.Command, "container command")
		assert.Equal(a.t, []string{"metrics", "--rate", "10"}, container.Args, "container args")
		job.UID = "owned"
		job.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
		condition := batchv1.JobComplete
		if a.mode == "failed" {
			condition = batchv1.JobFailed
		}
		if a.mode != "deadline" && a.mode != "unschedulable" {
			job.Status.Conditions = []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue, Reason: "WorkloadExited", Message: "export failed"}}
		}
		a.job = &job
		if a.mode == "lost create response" {
			http.Error(w, "response lost after persistence", http.StatusInternalServerError)
			return
		}
		if a.mode == "canceled" {
			a.cancel()
		}
		write(job)
		return
	}
	if strings.HasPrefix(r.URL.Path, jobsPath+"/") {
		if a.job == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Path != jobsPath+"/"+a.job.Name {
			a.t.Errorf("unexpected Job path %q", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			a.gets++
			if a.mode == "get failed" && a.gets == 1 {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			if a.mode == "complete" && a.parallelism > 1 && a.gets == 1 {
				partial := a.job.DeepCopy()
				partial.Status.Conditions = nil
				partial.Status.Succeeded = 1
				write(partial)
				return
			}
			if a.mode == "ownership changed" && a.gets > 1 {
				a.job.Labels[generatorOwnerLabel] = "someone-else"
			}
			if a.mode == "replaced" {
				a.job.UID = "replacement"
			}
			write(a.job)
		case http.MethodDelete:
			var options metav1.DeleteOptions
			data, err := io.ReadAll(r.Body)
			if err == nil {
				err = runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), data, &options)
			}
			if err != nil {
				a.t.Errorf("decode deletion: %v", err)
			}
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != a.job.UID || options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationForeground {
				a.t.Errorf("delete options = %+v, want UID precondition and foreground propagation", options)
			}
			if a.mode == "cleanup failed" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			a.job = nil
			a.remainingPods = a.parallelism - 1
			a.deleted = true
			write(metav1.Status{Status: "Success"})
		default:
			a.t.Errorf("unexpected method %s", r.Method)
		}
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == podsPath {
		if r.URL.Query().Get("labelSelector") != "batch.kubernetes.io/controller-uid=owned" {
			a.t.Errorf("Pod selector = %q, want owned UID", r.URL.RawQuery)
		}
		list := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
		if a.job != nil && a.job.UID == "owned" {
			controller := true
			list.Items = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "generator-pod", OwnerReferences: []metav1.OwnerReference{{UID: "owned", Controller: &controller}}}, Spec: a.job.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning}}}
			if a.mode == "unschedulable" {
				list.Items[0].Status = corev1.PodStatus{
					Phase: corev1.PodPending,
					Conditions: []corev1.PodCondition{{
						Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
						Message: "0/3 nodes satisfy topology spread",
					}},
				}
			}
		}
		if a.deleted && a.remainingPods > 0 {
			controller := true
			for i := range a.remainingPods {
				list.Items = append(list.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Name:            fmt.Sprintf("remaining-%d", i),
					OwnerReferences: []metav1.OwnerReference{{UID: "owned", Controller: &controller}},
				}})
			}
			a.remainingPods--
		}
		write(list)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == podsPath+"/generator-pod/log" {
		assert.Equal(a.t, "8192", r.URL.Query().Get("limitBytes"), "log byte limit")
		assert.Equal(a.t, "40", r.URL.Query().Get("tailLines"), "log line limit")
		assert.Equal(a.t, "load", r.URL.Query().Get("container"), "log container name")
		a.logsRead = true
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "export failed\n", strings.Repeat("x", 16384))
		return
	}
	a.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	http.Error(w, "unexpected request", http.StatusNotFound)
}

func TestJobRunRequiresDeadline(t *testing.T) {
	g, err := NewJobGenerator(jobSpec(t, jobTestSpec), "/does/not/exist")
	require.NoError(t, err, "NewJobGenerator()")
	assert.ErrorContains(t, g.Run(t.Context()), "deadline", "missing deadline before credential access")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, g.Run(ctx), context.Canceled, "cancellation before credential access")
}

func TestJobCleanupWaitIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "uid", Labels: map[string]string{generatorOwnerLabel: "owned"}}}
		client := &stuckJobClient{job: job}
		ctx, cancel := context.WithTimeout(t.Context(), jobCleanupTimeout)
		defer cancel()
		if err := cleanupJob(ctx, client, nil, job); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("cleanupJob() error = %v, want context.DeadlineExceeded", err)
		}
	})
}

// stuckJobClient models foreground deletion blocked by a controller or finalizer.
type stuckJobClient struct {
	batchclient.JobInterface
	job *batchv1.Job
}

func (c *stuckJobClient) Get(context.Context, string, metav1.GetOptions) (*batchv1.Job, error) {
	return c.job, nil
}

func (*stuckJobClient) Delete(context.Context, string, metav1.DeleteOptions) error {
	return nil
}

func TestNewJobGeneratorRequiresKubeconfig(t *testing.T) {
	for _, path := range []string{"", "relative/kubeconfig"} {
		t.Run(path, func(t *testing.T) {
			_, err := NewJobGenerator(jobSpec(t, jobTestSpec), path)
			assert.Error(t, err, "NewJobGenerator() requires an absolute kubeconfig path")
		})
	}
}
