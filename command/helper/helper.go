package helper

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xPolygon/minimal/chain"
	helperFlags "github.com/0xPolygon/minimal/helper/flags"
	"github.com/0xPolygon/minimal/minimal"
	"github.com/0xPolygon/minimal/types"
	"github.com/mitchellh/cli"
	"github.com/ryanuber/columnize"
	"google.golang.org/grpc"
)

const (
	GenesisFileName        = "./genesis.json"
	DefaultChainName       = "example"
	DefaultChainID         = 100
	DefaultPremineBalance  = "0x3635C9ADC5DEA00000" // 1000 ETH
	DefaultPrestakeBalance = "0x8AC7230489E80000"   // 10 ETH
	DefaultConsensus       = "pow"
)

// FlagDescriptor contains the description elements for a command flag
type FlagDescriptor struct {
	Description       string   // Flag description
	Arguments         []string // Arguments list
	ArgumentsOptional bool     // Flag indicating if flag arguments are optional
	FlagOptional      bool
}

// GetDescription gets the flag description
func (fd *FlagDescriptor) GetDescription() string {
	return fd.Description
}

// GetArgumentsList gets the list of arguments for the flag
func (fd *FlagDescriptor) GetArgumentsList() []string {
	return fd.Arguments
}

// AreArgumentsOptional checks if the flag arguments are optional
func (fd *FlagDescriptor) AreArgumentsOptional() bool {
	return fd.ArgumentsOptional
}

// IsFlagOptional checks if the flag itself is optional
func (fd *FlagDescriptor) IsFlagOptional() bool {
	return fd.FlagOptional
}

// GenerateHelp is a utility function called by every command's Help() method
func GenerateHelp(synopsys string, usage string, flagMap map[string]FlagDescriptor) string {
	helpOutput := ""

	flagCounter := 0
	for flagEl, descriptor := range flagMap {
		helpOutput += GenerateFlagDesc(flagEl, descriptor) + "\n"
		flagCounter++

		if flagCounter < len(flagMap) {
			helpOutput += "\n"
		}
	}

	if len(flagMap) > 0 {
		return fmt.Sprintf("Description:\n\n%s\n\nUsage:\n\n\t%s\n\nFlags:\n\n%s", synopsys, usage, helpOutput)
	} else {
		return fmt.Sprintf("Description:\n\n%s\n\nUsage:\n\n\t%s\n", synopsys, usage)
	}
}

// GenerateFlagDesc generates the flag descriptions in a readable format
func GenerateFlagDesc(flagEl string, descriptor FlagDescriptor) string {
	// Generate the top row (with various flags)
	topRow := fmt.Sprintf("--%s", flagEl)

	argumentsOptional := descriptor.AreArgumentsOptional()
	argumentsList := descriptor.GetArgumentsList()

	argLength := len(argumentsList)

	if argLength > 0 {
		topRow += " "
		if argumentsOptional {
			topRow += "["
		}

		for argIndx, argument := range argumentsList {
			topRow += argument

			if argIndx < argLength-1 && argLength > 1 {
				topRow += " "
			}
		}

		if argumentsOptional {
			topRow += "]"
		}
	}

	// Generate the bottom description
	bottomRow := fmt.Sprintf("\t%s", descriptor.GetDescription())

	return fmt.Sprintf("%s\n%s", topRow, bottomRow)
}

// GenerateUsage is a helper function for generating command usage text
func GenerateUsage(baseCommand string, flagMap map[string]FlagDescriptor) string {
	output := baseCommand + " "

	maxFlagsPerLine := 3 // Just an arbitrary value, can be anything reasonable

	var addedFlags int // Keeps track of when a newline character needs to be inserted
	for flagEl, descriptor := range flagMap {
		// Open the flag bracket
		if descriptor.IsFlagOptional() {
			output += "["
		}

		// Add the actual flag name
		output += fmt.Sprintf("--%s", flagEl)

		// Open the argument bracket
		if descriptor.AreArgumentsOptional() {
			output += " ["
		}

		argumentsList := descriptor.GetArgumentsList()

		// Add the flag arguments list
		for argIndex, argument := range argumentsList {
			if argIndex == 0 {
				// Only called for the first argument
				output += " "
			}

			output += argument

			if argIndex < len(argumentsList)-1 {
				output += " "
			}
		}

		// Close the argument bracket
		if descriptor.AreArgumentsOptional() {
			output += "]"
		}

		// Close the flag bracket
		if descriptor.IsFlagOptional() {
			output += "]"
		}

		addedFlags++
		if addedFlags%maxFlagsPerLine == 0 {
			output += "\n\t"
		} else {
			output += " "
		}
	}

	return output
}

