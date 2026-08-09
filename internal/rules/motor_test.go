package rules_test

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/rules"
)

// storeFalso implementa lo mínimo que el motor necesita, para testear la
// lógica sin base.
type storeFalso struct {
	abiertos map[string]model.Incidente
	proximo  int64
	cerrados []int64
}

func nuevoStoreFalso() *storeFalso {
	return &storeFalso{abiertos: map[string]model.Incidente{}, proximo: 1}
}

func (s *storeFalso) IncidentesAbiertos() ([]model.Incidente, error) {
	var out []model.Incidente
	for _, i := range s.abiertos {
		out = append(out, i)
	}
	return out, nil
}

func (s *storeFalso) AbrirIncidente(i model.Incidente) (int64, error) {
	i.ID = s.proximo
	s.proximo++
	s.abiertos[i.Sujeto] = i
	return i.ID, nil
}

func (s *storeFalso) CerrarIncidente(id int64, cuando time.Time) error {
	s.cerrados = append(s.cerrados, id)
	for k, v := range s.abiertos {
		if v.ID == id {
			delete(s.abiertos, k)
		}
	}
	return nil
}

func hostSano() model.HostSample {
	return model.HostSample{
		DiskUsedBytes: 10, DiskTotalBytes: 100,
		MemUsedBytes: 10, MemTotalBytes: 100,
		SwapUsedBytes: 0, SwapTotalBytes: 100,
		Load1: 0.1,
	}
}

func TestMotorAbreALaTerceraFalla(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	caido := []model.ProbeResult{{Servicio: "comm-tool", OK: false, Error: "HTTP 502"}}

	for i := range 2 {
		trs, err := m.EvaluarProbes(caido)
		if err != nil {
			t.Fatal(err)
		}
		if len(trs) != 0 {
			t.Fatalf("hubo transiciones en la falla %d: %+v", i+1, trs)
		}
		reloj.Advance(time.Minute)
	}

	trs, err := m.EvaluarProbes(caido)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 {
		t.Fatalf("hubo %d transiciones, quería 1", len(trs))
	}
	if trs[0].Tipo != rules.Abre || trs[0].Incidente.Sujeto != "service:comm-tool" {
		t.Errorf("transición = %+v", trs[0])
	}
	if trs[0].Incidente.Severidad != "critical" {
		t.Errorf("severidad = %q, quería critical", trs[0].Incidente.Severidad)
	}
	if len(st.abiertos) != 1 {
		t.Error("no quedó el incidente abierto en el store")
	}
}

func TestMotorCierraCuandoSeRecupera(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	caido := []model.ProbeResult{{Servicio: "comm-tool", OK: false, Error: "HTTP 502"}}
	sano := []model.ProbeResult{{Servicio: "comm-tool", OK: true, StatusCode: 200}}

	for range 3 {
		m.EvaluarProbes(caido)
		reloj.Advance(time.Minute)
	}
	m.EvaluarProbes(sano)
	reloj.Advance(time.Minute)

	trs, err := m.EvaluarProbes(sano)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 || trs[0].Tipo != rules.Cierra {
		t.Fatalf("transiciones = %+v, quería un Cierra", trs)
	}
	if trs[0].Incidente.CerradoEn == nil {
		t.Error("el incidente cerrado vino sin CerradoEn")
	}
	if len(st.abiertos) != 0 {
		t.Error("el incidente quedó abierto en el store")
	}
}

// Invariante del spec: reiniciar el proceso no reabre nada ni remanda nada.
// Un motor nuevo tiene que ver el incidente que ya está en la base y callarse.
func TestUnMotorNuevoNoReabreLoQueYaEstabaAbierto(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))

	primero := rules.NewMotor(st, reloj, rules.Defaults())
	caido := []model.ProbeResult{{Servicio: "comm-tool", OK: false, Error: "HTTP 502"}}
	for range 3 {
		primero.EvaluarProbes(caido)
		reloj.Advance(time.Minute)
	}
	if len(st.abiertos) != 1 {
		t.Fatalf("preparación: esperaba 1 incidente abierto, hay %d", len(st.abiertos))
	}

	// "Reinicio": motor nuevo, mismo store.
	segundo := rules.NewMotor(st, reloj, rules.Defaults())
	for range 5 {
		trs, err := segundo.EvaluarProbes(caido)
		if err != nil {
			t.Fatal(err)
		}
		if len(trs) != 0 {
			t.Fatalf("el motor nuevo emitió %+v sobre un incidente que ya estaba abierto", trs)
		}
		reloj.Advance(time.Minute)
	}
}

func TestMotorAbrePorUmbralDeDisco(t *testing.T) {
	st := nuevoStoreFalso()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	reloj := clock.NewFake(t0)
	m := rules.NewMotor(st, reloj, rules.Defaults())

	lleno := hostSano()
	lleno.DiskUsedBytes = 85

	if trs, _ := m.EvaluarHost(lleno); len(trs) != 0 {
		t.Fatalf("abrió sin esperar el sostenido: %+v", trs)
	}
	reloj.Advance(6 * time.Minute)

	trs, err := m.EvaluarHost(lleno)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 || trs[0].Incidente.Sujeto != "host:disk" {
		t.Fatalf("transiciones = %+v, quería un Abre de host:disk", trs)
	}
	if trs[0].Incidente.Severidad != "warning" {
		t.Errorf("severidad = %q, quería warning", trs[0].Incidente.Severidad)
	}
}

// Un host sano no puede generar ninguna transición, por más veces que se lo
// evalúe: si lo hiciera, el bot mandaría un mensaje por minuto.
func TestHostSanoNoGeneraNada(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	for range 20 {
		trs, err := m.EvaluarHost(hostSano())
		if err != nil {
			t.Fatal(err)
		}
		if len(trs) != 0 {
			t.Fatalf("un host sano generó %+v", trs)
		}
		reloj.Advance(time.Minute)
	}
}

// Varios servicios caídos a la vez tienen que dar un incidente cada uno,
// con sujetos distintos.
func TestDosServiciosCaidosDanDosIncidentes(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	caidos := []model.ProbeResult{
		{Servicio: "comm-tool", OK: false, Error: "HTTP 502"},
		{Servicio: "sitio", OK: false, Error: "timeout"},
	}

	var ultimas []rules.Cambio
	for range 3 {
		ultimas, _ = m.EvaluarProbes(caidos)
		reloj.Advance(time.Minute)
	}

	if len(ultimas) != 2 {
		t.Fatalf("hubo %d transiciones, quería 2", len(ultimas))
	}
	sujetos := map[string]bool{}
	for _, c := range ultimas {
		sujetos[c.Incidente.Sujeto] = true
	}
	if !sujetos["service:comm-tool"] || !sujetos["service:sitio"] {
		t.Errorf("sujetos = %v", sujetos)
	}
}
