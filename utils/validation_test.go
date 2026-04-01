package utils

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateAddressKshs(t *testing.T) {
	// Valid kshs_ address
	valid := "kshs_1zyb1s96twbtycqwgh1o6wsnpsksgdoohokikgjqjaz63pxnju457pz8tm3r"
	assert.Equal(t, true, ValidateAddress(valid))
	// Invalid: wrong length
	invalid := "kshs_1zyb1s96twbtycqwgh1o6wsnpsksgdoohokikgjqjaz63pxnju457pz8tm3ra"
	assert.Equal(t, false, ValidateAddress(invalid))
	// Invalid: bad checksum
	invalid = "kshs_1zyb1s96twbtycqwgh1o6wsnpsksgdoohokikgjqjaz63pxnju457pz8tm3rb"
	assert.Equal(t, false, ValidateAddress(invalid))
	// Invalid: wrong prefix
	invalid = "nano_1zyb1s96twbtycqwgh1o6wsnpsksgdoohokikgjqjaz63pxnju457pz8tm3r"
	assert.Equal(t, false, ValidateAddress(invalid))
	// Invalid: ban_ prefix
	invalid = "ban_1zyb1s96twbtycqwgh1o6wsnpsksgdoohokikgjqjaz63pxnju457pz8tm3r"
	assert.Equal(t, false, ValidateAddress(invalid))
}

func TestAddressToPub(t *testing.T) {
	address := "kshs_1zyb1s96twbtycqwgh1o6wsnpsksgdoohokikgjqjaz63pxnju457pz8tm3r"
	pub, err := AddressToPub(address)
	assert.Equal(t, nil, err)
	assert.Equal(t, "7fc9064e4d713af2afc73c1527334b665972eb57d65093a378a3e40dbb48ec43", hex.EncodeToString(pub))
}
