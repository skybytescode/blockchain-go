package node

import (
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/skybytescode/blockchain-go/proto"
	"github.com/skybytescode/blockchain-go/types"
)

// Mempool holds valid transactions waiting to be put in a block. It also
// remembers which outputs pending transactions spend, so a second transaction
// spending the same output is turned away before it reaches a block.
type Mempool struct {
	lock   sync.Mutex
	txx    map[string]*proto.Transaction
	inputs map[string]string // spent output -> hash of the pending tx spending it
}

func NewMempool() *Mempool {
	return &Mempool{
		txx:    make(map[string]*proto.Transaction),
		inputs: make(map[string]string),
	}
}

// Add stores tx and reports whether it was new. It returns an error when tx
// spends an output that another pending transaction already spends.
func (pool *Mempool) Add(tx *proto.Transaction) (bool, error) {
	pool.lock.Lock()
	defer pool.lock.Unlock()

	hash := hex.EncodeToString(types.HashTransaction(tx))
	if _, ok := pool.txx[hash]; ok {
		return false, nil
	}
	for _, input := range tx.Inputs {
		key := utxoKey(hex.EncodeToString(input.PrevTxHash), int(input.PrevOutIndex))
		if other, ok := pool.inputs[key]; ok {
			return false, fmt.Errorf("output %s is already spent by pending tx %s", key, other)
		}
	}
	pool.txx[hash] = tx
	for _, input := range tx.Inputs {
		pool.inputs[utxoKey(hex.EncodeToString(input.PrevTxHash), int(input.PrevOutIndex))] = hash
	}
	return true, nil
}

// Pending returns the pending transactions without removing them.
func (pool *Mempool) Pending() []*proto.Transaction {
	pool.lock.Lock()
	defer pool.lock.Unlock()

	txx := make([]*proto.Transaction, 0, len(pool.txx))
	for _, tx := range pool.txx {
		txx = append(txx, tx)
	}
	return txx
}

// Remove drops transactions, typically because a block included them.
func (pool *Mempool) Remove(txx []*proto.Transaction) {
	pool.lock.Lock()
	defer pool.lock.Unlock()

	for _, tx := range txx {
		hash := hex.EncodeToString(types.HashTransaction(tx))
		if _, ok := pool.txx[hash]; !ok {
			continue
		}
		delete(pool.txx, hash)
		for _, input := range tx.Inputs {
			delete(pool.inputs, utxoKey(hex.EncodeToString(input.PrevTxHash), int(input.PrevOutIndex)))
		}
	}
}

func (pool *Mempool) Len() int {
	pool.lock.Lock()
	defer pool.lock.Unlock()
	return len(pool.txx)
}
