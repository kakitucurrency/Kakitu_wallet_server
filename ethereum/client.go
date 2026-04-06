package ethereum

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"k8s.io/klog/v2"
)

const (
	// Base mainnet chain ID
	BaseChainID = 8453

	// 18 decimal places — standard ERC20
	decimals = 18
)

// Client wraps an ethclient and the KSHS contract binding.
type Client struct {
	rpc      *ethclient.Client
	contract *bind.BoundContract
	address  common.Address
	key      *ecdsa.PrivateKey
	chainID  *big.Int
}

// New connects to Base via the RPC URL in env and loads the KSHS contract.
func New() (*Client, error) {
	rpcURL := utils.GetEnv("BASE_RPC_URL", "")
	if rpcURL == "" {
		return nil, fmt.Errorf("BASE_RPC_URL is not set")
	}

	contractAddr := utils.GetEnv("KSHS_CONTRACT_ADDRESS", "")
	if contractAddr == "" {
		return nil, fmt.Errorf("KSHS_CONTRACT_ADDRESS is not set")
	}

	minterKey := utils.GetEnv("MINTER_PRIVATE_KEY", "")
	if minterKey == "" {
		return nil, fmt.Errorf("MINTER_PRIVATE_KEY is not set")
	}

	ec, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to Base RPC: %w", err)
	}

	parsedABI, err := abi.JSON(strings.NewReader(KshsABI))
	if err != nil {
		return nil, fmt.Errorf("parsing KSHS ABI: %w", err)
	}

	addr := common.HexToAddress(contractAddr)
	contract := bind.NewBoundContract(addr, parsedABI, ec, ec, ec)

	privKey, err := crypto.HexToECDSA(strings.TrimPrefix(minterKey, "0x"))
	if err != nil {
		return nil, fmt.Errorf("loading minter private key: %w", err)
	}

	return &Client{
		rpc:      ec,
		contract: contract,
		address:  addr,
		key:      privKey,
		chainID:  big.NewInt(BaseChainID),
	}, nil
}

// MintKSHS mints `amountKES` KSHS (1 KES = 1 KSHS = 1e18 units) to `toAddress`.
// mpesaRef is the Daraja receipt number stored on-chain for audit.
func (c *Client) MintKSHS(toAddress, mpesaRef string, amountKES int64) (string, error) {
	to := common.HexToAddress(toAddress)
	amount := kesToWei(amountKES)

	opts, err := c.txOpts()
	if err != nil {
		return "", err
	}

	tx, err := c.contract.Transact(opts, "mint", to, amount, mpesaRef)
	if err != nil {
		return "", fmt.Errorf("mint tx failed: %w", err)
	}

	klog.Infof("Minted %d KSHS to %s | tx: %s | mpesaRef: %s", amountKES, toAddress, tx.Hash().Hex(), mpesaRef)
	return tx.Hash().Hex(), nil
}

// BurnKSHS burns `amountKES` KSHS from `fromAddress`.
// mpesaRef is the Daraja transaction ID for the corresponding B2C payout.
func (c *Client) BurnKSHS(fromAddress, mpesaRef string, amountKES int64) (string, error) {
	from := common.HexToAddress(fromAddress)
	amount := kesToWei(amountKES)

	opts, err := c.txOpts()
	if err != nil {
		return "", err
	}

	tx, err := c.contract.Transact(opts, "burn", from, amount, mpesaRef)
	if err != nil {
		return "", fmt.Errorf("burn tx failed: %w", err)
	}

	klog.Infof("Burned %d KSHS from %s | tx: %s | mpesaRef: %s", amountKES, fromAddress, tx.Hash().Hex(), mpesaRef)
	return tx.Hash().Hex(), nil
}

// BalanceOf returns the KSHS balance of an address in whole KES units.
func (c *Client) BalanceOf(address string) (int64, error) {
	addr := common.HexToAddress(address)
	var result []interface{}
	err := c.contract.Call(&bind.CallOpts{Context: context.Background()}, &result, "balanceOf", addr)
	if err != nil {
		return 0, fmt.Errorf("balanceOf call failed: %w", err)
	}
	bal := result[0].(*big.Int)
	return weiToKes(bal), nil
}

