package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRawToBigInt(t *testing.T) {
	raw := "1000000000000000000000000000000"

	asInt, err := RawToBigInt(raw)
	assert.Equal(t, nil, err)
	assert.Equal(t, "1000000000000000000000000000000", asInt.String())
}

func TestRawToKshs(t *testing.T) {
	raw := "1000000000000000000000000000000"

	converted, err := RawToKshs(raw, true)
	assert.Equal(t, nil, err)
	assert.Equal(t, 1.0, converted)

	raw = "5673567900000000000000000000000"
	converted, err = RawToKshs(raw, true)
	assert.Equal(t, nil, err)
	assert.Equal(t, 5.673568, converted)
}

func TestKshsToRaw(t *testing.T) {
	amount := 1.0

	converted := KshsToRaw(amount)
	assert.Equal(t, "1000000000000000000000000000000", converted)
}
