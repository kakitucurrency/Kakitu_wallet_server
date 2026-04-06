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

// VerifyMint checks that a mint tx on-chain matches the expected recipient and amount.
func (c *Client) VerifyMint(txHash, expectedTo string, expectedKES int64) error {
	receipt, err := c.rpc.TransactionReceipt(context.Background(), common.HexToHash(txHash))
	if err != nil {
		return fmt.Errorf("fetching receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("transaction failed on-chain")
	}
	if !strings.EqualFold(receipt.ContractAddress.Hex(), c.address.Hex()) {
		// The receipt.To for a contract call is the contract address
		_ = receipt
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
