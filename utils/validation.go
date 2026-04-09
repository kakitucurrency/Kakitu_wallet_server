package utils

import (
	"encoding/base32"
	"errors"
	"regexp"

	"github.com/kakitucurrency/kakitu-wallet-server/utils/ed25519"
	"golang.org/x/crypto/blake2b"
)

// Kakitu uses the same non-standard base32 character set as NANO
const EncodeNano = "13456789abcdefghijkmnopqrstuwxyz"

var NanoEncoding = base32.NewEncoding(EncodeNano)

// kshs_ prefix, 65 chars total (5 prefix + 60 body)
const kshs_RegexStr = "(?:kshs)(?:_)(?:1|3)(?:[13456789abcdefghijkmnopqrstuwxyz]{59})"

var kshsRegex = regexp.MustCompile(kshs_RegexStr)

var ethAddressRegex = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// ValidateEthAddress returns true if s is a well-formed Ethereum address.
func ValidateEthAddress(s string) bool {
	return ethAddressRegex.MatchString(s)
}

// ValidateAddress returns true if a kshs_ address is valid.
func ValidateAddress(account string) bool {
	if !kshsRegex.MatchString(account) {
		return false
	}
	_, err := AddressToPub(account)
	return err == nil
}

// Convert address to a public key
func AddressToPub(account string) (public_key []byte, err error) {
	address := string(account)

	if len(address) >= 5 && address[:5] == "kshs_" {
		address = address[5:]
	} else {
		return nil, errors.New("Invalid address format: must start with kshs_")
	}

	// A valid kshs address is 65 bytes long (5 prefix + 60 body)
	// The 60-char body: 52 key chars + 8 checksum chars
	if len(address) == 60 {
		// The address string is 260 bits — pad to 280 bits with zeros.
		// (zeros are encoded as 1 in nano's base32 alphabet)
		key_b32nano := "1111" + address[0:52]
		input_checksum := address[52:]

		key_bytes, err := NanoEncoding.DecodeString(key_b32nano)
		if err != nil {
			return nil, err
		}
		// Strip off upper 24 bits (3 bytes): 20 padding + 4 unused
		key_bytes = key_bytes[3:]

		valid := NanoEncoding.EncodeToString(GetAddressChecksum(key_bytes)) == input_checksum
		if valid {
			return key_bytes, nil
		}
		return nil, errors.New("Invalid address checksum")
	}

	return nil, errors.New("Invalid address format")
}

func GetAddressChecksum(pub ed25519.PublicKey) []byte {
	hash, err := blake2b.New(5, nil)
	if err != nil {
		panic("Unable to create hash")
	}
	hash.Write(pub)
	return Reversed(hash.Sum(nil))
}

func Reversed(str []byte) (result []byte) {
	for i := len(str) - 1; i >= 0; i-- {
		result = append(result, str[i])
	}
	return result
}
