package rules_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/rules"
)

// storeFalso implementa lo mínimo que el motor necesita, para testear la
// lógica sin base.
type storeFalso struct {
	abiertos  map[string]model.Incidente
	historial []model.Incidente
	proximo   int64
	cerrados  []int64
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
	s.historial = append(s.historial, i)
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

func (s *storeFalso) CiclosEnVentana(sujeto string, desde time.Time) (int, error) {
	n := 0
	for _, i := range s.historial {
		if i.Sujeto == sujeto && !i.AbiertoEn.Before(desde) {
			n++
		}
	}
	return n, nil
}

func TestContainerCaidoAbreIncidente(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	muerto := []model.ContainerSample{
		{Name: "supabase-db", State: "exited", Health: "none"},
	}

	var ultimas []rules.Cambio
	for range 3 {
		ultimas, _ = m.EvaluarContainers(muerto)
		reloj.Advance(time.Minute)
	}

	if len(ultimas) != 1 {
		t.Fatalf("transiciones = %+v, quería 1", ultimas)
	}
	if ultimas[0].Incidente.Sujeto != "container:supabase-db" {
		t.Errorf("sujeto = %q", ultimas[0].Incidente.Sujeto)
	}
	if ultimas[0].Incidente.Tipo != "down" {
		t.Errorf("tipo = %q, quería down", ultimas[0].Incidente.Tipo)
	}
}

// Un container unhealthy está corriendo pero su healthcheck falla. Es un
// problema distinto de estar caído y se reporta como tal.
func TestContainerUnhealthyAbreIncidenteDeOtroTipo(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	enfermo := []model.ContainerSample{
		{Name: "comm-tool-db", State: "running", Health: "unhealthy"},
	}

	var ultimas []rules.Cambio
	for range 3 {
		ultimas, _ = m.EvaluarContainers(enfermo)
		reloj.Advance(time.Minute)
	}

	if len(ultimas) != 1 || ultimas[0].Incidente.Tipo != "unhealthy" {
		t.Fatalf("transiciones = %+v, quería un unhealthy", ultimas)
	}
}

// 'starting' es transitorio: un container que arranca no está roto.
func TestContainerStartingNoAbreNada(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	arrancando := []model.ContainerSample{
		{Name: "x", State: "running", Health: "starting"},
	}

	for range 5 {
		trs, _ := m.EvaluarContainers(arrancando)
		if len(trs) != 0 {
			t.Fatalf("un container 'starting' generó %+v", trs)
		}
		reloj.Advance(time.Minute)
	}
}

func TestContainersSanosNoGeneranNada(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	sanos := []model.ContainerSample{
		{Name: "a", State: "running", Health: "healthy"},
		{Name: "b", State: "running", Health: "none"},
	}

	for range 10 {
		trs, _ := m.EvaluarContainers(sanos)
		if len(trs) != 0 {
			t.Fatalf("containers sanos generaron %+v", trs)
		}
		reloj.Advance(time.Minute)
	}
}

// El escenario que rompe la política "uno al caer, uno al recuperarse":
// un servicio que rebota seis veces son doce mensajes.
func TestRebotarSeisVecesTerminaEnSilencio(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	caido := []model.ProbeResult{{Servicio: "x", OK: false, Error: "502"}}
	sano := []model.ProbeResult{{Servicio: "x", OK: true, StatusCode: 200}}

	mensajes := 0
	huboFlapping := false
	contar := func(trs []rules.Cambio) {
		mensajes += len(trs)
		for _, c := range trs {
			if c.Incidente.Tipo == "flapping" {
				huboFlapping = true
			}
		}
	}

	for ciclo := range 6 {
		for range 3 {
			trs, err := m.EvaluarProbes(caido)
			if err != nil {
				t.Fatalf("ciclo %d: %v", ciclo, err)
			}
			contar(trs)
			reloj.Advance(time.Minute)
		}
		for range 2 {
			trs, _ := m.EvaluarProbes(sano)
			contar(trs)
			reloj.Advance(time.Minute)
		}
	}

	if !huboFlapping {
		t.Error("seis ciclos en media hora y nunca emitió el aviso de inestabilidad")
	}
	// Sin amortiguación serían 12.
	if mensajes > 9 {
		t.Errorf("se emitieron %d mensajes en 6 rebotes: la amortiguación no frena", mensajes)
	}
}

// Un solo ciclo no dispara nada raro: la amortiguación no puede tapar
// el caso normal.
func TestUnCicloNormalNoDisparaFlapping(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	caido := []model.ProbeResult{{Servicio: "x", OK: false, Error: "502"}}
	sano := []model.ProbeResult{{Servicio: "x", OK: true, StatusCode: 200}}

	var todas []rules.Cambio
	for range 3 {
		trs, _ := m.EvaluarProbes(caido)
		todas = append(todas, trs...)
		reloj.Advance(time.Minute)
	}
	for range 2 {
		trs, _ := m.EvaluarProbes(sano)
		todas = append(todas, trs...)
		reloj.Advance(time.Minute)
	}

	if len(todas) != 2 {
		t.Fatalf("un ciclo normal dio %d mensajes, quería 2", len(todas))
	}
	for _, c := range todas {
		if c.Incidente.Tipo == "flapping" {
			t.Error("un solo ciclo disparó el aviso de inestabilidad")
		}
	}
}

// Las alertas de log reusan la política por umbral: 10 coincidencias en la
// ventana abren, 2 o menos cierran. No hace falta código nuevo de reglas.
func TestDiezErroresEnLaVentanaAbrenIncidente(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	trs, err := m.EvaluarLogs(map[string]rules.ConteoLog{
		"comm-tool": {Coincidencias: 12, Muestra: "ERROR conexion rechazada"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 {
		t.Fatalf("transiciones = %+v, quería 1", trs)
	}
	if trs[0].Incidente.Sujeto != "logs:comm-tool" {
		t.Errorf("sujeto = %q", trs[0].Incidente.Sujeto)
	}
	if trs[0].Incidente.Tipo != "log_pattern" {
		t.Errorf("tipo = %q", trs[0].Incidente.Tipo)
	}
	// El mensaje lleva el conteo y UNA muestra, nunca las doce.
	if !strings.Contains(trs[0].Incidente.Detalle, "12") ||
		!strings.Contains(trs[0].Incidente.Detalle, "conexion rechazada") {
		t.Errorf("detalle = %q", trs[0].Incidente.Detalle)
	}
}

// Un error suelto no despierta a nadie.
func TestUnErrorSueltoNoAbreNada(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	for range 10 {
		trs, _ := m.EvaluarLogs(map[string]rules.ConteoLog{"x": {Coincidencias: 1}})
		if len(trs) != 0 {
			t.Fatalf("un error por ventana generó %+v", trs)
		}
		reloj.Advance(time.Minute)
	}
}

// Histéresis: sin ella, un servicio que loguea un par de errores por ventana
// abriría y cerraría sin parar.
func TestBajarACeroCierraPeroDosNoReabre(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	m.EvaluarLogs(map[string]rules.ConteoLog{"x": {Coincidencias: 20, Muestra: "panic"}})
	reloj.Advance(time.Minute)

	// 5 está entre el cierre (2) y la apertura (10): no cierra todavía.
	if trs, _ := m.EvaluarLogs(map[string]rules.ConteoLog{"x": {Coincidencias: 5}}); len(trs) != 0 {
		t.Errorf("cerró con 5 coincidencias, el umbral de cierre es 2: %+v", trs)
	}
	reloj.Advance(time.Minute)

	trs, _ := m.EvaluarLogs(map[string]rules.ConteoLog{"x": {Coincidencias: 0}})
	if len(trs) != 1 || trs[0].Tipo != rules.Cierra {
		t.Errorf("no cerró con 0 coincidencias: %+v", trs)
	}
}
