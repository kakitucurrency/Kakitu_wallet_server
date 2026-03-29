package utils

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"

	"github.com/kakitucurrency/kakitu-wallet-server/models"
	"github.com/kakitucurrency/kakitu-wallet-server/utils/ed25519"
	"golang.org/x/crypto/blake2b"
)

// RPCRequester abstracts the subset of RPCClient methods used by SendKSHS,
// allowing utils to avoid a direct import of the net package (which already
// imports utils, which would create a cycle).
type RPCRequester interface {
	MakeRequest(request interface{}) ([]byte, error)
	MakeAccountInfoRequest(account string) (map[string]interface{}, error)
	WorkGenerate(hash string, difficultyMultiplier int) (string, error)
}

// fixedReader is an io.Reader that returns a fixed sequence of bytes.
// It is used to feed a known seed into ed25519.GenerateKey.
type fixedReader struct {
	data []byte
	pos  int
}

func (r *fixedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// TreasuryKeyPair derives the treasury keypair from the TREASURY_SEED env var
// (hex-encoded 32-byte seed) using Nano key derivation at index 0:
//
//	indexedSeed = blake2b-256(seed || uint32_be(0))
//
// The indexed seed is fed into ed25519.GenerateKey via a fixedReader.
func TreasuryKeyPair() (ed25519.PrivateKey, ed25519.PublicKey, error) {
	seedHex := os.Getenv("TREASURY_SEED")
	if seedHex == "" {
		return nil, nil, errors.New("treasury: TREASURY_SEED env var not set")
	}

	seedBytes, err := hex.DecodeString(seedHex)
	if err != nil {
		return nil, nil, fmt.Errorf("treasury: invalid TREASURY_SEED hex: %w", err)
	}
	if len(seedBytes) != 32 {
		return nil, nil, fmt.Errorf("treasury: TREASURY_SEED must be 32 bytes, got %d", len(seedBytes))
	}

	// Nano key derivation: blake2b-256(seed || index_uint32_be)
	indexBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(indexBuf, 0)

	h, err := blake2b.New256(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("treasury: failed to create blake2b hasher: %w", err)
	}
	h.Write(seedBytes)
	h.Write(indexBuf)
	indexedSeed := h.Sum(nil) // 32 bytes

	// Feed the indexed seed into GenerateKey via a fixedReader
	reader := &fixedReader{data: indexedSeed}
	pubKey, privKey, err := ed25519.GenerateKey(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("treasury: failed to generate keypair: %w", err)
	}

	return privKey, pubKey, nil
}

// PubKeyToAddress converts a 32-byte Ed25519 public key to a kshs_ address.
// It reverses the encoding used in AddressToPub.
func PubKeyToAddress(pubKey []byte) (string, error) {
	if len(pubKey) != 32 {
		return "", fmt.Errorf("treasury: public key must be 32 bytes, got %d", len(pubKey))
	}

	// Prepend 3 zero bytes to get 35 bytes (280 bits)
	padded := make([]byte, 35)
	copy(padded[3:], pubKey)

	// Encode to Nano base32 → 56 chars
	encoded := NanoEncoding.EncodeToString(padded)
	if len(encoded) != 56 {
		return "", fmt.Errorf("treasury: unexpected encoded length %d", len(encoded))
	}

	// Take [4:56] to skip the leading padding chars ("1111" equivalent)
	keyStr := encoded[4:56]

	// Compute 5-byte checksum, reversed, then base32-encode to 8 chars
	checksumBytes := GetAddressChecksum(pubKey)
	checksumStr := NanoEncoding.EncodeToString(checksumBytes)

	return "kshs_" + keyStr + checksumStr, nil
}

// kshsRawPerUnit is 10^30, the number of raw units in 1 KSHS.
var kshsRawPerUnit, _ = new(big.Int).SetString("1000000000000000000000000000000", 10)

// KesToRaw converts a KES amount string (e.g. "100" or "100.5") to raw units (multiply by 10^30).
func KesToRaw(amountKES string) (*big.Int, error) {
	f, ok := new(big.Float).SetString(amountKES)
	if !ok {
		return nil, fmt.Errorf("invalid amount: %s", amountKES)
	}
	rawPerUnit := new(big.Float).SetInt(kshsRawPerUnit)
	rawFloat := new(big.Float).Mul(f, rawPerUnit)
	rawInt, _ := rawFloat.Int(nil)
	return rawInt, nil
}

// SendKSHS constructs, signs, and broadcasts a KSHS send block from the treasury
// wallet to destAddress for amountRaw raw units.
func SendKSHS(rpcClient RPCRequester, destAddress string, amountRaw *big.Int) error {
	// Step a: derive treasury keypair
	privKey, pubKey, err := TreasuryKeyPair()
	if err != nil {
		return fmt.Errorf("treasury: keypair derivation failed: %w", err)
	}

	// Step b: derive treasury address
	treasuryAddress, err := PubKeyToAddress(pubKey)
	if err != nil {
		return fmt.Errorf("treasury: address derivation failed: %w", err)
	}

	// Step c: get treasury account info (frontier, representative, balance)
	accountInfo, err := rpcClient.MakeAccountInfoRequest(treasuryAddress)
	if err != nil {
		return fmt.Errorf("treasury: account_info request failed: %w", err)
	}

	frontierHex, ok := accountInfo["frontier"].(string)
	if !ok || frontierHex == "" {
		return errors.New("treasury: missing or invalid frontier in account_info")
	}
	representativeStr, ok := accountInfo["representative"].(string)
	if !ok || representativeStr == "" {
		return errors.New("treasury: missing or invalid representative in account_info")
	}
	balanceStr, ok := accountInfo["balance"].(string)
	if !ok || balanceStr == "" {
		return errors.New("treasury: missing or invalid balance in account_info")
	}

	currentBalance, ok := new(big.Int).SetString(balanceStr, 10)
	if !ok {
		return fmt.Errorf("treasury: cannot parse balance %q", balanceStr)
	}

	// Step d: compute new balance; error if insufficient
	if currentBalance.Cmp(amountRaw) < 0 {
		return fmt.Errorf("treasury: insufficient balance: have %s, need %s", currentBalance.String(), amountRaw.String())
	}
	newBalance := new(big.Int).Sub(currentBalance, amountRaw)

	// Step e: compute block hash using blake2b-256
	// Hash fields: preamble(32) + accountPub(32) + prevBytes(32) + repPub(32) + balanceBytes(16) + linkPub(32)
	blockHash, err := computeSendBlockHash(
		pubKey,
		frontierHex,
		representativeStr,
		newBalance,
		destAddress,
	)
	if err != nil {
		return fmt.Errorf("treasury: block hash computation failed: %w", err)
	}

	// Step f: sign the block hash
	signature := ed25519.Sign(privKey, blockHash)
	signatureHex := hex.EncodeToString(signature)

	// Step g: generate proof of work
	work, err := rpcClient.WorkGenerate(frontierHex, 64)
	if err != nil {
		return fmt.Errorf("treasury: work generation failed: %w", err)
	}

	// Resolve destination public key hex for the link field
	destPubBytes, err := AddressToPub(destAddress)
	if err != nil {
		return fmt.Errorf("treasury: invalid destination address: %w", err)
	}
	linkHex := hex.EncodeToString(destPubBytes)

	// Step h: build and broadcast the block
	jsonBlockTrue := true
	subtypeSend := "send"
	block := &models.ProcessJsonBlock{
		Type:           "state",
		Account:        treasuryAddress,
		Previous:       frontierHex,
		Representative: representativeStr,
		Balance:        newBalance.String(),
		Link:           linkHex,
		Signature:      signatureHex,
		Work:           &work,
	}
	processReq := map[string]interface{}{
		"action":     "process",
		"json_block": jsonBlockTrue,
		"subtype":    subtypeSend,
		"block":      block,
	}

	respBytes, err := rpcClient.MakeRequest(processReq)
	if err != nil {
		return fmt.Errorf("treasury: process request failed: %w", err)
	}

	// Step i: check for error key in response
	var respMap map[string]interface{}
	if err := json.Unmarshal(respBytes, &respMap); err != nil {
		return fmt.Errorf("treasury: failed to parse process response: %w", err)
	}
	if errMsg, hasErr := respMap["error"]; hasErr {
		return fmt.Errorf("treasury: node returned error: %v", errMsg)
	}

	return nil
}

// computeSendBlockHash computes the blake2b-256 hash of a Nano-protocol state block.
//
// Hash layout (160 bytes total):
//
//	[0:32]   preamble  – 31 zero bytes + 0x06
//	[32:64]  accountPub (32 bytes)
//	[64:96]  previous block hash (32 bytes, from hex)
//	[96:128] representative public key (32 bytes, from kshs_ address)
//	[128:144] balance (16 bytes, big-endian uint128)
//	[144:176] link – destination public key (32 bytes, from kshs_ address)
func computeSendBlockHash(
	accountPub []byte,
	frontierHex string,
	representativeAddr string,
	newBalance *big.Int,
	destAddress string,
) ([]byte, error) {
	// Preamble: 32 bytes, byte[31] = 6
	preamble := make([]byte, 32)
	preamble[31] = 6

	// Previous hash from hex
	prevBytes, err := hex.DecodeString(frontierHex)
	if err != nil {
		return nil, fmt.Errorf("invalid frontier hex: %w", err)
	}
	if len(prevBytes) != 32 {
		return nil, fmt.Errorf("frontier must be 32 bytes, got %d", len(prevBytes))
	}

	// Representative public key
	repPub, err := AddressToPub(representativeAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid representative address: %w", err)
	}

	// Balance as 16-byte big-endian uint128
	balanceBytes := make([]byte, 16)
	balancePadded := newBalance.Bytes()
	if len(balancePadded) > 16 {
		return nil, errors.New("balance exceeds 128 bits")
	}
	copy(balanceBytes[16-len(balancePadded):], balancePadded)

	// Link: destination public key
	linkPub, err := AddressToPub(destAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid destination address: %w", err)
	}

	// Hash everything with blake2b-256
	h, err := blake2b.New256(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create blake2b hasher: %w", err)
	}
	h.Write(preamble)
	h.Write(accountPub)
	h.Write(prevBytes)
	h.Write(repPub)
	h.Write(balanceBytes)
	h.Write(linkPub)

	return h.Sum(nil), nil
}
