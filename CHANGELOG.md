# Changelog

This file lists the changes in each evmsync release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before v1.0.0, a minor version may
change the API.

## [Unreleased]

## [0.1.0] - 2026-10-20

The first release: the canonical log, the chain tracker and the consumer runtime. evmsync needs
Go 1.26 or later.

### Added

- `chain`: the block, header and log values, `BlockRef` with its parent hash, the ingest `Filter`
  (up to 1,000 addresses and 1,000 values in each of four topic positions) with a bloom check,
  and the `Source` interface that ingest reads a chain through.
- `chainlog`: the `Block`, `Revert` and `Finalize` entries and their binary record format, limited
  to 16 MiB per record. Every append goes through a validator `State`: a block extends the tip, a
  revert targets the finalized block or a block in the window, and a finalize never moves below
  the finalized block.
- `chainlog.Memory`: an in-memory log that passes the same conformance suite as the WAL.
- `chainlog/wal`: a durable log in segment files with CRC-32C checksums, on Unix systems. It takes
  an exclusive lock on its directory, recovers a torn tail on open, keeps a checkpoint and a meta
  file, fsyncs per `SyncPolicy`, and compacts whole segments below the finalized block.
- `tracker`: turns new heads and finalized blocks into log entries. It follows parent hashes to the
  common ancestor of a reorg, caps the window of unfinalized blocks (100,000 by default), and stops
  with a typed error on a reorg below the finalized block or a full window.
- `consumer`: `Run` feeds a consumer the log from its committed offset plus one, and `Memory` keeps
  in-memory state with a snapshot at the latest `Finalize` that it replays on a `Revert`.

[Unreleased]: https://github.com/hmdsefi/evmsync/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/hmdsefi/evmsync/releases/tag/v0.1.0
