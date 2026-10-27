package ethapi

import (
	"context"
	"github.com/ava-labs/avalanchego/graft/coreth/consensus/dummy"
	"github.com/ava-labs/avalanchego/graft/coreth/core"
	"github.com/ava-labs/avalanchego/graft/coreth/params"
	"github.com/ava-labs/avalanchego/graft/evm/rpc"
	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/core/types"
	"math/big"
	"testing"
	"time"
)

type simV1TestBackend struct{ *testBackend }

func (b simV1TestBackend) EstimateBaseFee(context.Context) (*big.Int, error) {
	return big.NewInt(0), nil
}

func TestSimulateV1SequentialState(t *testing.T) {
	account := common.HexToAddress("0x1234")
	contract := common.HexToAddress("0x5678")
	code := common.FromHex("0x60005460010160005560005460005260206000f3")
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{account: {Balance: big.NewInt(params.Ether)}, contract: {Balance: big.NewInt(0), Code: code}}}
	backend := newTestBackend(t, 1, genesis, dummy.NewCoinbaseFaker(), func(int, *core.BlockGen) {})
	api := NewBlockChainAPI(simV1TestBackend{backend})
	gas := hexutil.Uint64(100000)
	call := TransactionArgs{From: &account, To: &contract, Gas: &gas}
	out, err := api.SimulateV1(context.Background(), simV1Options{BlockStateCalls: []simV1Block{{Calls: []TransactionArgs{call, call}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Calls) != 2 {
		t.Fatalf("unexpected results: %+v", out)
	}
	for i, r := range out[0].Calls {
		if r.Status != 1 || new(big.Int).SetBytes(r.ReturnData).Uint64() != uint64(i+1) {
			t.Fatalf("call %d: %+v", i, r)
		}
	}
	again, err := api.SimulateV1(context.Background(), simV1Options{BlockStateCalls: []simV1Block{{Calls: []TransactionArgs{call}}}}, nil)
	if err != nil || len(again) != 1 || len(again[0].Calls) != 1 || new(big.Int).SetBytes(again[0].Calls[0].ReturnData).Uint64() != 1 {
		t.Fatalf("state leaked: %v %+v", err, again)
	}
}
func TestSimulateV1RejectsUnsupportedOptions(t *testing.T) {
	api := &BlockChainAPI{}
	cases := []simV1Options{
		{},
		{BlockStateCalls: []simV1Block{{Calls: []TransactionArgs{{}}}}, Validation: true},
		{BlockStateCalls: []simV1Block{{Calls: []TransactionArgs{{}}, BlockOverrides: &BlockOverrides{}}}},
	}
	for _, opts := range cases {
		if _, err := api.SimulateV1(context.Background(), opts, nil); err == nil {
			t.Fatal("expected error")
		}
	}
}

func TestSimulateV1RevertDoesNotCommitState(t *testing.T) {
	account := common.HexToAddress("0x1234")
	good := common.HexToAddress("0x5678")
	bad := common.HexToAddress("0x9876")
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{
		account: {Balance: big.NewInt(params.Ether)},
		good:    {Balance: big.NewInt(0), Code: common.FromHex("0x60005460010160005560005460005260206000f3")},
		bad:     {Balance: big.NewInt(0), Code: common.FromHex("0x600160005560006000fd")},
	}}
	backend := newTestBackend(t, 1, genesis, dummy.NewCoinbaseFaker(), func(int, *core.BlockGen) {})
	api := NewBlockChainAPI(simV1TestBackend{backend})
	gas := hexutil.Uint64(100000)
	opts := simV1Options{BlockStateCalls: []simV1Block{{Calls: []TransactionArgs{{From: &account, To: &good, Gas: &gas}, {From: &account, To: &bad, Gas: &gas}, {From: &account, To: &good, Gas: &gas}}}}}
	out, err := api.SimulateV1(context.Background(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := out[0].Calls
	if len(calls) != 3 || calls[0].Status != 1 || calls[1].Status != 0 || calls[1].Error == nil || calls[2].Status != 1 || new(big.Int).SetBytes(calls[2].ReturnData).Uint64() != 2 {
		t.Fatalf("unexpected results: %+v", calls)
	}
}

func TestSimulateV1ReturnsLogs(t *testing.T) {
	account := common.HexToAddress("0x1234")
	contract := common.HexToAddress("0x5678")
	// Emit one LOG1 with a 32-byte data word and topic 1.
	code := common.FromHex("0x6002600052600160206000a100")
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{account: {Balance: big.NewInt(params.Ether)}, contract: {Balance: big.NewInt(0), Code: code}}}
	backend := newTestBackend(t, 1, genesis, dummy.NewCoinbaseFaker(), func(int, *core.BlockGen) {})
	api := NewBlockChainAPI(simV1TestBackend{backend})
	gas := hexutil.Uint64(100000)
	call := TransactionArgs{From: &account, To: &contract, Gas: &gas}
	out, err := api.SimulateV1(context.Background(), simV1Options{BlockStateCalls: []simV1Block{{Calls: []TransactionArgs{call, call}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range out[0].Calls {
		if c.Status != 1 || len(c.Logs) != 1 || c.Logs[0].Address != contract || len(c.Logs[0].Topics) != 1 || c.Logs[0].Topics[0] != common.BigToHash(big.NewInt(1)) || new(big.Int).SetBytes(c.Logs[0].Data).Uint64() != 2 {
			t.Fatalf("call %d: %+v", i, c)
		}
	}
}

func TestSimulateV1RPCRegistration(t *testing.T) {
	api := &BlockChainAPI{}
	server := rpc.NewServer(time.Second)
	if err := server.RegisterName("eth", api); err != nil {
		t.Fatal(err)
	}
	client := rpc.DialInProc(server)
	defer client.Close()
	var result []simV1BlockResult
	err := client.Call(&result, "eth_simulateV1", simV1Options{})
	if err == nil || err.Error() != "simulation requires one block with 1 to 64 calls" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSimulateV1RPCSequentialCalls(t *testing.T) {
	account := common.HexToAddress("0x1234")
	contract := common.HexToAddress("0x5678")
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{
		account:  {Balance: big.NewInt(params.Ether)},
		contract: {Code: common.FromHex("0x60005460010160005560005460005260206000f3")},
	}}
	backend := newTestBackend(t, 1, genesis, dummy.NewCoinbaseFaker(), func(int, *core.BlockGen) {})
	server := rpc.NewServer(time.Second)
	if err := server.RegisterName("eth", NewBlockChainAPI(simV1TestBackend{backend})); err != nil {
		t.Fatal(err)
	}
	client := rpc.DialInProc(server)
	defer client.Close()
	gas := hexutil.Uint64(100000)
	call := TransactionArgs{From: &account, To: &contract, Gas: &gas}
	var result []simV1BlockResult
	if err := client.Call(&result, "eth_simulateV1", simV1Options{BlockStateCalls: []simV1Block{{Calls: []TransactionArgs{call, call}}}}, "latest"); err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || len(result[0].Calls) != 2 {
		t.Fatalf("unexpected RPC response: %+v", result)
	}
	for i, callResult := range result[0].Calls {
		if callResult.Status != 1 || new(big.Int).SetBytes(callResult.ReturnData).Uint64() != uint64(i+1) || callResult.GasUsed == 0 {
			t.Fatalf("RPC call %d: %+v", i, callResult)
		}
	}
}

func TestSimulateV1IncludesFlareDaemonLogs(t *testing.T) {
	account := common.HexToAddress("0x1234")
	contract := common.HexToAddress("0x5678")
	daemon := common.HexToAddress("0x1000000000000000000000000000000000000002")
	// Emit a daemon log and return a zero mint request.
	daemonCode := common.FromHex("0x6002600052600160206000a1600060005260206000f3")
	cfg := *params.TestChainConfig
	cfg.ChainID = big.NewInt(14)
	genesis := &core.Genesis{Config: &cfg, Alloc: types.GenesisAlloc{
		account:  {Balance: big.NewInt(params.Ether)},
		contract: {Balance: big.NewInt(0), Code: common.FromHex("0x00")},
		daemon:   {Balance: big.NewInt(0), Code: daemonCode},
	}}
	backend := newTestBackend(t, 1, genesis, dummy.NewCoinbaseFaker(), func(int, *core.BlockGen) {})
	api := NewBlockChainAPI(simV1TestBackend{backend})
	gas := hexutil.Uint64(100000)
	call := TransactionArgs{From: &account, To: &contract, Gas: &gas}
	out, err := api.SimulateV1(context.Background(), simV1Options{BlockStateCalls: []simV1Block{{Calls: []TransactionArgs{call}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	logs := out[0].Calls[0].Logs
	if len(logs) != 1 || logs[0].Address != daemon {
		t.Fatalf("expected daemon log: %+v", logs)
	}
}
