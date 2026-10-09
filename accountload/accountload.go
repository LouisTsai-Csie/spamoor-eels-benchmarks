// Package accountload replays the execution-specs test_account_access
// benchmark block for EXTCODEHASH, which reads only the account leaf's code hash.
// The chain must already hold the CREATE2 targets.
package accountload

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/ethpandaops/spamoor/scenario"
	"github.com/ethpandaops/spamoor/spamoor"
	"github.com/ethpandaops/spamoor/txbuilder"
	"github.com/ethpandaops/spamoor/txtypes"
)

// ScenarioOptions configures the account-access benchmark replay.
type ScenarioOptions struct {
	TotalCount      uint64  `yaml:"total_count"`
	Throughput      uint64  `yaml:"throughput"`
	MaxPending      uint64  `yaml:"max_pending"`
	MaxWallets      uint64  `yaml:"max_wallets"`
	Rebroadcast     uint64  `yaml:"rebroadcast"`
	BaseFee         float64 `yaml:"base_fee"`
	TipFee          float64 `yaml:"tip_fee"`
	BaseFeeWei      string  `yaml:"base_fee_wei"`
	TipFeeWei       string  `yaml:"tip_fee_wei"`
	TargetAddress   string  `yaml:"target_address"`
	DeployGas       uint64  `yaml:"deploy_gas"`
	DeployerFunding string  `yaml:"deployer_funding"`
	Timeout         string  `yaml:"timeout"`
	ClientGroup     string  `yaml:"client_group"`
	LogTxs          bool    `yaml:"log_txs"`
}

// Scenario replays TxSpecs in order, looping once the block is exhausted.
type Scenario struct {
	options    ScenarioOptions
	logger     *logrus.Entry
	walletPool *spamoor.WalletPool

	targetAddr common.Address
}

// ScenarioName is the name used on the CLI and in the web UI.
var ScenarioName = "account-load"

// ScenarioDefaultOptions is the starting configuration for the scenario.
var ScenarioDefaultOptions = ScenarioOptions{
	TotalCount:      0,
	Throughput:      uint64(len(TxSpecs)),
	MaxPending:      0,
	MaxWallets:      0,
	Rebroadcast:     120,
	BaseFee:         20,
	TipFee:          2,
	TargetAddress:   "",
	DeployGas:       500000,
	DeployerFunding: "500000000000000000",
	Timeout:         "",
	ClientGroup:     "",
	LogTxs:          false,
}

// ScenarioDescriptor registers the scenario with spamoor.
var ScenarioDescriptor = scenario.Descriptor{
	Name:           ScenarioName,
	Description:    "Replays the account-loading benchmark block (cold EXTCODEHASH over CREATE2 targets)",
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
	flags.Uint64VarP(&s.options.TotalCount, "count", "c", ScenarioDefaultOptions.TotalCount, "Total number of transactions to send")
	flags.Uint64VarP(&s.options.Throughput, "throughput", "t", ScenarioDefaultOptions.Throughput, "Number of transactions to send per slot")
	flags.Uint64Var(&s.options.MaxPending, "max-pending", ScenarioDefaultOptions.MaxPending, "Maximum number of pending transactions")
	flags.Uint64Var(&s.options.MaxWallets, "max-wallets", ScenarioDefaultOptions.MaxWallets, "Maximum number of child wallets to use")
	flags.Uint64Var(&s.options.Rebroadcast, "rebroadcast", ScenarioDefaultOptions.Rebroadcast, "Enable reliable rebroadcast system")
	flags.Float64Var(&s.options.BaseFee, "basefee", ScenarioDefaultOptions.BaseFee, "Max fee per gas to use in transactions (in gwei)")
	flags.Float64Var(&s.options.TipFee, "tipfee", ScenarioDefaultOptions.TipFee, "Max tip per gas to use in transactions (in gwei)")
	flags.StringVar(&s.options.BaseFeeWei, "basefee-wei", "", "Max fee per gas in wei (overrides --basefee)")
	flags.StringVar(&s.options.TipFeeWei, "tipfee-wei", "", "Max tip per gas in wei (overrides --tipfee)")
	flags.StringVar(&s.options.TargetAddress, "target-address", ScenarioDefaultOptions.TargetAddress, "Address of an already deployed run_code contract (skips deployment)")
	flags.Uint64Var(&s.options.DeployGas, "deploy-gas", ScenarioDefaultOptions.DeployGas, "Gas limit for the run_code deployment transaction")
	flags.StringVar(&s.options.DeployerFunding, "deployer-funding", ScenarioDefaultOptions.DeployerFunding, "Wei to fund the deployer wallet with")
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

	// Every transaction burns most of a block, so a handful of wallets is
	// plenty: more would only spread the refill budget thinner.
	walletCount := s.options.MaxWallets
	if walletCount == 0 {
		walletCount = uint64(len(TxSpecs))
	}
	s.walletPool.SetWalletCount(walletCount)

	funding, err := uint256.FromDecimal(s.options.DeployerFunding)
	if err != nil {
		return fmt.Errorf("invalid deployer-funding %q: %w", s.options.DeployerFunding, err)
	}
	s.walletPool.AddWellKnownWallet(&spamoor.WellKnownWalletConfig{
		Name:          "deployer",
		RefillAmount:  funding,
		RefillBalance: uint256.NewInt(0).Div(funding, uint256.NewInt(4)),
		VeryWellKnown: false,
	})

	if s.options.TotalCount == 0 && s.options.Throughput == 0 {
		return fmt.Errorf("neither total count nor throughput limit set, must define at least one of them")
	}

	return nil
}

