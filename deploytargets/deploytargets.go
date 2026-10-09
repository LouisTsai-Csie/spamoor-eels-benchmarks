// Package deploytargets populates a chain with the CREATE2 benchmark targets
// that the account-access scenario reads from.
package deploytargets

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/ethpandaops/spamoor/plugins/spamoor-eels-benchmarks/accountload"
	"github.com/ethpandaops/spamoor/scenario"
	"github.com/ethpandaops/spamoor/spamoor"
	"github.com/ethpandaops/spamoor/txbuilder"
	"github.com/ethpandaops/spamoor/txtypes"
)

// ScenarioOptions configures the target deployment.
type ScenarioOptions struct {
	TotalCount  uint64  `yaml:"total_count"`
	Throughput  uint64  `yaml:"throughput"`
	MaxPending  uint64  `yaml:"max_pending"`
	MaxWallets  uint64  `yaml:"max_wallets"`
	Rebroadcast uint64  `yaml:"rebroadcast"`
	BaseFee     float64 `yaml:"base_fee"`
	TipFee      float64 `yaml:"tip_fee"`
	BaseFeeWei  string  `yaml:"base_fee_wei"`
	TipFeeWei   string  `yaml:"tip_fee_wei"`
	StartSalt   uint64  `yaml:"start_salt"`
	GasLimit    uint64  `yaml:"gas_limit"`
	Timeout     string  `yaml:"timeout"`
	ClientGroup string  `yaml:"client_group"`
	LogTxs      bool    `yaml:"log_txs"`
}

// Scenario deploys one target per transaction, salt = start_salt + txIdx.
type Scenario struct {
	options    ScenarioOptions
	logger     *logrus.Entry
	walletPool *spamoor.WalletPool
}

// ScenarioName is the name used on the CLI and in the web UI.
var ScenarioName = "deploy-targets"

// ScenarioDefaultOptions is the starting configuration for the scenario.
var ScenarioDefaultOptions = ScenarioOptions{
	TotalCount:  accountload.TargetCount,
	Throughput:  1000,
	MaxPending:  0,
	MaxWallets:  0,
	Rebroadcast: 120,
	BaseFee:     20,
	TipFee:      2,
	StartSalt:   0,
	GasLimit:    300000,
	Timeout:     "",
	ClientGroup: "",
	LogTxs:      false,
}

// ScenarioDescriptor registers the scenario with spamoor.
var ScenarioDescriptor = scenario.Descriptor{
	Name:           ScenarioName,
	Description:    "Deploys the CREATE2 benchmark targets through the deterministic factory",
	DefaultOptions: ScenarioDefaultOptions,
	NewScenario:    newScenario,
}

func newScenario(logger logrus.FieldLogger) scenario.Scenario {
	return &Scenario{
		options: ScenarioDefaultOptions,
		logger:  logger.WithField("scenario", ScenarioName),
	}
}

