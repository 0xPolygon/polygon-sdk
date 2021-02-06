package minimal

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/0xPolygon/minimal/api"
	"github.com/0xPolygon/minimal/blockchain/storage"
	"github.com/0xPolygon/minimal/blockchain/storage/leveldb"
	"github.com/0xPolygon/minimal/chain"
	"github.com/0xPolygon/minimal/minimal/keystore"
	"github.com/0xPolygon/minimal/minimal/proto"
	"github.com/0xPolygon/minimal/protocol2"
	"github.com/0xPolygon/minimal/state"
	"github.com/armon/go-metrics"
	"github.com/armon/go-metrics/prometheus"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p-core/host"
	ma "github.com/multiformats/go-multiaddr"
	"google.golang.org/grpc"

	"github.com/0xPolygon/minimal/protocol"

	libp2pgrpc "github.com/0xPolygon/minimal/helper/grpc"
	itrie "github.com/0xPolygon/minimal/state/immutable-trie"
	"github.com/0xPolygon/minimal/state/runtime/evm"
	"github.com/0xPolygon/minimal/state/runtime/precompiled"

	"github.com/0xPolygon/minimal/blockchain"
	"github.com/0xPolygon/minimal/consensus"
	"github.com/0xPolygon/minimal/crypto"
	"github.com/0xPolygon/minimal/sealer"
)

// Minimal is the central manager of the blockchain client
type Server struct {
	logger hclog.Logger
	config *Config
	Sealer *sealer.Sealer

	backends  []protocol.Backend
	consensus consensus.Consensus

	// blockchain stack
	blockchain *blockchain.Blockchain
	storage    storage.Storage

	key       *ecdsa.PrivateKey
	chain     *chain.Chain
	apis      []api.API
	InmemSink *metrics.InmemSink
	devMode   bool

	// system grpc server
	grpcServer *grpc.Server

	// libp2p stack
	host         host.Host
	libp2pServer *libp2pgrpc.GRPCProtocol
	addrs        []ma.Multiaddr

	// syncer protocol
	syncer *protocol2.Syncer
}

var dirPaths = []string{
	"blockchain",
	"consensus",
	"keystore",
	"trie",
}

func NewServer(logger hclog.Logger, config *Config) (*Server, error) {
	m := &Server{
		logger:     logger,
		config:     config,
		backends:   []protocol.Backend{},
		apis:       []api.API{},
		chain:      config.Chain,
		grpcServer: grpc.NewServer(),
	}

	m.logger.Info("Data dir", "path", config.DataDir)

	// Generate all the paths in the dataDir
	if err := setupDataDir(config.DataDir, dirPaths); err != nil {
		return nil, fmt.Errorf("failed to create data directories: %v", err)
	}

	// Get the private key for the node
	keystore := keystore.NewLocalKeystore(filepath.Join(config.DataDir, "keystore"))
	key, err := keystore.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %v", err)
	}
	m.key = key

	storage, err := leveldb.NewLevelDBStorage(filepath.Join(config.DataDir, "blockchain"), logger)
	if err != nil {
		return nil, err
	}
	m.storage = storage

	// Setup consensus
	if err := m.setupConsensus(); err != nil {
		return nil, err
	}

	stateStorage, err := itrie.NewLevelDBStorage(filepath.Join(m.config.DataDir, "trie"), logger)
	if err != nil {
		return nil, err
	}

	st := itrie.NewState(stateStorage)

	executor := state.NewExecutor(config.Chain.Params, st)
	executor.SetRuntime(precompiled.NewPrecompiled())
	executor.SetRuntime(evm.NewEVM())

	// blockchain object
	m.blockchain = blockchain.NewBlockchain(storage, config.Chain.Params, m.consensus, executor)
	if err := m.blockchain.WriteGenesis(config.Chain.Genesis); err != nil {
		return nil, err
	}

	executor.GetHash = m.blockchain.GetHashHelper

	// Setup sealer
	sealerConfig := &sealer.Config{
		Coinbase: crypto.PubKeyToAddress(&m.key.PublicKey),
	}
	m.Sealer = sealer.NewSealer(sealerConfig, logger, m.blockchain, m.consensus, executor)
	m.Sealer.SetEnabled(m.config.Seal)

	// setup libp2p server
	if err := m.setupLibP2P(); err != nil {
		return nil, err
	}

	// setup grpc server
	if err := m.setupGRPC(); err != nil {
		return nil, err
	}

	// setup syncer protocol
	m.syncer = protocol2.NewSyncer()
	m.syncer.Register(m.libp2pServer.GetGRPCServer())
	m.syncer.Start()

	// register the libp2p GRPC endpoints
	proto.RegisterHandshakeServer(m.libp2pServer.GetGRPCServer(), &handshakeService{})

	m.libp2pServer.Serve()
	return m, nil
}

