package main

import (
	"crypto/rand"
	"math/big"
)

func randomBenchmarkDraw(size int) (int, error) {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(size)))
	if err != nil {
		return 0, err
	}
	return int(index.Int64()), nil
}
