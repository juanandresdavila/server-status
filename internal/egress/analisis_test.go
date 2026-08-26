package egress_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/juanandresdavila/server-status/internal/egress"
)

// brazos arma un resumen sintético: por cada brazo, fallas sobre intentos.
func brazos(t *testing.T, def map[string][2]int) map[string]egress.ResumenBrazo {
	t.Helper()
	m := map[string]egress.ResumenBrazo{}
	for n, fi := range def {
		m[n] = egress.ResumenBrazo{Brazo: n, Fallas: fi[0], Intentos: fi[1], Resets: fi[0]}
	}
	return m
}

// Una fila de la tabla pre-registrada por caso. Si alguien afloja la regla, o
// la reordena, esto falla.
func TestConcluirSigueLaTablaPreRegistrada(t *testing.T) {
	casos := []struct {
		nombre string
		def    map[string][2]int
		quiero string
	}{
		{
			"v6 falla y v4 está limpio",
			map[string][2]int{
				"v6-ka": {20, 5000}, "v6-fresh": {18, 5000},
				"v4-ka": {0, 5000}, "v4-fresh": {0, 5000},
			},
			egress.VerdFamilia,
		},
		{
			"fallan los dos keep-alive y ninguno de los fresh",
			map[string][2]int{
				"v6-ka": {20, 5000}, "v4-ka": {18, 5000},
				"v6-fresh": {0, 5000}, "v4-fresh": {0, 5000},
			},
			egress.VerdReuso,
		},
		{
			"falla solo v6-ka",
			map[string][2]int{
				"v6-ka": {25, 5000}, "v4-ka": {0, 5000},
				"v6-fresh": {0, 5000}, "v4-fresh": {0, 5000},
			},
			egress.VerdAmbas,
		},
		{
			"fallan los cuatro parejo",
			map[string][2]int{
				"v6-ka": {20, 5000}, "v4-ka": {19, 5000},
				"v6-fresh": {21, 5000}, "v4-fresh": {20, 5000},
			},
			egress.VerdUplink,
		},
		{
			"cero fallas en todos",
			map[string][2]int{
				"v6-ka": {0, 5000}, "v4-ka": {0, 5000},
				"v6-fresh": {0, 5000}, "v4-fresh": {0, 5000},
			},
			egress.VerdSinFallas,
		},
		{
			"pocas fallas repartidas: no alcanza",
			map[string][2]int{
				"v6-ka": {3, 5000}, "v4-ka": {1, 5000},
				"v6-fresh": {2, 5000}, "v4-fresh": {0, 5000},
			},
			egress.VerdNoConcluye,
		},
		{
			"falta un brazo",
			map[string][2]int{
				"v6-ka": {20, 5000}, "v4-ka": {0, 5000}, "v6-fresh": {0, 5000},
			},
			egress.VerdNoConcluye,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got := egress.Concluir(brazos(t, c.def))
			if got.Codigo != c.quiero {
				t.Errorf("Concluir = %q (%s), quería %q", got.Codigo, got.Detalle, c.quiero)
			}
		})
	}
}

// Nueve fallas con v4 en cero NO alcanzan: el umbral pre-registrado pide diez.
// Sin este test el umbral se puede aflojar sin que se note.
func TestConcluirRespetaElMinimoDeEventos(t *testing.T) {
	m := brazos(t, map[string][2]int{
		"v6-ka": {9, 5000}, "v6-fresh": {0, 5000},
		"v4-ka": {0, 5000}, "v4-fresh": {0, 5000},
	})
	if got := egress.Concluir(m); got.Codigo != egress.VerdNoConcluye {
		t.Errorf("Concluir = %q (%s), quería %q: 9 eventos están por debajo del mínimo de %d",
			got.Codigo, got.Detalle, egress.VerdNoConcluye, egress.EventosMinimos)
	}
}