// HandleSignals is a helper method for handling signals sent to the console
// Like stop, error, etc.
func HandleSignals(closeFn func(), ui cli.Ui) int {
	signalCh := make(chan os.Signal, 4)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	sig := <-signalCh

	output := fmt.Sprintf("\n[SIGNAL] Caught signal: %v\n", sig)
	output += "Gracefully shutting down client...\n"

	ui.Output(output)

	// Call the Minimal server close callback
	gracefulCh := make(chan struct{})
	go func() {
		if closeFn != nil {
			closeFn()
		}
		close(gracefulCh)
	}()

	select {
	case <-signalCh:
		return 1
	case <-time.After(5 * time.Second):
		return 1
	case <-gracefulCh:
		return 0
	}
}

const (
	StatError   = "StatError"
	ExistsError = "ExistsError"
)

// GenesisGenError is a specific error type for generating genesis
type GenesisGenError struct {
	message   string
	errorType string
}

// GetMessage returns the message of the genesis generation error
func (g *GenesisGenError) GetMessage() string {
	return g.message
}

// GetType returns the type of the genesis generation error
func (g *GenesisGenError) GetType() string {
	return g.errorType
}

// VerifyGenesisExistence checks if the genesis file at the specified path is present
func VerifyGenesisExistence(genesisPath string) *GenesisGenError {
	_, err := os.Stat(genesisPath)
	if err != nil && !os.IsNotExist(err) {
		return &GenesisGenError{
			message:   fmt.Sprintf("failed to stat (%s): %v", genesisPath, err),
			errorType: StatError,
		}
	}
	if !os.IsNotExist(err) {
		return &GenesisGenError{
			message:   fmt.Sprintf("genesis file at path (%s) already exists", genesisPath),
			errorType: ExistsError,
		}
	}

	return nil
}

// FillPremineMap fills the premine map for the genesis.json file with passed in balances and accounts
func FillPremineMap(
	premineMap map[types.Address]*chain.GenesisAccount,
	premine helperFlags.ArrayFlags,
) error {
	for _, prem := range premine {
		var addr types.Address
		val := DefaultPremineBalance
		if indx := strings.Index(prem, ":"); indx != -1 {
			// <addr>:<balance>
			addr, val = types.StringToAddress(prem[:indx]), prem[indx+1:]
		} else {
			// <addr>
			addr = types.StringToAddress(prem)
		}

		amount, err := types.ParseUint256orHex(&val)
		if err != nil {
			return fmt.Errorf("failed to parse amount %s: %v", val, err)
		}
		premineMap[addr] = &chain.GenesisAccount{
			Balance: amount,
		}
	}

	return nil
}

// FillPrestakeMap fills the premine map for the genesis.json file with passed in account staked balances
func FillPrestakeMap(
	prestakeMap map[types.Address]*chain.GenesisStake,
	prestake helperFlags.ArrayFlags,
) error {
	for _, prem := range prestake {
		var addr types.Address
		val := DefaultPrestakeBalance
		if indx := strings.Index(prem, ":"); indx != -1 {
			// <addr>:<balance>
			addr, val = types.StringToAddress(prem[:indx]), prem[indx+1:]
		} else {
			// <addr>
			addr = types.StringToAddress(prem)
		}

		amount, err := types.ParseUint256orHex(&val)
		if err != nil {
			return fmt.Errorf("failed to parse amount %s: %v", val, err)
		}

		prestakeMap[addr] = &chain.GenesisStake{StakedBalance: amount}
	}

	return nil
}

// WriteGenesisToDisk writes the passed in configuration to a genesis.json file at the specified path
func WriteGenesisToDisk(chain *chain.Chain, genesisPath string) error {
	data, err := json.MarshalIndent(chain, "", "    ")
	if err != nil {
		return fmt.Errorf("failed to generate genesis: %w", err)
	}
	if err := ioutil.WriteFile(genesisPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write genesis: %w", err)
	}

	return nil
}

// generateDevGenesis generates a base dev genesis file with premined balances
func generateDevGenesis(chainName string, premine helperFlags.ArrayFlags, prestake helperFlags.ArrayFlags) error {
	genesisPath := filepath.Join(".", GenesisFileName)

	generateError := VerifyGenesisExistence(genesisPath)

	if generateError != nil {
		switch generateError.GetType() {
		case StatError:
			// Unable to stat file
			return errors.New(generateError.GetMessage())
		case ExistsError:
			// Not an error for the dev command, it shouldn't regenerate the genesis
			return nil
		}
	}

	cc := &chain.Chain{
		Name: chainName,
		Genesis: &chain.Genesis{
			GasLimit:   5000,
			Difficulty: 1,
			Alloc:      map[types.Address]*chain.GenesisAccount{},
			AllocStake: map[types.Address]*chain.GenesisStake{},
			ExtraData:  []byte{},
		},
		Params: &chain.Params{
			ChainID: 100,
			Forks:   chain.AllForksEnabled,
			Engine: map[string]interface{}{
				"dev": map[string]interface{}{},
			},
		},
		Bootnodes: []string{},
	}

	if err := FillPremineMap(cc.Genesis.Alloc, premine); err != nil {
		return err
	}

	if err := FillPrestakeMap(cc.Genesis.AllocStake, prestake); err != nil {
		return err
	}

	return WriteGenesisToDisk(cc, genesisPath)
}

