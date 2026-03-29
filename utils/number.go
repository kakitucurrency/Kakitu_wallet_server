package utils

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
)

// KSHS uses the same raw unit scale as NANO: 1 KSHS = 10^30 raw
const rawPerKshsStr = "1000000000000000000000000000000"

var rawPerKshs, _ = new(big.Float).SetString(rawPerKshsStr)

const kshsPrecision = 1000000 // 0.000001 KSHS precision

// Raw to Big - converts raw amount to a big.Int
func RawToBigInt(raw string) (*big.Int, error) {
	rawBig, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil, errors.New(fmt.Sprintf("Unable to convert %s to big int", raw))
	}
	return rawBig, nil
}

// RawToKshs - Converts Raw amount to usable KSHS amount
func RawToKshs(raw string, truncate bool) (float64, error) {
	rawBig, ok := new(big.Float).SetString(raw)
	if !ok {
		return -1, errors.New(fmt.Sprintf("Unable to convert %s to float", raw))
	}
	asKshs := rawBig.Quo(rawBig, rawPerKshs)
	if !truncate {
		f, _ := asKshs.Float64()
		return f, nil
	}
	// Truncate precision beyond 0.000001
	asStr := asKshs.Text('f', 6)
	truncated, _ := strconv.ParseFloat(asStr, 64)
	return truncated, nil
}

// KshsToRaw - Converts KSHS amount to Raw amount
func KshsToRaw(kshs float64) string {
	kshsInt := int(kshs * 1000000)
	// 0.000001 KSHS = 10^24 raw
	kshsRaw, _ := new(big.Int).SetString("1000000000000000000000000", 10)
	res := kshsRaw.Mul(kshsRaw, big.NewInt(int64(kshsInt)))
	return fmt.Sprintf("%d", res)
}

// Aliases kept for compatibility with controller code that still calls RawToNano/NanoToRaw
func RawToNano(raw string, truncate bool) (float64, error) {
	return RawToKshs(raw, truncate)
}

func NanoToRaw(nano float64) string {
	return KshsToRaw(nano)
}
