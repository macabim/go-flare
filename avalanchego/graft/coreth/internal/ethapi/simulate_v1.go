// Copyright (C) 2019, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package ethapi

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ava-labs/avalanchego/graft/evm/rpc"
	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/crypto"
)

// simV1Options implements the one-block simulation used by Trader.
// Unsupported extensions are rejected instead of silently ignored.
type simV1Options struct {
	BlockStateCalls        []simV1Block `json:"blockStateCalls"`
	Validation             bool         `json:"validation"`
	TraceTransfers         bool         `json:"traceTransfers"`
	ReturnFullTransactions bool         `json:"returnFullTransactions"`
}
type simV1Block struct {
	Calls          []TransactionArgs `json:"calls"`
	StateOverrides *StateOverride    `json:"stateOverrides"`
	BlockOverrides *BlockOverrides   `json:"blockOverrides"`
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

// SimulateV1 executes calls in order on isolated state.
func (s *BlockChainAPI) SimulateV1(ctx context.Context, opts simV1Options, tag *rpc.BlockNumberOrHash) ([]simV1BlockResult, error) {
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
	state, header, err := s.b.StateAndHeaderByNumberOrHash(ctx, *tag)
	if err != nil || state == nil || header == nil {
		return nil, fmt.Errorf("simulation state unavailable: %v", err)
	}
	state = state.Copy()
	header = types.CopyHeader(header)
	parentHash := header.Hash()
	header.ParentHash = parentHash
	header.Number = new(big.Int).Add(header.Number, big.NewInt(1))
	header.Time = max(header.Time+1, uint64(time.Now().Unix()))
	header.BaseFee, err = s.b.EstimateBaseFee(ctx)
	if err != nil {
		return nil, err
	}
	block := opts.BlockStateCalls[0]
	if err := block.StateOverrides.Apply(state); err != nil {
		return nil, err
	}
	results := make([]simV1CallResult, 0, len(block.Calls))
	for i, args := range block.Calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := crypto.Keccak256Hash([]byte(fmt.Sprintf("simulation-%d", i)))
		state.SetTxContext(hash, i)
		result, err := doCall(ctx, s.b, args, state, header, nil, nil, s.b.RPCEVMTimeout(), s.b.RPCGasCap())
		if err != nil {
			return nil, fmt.Errorf("simulation call %d: %w", i, err)
		}
		logs := state.GetLogs(hash, header.Number.Uint64(), common.Hash{})
		if logs == nil {
			logs = []*types.Log{}
		}
		item := simV1CallResult{ReturnData: result.ReturnData, GasUsed: hexutil.Uint64(result.UsedGas), Status: 1, Logs: logs}
		if result.Failed() {
			item.Status = 0
			item.Error = &simV1Error{Code: 3, Message: result.Err.Error()}
		}
		results = append(results, item)
		state.Finalise(true)
	}
	return []simV1BlockResult{{Calls: results}}, nil
}