// El smoke test local lo destapó: sin IPv6 en la máquina, los cuatro intentos
// por v6 fallan con "no route to host" y la regla dictaminaba h1-familia. Eso no
// es "la familia tiene blips", es "no hay salida por esa familia", y confundirlo
// sería concluir el experimento desde una máquina mal configurada.
func TestConcluirNoConfundeUnaFamiliaSinSalidaConBlips(t *testing.T) {
	m := brazos(t, map[string][2]int{
		"v6-ka": {60, 60}, "v6-fresh": {60, 60},
		"v4-ka": {0, 60}, "v4-fresh": {0, 60},
	})
	got := egress.Concluir(m)
	if got.Codigo != egress.VerdBrazoCaido {
		t.Fatalf("Concluir = %q (%s), quería %q", got.Codigo, got.Detalle, egress.VerdBrazoCaido)
	}
}

// Pero el guardia no puede tapar el resultado que sí interesa: una tasa de
// blips real anda por el 0,4 % y tiene que seguir llegando al 2×2.
func TestElGuardiaDeBrazoCaidoNoTapaUnaTasaDeBlips(t *testing.T) {
	m := brazos(t, map[string][2]int{
		"v6-ka": {20, 5000}, "v6-fresh": {18, 5000},
		"v4-ka": {0, 5000}, "v4-fresh": {0, 5000},
	})
	if got := egress.Concluir(m); got.Codigo != egress.VerdFamilia {
		t.Fatalf("Concluir = %q (%s), quería %q", got.Codigo, got.Detalle, egress.VerdFamilia)
	}
}

// Un brazo que salió por la familia equivocada invalida todo, aunque los
// números den lindos. Es el guardia contra medir otra cosa.
func TestConcluirCantaElInstrumentoInfiel(t *testing.T) {
	m := brazos(t, map[string][2]int{
		"v6-ka": {20, 5000}, "v6-fresh": {18, 5000},
		"v4-ka": {0, 5000}, "v4-fresh": {0, 5000},
	})
	roto := m["v6-ka"]
	roto.Inconsistentes = 1
	m["v6-ka"] = roto

	got := egress.Concluir(m)
	if got.Codigo != egress.VerdInstrumento {
		t.Fatalf("Concluir = %q (%s), quería %q", got.Codigo, got.Detalle, egress.VerdInstrumento)
	}
}

func TestResumirCuentaPorClaseYDetectaLaFamiliaEquivocada(t *testing.T) {
	rs := []egress.Registro{
		{Brazo: "v6-ka", Red: "tcp6", Clase: egress.ClaseOK, Remota: "[2606:4700::1]:443"},
		{Brazo: "v6-ka", Red: "tcp6", Clase: egress.ClaseReset, Remota: "[2606:4700::1]:443"},
		{Brazo: "v6-ka", Red: "tcp6", Clase: egress.ClaseTimeout},
		{Brazo: "v6-ka", Red: "tcp6", Clase: egress.ClaseDNS},
		// éste salió por v4 estando en un brazo v6: no puede pasar
		{Brazo: "v6-ka", Red: "tcp6", Clase: egress.ClaseOK, Remota: "198.51.100.7:443"},
		{Brazo: "v4-ka", Red: "tcp4", Clase: egress.ClaseOK, Remota: "198.51.100.7:443"},
	}

	m := egress.Resumir(rs)

	v6 := m["v6-ka"]
	if v6.Intentos != 5 || v6.Fallas != 3 {
		t.Errorf("v6-ka: intentos=%d fallas=%d, quería 5 y 3", v6.Intentos, v6.Fallas)
	}
	if v6.Resets != 1 || v6.Timeouts != 1 || v6.Otras != 1 {
		t.Errorf("v6-ka: resets=%d timeouts=%d otras=%d, quería 1/1/1", v6.Resets, v6.Timeouts, v6.Otras)
	}
	if v6.Inconsistentes != 1 {
		t.Errorf("v6-ka: inconsistentes=%d, quería 1", v6.Inconsistentes)
	}
	if v4 := m["v4-ka"]; v4.Inconsistentes != 0 {
		t.Errorf("v4-ka: inconsistentes=%d, quería 0", v4.Inconsistentes)
	}
}