// VerifyBurn checks that a user-submitted burn tx on-chain matches the expected
// caller address and token amount. It decodes the transaction input data using the
// KSHS ABI and verifies that:
//   - the transaction succeeded
//   - the function called is burn(address,uint256,string)
//   - the `from` argument matches expectedFrom
//   - the `amount` argument matches expectedKES (converted to wei)
func (c *Client) VerifyBurn(txHash, expectedFrom string, expectedKES int64) error {
	hash := common.HexToHash(txHash)

	// Fetch the raw transaction to decode its input data.
	tx, _, err := c.rpc.TransactionByHash(context.Background(), hash)
	if err != nil {
		return fmt.Errorf("fetching transaction: %w", err)
	}

	// Also fetch the receipt to confirm the tx was mined successfully.
	receipt, err := c.rpc.TransactionReceipt(context.Background(), hash)
	if err != nil {
		return fmt.Errorf("fetching receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("transaction failed on-chain")
	}

	// Parse the ABI so we can unpack the input data.
	parsedABI, err := abi.JSON(strings.NewReader(KshsABI))
	if err != nil {
		return fmt.Errorf("parsing KSHS ABI: %w", err)
	}

	// The first 4 bytes of input are the method selector; the rest are the ABI-encoded args.
	input := tx.Data()
	if len(input) < 4 {
		return fmt.Errorf("transaction input too short to contain a method selector")
	}

	method, err := parsedABI.MethodById(input[:4])
	if err != nil {
		return fmt.Errorf("unrecognised method selector: %w", err)
	}
	if method.Name != "burn" {
		return fmt.Errorf("expected burn call, got %q", method.Name)
	}

	args, err := method.Inputs.Unpack(input[4:])
	if err != nil {
		return fmt.Errorf("unpacking burn arguments: %w", err)
	}
	// burn(address from, uint256 amount, string mpesaRef)
	if len(args) < 2 {
		return fmt.Errorf("burn call has fewer arguments than expected")
	}

	fromAddr, ok := args[0].(common.Address)
	if !ok {
		return fmt.Errorf("burn argument 0 is not an address")
	}
	if !strings.EqualFold(fromAddr.Hex(), common.HexToAddress(expectedFrom).Hex()) {
		return fmt.Errorf("burn from address %s does not match expected %s", fromAddr.Hex(), expectedFrom)
	}

	amountWei, ok := args[1].(*big.Int)
	if !ok {
		return fmt.Errorf("burn argument 1 is not a *big.Int")
	}
	expectedWei := kesToWei(expectedKES)
	if amountWei.Cmp(expectedWei) != 0 {
		return fmt.Errorf("burn amount %s wei does not match expected %d KES (%s wei)", amountWei.String(), expectedKES, expectedWei.String())
	}

	return nil
}

// VerifyTransferToTreasury checks that a user transferred the expected amount of
// KSHS to the treasury address. This is an alternative cash-out model where the
// user sends tokens to the treasury rather than burning them directly; the backend
// then calls BurnKSHS on their behalf.
func (c *Client) VerifyTransferToTreasury(txHash, expectedFrom, treasuryAddress string, expectedKES int64) error {
	hash := common.HexToHash(txHash)

	tx, _, err := c.rpc.TransactionByHash(context.Background(), hash)
	if err != nil {
		return fmt.Errorf("fetching transaction: %w", err)
	}

	receipt, err := c.rpc.TransactionReceipt(context.Background(), hash)
	if err != nil {
		return fmt.Errorf("fetching receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("transaction failed on-chain")
	}

	parsedABI, err := abi.JSON(strings.NewReader(KshsABI))
	if err != nil {
		return fmt.Errorf("parsing KSHS ABI: %w", err)
	}

	input := tx.Data()
	if len(input) < 4 {
		return fmt.Errorf("transaction input too short to contain a method selector")
	}

	method, err := parsedABI.MethodById(input[:4])
	if err != nil {
		return fmt.Errorf("unrecognised method selector: %w", err)
	}
	if method.Name != "transfer" {
		return fmt.Errorf("expected transfer call, got %q", method.Name)
	}

	args, err := method.Inputs.Unpack(input[4:])
	if err != nil {
		return fmt.Errorf("unpacking transfer arguments: %w", err)
	}
	// transfer(address to, uint256 value)
	if len(args) < 2 {
		return fmt.Errorf("transfer call has fewer arguments than expected")
	}

	toAddr, ok := args[0].(common.Address)
	if !ok {
		return fmt.Errorf("transfer argument 0 is not an address")
	}
	if !strings.EqualFold(toAddr.Hex(), common.HexToAddress(treasuryAddress).Hex()) {
		return fmt.Errorf("transfer recipient %s does not match treasury %s", toAddr.Hex(), treasuryAddress)
	}

	// Verify the tx sender matches expectedFrom using the transaction's From field.
	signer := types.LatestSignerForChainID(c.chainID)
	sender, err := types.Sender(signer, tx)
	if err != nil {
		return fmt.Errorf("recovering tx sender: %w", err)
	}
	if !strings.EqualFold(sender.Hex(), common.HexToAddress(expectedFrom).Hex()) {
		return fmt.Errorf("transfer sender %s does not match expected %s", sender.Hex(), expectedFrom)
	}

	amountWei, ok := args[1].(*big.Int)
	if !ok {
		return fmt.Errorf("transfer argument 1 is not a *big.Int")
	}
	expectedWei := kesToWei(expectedKES)
	if amountWei.Cmp(expectedWei) != 0 {
		return fmt.Errorf("transfer amount %s wei does not match expected %d KES (%s wei)", amountWei.String(), expectedKES, expectedWei.String())
	}

	return nil
}

// txOpts builds a signed transaction options object for the minter wallet.
func (c *Client) txOpts() (*bind.TransactOpts, error) {
	opts, err := bind.NewKeyedTransactorWithChainID(c.key, c.chainID)
	if err != nil {
		return nil, fmt.Errorf("building tx opts: %w", err)
	}
	opts.Context = context.Background()
	return opts, nil
}

// kesToWei converts whole KES units to ERC20 wei (1 KES = 1e18 units).
func kesToWei(kes int64) *big.Int {
	one := new(big.Int).Exp(big.NewInt(10), big.NewInt(decimals), nil)
	return new(big.Int).Mul(big.NewInt(kes), one)
}

// weiToKes converts ERC20 wei back to whole KES units.
func weiToKes(wei *big.Int) int64 {
	one := new(big.Int).Exp(big.NewInt(10), big.NewInt(decimals), nil)
	return new(big.Int).Div(wei, one).Int64()
}
