package generators

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// podTestSpec exercises native fields that must survive template replication.
const podTestSpec = `count: 3
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

func TestNewPodGenerator(t *testing.T) {
	for _, test := range []struct {
		name      string
		spec      string
		count     int
		wantError bool
	}{
		{name: "defaults", spec: "{}", count: 1},
		{name: "native template", spec: podTestSpec, count: 3},
		{name: "zero", spec: "count: 0"},
		{name: "no scale ceiling", spec: "count: 100000", count: 100000},
		{name: "API validates fields", spec: "podTemplate: {spec: {restartPolicy: invalid}}", count: 1},
		{name: "negative", spec: "count: -1", wantError: true},
		{name: "fractional", spec: "count: 1.5", wantError: true},
		{name: "old replicas field", spec: "replicas: 3", wantError: true},
		{name: "unknown generator field", spec: "duration: 1m", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, err := NewPodGenerator(parseSpec(t, test.spec), "/does/not/exist")
			if test.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.count, g.config.Count)
			if test.name == "native template" {
				assert.Equal(t, new(int64(7)), g.config.PodTemplate.Spec.TerminationGracePeriodSeconds)
			}
			if test.name == "defaults" {
				assert.Empty(t, g.config.PodTemplate.Spec.RestartPolicy)
			}
		})
	}
}

func TestPodRunWithoutCredentials(t *testing.T) {
	g, err := NewPodGenerator(parseSpec(t, "count: 0"), "/does/not/exist")
	require.NoError(t, err)
	assert.NoError(t, g.Run(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, g.Run(ctx), context.Canceled)
	g.config.Count = 1
	assert.ErrorIs(t, g.Run(ctx), context.Canceled)
}

func TestPodGeneratorRun(t *testing.T) {
	createErr := errors.New("create rejected")
	readErr := errors.New("list denied")
	cleanupErr := errors.New("delete denied")
	for _, test := range []struct {
		name         string
		namespace    string
		phase        corev1.PodPhase
		createErr    error
		lostResponse bool
		readErr      error
		cancel       bool
		cleanupErr   error
		wantError    string
		wantDeleted  int
	}{
		{name: "complete", wantDeleted: 3},
		{name: "explicit namespace", namespace: "load", wantDeleted: 3},
		{name: "partial create", createErr: createErr, wantError: "create Pod", wantDeleted: 1},
		{name: "lost create response", createErr: createErr, lostResponse: true, wantError: "create Pod", wantDeleted: 2},
		{name: "failed", phase: corev1.PodFailed, wantError: "WorkloadExited: load failed", wantDeleted: 3},
		{name: "list failed", readErr: readErr, wantError: "wait for Pods", wantDeleted: 3},
		{name: "canceled", cancel: true, wantError: "context canceled", wantDeleted: 3},
		{name: "deadline", phase: corev1.PodPending, wantError: "context deadline exceeded", wantDeleted: 3},
		{name: "cancel and cleanup failed", cancel: true, cleanupErr: cleanupErr, wantError: "delete Pod", wantDeleted: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g, err := NewPodGenerator(parseSpec(t, podTestSpec), "/does/not/exist")
				require.NoError(t, err)
				g.config.PodTemplate.Namespace = test.namespace
				original := g.config.PodTemplate.DeepCopy()
				namespace := test.namespace
				if namespace == "" {
					namespace = "default"
				}
				existing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: namespace}}
				client := fake.NewClientset(existing)
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				var created []*corev1.Pod
				client.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
					if test.createErr != nil && !test.lostResponse && len(created) == 1 {
						return true, nil, test.createErr
					}
					pod.UID = types.UID(pod.Name)
					pod.Status.Phase = corev1.PodSucceeded
					if test.phase == corev1.PodFailed {
						pod.Status = corev1.PodStatus{Phase: corev1.PodFailed, Reason: "WorkloadExited", Message: "load failed"}
					}
					if test.phase == corev1.PodPending {
						pod.Status.Phase = corev1.PodPending
					}
					created = append(created, pod.DeepCopy())
					if test.lostResponse && len(created) == 2 {
						require.NoError(t, client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, namespace))
						return true, nil, test.createErr
					}
					return false, nil, nil
				})
				client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					assert.Equal(t, generatorOwnerLabel+"="+created[0].Labels[generatorOwnerLabel], action.(ktesting.ListAction).GetListRestrictions().Labels.String())
					if test.cancel {
						cancel()
						return true, nil, ctx.Err()
					}
					if test.readErr != nil {
						return true, nil, test.readErr
					}
					return false, nil, nil
				})
				deleted := 0
				client.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					deletion := action.(ktesting.DeleteAction)
					assert.Equal(t, &metav1.Preconditions{UID: new(types.UID(deletion.GetName()))}, deletion.GetDeleteOptions().Preconditions)
					if test.cleanupErr != nil && deletion.GetName() == created[0].Name {
						return true, nil, test.cleanupErr
					}
					deleted++
					return false, nil, nil
				})
				err = g.run(ctx, client.CoreV1())
				if test.wantError == "" {
					assert.NoError(t, err)
				} else {
					assert.ErrorContains(t, err, test.wantError)
				}
				if ctx.Err() != nil {
					assert.ErrorIs(t, err, ctx.Err())
				}
				if test.cleanupErr != nil {
					assert.ErrorIs(t, err, test.cleanupErr)
				}
				assert.Equal(t, test.wantDeleted, deleted)
				for _, action := range client.Actions() {
					assert.Equal(t, "pods", action.GetResource().Resource)
					assert.Equal(t, namespace, action.GetNamespace())
				}
				for _, pod := range created {
					assert.Equal(t, original.Spec, pod.Spec)
					assert.Equal(t, original.Annotations, pod.Annotations)
					assert.Subset(t, pod.Labels, original.Labels)
					assert.NotEmpty(t, pod.Labels[generatorOwnerLabel])
					assert.Equal(t, namespace, pod.Namespace)
				}
				assert.Equal(t, *original, g.config.PodTemplate)
				remaining, err := client.Tracker().List(corev1.SchemeGroupVersion.WithResource("pods"), corev1.SchemeGroupVersion.WithKind("Pod"), namespace)
				require.NoError(t, err)
				assert.Len(t, remaining.(*corev1.PodList).Items, 1+len(created)-deleted)
				_, err = client.CoreV1().Pods(namespace).Get(t.Context(), existing.Name, metav1.GetOptions{})
				assert.NoError(t, err)
			})
		})
	}
}

func TestWaitForPods(t *testing.T) {
	for _, mode := range []string{"mixed phases", "missing", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				owned := []*corev1.Pod{
					{ObjectMeta: metav1.ObjectMeta{Name: "first", UID: "first", Labels: map[string]string{generatorOwnerLabel: "owner"}}},
					{ObjectMeta: metav1.ObjectMeta{Name: "second", UID: "second", Labels: map[string]string{generatorOwnerLabel: "owner"}}},
				}
				client := fake.NewClientset()
				polls := 0
				client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
					polls++
					if mode == "mixed phases" && polls == 1 {
						return true, nil, apierrors.NewServiceUnavailable("try again")
					}
					list := &corev1.PodList{Items: []corev1.Pod{*owned[0].DeepCopy(), *owned[1].DeepCopy()}}
					list.Items[0].Status.Phase = corev1.PodSucceeded
					list.Items[1].Status.Phase = corev1.PodSucceeded
					switch mode {
					case "mixed phases":
						if polls == 2 {
							list.Items[1].Status.Phase = corev1.PodPending
						} else if polls == 3 {
							list.Items[1].Status.Phase = corev1.PodRunning
						}
					case "missing":
						list.Items = list.Items[:1]
					case "replaced":
						list.Items[1].UID = "replacement"
					}
					return true, list, nil
				})
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				err := waitForPods(ctx, client.CoreV1().Pods("default"), "owner", owned)
				if mode == "mixed phases" {
					assert.NoError(t, err)
					assert.Equal(t, 4, polls, "wait for every Pod and retry transient List errors")
				} else {
					assert.ErrorContains(t, err, "disappeared or was replaced")
				}
			})
		})
	}
}

func TestCleanupPods(t *testing.T) {
	for _, mode := range []string{"owned", "lost response", "replaced", "foreign owner", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				first := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "first", UID: "first", Labels: map[string]string{generatorOwnerLabel: "owner"}}}
				second := first.DeepCopy()
				second.Name, second.UID = "second", "second"
				owned := []*corev1.Pod{first.DeepCopy(), second.DeepCopy()}
				switch mode {
				case "lost response":
					owned[0].UID = ""
				case "replaced":
					first.UID = "replacement"
				case "foreign owner":
					first.Labels[generatorOwnerLabel] = "foreign"
				}
				client := fake.NewClientset(first, second)
				deleted := make(map[string]bool)
				reads := make(map[string]int)
				client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					name := action.(ktesting.GetAction).GetName()
					key := fmt.Sprintf("%s/%t", name, deleted[name])
					reads[key]++
					if mode == "owned" && reads[key] < podReadAttempts {
						return true, nil, apierrors.NewServiceUnavailable("try again")
					}
					if deleted[name] && mode != "deadline" {
						assert.True(t, deleted[second.Name], "issue all deletes before waiting")
						return true, nil, apierrors.NewNotFound(corev1.Resource("pods"), name)
					}
					return false, nil, nil
				})
				client.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					deletion := action.(ktesting.DeleteAction)
					assert.Equal(t, &metav1.Preconditions{UID: new(types.UID(deletion.GetName()))}, deletion.GetDeleteOptions().Preconditions)
					deleted[deletion.GetName()] = true
					return true, nil, nil
				})
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				err := cleanupPods(ctx, client.CoreV1().Pods(""), "owner", owned)
				switch mode {
				case "replaced", "foreign owner":
					assert.ErrorContains(t, err, "refuse to delete unowned Pod")
					assert.Equal(t, map[string]bool{"second": true}, deleted)
				case "deadline":
					assert.ErrorIs(t, err, context.DeadlineExceeded)
				default:
					assert.NoError(t, err)
					assert.Len(t, deleted, 2)
				}
			})
		})
	}
}

func TestRetryPodRead(t *testing.T) {
	for _, test := range []struct {
		name         string
		err          error
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
				attempts := 0
				started := time.Now()
				err := retryPodRead(t.Context(), func(context.Context) error {
					attempts++
					return test.err
				})
				assert.ErrorIs(t, err, test.err)
				assert.Equal(t, test.wantAttempts, attempts)
				assert.Equal(t, time.Duration(test.wantAttempts-1)*podPollInterval, time.Since(started))
			})
		})
	}
}

func TestRetryPodReadCancellation(t *testing.T) {
	for _, mode := range []string{"already canceled", "cancel during wait", "deadline during wait"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				wantErr := context.Canceled
				wantAttempts := 1
				switch mode {
				case "already canceled":
					cancel()
					wantAttempts = 0
				case "cancel during wait":
					timer := time.AfterFunc(podPollInterval/2, cancel)
					defer timer.Stop()
				case "deadline during wait":
					ctx, cancel = context.WithTimeout(ctx, podPollInterval/2)
					defer cancel()
					wantErr = context.DeadlineExceeded
				}
				attempts := 0
				err := retryPodRead(ctx, func(context.Context) error {
					attempts++
					return apierrors.NewServiceUnavailable("try later")
				})
				assert.ErrorIs(t, err, wantErr)
				assert.Equal(t, wantAttempts, attempts)
			})
		})
	}
}
