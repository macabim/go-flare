// Copyright (C) 2019, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package cchain

import (
	"context"
	"math/big"
	"testing"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/libevm/options"
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/vms/saevm/saetest"

	cparams "github.com/ava-labs/avalanchego/graft/coreth/params"
)

// These tests cover the SAE eth_simulateV1 (sae/rpc/simulate_v1.go) over the
// real RPC transport, with the request and response shapes that Trader uses.
// They mirror graft/coreth/internal/ethapi/simulate_v1_test.go.

var (
	simAccount = common.HexToAddress("0x1234")
	// simCounter increments slot 0 and returns the new value.
	simCounter     = common.HexToAddress("0x5678")
	simCounterCode = common.FromHex("0x60005460010160005560005460005260206000f3")
	// simReverter writes slot 0 and reverts.
	simReverter     = common.HexToAddress("0x9876")
	simReverterCode = common.FromHex("0x600160005560006000fd")
	// simLogger emits one LOG1 with a 32-byte data word 2 and topic 1.
	simLogger     = common.HexToAddress("0x4321")
	simLoggerCode = common.FromHex("0x6002600052600160206000a100")
	// simDaemon emits the same log and returns a zero mint request.
	simDaemon     = common.HexToAddress("0x1000000000000000000000000000000000000002")
	simDaemonCode = common.FromHex("0x6002600052600160206000a1600060005260206000f3")
	// simBlockHash returns BLOCKHASH(NUMBER - 1).
	simBlockHash     = common.HexToAddress("0x8765")
	simBlockHashCode = common.FromHex("0x600143034060005260206000f3")
)

func withFlareChainID() sutOption {
	return options.Func[sutConfig](func(c *sutConfig) {
		c.genesis.Config.ChainID = cparams.LocalFlareChainID
	})
}

