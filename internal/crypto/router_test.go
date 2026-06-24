package crypto

import (
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakePool returns a non-nil *pgxpool.Pool pointer using the zero value.
// We only compare pointer identity; no real DB connection is made.
func fakePool() *pgxpool.Pool {
	return new(pgxpool.Pool)
}

func TestRouter_DefaultFallback(t *testing.T) {
	def := fakePool()
	r := NewRouter(def)

	// Unknown region → default.
	if got := r.PoolFor("us-east-1"); got != def {
		t.Fatalf("PoolFor(unknown): want default pool, got %p", got)
	}
	// Empty string → default.
	if got := r.PoolFor(""); got != def {
		t.Fatalf("PoolFor(empty): want default pool, got %p", got)
	}
}

func TestRouter_RegisterHit(t *testing.T) {
	def := fakePool()
	eu := fakePool()
	r := NewRouter(def)
	r.Register("eu-west-1", eu)

	if got := r.PoolFor("eu-west-1"); got != eu {
		t.Fatalf("PoolFor(eu-west-1): want eu pool, got %p", got)
	}
	// Other regions still fall back to default.
	if got := r.PoolFor("ap-south-1"); got != def {
		t.Fatalf("PoolFor(unregistered): want default, got %p", got)
	}
}

func TestRouter_ConcurrentRace(t *testing.T) {
	def := fakePool()
	r := NewRouter(def)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			p := fakePool()
			// Alternate between Register and PoolFor so reads and writes race.
			if i%2 == 0 {
				r.Register("region-a", p)
			} else {
				_ = r.PoolFor("region-a")
			}
		}(i)
	}
	wg.Wait()
}
