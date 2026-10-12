// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const collectorValidationWebhook = "vopentelemetrycollectorcreateupdatebeta.kb.io"

// TestAdmissionWebhook checks that the Operator webhook rejects a deployment
// Collector with a Target Allocator. The denial must identify the webhook.
// Cleanup removes the probe if it was unexpectedly accepted.
func TestAdmissionWebhook(t *testing.T) {
	const name = "nktp-e2e-invalid-webhook-probe"
	invalid := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "opentelemetry.io/v1beta1",
		"kind":       "OpenTelemetryCollector",
		"metadata": map[string]any{
			"name":      name,
			"namespace": testenv.EnvConf().Namespace(),
		},
		"spec": map[string]any{
			"mode": "deployment",
			"config": map[string]any{
				"receivers": map[string]any{
					"otlp": map[string]any{
						"protocols": map[string]any{
							"grpc": map[string]any{},
						},
					},
				},
				"exporters": map[string]any{
					"debug": map[string]any{},
				},
				"service": map[string]any{
					"pipelines": map[string]any{
						"traces": map[string]any{
							"receivers": []any{"otlp"},
							"exporters": []any{"debug"},
						},
					},
				},
			},
			"targetAllocator": map[string]any{
				"enabled": true,
			},
		},
	}}
	f := features.New("OpenTelemetry admission webhook").
		Assess("Rejects deployment collectors with a target allocator", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			resources := c.Client().Resources(c.Namespace())
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute+30*time.Second)
				defer cancel()
				if err := resources.Delete(cleanupCtx, invalid); !apierrors.IsNotFound(err) {
					assert.NoError(t, err, "deleting invalid Collector %s", invalid.GetName())
				}
			})
			createErr := resources.Create(requestCtx, invalid)
			require.Error(t, createErr, "an invalid Collector should be denied")
			assert.ErrorContains(t, createErr, `admission webhook "`+collectorValidationWebhook+`" denied the request`)
			return ctx
		}).
		Feature()
	testenv.Test(t, f)
}
