package egress_test

import (
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
