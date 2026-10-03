package types

import (
	"crypto/sha256"

	pb "google.golang.org/protobuf/proto"

	"github.com/skybytescode/blockchain-go/crypto"
	"github.com/skybytescode/blockchain-go/proto"
)

// SignTransaction signs the transaction's signing hash; put the signature in
// each input the key owns.
func SignTransaction(pk *crypto.PrivateKey, tx *proto.Transaction) *crypto.Signature {
	return pk.Sign(SigningHash(tx))
}

// HashTransaction is the transaction's ID: the hash of all its fields,
// signatures included.
func HashTransaction(tx *proto.Transaction) []byte {
	b, err := pb.Marshal(tx)
	if err != nil {
		panic(err)
	}
	hash := sha256.Sum256(b)
	return hash[:]
}

// SigningHash is what every input signs: the transaction with all signatures
// removed, so a multi-input transaction hashes the same for each signer.
func SigningHash(tx *proto.Transaction) []byte {
	unsigned := pb.Clone(tx).(*proto.Transaction)
	for _, input := range unsigned.Inputs {
		input.Signature = nil
	}
	return HashTransaction(unsigned)
}

// VerifyTransaction reports whether every input carries a valid signature by
// its public key. Transactions come from peers, so malformed ones are rejected
// rather than allowed to panic, and tx is never modified.
func VerifyTransaction(tx *proto.Transaction) bool {
	hash := SigningHash(tx)
	for _, input := range tx.Inputs {
		if len(input.Signature) != crypto.SignatureLen || len(input.PublicKey) != crypto.PubKeyLen {
			return false
		}
		sig := crypto.SignatureFromBytes(input.Signature)
		if !sig.Verify(crypto.PublicKeyFromBytes(input.PublicKey), hash) {
			return false
		}
	}
	return true
}
