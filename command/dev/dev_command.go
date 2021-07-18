package dev

import (
	"fmt"

	"github.com/0xPolygon/minimal/command/helper"
	"github.com/0xPolygon/minimal/minimal"
	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/cli"
)

// DevCommand is the command to show the version of the agent
type DevCommand struct {
	UI cli.Ui

	helper.Meta
}

// DefineFlags defines the command flags
func (d *DevCommand) DefineFlags() {
	if d.FlagMap == nil {
		// Flag map not initialized
		d.FlagMap = make(map[string]helper.FlagDescriptor)
	}

	d.FlagMap["log-level"] = helper.FlagDescriptor{
		Description: fmt.Sprintf("Sets the log level for console output. Default: %s", helper.DefaultConfig().LogLevel),
		Arguments: []string{
			"LOG_LEVEL",
		},
		FlagOptional: true,
	}

	d.FlagMap["premine"] = helper.FlagDescriptor{
		Description: fmt.Sprintf("Sets the premined accounts and balances. Default premined balance: %s", helper.DefaultPremineBalance),
		Arguments: []string{
			"ADDRESS:VALUE",
		},
		ArgumentsOptional: false,
		FlagOptional:      true,
	}

	d.FlagMap["prestake"] = helper.FlagDescriptor{
		Description: fmt.Sprintf("Sets the prestaked account balances. Default prestaked balance: %s", helper.DefaultPrestakeBalance),
		Arguments: []string{
			"ADDRESS:VALUE",
		},
		ArgumentsOptional: false,
		FlagOptional:      true,
	}

	d.FlagMap["dev-interval"] = helper.FlagDescriptor{
		Description: "Sets the client's dev notification interval. Default: 0",
		Arguments: []string{
			"DEV_INTERVAL",
		},
		FlagOptional: true,
	}
}

func (d *DevCommand) GetHelperText() string {
	return "\"Bypasses\" consensus and networking and starts a blockchain locally. " +
		"It starts a local node and mines every transaction in a separate block"
}

// Help implements the cli.Command interface
func (d *DevCommand) Help() string {
	d.DefineFlags()

	return helper.GenerateHelp(d.Synopsis(), helper.GenerateUsage(d.GetBaseCommand(), d.FlagMap), d.FlagMap)
}

// Synopsis implements the cli.Command interface
func (d *DevCommand) Synopsis() string {
	return d.GetHelperText()
}

func (d *DevCommand) GetBaseCommand() string {
	return "dev"
}

// Run implements the cli.Command interface
func (d *DevCommand) Run(args []string) int {
	conf, err := helper.BootstrapDevCommand(d.GetBaseCommand(), args)
	if err != nil {
		d.UI.Error(err.Error())

		return 1
	}

	config, err := conf.BuildConfig()
	if err != nil {
		d.UI.Error(err.Error())

		return 1
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:  "polygon-dev",
		Level: hclog.LevelFromString(conf.LogLevel),
	})

	server, err := minimal.NewServer(logger, config)
	if err != nil {
		d.UI.Error(err.Error())

		return 1
	}

	return helper.HandleSignals(server.Close, d.UI)
}