// Perder la última línea porque el proceso murió a mitad de un write no puede
// costar el experimento entero.
func TestLeerJSONLSaltaLineasRotas(t *testing.T) {
	entrada := `{"ts":"2026-08-26T20:00:00Z","brazo":"v6-ka","red":"tcp6","clase":"ok"}
{"ts":"2026-08-26T20:01:00Z","brazo":"v6-ka","red":"tcp6","clase":"reset"}
{"ts":"2026-08-26T20:02:0`

	rs, rotas, err := egress.LeerJSONL(strings.NewReader(entrada))
	if err != nil {
		t.Fatalf("LeerJSONL: %v", err)
	}
	if len(rs) != 2 {
		t.Errorf("leyó %d registros, quería 2", len(rs))
	}
	if rotas != 1 {
		t.Errorf("rotas = %d, quería 1", rotas)
	}
}

func TestDesempate30sSeparaCuandoElCortoNoFalla(t *testing.T) {
	m := brazos(t, map[string][2]int{"v6-ka": {25, 5000}, "v6-ka-30s": {0, 10000}})
	got := egress.Desempate30s(m)
	if !strings.Contains(got, "entre 30 y 60 s") {
		t.Errorf("Desempate30s = %q, quería que localizara la expiración", got)
	}
}

func TestDesempate30sNoConcluyeSiLosDosFallan(t *testing.T) {
	m := brazos(t, map[string][2]int{"v6-ka": {25, 5000}, "v6-ka-30s": {50, 10000}})
	got := egress.Desempate30s(m)
	if strings.Contains(got, "entre 30 y 60 s") {
		t.Errorf("Desempate30s = %q: los dos fallan igual, no puede localizar nada", got)
	}
}

// --- Enmienda del 26/08/2026: la unidad de análisis es el tick, no el intento.

// Un blip corta los cinco destinos en el mismo segundo. Contarlos como cinco
// observaciones independientes le da a Fisher cinco veces la evidencia que hay.
func TestResumirPorTickCuentaUnBlipUnaSolaVez(t *testing.T) {
	var rs []egress.Registro
	// un tick entero fallado: cinco destinos, mismo segundo
	for _, d := range []string{"a", "b", "c", "d", "e"} {
		rs = append(rs, egress.Registro{
			TS: "2026-08-26T21:03:30.021Z", Brazo: "v6-ka-30s", Red: "tcp6",
			Destino: d, Clase: egress.ClaseReset, Remota: "[2606:4700::1]:443",
		})
	}
	// y un tick sano
	for _, d := range []string{"a", "b", "c", "d", "e"} {
		rs = append(rs, egress.Registro{
			TS: "2026-08-26T21:04:00.021Z", Brazo: "v6-ka-30s", Red: "tcp6",
			Destino: d, Clase: egress.ClaseOK, Remota: "[2606:4700::1]:443",
		})
	}

	porIntento := egress.Resumir(rs)["v6-ka-30s"]
	if porIntento.Fallas != 5 || porIntento.Intentos != 10 {
		t.Fatalf("por intento: %d/%d, quería 5/10", porIntento.Fallas, porIntento.Intentos)
	}

	porTick := egress.ResumirPorTick(rs, false)["v6-ka-30s"]
	if porTick.Fallas != 1 || porTick.Intentos != 2 {
		t.Errorf("por tick: %d/%d, quería 1/2 — el blip es UN evento", porTick.Fallas, porTick.Intentos)
	}
	if porTick.Resets != 1 {
		t.Errorf("resets del tick = %d, quería 1", porTick.Resets)
	}
}

