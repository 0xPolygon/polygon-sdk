package chain

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"

	"github.com/0xPolygon/minimal/helper/hex"
	"github.com/0xPolygon/minimal/types"
	"github.com/hashicorp/go-multierror"
)

var (
	// GenesisGasLimit is the default gas limit of the Genesis block.
	GenesisGasLimit uint64 = 4712388

	// GenesisDifficulty is the default difficulty of the Genesis block.
	GenesisDifficulty = big.NewInt(131072)
)

// Chain is the blockchain chain configuration
type Chain struct {
	Name      string   `json:"name"`
	Genesis   *Genesis `json:"genesis"`
	Params    *Params  `json:"params"`
	Bootnodes []string `json:"bootnodes"`
}

var (
	IBFTEngine  = "ibft"
	DummyEngine = "dummy"
)

// Genesis specifies the header fields, state of a genesis block
type Genesis struct {
	Config *Params `json:"config"`

	Nonce      [8]byte                           `json:"nonce"`
	Timestamp  uint64                            `json:"timestamp"`
	ExtraData  []byte                            `json:"extraData,omitempty"`
	GasLimit   uint64                            `json:"gasLimit"`
	Difficulty uint64                            `json:"difficulty"`
	Mixhash    types.Hash                        `json:"mixHash"`
	Coinbase   types.Address                     `json:"coinbase"`
	Alloc      map[types.Address]*GenesisAccount `json:"alloc,omitempty"`

	// Initial staked balance
	AllocStake map[types.Address]*GenesisStake `json:"allocStake,omitempty"`

	// Override
	StateRoot types.Hash

	// Only for testing
	Number     uint64     `json:"number"`
	GasUsed    uint64     `json:"gasUsed"`
	ParentHash types.Hash `json:"parentHash"`
}

// GenesisHeader converts the initially defined genesis struct to a header
func (g *Genesis) GenesisHeader() *types.Header {
	stateRoot := types.EmptyRootHash

	if g.StateRoot != types.ZeroHash {
		stateRoot = g.StateRoot
	}

	head := &types.Header{
		Number:       g.Number,
		Nonce:        g.Nonce,
		Timestamp:    g.Timestamp,
		ParentHash:   g.ParentHash,
		ExtraData:    g.ExtraData,
		GasLimit:     g.GasLimit,
		GasUsed:      g.GasUsed,
		Difficulty:   g.Difficulty,
		MixHash:      g.Mixhash,
		Miner:        g.Coinbase,
		StateRoot:    stateRoot,
		Sha3Uncles:   types.EmptyUncleHash,
		ReceiptsRoot: types.EmptyRootHash,
		TxRoot:       types.EmptyRootHash,
	}

	// Set default values if none are passed in
	if g.GasLimit == 0 {
		head.GasLimit = GenesisGasLimit
	}

	if g.Difficulty == 0 {
		head.Difficulty = GenesisDifficulty.Uint64()
	}

	return head
}

// Hash computes the genesis hash
func (g *Genesis) Hash() types.Hash {
	header := g.GenesisHeader()
	header.ComputeHash()

	return header.Hash
}

// MarshalJSON implements the json interface
func (g *Genesis) MarshalJSON() ([]byte, error) {
	type Genesis struct {
		Nonce      string                      `json:"nonce"`
		Timestamp  *string                     `json:"timestamp,omitempty"`
		ExtraData  *string                     `json:"extraData,omitempty"`
		GasLimit   *string                     `json:"gasLimit,omitempty"`
		Difficulty *string                     `json:"difficulty,omitempty"`
		Mixhash    types.Hash                  `json:"mixHash"`
		Coinbase   types.Address               `json:"coinbase"`
		Alloc      *map[string]*GenesisAccount `json:"alloc,omitempty"`
		AllocStake *map[string]*GenesisStake   `json:"allocStake,omitempty"`
		Number     *string                     `json:"number,omitempty"`
		GasUsed    *string                     `json:"gasUsed,omitempty"`
		ParentHash types.Hash                  `json:"parentHash"`
	}

	var enc Genesis
	enc.Nonce = hex.EncodeToHex(g.Nonce[:])

	enc.Timestamp = types.EncodeUint64(g.Timestamp)
	enc.ExtraData = types.EncodeBytes(g.ExtraData)

	enc.GasLimit = types.EncodeUint64(g.GasLimit)
	enc.Difficulty = types.EncodeUint64(g.Difficulty)

	enc.Mixhash = g.Mixhash
	enc.Coinbase = g.Coinbase

	if g.Alloc != nil {
		alloc := make(map[string]*GenesisAccount, len(g.Alloc))
		for k, v := range g.Alloc {
			alloc[k.String()] = v
		}
		enc.Alloc = &alloc
	}

	if g.AllocStake != nil {
		allocStake := make(map[string]*GenesisStake, len(g.AllocStake))
		for k, v := range g.AllocStake {
			allocStake[k.String()] = v
		}
		enc.AllocStake = &allocStake
	}

	enc.Number = types.EncodeUint64(g.Number)
	enc.GasUsed = types.EncodeUint64(g.GasUsed)
	enc.ParentHash = g.ParentHash

	return json.Marshal(&enc)
}

