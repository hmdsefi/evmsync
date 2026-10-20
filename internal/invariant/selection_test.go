// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package invariant

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// childEnv marks a re-executed test binary, so the child-only tests run and the parent tests do
// not recurse.
const childEnv = "EVMSYNC_INVARIANT_CHILD"

func isChild() bool { return os.Getenv(childEnv) == "1" }

// mutantConfig is TestReorgInvariant's configuration with a subject that ignores reverts.
func mutantConfig() Config {
	cfg := memoryConfig()
	cfg.New = newIgnoreReverts
	cfg.Reopen = func(tb testing.TB, _ Subject) Subject { return newIgnoreReverts(tb) }
	return cfg
}

// TestMutantInvariant runs only in a child process: its subject is broken on purpose.
func TestMutantInvariant(t *testing.T) {
	if !isChild() {
		t.Skip("runs only when re-executed by TestRunSelection")
	}
	Run(t, mutantConfig())
}

// TestTameInvariant runs only in a child process: its chains almost never reorg.
func TestTameInvariant(t *testing.T) {
	if !isChild() {
		t.Skip("runs only when re-executed by TestTameness")
	}
	cfg := memoryConfig()
	cfg.Variant = func(uint64) Variant {
		return Variant{Params: fakechainParams(1, 1)}
	}
	Run(t, cfg)
}

// child runs this test binary with -test.run run, -test.v and the extra arguments, with env
// added to an environment that has no EVMSYNC_INVARIANT_ variable. It returns the combined
// output and whether the run passed.
func child(t *testing.T, run string, env []string, args ...string) (string, bool) {
	t.Helper()
	if isChild() {
		t.Skip("a child process does not re-execute itself")
	}
	argv := append([]string{"-test.run", run, "-test.v", "-test.count=1"}, args...)
	cmd := exec.CommandContext(t.Context(), os.Args[0], argv...) //nolint:gosec // this test binary
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "EVMSYNC_INVARIANT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Env = append(cmd.Env, childEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok { //nolint:errorlint // only the exit status is expected
			t.Fatalf("running %s: %v", run, err)
		}
	}
	return string(out), err == nil
}

var seedRun = regexp.MustCompile(`(?m)^=== RUN\s+\S+/seed=(\d+)$`)

// seedsRun returns the seeds of the subtests the output shows starting, in order.
func seedsRun(t *testing.T, out string) []uint64 {
	t.Helper()
	var seeds []uint64
	for _, m := range seedRun.FindAllStringSubmatch(out, -1) {
		n, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	}
	return seeds
}

func span(from, n uint64) []uint64 {
	var s []uint64
	for i := range n {
		s = append(s, from+i)
	}
	return s
}

func TestRunSelection(t *testing.T) {
	const run = "^TestReorgInvariant$"
	tests := []struct {
		name  string
		env   []string
		args  []string
		seeds []uint64
		trace bool
	}{
		{"one seed", []string{"EVMSYNC_INVARIANT_SEED=7"}, nil, []uint64{7}, true},
		{"hex seed", []string{"EVMSYNC_INVARIANT_SEED=0x10"}, nil, []uint64{16}, true},
		{"seed wins over count", []string{"EVMSYNC_INVARIANT_SEED=7", "EVMSYNC_INVARIANT_SEEDS=3"}, nil,
			[]uint64{7}, true},
		{"count from base", []string{"EVMSYNC_INVARIANT_SEEDS=3", "EVMSYNC_INVARIANT_BASE=10"}, nil,
			[]uint64{10, 11, 12}, false},
		{"count from 1", []string{"EVMSYNC_INVARIANT_SEEDS=2"}, nil, []uint64{1, 2}, false},
		{"short", nil, []string{"-test.short"}, span(1, 100), false},
		{"short from base", []string{"EVMSYNC_INVARIANT_BASE=0x20"}, []string{"-test.short"}, span(32, 100), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, ok := child(t, run, tt.env, tt.args...)
			got := seedsRun(t, out)
			slices.Sort(got)
			if !ok || !slices.Equal(got, tt.seeds) {
				t.Fatalf("passed %v, seeds %v, want %v; output:\n%s", ok, got, tt.seeds, tail(out))
			}
			if traced := strings.Contains(out, "step 0: "); traced != tt.trace {
				t.Errorf("trace lines present %v, want %v", traced, tt.trace)
			}
		})
	}

	bad := []struct{ env, want string }{
		{"EVMSYNC_INVARIANT_SEED=x", "invariant: bad EVMSYNC_INVARIANT_SEED: x"},
		{"EVMSYNC_INVARIANT_SEEDS=-1", "invariant: bad EVMSYNC_INVARIANT_SEEDS: -1"},
		{"EVMSYNC_INVARIANT_BASE=1e3", "invariant: bad EVMSYNC_INVARIANT_BASE: 1e3"},
	}
	for _, tt := range bad {
		t.Run(tt.env, func(t *testing.T) {
			out, ok := child(t, run, []string{tt.env})
			if ok || !strings.Contains(out, tt.want) || len(seedsRun(t, out)) != 0 {
				t.Errorf("passed %v, want a failure with %q and no seed run; output:\n%s", ok, tt.want, tail(out))
			}
		})
	}

	t.Run("reproduce line", func(t *testing.T) {
		var seed uint64
		for s := uint64(1); s <= 50 && seed == 0; s++ {
			if _, err := Check(t, mutantConfig(), s); err != nil {
				seed = s
			}
		}
		if seed == 0 {
			t.Fatal("the mutant subject passes seeds 1..50")
		}
		out, ok := child(t, "^TestMutantInvariant$", []string{fmt.Sprintf("EVMSYNC_INVARIANT_SEED=%d", seed)})
		want := fmt.Sprintf("reproduce: EVMSYNC_INVARIANT_SEED=%d go test ./internal/invariant "+
			"-run '^TestMutantInvariant$' -count=1 -v", seed)
		lines := strings.Split(out, "\n")
		at := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == want })
		if ok || at < 1 || !strings.Contains(lines[at-1], fmt.Sprintf("invariant: seed %d: ", seed)) {
			t.Errorf("passed %v, want a failure line then %q; output:\n%s", ok, want, tail(out))
		}
	})
}

