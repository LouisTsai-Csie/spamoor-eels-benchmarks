// Package plugin is the spamoor entry point for the eels-benchmarks plugin.
package plugin

import (
	"github.com/ethpandaops/spamoor/plugins/eels-benchmarks/accountaccess"
	"github.com/ethpandaops/spamoor/scenario"
)

// PluginDescriptor is the symbol the spamoor plugin loader looks for.
var PluginDescriptor = scenario.PluginDescriptor{
	Name:        "eels-benchmarks",
	Description: "Replays execution-specs benchmark blocks as continuous load",
	Categories: []*scenario.Category{
		{
			Name:        "Benchmarks",
			Description: "execution-specs repricing benchmarks",
			Descriptors: []*scenario.Descriptor{
				&accountaccess.ScenarioDescriptor,
			},
		},
	},
}