func (s *Server) setupConsensus() error {
	engineName := s.config.Chain.Params.GetEngine()
	engine, ok := consensusBackends[engineName]
	if !ok {
		return fmt.Errorf("consensus engine '%s' not found", engineName)
	}

	config := &consensus.Config{
		Params: s.config.Chain.Params,
		Config: s.config.ConsensusConfig,
	}
	config.Config["path"] = filepath.Join(s.config.DataDir, "consensus")

	consensus, err := engine(context.Background(), config, s.key, s.storage, s.logger)
	if err != nil {
		return err
	}
	s.consensus = consensus
	return nil
}

func (s *Server) setupGRPC() error {
	s.grpcServer = grpc.NewServer()

	proto.RegisterSystemServer(s.grpcServer, &systemService{s})

	lis, err := net.Listen("tcp", s.config.GRPCAddr.String())
	if err != nil {
		return err
	}

	go func() {
		if err := s.grpcServer.Serve(lis); err != nil {
			s.logger.Error(err.Error())
		}
	}()

	s.logger.Info("GRPC server running", "addr", s.config.GRPCAddr.String())
	return nil
}

// Chain returns the chain object of the client
func (s *Server) Chain() *chain.Chain {
	return s.chain
}

func (s *Server) Join(addr0 string) error {
	s.logger.Info("[INFO]: Join peer", "addr", addr0)

	// add peer to the libp2p peerstore
	peerID, err := s.AddPeerFromMultiAddrString(addr0)
	if err != nil {
		return err
	}

	// perform handshake protocol
	conn, err := s.dial(peerID)
	if err != nil {
		return err
	}
	clt := proto.NewHandshakeClient(conn)
	if _, err := clt.Hello(context.Background(), &empty.Empty{}); err != nil {
		return err
	}

	// send the connection to the syncer
	go s.syncer.HandleUser(conn)

	return nil
}

func (s *Server) Close() {
	if err := s.blockchain.Close(); err != nil {
		s.logger.Error("failed to close blockchain", "err", err.Error())
	}
	s.host.Close()
}

// Entry is a backend configuration entry
type Entry struct {
	Enabled bool
	Config  map[string]interface{}
}

func (e *Entry) addPath(path string) {
	if len(e.Config) == 0 {
		e.Config = map[string]interface{}{}
	}
	if _, ok := e.Config["path"]; !ok {
		e.Config["path"] = path
	}
}

func addPath(paths []string, path string, entries map[string]*Entry) []string {
	newpath := paths[0:]
	newpath = append(newpath, path)
	for name := range entries {
		newpath = append(newpath, filepath.Join(path, name))
	}
	return newpath
}

func setupDataDir(dataDir string, paths []string) error {
	if err := createDir(dataDir); err != nil {
		return fmt.Errorf("Failed to create data dir: (%s): %v", dataDir, err)
	}

	for _, path := range paths {
		path := filepath.Join(dataDir, path)
		if err := createDir(path); err != nil {
			return fmt.Errorf("Failed to create path: (%s): %v", path, err)
		}
	}
	return nil
}

func createDir(path string) error {
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, os.ModePerm); err != nil {
			return err
		}
	}
	return nil
}

func getSingleKey(i map[string]*Entry) string {
	for k := range i {
		return k
	}
	panic("internal. key not found")
}

func (s *Server) startTelemetry() error {
	s.InmemSink = metrics.NewInmemSink(10*time.Second, time.Minute)
	metrics.DefaultInmemSignal(s.InmemSink)

	metricsConf := metrics.DefaultConfig("minimal")
	metricsConf.EnableHostnameLabel = false
	metricsConf.HostName = ""

	var sinks metrics.FanoutSink

	prom, err := prometheus.NewPrometheusSink()
	if err != nil {
		return err
	}

	sinks = append(sinks, prom)
	sinks = append(sinks, s.InmemSink)

	metrics.NewGlobal(metricsConf, sinks)
	return nil
}