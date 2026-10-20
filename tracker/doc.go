// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

// Package tracker turns new heads and finalized blocks of a chain into canonical-log entries. It
// detects reorgs by following parent hashes and keeps the window of unfinalized blocks.
package tracker