// Flags registers the CLI flags for the scenario.
func (s *Scenario) Flags(flags *pflag.FlagSet) error {
	flags.Uint64VarP(&s.options.TotalCount, "count", "c", ScenarioDefaultOptions.TotalCount, "Number of targets to deploy")
	flags.Uint64VarP(&s.options.Throughput, "throughput", "t", ScenarioDefaultOptions.Throughput, "Deployments to submit per slot")
	flags.Uint64Var(&s.options.MaxPending, "max-pending", ScenarioDefaultOptions.MaxPending, "Maximum number of pending transactions")
	flags.Uint64Var(&s.options.MaxWallets, "max-wallets", ScenarioDefaultOptions.MaxWallets, "Maximum number of child wallets to use")
	flags.Uint64Var(&s.options.Rebroadcast, "rebroadcast", ScenarioDefaultOptions.Rebroadcast, "Enable reliable rebroadcast system")
	flags.Float64Var(&s.options.BaseFee, "basefee", ScenarioDefaultOptions.BaseFee, "Max fee per gas to use in transactions (in gwei)")
	flags.Float64Var(&s.options.TipFee, "tipfee", ScenarioDefaultOptions.TipFee, "Max tip per gas to use in transactions (in gwei)")
	flags.StringVar(&s.options.BaseFeeWei, "basefee-wei", "", "Max fee per gas in wei (overrides --basefee)")
	flags.StringVar(&s.options.TipFeeWei, "tipfee-wei", "", "Max tip per gas in wei (overrides --tipfee)")
	flags.Uint64Var(&s.options.StartSalt, "start-salt", ScenarioDefaultOptions.StartSalt, "First CREATE2 salt to deploy")
	flags.Uint64Var(&s.options.GasLimit, "gas-limit", ScenarioDefaultOptions.GasLimit, "Gas limit per deployment transaction")
	flags.StringVar(&s.options.Timeout, "timeout", ScenarioDefaultOptions.Timeout, "Timeout for the scenario (e.g. '1h', '30m')")
	flags.StringVar(&s.options.ClientGroup, "client-group", ScenarioDefaultOptions.ClientGroup, "Client group to use for sending transactions")
	flags.BoolVar(&s.options.LogTxs, "log-txs", ScenarioDefaultOptions.LogTxs, "Log all submitted transactions")
	return nil
}

// Init prepares the wallet pool and validates the configuration.
func (s *Scenario) Init(options *scenario.Options) error {
	s.walletPool = options.WalletPool

	if options.Config != "" {
		if err := scenario.ParseAndValidateConfig(&ScenarioDescriptor, options.Config, &s.options, s.logger); err != nil {
			return err
		}
	}

	walletCount := s.options.MaxWallets
	if walletCount == 0 {
		// Deployments are independent, so wallets only need to cover the
		// in-flight window rather than the whole run.
		walletCount = s.options.Throughput / 10
		if walletCount < 10 {
			walletCount = 10
		} else if walletCount > 500 {
			walletCount = 500
		}
	}
	s.walletPool.SetWalletCount(walletCount)

	if s.options.TotalCount == 0 && s.options.Throughput == 0 {
		return fmt.Errorf("neither total count nor throughput limit set, must define at least one of them")
	}

	return nil
}

// Run deploys one target per transaction.
func (s *Scenario) Run(ctx context.Context) error {
	s.logger.Infof("starting scenario: %s", ScenarioName)
	defer s.logger.Infof("scenario %s finished.", ScenarioName)

	maxPending := s.options.MaxPending
	if maxPending == 0 {
		maxPending = s.options.Throughput * 2
	}

	var timeout time.Duration
	if s.options.Timeout != "" {
		parsed, err := time.ParseDuration(s.options.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout value: %v", err)
		}
		timeout = parsed
	}

	last := s.options.StartSalt + s.options.TotalCount - 1
	s.logger.Infof("deploying salts %d..%d through factory %v",
		s.options.StartSalt, last, accountload.DeterministicFactory.Hex())

	return scenario.RunTransactionScenario(ctx, scenario.TransactionScenarioOptions{
		TotalCount: s.options.TotalCount,
		Throughput: s.options.Throughput,
		MaxPending: maxPending,
		Timeout:    timeout,
		WalletPool: s.walletPool,
		Logger:     s.logger,
		ProcessNextTxFn: func(ctx context.Context, params *scenario.ProcessNextTxParams) error {
			logger := s.logger
			receiptChan, tx, client, wallet, err := s.sendNextTransaction(ctx, params.TxIdx)
			if client != nil {
				logger = logger.WithField("rpc", client.GetName())
			}
			if wallet != nil {
				logger = logger.WithField("wallet", s.walletPool.GetWalletName(wallet.GetAddress()))
			}

			params.NotifySubmitted()
			params.OrderedLogCb(func() {
				salt := s.options.StartSalt + params.TxIdx
				if err != nil {
					logger.Warnf("could not send deployment: %v", err)
				} else if s.options.LogTxs {
					logger.Infof("deployed salt %d: %v", salt, tx.Hash().String())
				} else {
					logger.Debugf("deployed salt %d: %v", salt, tx.Hash().String())
				}
			})

			if _, waitErr := receiptChan.Wait(ctx); waitErr != nil {
				return waitErr
			}

			return err
		},
	})
}