type simCallResult struct {
	ReturnData hexutil.Bytes  `json:"returnData"`
	GasUsed    hexutil.Uint64 `json:"gasUsed"`
	Status     hexutil.Uint64 `json:"status"`
	Logs       []*types.Log   `json:"logs"`
	Error      *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func simCall(to common.Address) map[string]any {
	return map[string]any{"from": simAccount, "to": to, "gas": hexutil.Uint64(100_000)}
}

func simOpts(calls ...map[string]any) map[string]any {
	return map[string]any{"blockStateCalls": []map[string]any{{"calls": calls}}}
}

func newSimSUT(t *testing.T, opts ...sutOption) (context.Context, *SUT, func(any) ([]simCallResult, error)) {
	t.Helper()
	opts = append([]sutOption{
		withMaxAllocFor(simAccount),
		withAccount(simCounter, types.Account{Code: simCounterCode}),
		withAccount(simReverter, types.Account{Code: simReverterCode}),
		withAccount(simLogger, types.Account{Code: simLoggerCode}),
	}, opts...)
	ctx, sut := newSUT(t, opts...)
	simulate := func(params any) ([]simCallResult, error) {
		var out []struct {
			Calls []simCallResult `json:"calls"`
		}
		if err := sut.ethclient.Client().CallContext(ctx, &out, "eth_simulateV1", params, "latest"); err != nil {
			return nil, err
		}
		require.Len(t, out, 1, "one simulated block")
		return out[0].Calls, nil
	}
	return ctx, sut, simulate
}

func simUint(t *testing.T, r simCallResult) uint64 {
	t.Helper()
	return new(big.Int).SetBytes(r.ReturnData).Uint64()
}

func TestSimulateV1SequentialState(t *testing.T) {
	_, _, simulate := newSimSUT(t)

	calls, err := simulate(simOpts(simCall(simCounter), simCall(simCounter)))
	require.NoError(t, err)
	require.Len(t, calls, 2)
	for i, c := range calls {
		require.EqualValuesf(t, 1, c.Status, "call %d status", i)
		require.Equalf(t, uint64(i+1), simUint(t, c), "call %d sees the state of the previous calls", i)
		require.NotZerof(t, c.GasUsed, "call %d gas", i)
		require.NotNilf(t, c.Logs, "call %d logs must be an array", i)
	}

	again, err := simulate(simOpts(simCall(simCounter)))
	require.NoError(t, err)
	require.Len(t, again, 1)
	require.Equal(t, uint64(1), simUint(t, again[0]), "state must not leak between simulations")
}

func TestSimulateV1RevertDoesNotCommitState(t *testing.T) {
	_, _, simulate := newSimSUT(t)

	calls, err := simulate(simOpts(simCall(simCounter), simCall(simReverter), simCall(simCounter)))
	require.NoError(t, err)
	require.Len(t, calls, 3)
	require.EqualValues(t, 1, calls[0].Status)
	require.EqualValues(t, 0, calls[1].Status)
	require.NotNil(t, calls[1].Error, "reverted call has an error")
	require.Equal(t, 3, calls[1].Error.Code)
	require.EqualValues(t, 1, calls[2].Status)
	require.Equal(t, uint64(2), simUint(t, calls[2]), "reverted call must not change state")
}

func TestSimulateV1ReturnsLogs(t *testing.T) {
	_, _, simulate := newSimSUT(t)

	calls, err := simulate(simOpts(simCall(simLogger), simCall(simLogger)))
	require.NoError(t, err)
	require.Len(t, calls, 2)
	for i, c := range calls {
		require.EqualValuesf(t, 1, c.Status, "call %d status", i)
		require.Lenf(t, c.Logs, 1, "call %d logs", i)
		require.Equal(t, simLogger, c.Logs[0].Address)
		require.Equal(t, []common.Hash{common.BigToHash(big.NewInt(1))}, c.Logs[0].Topics)
		require.Equal(t, uint64(2), new(big.Int).SetBytes(c.Logs[0].Data).Uint64())
	}
}

func TestSimulateV1RejectsUnsupportedOptions(t *testing.T) {
	_, _, simulate := newSimSUT(t)

	tooMany := make([]map[string]any, 65)
	for i := range tooMany {
		tooMany[i] = simCall(simCounter)
	}
	tests := []struct {
		name    string
		params  any
		wantErr string
	}{
		{"no_blocks", map[string]any{}, "simulation requires one block with 1 to 64 calls"},
		{"no_calls", simOpts(), "simulation requires one block with 1 to 64 calls"},
		{"too_many_calls", simOpts(tooMany...), "simulation requires one block with 1 to 64 calls"},
		{"two_blocks", map[string]any{"blockStateCalls": []map[string]any{
			{"calls": []any{simCall(simCounter)}},
			{"calls": []any{simCall(simCounter)}},
		}}, "simulation requires one block with 1 to 64 calls"},
		{"validation", map[string]any{"validation": true, "blockStateCalls": []map[string]any{{"calls": []any{simCall(simCounter)}}}}, "unsupported simulation option"},
		{"trace_transfers", map[string]any{"traceTransfers": true, "blockStateCalls": []map[string]any{{"calls": []any{simCall(simCounter)}}}}, "unsupported simulation option"},
		{"full_transactions", map[string]any{"returnFullTransactions": true, "blockStateCalls": []map[string]any{{"calls": []any{simCall(simCounter)}}}}, "unsupported simulation option"},
		{"block_overrides", map[string]any{"blockStateCalls": []map[string]any{{"calls": []any{simCall(simCounter)}, "blockOverrides": map[string]any{}}}}, "unsupported simulation option"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := simulate(tt.params)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSimulateV1StateOverrides(t *testing.T) {
	_, _, simulate := newSimSUT(t)

	params := map[string]any{"blockStateCalls": []map[string]any{{
		"calls": []any{simCall(simCounter)},
		"stateOverrides": map[string]any{
			simCounter.Hex(): map[string]any{"stateDiff": map[string]any{
				common.Hash{}.Hex(): common.BigToHash(big.NewInt(41)).Hex(),
			}},
		},
	}}}
	calls, err := simulate(params)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, uint64(42), simUint(t, calls[0]), "call runs on the overridden state")
}

// TestSimulateV1IncludesFlareDaemonLogs asserts that each simulated call runs
// the Flare daemon after it, as live execution does, so its logs appear in the
// result.
func TestSimulateV1IncludesFlareDaemonLogs(t *testing.T) {
	_, _, simulate := newSimSUT(t, withFlareChainID(), withAccount(simDaemon, types.Account{Code: simDaemonCode}))

	calls, err := simulate(simOpts(simCall(simCounter), simCall(simCounter)))
	require.NoError(t, err)
	require.Len(t, calls, 2)
	for i, c := range calls {
		require.EqualValuesf(t, 1, c.Status, "call %d status", i)
		require.Equalf(t, uint64(i+1), simUint(t, c), "call %d sequential state", i)
		require.Lenf(t, c.Logs, 1, "call %d daemon log", i)
		require.Equalf(t, simDaemon, c.Logs[0].Address, "call %d daemon log address", i)
	}
}

// TestSimulateV1NoDaemonOnOtherChains asserts that the daemon runs only on
// Flare-family chain IDs.
func TestSimulateV1NoDaemonOnOtherChains(t *testing.T) {
	_, _, simulate := newSimSUT(t, withAccount(simDaemon, types.Account{Code: simDaemonCode}))

	calls, err := simulate(simOpts(simCall(simCounter)))
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Empty(t, calls[0].Logs, "no daemon call on a non-Flare chain")
}

// TestSimulateV1OnExecutedBlock asserts that the simulation starts from the
// state of the last executed block, settles the Flare fee of a priced call, and
// sees the canonical hash of that block through BLOCKHASH.
func TestSimulateV1OnExecutedBlock(t *testing.T) {
	w := saetest.NewUNSAFEWallet(t, 1, types.LatestSignerForChainID(cparams.LocalFlareChainID))
	ctx, sut, simulate := newSimSUT(t,
		withFlareChainID(),
		withMaxAllocFor(w.Addresses()[0]),
		withAccount(simBlockHash, types.Account{Code: simBlockHashCode}),
	)

	tx := w.SetNonceAndSign(t, 0, &types.LegacyTx{To: &simCounter, Gas: 100_000, GasPrice: big.NewInt(1)})
	require.NoError(t, sut.ethclient.SendTransaction(ctx, tx))
	sut.waitForPendingEthTxs(ctx, t, tx)
	blk := sut.runConsensusLoop(ctx, t)
	require.Len(t, blk.Receipts(), 1)
	require.Equal(t, types.ReceiptStatusSuccessful, blk.Receipts()[0].Status)

	priced := simCall(simCounter)
	priced["maxFeePerGas"] = (*hexutil.Big)(big.NewInt(1e12))
	priced["maxPriorityFeePerGas"] = (*hexutil.Big)(big.NewInt(1e12))
	calls, err := simulate(simOpts(priced, simCall(simBlockHash)))
	require.NoError(t, err)
	require.Len(t, calls, 2)
	require.EqualValues(t, 1, calls[0].Status)
	require.Equal(t, uint64(2), simUint(t, calls[0]), "simulation starts from the executed block")
	require.EqualValues(t, 1, calls[1].Status)
	require.Equal(t, blk.Hash(), common.BytesToHash(calls[1].ReturnData), "BLOCKHASH(latest)")
}
