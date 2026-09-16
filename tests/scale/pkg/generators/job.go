package generators

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// jobOwnerLabel records the generated Job name so cleanup can verify ownership
	// even when the Create response is lost and the UID is unknown.
	jobOwnerLabel = "scale.nktp.io/generator"

	jobPollInterval = time.Second

	// jobCleanupTimeout gives resource deletion its own budget after cancellation.
	jobCleanupTimeout = 30 * time.Second

	// jobDiagnosticsTimeout limits failure inspection before cleanup starts.
	jobDiagnosticsTimeout = 5 * time.Second
)

// JobGenerator runs one finite binary in a Kubernetes Job. Construct it with NewJobGenerator.
type JobGenerator struct {
	kubeconfig string
	config     jobConfig
}

// jobConfig is the plan's Job specification. Kubeconfig selection is supplied
// separately so it can be shared across the plan.
type jobConfig struct {
	Context   string   `yaml:"context"`
	Namespace string   `yaml:"namespace"`
	Image     string   `yaml:"image"`
	Cmd       []string `yaml:"cmd"`
	Args      []string `yaml:"args"`
}

// NewJobGenerator validates a Job specification without reading credentials or
// contacting Kubernetes. Kubeconfig must be an absolute path. Image and namespace
// are required in spec. Context defaults to that file's current context. Cmd and
// Args are optional string arrays. Omitting Cmd uses the image entrypoint.
func NewJobGenerator(spec yaml.Node, kubeconfig string) (*JobGenerator, error) {
	if spec.Kind != yaml.MappingNode {
		return nil, errors.New("job spec must be a mapping")
	}
	data, err := yaml.Marshal(&spec)
	if err != nil {
		return nil, fmt.Errorf("encode job spec: %w", err)
	}
	var cfg jobConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode job spec: %w", err)
	}
	// yaml permits scalar coercion to strings. Require literal string values so
	// flags such as "true" and "10" must be quoted, and reject aliases/merge keys.
	for i := 0; i < len(spec.Content); i += 2 {
		key, value := spec.Content[i], spec.Content[i+1]
		values := []*yaml.Node{value}
		if key.Value == "cmd" || key.Value == "args" {
			if value.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("%s must be a string array", key.Value)
			}
			values = value.Content
		}
		for _, v := range values {
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				return nil, fmt.Errorf("%s must contain only strings", key.Value)
			}
		}
	}
	if !filepath.IsAbs(kubeconfig) {
		return nil, errors.New("job requires an absolute kubeconfig path")
	}
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, errors.New("job requires an image")
	}
	if errs := validation.IsDNS1123Label(cfg.Namespace); len(errs) != 0 {
		return nil, fmt.Errorf("namespace %q: %s", cfg.Namespace, strings.Join(errs, ", "))
	}
	if len(cfg.Cmd) > 0 && strings.TrimSpace(cfg.Cmd[0]) == "" {
		return nil, errors.New("cmd executable must not be empty")
	}
	return &JobGenerator{kubeconfig: kubeconfig, config: cfg}, nil
}

