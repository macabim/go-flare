// Copyright (C) 2019, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package txgossip

import (
	"context"
	"errors"

	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/libevm/ethapi"

	"github.com/ava-labs/avalanchego/network/p2p/gossip"
)

// SendTx implements the respective method of [ethapi.Backend], accepting
// transactions submitted via the `eth_sendTransaction` RPC method. Unlike
// [gossip.BloomSet.Add], transactions added via this method will not be added
// to the Bloom filter, but will be push-gossiped; see
// [Set.RegisterPushGossiper].
func (s *Set) SendTx(ctx context.Context, ethTx *types.Transaction) error {
	var _ ethapi.Backend // protect the import for [comment] rendering

	// Note: the Bloom filter is only necessary for pull gossip, which is only
	// done between validator nodes. Validators are encouraged to NOT expose RPC
	// APIs (i.e. this method) and SHOULD therefore only add transactions via
	// [gossip.BloomSet.Add]. Hence there is a MECE separation between
	// pull+Bloom vs API+push under recommended usage patterns.

	if err := errors.Join(s.set.addToPool(true /*local*/, ethTx)...); err != nil {
		return err
	}
	for _, add := range s.pushTo {
		add(Transaction{ethTx})
	}
	return nil
}

// Add implements [gossip.Set] like [gossip.BloomSet.Add]. Flare: after
// [Set.RelayGossip], a transaction that the pool accepts is also passed to the
// registered push gossipers, so it leaves this node on the next push tick.
// The push gossiper skips a transaction that it already tracks.
func (s *Set) Add(tx Transaction) error {
	if err := s.BloomSet.Add(tx); err != nil {
		return err
	}
	if s.relay {
		for _, add := range s.pushTo {
			add(tx)
		}
	}
	return nil
}

// RelayGossip makes [Set.Add] push-gossip the transactions it accepts. Flare:
// without it, a transaction received from gossip waits for the push of the
// node that first received it, while a transaction submitted here leaves on
// the next push tick. A backrun submitted here can then reach a validator
// before its target. With it, both leave this node in arrival order.
//
// NOTE: like [Set.RegisterPushGossiper], call it before the gossip handler is
// registered.
func (s *Set) RelayGossip() {
	s.relay = true
}

// RegisterPushGossiper registers [gossip.PushGossiper.Add] as a callback for
// every transaction received via [Set.SendTx].
//
// NOTE: it is not safe to call this method concurrently with [Set.SendTx];
// registration is expected to occur immediately after construction of the
// [Set].
func (s *Set) RegisterPushGossiper(push *gossip.PushGossiper[Transaction]) {
	s.pushTo = append(s.pushTo, push.Add)
}
