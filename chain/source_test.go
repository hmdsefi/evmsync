// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"errors"
	"testing"
)

func TestErrors(t *testing.T) {
	sentinels := []struct {
		err  error
		want string
	}{
		{ErrNotFound, "chain: not found"},
		{ErrUnsupported, "chain: unsupported"},
		{ErrInvalidRange, "chain: invalid block range"},
		{ErrInvalidFilter, "chain: invalid filter"},
	}
	for i, s := range sentinels {
		if s.err.Error() != s.want {
			t.Errorf("sentinel %d: Error() = %q, want %q", i, s.err.Error(), s.want)
		}
		for j, o := range sentinels {
			if i != j && errors.Is(s.err, o.err) {
				t.Errorf("errors.Is(%q, %q) = true, want false", s.want, o.want)
			}
		}
	}
}
