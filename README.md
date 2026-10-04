# evmsync

**Reorg-safe EVM indexing framework for Go.**
Typed event handlers in, consistent onchain state out.

evmsync streams blocks and logs from EVM chains, decodes them into typed Go events,
runs your handlers, and keeps your database consistent — rolling back automatically
on reorgs, backfilling in parallel, then switching seamlessly to realtime.

> Status: **design phase**.

## What "evmsync" means here

The name is literal on purpose: it's two words, and each one says something.

**1. EVM.** The *Ethereum Virtual Machine* is the execution environment shared by
Ethereum and a large family of chains: Base, Arbitrum, Optimism, Polygon, BNB
Chain, Avalanche C-Chain, and more. They all expose the same JSON-RPC API, the
same log format, and the same ABI encoding. That's why one framework and one set
of typed handlers can index any of them, often several at once.

**2. sync.** evmsync isn't a one-time import. It keeps your database continuously
in sync with the chain, which is the source of truth, the way a database replica
follows its primary:

| Database replica | evmsync |
|---|---|
| Initial snapshot copy | **Backfill**: replay historical blocks in parallel |
| Follows the primary's log | **Realtime**: follow the chain head, block by block |
| Applies changes in order | Runs your handlers in block and log order |
| Handles the primary rewriting history | **Reorgs**: rolls your state back to the common ancestor and reapplies the new canonical blocks |
| Resumes after a crash without gaps or duplicates | Checkpoints and resumes exactly where it stopped |

The important part is that sync goes **both ways in time**: forward when new
blocks arrive, and backward when the chain reorganizes. Most hand-rolled indexers
only handle forward.

**What it doesn't mean:**

- **It isn't node sync.** In go-ethereum, "sync" (snap sync, full sync) means
  downloading the entire blockchain into a node. evmsync doesn't run a node or store
  the chain. It reads from RPC providers and keeps only the events you subscribe
  to, transformed by your handlers.
- **It isn't cross-chain sync.** It doesn't bridge assets or relay state between
  chains. Indexing several chains means several independent sources feeding
  your database.
- **It's read-only.** It never sends transactions.

## Built with

[gograph](https://github.com/hmdsefi/gograph) orders handlers by their table
dependencies, rejects cycles at startup, finds downstream tables to rebuild after
a rollback, and tracks contracts deployed by factories.
