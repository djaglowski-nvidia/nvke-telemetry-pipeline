//go:build e2e && (e2e_release_owned || !e2e_release_shared)

package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestTargetAllocatorRBAC verifies Role rules and the creation and removal of
// Roles and RoleBindings as secretNamespaces changes.
func TestTargetAllocatorRBAC(t *testing.T) {
	if !*install {
		t.Skip("RBAC updates require a release owned by the test")
	}

	deployed := readyRelease(t.Context(), t)
	baseline := slices.Clone(deployed.ValueFiles)

	ctx, cancel := deadline(t.Context())
	defer cancel()

	secretNamespace, err := k.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "nktp-e2e-secrets-"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create namespace: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), *timeout)
		defer cancel()
		if err := k.CoreV1().Namespaces().Delete(ctx, secretNamespace.Name, metav1.DeleteOptions{}); err != nil {
			t.Errorf("Delete namespace %q: %v", secretNamespace.Name, err)
		}
	})

	override := filepath.Join(t.TempDir(), "secret-namespaces.yaml")
	values := fmt.Sprintf(`daemonset:
  kubernetes:
    targetAllocator:
      prometheusCR:
        secretNamespaces:
          - %s
`, secretNamespace.Name)
	if err := os.WriteFile(override, []byte(values), 0600); err != nil {
		t.Fatalf("Write secretNamespaces override: %v", err)
	}

	if !t.Run("no Role or RoleBinding exists by default", func(t *testing.T) {
		ctx, cancel := deadline(t.Context())
		defer cancel()

		if err := checkTargetAllocatorRBAC(ctx, secretNamespace.Name, false); err != nil {
			t.Error(err)
		}
	}) {
		return
	}

	if !t.Run("configuring secretNamespaces creates a Role and RoleBinding", func(t *testing.T) {
		ctx, cancel := deadline(t.Context())
		defer cancel()

		deployed.ValueFiles = append(slices.Clone(baseline), override)
		if _, err := deployed.Apply(ctx); err != nil {
			t.Fatalf("Apply secretNamespaces override: %v", err)
		}
		if err := checkTargetAllocatorRBAC(ctx, secretNamespace.Name, true); err != nil {
			t.Error(err)
		}
	}) {
		return
	}

	t.Run("removing the override deletes the Role and RoleBinding", func(t *testing.T) {
		ctx, cancel := deadline(t.Context())
		defer cancel()

		deployed.ValueFiles = baseline
		if _, err := deployed.Apply(ctx); err != nil {
			t.Fatalf("Restore baseline values: %v", err)
		}
		if err := checkTargetAllocatorRBAC(ctx, secretNamespace.Name, false); err != nil {
			t.Error(err)
		}
	})
}

func checkTargetAllocatorRBAC(ctx context.Context, secretNamespace string, present bool) error {
	var errs []error
	name := "nktp-daemonset-monitor-" + *namespace
	role, roleErr := k.RbacV1().Roles(secretNamespace).Get(ctx, name, metav1.GetOptions{})
	_, bindingErr := k.RbacV1().RoleBindings(secretNamespace).Get(ctx, name, metav1.GetOptions{})
	for resource, err := range map[string]error{"Role": roleErr, "RoleBinding": bindingErr} {
		if present {
			if err != nil {
				errs = append(errs, fmt.Errorf("get %s %s/%s: %w", resource, secretNamespace, name, err))
			}
		} else if !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("get %s %s/%s returned %v, want NotFound", resource, secretNamespace, name, err))
		}
	}

	if present && roleErr == nil {
		wantRules := []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}},
		}
		if diff := cmp.Diff(wantRules, role.Rules); diff != "" {
			errs = append(errs, fmt.Errorf("Role %s/%s rules mismatch (-want +got):\n%s", secretNamespace, name, diff))
		}
	}
	return errors.Join(errs...)
}