func TestTameness(t *testing.T) {
	out, ok := child(t, "^TestTameInvariant$", []string{"EVMSYNC_INVARIANT_SEEDS=100"})
	if ok || !strings.Contains(out, "invariant: scenarios too tame: ") || len(seedsRun(t, out)) != 100 {
		t.Errorf("100 tame seeds: passed %v, want a failure with the tameness message; output:\n%s", ok, tail(out))
	}
	out, ok = child(t, "^TestTameInvariant$", []string{"EVMSYNC_INVARIANT_SEEDS=99"})
	if !ok || strings.Contains(out, "too tame") {
		t.Errorf("99 tame seeds: passed %v, want a pass without the guard; output:\n%s", ok, tail(out))
	}
}

// tail returns the last 40 lines of out.
func tail(out string) string {
	lines := strings.Split(out, "\n")
	return strings.Join(lines[max(0, len(lines)-40):], "\n")
}

func TestSelectSeeds(t *testing.T) {
	cfg := memoryConfig().withDefaults()
	cfg.Seeds, cfg.ShortSeeds = 7, 3
	tests := []struct {
		name  string
		env   map[string]string
		want  []uint64
		one   bool
		error string
	}{
		{"default", nil, span(1, map[bool]uint64{false: 7, true: 3}[testing.Short()]), false, ""},
		{"base", map[string]string{envBase: "40"}, span(40, map[bool]uint64{false: 7, true: 3}[testing.Short()]), false, ""},
		{"count", map[string]string{envSeeds: "0x2", envBase: "9"}, []uint64{9, 10}, false, ""},
		{"zero count", map[string]string{envSeeds: "0"}, []uint64{}, false, ""},
		{"one", map[string]string{envSeed: "5", envSeeds: "9", envBase: "x"}, []uint64{5}, true, ""},
		{"max seed", map[string]string{envSeed: "18446744073709551615"}, []uint64{1<<64 - 1}, true, ""},
		{"bad seed", map[string]string{envSeed: "-3"}, nil, false, "invariant: bad EVMSYNC_INVARIANT_SEED: -3"},
		{"bad count", map[string]string{envSeeds: "two"}, nil, false, "invariant: bad EVMSYNC_INVARIANT_SEEDS: two"},
		{"bad base", map[string]string{envBase: "0x"}, nil, false, "invariant: bad EVMSYNC_INVARIANT_BASE: 0x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range []string{envSeed, envSeeds, envBase} {
				t.Setenv(name, tt.env[name])
			}
			got, one, err := selectSeeds(cfg)
			if tt.error != "" {
				if err == nil || err.Error() != tt.error {
					t.Errorf("error %v, want %q", err, tt.error)
				}
				return
			}
			if err != nil || one != tt.one || !slices.Equal(got, tt.want) {
				t.Errorf("selectSeeds = %v, %v, %v; want %v, %v", got, one, err, tt.want, tt.one)
			}
		})
	}
}

func TestTamenessGuard(t *testing.T) {
	reports := func(n, reverted int, depth uint64) []Report {
		rs := make([]Report, n)
		for i := range reverted {
			rs[i].Reverts = 1
			rs[i].MaxRevertDepth = 1
		}
		if reverted > 0 {
			rs[0].MaxRevertDepth = depth
		}
		return rs
	}
	tests := []struct {
		name string
		rs   []Report
		want string
	}{
		{"fewer than 100 seeds", reports(99, 0, 0), ""},
		{"lively", reports(100, 50, 3), ""},
		{"too few reverts", reports(100, 49, 8), "49 of 100 seeds reverted, want at least half"},
		{"too shallow", reports(101, 101, 2), "no seed of 101 reverted 3 or more blocks"},
	}
	for _, tt := range tests {
		if got := tameness(tt.rs); got != tt.want {
			t.Errorf("%s: tameness = %q, want %q", tt.name, got, tt.want)
		}
	}
}
