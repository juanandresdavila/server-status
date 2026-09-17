package main

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/rules"
	"github.com/juanandresdavila/server-status/internal/store"
)

func abrirBase(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// tick pasa un minuto por el mismo camino que el ciclo de main.go. arranques
// es lo que devolvió el colector: StartedAt en cero es un Inspect fallido, y un
// nombre que no está es un container que el listado no trajo. El host va vacío
// para que el único evento posible sea el de containers.
func tick(t *testing.T, s *store.Store, ts time.Time, arranques map[string]time.Time) []model.Evento {
	t.Helper()
	ms := make([]model.ContainerSample, 0, len(arranques))
	for nombre, arranco := range arranques {
		ms = append(ms, model.ContainerSample{TS: ts, Name: nombre, State: "running", StartedAt: arranco})
	}
	return guardarContainersYDetectar(s, model.HostSample{}, model.HostSample{}, ms, ts)
}

// El caso del 17/09/2026. supabase-auth se recreó a las 14:47:52: el tick de
// las 14:47 lo listó viejo, el Inspect dio 404 y la muestra quedó con
// started_at = 0. El de las 14:48 lo vio nuevo y no avisó nada, porque
// comparaba contra ese cero.
//
// Pasa por SQLite y con StartedAt en nanosegundos a propósito: el bug del
// 22/08 solo se veía después del round-trip por la base. supabase-kong está
// quieto en los tres ticks con fracción de segundo; si esa fracción perdida
// volviera a leerse como reinicio, aparecería en el detalle. Las fracciones de
// los arranques viejos son inventadas: la base solo guardó los segundos.
func TestRecreadoConElInspectFallidoAvisaAlTickSiguiente(t *testing.T) {
	s := abrirBase(t)
	t1446 := time.Date(2026, 9, 17, 14, 46, 0, 0, time.UTC)
	t1447 := t1446.Add(time.Minute)
	t1448 := t1447.Add(time.Minute)

	viejo := time.Date(2026, 8, 22, 5, 0, 41, 207318554, time.UTC)
	nuevo := time.Date(2026, 9, 17, 14, 47, 52, 509434637, time.UTC)
	kong := time.Date(2026, 8, 22, 5, 0, 40, 881224310, time.UTC)

	if evs := tick(t, s, t1446, map[string]time.Time{"supabase-auth": viejo, "supabase-kong": kong}); len(evs) != 0 {
		t.Fatalf("14:46 avisó %+v", evs)
	}
	if evs := tick(t, s, t1447, map[string]time.Time{"supabase-auth": {}, "supabase-kong": kong}); len(evs) != 0 {
		t.Fatalf("14:47 avisó %+v", evs)
	}
	evs := tick(t, s, t1448, map[string]time.Time{"supabase-auth": nuevo, "supabase-kong": kong})
	if len(evs) != 1 {
		t.Fatalf("a las 14:48 hubo %d eventos, quería 1: el reinicio de supabase-auth se perdió", len(evs))
	}
	ev := evs[0]
	if ev.Tipo != "container_restart" {
		t.Errorf("tipo = %q, quería container_restart", ev.Tipo)
	}
	if !ev.OcurridoEn.Equal(t1448) {
		t.Errorf("ocurrido en %v, quería %v", ev.OcurridoEn, t1448)
	}
	if ev.Detalle != "1 container arrancó de nuevo: supabase-auth" {
		t.Errorf("detalle = %q", ev.Detalle)
	}
}

// Las secuencias de started_at de la tabla del §3 del spec, más las que
// agregaron las decisiones del 17/09: el borde de la ventana de 60 minutos y
// server-status caído más que la ventana. Cada paso es un tick entero por
// SQLite, y caddy está quieto en todos con fracción de segundo.
func TestLaBaseDeLosReiniciosEsElUltimoArranqueConocido(t *testing.T) {
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	viejo := time.Date(2026, 8, 22, 5, 0, 41, 207318554, time.UTC)
	nuevo := base.Add(52*time.Second + 509434637*time.Nanosecond)
	caddy := time.Date(2026, 8, 22, 5, 0, 39, 118000001, time.UTC)
	var cero time.Time

	con := func(auth time.Time) map[string]time.Time {
		return map[string]time.Time{"supabase-auth": auth, "caddy": caddy}
	}
	soloCaddy := map[string]time.Time{"caddy": caddy}

	type paso struct {
		min       int
		arranques map[string]time.Time
	}
	casos := []struct {
		nombre  string
		pasos   []paso
		avisaEn int // índice del paso que tiene que avisar; -1 si ninguno
	}{
		{"conocido, cero, nuevo", []paso{{0, con(viejo)}, {1, con(cero)}, {2, con(nuevo)}}, 2},
		{"conocido, dos ceros, nuevo", []paso{{0, con(viejo)}, {1, con(cero)}, {2, con(cero)}, {3, con(nuevo)}}, 3},
		{"todo en cero, como antes de la migración 10", []paso{{0, con(cero)}, {1, con(cero)}, {2, con(cero)}}, -1},
		{"container que aparece por primera vez", []paso{{0, soloCaddy}, {1, con(nuevo)}}, -1},
		{"conocido y nuevo sin cero en el medio", []paso{{0, con(viejo)}, {1, con(nuevo)}}, 1},
		{"el mismo arranque tick tras tick", []paso{{0, con(viejo)}, {1, con(viejo)}, {2, con(viejo)}}, -1},
		{"visto hace justo 60 minutos todavía es base", []paso{{0, con(viejo)}, {60, soloCaddy}, {61, con(nuevo)}}, 2},
		{"visto hace 61 minutos ya es un container nuevo", []paso{{0, con(viejo)}, {61, soloCaddy}, {62, con(nuevo)}}, -1},
		{"server-status caído más que la ventana", []paso{{0, con(viejo)}, {120, con(nuevo)}}, 1},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			s := abrirBase(t)
			for i, p := range c.pasos {
				evs := tick(t, s, base.Add(time.Duration(p.min)*time.Minute), p.arranques)
				if i != c.avisaEn {
					if len(evs) != 0 {
						t.Errorf("paso %d (min %d): avisó %q y no tenía que avisar", i, p.min, evs[0].Detalle)
					}
					continue
				}
				if len(evs) != 1 || evs[0].Detalle != "1 container arrancó de nuevo: supabase-auth" {
					t.Errorf("paso %d (min %d): eventos = %+v, quería uno solo por supabase-auth", i, p.min, evs)
				}
			}
		})
	}
}

