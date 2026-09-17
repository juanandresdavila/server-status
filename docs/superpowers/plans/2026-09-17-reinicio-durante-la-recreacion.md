# Un container recreado avisa aunque el tick caiga en la recreación, plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** que un container recreado genere su `container_restart` aunque el tick
de ese minuto haya guardado `started_at = 0` porque el `Inspect` dio 404.

**Architecture:** la base del detector deja de ser la foto del minuto anterior
(`UltimoEstadoContainers()`) y pasa a ser, por container, el último
`started_at` distinto de cero dentro de los 60 minutos que terminan en el
**último tick guardado** (`store.UltimoArranqueConocido`). Leer la base, guardar
la foto y detectar se juntan en una función de `cmd/server-status`
(`guardarContainersYDetectar`), así el cambio de base se testea pasando por
SQLite; el ciclo de `main.go` solo la llama. La lógica de
`rules.DetectarReinicioContainers` no cambia. El panel sigue con
`UltimoEstadoContainers()`.

**Tech Stack:** Go 1.26 sin cgo, `modernc.org/sqlite`, SQLite en WAL.

**Spec:** `docs/superpowers/specs/2026-09-17-reinicio-durante-la-recreacion-design.md`.

## Decisiones de Juan del 17/09/2026 (cambian el spec)

| Tema | Decía el spec | Decidido | Por qué |
|---|---|---|---|
| Ventana | 10 min ⚖️ | **60 min** | Quedarse corto es un aviso perdido, que es el bug. Pasarse cuesta como mucho un «arrancó de nuevo» para un container nuevo con nombre reusado. Con 10, un `down` de 15 min seguido de `up` no avisaba nada. |
| Desde dónde se cuenta | `ts >= desde`, con `desde` del reloj | **`ts >= MAX(ts) - ventana`**: el último tick guardado | Hoy `UltimoEstadoContainers()` toma el último tick sin importar su edad: si server-status estuvo caído 20 min y un container se reinició, al volver avisa. Contar desde el reloj perdía eso, y el spec no lo listaba. |
| Firma | `UltimoArranqueConocido(desde time.Time)` | **`UltimoArranqueConocido(ventana time.Duration)`** | Consecuencia de lo anterior: la consulta no necesita reloj. |
| Copia para reproducir | la del backup | **`server-status backup` hoy** | La copia de las 04:00 ART del 17/09 no tiene las 14:47 UTC; la del 18/09 recién llega mañana. |

## Global Constraints

Valen para TODAS las tareas.

- **TDD estricto** (superpowers:test-driven-development): el test primero, correrlo
  y VER el fallo por el motivo correcto, después el código mínimo.
- 🚨 **El test del caso del 17/09 pasa por SQLite con `StartedAt` en
  nanosegundos** (`14:47:52.509434637`, como lo devuelve Docker). La lección del
  22/08: el bug de aquella tanda solo se veía después del round-trip por la base.
- 🚨 **Mutación que no se negocia:** volver `guardarContainersYDetectar` a
  `s.UltimoEstadoContainers()` pone en rojo
  `TestRecreadoConElInspectFallidoAvisaAlTickSiguiente`. Si queda verde, el test no
  mide el bug.
- 🚨 **Reproducción que no se negocia:** con la copia de `status.db` que deja
  `server-status backup`, el tick de las 14:48 UTC del 17/09 da un
  `container_restart` que nombra a `supabase-auth`, con `OcurridoEn` 14:48 UTC.
- **Sin migración nueva.** La consulta usa la PK `(ts, name)`: verificado con
  `EXPLAIN QUERY PLAN` → `SEARCH container_samples USING INDEX
  sqlite_autoindex_container_samples_1 (ts>?)`.
- **El panel no cambia**: sigue con `UltimoEstadoContainers()`, que está bien
  para mostrar la foto actual.
- **Invariante 5**: nada de `time.Now()` nuevo. La consulta no lleva reloj.
- **Verificar sin pipes** que tapen el exit code. Comandos sueltos, o la salida a
  un archivo y el `exit=$?` impreso.
- **CI rojo conocido**: `TestNovedadesOrdenaDeLoMasNuevoALoMasViejo`
  (`internal/web/panel_test.go:464`) falla en `origin/main` por una fecha fija
  (línea de base corrida el 17/09 en este worktree: todo `ok` salvo ese test). Un
  rojo SOLO por ese test no es de este cambio; cualquier otro, sí.
- **Repo público**: ninguna IP en ningún archivo ni commit. La copia de
  `status.db` vive en el scratchpad de la sesión y **nunca** entra al repo.
- **Commits**: el noreply ya está configurado en el repo; prefijo convencional;
  **sin `Co-Authored-By` ni ninguna línea de autoría de IA**.
- **Mutaciones solo sobre archivos commiteados**, y se revierten con
  `git checkout -- <archivo>` seguido de `git diff --exit-code`. Mutar algo sin
  commitear y hacer checkout borra el trabajo.
- ⛔ **Con ok explícito de Juan, cada uno por separado**: correr
  `server-status backup` en el VPS, push, abrir el PR, mergear (con `--rebase`,
  nunca `--squash`), deploy, y la recreación de prueba después del deploy.

---

## File Structure

| Archivo | Qué le toca |
|---|---|
| `internal/store/store.go` | `UltimoArranqueConocido(ventana)`; `escanearContainers`, compartido con `UltimoEstadoContainers`. |
| `internal/store/store_test.go` | Saltea el cero y trae el más reciente; ventana contada desde el último tick con su borde; base vacía. |
| `cmd/server-status/reinicios.go` (nuevo) | `ventanaReinicios` y `guardarContainersYDetectar`. |
| `cmd/server-status/reinicios_test.go` (nuevo) | El caso del 17/09 con nanosegundos, la tabla del §3 más borde y caída del proceso, y la reproducción contra la copia de producción (se saltea sin `SERVER_STATUS_COPIA`). |
| `cmd/server-status/main.go` (~292-330) | El ciclo del minuto llama a `guardarContainersYDetectar`. |
| `internal/rules/eventos.go` (~78-82) | Solo el comentario de la guarda del cero. |
| `CLAUDE.md` | Un punto en «Gotchas que costaron caro». |
| el spec | Estado y decisiones del 17/09. |

---

## Task 0: Commitear el plan revisado

Con el ok de Juan y sus cambios aplicados, antes de tocar código:

```bash
git add docs/superpowers/plans/2026-09-17-reinicio-durante-la-recreacion.md
git commit -m "docs(plan): un container recreado avisa aunque el tick caiga en la recreación"
```

---

## Task 1: `UltimoArranqueConocido` en el store

**Files:**
- Modify: `internal/store/store.go` (junto a `UltimoEstadoContainers`, ~380-410)
- Test: `internal/store/store_test.go` (al final)

**Interfaces:**
- Consumes: nada nuevo.
- Produces: `func (s *Store) UltimoArranqueConocido(ventana time.Duration) ([]model.ContainerSample, error)`.
  Una fila por container, ordenadas por `Name`. Todas las columnas son de la
  muestra más reciente con `started_at > 0` y `ts >= MAX(ts) - ventana` (borde
  inclusive). `StartedAt` vuelve en segundos, UTC, nunca cero. Tabla vacía →
  `nil, nil`.

- [ ] **Step 1: Escribir los tests que fallan**

Agregar al final de `internal/store/store_test.go`:

```go
// La base del detector de reinicios. El 17/09/2026 supabase-auth se recreó
// justo en el tick de las 14:47: el Inspect dio 404 y esa muestra quedó con
// started_at = 0. La base tiene que saltear ese cero y traer el último
// arranque conocido: el de la muestra más reciente, no uno cualquiera.
func TestUltimoArranqueConocidoSalteaElCeroYTraeElMasReciente(t *testing.T) {
	s := abrir(t)
	t1440 := time.Date(2026, 9, 17, 14, 40, 0, 0, time.UTC)
	t1446 := t1440.Add(6 * time.Minute)
	t1447 := t1446.Add(time.Minute)

	masViejo := time.Date(2026, 8, 20, 9, 12, 3, 0, time.UTC)
	conocido := time.Date(2026, 8, 22, 5, 0, 41, 207318554, time.UTC)

	for _, tanda := range [][]model.ContainerSample{
		{{TS: t1440, Name: "supabase-auth", State: "running", Health: "starting", StartedAt: masViejo}},
		{{TS: t1446, Name: "supabase-auth", State: "running", Health: "healthy", StartedAt: conocido}},
		{{TS: t1447, Name: "supabase-auth", State: "running", Health: "", StartedAt: time.Time{}}},
	} {
		if err := s.InsertContainerSamples(tanda); err != nil {
			t.Fatalf("InsertContainerSamples: %v", err)
		}
	}

	got, err := s.UltimoArranqueConocido(time.Hour)
	if err != nil {
		t.Fatalf("UltimoArranqueConocido: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("volvieron %d filas, quería 1: %+v", len(got), got)
	}
	// Vuelve en segundos: la base no guarda la fracción.
	quiero := time.Unix(conocido.Unix(), 0).UTC()
	if !got[0].StartedAt.Equal(quiero) {
		t.Errorf("StartedAt = %v, quería %v: ni el cero ni el más viejo", got[0].StartedAt, quiero)
	}
	// Las demás columnas son de ESA fila y no de otra del mismo container.
	if !got[0].TS.Equal(t1446) || got[0].Health != "healthy" {
		t.Errorf("fila = %+v, quería la de las 14:46", got[0])
	}
}

// La ventana se cuenta desde el último tick GUARDADO, no desde el reloj: si
// server-status estuvo caído más que la ventana, al volver la base sigue siendo
// lo último que vio. Y un container que no aparece desde hace más que la
// ventana deja de ser base: si vuelve, es nuevo. El borde es inclusive.
func TestUltimoArranqueConocidoCuentaLaVentanaDesdeElUltimoTick(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	arranco := time.Date(2026, 8, 22, 5, 0, 41, 0, time.UTC)
	fila := func(min int, name string) []model.ContainerSample {
		return []model.ContainerSample{{
			TS: base.Add(time.Duration(min) * time.Minute), Name: name,
			State: "running", StartedAt: arranco,
		}}
	}

	// El último tick es el del minuto 61. "borde" se vio por última vez 60
	// minutos antes; "afuera", 61.
	for _, tanda := range [][]model.ContainerSample{
		fila(0, "afuera"), fila(1, "borde"), fila(61, "quieto"),
	} {
		if err := s.InsertContainerSamples(tanda); err != nil {
			t.Fatalf("InsertContainerSamples: %v", err)
		}
	}

	got, err := s.UltimoArranqueConocido(time.Hour)
	if err != nil {
		t.Fatalf("UltimoArranqueConocido: %v", err)
	}
	var nombres []string
	for _, c := range got {
		nombres = append(nombres, c.Name)
	}
	if !reflect.DeepEqual(nombres, []string{"borde", "quieto"}) {
		t.Errorf("base = %v, quería [borde quieto]", nombres)
	}
}

// Base recién creada: no hay con qué comparar, y eso no es un error.
func TestUltimoArranqueConocidoSinMuestras(t *testing.T) {
	s := abrir(t)
	got, err := s.UltimoArranqueConocido(time.Hour)
	if err != nil {
		t.Fatalf("UltimoArranqueConocido: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("volvieron %d filas de una base vacía", len(got))
	}
}
```

(`reflect` ya está importado en `store_test.go`.)

- [ ] **Step 2: Correrlos y ver que fallan**

Run: `go test ./internal/store -race -run 'TestUltimoArranqueConocido' -v`
Expected: FAIL de compilación, `s.UltimoArranqueConocido undefined`.

- [ ] **Step 3: Implementar**

En `internal/store/store.go`, reemplazar `UltimoEstadoContainers` entera por
esto (misma consulta, el escaneo pasa a una función compartida) y agregar
`UltimoArranqueConocido` y `escanearContainers` debajo:

```go
// UltimoEstadoContainers devuelve la foto del minuto más reciente.
func (s *Store) UltimoEstadoContainers() ([]model.ContainerSample, error) {
	filas, err := s.db.Query(`
		SELECT ts, name, state, health, restarts, cpu_pct, mem_bytes, started_at
		FROM container_samples
		WHERE ts = (SELECT MAX(ts) FROM container_samples)
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer filas.Close()
	return escanearContainers(filas)
}

