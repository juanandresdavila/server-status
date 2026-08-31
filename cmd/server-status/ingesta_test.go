package main

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/collector/docker"
	"github.com/juanandresdavila/server-status/internal/logs"
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

	nuevas, cursor := nuevasLineas(crudas, base, "app", nil)
	if len(nuevas) != 2 {
		t.Fatalf("primera pasada trajo %d, quería 2", len(nuevas))
	}

	// Segunda pasada desde el cursor que dejó la primera: no puede repetir.
	otra, _ := nuevasLineas(crudas, cursor, "app", nil)
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

	_, cursor := nuevasLineas(crudas, base, "app", nil)
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

	nuevas, _ := nuevasLineas(crudas, base, "app", nil)
	if len(nuevas) != 1 || nuevas[0].Linea != "nueva" {
		t.Errorf("nuevas = %+v", nuevas)
	}
	if nuevas[0].Container != "app" {
		t.Errorf("no le puso el container: %+v", nuevas[0])
	}
}

// Lo que entra por el tick sale ya nivelado por las reglas. Sin esto, una
// regla arreglaría el pasado y el ruido volvería en el minuto siguiente.
func TestNuevasLineasAplicaLasReglas(t *testing.T) {
	base := time.Date(2026, 8, 31, 19, 0, 0, 0, time.UTC)
	const kong = `172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "curl/8.7.1"`
	crudas := []docker.LineaLog{{TS: base.Add(time.Second), Stream: "stdout", Linea: kong}}

	// Sin reglas es lo que dice el clasificador: un 4xx de un cliente de
	// verdad es WARN, y tiene que seguir siéndolo.
	sinReglas, _ := nuevasLineas(crudas, base, "supabase-kong", nil)
	if len(sinReglas) != 1 || sinReglas[0].Nivel != "WARN" {
		t.Fatalf("sin reglas quedó en %+v, quería WARN", sinReglas)
	}

	reglas := logs.Reglas{{Patron: "/auth/v1/health", Nivel: logs.Trace}}
	conReglas, _ := nuevasLineas(crudas, base, "supabase-kong", reglas)
	if len(conReglas) != 1 || conReglas[0].Nivel != "TRACE" {
		t.Errorf("con la regla puesta quedó en %+v, quería TRACE", conReglas)
	}

	// Una regla de otro container no toca esta línea.
	deOtro := logs.Reglas{{Patron: "/auth/v1/health", Container: "otro", Nivel: logs.Trace}}
	ajena, _ := nuevasLineas(crudas, base, "supabase-kong", deOtro)
	if len(ajena) != 1 || ajena[0].Nivel != "WARN" {
		t.Errorf("una regla de otro container cambió esta línea: %+v", ajena)
	}
}