// UnmarshalJSON implements the json interface
func (g *Genesis) UnmarshalJSON(data []byte) error {
	type Genesis struct {
		Nonce      *string                    `json:"nonce"`
		Timestamp  *string                    `json:"timestamp"`
		ExtraData  *string                    `json:"extraData"`
		GasLimit   *string                    `json:"gasLimit"`
		Difficulty *string                    `json:"difficulty"`
		Mixhash    *types.Hash                `json:"mixHash"`
		Coinbase   *types.Address             `json:"coinbase"`
		Alloc      map[string]*GenesisAccount `json:"alloc"`
		AllocStake map[string]*GenesisStake   `json:"allocStake"`
		Number     *string                    `json:"number"`
		GasUsed    *string                    `json:"gasUsed"`
		ParentHash *types.Hash                `json:"parentHash"`
	}

	var dec Genesis
	if err := json.Unmarshal(data, &dec); err != nil {
		return err
	}

	var err, subErr error
	parseError := func(field string, subErr error) {
		err = multierror.Append(err, fmt.Errorf("%s: %v", field, subErr))
	}

	nonce, subErr := types.ParseUint64orHex(dec.Nonce)
	if subErr != nil {
		parseError("nonce", subErr)
	}
	binary.BigEndian.PutUint64(g.Nonce[:], nonce)

	g.Timestamp, subErr = types.ParseUint64orHex(dec.Timestamp)
	if subErr != nil {
		parseError("timestamp", subErr)
	}

	if dec.ExtraData != nil {
		g.ExtraData, subErr = types.ParseBytes(dec.ExtraData)
		if subErr != nil {
			parseError("extradata", subErr)
		}
	}

	if dec.GasLimit == nil {
		return fmt.Errorf("field 'gaslimit' is required")
	}
	g.GasLimit, subErr = types.ParseUint64orHex(dec.GasLimit)
	if subErr != nil {
		parseError("gaslimit", subErr)
	}
	g.Difficulty, subErr = types.ParseUint64orHex(dec.Difficulty)
	if subErr != nil {
		parseError("difficulty", subErr)
	}

	if dec.Mixhash != nil {
		g.Mixhash = *dec.Mixhash
	}
	if dec.Coinbase != nil {
		g.Coinbase = *dec.Coinbase
	}
	if dec.Alloc != nil {
		g.Alloc = make(map[types.Address]*GenesisAccount, len(dec.Alloc))
		for k, v := range dec.Alloc {
			g.Alloc[types.StringToAddress(k)] = v
		}
	}
	if dec.AllocStake != nil {
		g.AllocStake = make(map[types.Address]*GenesisStake, len(dec.AllocStake))
		for k, v := range dec.AllocStake {
			g.AllocStake[types.StringToAddress(k)] = v
		}
	}

	g.Number, subErr = types.ParseUint64orHex(dec.Number)
	if subErr != nil {
		parseError("number", subErr)
	}
	g.GasUsed, subErr = types.ParseUint64orHex(dec.GasUsed)
	if subErr != nil {
		parseError("gasused", subErr)
	}

	if dec.ParentHash != nil {
		g.ParentHash = *dec.ParentHash
	}
	return err
}

// Genesis alloc

// GenesisAccount is an account in the state of the genesis block.
type GenesisAccount struct {
	Code       []byte                    `json:"code,omitempty"`
	Storage    map[types.Hash]types.Hash `json:"storage,omitempty"`
	Balance    *big.Int                  `json:"balance,omitempty"`
	Nonce      uint64                    `json:"nonce,omitempty"`
	PrivateKey []byte                    `json:"secretKey,omitempty"` // for tests
}

type GenesisStake struct {
	StakedBalance *big.Int `json:"stakedBalance,omitempty"`
}