// Reproduce el tick de las 14:48 UTC del 17/09/2026 contra una copia de la
// base de producción, la que deja `server-status backup`. La copia no entra al
// repo, así que sin SERVER_STATUS_COPIA el test se saltea:
//
//	SERVER_STATUS_COPIA=/ruta/status.db go test ./cmd/server-status -run TestReproduccion -v
//
// Arma dos copias de trabajo: una cortada en el minuto de las 14:48, de donde
// sale lo que vio ese tick, y otra cortada en el de las 14:47, que es la base
// tal como estaba justo antes. Sobre la segunda corre el mismo camino que el
// ciclo del minuto. El archivo original no se toca.
func TestReproduccionContraLaCopiaDeProduccion(t *testing.T) {
	origen := os.Getenv("SERVER_STATUS_COPIA")
	if origen == "" {
		t.Skip("sin SERVER_STATUS_COPIA: la reproducción necesita una copia de la base de producción")
	}
	t1447 := time.Date(2026, 9, 17, 14, 47, 0, 0, time.UTC)
	t1448 := t1447.Add(time.Minute)

	// Lo que vio el tick de las 14:48.
	hasta1448 := copiaHasta(t, origen, t1448)
	despues, err := hasta1448.UltimoEstadoContainers()
	if err != nil {
		t.Fatalf("UltimoEstadoContainers: %v", err)
	}
	hostAhora, _, err := hasta1448.UltimaHostSample()
	if err != nil {
		t.Fatalf("UltimaHostSample: %v", err)
	}
	if !hostAhora.TS.Equal(t1448) {
		t.Fatalf("la copia no tiene el tick de las 14:48: el último es %v", hostAhora.TS)
	}
	auth, ok := buscarContainer(despues, "supabase-auth")
	if !ok || auth.StartedAt.IsZero() {
		t.Fatalf("a las 14:48 supabase-auth = %+v, quería su arranque nuevo", auth)
	}
	t.Logf("14:48 supabase-auth arrancó %s", auth.StartedAt.Format(time.RFC3339))

	// La base justo antes de ese tick.
	hasta1447 := copiaHasta(t, origen, t1447)
	previo, err := hasta1447.UltimoEstadoContainers()
	if err != nil {
		t.Fatalf("UltimoEstadoContainers: %v", err)
	}
	hostAntes, _, err := hasta1447.UltimaHostSample()
	if err != nil {
		t.Fatalf("UltimaHostSample: %v", err)
	}

	// Sin el cero, esta copia no reproduce el bug y el test no prueba nada.
	if a, ok := buscarContainer(previo, "supabase-auth"); !ok || !a.StartedAt.IsZero() {
		t.Fatalf("a las 14:47 supabase-auth = %+v, quería started_at en cero", a)
	}

	// Con la base de antes del arreglo no sale: es el bug, sobre datos reales.
	for _, ev := range rules.DetectarEventos(hostAntes, hostAhora, previo, despues, t1448) {
		if strings.Contains(ev.Detalle, "supabase-auth") {
			t.Fatalf("la base vieja ya avisaba (%q): la copia no reproduce el bug", ev.Detalle)
		}
	}

	var encontrado *model.Evento
	evs := guardarContainersYDetectar(hasta1447, hostAntes, hostAhora, despues, t1448)
	for i := range evs {
		t.Logf("evento: tipo=%s ocurrido=%s detalle=%q",
			evs[i].Tipo, evs[i].OcurridoEn.UTC().Format(time.RFC3339), evs[i].Detalle)
		if evs[i].Tipo == "container_restart" && strings.Contains(evs[i].Detalle, "supabase-auth") {
			encontrado = &evs[i]
		}
	}
	if encontrado == nil {
		t.Fatal("el tick de las 14:48 no dio el container_restart de supabase-auth")
	}
	if !encontrado.OcurridoEn.Equal(t1448) {
		t.Errorf("ocurrido en %v, quería %v", encontrado.OcurridoEn, t1448)
	}
}

