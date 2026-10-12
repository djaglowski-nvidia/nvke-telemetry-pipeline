// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestClusterEvents verifies that Normal and Warning Events reach Loki with
// their messages, severity, object identity, and reporting controller intact.
// Events are refreshed to accommodate leader-election delays. Cleanup requests
// deletion of the isolated fixture namespace and its objects.
func TestClusterEvents(t *testing.T) {
	releaseNamespace := testenv.EnvConf().Namespace()
	type eventCheck struct {
		event      *corev1.Event
		expression string
		found      bool
		queryErr   error
	}
	var checks []*eventCheck
	var started time.Time
	var proxyClient coreclient.CoreV1Interface
	ns := envconf.RandomName("nktp-e2e-events", 48)
	f := features.New("Cluster events").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			var err error
			proxyClient, err = coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			setupCtx, err = envfuncs.CreateNamespace(ns)(setupCtx, c)
			require.NoError(t, err, "creating fixture namespace")
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(setupCtx), 30*time.Second)
				defer cancel()
				_, err := envfuncs.DeleteNamespace(ns)(cleanupCtx, c)
				assert.NoError(t, err, "deleting fixture namespace %s", ns)
			})
			started = time.Now()
			object := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "fixture", Namespace: ns}, Data: map[string]string{"fixture": "true"},
			}
			require.NoError(t, c.Client().Resources(ns).Create(setupCtx, object))

			for _, tc := range []struct{ eventType, reason, severity string }{
				{corev1.EventTypeNormal, "NKTPFixtureNormal", "9"},
				{corev1.EventTypeWarning, "NKTPFixtureWarning", "13"},
			} {
				now := metav1.Now()
				event := &corev1.Event{
					ObjectMeta:     metav1.ObjectMeta{GenerateName: "fixture-", Namespace: ns},
					InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "ConfigMap", Namespace: ns, Name: object.Name, UID: object.UID},
					Reason:         tc.reason, Message: ns + " " + tc.reason,
					Type: tc.eventType, Action: "Observe", Count: 1,
					FirstTimestamp: now, LastTimestamp: now,
					Source:              corev1.EventSource{Component: "nktp-e2e"},
					ReportingController: "nktp-e2e", ReportingInstance: "fixture",
				}
				require.NoError(t, c.Client().Resources(ns).Create(setupCtx, event), "creating %s fixture Event", tc.eventType)
				expression := fmt.Sprintf(`{service_name=%q} | k8s_namespace_name=%q | k8s_event_uid=%q | k8s_event_name=%q | k8s_event_reason=%q | k8s_event_reporting_controller=%q | k8s_object_kind="ConfigMap" | k8s_object_name=%q | k8s_object_uid=%q | severity_number=%q | severity_text=%q |= %q`,
					event.ReportingController, ns, event.UID, event.Name, tc.reason, event.ReportingController, object.Name, object.UID, tc.severity, tc.eventType, event.Message)
				checks = append(checks, &eventCheck{event: event, expression: expression})
			}

			return ctx
		}).
		Assess("Normal and Warning Events retain metadata and service_name matches their reporting controller", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			lastRefresh := time.Now()
			err := wait.For(func(ctx context.Context) (bool, error) {
				refresh := time.Since(lastRefresh) >= 10*time.Second
				if refresh {
					lastRefresh = time.Now()
				}
				complete := true
				for _, check := range checks {
					if check.found {
						continue
					}
					if refresh {
						updated := check.event.DeepCopy()
						updated.Count++
						updated.LastTimestamp = metav1.Now()
						err := c.Client().Resources(ns).Update(ctx, updated)
						if err != nil {
							return false, fmt.Errorf("refreshing %s fixture Event: %w", updated.Type, err)
						}
						check.event = updated
					}
					check.found, check.queryErr = lokiHasLogsSince(ctx, proxyClient.Services(releaseNamespace), check.expression, started)
					complete = complete && check.found && check.queryErr == nil
				}
				return complete, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			for _, check := range checks {
				if assert.NoError(t, check.queryErr, "querying %s Event", check.event.Type) {
					assert.True(t, check.found, "fixture Event missing from Loki or missing expected metadata: %s", check.expression)
				}
			}
			assert.NoError(t, err, "waiting for Kubernetes Events in Loki")
			return ctx
		}).Feature()
	testenv.Test(t, f)
}
