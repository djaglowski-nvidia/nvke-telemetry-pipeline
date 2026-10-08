// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package lifecycle defines successful cancellation shared by the runner and generators.
package lifecycle

import (
	"context"
	"errors"
)

// ErrStageComplete requests generator shutdown after the measurement target is reached.
// It is a context cancellation cause, not a returned execution error.
var ErrStageComplete = errors.New("stage target reached")

// RunError removes only expected work cancellation during successful stage stopping.
// Call it before joining cleanup errors. Joined errors are preserved in full.
func RunError(ctx context.Context, err error) error {
	if context.Cause(ctx) == ErrStageComplete {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			if cause == context.Canceled {
				return nil
			}
		}
	}
	return err
}
