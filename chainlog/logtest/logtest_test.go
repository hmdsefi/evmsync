// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package logtest_test

import (
	"testing"

	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/chainlog/logtest"
)

// TestRun runs the suite on the in-memory log, so the suite's own code is measured by coverage.
func TestRun(t *testing.T) {
	logtest.Run(t, func(t *testing.T) chainlog.Log {
		return chainlog.NewMemory(logtest.Meta(), logtest.WindowCap)
	})
}