// UltimoArranqueConocido es la base contra la que se detectan los reinicios:
// por container, la muestra más reciente con started_at conocido dentro de la
// ventana que termina en el último tick guardado.
//
// No es la foto del minuto anterior (UltimoEstadoContainers) porque el cero de
// started_at también significa "el Inspect falló", y el Inspect falla justo
// cuando un container se recrea: el listado trae el viejo, se pide su detalle
// y compose ya lo borró. Pasó con supabase-auth el 17/09/2026: comparar contra
// esa foto era comparar contra un cero, y el detector saltea los ceros.
//
// La ventana se cuenta desde MAX(ts) y no desde el reloj: si el proceso estuvo
// caído más que la ventana, al volver la base sigue siendo lo último que vio,
// igual que con UltimoEstadoContainers.
//
// Las columnas sueltas junto a MAX(ts) salen de la fila que tiene ese máximo.
// Es comportamiento documentado de SQLite cuando hay un único min() o max()
// (sqlite.org/lang_select.html#bareagg), y el test lo verifica.
func (s *Store) UltimoArranqueConocido(ventana time.Duration) ([]model.ContainerSample, error) {
	filas, err := s.db.Query(`
		SELECT MAX(ts), name, state, health, restarts, cpu_pct, mem_bytes, started_at
		FROM container_samples
		WHERE started_at > 0
		  AND ts >= (SELECT MAX(ts) FROM container_samples) - ?
		GROUP BY name
		ORDER BY name`, int64(ventana/time.Second))
	if err != nil {
		return nil, err
	}
	defer filas.Close()
	return escanearContainers(filas)
}

// escanearContainers lee filas con las columnas en este orden: ts, name,
// state, health, restarts, cpu_pct, mem_bytes, started_at.
func escanearContainers(filas *sql.Rows) ([]model.ContainerSample, error) {
	var out []model.ContainerSample
	for filas.Next() {
		var (
			c       model.ContainerSample
			ts      int64
			mem     int64
			arranco int64
		)
		if err := filas.Scan(&ts, &c.Name, &c.State, &c.Health, &c.Restarts, &c.CPUPct, &mem, &arranco); err != nil {
			return nil, err
		}
		c.TS = time.Unix(ts, 0).UTC()
		c.MemBytes = uint64(mem)
		// started_at en 0 es "no se sabe": las filas anteriores a la migración
		// 10 no lo tienen, y un Inspect fallido lo deja en cero. Se deja el cero
		// de time.Time para que el detector las ignore en vez de leerlas como
		// un arranque en 1970.
		if arranco > 0 {
			c.StartedAt = time.Unix(arranco, 0).UTC()
		}
		out = append(out, c)
	}
	return out, filas.Err()
}
```

- [ ] **Step 4: Correr y ver que pasan, más los tests viejos de la foto**

Run: `go test ./internal/store -race -run 'TestUltimoArranqueConocido|TestUltimoEstadoContainers|TestInsertContainerSamplesYConsulta' -v`
Expected: PASS los cinco.

Run: `go test ./internal/store -race`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): último arranque conocido por container, contado desde el último tick"
```

---

## Task 2: La foto de containers y la detección, en una función

Refactor sin cambio de comportamiento: la base sigue siendo
`UltimoEstadoContainers()`. Existe para que la Task 3 pueda ver el test en rojo
por el motivo correcto y para que la mutación del spec tenga dónde aplicarse.

**Files:**
- Create: `cmd/server-status/reinicios.go`
- Modify: `cmd/server-status/main.go` (~292-330, bloque `case <-persistencia.C:`)

**Interfaces:**
- Consumes: `store.Store.UltimoEstadoContainers`, `store.Store.InsertContainerSamples`, `rules.DetectarEventos`.
- Produces: `func guardarContainersYDetectar(s *store.Store, hostAntes, host model.HostSample, ms []model.ContainerSample, ahora time.Time) []model.Evento`.
  Lee la base, guarda `ms` y devuelve lo que diga `rules.DetectarEventos`. Los
  errores de lectura o escritura se loguean y la detección sigue.

- [ ] **Step 1: Crear `cmd/server-status/reinicios.go`**

```go
package main

import (
	"log/slog"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/rules"
	"github.com/juanandresdavila/server-status/internal/store"
)

// guardarContainersYDetectar guarda la foto de containers del minuto y
// devuelve los hechos puntuales del tick: reinicio del host y de containers.
//
// Vive afuera del ciclo de correr() para que la base contra la que se compara
// se pueda testear pasando por SQLite, que es donde se escondieron el bug del
// 22/08 (los nanosegundos) y el del 17/09 (el cero del Inspect fallido).
//
// Un error de lectura o de escritura se loguea y la detección sigue con lo que
// haya: es un monitor, y callarse entero por una consulta es lo peor que puede
// hacer.
func guardarContainersYDetectar(s *store.Store, hostAntes, host model.HostSample,
	ms []model.ContainerSample, ahora time.Time) []model.Evento {

	// La base se lee ANTES de insertar, o la foto nueva termina comparada
	// contra sí misma.
	antes, err := s.UltimoEstadoContainers()
	if err != nil {
		slog.Error("no se pudo leer el estado anterior de los containers", "err", err)
	}
	if err := s.InsertContainerSamples(ms); err != nil {
		slog.Error("no se pudieron guardar los containers", "err", err)
	}
	return rules.DetectarEventos(hostAntes, host, antes, ms, ahora)
}
```

- [ ] **Step 2: Que el ciclo de `main.go` la use**

En `cmd/server-status/main.go`, dentro de `case <-persistencia.C:`, borrar:

```go
			contAntes, err := s.UltimoEstadoContainers()
			if err != nil {
				slog.Error("no se pudo leer el estado anterior de los containers", "err", err)
			}
```

y reemplazar:

```go
			if err := s.InsertContainerSamples(ms); err != nil {
				slog.Error("no se pudieron guardar los containers", "err", err)
			}

			// Eventos discretos: reinicios. El motor de reglas no los ve porque
			// solo sabe de estados sostenidos —tres muestras malas seguidas— y
			// un reinicio dura segundos. El del host del 22/08 duró 18.
			for _, ev := range rules.DetectarEventos(hostAntes, m, contAntes, ms, clock.Real{}.Now()) {
```

por:

```go
			// Eventos discretos: reinicios. El motor de reglas no los ve porque
			// solo sabe de estados sostenidos —tres muestras malas seguidas— y
			// un reinicio dura segundos. El del host del 22/08 duró 18.
			for _, ev := range guardarContainersYDetectar(s, hostAntes, m, ms, clock.Real{}.Now()) {
```

