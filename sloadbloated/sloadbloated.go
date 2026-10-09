// Package sloadbloated replays the execution-specs sload-bloated benchmark block
// against a storage-bloated, EIP-7702 delegated EOA.
package sloadbloated

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	"github.com/ethpandaops/spamoor/plugins/spamoor-eels-benchmarks/bloated"
	"github.com/ethpandaops/spamoor/scenario"
	"github.com/ethpandaops/spamoor/spamoor"
	"github.com/ethpandaops/spamoor/txbuilder"
	"github.com/ethpandaops/spamoor/txtypes"
)

// ScenarioOptions configures the benchmark replay.
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
	AuthorityKey    string  `yaml:"authority_key"`
	DeployGas       uint64  `yaml:"deploy_gas"`
	DeployerFunding string  `yaml:"deployer_funding"`
	Timeout         string  `yaml:"timeout"`
	ClientGroup     string  `yaml:"client_group"`
	LogTxs          bool    `yaml:"log_txs"`
	ExistingSlots   bool    `yaml:"existing_slots"`
}

// Scenario replays the benchmark block against the delegated authority.
type Scenario struct {
	options    ScenarioOptions
	logger     *logrus.Entry
	walletPool *spamoor.WalletPool

	authority   *bloated.Authority
	runtimeAddr common.Address
	setterAddr  common.Address
}

// ScenarioName is the name used on the CLI and in the web UI.
var ScenarioName = "sload-bloated"

// ScenarioDefaultOptions is the starting configuration for the scenario.
var ScenarioDefaultOptions = ScenarioOptions{
	TotalCount:      uint64(len(GasLimits)),
	Throughput:      uint64(len(GasLimits)),
	MaxPending:      0,
	MaxWallets:      0,
	Rebroadcast:     120,
	BaseFee:         20,
	TipFee:          2,
	DeployGas:       1000000,
	DeployerFunding: "500000000000000000",
	Timeout:         "",
	ClientGroup:     "",
	LogTxs:          false,
	ExistingSlots:   false,
}

// ScenarioDescriptor registers the scenario with spamoor.
var ScenarioDescriptor = scenario.Descriptor{
	Name:           ScenarioName,
	Description:    "Replays test_sload_bloated against a 7702-delegated, storage-bloated EOA",
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
	flags.StringVar(&s.options.AuthorityKey, "authority-key", "", "Private key of the storage-bloated EOA to delegate (required)")
	flags.Uint64Var(&s.options.DeployGas, "deploy-gas", ScenarioDefaultOptions.DeployGas, "Gas limit for the runtime deployment transaction")
	flags.StringVar(&s.options.DeployerFunding, "deployer-funding", ScenarioDefaultOptions.DeployerFunding, "Wei to fund the deployer wallet with")
	flags.StringVar(&s.options.Timeout, "timeout", ScenarioDefaultOptions.Timeout, "Timeout for the scenario (e.g. '1h', '30m')")
	flags.StringVar(&s.options.ClientGroup, "client-group", ScenarioDefaultOptions.ClientGroup, "Client group to use for sending transactions")
	flags.BoolVar(&s.options.LogTxs, "log-txs", ScenarioDefaultOptions.LogTxs, "Log all submitted transactions")
	flags.BoolVar(&s.options.ExistingSlots, "existing-slots", ScenarioDefaultOptions.ExistingSlots, "Target slots that already hold values")
	return nil
}

