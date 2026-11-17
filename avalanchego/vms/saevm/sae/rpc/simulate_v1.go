// Copyright (C) 2019, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package rpc

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/core"
	"github.com/ava-labs/libevm/core/rawdb"
	"github.com/ava-labs/libevm/core/state"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/core/vm"
	"github.com/ava-labs/libevm/crypto"
	"github.com/ava-labs/libevm/libevm/ethapi"
	"github.com/ava-labs/libevm/rpc"

	"github.com/ava-labs/avalanchego/vms/saevm/saexec"
)

// simV1Options implements the one-block simulation used by Trader. It is the
// SAE version of graft/coreth/internal/ethapi/simulate_v1.go and keeps the same
// request, response and limits. Unsupported extensions are rejected instead of
// silently ignored.
type simV1Options struct {
	BlockStateCalls        []simV1Block `json:"blockStateCalls"`
	Validation             bool         `json:"validation"`
	TraceTransfers         bool         `json:"traceTransfers"`
	ReturnFullTransactions bool         `json:"returnFullTransactions"`
}
type simV1Block struct {
	Calls          []ethapi.TransactionArgs `json:"calls"`
	StateOverrides *ethapi.StateOverride    `json:"stateOverrides"`
	BlockOverrides *ethapi.BlockOverrides   `json:"blockOverrides"`
}
type simV1CallResult struct {
	ReturnData hexutil.Bytes  `json:"returnData"`
	GasUsed    hexutil.Uint64 `json:"gasUsed"`
	Status     hexutil.Uint64 `json:"status"`
	Logs       []*types.Log   `json:"logs"`
	Error      *simV1Error    `json:"error,omitempty"`
}
type simV1Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type simV1BlockResult struct {
	Calls []simV1CallResult `json:"calls"`
}

var simV1Slots = make(chan struct{}, 4)

// SimulateV1 executes calls in order on isolated state. Each call goes through
// [saexec.ApplyMessageWithExtras], so the Flare fee settlement and daemon logs
// match live execution.
func (b *blockChainAPI) SimulateV1(ctx context.Context, opts simV1Options, tag *rpc.BlockNumberOrHash) ([]simV1BlockResult, error) {
	if len(opts.BlockStateCalls) != 1 || len(opts.BlockStateCalls[0].Calls) < 1 || len(opts.BlockStateCalls[0].Calls) > 64 {
		return nil, fmt.Errorf("simulation requires one block with 1 to 64 calls")
	}
	if opts.Validation || opts.TraceTransfers || opts.ReturnFullTransactions || opts.BlockStateCalls[0].BlockOverrides != nil {
		return nil, fmt.Errorf("unsupported simulation option")
	}
	select {
	case simV1Slots <- struct{}{}:
		defer func() { <-simV1Slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if tag == nil {
		latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
		tag = &latest
	}
	sdb, header, err := b.b.StateAndHeaderByNumberOrHash(ctx, *tag)
	if err != nil || sdb == nil || header == nil {
		return nil, fmt.Errorf("simulation state unavailable: %v", err)
	}
	// The header carries the executed root and base fee, so its own hash is
	// not the block hash. BLOCKHASH needs the canonical one.
	header.ParentHash = rawdb.ReadCanonicalHash(b.b.DB(), header.Number.Uint64())
	header.Number = new(big.Int).Add(header.Number, big.NewInt(1))
	header.Time = max(header.Time+1, uint64(time.Now().Unix()))

	block := opts.BlockStateCalls[0]
	if err := block.StateOverrides.Apply(sdb); err != nil {
		return nil, err
	}
	blockCtx := core.NewEVMBlockContext(header, b.b.ChainContext(), &header.Coinbase)
	results := make([]simV1CallResult, 0, len(block.Calls))
	for i, args := range block.Calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := crypto.Keccak256Hash([]byte(fmt.Sprintf("simulation-%d", i)))
		sdb.SetTxContext(hash, i)
		result, err := b.b.simulateCall(ctx, args, sdb, blockCtx)
		if err != nil {
			return nil, fmt.Errorf("simulation call %d: %w", i, err)
		}
		logs := sdb.GetLogs(hash, header.Number.Uint64(), common.Hash{})
		if logs == nil {
			logs = []*types.Log{}
		}
		item := simV1CallResult{ReturnData: result.ReturnData, GasUsed: hexutil.Uint64(result.UsedGas), Status: 1, Logs: logs}
		if result.Failed() {
			item.Status = 0
			item.Error = &simV1Error{Code: 3, Message: result.Err.Error()}
		}
		results = append(results, item)
		sdb.Finalise(true)
	}
	return []simV1BlockResult{{Calls: results}}, nil
}

// simulateCall mirrors libevm's unexported ethapi.doCall on a caller-owned
// state, but applies the message with the Flare extras.
func (b *backend) simulateCall(ctx context.Context, args ethapi.TransactionArgs, sdb *state.StateDB, blockCtx vm.BlockContext) (*core.ExecutionResult, error) {
	timeout := b.RPCEVMTimeout()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	msg, err := args.ToMessage(b.RPCGasCap(), blockCtx.BaseFee)
	if err != nil {
		return nil, err
	}
	evm := vm.NewEVM(blockCtx, core.NewEVMTxContext(msg), sdb, b.ChainConfig(), vm.Config{NoBaseFee: true})
	go func() {
		<-ctx.Done()
		evm.Cancel()
	}()

	result, err := saexec.ApplyMessageWithExtras(evm, sdb, msg, new(core.GasPool).AddGas(math.MaxUint64))
	if err := sdb.Error(); err != nil {
		return nil, err
	}
	if evm.Cancelled() {
		return nil, fmt.Errorf("execution aborted (timeout = %v)", timeout)
	}
	if err != nil {
		return result, fmt.Errorf("err: %w (supplied gas %d)", err, msg.GasLimit)
	}
	return result, nil
}