El resto del `for` (guardar el evento y loguearlo) queda igual. Leer la base
después de `cli.Recolectar` en vez de antes no cambia nada: nadie más escribe
`container_samples` y, si `Recolectar` falla, el `continue` sigue salteando todo
como hoy. El comentario de `hostAntes` («El "antes" se lee ANTES de insertar»)
queda, porque sigue valiendo para el host.

- [ ] **Step 3: Compilar, vet y tests del paquete**

Run: `go build ./... && go vet ./cmd/server-status`
Expected: sin salida.

Run: `go test ./cmd/server-status -race`
Expected: `ok`.

Run: `grep -n 'UltimoEstadoContainers\|guardarContainersYDetectar' cmd/server-status/main.go`
Expected: `guardarContainersYDetectar` en el ciclo, y `UltimoEstadoContainers`
solo en `listarContainers` (el subcomando `containers`).

- [ ] **Step 4: Commit**

```bash
git add cmd/server-status/reinicios.go cmd/server-status/main.go
git commit -m "refactor: la foto de containers y la detección de reinicios, en una función"
```

---

## Task 3: El caso del 17/09 en rojo, y el arreglo

**Files:**
- Create: `cmd/server-status/reinicios_test.go`
- Modify: `cmd/server-status/reinicios.go`
- Modify: `internal/rules/eventos.go` (~78-82, solo el comentario)

**Interfaces:**
- Consumes: `guardarContainersYDetectar` (Task 2), `store.Store.UltimoArranqueConocido` (Task 1).
- Produces: `const ventanaReinicios = time.Hour`; helpers de test `abrirBase(t) *store.Store` y `tick(t, s, ts, arranques map[string]time.Time) []model.Evento`, que la Task 4 reusa.

- [ ] **Step 1: Escribir los tests**

Crear `cmd/server-status/reinicios_test.go`:

```go
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
```

- [ ] **Step 2: Correrlos y ver el rojo por el motivo correcto**

Run: `go test ./cmd/server-status -race -run 'TestRecreadoConElInspectFallidoAvisaAlTickSiguiente|TestLaBaseDeLosReiniciosEsElUltimoArranqueConocido' -v`

Expected: FAIL, y exactamente estos, porque la base todavía es la foto del
minuto anterior:
- `TestRecreadoConElInspectFallidoAvisaAlTickSiguiente`: `a las 14:48 hubo 0 eventos, quería 1`.
- `.../conocido,_cero,_nuevo` y `.../conocido,_dos_ceros,_nuevo`: `eventos = [], quería uno solo`.
- `.../visto_hace_justo_60_minutos_todavía_es_base`: `eventos = [], quería uno solo`.

Los otros cinco subtests en PASS. Si falla alguno más, o alguno de estos por
otro mensaje, parar: el test no está midiendo lo que dice.

- [ ] **Step 3: El arreglo**

En `cmd/server-status/reinicios.go`, agregar la constante antes de la función:

```go
// ventanaReinicios es hasta dónde para atrás se busca el último arranque
// conocido de un container, contada desde el último tick guardado.
//
// 60 minutos, decidido por Juan el 17/09/2026. La recreación dura segundos y
// cualquier ventana la cubre; lo que decide es cuánto cuesta equivocarse.
// Quedarse corto es un aviso perdido, que es justo el bug que esto arregla:
// con 10 minutos, un stack bajado 15 y vuelto a subir no avisaba nada.
// Pasarse cuesta como mucho un «arrancó de nuevo» para un container nuevo que
// reusa el nombre de uno viejo.
const ventanaReinicios = time.Hour
```

y en `guardarContainersYDetectar` reemplazar:

```go
	// La base se lee ANTES de insertar, o la foto nueva termina comparada
	// contra sí misma.
	antes, err := s.UltimoEstadoContainers()
	if err != nil {
		slog.Error("no se pudo leer el estado anterior de los containers", "err", err)
	}
```

por:

```go
	// La base se lee ANTES de insertar, o la foto nueva termina comparada
	// contra sí misma. Y no es la foto del minuto anterior sino el último
	// arranque conocido: si el tick anterior cayó en una recreación, su foto
	// tiene started_at en cero y el reinicio se pierde (17/09/2026).
	antes, err := s.UltimoArranqueConocido(ventanaReinicios)
	if err != nil {
		slog.Error("no se pudo leer el último arranque conocido de los containers", "err", err)
	}
```

- [ ] **Step 4: El comentario de la guarda del cero**

En `internal/rules/eventos.go`, reemplazar:

```go
		// started_at en cero es "no se sabe" —las filas anteriores a la
		// migración 10 no lo tienen—. Leerlo como un arranque en 1970 haría
		// que TODO container pareciera recién reiniciado en el primer tick
		// después del deploy.
		case anterior.IsZero() || c.StartedAt.IsZero():
```

por:

```go
		// started_at en cero es "no se sabe", por dos motivos: las filas
		// anteriores a la migración 10 no lo tienen, y un Inspect que falló lo
		// deja en cero (da 404 justo cuando compose recrea el container).
		// Leerlo como un arranque en 1970 haría que TODO container pareciera
		// recién reiniciado en el primer tick después del deploy.
		//
		// El ciclo ya no le pasa ceros en "antes": su base es el último
		// arranque conocido (store.UltimoArranqueConocido), porque comparar
		// contra la foto del minuto anterior perdió la recreación del
		// 17/09/2026. La guarda queda por el "después" en cero, que es el tick
		// que cae en la recreación.
		case anterior.IsZero() || c.StartedAt.IsZero():
```

- [ ] **Step 5: Correr y ver el verde**

Run: `go test ./cmd/server-status -race -run 'TestRecreadoConElInspectFallidoAvisaAlTickSiguiente|TestLaBaseDeLosReiniciosEsElUltimoArranqueConocido' -v`
Expected: PASS, los nueve subtests incluidos.

Run: `go test ./cmd/server-status ./internal/rules ./internal/store -race`
Expected: tres `ok`.

- [ ] **Step 6: Commit**

```bash
git add cmd/server-status/reinicios.go cmd/server-status/reinicios_test.go internal/rules/eventos.go
git commit -m "fix: un container recreado avisa aunque el Inspect de ese tick haya fallado"
```

---

## Task 4: Reproducción contra la copia de producción

**Files:**
- Modify: `cmd/server-status/reinicios_test.go` (imports y un test al final)

