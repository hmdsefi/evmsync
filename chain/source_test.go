// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package chain

import (
	"errors"
	"testing"
)

func TestErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"ErrNotFound", ErrNotFound, "chain: not found"},
		{"ErrUnsupported", ErrUnsupported, "chain: unsupported"},
		{"ErrInvalidRange", ErrInvalidRange, "chain: invalid block range"},
		{"ErrInvalidFilter", ErrInvalidFilter, "chain: invalid filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Error() != tt.want {
				t.Errorf("Error() = %q, want %q", tt.err.Error(), tt.want)
			}
			for _, o := range tests {
				if o.name != tt.name && errors.Is(tt.err, o.err) {
					t.Errorf("errors.Is(%s, %s) = true, want false", tt.name, o.name)
				}
			}
		})
	}
}
