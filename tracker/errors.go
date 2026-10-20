// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package tracker

import "errors"

var (
	// ErrReorgBelowFinalized reports a branch that does not descend from the finalized
	// block: the provider claims a different finalized history.
	ErrReorgBelowFinalized = errors.New("tracker: reorg below the finalized block")
	// ErrWindowFull reports that the call would grow the window past its cap.
	ErrWindowFull = errors.New("tracker: unfinalized window full")
	// ErrUnanchored reports a Head call before the first Finalize.
	ErrUnanchored = errors.New("tracker: no finalized block")
	// ErrInconsistentSource reports a Source answer that contradicts the request or
	// an earlier answer.
	ErrInconsistentSource = errors.New("tracker: inconsistent source")
)