// copiaHasta copia la base a un directorio temporal y la corta al final del
// minuto dado, como estaba cuando terminó ese tick. El ts de host y de
// containers se guarda truncado al minuto, así que `ts > minuto` es todo lo
// posterior.
func copiaHasta(t *testing.T, origen string, minuto time.Time) *store.Store {
	t.Helper()
	destino := filepath.Join(t.TempDir(), "status.db")
	copiarArchivo(t, origen, destino)

	db, err := sql.Open("sqlite", destino)
	if err != nil {
		t.Fatalf("abrir la copia: %v", err)
	}
	for _, tabla := range []string{"container_samples", "host_samples"} {
		if _, err := db.Exec(`DELETE FROM `+tabla+` WHERE ts > ?`, minuto.Unix()); err != nil {
			t.Fatalf("recortar %s: %v", tabla, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("cerrar la copia: %v", err)
	}

	s, err := store.Open(destino)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func copiarArchivo(t *testing.T, origen, destino string) {
	t.Helper()
	in, err := os.Open(origen)
	if err != nil {
		t.Fatalf("abrir %s: %v", origen, err)
	}
	defer in.Close()
	out, err := os.Create(destino)
	if err != nil {
		t.Fatalf("crear %s: %v", destino, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copiar: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("cerrar %s: %v", destino, err)
	}
}

func buscarContainer(cs []model.ContainerSample, nombre string) (model.ContainerSample, bool) {
	for _, c := range cs {
		if c.Name == nombre {
			return c, true
		}
	}
	return model.ContainerSample{}, false
}
