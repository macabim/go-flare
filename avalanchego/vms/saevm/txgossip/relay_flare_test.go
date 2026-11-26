// Copyright (C) 2019, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package txgossip

import (
	"math/big"
	"testing"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/params"
	"github.com/stretchr/testify/require"
)

// Flare: a gossiped transaction reaches the push gossipers only with relay.
func TestAddRelaysGossipOnlyWithRelay(t *testing.T) {
	for _, relay := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_relay", true: "relay"}[relay], func(t *testing.T) {
			s := newSUT(t, 1)
			var pushed []Transaction
			s.pushTo = append(s.pushTo, func(txs ...Transaction) { pushed = append(pushed, txs...) })
			if relay {
				s.RelayGossip()
			}

			tx := Transaction{s.wallet.SetNonceAndSign(t, 0, &types.DynamicFeeTx{
				To:        &common.Address{},
				Gas:       params.TxGas,
				GasFeeCap: big.NewInt(100),
				GasTipCap: big.NewInt(1),
			})}
			require.NoError(t, s.Add(tx), "Add()")

			if !relay {
				require.Empty(t, pushed, "pushed without relay")
				return
			}
			require.Len(t, pushed, 1, "pushed with relay")
			require.Equal(t, tx.Hash(), pushed[0].Hash(), "pushed transaction")
		})
	}
}

// Flare: a transaction that the pool rejects is not relayed.
func TestAddDoesNotRelayRejectedGossip(t *testing.T) {
	s := newSUT(t, 1)
	var pushed []Transaction
	s.pushTo = append(s.pushTo, func(txs ...Transaction) { pushed = append(pushed, txs...) })
	s.RelayGossip()

	const zeroCalldataBytes = 1 << 20
	tx := Transaction{s.wallet.SetNonceAndSign(t, 0, &types.DynamicFeeTx{
		To:        &common.Address{},
		Gas:       params.TxGas,
		GasFeeCap: big.NewInt(100),
		GasTipCap: big.NewInt(1),
		Data:      make([]byte, zeroCalldataBytes),
	})}
	require.ErrorIs(t, s.Add(tx), errInsufficientGasPerByte, "Add()")
	require.Empty(t, pushed, "pushed a rejected transaction")
}
