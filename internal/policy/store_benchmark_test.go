package policy

import (
	"fmt"
	"path/filepath"
	"testing"
)

var benchmarkKeyResult *KeyConfig

func benchmarkStoreWithKeys(b *testing.B, count int) (*Store, string) {
	b.Helper()
	keys := make([]KeyConfig, 0, count)
	target := ""
	for index := 0; index < count; index++ {
		plain := fmt.Sprintf("cpa_benchmark_%04d", index)
		hash, err := HashKey(plain)
		if err != nil {
			b.Fatal(err)
		}
		keys = append(keys, KeyConfig{ID: fmt.Sprintf("key-%04d", index), Name: "benchmark", KeyHash: hash, Enabled: true})
		target = plain
	}
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(b.TempDir(), "state.json"), Keys: keys}); err != nil {
		b.Fatal(err)
	}
	return store, target
}

func BenchmarkFindBySecretIndexed1000(b *testing.B) {
	store, target := benchmarkStoreWithKeys(b, 1000)
	b.ResetTimer()
	for range b.N {
		benchmarkKeyResult = store.findBySecret(target)
	}
}

func BenchmarkFindBySecretLinear1000(b *testing.B) {
	store, target := benchmarkStoreWithKeys(b, 1000)
	b.ResetTimer()
	for range b.N {
		store.mu.RLock()
		var result *KeyConfig
		for _, key := range store.keys {
			if MatchHash(target, key.KeyHash) {
				copyKey := *key
				result = &copyKey
				break
			}
		}
		store.mu.RUnlock()
		benchmarkKeyResult = result
	}
}
