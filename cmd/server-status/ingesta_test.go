package main

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/collector/docker"
)

// El bug que este test blinda: el cursor se guardaba con resolución de
// SEGUNDOS y se comparaba contra timestamps con NANOSEGUNDOS. Una línea de
// las 12:00:00.5 quedaba "después" del cursor 12:00:00 y se re-ingería en
// cada tick, para siempre.
func TestNoReingiereLineasConFraccionDeSegundo(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	crudas := []docker.LineaLog{
		{TS: base.Add(500 * time.Millisecond), Stream: "stdout", Linea: "con fracción"},
		{TS: base.Add(900 * time.Millisecond), Stream: "stdout", Linea: "otra"},
	}

	nuevas, cursor := nuevasLineas(crudas, base, "app")
	if len(nuevas) != 2 {
		t.Fatalf("primera pasada trajo %d, quería 2", len(nuevas))
	}

	// Segunda pasada desde el cursor que dejó la primera: no puede repetir.
	otra, _ := nuevasLineas(crudas, cursor, "app")
	if len(otra) != 0 {
		t.Errorf("re-ingirió %d líneas ya vistas: el cursor perdió la fracción de segundo", len(otra))
	}
}

func TestElCursorQuedaEnLaLineaMasNueva(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	crudas := []docker.LineaLog{
		{TS: base.Add(3 * time.Second), Linea: "tercera"},
		{TS: base.Add(time.Second), Linea: "primera"},
		{TS: base.Add(2 * time.Second), Linea: "segunda"},
	}

	_, cursor := nuevasLineas(crudas, base, "app")
	if !cursor.Equal(base.Add(3 * time.Second)) {
		t.Errorf("cursor = %v, quería la más nueva (+3s)", cursor)
	}
}

func TestDescartaLoQueYaEstabaAntesDelCursor(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	crudas := []docker.LineaLog{
		{TS: base.Add(-time.Minute), Linea: "vieja"},
		{TS: base.Add(time.Second), Linea: "nueva"},
	}

	nuevas, _ := nuevasLineas(crudas, base, "app")
	if len(nuevas) != 1 || nuevas[0].Linea != "nueva" {
		t.Errorf("nuevas = %+v", nuevas)
	}
	if nuevas[0].Container != "app" {
		t.Errorf("no le puso el container: %+v", nuevas[0])
	}
}
