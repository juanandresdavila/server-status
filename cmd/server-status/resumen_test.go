package main

import (
	"testing"
	"time"
)

func TestTocaResumenSoloUnaVezPorDia(t *testing.T) {
	loc := time.UTC
	var r recordatorioDiario

	ayer := time.Date(2026, 8, 8, 8, 0, 0, 0, loc)
	hoyTemprano := time.Date(2026, 8, 9, 7, 59, 0, 0, loc)
	hoyEnHora := time.Date(2026, 8, 9, 8, 0, 30, 0, loc)
	hoyMasTarde := time.Date(2026, 8, 9, 9, 0, 0, 0, loc)

	if !r.toca(ayer, 8) {
		t.Fatal("no disparó la primera vez")
	}
	if r.toca(hoyTemprano, 8) {
		t.Error("disparó antes de la hora")
	}
	if !r.toca(hoyEnHora, 8) {
		t.Error("no disparó en la hora del día siguiente")
	}
	// Ya salió hoy: no puede volver a salir.
	if r.toca(hoyMasTarde, 8) {
		t.Error("disparó dos veces el mismo día")
	}
}

// Si el proceso estuvo caído durante la hora del resumen, al volver más tarde
// tiene que mandarlo igual: es la señal de que el circuito está vivo.
func TestSiSePierdeLaHoraLoMandaIgualEseDia(t *testing.T) {
	var r recordatorioDiario
	r.toca(time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC), 8)

	tarde := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	if !r.toca(tarde, 8) {
		t.Error("no mandó el resumen del día por haberse perdido la hora exacta")
	}
}

// Recién arrancado, el recordatorio no puede disparar de madrugada: si el
// servicio se reinicia a las 3 AM, el resumen es del día anterior y ya salió.
func TestNoDisparaAntesDeLaHoraAunqueSeaLaPrimeraVez(t *testing.T) {
	var r recordatorioDiario
	if r.toca(time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC), 8) {
		t.Error("disparó a las 3 AM con la hora puesta en 8")
	}
}
