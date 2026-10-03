package node

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/skybytescode/blockchain-go/crypto"
	"github.com/skybytescode/blockchain-go/proto"
	"github.com/skybytescode/blockchain-go/types"
)

const genesisTxHash = "52364e5e9fbc479ddf02f22cdd95f8bae0dd59a2bcd56f4d9013b54d46f06d3c"

// signedTx builds a transaction that spends the given outputs and signs every
// input with signer.
func signedTx(signer *crypto.PrivateKey, inputs []*proto.TxInput, outputs []*proto.TxOutput) *proto.Transaction {
	for _, in := range inputs {
		in.PublicKey = signer.Public().Bytes()
	}
	tx := &proto.Transaction{Version: 1, Inputs: inputs, Outputs: outputs}
	sig := types.SignTransaction(signer, tx)
	for _, in := range tx.Inputs {
		in.Signature = sig.Bytes()
	}
	return tx
}

func spend(t *testing.T, chain *Chain, txHash string, outIndex uint32) *proto.TxInput {
	t.Helper()
	prev, err := chain.txStore.Get(txHash)
	require.NoError(t, err)
	return &proto.TxInput{PrevTxHash: types.HashTransaction(prev), PrevOutIndex: outIndex}
}

func blockWith(t *testing.T, chain *Chain, txx ...*proto.Transaction) *proto.Block {
	t.Helper()
	b := randomBlock(t, chain)
	b.Transactions = txx
	types.SignBlock(crypto.GeneratePrivateKey(), b)
	return b
}

func TestCannotSpendSomeoneElsesCoins(t *testing.T) {
	chain := NewChain(NewMemoryBlockStore(), NewMemoryTXStore())
	thief := crypto.GeneratePrivateKey()

	// The genesis output belongs to the genesis key; the thief signs with their own.
	tx := signedTx(thief,
		[]*proto.TxInput{spend(t, chain, genesisTxHash, 0)},
		[]*proto.TxOutput{{Amount: 1000, Address: thief.Public().Address().Bytes()}})

	require.ErrorContains(t, chain.AddBlock(blockWith(t, chain, tx)), "not owned")
}

func TestRejectsNegativeOutputs(t *testing.T) {
	chain := NewChain(NewMemoryBlockStore(), NewMemoryTXStore())
	owner := crypto.NewPrivateKeyFromSeedStr(genesisSeed)
	other := crypto.GeneratePrivateKey()

	// 2000 - 1000 = 1000 "balances", but would create 1000 coins from nothing.
	tx := signedTx(owner,
		[]*proto.TxInput{spend(t, chain, genesisTxHash, 0)},
		[]*proto.TxOutput{
			{Amount: 2000, Address: owner.Public().Address().Bytes()},
			{Amount: -1000, Address: other.Public().Address().Bytes()},
		})

	require.ErrorContains(t, chain.AddBlock(blockWith(t, chain, tx)), "must be positive")
}

func TestSpendsTheReferencedOutput(t *testing.T) {
	chain := NewChain(NewMemoryBlockStore(), NewMemoryTXStore())
	owner := crypto.NewPrivateKeyFromSeedStr(genesisSeed)
	alice := crypto.GeneratePrivateKey()

	// Split the genesis coins: 100 to Alice (output 0), 900 change (output 1).
	split := signedTx(owner,
		[]*proto.TxInput{spend(t, chain, genesisTxHash, 0)},
		[]*proto.TxOutput{
			{Amount: 100, Address: alice.Public().Address().Bytes()},
			{Amount: 900, Address: owner.Public().Address().Bytes()},
		})
	require.NoError(t, chain.AddBlock(blockWith(t, chain, split)))

	// Spending the 900 change must look up output 1, not output 0.
	change := &proto.TxInput{PrevTxHash: types.HashTransaction(split), PrevOutIndex: 1}
	tx := signedTx(owner, []*proto.TxInput{change},
		[]*proto.TxOutput{{Amount: 900, Address: alice.Public().Address().Bytes()}})
	require.NoError(t, chain.AddBlock(blockWith(t, chain, tx)))
}

func TestRejectsDoubleSpends(t *testing.T) {
	owner := crypto.NewPrivateKeyFromSeedStr(genesisSeed)
	to := func() []*proto.TxOutput {
		return []*proto.TxOutput{{Amount: 1000, Address: crypto.GeneratePrivateKey().Public().Address().Bytes()}}
	}

	t.Run("same output twice in one block", func(t *testing.T) {
		chain := NewChain(NewMemoryBlockStore(), NewMemoryTXStore())
		a := signedTx(owner, []*proto.TxInput{spend(t, chain, genesisTxHash, 0)}, to())
		b := signedTx(owner, []*proto.TxInput{spend(t, chain, genesisTxHash, 0)}, to())
		require.ErrorContains(t, chain.AddBlock(blockWith(t, chain, a, b)), "spent twice")
	})

	t.Run("same output twice in one transaction", func(t *testing.T) {
		chain := NewChain(NewMemoryBlockStore(), NewMemoryTXStore())
		tx := signedTx(owner, []*proto.TxInput{spend(t, chain, genesisTxHash, 0), spend(t, chain, genesisTxHash, 0)},
			[]*proto.TxOutput{{Amount: 2000, Address: owner.Public().Address().Bytes()}})
		require.ErrorContains(t, chain.AddBlock(blockWith(t, chain, tx)), "spent twice")
	})

	t.Run("output already spent in an earlier block", func(t *testing.T) {
		chain := NewChain(NewMemoryBlockStore(), NewMemoryTXStore())
		first := signedTx(owner, []*proto.TxInput{spend(t, chain, genesisTxHash, 0)}, to())
		require.NoError(t, chain.AddBlock(blockWith(t, chain, first)))
		again := signedTx(owner, []*proto.TxInput{spend(t, chain, genesisTxHash, 0)}, to())
		require.ErrorContains(t, chain.AddBlock(blockWith(t, chain, again)), "already spent")
	})
}

func TestUnknownInputIsAnErrorNotAPanic(t *testing.T) {
	chain := NewChain(NewMemoryBlockStore(), NewMemoryTXStore())
	owner := crypto.NewPrivateKeyFromSeedStr(genesisSeed)
	missing := &proto.TxInput{PrevTxHash: make([]byte, 32), PrevOutIndex: 0}
	tx := signedTx(owner, []*proto.TxInput{missing},
		[]*proto.TxOutput{{Amount: 1, Address: owner.Public().Address().Bytes()}})

	require.ErrorContains(t, chain.AddBlock(blockWith(t, chain, tx)), "could not find utxo")
}
