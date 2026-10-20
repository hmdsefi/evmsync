// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: Apache-2.0

package archtest

// rule says what one package may import.
type rule struct {
	module   []string // module packages it may import, as relative paths ("" is the root)
	external []string // exact non-standard import paths it may import
	testing  bool     // it may import "testing"
	noNet    bool     // it may not import net, net/..., os/exec, math/rand or math/rand/v2
	noTime   bool     // it may not import time
}

const (
	gethCommon = "github.com/ethereum/go-ethereum/common"
	hexutil    = "github.com/ethereum/go-ethereum/common/hexutil"
	abi        = "github.com/ethereum/go-ethereum/accounts/abi"
	keccak     = "golang.org/x/crypto/sha3"
	fakechain  = "internal/fakechain"
)

// rules is the import table of architecture §5.1, keyed by relative package path.
var rules = map[string]rule{
	"chain":              {external: []string{gethCommon, keccak}, noNet: true},
	"chainlog":           {module: []string{"chain"}, external: []string{gethCommon}, noNet: true},
	"chainlog/wal":       {module: []string{"chain", "chainlog"}},
	"chainlog/logtest":   {module: []string{"chain", "chainlog"}, testing: true},
	"tracker":            {module: []string{"chain", "chainlog"}, external: []string{gethCommon}, noNet: true, noTime: true},
	"consumer":           {module: []string{"chain", "chainlog"}, noNet: true},
	"rpc":                {module: []string{"chain"}, external: []string{gethCommon, hexutil}},
	"ingest":             {module: []string{"chain", "chainlog", "tracker"}, external: []string{gethCommon}},
	"":                   {module: []string{"chain", "chainlog", "chainlog/wal", "consumer", "ingest", "rpc", "tracker"}, external: []string{gethCommon}},
	"memstore":           {module: []string{"", "chain", "chainlog"}, noNet: true},
	"internal/gen":       {external: []string{abi, gethCommon}},
	"cmd/evmsync":        {module: []string{"internal/gen"}},
	fakechain:            {module: []string{"chain", "chainlog"}, external: []string{gethCommon, keccak}},
	"internal/invariant": {module: []string{"chain", "chainlog", "consumer", "tracker", fakechain}, external: []string{gethCommon, keccak}, testing: true},
	"internal/archtest":  {},
}
