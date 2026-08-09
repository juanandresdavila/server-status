package clock_test

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
)

func TestFakeAvanzaSoloCuandoSeLoPide(t *testing.T) {
	inicio := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	f := clock.NewFake(inicio)

	if got := f.Now(); !got.Equal(inicio) {
		t.Fatalf("Now() = %v, quería %v", got, inicio)
	}

	f.Advance(90 * time.Second)

	quiero := inicio.Add(90 * time.Second)
	if got := f.Now(); !got.Equal(quiero) {
		t.Fatalf("después de Advance, Now() = %v, quería %v", got, quiero)
	}
}
