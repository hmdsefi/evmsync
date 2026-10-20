// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

// Package consumer applies a canonical log to state: the Consumer contract, Run, which feeds a
// consumer the log from its committed offset, and Memory, a consumer for in-memory state machines
// that cannot undo their writes.
package consumer
