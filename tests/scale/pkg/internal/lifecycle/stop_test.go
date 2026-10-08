// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRunError keeps actual failures while recognizing expected work cancellation.
func TestRunError(t *testing.T) {
	failure := errors.New("cleanup failed")
	for _, test := range []struct {
		name  string
		cause error
		err   error
		want  error
	}{
		{name: "successful stop", cause: ErrStageComplete, err: fmt.Errorf("wait: %w", context.Canceled)},
		{name: "caller cancellation", cause: context.Canceled, err: context.Canceled, want: context.Canceled},
		{name: "joined failure", cause: ErrStageComplete, err: errors.Join(context.Canceled, failure), want: failure},
		{name: "deadline", cause: ErrStageComplete, err: context.DeadlineExceeded, want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(test.cause)
			assert.ErrorIs(t, RunError(ctx, test.err), test.want)
		})
	}
}
