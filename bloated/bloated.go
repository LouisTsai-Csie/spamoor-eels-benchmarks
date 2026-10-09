// Package bloated holds the EIP-7702 plumbing the bloated-EOA benchmarks
// share: delegating the target EOA and resetting its slot-0 cursor.
package bloated

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/spamoor/spamoor"
	"github.com/ethpandaops/spamoor/txbuilder"
	"github.com/ethpandaops/spamoor/txtypes"
)

// AuthorityChainID is the chain id the execution-specs helper signs
// authorizations with. Zero makes an authorization valid on every chain
// (tests/benchmark/helper/delegation.py:134).
const AuthorityChainID = 0

// DelegationGas covers the authorization plus the one-SSTORE setter call.
// The helper adds a 500k buffer on top of the intrinsic cost.
const DelegationGas = 600000

// Authority is the storage-bloated EOA the benchmark targets. Its key signs
// the 7702 authorizations, so it must be supplied by the operator.
type Authority struct {
	Wallet *spamoor.Wallet
}

// LoadAuthority builds an Authority from a hex private key.
func LoadAuthority(privkey string) (*Authority, error) {
	if privkey == "" {
		return nil, fmt.Errorf("authority private key is required (--authority-key)")
	}
	key, address, err := spamoor.LoadPrivateKey(privkey)
	if err != nil {
		return nil, fmt.Errorf("invalid authority key: %w", err)
	}
	return &Authority{Wallet: spamoor.NewWallet(key, address)}, nil
}

// Address returns the bloated EOA's address.
func (a *Authority) Address() common.Address {
	return a.Wallet.GetAddress()
}

// Delegate points the authority at codeAddr and calls it with calldata in the
// same transaction, mirroring delegate_with_calldata. The sender pays; the
// authority only signs. The authority nonce is read from the chain each time
// rather than tracked locally: a stale nonce yields a silently inert
// authorization, with the transaction still succeeding.
func (a *Authority) Delegate(
	ctx context.Context,
	walletPool *spamoor.WalletPool,
	sender *spamoor.Wallet,
	client *spamoor.Client,
	codeAddr common.Address,
	calldata []byte,
	feeCap, tipCap *uint256.Int,
	logger *logrus.Entry,
) (*txtypes.Receipt, error) {
	nonce, err := client.GetEthClient().PendingNonceAt(ctx, a.Address())
	if err != nil {
		return nil, fmt.Errorf("failed to read authority nonce: %w", err)
	}

	authorization := txtypes.SetCodeAuthorization{
		ChainID: *uint256.NewInt(AuthorityChainID),
		Address: codeAddr,
		Nonce:   nonce,
	}
	authorization, err = txtypes.SignAuthorization(
		authorization, a.Wallet.GetPrivateKey())
	if err != nil {
		return nil, fmt.Errorf("failed to sign authorization: %w", err)
	}

	authority := a.Address()
	txData, err := txbuilder.SetCodeTx(&txbuilder.TxMetadata{
		GasFeeCap: feeCap,
		GasTipCap: tipCap,
		Gas:       DelegationGas,
		To:        &authority,
		Value:     uint256.NewInt(0),
		Data:      calldata,
		AuthList:  []txtypes.SetCodeAuthorization{authorization},
	})
	if err != nil {
		return nil, err
	}

	tx, err := sender.BuildSetCodeTx(txData)
	if err != nil {
		return nil, err
	}

	receipt, err := walletPool.GetTxPool().SendAndAwaitTransaction(
		ctx, sender, tx, &spamoor.SendTransactionOptions{
			Client:      client,
			Rebroadcast: true,
		})
	if err != nil {
		return nil, fmt.Errorf("delegation transaction failed: %w", err)
	}
	if receipt.Status != txtypes.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("delegation transaction reverted")
	}

	logger.Infof("delegated %v -> %v (authority nonce %d)",
		authority.Hex(), codeAddr.Hex(), nonce)
	return receipt, nil
}

// VerifyDelegation checks that the authority carries the EIP-7702 designator
// for codeAddr. A delegation that did not take effect leaves the EOA without
// code while the transaction still reports success.
func (a *Authority) VerifyDelegation(
	ctx context.Context, client *spamoor.Client, codeAddr common.Address,
) error {
	want := append([]byte{0xef, 0x01, 0x00}, codeAddr.Bytes()...)
	// A load-balanced endpoint can answer from a node that has not applied
	// the delegation block yet, so poll rather than trust the first read.
	var code []byte
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		code, err = client.GetEthClient().CodeAt(ctx, a.Address(), nil)
		if err == nil && bytes.Equal(code, want) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	if err != nil {
		return fmt.Errorf("failed to read authority code: %w", err)
	}
	return fmt.Errorf("authority %v is not delegated to %v (code %v)",
		a.Address().Hex(), codeAddr.Hex(), common.Bytes2Hex(code))
}

// DeployContract deploys initcode from wallet and returns its address.
func DeployContract(
	ctx context.Context,
	walletPool *spamoor.WalletPool,
	wallet *spamoor.Wallet,
	client *spamoor.Client,
	initcode []byte,
	gas uint64,
	feeCap, tipCap *uint256.Int,
) (common.Address, error) {
	txData, err := txbuilder.DynFeeTx(&txbuilder.TxMetadata{
		GasFeeCap: feeCap,
		GasTipCap: tipCap,
		Gas:       gas,
		To:        nil,
		Value:     uint256.NewInt(0),
		Data:      initcode,
	})
	if err != nil {
		return common.Address{}, err
	}
	tx, err := wallet.BuildDynamicFeeTx(txData)
	if err != nil {
		return common.Address{}, err
	}
	receipt, err := walletPool.GetTxPool().SendAndAwaitTransaction(
		ctx, wallet, tx, &spamoor.SendTransactionOptions{
			Client:      client,
			Rebroadcast: true,
		})
	if err != nil {
		return common.Address{}, err
	}
	if receipt.Status != txtypes.ReceiptStatusSuccessful {
		return common.Address{}, fmt.Errorf("deployment reverted")
	}
	return receipt.ContractAddress, nil
}