// Init prepares the wallet pool and loads the authority key.
func (s *Scenario) Init(options *scenario.Options) error {
	s.walletPool = options.WalletPool

	if options.Config != "" {
		if err := scenario.ParseAndValidateConfig(&ScenarioDescriptor, options.Config, &s.options, s.logger); err != nil {
			return err
		}
	}

	authority, err := bloated.LoadAuthority(s.options.AuthorityKey)
	if err != nil {
		return err
	}
	s.authority = authority

	walletCount := s.options.MaxWallets
	if walletCount == 0 {
		walletCount = s.options.Throughput
		if walletCount < 4 {
			walletCount = 4
		}
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

// Run deploys the runtime, delegates the authority to it, then replays.
func (s *Scenario) Run(ctx context.Context) error {
	s.logger.Infof("starting scenario: %s", ScenarioName)
	defer s.logger.Infof("scenario %s finished.", ScenarioName)

	deployer := s.walletPool.GetWellKnownWallet("deployer")
	if deployer == nil {
		return fmt.Errorf("deployer wallet not found")
	}
	client := s.walletPool.GetClient(
		spamoor.WithClientSelectionMode(spamoor.SelectClientByIndex, 0),
		spamoor.WithClientGroup(s.options.ClientGroup),
	)
	if client == nil {
		return scenario.ErrNoClients
	}
	baseFeeWei, tipFeeWei := spamoor.ResolveFees(s.options.BaseFee, s.options.TipFee, s.options.BaseFeeWei, s.options.TipFeeWei)
	feeCapBig, tipCapBig, err := s.walletPool.GetSuggestedFees(client, baseFeeWei, tipFeeWei)
	if err != nil {
		return fmt.Errorf("failed to get suggested fees: %w", err)
	}
	feeCap := uint256.MustFromBig(feeCapBig)
	tipCap := uint256.MustFromBig(tipCapBig)

	s.runtimeAddr, err = bloated.DeployContract(ctx, s.walletPool, deployer,
		client, sloadInitcode(), s.options.DeployGas, feeCap, tipCap)
	if err != nil {
		return fmt.Errorf("failed to deploy runtime: %w", err)
	}
	s.logger.Infof("deployed sload runtime at %v", s.runtimeAddr.Hex())

	// The cursor lives in slot 0, so the sweep must be anchored before the
	// runtime is delegated in: a stale cursor silently reads empty slots.
	s.setterAddr, err = bloated.DeployContract(ctx, s.walletPool, deployer,
		client, SetterInitcode, s.options.DeployGas, feeCap, tipCap)
	if err != nil {
		return fmt.Errorf("failed to deploy setter: %w", err)
	}
	if _, err = s.authority.Delegate(ctx, s.walletPool, deployer, client,
		s.setterAddr, s.startSlot(), feeCap, tipCap, s.logger); err != nil {
		return fmt.Errorf("failed to set slot 0: %w", err)
	}
	if _, err = s.authority.Delegate(ctx, s.walletPool, deployer, client,
		s.runtimeAddr, make([]byte, 32), feeCap, tipCap, s.logger); err != nil {
		return fmt.Errorf("failed to delegate runtime: %w", err)
	}
	if err = s.authority.VerifyDelegation(ctx, client, s.runtimeAddr); err != nil {
		return err
	}

	maxPending := s.options.MaxPending
	if maxPending == 0 {
		maxPending = s.options.Throughput * 2
	}
	var timeout time.Duration
	if s.options.Timeout != "" {
		parsed, perr := time.ParseDuration(s.options.Timeout)
		if perr != nil {
			return fmt.Errorf("invalid timeout value: %v", perr)
		}
		timeout = parsed
	}

	return scenario.RunTransactionScenario(ctx, scenario.TransactionScenarioOptions{
		TotalCount: s.options.TotalCount,
		Throughput: s.options.Throughput,
		MaxPending: maxPending,
		Timeout:    timeout,
		WalletPool: s.walletPool,
		Logger:     s.logger,
		ProcessNextTxFn: func(ctx context.Context, params *scenario.ProcessNextTxParams) error {
			logger := s.logger
			receiptChan, tx, c, wallet, terr := s.sendNextTransaction(ctx, params.TxIdx)
			if c != nil {
				logger = logger.WithField("rpc", c.GetName())
			}
			if wallet != nil {
				logger = logger.WithField("wallet", s.walletPool.GetWalletName(wallet.GetAddress()))
			}
			params.NotifySubmitted()
			params.OrderedLogCb(func() {
				if terr != nil {
					logger.Warnf("could not send transaction: %v", terr)
				} else if s.options.LogTxs {
					logger.Infof("sent tx #%6d: %v", params.TxIdx+1, tx.Hash().String())
				} else {
					logger.Debugf("sent tx #%6d: %v", params.TxIdx+1, tx.Hash().String())
				}
			})
			if _, werr := receiptChan.Wait(ctx); werr != nil {
				return werr
			}
			return terr
		},
	})
}

// startSlot returns the slot-0 cursor the sweep starts from.
func (s *Scenario) startSlot() []byte {
	if s.options.ExistingSlots {
		value := make([]byte, 32)
		value[31] = StartSlotExisting
		return value
	}
	return StartSlotNew
}

func sloadInitcode() []byte { return RuntimeInitcode }

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
		return nil, nil, client, wallet, err
	}

	// The loop is gas-bounded, so the only per-transaction variable is the
	// gas limit; the sweep position is carried in the authority's slot 0.
	gas := GasLimits[txIdx%uint64(len(GasLimits))]
	authority := s.authority.Address()
	txData, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: uint256.MustFromBig(feeCap),
		GasTipCap: uint256.MustFromBig(tipCap),
		Gas:       gas,
		To:        &authority,
		Value:     uint256.NewInt(0),
		Data:      nil,
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
		LogFn: spamoor.GetDefaultLogFn(s.logger, "", fmt.Sprintf("%6d", txIdx+1), tx),
	})
	if err != nil {
		wallet.MarkSkippedNonce(tx.Nonce())
		return nil, nil, client, wallet, err
	}
	return receiptChan, tx, client, wallet, nil
}
