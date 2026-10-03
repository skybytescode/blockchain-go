package node

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/skybytescode/blockchain-go/crypto"
	"github.com/skybytescode/blockchain-go/proto"
	"github.com/skybytescode/blockchain-go/types"
)

const defaultBlockTime = 5 * time.Second

type ServerConfig struct {
	Version    string
	ListenAddr string
	// PrivateKey makes the node a validator that produces blocks.
	PrivateKey *crypto.PrivateKey
	// Validators are the public keys allowed to sign blocks (proof of
	// authority). Blocks signed by any other key are rejected.
	Validators []*crypto.PublicKey
	// BlockTime is how often a validator turns pending transactions into a
	// block. Zero means 5 seconds.
	BlockTime time.Duration
}

type Node struct {
	ServerConfig
	logger *zap.SugaredLogger

	peerLock sync.RWMutex
	peers    map[proto.NodeClient]*proto.Version
	mempool  *Mempool

	chainLock sync.Mutex // Chain is not safe for concurrent use
	chain     *Chain

	server *grpc.Server

	proto.UnimplementedNodeServer
}

func NewNode(cfg ServerConfig) *Node {
	if cfg.BlockTime == 0 {
		cfg.BlockTime = defaultBlockTime
	}
	loggerConfig := zap.NewDevelopmentConfig()
	loggerConfig.EncoderConfig.TimeKey = ""
	loggerConfig.DisableStacktrace = true // a rejected peer message is routine, not a crash
	logger, _ := loggerConfig.Build()
	n := &Node{
		ServerConfig: cfg,
		peers:        make(map[proto.NodeClient]*proto.Version),
		logger:       logger.Sugar().With("node", cfg.ListenAddr),
		mempool:      NewMempool(),
		chain:        NewChain(NewMemoryBlockStore(), NewMemoryTXStore()),
		server:       grpc.NewServer(),
	}
	proto.RegisterNodeServer(n.server, n)
	return n
}