// BootstrapDevCommand creates a config and generates the dev genesis file
func BootstrapDevCommand(baseCommand string, args []string) (*Config, error) {
	config := DefaultConfig()

	cliConfig := &Config{
		Network: &Network{
			NoDiscover: true,
			MaxPeers:   0,
		},
	}
	cliConfig.Seal = true
	cliConfig.Dev = true
	cliConfig.Chain = "genesis.json"

	flags := flag.NewFlagSet(baseCommand, flag.ContinueOnError)
	flags.Usage = func() {}

	var premine helperFlags.ArrayFlags
	var prestake helperFlags.ArrayFlags

	flags.StringVar(&cliConfig.LogLevel, "log-level", DefaultConfig().LogLevel, "")
	flags.Var(&premine, "premine", "")
	flags.Var(&prestake, "prestake", "")
	flags.Uint64Var(&cliConfig.DevInterval, "dev-interval", 0, "")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	if err := config.mergeConfigWith(cliConfig); err != nil {
		return nil, err
	}

	if err := generateDevGenesis(config.Chain, premine, prestake); err != nil {
		return nil, err
	}

	return config, nil
}

func ReadConfig(baseCommand string, args []string) (*Config, error) {
	config := DefaultConfig()

	cliConfig := &Config{
		Network: &Network{},
	}

	flags := flag.NewFlagSet(baseCommand, flag.ContinueOnError)
	flags.Usage = func() {}

	var configFile string
	flags.StringVar(&cliConfig.LogLevel, "log-level", "", "")
	flags.BoolVar(&cliConfig.Seal, "seal", false, "")
	flags.StringVar(&configFile, "config", "", "")
	flags.StringVar(&cliConfig.Chain, "chain", "", "")
	flags.StringVar(&cliConfig.DataDir, "data-dir", "", "")
	flags.StringVar(&cliConfig.GRPCAddr, "grpc", "", "")
	flags.StringVar(&cliConfig.JSONRPCAddr, "jsonrpc", "", "")
	flags.StringVar(&cliConfig.Join, "join", "", "")
	flags.StringVar(&cliConfig.Network.Addr, "libp2p", "", "")
	flags.StringVar(&cliConfig.Network.NatAddr, "nat", "", "the external IP address without port, as can be seen by peers")
	flags.BoolVar(&cliConfig.Network.NoDiscover, "no-discover", false, "")
	flags.Uint64Var(&cliConfig.Network.MaxPeers, "max-peers", 0, "")
	flags.BoolVar(&cliConfig.Dev, "dev", false, "")
	flags.Uint64Var(&cliConfig.DevInterval, "dev-interval", 0, "")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	if configFile != "" {
		// A config file has been passed in, parse it
		diskConfigFile, err := readConfigFile(configFile)
		if err != nil {
			return nil, err
		}

		if err := config.mergeConfigWith(diskConfigFile); err != nil {
			return nil, err
		}
	}

	if err := config.mergeConfigWith(cliConfig); err != nil {
		return nil, err
	}

	return config, nil
}

type HelpGenerator interface {
	DefineFlags()
}

// Meta is a helper utility for the commands
type Meta struct {
	UI   cli.Ui
	Addr string

	FlagMap map[string]FlagDescriptor
}

// DefineFlags sets global flags used by several commands
func (m *Meta) DefineFlags() {
	m.FlagMap = make(map[string]FlagDescriptor)

	m.FlagMap["grpc-address"] = FlagDescriptor{
		Description: fmt.Sprintf("Address of the gRPC API. Default: %s:%d", "127.0.0.1", minimal.DefaultGRPCPort),
		Arguments: []string{
			"GRPC_ADDRESS",
		},
		ArgumentsOptional: false,
		FlagOptional:      true,
	}
}

// FlagSet adds some default commands to handle grpc connections with the server
func (m *Meta) FlagSet(n string) *flag.FlagSet {
	f := flag.NewFlagSet(n, flag.ContinueOnError)
	f.StringVar(&m.Addr, "grpc-address", fmt.Sprintf("%s:%d", "127.0.0.1", minimal.DefaultGRPCPort), "")

	return f
}

// Conn returns a grpc connection
func (m *Meta) Conn() (*grpc.ClientConn, error) {
	conn, err := grpc.Dial(m.Addr, grpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %v", err)
	}

	return conn, nil
}

// OUTPUT FORMATTING //

// FormatList formats a list, using a specific blank value replacement
func FormatList(in []string) string {
	columnConf := columnize.DefaultConfig()
	columnConf.Empty = "<none>"

	return columnize.Format(in, columnConf)
}

// FormatKV formats key value pairs:
//
// Key = Value
//
// Key = <none>
func FormatKV(in []string) string {
	columnConf := columnize.DefaultConfig()
	columnConf.Empty = "<none>"
	columnConf.Glue = " = "

	return columnize.Format(in, columnConf)
}
