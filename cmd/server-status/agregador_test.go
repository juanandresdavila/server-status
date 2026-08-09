package main

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
)

func TestAgregadorPromediaYGuardaElPico(t *testing.T) {
	var a agregador
	base := time.Date(2026, 8, 8, 23, 12, 0, 0, time.UTC)

	a.add(model.HostSample{TS: base, CPUPctAvg: 10})
	a.add(model.HostSample{TS: base.Add(15 * time.Second), CPUPctAvg: 90})
	a.add(model.HostSample{TS: base.Add(30 * time.Second), CPUPctAvg: 20})

	got, ok := a.flush()
	if !ok {
		t.Fatal("flush devolvió ok=false con 3 muestras adentro")
	}
	if got.CPUPctAvg != 40 {
		t.Errorf("CPUPctAvg = %v, quería 40", got.CPUPctAvg)
	}
	// El máximo es el punto: un pico de 15 segundos desaparece en el promedio.
	if got.CPUPctMax != 90 {
		t.Errorf("CPUPctMax = %v, quería 90", got.CPUPctMax)
	}
	// Los valores instantáneos (disco, uptime, contadores) se toman de la última.
	if !got.TS.Equal(base.Add(30 * time.Second)) {
		t.Errorf("TS = %v, quería el de la última muestra", got.TS)
	}
}

func TestAgregadorVacioNoEmiteNada(t *testing.T) {
	var a agregador
	if _, ok := a.flush(); ok {
		t.Fatal("flush devolvió ok=true sin muestras")
	}
}

func TestFlushDejaElAgregadorLimpio(t *testing.T) {
	var a agregador
	a.add(model.HostSample{CPUPctAvg: 50})
	a.flush()
	if _, ok := a.flush(); ok {
		t.Fatal("el segundo flush devolvió ok=true: no se limpió")
	}
}
