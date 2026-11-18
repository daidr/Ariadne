package settings

import (
	"fmt"
	"testing"
)

// Resolve a commonly used model while many other catalog entries are saved.
// The catalog size must not cause the entire private configuration to be copied.
func BenchmarkResolveModelCatalogSize(b *testing.B) {
	for _, count := range []int{1, 20000} {
		b.Run(fmt.Sprintf("models-%d", count), func(b *testing.B) {
			config := Defaults()
			models := object(config["models"])
			entries := make([]any, count)
			for index := range count {
				entries[index] = Object{"id": fmt.Sprint("m", index), "provider": "p", "model": "model"}
			}
			models["models"] = entries
			models["providers"] = []any{Object{"id": "p", "kind": "anthropic", "name": "API", "settings": Object{"apiKey": "secret"}}}
			object(object(models["roles"])["thread"])["model"] = "m0"
			store := &Store{config: config}
			kinds := testModelKinds()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if resolved, err := store.ResolveModel("thread", "", kinds); err != nil || resolved == nil {
					b.Fatal(resolved, err)
				}
			}
		})
	}
}
