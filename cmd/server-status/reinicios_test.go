package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
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