**Interfaces:**
- Consumes: `guardarContainersYDetectar` (Task 3), `store.Open`, `store.Store.UltimoEstadoContainers`, `store.Store.UltimaHostSample`, `rules.DetectarEventos`. No usa `abrirBase`: trabaja sobre copias recortadas de la base real.
- Produces: `TestReproduccionContraLaCopiaDeProduccion`, que se saltea sin `SERVER_STATUS_COPIA`.

- [ ] **Step 1: Escribir el test**

En `cmd/server-status/reinicios_test.go`, el bloque de imports pasa a ser:

```go
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
```

(El driver `sqlite` ya queda registrado por el import de `store`.) Y al final del
archivo:

```go
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
```

- [ ] **Step 2: Sin la copia, se saltea y el resto sigue verde**

Run: `go test ./cmd/server-status -race -run TestReproduccionContraLaCopiaDeProduccion -v`
Expected: `--- SKIP: TestReproduccionContraLaCopiaDeProduccion` con el mensaje de
`SERVER_STATUS_COPIA`, y `ok`.

Run: `go vet ./cmd/server-status`
Expected: sin salida.

- [ ] **Step 3: ⛔ Traer la copia (con ok explícito de Juan)**

`SP` es el scratchpad de la sesión. Nada de esto entra al repo.

```bash
ssh vps 'sudo -u server-status /usr/local/bin/server-status -config /etc/server-status/config.yaml backup'
```
Expected: `copia consistente en /var/lib/server-status/backup/status.db`.

```bash
ssh vps 'sudo sha256sum /var/lib/server-status/backup/status.db'
ssh vps 'sudo cat /var/lib/server-status/backup/status.db' > "$SP/status-2026-09-17.db"
shasum -a 256 "$SP/status-2026-09-17.db"
sqlite3 "$SP/status-2026-09-17.db" 'PRAGMA integrity_check'
```
Expected: los dos sha256 iguales (`scp`/Tailscale ya cortó archivos a mitad
devolviendo 0) e `integrity_check` = `ok`.

```bash
sqlite3 "$SP/status-2026-09-17.db" "SELECT datetime(ts,'unixepoch'), health, datetime(started_at,'unixepoch') FROM container_samples WHERE name='supabase-auth' AND ts BETWEEN strftime('%s','2026-09-17 14:45:00') AND strftime('%s','2026-09-17 14:49:00') ORDER BY ts"
```
Expected: 14:46 `healthy` con arranque del 22/08; 14:47 health vacío y
`1970-01-01 00:00:00` (el cero); 14:48 `healthy` con arranque 14:47:52. Es la
tabla del §1 del spec.

- [ ] **Step 4: Correr la reproducción**

Run: `SERVER_STATUS_COPIA="$SP/status-2026-09-17.db" go test ./cmd/server-status -race -run TestReproduccionContraLaCopiaDeProduccion -v`
Expected: PASS, con un log `evento: tipo=container_restart
ocurrido=2026-09-17T14:48:00Z detalle="... supabase-auth ..."`. Guardar la
salida en `$SP/reproduccion.txt` para el cierre.

- [ ] **Step 5: El costo de la consulta sobre la base real**

```bash
sqlite3 "$SP/status-2026-09-17.db" <<'SQL'
.timer on
SELECT COUNT(*) FROM container_samples;
EXPLAIN QUERY PLAN SELECT MAX(ts), name, state, health, restarts, cpu_pct, mem_bytes, started_at FROM container_samples WHERE started_at > 0 AND ts >= (SELECT MAX(ts) FROM container_samples) - 3600 GROUP BY name ORDER BY name;
SELECT COUNT(*) FROM (SELECT MAX(ts), name FROM container_samples WHERE started_at > 0 AND ts >= (SELECT MAX(ts) FROM container_samples) - 3600 GROUP BY name);
SQL
```
Expected: `SEARCH container_samples USING INDEX sqlite_autoindex_container_samples_1 (ts>?)`,
un container por fila (~21) y un tiempo del orden de milisegundos. Anotar los
números: corre una vez por minuto.

- [ ] **Step 6: Commit**

```bash
git add cmd/server-status/reinicios_test.go
git commit -m "test: reproducción del 17/09 contra una copia de producción"
```

---

## Task 5: Documentación

**Files:**
- Modify: `CLAUDE.md` («Gotchas que costaron caro»)
- Modify: `docs/superpowers/specs/2026-09-17-reinicio-durante-la-recreacion-design.md`

- [ ] **Step 1: `CLAUDE.md`**

Agregar, justo después del punto que empieza con «**Una marca de tiempo que se
guarda en segundos no se compara con la que vino de Docker.**»:

```markdown
- **`started_at = 0` no es solo «fila vieja»: también es «el Inspect falló».**
  Y el Inspect falla justo cuando un container se recrea: el listado trae el
  viejo, se pide su detalle y compose ya lo borró (404). El 17/09/2026 eso dejó
  a `supabase-auth` sin evento ni aviso, porque el detector comparaba contra la
  foto del minuto anterior, que era ese cero. Desde entonces la base es
  `UltimoArranqueConocido`: el último `started_at` distinto de cero dentro de
  **60 minutos contados desde el último tick guardado, no desde el reloj**, para
  que una caída de server-status más larga que la ventana no se coma los
  reinicios de ese lapso. La reproducción contra una copia real está en
  `TestReproduccionContraLaCopiaDeProduccion` (`SERVER_STATUS_COPIA`).
```

- [ ] **Step 2: El spec**

En el encabezado, reemplazar `**Estado:** 🔄 spec escrito, sin implementar` por:

```markdown
**Estado:** ✅ implementado el 17/09/2026 (plan en `docs/superpowers/plans/2026-09-17-reinicio-durante-la-recreacion.md`), sin deployar
```

En el §3, reemplazar desde `- Query nueva en el store, por ejemplo` hasta el
final de la lista de la ventana (el punto «Con 10 minutos alcanza…») por:

```markdown
- Query nueva en el store, `UltimoArranqueConocido(ventana time.Duration)`:
  por cada `name`, la muestra más reciente con `started_at > 0` y
  `ts >= MAX(ts) - ventana`.
- La lectura de la base, el insert y la detección van juntos en
  `guardarContainersYDetectar` (`cmd/server-status/reinicios.go`), que el ciclo
  de `main.go` llama. **El panel sigue usando `UltimoEstadoContainers()`**, que
  está bien para mostrar la foto actual.
- La guarda del cero en el detector se queda: sigue cubriendo el `despues` que
  vuelva en cero (el tick que cae justo en la ventana).

**Ventana: 60 minutos, contados desde el último tick guardado.** Decidido por
Juan el 17/09/2026; este spec proponía 10 minutos contados desde el reloj.
- Sin ventana, un container que se borró hace un mes y vuelve con el mismo
  nombre se avisaría como «reiniciado», cuando en realidad es nuevo.
- 60 y no 10: quedarse corto es un aviso perdido, que es el bug; pasarse cuesta
  como mucho un «arrancó de nuevo» para un nombre reusado. Con 10, un stack
  bajado 15 minutos y vuelto a subir no avisaba nada.
- Desde el último tick y no desde el reloj: `UltimoEstadoContainers()` tomaba el
  último tick sin importar su edad, así que después de una caída de
  server-status los reinicios de ese lapso sí avisaban. Contar desde el reloj
  habría perdido eso.
```

En la tabla de secuencias del §3, cambiar `conocido → 0 → 0 → nuevo | ❌ nada |
✅ evento (dentro de 10 min)` por `(dentro de 60 min)`, y agregar al final:

```markdown
| conocido → server-status caído 2 h → nuevo | ✅ evento | ✅ evento (la ventana se cuenta desde el último tick) |
| conocido → ausente 30 min (`down` y `up`) → nuevo | ❌ nada | ✅ evento |
| conocido → ausente más de 60 min → nuevo | nada | nada (es un container nuevo) |
```

(«Ausente» es que el listado no lo trae: con `down` el container no existe.
Hoy no avisa porque la foto del minuto anterior no lo tiene.)

En el §4, al final del punto 4 agregar: `Es
`TestReproduccionContraLaCopiaDeProduccion`, con la copia de `server-status
backup` del 17/09 en `SERVER_STATUS_COPIA`.` En el punto 3, cambiar «hace más de
10 minutos» por «hace más de 60 minutos».

En la tabla del §6, cambiar la fila de `cmd/server-status/main.go` por estas dos:

```markdown
| `cmd/server-status/reinicios.go` (nuevo) | `ventanaReinicios` y `guardarContainersYDetectar`: base, insert y detección |
| `cmd/server-status/main.go` (~296) | el ciclo llama a `guardarContainersYDetectar` |
```

y la de tests por `internal/store/store_test.go`, `cmd/server-status/reinicios_test.go`.

- [ ] **Step 3: Revisar que no entró ninguna IP y commitear**

Run: `git diff -U0 -- CLAUDE.md docs/`
Expected: solo los textos de arriba; ninguna IP ni casilla.

```bash
git add CLAUDE.md docs/superpowers/specs/2026-09-17-reinicio-durante-la-recreacion-design.md
git commit -m "docs: la base de los reinicios es el último arranque conocido"
```

---

## Task 6: Verificación completa (nada se commitea)

- [ ] **Step 1: Suite entera, vet y cross-compile**

```bash
go test ./... -race > "$SP/suite.txt" 2>&1; echo "exit=$?"
grep -E '^(--- FAIL|FAIL|ok)' "$SP/suite.txt"
go test ./internal/web -race -skip TestNovedadesOrdenaDeLoMasNuevoALoMasViejo
go vet ./...
make linux
```
Expected: en `suite.txt`, todos `ok` salvo `--- FAIL:
TestNovedadesOrdenaDeLoMasNuevoALoMasViejo` y `FAIL .../internal/web`, igual que
la línea de base. `internal/web` salteando ese test: `ok`. `vet` sin salida.
`make linux` compila con `CGO_ENABLED=0`.

- [ ] **Step 2: Mutaciones**

Cada una sobre el árbol commiteado. Después de cada una:
`git checkout -- <archivo>` y `git diff --exit-code` (tiene que salir 0).

**M1, la que no se negocia: la base vuelve a ser la foto del minuto anterior.**

```bash
sed -i '' 's/s.UltimoArranqueConocido(ventanaReinicios)/s.UltimoEstadoContainers()/' cmd/server-status/reinicios.go
git diff --stat
go test ./cmd/server-status -race -run 'TestRecreadoConElInspectFallidoAvisaAlTickSiguiente|TestLaBaseDeLosReiniciosEsElUltimoArranqueConocido' -v
SERVER_STATUS_COPIA="$SP/status-2026-09-17.db" go test ./cmd/server-status -race -run TestReproduccionContraLaCopiaDeProduccion -v
git checkout -- cmd/server-status/reinicios.go && git diff --exit-code
```
Expected: `git diff --stat` muestra 1 línea cambiada (si muestra 0, el `sed` no
aplicó y la mutación no prueba nada). FAIL en
`TestRecreadoConElInspectFallidoAvisaAlTickSiguiente`, en `conocido, cero,
nuevo`, `conocido, dos ceros, nuevo` y `visto hace justo 60 minutos`. La
reproducción: FAIL con `no dio el container_restart de supabase-auth`.

**M2: la consulta vuelve a mirar solo el último tick** (ventana cero).

```bash
sed -i '' 's/FROM container_samples) - ?/FROM container_samples) - 0 * ?/' internal/store/store.go
git diff --stat
go test ./cmd/server-status ./internal/store -race -run 'TestRecreado|TestLaBaseDeLosReinicios|TestUltimoArranqueConocido'
git checkout -- internal/store/store.go && git diff --exit-code
```
Expected: FAIL, como mínimo en
`TestRecreadoConElInspectFallidoAvisaAlTickSiguiente` y en
`TestUltimoArranqueConocidoSalteaElCeroYTraeElMasReciente` (caen también la
ventana del store y los casos de la tabla que avisan después de un cero o de un
hueco).

**M3: la ventana vuelve a 10 minutos.**

```bash
sed -i '' 's/const ventanaReinicios = time.Hour/const ventanaReinicios = 10 * time.Minute/' cmd/server-status/reinicios.go
git diff --stat
go test ./cmd/server-status -race -run TestLaBaseDeLosReiniciosEsElUltimoArranqueConocido -v
git checkout -- cmd/server-status/reinicios.go && git diff --exit-code
```
Expected: FAIL solo en `visto hace justo 60 minutos todavía es base`.

**M4: la ventana se cuenta desde el reloj.**