// Run deploys the attack contract if needed, then replays the block.
func (s *Scenario) Run(ctx context.Context) error {
	s.logger.Infof("starting scenario: %s", ScenarioName)
	defer s.logger.Infof("scenario %s finished.", ScenarioName)

	if s.options.TargetAddress != "" {
		if !common.IsHexAddress(s.options.TargetAddress) {
			return fmt.Errorf("invalid target address: %s", s.options.TargetAddress)
		}
		s.targetAddr = common.HexToAddress(s.options.TargetAddress)
		s.logger.Infof("using existing run_code contract at %v", s.targetAddr.Hex())
	} else {
		receipt, err := s.deployRunCode(ctx)
		if err != nil {
			return fmt.Errorf("failed to deploy run_code: %w", err)
		}
		s.targetAddr = receipt.ContractAddress
		s.logger.Infof("deployed run_code at %v (block #%v)", s.targetAddr.Hex(), receipt.BlockNumber.Uint64())
	}

	maxPending := s.options.MaxPending
	if maxPending == 0 {
		maxPending = uint64(len(TxSpecs))
	}

	var timeout time.Duration
	if s.options.Timeout != "" {
		parsed, err := time.ParseDuration(s.options.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout value: %v", err)
		}
		timeout = parsed
		s.logger.Infof("timeout set to %v", timeout)
	}

	s.logger.Infof("replaying %d transactions per block cycle", len(TxSpecs))

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
			if tx != nil {
				logger = logger.WithField("nonce", tx.Nonce())
			}
			if wallet != nil {
				logger = logger.WithField("wallet", s.walletPool.GetWalletName(wallet.GetAddress()))
			}

			params.NotifySubmitted()
			params.OrderedLogCb(func() {
				specIdx := params.TxIdx % uint64(len(TxSpecs))
				if err != nil {
					logger.Warnf("could not send transaction: %v", err)
				} else if s.options.LogTxs {
					logger.Infof("sent tx #%6d (spec %2d): %v", params.TxIdx+1, specIdx, tx.Hash().String())
				} else {
					logger.Debugf("sent tx #%6d (spec %2d): %v", params.TxIdx+1, specIdx, tx.Hash().String())
				}
			})

			if _, waitErr := receiptChan.Wait(ctx); waitErr != nil {
				return waitErr
			}

			return err
		},
	})
}

func (s *Scenario) deployRunCode(ctx context.Context) (*txtypes.Receipt, error) {
	deployer := s.walletPool.GetWellKnownWallet("deployer")
	if deployer == nil {
		return nil, fmt.Errorf("deployer wallet not found")
	}

	client := s.walletPool.GetClient(
		spamoor.WithClientSelectionMode(spamoor.SelectClientByIndex, 0),
		spamoor.WithClientGroup(s.options.ClientGroup),
	)
	if client == nil {
		return nil, scenario.ErrNoClients
	}

	baseFeeWei, tipFeeWei := spamoor.ResolveFees(s.options.BaseFee, s.options.TipFee, s.options.BaseFeeWei, s.options.TipFeeWei)
	feeCap, tipCap, err := s.walletPool.GetSuggestedFees(client, baseFeeWei, tipFeeWei)
	if err != nil {
		return nil, fmt.Errorf("failed to get suggested fees: %w", err)
	}

	// Contract creation: no To address, initcode as the payload.
	txData, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(feeCap),
		GasTipCap: uint256.MustFromBig(tipCap),
		Gas:       s.options.DeployGas,
		To:        nil,
		Value:     uint256.NewInt(0),
		Data:      RunCodeInitcode,
	})
	if err != nil {
		return nil, err
	}

	tx, err := deployer.BuildDynamicFeeTx(txData)
	if err != nil {
		return nil, err
	}

	receipt, err := s.walletPool.GetTxPool().SendAndAwaitTransaction(ctx, deployer, tx, &spamoor.SendTransactionOptions{
		Client:      client,
		Rebroadcast: true,
	})
	if err != nil {
		return nil, fmt.Errorf("deployment transaction failed: %w", err)
	}
	if receipt.Status != txtypes.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("deployment transaction reverted")
	}

	return receipt, nil
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

	// The block is replayed in order; wrapping starts the next cycle.
	spec := TxSpecs[txIdx%uint64(len(TxSpecs))]

	txData, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(feeCap),
		GasTipCap: uint256.MustFromBig(tipCap),
		Gas:       spec.Gas,
		To:        &s.targetAddr,
		Value:     uint256.NewInt(0),
		Data:      spec.CallData,
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
			// gasUsed well below the limit means the loop exited early.
			if receipt.GasUsed < spec.Gas-spec.Gas/100 {
				s.logger.Warnf("tx %v used %d of %d gas - loop may have exited early",
					tx.Hash().Hex(), receipt.GasUsed, spec.Gas)
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