// Run submits a single-pod Job and waits for completion or failure.
// The context must have a deadline. Run collects failure diagnostics and finishes
// cleanup of owned resources before returning. Diagnostics and cleanup may take
// up to 35 seconds after cancellation.
func (g *JobGenerator) Run(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("job execution requires a context deadline")
	}
	kubeconfig, err := clientcmd.LoadFromFile(g.kubeconfig)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	config, err := clientcmd.NewNonInteractiveClientConfig(*kubeconfig, g.config.Context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return fmt.Errorf("configure Kubernetes client: %w", err)
	}
	config.Timeout = 10 * time.Second
	batch, err := batchclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Job client: %w", err)
	}
	core, err := coreclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Pod client: %w", err)
	}
	jobs, pods := batch.Jobs(g.config.Namespace), core.Pods(g.config.Namespace)
	job := g.manifest(deadline)
	// Install cleanup before Create: a failed response can still mean that the
	// API server accepted the Job. Its unique name and label allow reconciliation.
	defer func() {
		if err != nil && job.UID != "" {
			diagnosticsCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobDiagnosticsTimeout)
			err = errors.Join(err, jobDiagnostics(diagnosticsCtx, pods, job))
			cancel()
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobCleanupTimeout)
		defer cancel()
		err = errors.Join(err, cleanupJob(cleanupCtx, jobs, pods, job), ctx.Err())
	}()
	created, err := jobs.Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create Job %s/%s: %w", job.Namespace, job.Name, err)
	}
	job = created
	err = wait.PollUntilContextCancel(ctx, jobPollInterval, true, func(ctx context.Context) (bool, error) {
		current, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.UID != job.UID {
			return false, errors.New("Job was replaced")
		}
		for _, condition := range current.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			switch condition.Type {
			case batchv1.JobFailed, batchv1.JobFailureTarget:
				return false, fmt.Errorf("%s: %s", condition.Reason, condition.Message)
			case batchv1.JobComplete:
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("wait for Job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return nil
}

// manifest builds a uniquely named Job without contacting Kubernetes. Its deadline
// and TTL let the controllers stop and remove the Job if the harness crashes.
func (g *JobGenerator) manifest(deadline time.Time) *batchv1.Job {
	name := "scale-" + strings.ToLower(rand.Text())
	one, zero := int32(1), int32(0)
	noToken := false
	// Controller bounds also cover a lost Create response or a crashed harness.
	activeSeconds := max(int64(1), int64(time.Until(deadline).Seconds())+1)
	ttlSeconds := int32(60)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: g.config.Namespace, Labels: map[string]string{jobOwnerLabel: name}},
		Spec: batchv1.JobSpec{
			Parallelism: &one, Completions: &one, BackoffLimit: &zero,
			ActiveDeadlineSeconds: &activeSeconds, TTLSecondsAfterFinished: &ttlSeconds,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &noToken,
				Containers: []corev1.Container{{Name: "generator", Image: g.config.Image, Command: g.config.Cmd, Args: g.config.Args}},
			}},
		},
	}
}

// cleanupJob checks the ownership label and any known UID before deleting the Job,
// then waits for that Job and its owned Pods to disappear. It can recover an
// unknown UID by name after a lost Create response. The caller must supply a
// bounded context independent of run cancellation.
func cleanupJob(ctx context.Context, jobs batchclient.JobInterface, pods coreclient.PodInterface, job *batchv1.Job) error {
	current, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && job.UID == "" {
		return nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("find Job for cleanup: %w", err)
	}
	uid := job.UID
	if err == nil {
		if current.Labels[jobOwnerLabel] != job.Name || (uid != "" && current.UID != uid) {
			return fmt.Errorf("refuse to delete unowned Job %s/%s", job.Namespace, job.Name)
		}
		uid = current.UID
		policy := metav1.DeletePropagationForeground
		err = jobs.Delete(ctx, job.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, PropagationPolicy: &policy})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete Job %s/%s: %w", job.Namespace, job.Name, err)
		}
	}
	err = wait.PollUntilContextCancel(ctx, jobPollInterval, true, func(ctx context.Context) (bool, error) {
		current, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		if err == nil && current.UID == uid {
			return false, nil
		}
		remaining, err := pods.List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/controller-uid=" + string(uid)})
		if err != nil {
			return false, err
		}
		for _, pod := range remaining.Items {
			if metav1.IsControlledBy(&pod, &metav1.ObjectMeta{UID: uid}) {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("wait for Job %s/%s cleanup: %w", job.Namespace, job.Name, err)
	}
	return nil
}

// jobDiagnostics gathers bounded status and logs from Pods owned by job.
// The caller must supply a Job with a known UID and a context with a deadline.
// Call it before cleanup. Collected details and collection errors are returned
// together for inclusion in the original run failure.
func jobDiagnostics(ctx context.Context, pods coreclient.PodInterface, job *batchv1.Job) error {
	list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/controller-uid=" + string(job.UID), Limit: 3})
	if err != nil {
		return fmt.Errorf("list generator Pods for diagnostics: %w", err)
	}
	var details []error
	for _, pod := range list.Items[:min(len(list.Items), 3)] {
		if !metav1.IsControlledBy(&pod, job) {
			continue
		}
		details = append(details, fmt.Errorf("Pod %s phase=%s: %s", pod.Name, pod.Status.Phase, pod.Status.Message))
		for _, status := range pod.Status.ContainerStatuses {
			details = append(details, fmt.Errorf("container %s: %+v", status.Name, status.State))
		}
		lines, limit := int64(40), int64(8192)
		stream, err := pods.GetLogs(pod.Name, &corev1.PodLogOptions{Container: "generator", TailLines: &lines, LimitBytes: &limit}).Stream(ctx)
		if err != nil {
			details = append(details, fmt.Errorf("read Pod %s logs: %w", pod.Name, err))
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, limit))
		closeErr := stream.Close()
		details = append(details, fmt.Errorf("Pod %s logs: %s", pod.Name, data), readErr, closeErr)
	}
	return errors.Join(details...)
}
