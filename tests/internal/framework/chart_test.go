// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package framework

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCollectorWorkloads(t *testing.T) {
	collector := func(name string, allocator any) unstructured.Unstructured {
		cr := unstructured.Unstructured{}
		cr.SetName(name)
		if allocator != nil {
			require.NoError(t, unstructured.SetNestedField(cr.Object, allocator, "spec", "targetAllocator", "enabled"))
		}
		return cr
	}

	t.Run("collector and enabled target allocator", func(t *testing.T) {
		got, err := CollectorWorkloads([]unstructured.Unstructured{
			collector("daemon", nil),
			collector("cluster", true),
		})
		require.NoError(t, err)
		assert.Equal(t, []string{
			"daemon-collector",
			"cluster-collector",
			"cluster-targetallocator",
		}, got)
	})

	t.Run("malformed target allocator flag", func(t *testing.T) {
		_, err := CollectorWorkloads([]unstructured.Unstructured{collector("cluster", "yes")})
		assert.ErrorContains(t, err, "reading the target allocator of cluster")
	})
}