```bash
sed -i '' "s/AND ts >= (SELECT MAX(ts) FROM container_samples) - ?/AND ts >= CAST(strftime('%s','now') AS INTEGER) - ?/" internal/store/store.go
git diff --stat
go test ./cmd/server-status ./internal/store -race -run 'TestRecreado|TestLaBaseDeLosReinicios|TestUltimoArranqueConocido' -v
git checkout -- internal/store/store.go && git diff --exit-code
```
Expected: FAIL, como mínimo en
`TestUltimoArranqueConocidoCuentaLaVentanaDesdeElUltimoTick` y en `server-status
caído más que la ventana`. Los datos de los tests son del 17/09 y quedan afuera
de cualquier ventana contada desde el reloj de hoy, así que caen también los
demás casos que avisan: lo que importa es que esos dos no pueden pasar con esta
mutación.

**M5: la comparación deja de truncar al segundo** (prueba que el test ve los
nanosegundos después de pasar por SQLite).

```bash
sed -i '' 's/c.StartedAt.Truncate(time.Second).After(anterior.Truncate(time.Second))/c.StartedAt.After(anterior)/' internal/rules/eventos.go
git diff --stat
go test ./cmd/server-status -race -run TestRecreadoConElInspectFallidoAvisaAlTickSiguiente -v
git checkout -- internal/rules/eventos.go && git diff --exit-code
```
Expected: FAIL, y no por falta de aviso: a las 14:47 o a las 14:48 aparece
`supabase-kong` como reiniciado.

- [ ] **Step 3: Repo público y autoría**

```bash
git diff origin/main -- . > "$SP/diff.txt"
grep -nE '\b([0-9]{1,3}\.){3}[0-9]{1,3}\b' "$SP/diff.txt"; echo "grep_exit=$?"
gitleaks git --no-banner --redact .
git log origin/main..HEAD --format='%h %ae %s'
git log origin/main..HEAD --format=%B > "$SP/mensajes.txt"
grep -ci 'co-authored' "$SP/mensajes.txt"; echo "grep_exit=$?"
```
Expected: ningún match de IP (`grep_exit=1`); gitleaks sin hallazgos; los seis
commits (plan, store, refactor, fix, test, docs) con `69881939+juanandresdavila@users.noreply.github.com`; `0` trailers
de autoría.

- [ ] **Step 4: Cierre con la skill `verificacion`**

Invocar `verificacion` y pasar por lo que afirma este cambio: el test del 17/09,
las cinco mutaciones, la reproducción, el costo de la consulta, y que la
documentación (CLAUDE.md y spec) dice lo que el código hace.

- [ ] **Step 5: Memoria**

Actualizar (Edit anclado, nunca Write) `server-status-reinicio-en-recreacion-no-avisa`
en `0. Memoria/`: estado implementado sin deployar, las decisiones de Juan
(60 min, desde el último tick, copia del 17/09), dónde quedó la rama, y que el
«How to apply» de «no dar por hecho el aviso» vale hasta el deploy. Ajustar su
línea en el hub `proyecto-server-status` y en `MEMORY.md` si cambia el gancho.

---

## Task 7: ⛔ Integración y deploy, cada paso con ok explícito de Juan

- [ ] **Step 1: Push** (el hook de `pre-push` corre gitleaks)

```bash
git push -u origin claude/reinicio-en-recreacion
```

- [ ] **Step 2: PR**

`gh pr create` con el resumen, las decisiones del 17/09, la salida de la
reproducción y de las mutaciones, y el aviso de que el CI va a dar rojo por
`TestNovedadesOrdenaDeLoMasNuevoALoMasViejo`, que ya falla en `main`. Cuando
llegue el CI, mirar **qué** test falló: un rojo en `server-status` no prueba
nada del cambio hasta que ese test se arregle.

- [ ] **Step 3: Merge**

```bash
gh pr merge <N> --rebase
```
Nunca `--squash`.

- [ ] **Step 4: Deploy**

```bash
make deploy
ssh vps 'sudo journalctl -u server-status -n 30 --no-pager'
```
Expected: el servicio arriba, sin `ERROR` nuevos y sin eventos
`container_restart` falsos en los primeros ticks (el síntoma del 22/08 fue un
aviso por minuto para los 21 containers: mirar al menos 3 minutos).

- [ ] **Step 5: Prueba post-deploy (§4.5 del spec)**

Recrear un container propio y sin riesgo, elegido con Juan, y confirmar la fila
en `eventos` y el aviso por Telegram. Una recreación prueba el caso normal; el
de la ventana ya lo probó la Task 4.

---

## Cobertura del spec

| Spec | Dónde |
|---|---|
| §3 query nueva en el store | Task 1 |
| §3 `main.go` usa la query nueva, el panel no | Tasks 2 y 3 |
| §3 la guarda del cero se queda | Task 3, Step 4 |
| §3 tabla de secuencias | Task 3, `TestLaBaseDeLosReiniciosEsElUltimoArranqueConocido` |
| §4.1 test por SQLite con nanosegundos | Task 3, `TestRecreadoConElInspectFallidoAvisaAlTickSiguiente` |
| §4.2 mutación | Task 3, Step 2 (el rojo antes del arreglo) y Task 6, M1 y M2 |
| §4.3 regresiones | Task 3 (tabla) y Task 1 (ventana y base vacía) |
| §4.4 datos de producción | Task 4 |
| §4.5 después del deploy | Task 7, Step 5 |
| §6 comentario de la guarda y CLAUDE.md | Task 3, Step 4 y Task 5 |

## Lo que el plan resuelve y el spec no decía

- **Dónde vive la mutación.** El spec dice «volver `main.go` a
  `UltimoEstadoContainers()`». El ciclo de `correr()` no se puede testear sin
  Docker, así que la llamada pasa a `guardarContainersYDetectar` y la mutación se
  aplica ahí. El `grep` de la Task 2 asegura que `main.go` no lee la base por
  otro lado.
- **Cómo se reproduce «con el binario nuevo».** No hay subcomando de replay y
  no se agrega uno: la reproducción es un test que corre el mismo código que el
  binario sobre dos copias recortadas de la base real. No se toca producción
  salvo el `server-status backup`, que solo reescribe la copia que igual se
  rehace a las 04:00.

## Lo que este plan NO promete

- Distinguir «recreado» de «reiniciado», ni avisar que un container desapareció
  (§5 del spec).
- Arreglar `TestNovedadesOrdenaDeLoMasNuevoALoMasViejo`: hay una tarea aparte.

---

## Lo medido al ejecutarlo (17/09/2026)

