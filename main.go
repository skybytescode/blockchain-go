// Command blockchain runs a three-node demo network on localhost: a validator
// on :7000 and two peers on :7001 and :7002, joined in a line. A demo wallet
// that owns the genesis coins sends signed payments to the node furthest from
// the validator; they travel to the validator, end up in blocks, and the
// blocks travel back to every node.
package main

import (
	"context"
	"encoding/hex"
	"log"
	"math/rand"
	"time"

	"github.com/skybytescode/blockchain-go/crypto"
	"github.com/skybytescode/blockchain-go/node"
	"github.com/skybytescode/blockchain-go/proto"
	"github.com/skybytescode/blockchain-go/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// Demo keys only. The genesis key owns the 1000 coins created in the
	// genesis block (see node/chain.go).
	genesisSeed   = "704c4c0aa9cce32540199cfa0e630a1aa12326e338426c2591caf4d7b89f9e93"
	validatorSeed = "a1c0ffee00000000000000000000000000000000000000000000000000000001"
	blockTime     = 3 * time.Second
)

func main() {
	validatorKey := crypto.NewPrivateKeyFromSeedStr(validatorSeed)
	trusted := []*crypto.PublicKey{validatorKey.Public()}

	validator := startNode(":7000", nil, validatorKey, trusted)
	time.Sleep(500 * time.Millisecond)
	startNode(":7001", []string{":7000"}, nil, trusted)
	time.Sleep(500 * time.Millisecond)
	edge := startNode(":7002", []string{":7001"}, nil, trusted)
	time.Sleep(time.Second)

	conn, err := grpc.NewClient(":7002", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	runWallet(proto.NewNodeClient(conn), edge, validator)
}

func startNode(addr string, bootstrap []string, key *crypto.PrivateKey, trusted []*crypto.PublicKey) *node.Node {
	n := node.NewNode(node.ServerConfig{
		Version:    "blockchain-go/1",
		ListenAddr: addr,
		PrivateKey: key,
		Validators: trusted,
		BlockTime:  blockTime,
	})
	go func() {
		if err := n.Start(addr, bootstrap); err != nil {
			log.Fatal(err)
		}
	}()
	return n
}

// runWallet spends the genesis coins in small payments, each one using the
// change of the previous payment once it is confirmed in a block.
func runWallet(client proto.NodeClient, edge, validator *node.Node) {
	owner := crypto.NewPrivateKeyFromSeedStr(genesisSeed)
	genesisTx := &proto.Transaction{Version: 1, Outputs: []*proto.TxOutput{{Amount: 1000, Address: owner.Public().Address().Bytes()}}}
	coin := &proto.TxInput{PrevTxHash: types.HashTransaction(genesisTx), PrevOutIndex: 0}
	balance := int64(1000)

	// A thief signing with their own key must be turned away.
	thief := crypto.GeneratePrivateKey()
	theft := sign(thief, []*proto.TxInput{{PrevTxHash: coin.PrevTxHash, PrevOutIndex: 0}},
		[]*proto.TxOutput{{Amount: 1000, Address: thief.Public().Address().Bytes()}})
	if _, err := client.HandleTransaction(context.Background(), theft); err != nil {
		log.Printf("wallet: theft attempt rejected: %v", err)
	}

	for balance > 0 {
		amount := int64(10 + rand.Intn(40))
		if amount > balance {
			amount = balance
		}
		to := crypto.GeneratePrivateKey().Public().Address()
		outputs := []*proto.TxOutput{{Amount: amount, Address: to.Bytes()}}
		if balance > amount {
			outputs = append(outputs, &proto.TxOutput{Amount: balance - amount, Address: owner.Public().Address().Bytes()})
		}
		tx := sign(owner, []*proto.TxInput{coin}, outputs)

		height := edge.Height()
		if _, err := client.HandleTransaction(context.Background(), tx); err != nil {
			log.Fatalf("wallet: payment rejected: %v", err)
		}
		balance -= amount
		log.Printf("wallet: sent %d to %s… (tx %s), balance %d", amount, to.String()[:10],
			hex.EncodeToString(types.HashTransaction(tx))[:10], balance)

		// Wait for the block to reach this node before spending the change.
		for edge.Height() == height || validator.Height() != edge.Height() {
			time.Sleep(100 * time.Millisecond)
		}
		coin = &proto.TxInput{PrevTxHash: types.HashTransaction(tx), PrevOutIndex: 1}
	}
	select {}
}

func sign(key *crypto.PrivateKey, inputs []*proto.TxInput, outputs []*proto.TxOutput) *proto.Transaction {
	for _, in := range inputs {
		in.PublicKey = key.Public().Bytes()
	}
	tx := &proto.Transaction{Version: 1, Inputs: inputs, Outputs: outputs}
	sig := types.SignTransaction(key, tx)
	for _, in := range tx.Inputs {
		in.Signature = sig.Bytes()
	}
	return tx
}
