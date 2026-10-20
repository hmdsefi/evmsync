// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chainlog_test

import (
	"testing"

	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/chainlog/logtest"
)

func TestMemoryConformance(t *testing.T) {
	logtest.Run(t, func(t *testing.T) chainlog.Log {
		return chainlog.NewMemory(logtest.Meta(), logtest.WindowCap)
	})
}
