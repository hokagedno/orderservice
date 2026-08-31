package id_test

import (
	"regexp"
	"testing"

	"github.com/hokagedno/orderservice/pkg/id"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUID_Format(t *testing.T) {
	for i := 0; i < 100; i++ {
		u, err := id.NewUUID()
		if err != nil {
			t.Fatalf("ошибка генерации: %v", err)
		}
		if !uuidV4.MatchString(u) {
			t.Fatalf("некорректный UUID v4: %q", u)
		}
	}
}

func TestNewUUID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		u := id.MustUUID()
		if _, dup := seen[u]; dup {
			t.Fatalf("коллизия на итерации %d: %s", i, u)
		}
		seen[u] = struct{}{}
	}
}

func BenchmarkNewUUID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = id.NewUUID()
	}
}
