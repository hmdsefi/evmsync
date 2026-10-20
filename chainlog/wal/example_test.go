// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package wal_test

import (
	"context"
	"fmt"
	"os"

	"github.com/hmdsefi/evmsync/chain"
	"github.com/hmdsefi/evmsync/chainlog"
	"github.com/hmdsefi/evmsync/chainlog/wal"
)

// ExampleOpen creates a log, appends a block and reads it back.
func ExampleOpen() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "wal-example")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	log, err := wal.Open(dir, wal.Options{Meta: chainlog.Meta{ChainID: 1}})
	if err != nil {
		panic(err)
	}
	defer log.Close()

	ref := chain.BlockRef{Number: 100, Hash: [32]byte{1}, Time: 1_700_000_000}
	if _, err := log.Append(ctx, chainlog.Entry{Kind: chainlog.KindBlock, Block: chain.Block{Ref: ref}}); err != nil {
		panic(err)
	}
	entries, err := log.Read(ctx, 1, 10)
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		fmt.Println(e.Offset, e.Kind, e.Block.Ref.Number)
	}
	// Output: 1 block 100
}