// Los intentos de un mismo tick pueden caer de los dos lados del segundo. Si el
// agrupamiento truncara en vez de redondear, un blip se partiría en dos ticks
// fallados y volvería a inflar el conteo.
func TestResumirPorTickAgrupaAunqueElTickCruceElSegundo(t *testing.T) {
	rs := []egress.Registro{
		{TS: "2026-08-26T21:03:59.998Z", Brazo: "v6-ka", Red: "tcp6", Clase: egress.ClaseReset},
		{TS: "2026-08-26T21:04:00.003Z", Brazo: "v6-ka", Red: "tcp6", Clase: egress.ClaseReset},
	}
	if got := egress.ResumirPorTick(rs, false)["v6-ka"]; got.Intentos != 1 || got.Fallas != 1 {
		t.Errorf("por tick: %d/%d, quería 1/1", got.Fallas, got.Intentos)
	}
}

// El brazo de 30 s es el ÚNICO que mira los ticks :30. Comparar su tasa contra
// la de v6-ka mezcla "cuánto ocio tenía la conexión" con "qué instantes miró".
// Restringido a :00 los dos tienen la misma exposición.
func TestResumirPorTickPuedeQuedarseSoloConLosTicksDelMinuto(t *testing.T) {
	rs := []egress.Registro{
		{TS: "2026-08-26T21:03:00.021Z", Brazo: "v6-ka-30s", Red: "tcp6", Clase: egress.ClaseOK},
		{TS: "2026-08-26T21:03:30.021Z", Brazo: "v6-ka-30s", Red: "tcp6", Clase: egress.ClaseReset},
		{TS: "2026-08-26T21:04:00.021Z", Brazo: "v6-ka-30s", Red: "tcp6", Clase: egress.ClaseOK},
		{TS: "2026-08-26T21:04:30.021Z", Brazo: "v6-ka-30s", Red: "tcp6", Clase: egress.ClaseReset},
	}

	todos := egress.ResumirPorTick(rs, false)["v6-ka-30s"]
	if todos.Intentos != 4 || todos.Fallas != 2 {
		t.Fatalf("todos los ticks: %d/%d, quería 2/4", todos.Fallas, todos.Intentos)
	}

	soloMinuto := egress.ResumirPorTick(rs, true)["v6-ka-30s"]
	if soloMinuto.Intentos != 2 || soloMinuto.Fallas != 0 {
		t.Errorf("solo ticks :00: %d/%d, quería 0/2 — las fallas eran todas del offset :30",
			soloMinuto.Fallas, soloMinuto.Intentos)
	}
}

// La corrección tiene que cambiar el veredicto en el caso que la motivó: once
// intentos fallados que en realidad son cinco momentos no alcanzan para nada.
func TestPorTickElCasoQueMotivoLaEnmiendaNoConcluye(t *testing.T) {
	var rs []egress.Registro
	destinos := []string{"a", "b", "c", "d", "e"}
	agregar := func(ts, brazo string, falla bool) {
		for _, d := range destinos {
			c := egress.ClaseOK
			if falla {
				c = egress.ClaseReset
			}
			rs = append(rs, egress.Registro{TS: ts, Brazo: brazo, Red: "tcp6", Destino: d, Clase: c})
		}
	}
	// cuatro blips en v6-ka-30s (cuatro destinos cada uno) y uno en v6-ka
	for _, ts := range []string{"2026-08-26T21:03:30Z", "2026-08-26T21:04:30Z",
		"2026-08-26T21:05:30Z", "2026-08-26T21:14:30Z"} {
		agregar(ts, "v6-ka-30s", true)
	}
	agregar("2026-08-26T21:05:00Z", "v6-ka", true)
	// y cien ticks sanos por brazo
	for i := range 100 {
		ts := fmt.Sprintf("2026-08-26T22:%02d:00Z", i%60)
		for _, br := range []string{"v6-ka", "v4-ka", "v6-fresh", "v4-fresh", "v6-ka-30s"} {
			agregar(ts, br, false)
		}
	}

	porTick := egress.ResumirPorTick(rs, false)
	if got := egress.Concluir(porTick); got.Codigo != egress.VerdNoConcluye {
		t.Errorf("Concluir por tick = %q (%s), quería %q: cinco momentos no alcanzan",
			got.Codigo, got.Detalle, egress.VerdNoConcluye)
	}
}
