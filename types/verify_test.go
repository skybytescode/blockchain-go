package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/skybytescode/blockchain-go/crypto"
	"github.com/skybytescode/blockchain-go/proto"
	"github.com/skybytescode/blockchain-go/util"
)

func newTx(signer *crypto.PrivateKey, nInputs int) *proto.Transaction {
	tx := &proto.Transaction{Version: 1}
	for i := 0; i < nInputs; i++ {
		tx.Inputs = append(tx.Inputs, &proto.TxInput{
			PrevTxHash: util.RandomHash(), PrevOutIndex: uint32(i), PublicKey: signer.Public().Bytes(),
		})
	}
	tx.Outputs = []*proto.TxOutput{{Amount: 10, Address: signer.Public().Address().Bytes()}}
	return tx
}

func signAll(signer *crypto.PrivateKey, tx *proto.Transaction) {
	sig := SignTransaction(signer, tx)
	for _, in := range tx.Inputs {
		in.Signature = sig.Bytes()
	}
}

// Peers send arbitrary transactions; bad ones must be rejected, not crash the node.
func TestVerifyTransactionRejectsMalformedInputsWithoutPanicking(t *testing.T) {
	key := crypto.GeneratePrivateKey()

	unsigned := newTx(key, 1)
	require.False(t, VerifyTransaction(unsigned))

	shortSig := newTx(key, 1)
	shortSig.Inputs[0].Signature = []byte{1, 2, 3}
	require.False(t, VerifyTransaction(shortSig))

	shortKey := newTx(key, 1)
	signAll(key, shortKey)
	shortKey.Inputs[0].PublicKey = []byte{1, 2, 3}
	require.False(t, VerifyTransaction(shortKey))
}

func TestVerifyTransactionWithSeveralInputs(t *testing.T) {
	key := crypto.GeneratePrivateKey()
	tx := newTx(key, 3)
	signAll(key, tx)
	require.True(t, VerifyTransaction(tx))

	tx.Outputs[0].Amount = 1_000_000
	require.False(t, VerifyTransaction(tx), "changing the outputs must break the signatures")
}

func TestVerifyTransactionLeavesTheTransactionUnchanged(t *testing.T) {
	key := crypto.GeneratePrivateKey()
	tx := newTx(key, 2)
	signAll(key, tx)
	tx.Outputs[0].Amount = 999 // invalid now
	before := HashTransaction(tx)

	require.False(t, VerifyTransaction(tx))
	require.Equal(t, before, HashTransaction(tx), "a failed verification must not strip signatures")
}

func TestVerifyBlockDetectsTamperedTransactions(t *testing.T) {
	key := crypto.GeneratePrivateKey()
	b := util.RandomBlock()
	tx := newTx(key, 1)
	signAll(key, tx)
	b.Transactions = []*proto.Transaction{tx}
	SignBlock(key, b)
	require.True(t, VerifyBlock(b))

	root := bytes.Clone(b.Header.RootHash)
	b.Transactions[0].Outputs[0].Amount = 1_000_000
	require.False(t, VerifyBlock(b))
	require.Equal(t, root, b.Header.RootHash, "verifying must not rewrite the block's Merkle root")
}