// Start serves the node on listenAddr, connects to the bootstrap nodes and, for
// a validator, starts producing blocks. It blocks until Stop is called.
func (n *Node) Start(listenAddr string, bootstrapNodes []string) error {
	if listenAddr != n.ListenAddr { // set before other goroutines read it
		n.ListenAddr = listenAddr
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	n.logger.Infow("node started", "validator", n.PrivateKey != nil)

	if len(bootstrapNodes) > 0 {
		go n.bootstrapNetwork(bootstrapNodes)
	}
	if n.PrivateKey != nil {
		go n.validatorLoop()
	}
	return n.server.Serve(ln)
}

// Stop shuts the gRPC server down.
func (n *Node) Stop() {
	n.server.Stop()
}

// Height is the height of the node's chain.
func (n *Node) Height() int {
	n.chainLock.Lock()
	defer n.chainLock.Unlock()
	return n.chain.Height()
}

func (n *Node) Handshake(ctx context.Context, v *proto.Version) (*proto.Version, error) {
	c, err := makeNodeClient(v.ListenAddr)
	if err != nil {
		return nil, err
	}
	n.addPeer(c, v)
	return n.getVersion(), nil
}

// HandleTransaction validates a transaction against the chain, keeps it for
// the next block and passes it on to the peers.
func (n *Node) HandleTransaction(ctx context.Context, tx *proto.Transaction) (*proto.Ack, error) {
	hash := hex.EncodeToString(types.HashTransaction(tx))

	n.chainLock.Lock()
	err := n.chain.ValidateTransaction(tx)
	n.chainLock.Unlock()
	if err != nil {
		n.logger.Warnw("rejected tx", "hash", short(hash), "reason", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	added, err := n.mempool.Add(tx)
	if err != nil {
		n.logger.Warnw("rejected tx", "hash", short(hash), "reason", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if added {
		n.logger.Infow("accepted tx", "hash", short(hash), "mempool", n.mempool.Len())
		go n.broadcastTx(tx)
	}
	return &proto.Ack{}, nil
}

// HandleBlock adds a block from a trusted validator to the chain and passes it
// on. Blocks the node already has are ignored, which ends the gossip.
func (n *Node) HandleBlock(ctx context.Context, b *proto.Block) (*proto.Ack, error) {
	hash := hex.EncodeToString(types.HashBlock(b))
	if !n.isValidator(b.PublicKey) {
		n.logger.Warnw("rejected block", "hash", short(hash), "reason", "not signed by a trusted validator")
		return nil, status.Error(codes.PermissionDenied, "block is not signed by a trusted validator")
	}

	n.chainLock.Lock()
	if _, err := n.chain.GetBlockByHash(types.HashBlock(b)); err == nil {
		n.chainLock.Unlock()
		return &proto.Ack{}, nil
	}
	err := n.chain.AddBlock(b)
	height := n.chain.Height()
	n.chainLock.Unlock()
	if err != nil {
		n.logger.Warnw("rejected block", "hash", short(hash), "reason", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	n.mempool.Remove(b.Transactions)
	n.logger.Infow("added block", "height", height, "hash", short(hash), "txs", len(b.Transactions))
	go n.broadcastBlock(b)
	return &proto.Ack{}, nil
}

func (n *Node) isValidator(pubKey []byte) bool {
	for _, v := range n.Validators {
		if bytes.Equal(v.Bytes(), pubKey) {
			return true
		}
	}
	return false
}

// validatorLoop turns pending transactions into a signed block every
// BlockTime. Transactions that are no longer valid are dropped.
func (n *Node) validatorLoop() {
	n.logger.Infow("validator loop started", "blockTime", n.BlockTime)
	ticker := time.NewTicker(n.BlockTime)
	defer ticker.Stop()
	for range ticker.C {
		if b := n.createBlock(); b != nil {
			go n.broadcastBlock(b)
		}
	}
}

func (n *Node) createBlock() *proto.Block {
	pending := n.mempool.Pending()
	if len(pending) == 0 {
		return nil
	}

	n.chainLock.Lock()
	defer n.chainLock.Unlock()

	var (
		included []*proto.Transaction
		dropped  []*proto.Transaction
		spent    = make(map[string]bool)
	)
	for _, tx := range pending {
		if err := n.chain.validateTransaction(tx, spent); err != nil {
			dropped = append(dropped, tx)
			continue
		}
		included = append(included, tx)
	}
	n.mempool.Remove(dropped)
	if len(included) == 0 {
		return nil
	}

	prev, err := n.chain.GetBlockByHeight(n.chain.Height())
	if err != nil {
		n.logger.Errorw("cannot read the chain head", "err", err)
		return nil
	}
	b := &proto.Block{
		Header: &proto.Header{
			Version:   1,
			Height:    int32(n.chain.Height() + 1),
			PrevHash:  types.HashBlock(prev),
			Timestamp: time.Now().UnixNano(),
		},
		Transactions: included,
	}
	types.SignBlock(n.PrivateKey, b)
	if err := n.chain.AddBlock(b); err != nil {
		n.logger.Errorw("own block rejected", "err", err)
		return nil
	}
	n.mempool.Remove(included)
	n.logger.Infow("created block", "height", b.Header.Height,
		"hash", short(hex.EncodeToString(types.HashBlock(b))), "txs", len(included))
	return b
}

func (n *Node) broadcastTx(tx *proto.Transaction) {
	for _, peer := range n.peerClients() {
		if _, err := peer.HandleTransaction(context.Background(), tx); err != nil {
			n.logger.Debugw("peer did not take tx", "err", err)
		}
	}
}

func (n *Node) broadcastBlock(b *proto.Block) {
	for _, peer := range n.peerClients() {
		if _, err := peer.HandleBlock(context.Background(), b); err != nil {
			n.logger.Debugw("peer did not take block", "err", err)
		}
	}
}

// peerClients copies the peer list so it can be used without holding the lock.
func (n *Node) peerClients() []proto.NodeClient {
	n.peerLock.RLock()
	defer n.peerLock.RUnlock()
	clients := make([]proto.NodeClient, 0, len(n.peers))
	for c := range n.peers {
		clients = append(clients, c)
	}
	return clients
}

func (n *Node) addPeer(c proto.NodeClient, v *proto.Version) {
	n.peerLock.Lock()
	defer n.peerLock.Unlock()

	n.peers[c] = v

	// Connect to the peers the new node knows about.
	if len(v.PeerList) > 0 {
		go n.bootstrapNetwork(v.PeerList)
	}

	n.logger.Debugw("peer connected", "remote", v.ListenAddr, "height", v.Height)
}

func (n *Node) bootstrapNetwork(addrs []string) {
	for _, addr := range addrs {
		if !n.canConnectWith(addr) {
			continue
		}
		c, v, err := n.dialRemoteNode(addr)
		if err != nil {
			n.logger.Warnw("cannot reach peer", "addr", addr, "err", err)
			continue
		}
		n.addPeer(c, v)
	}
}

func (n *Node) dialRemoteNode(addr string) (proto.NodeClient, *proto.Version, error) {
	c, err := makeNodeClient(addr)
	if err != nil {
		return nil, nil, err
	}
	v, err := c.Handshake(context.Background(), n.getVersion())
	if err != nil {
		return nil, nil, err
	}
	return c, v, nil
}

func (n *Node) getVersion() *proto.Version {
	return &proto.Version{
		Version:    n.Version,
		Height:     int32(n.Height()),
		ListenAddr: n.ListenAddr,
		PeerList:   n.getPeerList(),
	}
}

func (n *Node) canConnectWith(addr string) bool {
	if n.ListenAddr == addr {
		return false
	}
	for _, connected := range n.getPeerList() {
		if addr == connected {
			return false
		}
	}
	return true
}

func (n *Node) getPeerList() []string {
	n.peerLock.RLock()
	defer n.peerLock.RUnlock()

	peers := []string{}
	for _, version := range n.peers {
		peers = append(peers, version.ListenAddr)
	}
	return peers
}

func makeNodeClient(listenAddr string) (proto.NodeClient, error) {
	c, err := grpc.NewClient(listenAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return proto.NewNodeClient(c), nil
}

func short(hash string) string {
	if len(hash) > 10 {
		return hash[:10]
	}
	return hash
}
