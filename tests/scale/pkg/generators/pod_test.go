package generators

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
)

// podTestSpec exercises native fields that must survive template replication.
const podTestSpec = `replicas: 3
podTemplate:
  metadata:
    labels:
      run_id: test-run
      scale.nktp.io/lifecycle: complete-30s
    annotations:
      example.com/note: preserved
  spec:
    restartPolicy: Never
    terminationGracePeriodSeconds: 7
    nodeSelector:
      run_id: test-run
    tolerations:
    - key: kwok.x-k8s.io/node
      operator: Equal
      value: fake
      effect: NoSchedule
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

// podSpec decodes a fixture into the plan representation accepted by constructors.
func podSpec(t *testing.T, data string) yaml.Node {
	t.Helper()
	// document retains the YAML node rather than applying Go YAML field mapping.
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(data), &document); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return *document.Content[0]
}

// TestNewPodGenerator checks only decoding, harness defaults and replica counts.
func TestNewPodGenerator(t *testing.T) {
	for _, test := range []struct {
		// name identifies the parsing case.
		name string
		// spec is the constructor input.
		spec string
		// replicas is the expected count on successful parsing.
		replicas int
		// wantError distinguishes structural errors from API-owned validation.
		wantError bool
	}{
		{name: "defaults", spec: "{}", replicas: 1},
		{name: "native template", spec: podTestSpec, replicas: 3},
		{name: "zero", spec: "replicas: 0"},
		{name: "no scale ceiling", spec: "replicas: 100000", replicas: 100000},
		{name: "API validates fields", spec: "podTemplate: {spec: {restartPolicy: invalid}}", replicas: 1},
		{name: "negative", spec: "replicas: -1", wantError: true},
		{name: "unknown generator field", spec: "duration: 1m", wantError: true},
		{name: "unknown Pod field", spec: "podTemplate: {spec: {imag: typo}}", wantError: true},
		{name: "duplicate field", spec: "replicas: 1\nreplicas: 2", wantError: true},
		{name: "wrong type", spec: "replicas: many", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// g must be constructible without accessing the nonexistent kubeconfig.
			g, err := NewPodGenerator(podSpec(t, test.spec), "/does/not/exist")
			if test.wantError {
				if err == nil {
					t.Error("NewPodGenerator() succeeded, want decoding or replica error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPodGenerator(): %v", err)
			}
			if g.config.Replicas != test.replicas || g.config.PodTemplate.Namespace != "default" {
				t.Errorf("config = %+v, want replicas=%d and default namespace", g.config, test.replicas)
			}
			if test.name == "defaults" && g.config.PodTemplate.Spec.RestartPolicy != "" {
				t.Error("constructor injected a restart policy")
			}
			if test.replicas == 0 {
				if err := g.Run(t.Context()); err != nil {
					t.Errorf("empty Run(): %v", err)
				}
			}
			// ctx verifies cancellation before any credential or API access.
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := g.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Errorf("canceled Run() = %v, want context.Canceled", err)
			}
		})
	}
}

// TestPodGeneratorRun covers native submission, independent completion and cleanup.
func TestPodGeneratorRun(t *testing.T) {
	for _, test := range []struct {
		// mode selects the API behavior exercised by the case.
		mode string
		// wantError is a diagnostic fragment, empty on success.
		wantError string
		// wantDeleted counts only resources that this invocation owns.
		wantDeleted int
		// listCodes injects HTTP results, with zero indicating a successful read.
		listCodes []int
		// getCodes selects GET results by UID and an optional /deleted suffix.
		getCodes map[string][]int
		// wantLists checks the number of List calls when nonzero.
		wantLists int
		// wantGets checks per-Pod attempts independently across cleanup phases.
		wantGets map[string]int
	}{
		{mode: "mixed phases", wantDeleted: 3},
		{mode: "failed", wantError: "WorkloadExited: load failed", wantDeleted: 3},
		{mode: "partial create", wantError: "create Pod", wantDeleted: 1},
		{mode: "lost create response", wantError: "create Pod", wantDeleted: 2},
		{mode: "list failed", wantError: "wait for Pods", wantDeleted: 3, wantLists: 1},
		{mode: "transient list", listCodes: []int{503, 0}, wantLists: 2, wantDeleted: 3},
		{mode: "list count resets", listCodes: []int{503, 503, 0, 503, 503, 0}, wantLists: 6, wantDeleted: 3},
		{mode: "list exhausted", listCodes: []int{503, 503, 503}, wantLists: 3, wantDeleted: 3, wantError: "wait for Pods"},
		{mode: "cleanup reads recover", wantDeleted: 3,
			getCodes: map[string][]int{"uid-1": {503, 0}, "uid-2": {503, 0}, "uid-1/deleted": {503, 404}, "uid-2/deleted": {503, 404}},
			wantGets: map[string]int{"uid-1": 2, "uid-2": 2, "uid-1/deleted": 2, "uid-2/deleted": 2}},
		{mode: "ownership read exhausted", wantDeleted: 2, wantError: "find Pod",
			getCodes: map[string][]int{"uid-1": {503, 503, 503}}, wantGets: map[string]int{"uid-1": 3, "uid-2": 1, "uid-3": 1}},
		{mode: "ownership read not found", wantDeleted: 2,
			getCodes: map[string][]int{"uid-1": {503, 404}}, wantGets: map[string]int{"uid-1": 2}},
		{mode: "cleanup wait exhausted", wantDeleted: 3, wantError: "wait for Pod",
			getCodes: map[string][]int{"uid-1/deleted": {503, 503, 503}, "uid-2/deleted": {0, 404}, "uid-3/deleted": {0, 404}},
			wantGets: map[string]int{"uid-1/deleted": 3, "uid-2/deleted": 2, "uid-3/deleted": 2}},
		{mode: "cleanup wait permanent error", wantDeleted: 3, wantError: "wait for Pod",
			getCodes: map[string][]int{"uid-1/deleted": {403, 0}}, wantGets: map[string]int{"uid-1/deleted": 1}},
		{mode: "cleanup count resets", wantDeleted: 3,
			getCodes: map[string][]int{"uid-1/deleted": {503, 0, 503, 0, 404}}, wantGets: map[string]int{"uid-1/deleted": 5}},
		{mode: "canceled", wantError: "context canceled", wantDeleted: 3},
		{mode: "deadline", wantError: "context deadline exceeded", wantDeleted: 3},
		{mode: "disappeared", wantError: "disappeared or was replaced", wantDeleted: 2},
		{mode: "replaced", wantError: "refuse to delete unowned Pod", wantDeleted: 2},
		{mode: "foreign owner", wantError: "refuse to delete unowned Pod", wantDeleted: 2},
		{mode: "delete failed", wantError: "delete Pod", wantDeleted: 2},
	} {
		t.Run(test.mode, func(t *testing.T) {
			// timeout allows status progression but makes a Pending deadline test quick.
			timeout := 10 * time.Second
			if test.mode == "deadline" {
				timeout = 500 * time.Millisecond
			}
			// ctx represents the plan lifetime, independent of deferred cleanup.
			ctx, cancel := context.WithTimeout(t.Context(), timeout)
			defer cancel()
			// api provides the real typed client with deterministic HTTP responses.
			api := &podTestAPI{
				t: t, mode: test.mode, cancel: cancel,
				pods: make(map[string]*corev1.Pod), deleted: make(map[string]bool),
				listCodes: test.listCodes, getCodes: test.getCodes, gets: make(map[string]int),
			}
			// server records all requests, including unexpected Node API calls.
			server := httptest.NewServer(http.HandlerFunc(api.serveHTTP))
			t.Cleanup(server.Close)
			// path points only to this test's credentials.
			path := filepath.Join(t.TempDir(), "kubeconfig")
			// config uses current-context and an unrelated context namespace.
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: scale\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: scale\n  context:\n    cluster: test\n    namespace: wrong\n", server.URL)
			if err := os.WriteFile(path, []byte(config), 0600); err != nil {
				t.Fatalf("write kubeconfig: %v", err)
			}
			// g exercises construction and Run through the public API.
			g, err := NewPodGenerator(podSpec(t, podTestSpec), path)
			if err != nil {
				t.Fatalf("NewPodGenerator(): %v", err)
			}
			api.template = g.config.PodTemplate.DeepCopy()
			err = g.Run(ctx)
			if test.wantError == "" && err != nil {
				t.Errorf("Run(): %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Errorf("Run() = %v, want error containing %q", err, test.wantError)
			}
			if test.mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Run() = %v, want context.DeadlineExceeded", err)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if len(api.deleted) != test.wantDeleted {
				t.Errorf("deleted %d Pods, want %d", len(api.deleted), test.wantDeleted)
			}
			if test.mode == "mixed phases" && api.lists < 3 {
				t.Errorf("returned after %d observations, want all Pods to succeed", api.lists)
			}
			if test.wantLists != 0 && api.lists != test.wantLists {
				t.Errorf("List calls = %d, want %d", api.lists, test.wantLists)
			}
			for key, want := range test.wantGets {
				if api.gets[key] != want {
					t.Errorf("GET calls for %s = %d, want %d", key, api.gets[key], want)
				}
			}
			// diff identifies any mutation of the configured template.
			if diff := cmp.Diff(*api.template, g.config.PodTemplate); diff != "" {
				t.Errorf("Run() changed the configured template (-want +got):\n%s", diff)
			}
		})
	}
}

// podTestAPI models the Pod endpoints used by the generator and delayed deletion.
type podTestAPI struct {
	// t reports malformed or unexpected requests.
	t *testing.T
	// mu protects state shared by HTTP handlers and the test goroutine.
	mu sync.Mutex
	// mode selects the failure or progression scenario.
	mode string
	// cancel simulates cancellation while observing a batch.
	cancel context.CancelFunc
	// template is the unchanged input expected in every submitted Pod.
	template *corev1.PodTemplateSpec
	// pods stores resources, retaining them until all batch deletes are issued.
	pods map[string]*corev1.Pod
	// deleted records successful delete requests, not polling attempts.
	deleted map[string]bool
	// creates counts attempts, including failed responses.
	creates int
	// lists counts observations to advance mixed Pod phases.
	lists int
	// listCodes supplies deterministic failures and successful observations.
	listCodes []int
	// getCodes separates ownership reads from reads after deletion for each UID.
	getCodes map[string][]int
	// gets counts calls independently for each key in getCodes.
	gets map[string]int
}

// serveHTTP emulates only Pod APIs and rejects any unexpected cluster operation.
func (a *podTestAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	// base enforces default namespace even when the kubeconfig namespace differs.
	const base = "/api/v1/namespaces/default/pods"
	// write encodes native API responses and reports transport errors to the test.
	write := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			a.t.Errorf("encode response: %v", err)
		}
	}
	if r.Method == http.MethodPost && r.URL.Path == base {
		a.creates++
		if a.mode == "partial create" && a.creates == 2 {
			http.Error(w, "API rejected Pod", http.StatusUnprocessableEntity)
			return
		}
		// pod decodes the typed client's negotiated request format.
		var pod corev1.Pod
		if !a.decode(w, r, &pod) {
			return
		}
		// diff reports native Pod fields changed during template replication.
		if diff := cmp.Diff(a.template.Spec, pod.Spec); diff != "" {
			a.t.Errorf("submitted Pod %q spec mismatch (-want +got):\n%s", pod.Name, diff)
		}
		// diff reports annotations lost or changed during template replication.
		if diff := cmp.Diff(a.template.Annotations, pod.Annotations); diff != "" {
			a.t.Errorf("submitted Pod %q annotations mismatch (-want +got):\n%s", pod.Name, diff)
		}
		if pod.Namespace != a.template.Namespace {
			a.t.Errorf("submitted Pod %q namespace = %q, want %q", pod.Name, pod.Namespace, a.template.Namespace)
		}
		for key, value := range a.template.Labels {
			if pod.Labels[key] != value {
				a.t.Errorf("Pod label %s = %q, want %q", key, pod.Labels[key], value)
			}
		}
		if pod.Labels[podOwnerLabel] == "" || !strings.HasPrefix(pod.Name, pod.Labels[podOwnerLabel]+"-") || a.pods[pod.Name] != nil {
			a.t.Errorf("Pod %s lacks unique invocation identity", pod.Name)
		}
		pod.UID = types.UID(fmt.Sprintf("uid-%d", a.creates))
		pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
		a.pods[pod.Name] = &pod
		if a.mode == "lost create response" && a.creates == 2 {
			http.Error(w, "response lost after persistence", http.StatusInternalServerError)
			return
		}
		write(pod)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == base {
		a.lists++
		if a.lists <= len(a.listCodes) && a.listCodes[a.lists-1] != 0 {
			http.Error(w, "injected List failure", a.listCodes[a.lists-1])
			return
		}
		if a.mode == "list failed" {
			http.Error(w, "cannot list Pods", http.StatusForbidden)
			return
		}
		if a.mode == "canceled" {
			a.cancel()
		}
		// selector must restrict observation to this invocation.
		selector, err := labels.Parse(r.URL.Query().Get("labelSelector"))
		if err != nil || selector.Empty() {
			a.t.Errorf("invalid ownership selector: %q", r.URL.RawQuery)
			http.Error(w, "missing selector", http.StatusBadRequest)
			return
		}
		// list models mixed phases and unrelated resources in the same namespace.
		list := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
		for name, pod := range a.pods {
			pod.Status.Phase = corev1.PodSucceeded
			if a.lists < len(a.listCodes) {
				pod.Status.Phase = corev1.PodPending
			}
			if a.mode == "deadline" || a.mode == "canceled" || (a.mode == "mixed phases" && pod.UID == "uid-3" && a.lists < 3) {
				pod.Status.Phase = corev1.PodPending
			}
			if a.mode == "mixed phases" && pod.UID == "uid-2" && a.lists == 1 {
				pod.Status.Phase = corev1.PodRunning
			}
			if pod.UID == "uid-1" {
				switch a.mode {
				case "failed":
					pod.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "WorkloadExited", Message: "load failed"}
				case "disappeared":
					delete(a.pods, name)
					continue
				case "replaced":
					pod.UID = "replacement"
				case "foreign owner":
					pod.Labels[podOwnerLabel] = "another-invocation"
				}
			}
			if selector.Matches(labels.Set(pod.Labels)) {
				list.Items = append(list.Items, *pod)
			}
		}
		write(list)
		return
	}
	// name identifies the Pod addressed by cleanup GET and DELETE requests.
	name, ok := strings.CutPrefix(r.URL.Path, base+"/")
	if ok {
		// pod remains present until every owned Pod has received its delete request.
		pod := a.pods[name]
		if r.Method == http.MethodGet && pod != nil {
			// key gives each Pod separate ownership and deletion-wait sequences.
			key := string(pod.UID)
			if a.deleted[name] {
				key += "/deleted"
			}
			a.gets[key]++
			// codes can retain a deleted Pod to require another polling iteration.
			codes := a.getCodes[key]
			if a.gets[key] <= len(codes) {
				// code selects success or the HTTP error for this particular attempt.
				code := codes[a.gets[key]-1]
				if code == 0 {
					write(pod)
				} else {
					if code == http.StatusNotFound {
						delete(a.pods, name)
					}
					http.Error(w, "injected GET failure", code)
				}
				return
			}
		}
		if pod == nil || (a.deleted[name] && len(a.deleted) == len(a.pods)) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			// Failed and foreign deletions must not prevent other Pods disappearing.
			if a.deleted[name] && a.mode != "mixed phases" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			write(pod)
			return
		case http.MethodDelete:
			// options must protect against a replacement between GET and DELETE.
			var options metav1.DeleteOptions
			if !a.decode(w, r, &options) {
				return
			}
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != pod.UID {
				a.t.Error("DELETE lacks matching UID precondition")
			}
			if pod.UID == "replacement" || pod.Labels[podOwnerLabel] == "another-invocation" {
				a.t.Error("DELETE targeted a foreign or replaced Pod")
			}
			if a.mode == "delete failed" && pod.UID == "uid-1" {
				http.Error(w, "delete forbidden", http.StatusForbidden)
				return
			}
			a.deleted[name] = true
			write(metav1.Status{Status: "Success"})
			return
		}
	}
	a.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	http.Error(w, "unexpected request", http.StatusNotFound)
}

// decode accepts the JSON or protobuf encoding negotiated by client-go.
func (a *podTestAPI) decode(w http.ResponseWriter, r *http.Request, into runtime.Object) bool {
	// data holds one native API request body.
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

// TestRetryPodRead checks the retry boundary and keeps the final error available.
func TestRetryPodRead(t *testing.T) {
	for _, test := range []struct {
		// name identifies an API or transport result.
		name string
		// err is returned by every read attempt.
		err error
		// wantAttempts distinguishes transient failures from terminal results.
		wantAttempts int
	}{
		{name: "success", wantAttempts: 1},
		{name: "throttled", err: apierrors.NewTooManyRequests("busy", 0), wantAttempts: 3},
		{name: "internal error", err: apierrors.NewInternalError(errors.New("server failed")), wantAttempts: 3},
		{name: "unavailable", err: apierrors.NewServiceUnavailable("restarting"), wantAttempts: 3},
		{name: "bad gateway", err: apierrors.NewGenericServerResponse(502, "get", schema.GroupResource{Resource: "pods"}, "test", "proxy failed", 0, false), wantAttempts: 3},
		{name: "API timeout", err: apierrors.NewTimeoutError("timed out", 0), wantAttempts: 3},
		{name: "server timeout", err: apierrors.NewServerTimeout(schema.GroupResource{Resource: "pods"}, "get", 0), wantAttempts: 3},
		{name: "network timeout", err: &net.DNSError{Err: "timed out", IsTimeout: true}, wantAttempts: 3},
		{name: "connection reset", err: fmt.Errorf("read: %w", syscall.ECONNRESET), wantAttempts: 3},
		{name: "connection refused", err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), wantAttempts: 3},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, wantAttempts: 3},
		{name: "unauthorized", err: apierrors.NewUnauthorized("credentials"), wantAttempts: 1},
		{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "test", errors.New("denied")), wantAttempts: 1},
		{name: "forbidden transport message", err: apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "test", errors.New("connection reset by peer")), wantAttempts: 1},
		{name: "bad request", err: apierrors.NewBadRequest("invalid"), wantAttempts: 1},
		{name: "not found", err: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "test"), wantAttempts: 1},
		{name: "canceled", err: context.Canceled, wantAttempts: 1},
		{name: "other error", err: errors.New("permanent failure"), wantAttempts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// attempts measures the policy without client-go's internal retries.
				attempts := 0
				// started checks spacing using synctest's deterministic clock.
				started := time.Now()
				// err must retain the final native error for callers to inspect.
				err := retryPodRead(t.Context(), func(context.Context) error {
					attempts++
					return test.err
				})
				if !errors.Is(err, test.err) || attempts != test.wantAttempts {
					t.Errorf("retryPodRead() = (%v, %d attempts), want (%v, %d)", err, attempts, test.err, test.wantAttempts)
				}
				if got, want := time.Since(started), time.Duration(test.wantAttempts-1)*podPollInterval; got != want {
					t.Errorf("retryPodRead() elapsed = %v, want %v", got, want)
				}
			})
		})
	}
}

// TestRetryPodReadCancellation verifies that cancellation interrupts retry waits.
func TestRetryPodReadCancellation(t *testing.T) {
	for _, mode := range []string{"already canceled", "cancel during wait", "deadline during wait"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// ctx is the caller's deadline or explicit cancellation source.
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				// wantErr distinguishes an expired context from explicit cancellation.
				wantErr := context.Canceled
				// wantAttempts excludes any read after cancellation.
				wantAttempts := 1
				switch mode {
				case "already canceled":
					cancel()
					wantAttempts = 0
				case "cancel during wait":
					// timer cancels halfway through the first retry interval.
					timer := time.AfterFunc(podPollInterval/2, cancel)
					defer timer.Stop()
				case "deadline during wait":
					ctx, cancel = context.WithTimeout(ctx, podPollInterval/2)
					defer cancel()
					wantErr = context.DeadlineExceeded
				}
				// attempts records actual callbacks, excluding interrupted retries.
				attempts := 0
				// err must expose context cancellation, not only the preceding API error.
				err := retryPodRead(ctx, func(context.Context) error {
					attempts++
					return apierrors.NewServiceUnavailable("try later")
				})
				if !errors.Is(err, wantErr) || attempts != wantAttempts {
					t.Errorf("retryPodRead() = (%v, %d attempts), want (%v, %d)", err, attempts, wantErr, wantAttempts)
				}
			})
		})
	}
}