type genesisStakeEncoder struct {
	StakedBalance *string `json:"stakedBalance"`
}

type genesisAccountEncoder struct {
	Code       *string                   `json:"code,omitempty"`
	Storage    map[types.Hash]types.Hash `json:"storage,omitempty"`
	Balance    *string                   `json:"balance"`
	Nonce      *string                   `json:"nonce,omitempty"`
	PrivateKey *string                   `json:"secretKey,omitempty"`
}

// ENCODING //

func (g *GenesisAccount) MarshalJSON() ([]byte, error) {
	obj := &genesisAccountEncoder{}
	if g.Code != nil {
		obj.Code = types.EncodeBytes(g.Code)
	}
	if len(g.Storage) != 0 {
		obj.Storage = g.Storage
	}
	if g.Balance != nil {
		obj.Balance = types.EncodeBigInt(g.Balance)
	}
	if g.Nonce != 0 {
		obj.Nonce = types.EncodeUint64(g.Nonce)
	}
	if g.PrivateKey != nil {
		obj.PrivateKey = types.EncodeBytes(g.PrivateKey)
	}
	return json.Marshal(obj)
}

func (g *GenesisStake) MarshalJSON() ([]byte, error) {
	obj := &genesisStakeEncoder{}
	if g.StakedBalance != nil {
		obj.StakedBalance = types.EncodeBigInt(g.StakedBalance)
	}
	return json.Marshal(obj)
}

// DECODING //

func (g *GenesisAccount) UnmarshalJSON(data []byte) error {
	type GenesisAccount struct {
		Code       *string                   `json:"code,omitempty"`
		Storage    map[types.Hash]types.Hash `json:"storage,omitempty"`
		Balance    *string                   `json:"balance"`
		Nonce      *string                   `json:"nonce,omitempty"`
		PrivateKey *string                   `json:"secretKey,omitempty"`
	}

	var dec GenesisAccount
	if err := json.Unmarshal(data, &dec); err != nil {
		return err
	}

	var err error
	var subErr error

	parseError := func(field string, subErr error) {
		err = multierror.Append(err, fmt.Errorf("%s: %v", field, subErr))
	}

	if dec.Code != nil {
		g.Code, subErr = types.ParseBytes(dec.Code)
		if subErr != nil {
			parseError("code", subErr)
		}
	}

	if dec.Storage != nil {
		g.Storage = dec.Storage
	}

	g.Balance, subErr = types.ParseUint256orHex(dec.Balance)
	if subErr != nil {
		parseError("balance", subErr)
	}

	g.Nonce, subErr = types.ParseUint64orHex(dec.Nonce)
	if subErr != nil {
		parseError("nonce", subErr)
	}

	if dec.PrivateKey != nil {
		g.PrivateKey, subErr = types.ParseBytes(dec.PrivateKey)
		if subErr != nil {
			parseError("privatekey", subErr)
		}
	}

	return err
}

func (g *GenesisStake) UnmarshalJSON(data []byte) error {
	type GenesisStake struct {
		StakedBalance *string `json:"stakedBalance"`
	}

	var dec GenesisStake
	if err := json.Unmarshal(data, &dec); err != nil {
		return err
	}

	var err error
	var subErr error

	parseError := func(field string, subErr error) {
		err = multierror.Append(err, fmt.Errorf("%s: %v", field, subErr))
	}

	g.StakedBalance, subErr = types.ParseUint256orHex(dec.StakedBalance)
	if subErr != nil {
		parseError("stakedBalance", subErr)
	}

	return err
}

// Import / Export

func Import(chain string) (*Chain, error) {
	c, err := ImportFromName(chain)
	if err == nil {
		return c, nil
	}

	return ImportFromFile(chain)
}

// ImportFromName imports a chain from the precompiled json chains (i.e. foundation)
func ImportFromName(chain string) (*Chain, error) {
	data, err := Asset("chain/chains/" + chain + ".json")
	if err != nil {
		return nil, err
	}

	return importChain(data)
}

// ImportFromFile imports a chain from a filepath
func ImportFromFile(filename string) (*Chain, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	return importChain(data)
}

func importChain(content []byte) (*Chain, error) {
	var chain *Chain
	if err := json.Unmarshal(content, &chain); err != nil {
		return nil, err
	}
	if engines := chain.Params.Engine; len(engines) != 1 {
		return nil, fmt.Errorf("Expected one consensus engine but found %d", len(engines))
	}

	return chain, nil
}
