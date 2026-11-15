package toolcheck

import (
	"crypto/sha256"
	"encoding/hex"

	lru "github.com/hashicorp/golang-lru/v2"
)

const compiledCacheSize = 64

var compiledCache, _ = lru.New[string, *Validator](compiledCacheSize)

func CompileCached(toolsRaw []byte) *Validator {
	sum := sha256.Sum256(toolsRaw)
	key := hex.EncodeToString(sum[:])
	if v, ok := compiledCache.Get(key); ok {
		return v
	}
	v := Compile(toolsRaw)
	compiledCache.Add(key, v)
	return v
}
