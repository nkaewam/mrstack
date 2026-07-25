package stack

import (
	"math/rand"
	"testing"
)

func TestDispositionPrecedenceIsOrderIndependent(t *testing.T) {
	all := []Finding{
		{Disposition: DispositionWaiting},
		{Disposition: DispositionHumanRequired},
		{Disposition: DispositionActionRequired},
		{Disposition: DispositionInvalid},
	}
	for seed := range 100 {
		shuffled := append([]Finding(nil), all...)
		rand.New(rand.NewSource(int64(seed))).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := ResolveDisposition(shuffled, DispositionReady); got != DispositionInvalid {
			t.Fatalf("seed %d: got %s", seed, got)
		}
	}
}

func TestDispositionPairwisePrecedence(t *testing.T) {
	order := []Disposition{
		DispositionComplete, DispositionReady, DispositionWaiting,
		DispositionHumanRequired, DispositionActionRequired, DispositionInvalid,
	}
	for i, lower := range order {
		for j, higher := range order {
			got := ResolveDisposition([]Finding{{Disposition: lower}, {Disposition: higher}}, DispositionComplete)
			want := order[max(i, j)]
			if got != want {
				t.Fatalf("%s + %s = %s, want %s", lower, higher, got, want)
			}
		}
	}
}