// factoryCalldata builds the deterministic factory's raw `salt || initcode`
// payload. The proxy takes no function selector.
func factoryCalldata(salt uint64) []byte {
	data := make([]byte, 32+len(accountload.TargetInitcode))
	binary.BigEndian.PutUint64(data[24:32], salt)
	copy(data[32:], accountload.TargetInitcode)
	return data
}

func (s *Scenario) sendNextTransaction(ctx context.Context, txIdx uint64) (scenario.ReceiptChan, *txtypes.Transaction, *spamoor.Client, *spamoor.Wallet, error) {
	wallet := s.walletPool.GetWallet(spamoor.SelectWalletByIndex, int(txIdx))
	if wallet == nil {
		return nil, nil, nil, nil, scenario.ErrNoWallet
	}

	client := s.walletPool.GetClient(
		spamoor.WithClientSelectionMode(spamoor.SelectClientByIndex, int(txIdx)),
		spamoor.WithClientGroup(s.options.ClientGroup),
	)
	if client == nil {
		return nil, nil, client, wallet, scenario.ErrNoClients
	}

	if err := wallet.ResetNoncesIfNeeded(ctx, client); err != nil {
		return nil, nil, client, wallet, err
	}

	baseFeeWei, tipFeeWei := spamoor.ResolveFees(s.options.BaseFee, s.options.TipFee, s.options.BaseFeeWei, s.options.TipFeeWei)
	feeCap, tipCap, err := s.walletPool.GetSuggestedFees(client, baseFeeWei, tipFeeWei)
	if err != nil {
		return nil, nil, client, wallet, fmt.Errorf("failed to get suggested fees: %w", err)
	}

	factory := accountload.DeterministicFactory
	txData, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(feeCap),
		GasTipCap: uint256.MustFromBig(tipCap),
		Gas:       s.options.GasLimit,
		To:        &factory,
		Value:     uint256.NewInt(0),
		Data:      factoryCalldata(s.options.StartSalt + txIdx),
	})
	if err != nil {
		return nil, nil, client, wallet, err
	}

	tx, err := wallet.BuildDynamicFeeTx(txData)
	if err != nil {
		return nil, nil, client, wallet, err
	}

	receiptChan := make(scenario.ReceiptChan, 1)
	err = s.walletPool.GetTxPool().SendTransaction(ctx, wallet, tx, &spamoor.SendTransactionOptions{
		Client:      client,
		ClientGroup: s.options.ClientGroup,
		Rebroadcast: s.options.Rebroadcast > 0,
		OnComplete: func(tx *txtypes.Transaction, receipt *txtypes.Receipt, err error) {
			receiptChan <- receipt
		},
		OnConfirm: func(tx *txtypes.Transaction, receipt *txtypes.Receipt) {
			// The proxy returns the deployed address rather than creating the
			// contract itself, so a reverted receipt is the only signal that
			// the deployment failed.
			if receipt.Status != txtypes.ReceiptStatusSuccessful {
				s.logger.Warnf("deployment tx %v reverted (gasUsed %d of %d)",
					tx.Hash().Hex(), receipt.GasUsed, s.options.GasLimit)
			}
		},
		LogFn: spamoor.GetDefaultLogFn(s.logger, "", fmt.Sprintf("%6d", txIdx+1), tx),
	})
	if err != nil {
		wallet.MarkSkippedNonce(tx.Nonce())
		return nil, nil, client, wallet, err
	}

	return receiptChan, tx, client, wallet, nil
}
