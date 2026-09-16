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
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.yaml.in/yaml/v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
)

func TestNewJobGenerator(t *testing.T) {
	const valid = "context: scale\nnamespace: load\nimage: example:test\n"
	for _, test := range []struct {
		name      string
		spec      string
		wantError bool
	}{
		{name: "inert constructor", spec: valid},
		{name: "command arrays", spec: valid + "cmd: [/binary]\nargs: [metrics, '--rate', '10']"},
		{name: "empty arrays", spec: valid + "cmd: []\nargs: []"},
		{name: "unknown field", spec: valid + "replicas: 2", wantError: true},
		{name: "duplicate field", spec: valid + "image: other:test", wantError: true},
		{name: "missing image", spec: strings.ReplaceAll(valid, "image: example:test\n", ""), wantError: true},
		{name: "default context", spec: strings.ReplaceAll(valid, "context: scale\n", "")},
		{name: "invalid namespace", spec: strings.ReplaceAll(valid, "namespace: load", "namespace: Invalid"), wantError: true},
		{name: "scalar args", spec: valid + "args: metrics", wantError: true},
		{name: "numeric argument", spec: valid + "args: [metrics, 10]", wantError: true},
		{name: "boolean argument", spec: valid + "args: [true]", wantError: true},
		{name: "null args", spec: valid + "args: null", wantError: true},
		{name: "empty executable", spec: valid + "cmd: ['']", wantError: true},
		{name: "merge key", spec: valid + "<<: {cmd: [/binary]}", wantError: true},
		{name: "nonmapping", spec: "[]", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewJobGenerator(jobSpec(t, test.spec), "/does/not/exist")
			if (err != nil) != test.wantError {
				t.Errorf("NewJobGenerator() error = %v, want error %v", err, test.wantError)
			}
		})
	}
}

func jobSpec(t *testing.T, data string) yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(data), &document); err != nil {
		t.Fatalf("decode test spec: %v", err)
	}
	return *document.Content[0]
}

func TestJobGeneratorRun(t *testing.T) {
	for _, test := range []struct {
		name        string
		wantError   string
		wantDeleted bool
	}{
		{name: "complete", wantDeleted: true},
		{name: "current context", wantDeleted: true},
		{name: "missing current context", wantError: "configure Kubernetes client"},
		{name: "failed", wantError: "export failed", wantDeleted: true},
		{name: "create rejected", wantError: "create Job"},
		{name: "lost create response", wantError: "create Job", wantDeleted: true},
		{name: "canceled", wantError: "context canceled", wantDeleted: true},
		{name: "deadline", wantError: "context deadline exceeded", wantDeleted: true},
		{name: "get failed", wantError: "wait for Job", wantDeleted: true},
		{name: "replaced", wantError: "refuse to delete unowned Job"},
		{name: "ownership changed", wantError: "refuse to delete unowned Job"},
		{name: "cleanup failed", wantError: "delete Job"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			api := &jobTestAPI{t: t, mode: test.name, cancel: cancel}
			server := httptest.NewServer(http.HandlerFunc(api.serveHTTP))
			t.Cleanup(server.Close)
			path := filepath.Join(t.TempDir(), "kubeconfig")
			// An unusable current-context proves that the explicitly named context wins.
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: wrong\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: scale\n  context:\n    cluster: test\n    namespace: wrong\n", server.URL)
			if test.name == "current context" {
				config = strings.ReplaceAll(config, "current-context: wrong", "current-context: scale")
			}
			if err := os.WriteFile(path, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			spec := "context: scale\nnamespace: load\nimage: example:test\ncmd: [/binary]\nargs: [metrics, '--rate', '10']"
			if test.name == "current context" || test.name == "missing current context" {
				spec = strings.ReplaceAll(spec, "context: scale\n", "")
			}
			g, err := NewJobGenerator(jobSpec(t, spec), path)
			if err != nil {
				t.Fatalf("NewJobGenerator() error = %v", err)
			}
			err = g.Run(ctx)
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Errorf("Run() error = %v, want containing %q", err, test.wantError)
			}
			if test.name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Errorf("Run() error = %v, want context.Canceled", err)
			}
			if test.name == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Run() error = %v, want context.DeadlineExceeded", err)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.deleted != test.wantDeleted {
				t.Errorf("Job deleted = %v, want %v", api.deleted, test.wantDeleted)
			}
			if test.name == "failed" && err != nil && len(err.Error()) > 9000 {
				t.Errorf("Run() error contains %d bytes, want bounded logs", len(err.Error()))
			}
			if test.name == "failed" && !api.logsRead {
				t.Error("Run() did not collect failure logs from the Running pod")
			}
			if api.deleted && api.job != nil {
				t.Error("Run() returned with the owned Job present")
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
}

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
		if job.Namespace != "load" || job.Labels[jobOwnerLabel] != job.Name {
			a.t.Errorf("Job metadata = %+v, want explicit namespace and ownership", job.ObjectMeta)
		}
		if *job.Spec.Parallelism != 1 || *job.Spec.Completions != 1 || *job.Spec.BackoffLimit != 0 || job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
			a.t.Errorf("Job spec = %+v, want one finite pod without retries", job.Spec)
		}
		if *job.Spec.ActiveDeadlineSeconds <= 0 || *job.Spec.TTLSecondsAfterFinished <= 0 {
			a.t.Error("Job is missing controller deadline/cleanup fallback")
		}
		container := job.Spec.Template.Spec.Containers[0]
		if container.Image != "example:test" || !reflect.DeepEqual(container.Command, []string{"/binary"}) || !reflect.DeepEqual(container.Args, []string{"metrics", "--rate", "10"}) {
			a.t.Errorf("container = %+v, want configured executable and args", container)
		}
		job.UID = "owned"
		job.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
		condition := batchv1.JobComplete
		if a.mode == "failed" {
			condition = batchv1.JobFailed
		}
		if a.mode != "deadline" {
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
			if a.mode == "ownership changed" && a.gets > 1 {
				a.job.Labels[jobOwnerLabel] = "someone-else"
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
			list.Items = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "generator-pod", OwnerReferences: []metav1.OwnerReference{{UID: "owned", Controller: &controller}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}}
		}
		write(list)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == podsPath+"/generator-pod/log" {
		if r.URL.Query().Get("limitBytes") != "8192" || r.URL.Query().Get("tailLines") != "40" || r.URL.Query().Get("container") != "generator" {
			a.t.Errorf("log options = %s, want bounded generator logs", r.URL.RawQuery)
		}
		a.logsRead = true
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "export failed\n", strings.Repeat("x", 16384))
		return
	}
	a.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	http.Error(w, "unexpected request", http.StatusNotFound)
}

func TestJobRunRequiresDeadline(t *testing.T) {
	g, err := NewJobGenerator(jobSpec(t, "context: scale\nnamespace: load\nimage: example:test"), "/does/not/exist")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("Run() error = %v, want missing deadline before credential access", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := g.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want cancellation before credential access", err)
	}
}

func TestJobCleanupWaitIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "uid", Labels: map[string]string{jobOwnerLabel: "owned"}}}
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
			_, err := NewJobGenerator(jobSpec(t, "namespace: load\nimage: example:test"), path)
			if err == nil {
				t.Errorf("NewJobGenerator() succeeded with kubeconfig %q, want absolute path required", path)
			}
		})
	}
}