Tasks 0 a 6 ejecutadas el 17/09/2026; la 7 (push, PR, merge, deploy) queda
esperando el ok de Juan. Todo lo de abajo se corrió en el worktree
`.claude/worktrees/reinicio-en-recreacion`, rama `claude/reinicio-en-recreacion`.

### Lo que el plan decía y resultó distinto

- **El CI rojo conocido dejó de existir.** Mientras se ejecutaba, `main` avanzó
  con el PR #27, que arregló `TestNovedadesOrdenaDeLoMasNuevoALoMasViejo`. Se
  rebaseó la rama sobre `14ef9f6` (sin conflictos) y la suite entera quedó en
  verde: `go test ./... -race -count=1` → 16 paquetes `ok`, ningún `FAIL`.
- **El `grep` de la Task 2** esperaba `UltimoEstadoContainers` en el subcomando
  `containers`. Ese subcomando le pregunta a Docker, no a la base: después del
  refactor `main.go` no llama a `UltimoEstadoContainers` en ningún lado.
- **Son 26 containers, no ~21.**
- **Siete commits y no seis**: este apartado va en uno propio.

### Reproducción contra la copia de producción

Copia: `server-status backup` en el VPS a las 16:21 UTC, sha256 igual en el VPS
y en la Mac, `PRAGMA integrity_check` = `ok`. Las filas de `supabase-auth` son
las del §1 del spec (14:47 con health vacío y `started_at` = 0).

```
SERVER_STATUS_COPIA=<copia> go test ./cmd/server-status -race -count=1 -run TestReproduccionContraLaCopiaDeProduccion -v
    reinicios_test.go:172: 14:48 supabase-auth arrancó 2026-09-17T14:47:52Z
    reinicios_test.go:200: evento: tipo=container_restart ocurrido=2026-09-17T14:48:00Z detalle="1 container arrancó de nuevo: supabase-auth"
--- PASS: TestReproduccionContraLaCopiaDeProduccion (4.00s)
```

### Mutaciones, sobre el árbol final

| | Mutación | Cae |
|---|---|---|
| M1 | base → `UltimoEstadoContainers()` | `TestRecreado…` («a las 14:48 hubo 0 eventos»); tabla: `conocido, cero, nuevo`, `dos ceros`, `justo 60 minutos`; reproducción: «no dio el container_restart de supabase-auth» |
| M2 | ventana `- 0 * ?` | `TestRecreado…`; tabla: los mismos tres; store: `SalteaElCero…` y `CuentaLaVentana…` |
| M3 | ventana de 10 min | solo `visto hace justo 60 minutos todavía es base` |
| M4 | ventana desde el reloj | `TestRecreado…`; tabla: `cero, nuevo`, `dos ceros`, `sin cero en el medio`, `justo 60`, `server-status caído`; store: `SalteaElCero…` y `CuentaLaVentana…` |
| M5 | sin truncar al segundo | `TestRecreado…`: «14:47 avisó … supabase-kong» |

### Barrido: la base vieja contra la nueva, sobre toda la historia

La suite no verifica un cambio de base o de ventana, porque los casos salen de la
misma cabeza que el código. Esto hace el replay del detector en SQL sobre la
copia, abierta con `sqlite3 -readonly` y tablas temporales:

```sql
CREATE TEMP TABLE ticks AS SELECT ts, LAG(ts) OVER (ORDER BY ts) AS prev
  FROM (SELECT DISTINCT ts FROM container_samples);
CREATE TEMP TABLE cs AS SELECT ts, name, started_at AS sa FROM container_samples;
CREATE UNIQUE INDEX temp.cs_nt ON cs(name, ts);
-- vieja: la foto del tick anterior
CREATE TEMP TABLE viejo AS SELECT r.ts, r.name FROM cs r
  JOIN ticks t ON t.ts = r.ts JOIN cs p ON p.name = r.name AND p.ts = t.prev
  WHERE r.sa > 0 AND p.sa > 0 AND r.sa > p.sa;
-- nueva: el último conocido, visto dentro de los 3600 s que terminan en el tick anterior
CREATE TEMP TABLE conocidos AS SELECT ts, name, sa, LAG(ts) OVER w AS kts, LAG(sa) OVER w AS ksa
  FROM cs WHERE sa > 0 WINDOW w AS (PARTITION BY name ORDER BY ts);
CREATE TEMP TABLE nuevo AS SELECT c.ts, c.name FROM conocidos c JOIN ticks t ON t.ts = c.ts
  WHERE c.kts IS NOT NULL AND c.kts >= t.prev - 3600 AND c.sa > c.ksa;
```

Sobre 56 991 ticks (09/08 02:09 a 17/09 16:20 UTC) y 1 326 447 filas:

- **El replay de la base vieja reproduce `eventos` exacto**: 8 pares
  (container, tick) en 7 ticks, los mismos 7 `container_restart` que guardó
  producción, en el mismo minuto y con los mismos nombres. Sin eso, el barrido no
  probaría nada.
- **Base nueva: 9 pares.** Vieja menos nueva: vacío. Nueva menos vieja: uno solo,
  `supabase-auth` a las 14:48 del 17/09 (base vista a las 14:46, cero a las
  14:47). Ningún aviso nuevo que no sea el que se perdió.
- **Resultado negativo**: cero arranques con `started_at` mayor que el último
  conocido que la base nueva siga sin avisar; cero `started_at` que vayan para
  atrás. Lo único que no avisa son 26 primeras apariciones, todas containers
  nuevos de verdad (el deploy de la migración 10 el 22/08, guacamole el 26/08,
  `supabase-gym-imgproxy` y `supabase-gym-storage` el 05/09).
- **Filas con `started_at = 0` desde la migración 10: una sola**, la de
  `supabase-auth` a las 14:47 del 17/09.

### Costo de la consulta, sobre la copia

`EXPLAIN QUERY PLAN` → `SEARCH container_samples USING INDEX
sqlite_autoindex_container_samples_1 (ts>?)`, con la subconsulta por `COVERING
INDEX` y `TEMP B-TREE FOR GROUP BY`. `.timer on`: 1,3 ms sobre 1 326 447 filas,
26 containers devueltos, todos del último tick.

### Repo público

Ninguna IPv4 en `git diff origin/main`. `gitleaks detect --log-opts=origin/main..HEAD`
y `gitleaks dir .`: `no leaks found`. Autor y committer de los commits: el
noreply; cero líneas de autoría de IA.
