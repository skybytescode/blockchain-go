package node

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/skybytescode/blockchain-go/crypto"
	"github.com/skybytescode/blockchain-go/proto"
	"github.com/skybytescode/blockchain-go/types"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().String()
}

// startNetwork runs a validator and two peers in a line: c -> b -> validator.
func startNetwork(t *testing.T) (validator, b, c *Node, validatorKey *crypto.PrivateKey) {
	t.Helper()
	validatorKey = crypto.GeneratePrivateKey()
	trusted := []*crypto.PublicKey{validatorKey.Public()}

	start := func(key *crypto.PrivateKey, bootstrap ...string) *Node {
		addr := freeAddr(t)
		n := NewNode(ServerConfig{Version: "test", ListenAddr: addr, PrivateKey: key,
			Validators: trusted, BlockTime: 200 * time.Millisecond})
		go n.Start(addr, bootstrap)
		t.Cleanup(n.Stop)
		return n
	}
	validator = start(validatorKey)
	time.Sleep(100 * time.Millisecond)
	b = start(nil, validator.ListenAddr)
	time.Sleep(100 * time.Millisecond)
	c = start(nil, b.ListenAddr)

	// c learns about the validator through b's peer list.
	require.Eventually(t, func() bool { return len(c.peerClients()) == 2 && len(validator.peerClients()) == 2 },
		5*time.Second, 50*time.Millisecond, "nodes did not connect")
	return validator, b, c, validatorKey
}

func client(t *testing.T, n *Node) proto.NodeClient {
	t.Helper()
	c, err := makeNodeClient(n.ListenAddr)
	require.NoError(t, err)
	return c
}

func genesisSpend(chain *Chain, signer *crypto.PrivateKey, to []byte, amount, change int64) *proto.Transaction {
	prev, _ := chain.txStore.Get(genesisTxHash)
	in := &proto.TxInput{PrevTxHash: types.HashTransaction(prev), PrevOutIndex: 0}
	outs := []*proto.TxOutput{{Amount: amount, Address: to}}
	if change > 0 {
		outs = append(outs, &proto.TxOutput{Amount: change, Address: signer.Public().Address().Bytes()})
	}
	return signedTx(signer, []*proto.TxInput{in}, outs)
}

func TestTransactionBecomesABlockOnEveryNode(t *testing.T) {
	validator, b, c, _ := startNetwork(t)
	owner := crypto.NewPrivateKeyFromSeedStr(genesisSeed)
	alice := crypto.GeneratePrivateKey().Public().Address().Bytes()

	// Sent to c, two hops from the validator.
	tx := genesisSpend(c.chain, owner, alice, 100, 900)
	_, err := client(t, c).HandleTransaction(context.Background(), tx)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return validator.Height() == 1 && b.Height() == 1 && c.Height() == 1
	}, 5*time.Second, 50*time.Millisecond, "the block did not reach every node")

	for _, n := range []*Node{validator, b, c} {
		n.chainLock.Lock()
		block, err := n.chain.GetBlockByHeight(1)
		n.chainLock.Unlock()
		require.NoError(t, err)
		require.Len(t, block.Transactions, 1)
		require.Equal(t, types.HashTransaction(tx), types.HashTransaction(block.Transactions[0]))
		require.Zero(t, n.mempool.Len(), "included transactions must leave the mempool")
	}
}

func TestNodesRejectInvalidTransactions(t *testing.T) {
	_, _, c, _ := startNetwork(t)
	thief := crypto.GeneratePrivateKey()

	theft := genesisSpend(c.chain, thief, thief.Public().Address().Bytes(), 1000, 0)
	_, err := client(t, c).HandleTransaction(context.Background(), theft)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "not owned")
	require.Zero(t, c.mempool.Len())
}

func TestNodesRejectBlocksFromUntrustedSigners(t *testing.T) {
	_, b, _, _ := startNetwork(t)

	b.chainLock.Lock()
	head, err := b.chain.GetBlockByHeight(0)
	b.chainLock.Unlock()
	require.NoError(t, err)
	forged := &proto.Block{Header: &proto.Header{Version: 1, Height: 1, PrevHash: types.HashBlock(head)}}
	types.SignBlock(crypto.GeneratePrivateKey(), forged)

	_, err = client(t, b).HandleBlock(context.Background(), forged)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, 0, b.Height())
}

func TestMempoolRejectsConflictingTransactions(t *testing.T) {
	chain := NewChain(NewMemoryBlockStore(), NewMemoryTXStore())
	owner := crypto.NewPrivateKeyFromSeedStr(genesisSeed)
	pool := NewMempool()

	first := genesisSpend(chain, owner, crypto.GeneratePrivateKey().Public().Address().Bytes(), 1000, 0)
	added, err := pool.Add(first)
	require.NoError(t, err)
	require.True(t, added)

	added, err = pool.Add(first)
	require.NoError(t, err)
	require.False(t, added, "the same transaction twice is not an error, just not new")

	second := genesisSpend(chain, owner, crypto.GeneratePrivateKey().Public().Address().Bytes(), 1000, 0)
	_, err = pool.Add(second)
	require.ErrorContains(t, err, "already spent by pending tx")

	pool.Remove([]*proto.Transaction{first})
	added, err = pool.Add(second)
	require.NoError(t, err)
	require.True(t, added, "once the first is gone its inputs are free again")
}
