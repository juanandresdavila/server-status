# Reglas de nivel desde una línea, plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** poder tocar una línea en `/logs`, decir "esto no es un WARN", y que ese
nivel valga para las líneas ya guardadas y para las que vengan.

**Architecture:** tres piezas que no se mezclan. `logs.Clasificar` sigue siendo
una función pura y no se toca; las reglas son un tipo aparte del mismo paquete
(`logs.Reglas`) que se compone encima (`logs.Nivelar`). El store guarda la tabla
`reglas_nivel` (migración 12) y aplica el efecto retroactivo con un solo
`INSERT ... ON CONFLICT` sobre `log_niveles`. El panel expone crear, listar y
borrar con el mismo molde server-rendered que ya usan resolver y archivar
incidentes. La ingesta relee las reglas una vez por tick del minuto.

**Tech Stack:** Go 1.22+ sin cgo, `modernc.org/sqlite`, SQLite con FTS5,
`html/template`, `net/http.ServeMux` con patrones por método.

## Global Constraints

Valen para TODAS las tareas. Cada una las hereda sin repetirlas.

- **TDD estricto** (superpowers:test-driven-development): primero el test que
  falla, correrlo y VER el fallo, después el código mínimo. Un paso que no puede
  fallar de verdad no es un test.
- **`logs.Clasificar` sigue siendo pura.** Nada de consultas, red ni reloj
  adentro del clasificador. Las reglas son un tipo aparte que se compone encima.
- 🚨 **Una sola definición de "coincide":** `strings.Contains` en Go e
  `instr(linea, ?) > 0` en SQL. **Nunca `LIKE`**, que es case-insensitive para
  ASCII mientras Go no lo es. **Sin regex.** Va con un test que corre los dos
  caminos sobre el mismo corpus y falla con una sola línea de diferencia.
- **El store abre UNA sola conexión a SQLite.** El UPDATE retroactivo está
  medido: 43 679 filas en 9,4 s sobre 925 000, y durante esos 9 s la base no
  está para el ciclo del minuto. Es tolerable porque es manual y explícito, y
  eso tiene que quedar **escrito en el código**, no solo en este plan.
- **Las reglas se releen una vez por tick del minuto.** Sin mutex, sin
  `atomic.Pointer`, sin invalidación desde el handler web.
- **Borrar una regla re-nivela las filas afectadas**: `Clasificar` más las
  reglas que quedan. El undo es exacto, no aproximado.
- **Sin cgo** (invariante 7). `make linux` y CI lo verifican con `CGO_ENABLED=0`.
- **Verificar sin pipes.** `go test ./... | tail` devuelve el exit code de `tail`
  y tapa el fallo; ya pasó en este repo. Comandos sueltos.
- **UI server-rendered, sin JS nuevo**, con el molde de resolver/archivar
  incidentes: acción por POST, campo `volver` filtrado por `rutaPropia`. Panel
  bilingüe: cada texto nuevo va a `internal/web/idiomas.go` en `es` y en `en`.
- 🚨 **No se toca el VPS.** Hay una medición de egress corriendo y producción es
  su control positivo. Este plan llega hasta el código y los tests; **el deploy
  lo decide Juan**, después de revisar.
- **Repo público:** ninguna IP del VPS entra a ningún archivo. Las `172.19.x.x`
  de los corpus son de la red interna de Docker y ya están en el repo.
- **Commits** con prefijo convencional y **sin `Co-Authored-By` de Claude** ni
  ninguna línea de autoría de IA.

---

## File Structure

| Archivo | Qué le toca |
|---|---|
| `internal/logs/reglas.go` (nuevo) | `Regla`, `Reglas`, `Coincide`, `Aplicar`, `Nivelar`, `PatronSugerido`. Puro: no importa nada del proyecto. |
| `internal/logs/reglas_test.go` (nuevo) | Tabla de casos de `Aplicar` y de `PatronSugerido`. |
| `internal/model/model.go` | `ReglaNivel` (la fila persistida) y `LineaLog.Rowid`. |
| `internal/store/store.go` | Migración 12, `dondeCoincide`, `ContarPorPatron`, `ReglasNivel`, `ReglasParaAplicar`, `CrearReglaNivel`, `BorrarReglaNivel`, `LineaPorRowid`, `selectLogs` unificado, `BackfillNiveles` con container. |
| `internal/store/store_test.go` | Corpus Go vs SQL, round trip, preview == efecto, migración 12. |
| `cmd/server-status/main.go` | Relee las reglas una vez por tick, se las pasa a `ingerirLogs`/`nuevasLineas` y al backfill. |
| `cmd/server-status/ingesta_test.go` | Que la línea recién ingerida salga con la regla aplicada. |
| `internal/web/panel.go` | `Datos` crece cinco métodos; rutas `/logs/reglas`, `/logs/reglas/nueva`, `POST /logs/reglas`, `POST /logs/reglas/{id}/borrar`; conteo en el encabezado de `/logs`. |
| `internal/web/plantillas/regla-nueva.html` (nuevo) | Form único con preview y botón de recalcular. |
| `internal/web/plantillas/reglas.html` (nuevo) | Lista de reglas activas con motivo, fecha, coincidencias y borrar. |
| `internal/web/plantillas/logs.html` | La celda del nivel pasa a ser el control; encabezado con "N reglas activas". |
| `internal/web/idiomas.go` | Textos nuevos en `es` y `en`. |
| `internal/web/panel_test.go` | `datosFalsos` implementa lo nuevo; tests de las cuatro rutas. |
| `CLAUDE.md` | Entrada de la tanda. |

---

## Task 1: Las reglas como tipo puro en `internal/logs`

**Files:**
- Create: `internal/logs/reglas.go`
- Test: `internal/logs/reglas_test.go`

**Interfaces:**
- Consumes: `logs.Nivel`, `logs.Clasificar(linea, stream string) Nivel` (ya existen).
- Produces:
  - `type Regla struct{ Patron, Container string; Nivel Nivel }`
  - `func (r Regla) Coincide(linea, container string) bool`
  - `type Reglas []Regla`
  - `func (rs Reglas) Aplicar(n Nivel, linea, container string) Nivel`
  - `func Nivelar(rs Reglas, linea, stream, container string) Nivel`

- [ ] **Step 1: Escribir el test que falla**

En `internal/logs/reglas_test.go` (package `logs`, como `nivel_test.go`):

```go
package logs

import "testing"

// La regla de conflictos es "gana la última que matchea", por orden de id. Es
// arbitraria pero predecible, y sin test se convierte en "depende del orden en
// que las devolvió el SELECT".
func TestReglasAplicar(t *testing.T) {
	const kong = `172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "curl/8.7.1"`

	casos := []struct {
		nombre string
		reglas Reglas
		quiero Nivel
	}{
		{"sin reglas devuelve lo que le pasaron", nil, Warn},
		{
			"una que matchea baja el nivel",
			Reglas{{Patron: `"GET /auth/v1/health HTTP/1.1" 401`, Nivel: Trace}},
			Trace,
		},
		{
			"container vacío es todos los containers",
			Reglas{{Patron: "401", Container: "", Nivel: Trace}},
			Trace,
		},
		{
			"una regla de otro container no toca esta línea",
			Reglas{{Patron: "401", Container: "otro-kong", Nivel: Trace}},
			Warn,
		},
		{
			"dos que se pisan: gana la última",
			Reglas{
				{Patron: "401", Nivel: Trace},
				{Patron: "401", Nivel: Error},
			},
			Error,
		},
		{
			"la que no matchea no le gana a la que sí",
			Reglas{
				{Patron: "401", Nivel: Trace},
				{Patron: "no-esta-en-la-linea", Nivel: Error},
			},
			Trace,
		},
		{
			// strings.Contains es case-sensitive y el LIKE de SQL no. Si esto
			// se relaja, el número del preview deja de ser el que queda.
			"el patrón es case-sensitive",
			Reglas{{Patron: "GET /AUTH/V1/HEALTH", Nivel: Trace}},
			Warn,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := c.reglas.Aplicar(Warn, kong, "supabase-kong"); got != c.quiero {
				t.Errorf("Aplicar = %q, quiero %q", got, c.quiero)
			}
		})
	}
}

// Nivelar es la ÚNICA composición de clasificador y reglas: la usan la ingesta,
// el backfill y el borrado de una regla. Si cada una compusiera por su cuenta,
// el nivel guardado dependería de por dónde entró la línea.
func TestNivelarComponeClasificarYReglas(t *testing.T) {
	const kong = `172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "curl/8.7.1"`

	if got := Nivelar(nil, kong, "stdout", "supabase-kong"); got != Warn {
		t.Fatalf("sin reglas Nivelar = %q, quiero WARN (lo que dice Clasificar)", got)
	}
	rs := Reglas{{Patron: `401 96 "-" "curl/8.7.1"`, Nivel: Trace}}
	if got := Nivelar(rs, kong, "stdout", "supabase-kong"); got != Trace {
		t.Errorf("con la regla puesta Nivelar = %q, quiero TRACE", got)
	}
}
```

- [ ] **Step 2: Correr el test y verlo fallar**

```bash
go test ./internal/logs/ -run 'TestReglasAplicar|TestNivelar' -v
```

Esperado: FAIL de compilación, `undefined: Reglas`, `undefined: Nivelar`.

- [ ] **Step 3: Escribir el código mínimo**

`internal/logs/reglas.go`:

```go
package logs

import "strings"

// Regla baja o sube el nivel de las líneas que contienen un patrón.
//
// Vive acá y no adentro de Clasificar a propósito: Clasificar es una función
// pura, se testea sin base, sin red y sin reloj, y meterle una consulta la
// convertiría en otra cosa. Las reglas se COMPONEN encima, con Nivelar.
//
// Container vacío significa "todos los containers".
type Regla struct {
	Patron    string
	Container string
	Nivel     Nivel
}

// Coincide es la definición de "esta línea matchea", en Go.
//
// strings.Contains y nada más: substring, case-sensitive, sin regex. El
// equivalente en SQL es instr(linea, ?) > 0, en store.dondeCoincide, y hay un
// test que corre los dos caminos sobre el mismo corpus. Si divergen, el número
// que el usuario confirma en el preview no es el que le queda aplicado.
//
// LIKE queda descartado por eso mismo: es case-insensitive para ASCII y esto
// no. Regex también: invita a patrones catastróficos y abre entre Go y SQL un
// abismo de semántica que no se puede cerrar con un test.
func (r Regla) Coincide(linea, container string) bool {
	if r.Container != "" && r.Container != container {
		return false
	}
	return strings.Contains(linea, r.Patron)
}

// Reglas es el conjunto en el orden en que se aplican: el de la columna id.
type Reglas []Regla

// Aplicar devuelve el nivel que corresponde después de las reglas. Si ninguna
// matchea devuelve el que le pasaron.
//
// Gana la ÚLTIMA que matchea. Es arbitrario pero predecible, que es lo único
// que se le puede pedir a un desempate entre dos reglas que alguien escribió
// para la misma línea.
func (rs Reglas) Aplicar(n Nivel, linea, container string) Nivel {
	for _, r := range rs {
		if r.Coincide(linea, container) {
			n = r.Nivel
		}
	}
	return n
}

// Nivelar es la composición completa: lo que dice el clasificador, corregido
// por las reglas. Es la única forma en que se calcula el nivel guardado de una
// línea, la ingiera el tick, la reprocese el backfill o la re-nivele el
// borrado de una regla.
func Nivelar(rs Reglas, linea, stream, container string) Nivel {
	return rs.Aplicar(Clasificar(linea, stream), linea, container)
}
```

- [ ] **Step 4: Correr el test y verlo pasar**

```bash
go test ./internal/logs/ -run 'TestReglasAplicar|TestNivelar' -v
```

Esperado: PASS en los ocho subtests.

- [ ] **Step 5: Correr el paquete entero, sin pipes**

```bash
go test ./internal/logs/ -race
```

Esperado: `ok`. Confirma que agregar el archivo no rompió `Clasificar`.

- [ ] **Step 6: Commit**

```bash
git add internal/logs/reglas.go internal/logs/reglas_test.go
git commit -m "feat(logs): reglas de nivel como tipo aparte, con Clasificar intacta"
```

---

## Task 2: `PatronSugerido`

La línea entera es única porque trae timestamp e IP, así que el patrón hay que
sacarlo de adentro. Es un punto de partida **editable**, no una promesa: que el
adivinador falle no rompe nada, que el conteo mienta sí.

**Files:**
- Modify: `internal/logs/reglas.go`
- Test: `internal/logs/reglas_test.go`

**Interfaces:**
- Produces: `func PatronSugerido(linea string) string`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/logs/reglas_test.go`:

```go
// Las dos primeras líneas son reales: la de Kong es la misma que ya usa
// nivel_test.go, y la de Postgres tiene el formato exacto que loguean los dos
// Supabase. El acierto medido de la primera fue 8625 de 8625.
func TestPatronSugerido(t *testing.T) {
	casos := []struct {
		nombre, linea, quiero string
	}{
		{
			"acceso de Kong: se van la IP y el corchete de fecha",
			`172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
			`"GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
		},
		{
			// El pid entre corchetes SOBREVIVE, y está bien que se vea: el
			// campo es editable y el conteo previo dice enseguida que un
			// patrón con pid matchea muy poco.
			"Postgres: se va el timestamp con su zona",
			` 2026-08-22 07:38:00.012 UTC [38] LOG:  cron job 1 completed: 0 rows`,
			`[38] LOG:  cron job 1 completed: 0 rows`,
		},
		{
			"ISO-8601 con T y Z al principio",
			`2026-08-31T19:50:30.123Z Schema cache loaded 13 Relations, 24 Relationships`,
			`Schema cache loaded 13 Relations, 24 Relationships`,
		},
		{
			// Sin esto, "el proceso arrancó bien" quedaría en "arrancó bien":
			// tres palabras comidas de una frase común.
			"una línea en prosa vuelve entera",
			`el proceso arrancó bien y no hay nada que sacarle`,
			`el proceso arrancó bien y no hay nada que sacarle`,
		},
		{"una línea vacía no explota", "", ""},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := PatronSugerido(c.linea); got != c.quiero {
				t.Errorf("PatronSugerido = %q\nquiero          %q", got, c.quiero)
			}
		})
	}
}
```

- [ ] **Step 2: Correr el test y verlo fallar**

```bash
go test ./internal/logs/ -run TestPatronSugerido -v
```

Esperado: FAIL de compilación, `undefined: PatronSugerido`.

- [ ] **Step 3: Escribir el código mínimo**

Agregar a `internal/logs/reglas.go`:

```go
import (
	"regexp"
	"strings"
)

var (
	// El "172.19.0.2 - - " del formato de acceso común. Se pide que lo siga el
	// corchete de la fecha, y por eso el corchete se captura y se devuelve: sin
	// esa condición, la regla se comería las tres primeras palabras de
	// cualquier frase en prosa.
	rePrefijoAcceso = regexp.MustCompile(`^\S+ \S+ \S+ (\[)`)

	// [31/Aug/2026:19:50:30 +0000]
	reFechaCorchete = regexp.MustCompile(`^\[\d{1,2}/[A-Za-z]{3}/\d{4}(?::\d{2}){3} [+-]\d{4}\]\s*`)

	// 2026-08-31T19:50:30.123Z, 2026-08-22 07:38:00.012 UTC, con o sin zona.
	reISOInicial = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?(?: [A-Z]{2,4})?\s*`)
)

// PatronSugerido saca de la línea la parte que se repite, tirando lo que la
// hace única. En este orden: la IP inicial del formato de acceso, el corchete
// de fecha, y un timestamp ISO-8601 al principio.
//
// Es un PUNTO DE PARTIDA EDITABLE, no una promesa. Que el adivinador falle no
// rompe nada: el campo se corrige a mano y el conteo previo dice si el patrón
// quedó demasiado ancho o demasiado angosto. Que el conteo mienta sí rompería
// algo, y por eso ahí no hay heurística ninguna.
func PatronSugerido(linea string) string {
	p := strings.TrimSpace(linea)
	p = rePrefijoAcceso.ReplaceAllString(p, "$1")
	p = reFechaCorchete.ReplaceAllString(p, "")
	p = reISOInicial.ReplaceAllString(p, "")
	return strings.TrimSpace(p)
}
```

- [ ] **Step 4: Correr el test y verlo pasar**

```bash
go test ./internal/logs/ -run TestPatronSugerido -v
```

Esperado: PASS en los cinco subtests. Si el de Postgres falla por el pid, **no
se toca el test**: el pid queda, es lo acordado.

- [ ] **Step 5: Commit**

```bash
git add internal/logs/reglas.go internal/logs/reglas_test.go
git commit -m "feat(logs): patrón sugerido a partir de una línea"
```

---

## Task 3: 🚨 Una sola definición de "coincide", en SQL

Esta es la tarea que sostiene la función entera. Si el conteo del preview y el
efecto se separan, el usuario confirma un número y le queda otro, que es
exactamente la clase de verificación que mide otra cosa.

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `logs.Regla.Coincide` (Task 1).
- Produces:
  - `func dondeCoincide(patron, container string) (string, []any)` (privada del paquete `store`)
  - `func (s *Store) ContarPorPatron(patron, container string) (int, error)`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/store/store_test.go` (package `store_test`):

```go
// El corpus es de líneas reales del VPS, con variantes de caja a propósito.
// Es lo que hace que el test pueda fallar: con LIKE, el patrón "egress-probe"
// también matchearía "EGRESS-PROBE" y el conteo de SQL se iría por encima del
// de Go en exactamente una línea.
var corpusReal = []string{
	`172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
	`172.19.0.2 - - [31/Aug/2026:19:51:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
	`172.19.0.2 - - [31/Aug/2026:19:52:30 +0000] "GET /auth/v1/health HTTP/1.1" 200 107 "-" "server-status"`,
	`172.19.0.2 - - [31/Aug/2026:15:34:35 +0000] "GET /rest/v1/workouts?select=id HTTP/1.1" 401 79 "https://gym-tracker-brown-one.vercel.app/" "Mozilla/5.0"`,
	`172.19.0.2 - - [31/Aug/2026:15:35:35 +0000] "GET /egress-probe HTTP/1.1" 404 79 "-" "curl/8.7.1"`,
	`172.19.0.2 - - [31/Aug/2026:15:36:35 +0000] "GET /health HTTP/1.1" 200 79 "-" "EGRESS-PROBE"`,
	` 2026-08-22 07:38:00.012 UTC [38] ERROR:  relation "x" does not exist`,
	` 2026-08-22 07:38:01.012 UTC [38] LOG:  cron job 1 completed: 0 rows`,
	`{"component":"api","level":"info","method":"GET","msg":"400: Unsupported provider","path":"/authorize"}`,
	"\tselect v.planned_minutes, error from con_subs;",
}

// El mismo corpus por los dos caminos. Si instr() y strings.Contains difieren
// en UNA sola línea, este test lo dice.
func TestGoYSQLCuentanIgual(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 31, 19, 0, 0, 0, time.UTC)
	if err := s.InsertLogs(lineas(base, "supabase-kong", corpusReal...)); err != nil {
		t.Fatal(err)
	}
	// La misma línea en otro container: sin esto el scope por container no se
	// ejercita y una regla global pasaría por una acotada.
	if err := s.InsertLogs(lineas(base, "otro-kong", corpusReal[0])); err != nil {
		t.Fatal(err)
	}

	patrones := []string{
		`"GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
		"egress-probe",
		"EGRESS-PROBE",
		"ERROR:",
		"error",
		"cron job",
		`"level":"info"`,
		"no aparece en ninguna línea",
	}
	containers := []string{"", "supabase-kong", "otro-kong"}

	todas := append(append([]model.LineaLog{}, lineas(base, "supabase-kong", corpusReal...)...),
		lineas(base, "otro-kong", corpusReal[0])...)

	for _, p := range patrones {
		for _, c := range containers {
			enSQL, err := s.ContarPorPatron(p, c)
			if err != nil {
				t.Fatalf("ContarPorPatron(%q, %q): %v", p, c, err)
			}
			r := logs.Regla{Patron: p, Container: c}
			enGo := 0
			for _, l := range todas {
				if r.Coincide(l.Linea, l.Container) {
					enGo++
				}
			}
			if enSQL != enGo {
				t.Errorf("patrón %q container %q: SQL contó %d y Go %d", p, c, enSQL, enGo)
			}
		}
	}
}
```

`store_test.go` necesita importar `"github.com/juanandresdavila/server-status/internal/logs"`.

- [ ] **Step 2: Correr el test y verlo fallar**

```bash
go test ./internal/store/ -run TestGoYSQLCuentanIgual -v
```

Esperado: FAIL de compilación, `s.ContarPorPatron undefined`.

- [ ] **Step 3: Escribir el código mínimo**

Agregar a `internal/store/store.go`, al lado de `filtroLogs`:

```go
// dondeCoincide es la ÚNICA definición SQL de "esta línea matchea el patrón".
// La usan el conteo previo, la aplicación retroactiva y el borrado, así que los
// tres no pueden diferir ni queriendo.
//
// instr() y NUNCA LIKE: LIKE es case-insensitive para ASCII y el equivalente en
// Go, logs.Regla.Coincide con strings.Contains, no lo es. Con LIKE, el número
// que el usuario confirma en el preview no es el que le queda aplicado.
// TestGoYSQLCuentanIgual corre los dos caminos sobre el mismo corpus.
//
// Sin regex: además de invitar a patrones catastróficos, la semántica de
// SQLite y la de Go no coinciden y no hay test que pueda cerrar ese abismo.
func dondeCoincide(patron, container string) (string, []any) {
	q := ` WHERE instr(l.linea, ?) > 0`
	args := []any{patron}
	if container != "" {
		q += ` AND l.container = ?`
		args = append(args, container)
	}
	return q, args
}

// ContarPorPatron dice cuántas líneas guardadas matchean. Es el número que el
// preview muestra antes de confirmar, y TIENE que ser el mismo que devuelve
// CrearReglaNivel: es la afirmación central de toda esta función.
//
// ⚠️ Es un scan completo: en la tabla FTS5 `logs`, ts y container son
// UNINDEXED. Sobre las 925 000 filas de la base del VPS eso son segundos, y el
// store abre UNA sola conexión, así que mientras dura el ciclo del minuto
// espera. Es tolerable porque lo dispara una persona apretando un botón; no lo
// sería para algo automático.
func (s *Store) ContarPorPatron(patron, container string) (int, error) {
	donde, args := dondeCoincide(patron, container)
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM logs l`+donde, args...).Scan(&n)
	return n, err
}
```

- [ ] **Step 4: Correr el test y verlo pasar**

```bash
go test ./internal/store/ -run TestGoYSQLCuentanIgual -v
```

Esperado: PASS.

- [ ] **Step 5: Confirmar que el test sirve para algo**

Cambiar a mano `instr(l.linea, ?) > 0` por `l.linea LIKE '%' || ? || '%'`,
correr el test y **verlo fallar** con el patrón `egress-probe` (SQL cuenta una
de más, la de `"EGRESS-PROBE"`). Revertir el cambio y volver a correrlo.

```bash
go test ./internal/store/ -run TestGoYSQLCuentanIgual -v
```

Esperado: primero FAIL con "SQL contó 3 y Go 2" (o similar), después PASS. Un
test que no falla cuando se rompe lo que vigila no vigila nada.

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): instr como única definición de coincidencia, con test contra Go"
```

---

## Task 4: Migración 12, guardar una regla y aplicarla a lo viejo

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/store/store.go` (migración 12 al final del slice, métodos nuevos)
- Modify: `internal/store/export_test.go` (helper para simular una fila sin nivel)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `dondeCoincide` (Task 3), `logs.Regla`/`logs.Reglas` (Task 1).
- Produces:
  - `type model.ReglaNivel struct{ ID int64; Patron, Container, Nivel, Motivo string; Creada time.Time }`
  - `func (s *Store) ReglasNivel() ([]model.ReglaNivel, error)`
  - `func (s *Store) ReglasParaAplicar() (logs.Reglas, error)`
  - `func (s *Store) CrearReglaNivel(r model.ReglaNivel) (int64, int, error)`

- [ ] **Step 1: Escribir el test que falla**

Primero, cambiar el número en el test que ya existe (`internal/store/store_test.go`):

```go
func TestUltimaMigracionAplicada(t *testing.T) {
	s := abrir(t)
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != 12 {
		t.Errorf("versión = %d, quería 12", v)
	}
}
```

Y agregar los tests de la tarea:

```go
// Las reglas vuelven en orden de id, que es el orden en que se aplican: gana
// la última que matchea. Si el SELECT no lo fija, el desempate pasa a depender
// de cómo le pinte a SQLite devolver las filas.
func TestReglasNivelRoundTrip(t *testing.T) {
	s := abrir(t)
	creada := time.Date(2026, 8, 31, 20, 0, 0, 0, time.UTC)

	primera, _, err := s.CrearReglaNivel(model.ReglaNivel{
		Patron: "egress-probe", Container: "", Nivel: "TRACE",
		Motivo: "es nuestra propia sonda", Creada: creada,
	})
	if err != nil {
		t.Fatal(err)
	}
	segunda, _, err := s.CrearReglaNivel(model.ReglaNivel{
		Patron: "cron job", Container: "supabase-db", Nivel: "ERROR",
		Motivo: "quiero verlo en la línea de tiempo", Creada: creada.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	rs, err := s.ReglasNivel()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].ID != primera || rs[1].ID != segunda {
		t.Fatalf("reglas = %+v, quería las dos en orden de id", rs)
	}
	if rs[0].Motivo != "es nuestra propia sonda" || !rs[0].Creada.Equal(creada) {
		t.Errorf("la primera volvió incompleta: %+v", rs[0])
	}
	if rs[1].Container != "supabase-db" || rs[1].Nivel != "ERROR" {
		t.Errorf("la segunda volvió incompleta: %+v", rs[1])
	}

	aplicar, err := s.ReglasParaAplicar()
	if err != nil {
		t.Fatal(err)
	}
	quiero := logs.Reglas{
		{Patron: "egress-probe", Container: "", Nivel: logs.Trace},
		{Patron: "cron job", Container: "supabase-db", Nivel: logs.Error},
	}
	if !reflect.DeepEqual(aplicar, quiero) {
		t.Errorf("ReglasParaAplicar = %+v, quería %+v", aplicar, quiero)
	}
}

// LA afirmación central: el número que se muestra antes de confirmar es el
// número de filas que quedan cambiadas. Si divergen, el preview es decorativo.
//
// La regla SUBE a ERROR en vez de bajar a TRACE a propósito: el 401 de la
// sonda propia ya es TRACE desde el arreglo del clasificador, así que una
// regla a TRACE no dejaría ver ninguna diferencia y este test pasaría igual
// con la aplicación retroactiva rota. De paso ejercita la otra dirección, que
// también existe: subir algo que el clasificador subestimó.
func TestElPreviewEsExactamenteElEfecto(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 31, 19, 0, 0, 0, time.UTC)
	if err := s.InsertLogs(lineas(base, "supabase-kong", corpusReal...)); err != nil {
		t.Fatal(err)
	}
	// La MISMA línea en otro container: la regla está acotada y no la toca.
	if err := s.InsertLogs(lineas(base, "otro-kong", corpusReal[0])); err != nil {
		t.Fatal(err)
	}

	const patron = `"-" "egress-probe"`
	previo, err := s.ContarPorPatron(patron, "supabase-kong")
	if err != nil {
		t.Fatal(err)
	}
	if previo != 2 {
		t.Fatalf("el preview contó %d, quería las 2 líneas de la sonda", previo)
	}

	_, filas, err := s.CrearReglaNivel(model.ReglaNivel{
		Patron: patron, Container: "supabase-kong", Nivel: "ERROR",
		Motivo: "quiero verla en la línea de tiempo", Creada: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filas != previo {
		t.Errorf("el preview dijo %d y cambiaron %d filas", previo, filas)
	}

	// Y el efecto se ve donde importa, que es la vista.
	enError, err := s.BuscarLogs("", "supabase-kong", []string{"ERROR"},
		time.Time{}, base.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	quedaron := 0
	for _, l := range enError {
		if strings.Contains(l.Linea, patron) {
			quedaron++
		}
	}
	if quedaron != previo {
		t.Errorf("quedaron %d líneas en ERROR, quería %d", quedaron, previo)
	}

	// La del otro container no se toca: el scope es parte de la regla.
	ajenas, err := s.BuscarLogs("", "otro-kong", []string{"ERROR"},
		time.Time{}, base.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ajenas) != 0 {
		t.Errorf("la regla acotada a supabase-kong tocó al otro container: %+v", ajenas)
	}
}

// Una fila anterior a la migración 9 que el backfill todavía no alcanzó NO
// tiene su fila en log_niveles. El preview la cuenta igual, porque sale de
// logs. Con un UPDATE pelado no se tocaría, y el número confirmado sería más
// grande que el aplicado: por eso la aplicación retroactiva es un upsert.
func TestLaReglaAlcanzaALasFilasSinNivel(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 31, 19, 0, 0, 0, time.UTC)
	if err := s.InsertLogs(lineas(base, "kong", "ruido de la sonda", "otra cosa")); err != nil {
		t.Fatal(err)
	}
	if err := s.OlvidarNivelParaTest(1); err != nil {
		t.Fatal(err)
	}

	previo, err := s.ContarPorPatron("ruido de la sonda", "")
	if err != nil {
		t.Fatal(err)
	}
	_, filas, err := s.CrearReglaNivel(model.ReglaNivel{
		Patron: "ruido de la sonda", Nivel: "TRACE",
		Motivo: "la fila no tenía nivel", Creada: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filas != previo || filas != 1 {
		t.Fatalf("preview %d, filas %d, quería 1 y 1", previo, filas)
	}

	got, err := s.BuscarLogs("", "", []string{"TRACE"}, time.Time{}, base.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Linea != "ruido de la sonda" {
		t.Errorf("got = %+v, quería la línea sin nivel ya en TRACE", got)
	}
}
```

`store_test.go` necesita importar `"reflect"` y `"strings"` si todavía no los tiene.

- [ ] **Step 2: Correr los tests y verlos fallar**

```bash
go test ./internal/store/ -run 'TestUltimaMigracionAplicada|TestReglasNivelRoundTrip|TestElPreviewEsExactamenteElEfecto|TestLaReglaAlcanzaALasFilasSinNivel' -v
```

Esperado: FAIL de compilación (`s.CrearReglaNivel undefined`,
`model.ReglaNivel undefined`, `s.OlvidarNivelParaTest undefined`). Al resolver
la compilación, `TestUltimaMigracionAplicada` falla con `versión = 11, quería 12`
hasta que exista la migración.

- [ ] **Step 3: Escribir el código mínimo**

En `internal/model/model.go`, al lado de `LineaLog`:

```go
// ReglaNivel es una corrección guardada del nivel de una línea: "todo lo que
// contiene este patrón es TRACE".
//
// Nivel y Container son strings por la misma razón que en LineaLog: este
// paquete no importa nada del proyecto. Quien las aplica es internal/logs.
//
// Motivo NO es decorativo y no puede quedar vacío: dentro de tres meses, una
// regla sin motivo es una regla que nadie se va a animar a borrar.
type ReglaNivel struct {
	ID        int64
	Patron    string
	Container string // "" = todos
	Nivel     string // TRACE | INFO | WARN | ERROR
	Motivo    string
	Creada    time.Time
}
```

En `internal/store/store.go`, agregar la migración 12 **al final** del slice
`migraciones` (nunca se edita una ya aplicada):

```go
	// Las reglas de nivel corrigen lo que el clasificador no puede saber: que
	// el 401 que Kong le devuelve a nuestra propia sonda no es un problema del
	// sistema. Sin esto, cada container ruidoso nuevo cuesta un commit, un
	// cross-compile, un deploy y un UPDATE a mano, que es lo que costó el
	// 31/08/2026, cuando 8625 de los 8625 WARN de 24 h eran esa sonda.
	//
	// Se aplican por orden de id y gana la última que matchea: arbitrario,
	// pero predecible.
	`CREATE TABLE reglas_nivel (
		id        INTEGER PRIMARY KEY,
		patron    TEXT NOT NULL,
		container TEXT NOT NULL,   -- '' = todos
		nivel     TEXT NOT NULL,   -- TRACE | INFO | WARN | ERROR
		motivo    TEXT NOT NULL,
		creada    INTEGER NOT NULL
	) STRICT;`,
```

Y los métodos:

```go
// consultador es lo que necesitan las lecturas de reglas, para poder correr
// tanto sobre la base como adentro de una transacción.
type consultador interface {
	Query(string, ...any) (*sql.Rows, error)
}

// ReglasNivel devuelve las reglas guardadas EN ORDEN DE ID, que es el orden en
// que se aplican. Es la lista que muestra el panel.
func (s *Store) ReglasNivel() ([]model.ReglaNivel, error) { return leerReglas(s.db) }

func leerReglas(q consultador) ([]model.ReglaNivel, error) {
	filas, err := q.Query(`SELECT id, patron, container, nivel, motivo, creada
		FROM reglas_nivel ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.ReglaNivel
	for filas.Next() {
		var r model.ReglaNivel
		var creada int64
		if err := filas.Scan(&r.ID, &r.Patron, &r.Container, &r.Nivel, &r.Motivo, &creada); err != nil {
			return nil, err
		}
		r.Creada = time.Unix(creada, 0).UTC()
		out = append(out, r)
	}
	return out, filas.Err()
}

// ReglasParaAplicar es lo mismo reducido a lo que hace falta para nivelar una
// línea. La conversión vive en UN solo lugar a propósito: repetirla en el
// ingestor y acá es la forma segura de que un día difieran.
func (s *Store) ReglasParaAplicar() (logs.Reglas, error) {
	filas, err := s.ReglasNivel()
	if err != nil {
		return nil, err
	}
	return reglasDe(filas), nil
}

func reglasDe(filas []model.ReglaNivel) logs.Reglas {
	out := make(logs.Reglas, 0, len(filas))
	for _, f := range filas {
		out = append(out, logs.Regla{Patron: f.Patron, Container: f.Container, Nivel: logs.Nivel(f.Nivel)})
	}
	return out
}

// CrearReglaNivel guarda la regla y la aplica a lo que ya estaba guardado.
// Devuelve el id y cuántas filas quedaron con el nivel nuevo, que TIENE que ser
// el mismo número que devolvió ContarPorPatron con el mismo patrón y container.
//
// ⚠️ Las dos cosas van en la misma transacción y el UPDATE está medido: 43 679
// filas en 9,4 s sobre las 925 000 de la base del VPS. El store abre UNA sola
// conexión a SQLite, así que durante esos 9 segundos el ciclo del minuto no
// tiene base: el tick de muestreo se atrasa y los logs de ese minuto entran en
// el siguiente. Es tolerable porque esto lo dispara una persona apretando un
// botón; no lo sería para algo automático ni para algo por lotes en background.
func (s *Store) CrearReglaNivel(r model.ReglaNivel) (int64, int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO reglas_nivel (patron, container, nivel, motivo, creada)
		VALUES (?,?,?,?,?)`, r.Patron, r.Container, r.Nivel, r.Motivo, r.Creada.Unix())
	if err != nil {
		return 0, 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}

	donde, args := dondeCoincide(r.Patron, r.Container)
	// INSERT ... ON CONFLICT y no un UPDATE pelado: una fila anterior a la
	// migración 9 que el backfill todavía no alcanzó no tiene fila en
	// log_niveles y un UPDATE no la tocaría. El preview la cuenta igual,
	// porque sale de logs, así que con UPDATE el número confirmado sería más
	// grande que el aplicado. Acá el upsert la crea.
	res, err = tx.Exec(`INSERT INTO log_niveles (rowid, nivel)
		SELECT l.rowid, ? FROM logs l`+donde+`
		ON CONFLICT(rowid) DO UPDATE SET nivel = excluded.nivel`,
		append([]any{r.Nivel}, args...)...)
	if err != nil {
		return 0, 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return id, int(n), nil
}
```

En `internal/store/export_test.go`:

```go
// OlvidarNivelParaTest borra la fila lateral de una línea, para simular lo que
// dejó la migración 9: filas viejas sin nivel hasta que el backfill las
// alcance. En producción eso no se provoca, se hereda.
func (s *Store) OlvidarNivelParaTest(rowid int64) error {
	_, err := s.db.Exec(`DELETE FROM log_niveles WHERE rowid = ?`, rowid)
	return err
}
```

- [ ] **Step 4: Correr los tests y verlos pasar**

```bash
go test ./internal/store/ -run 'TestUltimaMigracionAplicada|TestReglasNivelRoundTrip|TestElPreviewEsExactamenteElEfecto|TestLaReglaAlcanzaALasFilasSinNivel' -v
```

Esperado: PASS los cuatro.

- [ ] **Step 5: Correr el paquete entero**

```bash
go test ./internal/store/ -race
```

Esperado: `ok`. Confirma que la migración nueva no rompió ningún test que abra
la base.

- [ ] **Step 6: Commit**

```bash
git add internal/model/model.go internal/store/store.go internal/store/store_test.go internal/store/export_test.go
git commit -m "feat(store): migración 12 y aplicación retroactiva de una regla de nivel"
```

---

## Task 5: Borrar una regla re-nivela exacto

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `dondeCoincide`, `leerReglas`, `reglasDe`, `logs.Nivelar`.
- Produces: `func (s *Store) BorrarReglaNivel(id int64) (int, error)`

- [ ] **Step 1: Escribir el test que falla**

```go
// El undo es EXACTO y no aproximado: el nivel que queda es el que tendría la
// línea si la regla no hubiera existido nunca. Se puede porque Clasificar es
// determinística.
func TestBorrarUnaReglaDevuelveElNivelOriginal(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 31, 19, 0, 0, 0, time.UTC)
	if err := s.InsertLogs(lineas(base, "supabase-kong", corpusReal...)); err != nil {
		t.Fatal(err)
	}

	antes, err := s.BuscarLogs("", "supabase-kong", []string{"TRACE", "INFO", "WARN", "ERROR"},
		time.Time{}, base.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}

	id, filas, err := s.CrearReglaNivel(model.ReglaNivel{
		Patron: "egress-probe", Nivel: "TRACE", Motivo: "sonda propia", Creada: base,
	})
	if err != nil {
		t.Fatal(err)
	}

	deshechas, err := s.BorrarReglaNivel(id)
	if err != nil {
		t.Fatal(err)
	}
	if deshechas != filas {
		t.Errorf("borrar tocó %d filas y crear había tocado %d", deshechas, filas)
	}

	despues, err := s.BuscarLogs("", "supabase-kong", []string{"TRACE", "INFO", "WARN", "ERROR"},
		time.Time{}, base.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(antes) != len(despues) {
		t.Fatalf("cambió la cantidad de líneas: %d contra %d", len(antes), len(despues))
	}
	for i := range antes {
		if antes[i].Linea != despues[i].Linea || antes[i].Nivel != despues[i].Nivel {
			t.Errorf("línea %q quedó en %q y era %q", despues[i].Linea, despues[i].Nivel, antes[i].Nivel)
		}
	}

	if rs, err := s.ReglasNivel(); err != nil || len(rs) != 0 {
		t.Errorf("reglas = %+v err = %v, quería ninguna", rs, err)
	}
}

// Con dos reglas encima de la misma línea, borrar una no puede llevarse el
// efecto de la otra. Por eso el undo re-aplica las reglas que quedan y no
// solamente Clasificar.
func TestBorrarUnaReglaNoSeLlevaLaOtra(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 31, 19, 0, 0, 0, time.UTC)
	if err := s.InsertLogs(lineas(base, "kong", corpusReal[0])); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.CrearReglaNivel(model.ReglaNivel{
		Patron: "egress-probe", Nivel: "INFO", Motivo: "primera", Creada: base,
	}); err != nil {
		t.Fatal(err)
	}
	segunda, _, err := s.CrearReglaNivel(model.ReglaNivel{
		Patron: "auth/v1/health", Nivel: "TRACE", Motivo: "segunda", Creada: base,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.BorrarReglaNivel(segunda); err != nil {
		t.Fatal(err)
	}

	got, err := s.BuscarLogs("", "", []string{"TRACE", "INFO", "WARN", "ERROR"},
		time.Time{}, base.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Nivel != "INFO" {
		t.Errorf("quedó en %+v, quería INFO: borrar la segunda se llevó la primera", got)
	}
}

// Borrar dos veces la misma regla no es un error: el segundo click no puede
// devolver un 500.
func TestBorrarUnaReglaQueNoEstaNoEsError(t *testing.T) {
	s := abrir(t)
	n, err := s.BorrarReglaNivel(404)
	if err != nil || n != 0 {
		t.Errorf("n=%d err=%v, quería 0 y nil", n, err)
	}
}
```

- [ ] **Step 2: Correr los tests y verlos fallar**

```bash
go test ./internal/store/ -run 'TestBorrarUnaRegla' -v
```

Esperado: FAIL de compilación, `s.BorrarReglaNivel undefined`.

- [ ] **Step 3: Escribir el código mínimo**

```go
// BorrarReglaNivel saca la regla y devuelve las filas que tocaba al nivel que
// les corresponde SIN ella. El undo es exacto y no aproximado porque
// Clasificar es determinística: sin esto, una regla sería un cambio
// irreversible sobre datos.
//
// Se re-aplican las reglas que QUEDAN y no solo Clasificar: si dos reglas
// pisan la misma línea, borrar una no puede llevarse el efecto de la otra. Es
// la misma composición que usa la ingesta, logs.Nivelar, y por eso el nivel
// guardado siempre termina siendo el mismo que tendría una línea recién
// entrada.
//
// ⚠️ Vale la misma advertencia que CrearReglaNivel: son decenas de miles de
// filas sobre la única conexión a SQLite, y mientras dura el ciclo del minuto
// espera. Las filas se leen ENTERAS a memoria antes de escribir porque con una
// sola conexión no se puede escribir con un cursor de lectura abierto; son unos
// pocos MB para el caso medido.
//
// Una regla que no existe devuelve 0 y nil: el segundo click del panel no es
// un error.
func (s *Store) BorrarReglaNivel(id int64) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var patron, container string
	err = tx.QueryRow(`SELECT patron, container FROM reglas_nivel WHERE id = ?`, id).
		Scan(&patron, &container)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM reglas_nivel WHERE id = ?`, id); err != nil {
		return 0, err
	}

	quedan, err := leerReglas(tx)
	if err != nil {
		return 0, err
	}
	reglas := reglasDe(quedan)

	donde, args := dondeCoincide(patron, container)
	filas, err := tx.Query(`SELECT l.rowid, l.linea, l.stream, l.container FROM logs l`+donde, args...)
	if err != nil {
		return 0, err
	}
	type fila struct {
		rowid                    int64
		linea, stream, container string
	}
	var fs []fila
	for filas.Next() {
		var f fila
		if err := filas.Scan(&f.rowid, &f.linea, &f.stream, &f.container); err != nil {
			filas.Close()
			return 0, err
		}
		fs = append(fs, f)
	}
	filas.Close()
	if err := filas.Err(); err != nil {
		return 0, err
	}

	stmt, err := tx.Prepare(`INSERT INTO log_niveles (rowid, nivel) VALUES (?,?)
		ON CONFLICT(rowid) DO UPDATE SET nivel = excluded.nivel`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, f := range fs {
		nivel := logs.Nivelar(reglas, f.linea, f.stream, f.container)
		if _, err := stmt.Exec(f.rowid, string(nivel)); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(fs), nil
}
```

- [ ] **Step 4: Correr los tests y verlos pasar**

```bash
go test ./internal/store/ -run 'TestBorrarUnaRegla' -v
```

Esperado: PASS los tres.

- [ ] **Step 5: Correr el paquete entero, con race**

```bash
go test ./internal/store/ -race
```

Esperado: `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): borrar una regla devuelve las filas a su nivel exacto"
```

---

## Task 6: La ingesta y el backfill nivelan con las reglas puestas

**Files:**
- Modify: `internal/store/store.go` (`BackfillNiveles` recibe también el container)
- Modify: `cmd/server-status/main.go` (`ingerirLogs`, `nuevasLineas`, `backfillDeNiveles`, el tick)
- Test: `internal/store/store_test.go`, `cmd/server-status/ingesta_test.go`

**Interfaces:**
- Consumes: `logs.Nivelar` (Task 1), `Store.ReglasParaAplicar` (Task 4).
- Produces:
  - `func (s *Store) BackfillNiveles(nivelar func(linea, stream, container string) string, lote int) (int, bool, error)`
  - `func nuevasLineas(crudas []docker.LineaLog, desde time.Time, container string, reglas logs.Reglas) ([]model.LineaLog, time.Time)`
  - `func ingerirLogs(ctx context.Context, cli *docker.Client, s *store.Store, cs []docker.Container, reglas logs.Reglas)`

- [ ] **Step 1: Escribir los tests que fallan**

En `internal/store/store_test.go`:

```go
// El backfill tiene que pasarle el container a quien nivela: una regla puede
// estar acotada a un container, y sin ese dato la reprocesada de las filas
// viejas la ignoraría en silencio.
func TestBackfillLePasaElContainer(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	if err := s.InsertLogs(lineas(base, "kong", "una", "dos")); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertLogs(lineas(base, "db", "una")); err != nil {
		t.Fatal(err)
	}
	if err := s.ReiniciarBackfillParaTest(); err != nil {
		t.Fatal(err)
	}

	nivelar := func(linea, stream, container string) string {
		if container == "db" {
			return "ERROR"
		}
		return "TRACE"
	}
	for i := 0; i < 10; i++ {
		_, listo, err := s.BackfillNiveles(nivelar, 2)
		if err != nil {
			t.Fatal(err)
		}
		if listo {
			break
		}
	}

	got, err := s.BuscarLogs("", "", []string{"ERROR"}, time.Time{}, base.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Container != "db" {
		t.Errorf("got = %+v, quería solo la línea de db en ERROR", got)
	}
}
```

En `cmd/server-status/ingesta_test.go`:

```go
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
```

Los tests que ya existen en `ingesta_test.go` llaman a `nuevasLineas` con tres
argumentos: hay que agregarles `nil` como cuarto. Eso es parte del paso, no un
descuido.

- [ ] **Step 2: Correr los tests y verlos fallar**

```bash
go test ./internal/store/ -run TestBackfillLePasaElContainer -v
```

Esperado: FAIL de compilación, la función que se le pasa a `BackfillNiveles` no
acepta tres argumentos.

```bash
go test ./cmd/server-status/ -run TestNuevasLineasAplicaLasReglas -v
```

Esperado: FAIL de compilación, `nuevasLineas` toma tres argumentos y no cuatro.

- [ ] **Step 3: Escribir el código mínimo**

En `internal/store/store.go`, `BackfillNiveles`:

```go
// BackfillNiveles clasifica un lote de las líneas que ya estaban guardadas
// cuando se aplicó la migración 9. Devuelve cuántas procesó y si ya terminó.
//
// Quien nivela entra por parámetro y no por import para que el backfill se
// pueda testear con una función de tres líneas. Recibe también el CONTAINER
// porque una regla de nivel puede estar acotada a uno: sin ese dato, la
// reprocesada de las filas viejas ignoraría el scope en silencio.
func (s *Store) BackfillNiveles(nivelar func(linea, stream, container string) string, lote int) (int, bool, error) {
```

y adentro, la consulta y el uso:

```go
	filas, err := s.db.Query(`
		SELECT rowid, linea, stream, container FROM logs
		WHERE rowid > ? AND rowid <= ? ORDER BY rowid LIMIT ?`, ultimo, techo, lote)
	...
	type fila struct {
		rowid                    int64
		linea, stream, container string
	}
	...
		if err := filas.Scan(&f.rowid, &f.linea, &f.stream, &f.container); err != nil {
	...
		if _, err := stmt.Exec(f.rowid, nivelar(f.linea, f.stream, f.container)); err != nil {
```

En `cmd/server-status/main.go`:

```go
// nuevasLineas filtra lo que ya se ingirió y devuelve el cursor nuevo.
//
// El nivel sale de logs.Nivelar: lo que dice el clasificador, corregido por las
// reglas que el usuario haya puesto desde el panel. Las reglas llegan por
// parámetro y no se leen acá adentro, para que esta función siga siendo pura y
// testeable sin base.
func nuevasLineas(crudas []docker.LineaLog, desde time.Time, container string, reglas logs.Reglas) ([]model.LineaLog, time.Time) {
	...
		out = append(out, model.LineaLog{
			TS: l.TS, Container: container, Stream: l.Stream, Linea: l.Linea,
			Nivel: string(logs.Nivelar(reglas, l.Linea, l.Stream, container)),
		})
	...
}
```

`ingerirLogs` toma las reglas y se las pasa:

```go
func ingerirLogs(ctx context.Context, cli *docker.Client, s *store.Store, cs []docker.Container, reglas logs.Reglas) {
	...
		ms, ultima := nuevasLineas(lineas, desde, c.Name, reglas)
	...
}
```

En el tick, justo antes de la llamada:

```go
			// Las reglas de nivel se releen UNA VEZ POR TICK. Sin mutex, sin
			// atomic.Pointer y sin invalidación desde el handler web: la
			// desactualización máxima es de un minuto, y una regla recién
			// creada ya quedó aplicada sobre todo lo guardado por
			// CrearReglaNivel. Nada de eso justifica una primitiva de
			// concurrencia ni un camino de invalidación que después hay que
			// testear.
			//
			// Un error leyéndolas no puede frenar la ingesta: se sigue con el
			// conjunto vacío, que es exactamente el comportamiento anterior.
			reglas, err := s.ReglasParaAplicar()
			if err != nil {
				slog.Error("no se pudieron leer las reglas de nivel", "err", err)
			}
			ingerirLogs(ctx, cli, s, cs, reglas)
```

Y `backfillDeNiveles`:

```go
func backfillDeNiveles(ctx context.Context, s *store.Store) {
	const lote = 2000

	// Las reglas se leen UNA vez, al empezar la pasada: si no se aplicaran, el
	// backfill le pisaría el nivel a las filas viejas que una regla ya había
	// corregido, y el ruido volvería sin que nadie tocara nada.
	reglas, err := s.ReglasParaAplicar()
	if err != nil {
		slog.Error("no se pudieron leer las reglas de nivel para el backfill", "err", err)
	}
	nivelar := func(linea, stream, container string) string {
		return string(logs.Nivelar(reglas, linea, stream, container))
	}
	...
		n, listo, err := s.BackfillNiveles(nivelar, lote)
	...
}
```

- [ ] **Step 4: Correr los tests y verlos pasar**

```bash
go test ./internal/store/ -run TestBackfill -v
```

Esperado: PASS, incluidos los dos que ya existían.

```bash
go test ./cmd/server-status/ -v
```

Esperado: PASS, incluidos los tres tests de cursor que ya existían.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go cmd/server-status/main.go cmd/server-status/ingesta_test.go
git commit -m "feat(ingesta): las líneas nuevas entran ya niveladas por las reglas"
```

---

## Task 7: El rowid llega hasta la vista

La línea entera no sirve como identificador para el link del panel, y buscarla
por texto sería otra vez el mismo problema. El `rowid` de FTS5 ya está y es
estable: solo hay que dejar de tirarlo.

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/store/store.go` (`selectLogs` unificado, `LineaPorRowid`)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces:
  - `model.LineaLog.Rowid int64`
  - `func (s *Store) LineaPorRowid(rowid int64) (model.LineaLog, bool, error)`

- [ ] **Step 1: Escribir el test que falla**

```go
// La vista necesita el rowid para poder linkear a "hacer una regla con esta
// línea". Antes se descartaba a propósito en selectLogs, con un 0 literal.
func TestBuscarLogsTraeElRowid(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 31, 19, 0, 0, 0, time.UTC)
	if err := s.InsertLogs(lineas(base, "kong", "una", "dos")); err != nil {
		t.Fatal(err)
	}

	got, err := s.BuscarLogs("", "", nil, time.Time{}, base.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d líneas, quería 2", len(got))
	}
	for _, l := range got {
		if l.Rowid == 0 {
			t.Errorf("la línea %q volvió sin rowid", l.Linea)
		}
	}

	l, hay, err := s.LineaPorRowid(got[0].Rowid)
	if err != nil || !hay {
		t.Fatalf("LineaPorRowid: hay=%v err=%v", hay, err)
	}
	if l.Linea != got[0].Linea || l.Container != "kong" || l.Nivel != got[0].Nivel {
		t.Errorf("volvió %+v, quería %+v", l, got[0])
	}

	if _, hay, err := s.LineaPorRowid(99999); err != nil || hay {
		t.Errorf("un rowid que no existe: hay=%v err=%v, quería false y nil", hay, err)
	}
}
```

- [ ] **Step 2: Correr el test y verlo fallar**

```bash
go test ./internal/store/ -run TestBuscarLogsTraeElRowid -v
```

Esperado: FAIL de compilación, `l.Rowid undefined` y `s.LineaPorRowid undefined`.

- [ ] **Step 3: Escribir el código mínimo**

En `internal/model/model.go`, dentro de `LineaLog`:

```go
	// Rowid es el de la tabla FTS5, y es lo que identifica una línea guardada.
	// Vale 0 en una línea que todavía no se insertó. El panel lo usa para
	// linkear "hacer una regla de nivel con esta línea".
	Rowid int64
```

En `internal/store/store.go`, las dos constantes se vuelven una sola:

```go
// COALESCE porque una fila insertada antes de la migración 9 todavía puede no
// tener nivel si el backfill no llegó: se la trata como INFO en vez de hacerla
// desaparecer.
//
// El rowid viene SIEMPRE. Antes había dos constantes, una con un 0 literal en
// su lugar, y eso dejaba a la vista sin forma de referirse a una línea.
const selectLogs = `SELECT l.linea, l.container, l.stream, l.ts, COALESCE(n.nivel, 'INFO'), l.rowid
	      FROM logs l LEFT JOIN log_niveles n ON n.rowid = l.rowid`
```

`selectLogsConRowid` se borra y `LogsDesdeRowid` pasa a usar `selectLogs`. En
`consultarLogs`, el rowid escaneado se guarda en la línea:

```go
		l.TS = time.Unix(ts, 0).UTC()
		l.Rowid = rowid
		if rowid > max {
			max = rowid
		}
```

Y el método nuevo:

```go
// LineaPorRowid trae una línea guardada por su id. Es lo que prellena el form
// de una regla nueva. El bool es false cuando la línea ya no está: la
// retención la puede haber podado entre que se pintó la vista y se hizo click.
func (s *Store) LineaPorRowid(rowid int64) (model.LineaLog, bool, error) {
	ls, _, err := s.consultarLogs(selectLogs+` WHERE l.rowid = ?`, []any{rowid})
	if err != nil || len(ls) == 0 {
		return model.LineaLog{}, false, err
	}
	return ls[0], true, nil
}
```

- [ ] **Step 4: Correr el test y verlo pasar**

```bash
go test ./internal/store/ -run TestBuscarLogsTraeElRowid -v
```

Esperado: PASS.

- [ ] **Step 5: Correr store y web enteros**

El modo en vivo lee el mismo `consultarLogs`, así que hay que confirmar que no
se movió el cursor.

```bash
go test ./internal/store/ ./internal/web/ -race
```

Esperado: `ok` los dos.

- [ ] **Step 6: Commit**

```bash
git add internal/model/model.go internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): las líneas de la vista traen su rowid"
```

---

## Task 8: La página para crear una regla

**Files:**
- Modify: `internal/web/panel.go` (interfaz `Datos`, rutas, helpers)
- Modify: `internal/web/idiomas.go`
- Create: `internal/web/plantillas/regla-nueva.html`
- Test: `internal/web/panel_test.go`

**Interfaces:**
- Consumes: `logs.PatronSugerido` (Task 2), `Store.ContarPorPatron` (Task 3),
  `Store.CrearReglaNivel` (Task 4), `Store.LineaPorRowid` (Task 7).
- Produces: rutas `GET /logs/reglas/nueva` y `POST /logs/reglas`; la interfaz
  `Datos` con cinco métodos más.

- [ ] **Step 1: Escribir los tests que fallan**

En `internal/web/panel_test.go`, primero los métodos nuevos del doble. Se
agregan los CINCO de una, aunque dos los use recién la Task 9: la interfaz
describe lo que el panel necesita, y partirla en dos tandas obliga a tocar el
doble dos veces.

```go
// espiaReglas registra lo que le piden, que es lo único que estos tests pueden
// verificar sin base.
type espiaReglas struct {
	datosFalsos
	pedidoPatron, pedidoContainer string
	creada                        model.ReglaNivel
	borrada                       int64
}

func (e *espiaReglas) ContarPorPatron(patron, container string) (int, error) {
	e.pedidoPatron, e.pedidoContainer = patron, container
	return 42, nil
}

func (e *espiaReglas) CrearReglaNivel(r model.ReglaNivel) (int64, int, error) {
	e.creada = r
	return 7, 42, nil
}

func (e *espiaReglas) BorrarReglaNivel(id int64) (int, error) {
	e.borrada = id
	return 42, nil
}

// Y en datosFalsos, para que el resto de los tests siga compilando:

func (datosFalsos) ReglasNivel() ([]model.ReglaNivel, error) {
	return []model.ReglaNivel{{
		ID: 3, Patron: `"-" "egress-probe"`, Container: "supabase-kong", Nivel: "TRACE",
		Motivo: "es nuestra propia sonda, no un problema del sistema",
		Creada: time.Date(2026, 8, 31, 20, 15, 0, 0, time.UTC),
	}}, nil
}

func (datosFalsos) ContarPorPatron(patron, container string) (int, error) { return 42, nil }

func (datosFalsos) CrearReglaNivel(r model.ReglaNivel) (int64, int, error) { return 7, 42, nil }

func (datosFalsos) BorrarReglaNivel(id int64) (int, error) { return 42, nil }

func (datosFalsos) LineaPorRowid(rowid int64) (model.LineaLog, bool, error) {
	if rowid != 99 {
		return model.LineaLog{}, false, nil
	}
	return model.LineaLog{
		TS:        time.Date(2026, 8, 31, 19, 50, 30, 0, time.UTC),
		Container: "supabase-kong", Stream: "stdout", Nivel: "WARN", Rowid: 99,
		Linea: `172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
	}, true, nil
}
```

Y los tests:

```go
// La página se abre desde una línea concreta: el patrón viene sugerido, el
// container viene puesto, y el número de líneas afectadas se ve ANTES de
// confirmar. Ese número es toda la función: sin él, una regla es un salto al
// vacío sobre datos guardados.
func TestLaPaginaDeReglaNuevaSePrellenaDesdeLaLinea(t *testing.T) {
	e := &espiaReglas{}
	rec := httptest.NewRecorder()
	web.NuevoPanel(e, zonaDePrueba).ServeHTTP(rec,
		httptest.NewRequest("GET", "/logs/reglas/nueva?rowid=99&volver=%2Flogs%3Fhoras%3D6", nil))

	if rec.Code != 200 {
		t.Fatalf("código = %d, quería 200", rec.Code)
	}
	cuerpo := rec.Body.String()
	// El patrón sugerido es el de PatronSugerido, ya sin la IP ni la fecha.
	if !strings.Contains(cuerpo, `&#34;GET /auth/v1/health HTTP/1.1&#34; 401 96 &#34;-&#34; &#34;egress-probe&#34;`) {
		t.Error("el patrón no viene sugerido en el form")
	}
	if !strings.Contains(cuerpo, "42") {
		t.Error("no se muestra cuántas líneas afecta")
	}
	if !strings.Contains(cuerpo, `value="/logs?horas=6"`) {
		t.Error("se perdió el volver")
	}
	if e.pedidoContainer != "supabase-kong" {
		t.Errorf("contó sobre el container %q, quería supabase-kong", e.pedidoContainer)
	}
}

// Recalcular es re-submitear el mismo GET: el conteo se hace sobre lo que el
// usuario editó, no sobre lo que se sugirió al abrir.
func TestRecalcularUsaElPatronEditado(t *testing.T) {
	e := &espiaReglas{}
	rec := httptest.NewRecorder()
	web.NuevoPanel(e, zonaDePrueba).ServeHTTP(rec,
		httptest.NewRequest("GET", "/logs/reglas/nueva?patron=egress-probe&container=&nivel=TRACE&motivo=x", nil))

	if rec.Code != 200 {
		t.Fatalf("código = %d, quería 200", rec.Code)
	}
	if e.pedidoPatron != "egress-probe" || e.pedidoContainer != "" {
		t.Errorf("contó %q/%q, quería egress-probe en todos los containers", e.pedidoPatron, e.pedidoContainer)
	}
}

// Una línea que la retención ya se llevó no es un 500 ni una página a medias.
func TestReglaNuevaConUnaLineaQueYaNoEsta(t *testing.T) {
	rec := httptest.NewRecorder()
	web.NuevoPanel(datosFalsos{}, zonaDePrueba).ServeHTTP(rec,
		httptest.NewRequest("GET", "/logs/reglas/nueva?rowid=12345", nil))
	if rec.Code != 404 {
		t.Errorf("código = %d, quería 404", rec.Code)
	}
}

func TestCrearUnaReglaYVolver(t *testing.T) {
	e := &espiaReglas{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/logs/reglas", strings.NewReader(
		"patron=egress-probe&container=supabase-kong&nivel=TRACE&motivo=sonda+propia&volver=%2Flogs%3Fhoras%3D6"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	web.NuevoPanel(e, zonaDePrueba).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("código = %d, quería 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/logs?horas=6" {
		t.Errorf("Location = %q, quería /logs?horas=6", loc)
	}
	if e.creada.Patron != "egress-probe" || e.creada.Container != "supabase-kong" ||
		e.creada.Nivel != "TRACE" || e.creada.Motivo != "sonda propia" {
		t.Errorf("se creó %+v", e.creada)
	}
	if e.creada.Creada.IsZero() {
		t.Error("la regla se guardó sin fecha de creación")
	}
}

// Las tres validaciones. El motivo es obligatorio a propósito: una regla sin
// motivo es una regla que dentro de tres meses nadie se anima a borrar.
func TestUnaReglaIncompletaNoSeCrea(t *testing.T) {
	casos := []struct{ nombre, cuerpo string }{
		{"sin patrón", "patron=&nivel=TRACE&motivo=algo"},
		{"patrón de puros espacios", "patron=+++&nivel=TRACE&motivo=algo"},
		{"sin motivo", "patron=x&nivel=TRACE&motivo="},
		{"nivel inventado", "patron=x&nivel=SILENCIO&motivo=algo"},
	}
	for _, c := range casos {
		e := &espiaReglas{}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/logs/reglas", strings.NewReader(c.cuerpo))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		web.NuevoPanel(e, zonaDePrueba).ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: código = %d, quería 400", c.nombre, rec.Code)
		}
		if e.creada.Patron != "" {
			t.Errorf("%s: se creó igual: %+v", c.nombre, e.creada)
		}
	}
}
```

- [ ] **Step 2: Correr los tests y verlos fallar**

```bash
go test ./internal/web/ -run 'Regla' -v
```

Esperado: FAIL de compilación, `*espiaReglas` no implementa `web.Datos` y las
rutas no existen. Al llegar a correr, los GET dan 404.

- [ ] **Step 3: Escribir el código mínimo**

En `internal/web/panel.go`, la interfaz crece:

```go
	// Las reglas de nivel: la lista, el conteo previo, crear y borrar. El
	// conteo y el efecto salen de la MISMA definición de coincidencia en el
	// store, que es lo que hace que el número que se confirma sea el que
	// queda.
	ReglasNivel() ([]model.ReglaNivel, error)
	ContarPorPatron(patron, container string) (int, error)
	CrearReglaNivel(r model.ReglaNivel) (int64, int, error)
	BorrarReglaNivel(id int64) (int, error)
	// La línea que el usuario tocó en /logs, para prellenar la regla.
	LineaPorRowid(rowid int64) (model.LineaLog, bool, error)
```

Helpers, al lado de `togglesDe`:

```go
// nivelesConocidos es la lista canónica: los pills del filtro y la validación
// del form salen de acá, para que no puedan divergir.
var nivelesConocidos = []string{"TRACE", "INFO", "WARN", "ERROR"}

// esNivel valida lo que llega del form. NivelValido de logs no sirve acá: cae
// a INFO ante cualquier basura, que es lo correcto para un filtro de la vista
// y lo contrario de lo que hace falta para guardar una regla.
func esNivel(s string) bool {
	for _, n := range nivelesConocidos {
		if s == n {
			return true
		}
	}
	return false
}
```

`togglesDe` pasa a recorrer `nivelesConocidos` en vez de su literal propio.

La vista y los dos handlers:

```go
// vistaRegla es el form de una regla nueva con su conteo previo.
type vistaRegla struct {
	Nav       nav
	Linea     string // la línea que la originó, como contexto
	Actual    string // el nivel que tiene hoy esa línea
	Patron    string
	Container string
	Nivel     string
	Motivo    string
	Afectadas int
	HayConteo bool
	Volver    string
	// Niveles son las cuatro opciones del <select>. Salen de
	// nivelesConocidos, la misma lista que valida el POST: si la vista
	// ofreciera una que la validación rechaza, el form sería una trampa.
	Niveles []string
}

mux.HandleFunc("GET /logs/reglas/nueva", func(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	idioma := idiomaDe(w, r)
	v := vistaRegla{
		Niveles: nivelesConocidos,
		Volver:  rutaPropia(q.Get("volver")),
		// TRACE por default: silenciar ruido es el caso que motivó todo esto.
		// Subir a ERROR sigue estando a un click.
		Nivel: string(logs.Trace),
	}

	if q.Has("patron") {
		// Segunda vuelta: recalcular es re-submitear este mismo GET, así que
		// el conteo tiene que salir de lo que el usuario editó y no de lo que
		// se sugirió al abrir.
		v.Patron, v.Container = q.Get("patron"), q.Get("container")
		v.Motivo, v.Linea, v.Actual = q.Get("motivo"), q.Get("linea"), q.Get("actual")
		if esNivel(q.Get("nivel")) {
			v.Nivel = q.Get("nivel")
		}
	} else {
		rowid, err := strconv.ParseInt(q.Get("rowid"), 10, 64)
		if err != nil {
			http.Error(w, "rowid inválido", http.StatusBadRequest)
			return
		}
		l, hay, err := d.LineaPorRowid(rowid)
		if err != nil {
			http.Error(w, "no se pudo leer la línea", http.StatusInternalServerError)
			return
		}
		// La retención pudo llevarse la línea entre que se pintó la vista y se
		// hizo click. Es un 404 y no un 500: no se rompió nada.
		if !hay {
			http.Error(w, "esa línea ya no está guardada", http.StatusNotFound)
			return
		}
		v.Linea, v.Container, v.Actual = l.Linea, l.Container, l.Nivel
		v.Patron = logs.PatronSugerido(l.Linea)
	}
	v.Nav = armarNav("logs", horasDe(r), v.Container)

	if strings.TrimSpace(v.Patron) != "" {
		n, err := d.ContarPorPatron(v.Patron, v.Container)
		if err != nil {
			http.Error(w, "no se pudieron contar las coincidencias", http.StatusInternalServerError)
			return
		}
		v.Afectadas, v.HayConteo = n, true
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := plantillasIdioma[idioma].ExecuteTemplate(w, "regla-nueva.html", v); err != nil {
		slog.Error("no se pudo renderizar la regla nueva", "err", err)
	}
})

mux.HandleFunc("POST /logs/reglas", func(w http.ResponseWriter, r *http.Request) {
	patron := r.FormValue("patron")
	nivel := r.FormValue("nivel")
	// El motivo NO es decorativo: dentro de tres meses, una regla sin motivo
	// es una regla que nadie se anima a borrar. Por eso es obligatorio.
	motivo := strings.TrimSpace(r.FormValue("motivo"))
	if strings.TrimSpace(patron) == "" || motivo == "" || !esNivel(nivel) {
		http.Error(w, "la regla necesita patrón, nivel y motivo", http.StatusBadRequest)
		return
	}

	// ⚠️ Esto puede tardar segundos: aplica la regla a todo lo guardado sobre
	// la única conexión a SQLite. Ver CrearReglaNivel en el store.
	id, filas, err := d.CrearReglaNivel(model.ReglaNivel{
		Patron: patron, Container: r.FormValue("container"), Nivel: nivel,
		Motivo: motivo, Creada: time.Now(),
	})
	if err != nil {
		slog.Error("no se pudo crear la regla de nivel", "patron", patron, "err", err)
		http.Error(w, "no se pudo crear la regla", http.StatusInternalServerError)
		return
	}
	// Queda en el journal cuántas filas cambió: es el número que el usuario
	// confirmó, y el único rastro de una acción que reescribe datos guardados.
	slog.Info("regla de nivel creada", "id", id, "patron", patron,
		"container", r.FormValue("container"), "nivel", nivel, "filas", filas)

	http.Redirect(w, r, rutaPropia(r.FormValue("volver")), http.StatusSeeOther)
})
```

`plantillasCon` suma las dos plantillas nuevas al `ParseFS`:

```go
	}).ParseFS(plantillas, "plantillas/nav.html", "plantillas/panel.html",
		"plantillas/logs.html", "plantillas/tail.html", "plantillas/eventos.html",
		"plantillas/regla-nueva.html", "plantillas/reglas.html"))
```

`internal/web/plantillas/regla-nueva.html`:

```html
<!doctype html>
<html lang="{{ lang }}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{ t "regla-nueva" }} · server-status</title>
<style>
  :root { color-scheme: light dark; --borde:#8883; --err:#c0392b; --warn:#b7791f; --ok:#1a7f4b; }
  * { box-sizing: border-box; }
  body { font: 15px/1.5 system-ui, -apple-system, sans-serif; margin: 0; padding: 1.5rem;
         max-width: 60rem; }
  h1 { font-size: 1.25rem; margin: 0 0 1rem; }
  label { display: block; font-size: .8rem; opacity: .6; margin-bottom: .2rem; }
  input, select, button, textarea { font: inherit; padding: .4rem .6rem; border-radius: .4rem;
                          border: 1px solid var(--borde); background: transparent; color: inherit; }
  .campo { margin-bottom: .9rem; }
  .campo input[type=text] { width: 100%; }
  .linea { font: 12.5px/1.6 ui-monospace, Menlo, monospace; border: 1px solid var(--borde);
           border-radius: .5rem; padding: .6rem .75rem; margin-bottom: 1.25rem;
           white-space: pre-wrap; word-break: break-word; opacity: .75; }
  .conteo { border: 1px solid var(--borde); border-left: 3px solid var(--warn);
            border-radius: .4rem; padding: .7rem .9rem; margin: 1rem 0; }
  .conteo b { font-size: 1.15rem; }
  .acciones { display: flex; gap: .5rem; align-items: center; }
{{ template "nav-css" }}
</style>
</head>
<body>
{{ template "nav" .Nav }}

<h1>{{ t "regla-nueva" }}</h1>

{{ with .Linea }}<div class="linea">{{ . }}</div>{{ end }}

{{/* UN solo form. El botón de recalcular cambia método y destino con
     formmethod/formaction, igual que el de exportar en /logs: con dos forms
     separados, editar el patrón en uno y confirmar en el otro mandaría los
     valores viejos. */}}
<form method="post" action="/logs/reglas">
  {{/* El botón oculto va PRIMERO para que Enter recalcule y no cree la regla
       sin haber visto el número. */}}
  <button hidden tabindex="-1" aria-hidden="true" formmethod="get" formaction="/logs/reglas/nueva"></button>
  <input type="hidden" name="volver" value="{{ .Volver }}">
  <input type="hidden" name="linea" value="{{ .Linea }}">
  <input type="hidden" name="actual" value="{{ .Actual }}">

  <div class="campo">
    <label for="patron">{{ t "patron" }}</label>
    <input type="text" id="patron" name="patron" value="{{ .Patron }}" autofocus>
  </div>
  <div class="campo">
    <label for="container">{{ t "container" }}</label>
    <input type="text" id="container" name="container" value="{{ .Container }}" placeholder="{{ t "todos" }}">
  </div>
  <div class="campo">
    <label for="nivel">{{ t "nivel-nuevo" }}{{ with .Actual }} ({{ t "nivel-actual" }} {{ . }}){{ end }}</label>
    <select id="nivel" name="nivel">
      {{ range $n := $.Niveles }}<option value="{{ $n }}"{{ if eq $n $.Nivel }} selected{{ end }}>{{ $n }}</option>{{ end }}
    </select>
  </div>
  <div class="campo">
    <label for="motivo">{{ t "motivo" }}</label>
    <input type="text" id="motivo" name="motivo" value="{{ .Motivo }}" placeholder="{{ t "motivo-ph" }}" required>
  </div>

  {{ if .HayConteo }}
  <div class="conteo"><b>{{ .Afectadas }}</b> {{ t "lineas-afectadas" }}</div>
  {{ end }}

  <div class="acciones">
    <button type="submit" formmethod="get" formaction="/logs/reglas/nueva">{{ t "recalcular" }}</button>
    <button type="submit">{{ t "crear-regla" }}</button>
    <a href="{{ .Volver }}">{{ t "cancelar" }}</a>
  </div>
</form>
</body>
</html>
```

`vistaRegla` necesita entonces el campo `Niveles []string` con
`nivelesConocidos`, seteado en el handler.

En `internal/web/idiomas.go`, la sección nueva:

```go
	// reglas de nivel
	"regla-nueva":       {"regla de nivel", "level rule"},
	"patron":            {"patrón (la línea tiene que contenerlo, tal cual, respetando mayúsculas)", "pattern (the line must contain it, exactly, case-sensitive)"},
	"container":         {"container", "container"},
	"nivel-nuevo":       {"nivel que le queda", "level it gets"},
	"nivel-actual":      {"hoy", "now"},
	"motivo":            {"motivo", "reason"},
	"motivo-ph":         {"por qué esto no es lo que el clasificador cree", "why this isn't what the classifier thinks"},
	"lineas-afectadas":  {"líneas guardadas cambian de nivel al confirmar", "stored lines change level on confirm"},
	"recalcular":        {"recalcular", "recount"},
	"crear-regla":       {"crear la regla", "create the rule"},
	"cancelar":          {"cancelar", "cancel"},
```

- [ ] **Step 4: Correr los tests y verlos pasar**

```bash
go test ./internal/web/ -run 'Regla' -v
```

Esperado: PASS los cinco.

- [ ] **Step 5: Correr el paquete entero**

```bash
go test ./internal/web/ -race
```

Esperado: `ok`. Las plantillas se parsean al arrancar el paquete, así que un
error de sintaxis en `regla-nueva.html` hace explotar TODOS los tests de `web`,
no solo los nuevos.

- [ ] **Step 6: Commit**

```bash
git add internal/web/panel.go internal/web/idiomas.go internal/web/plantillas/regla-nueva.html internal/web/panel_test.go
git commit -m "feat(web): crear una regla de nivel desde una línea, con conteo previo"
```

---

## Task 9: La lista de reglas, borrar, y el aviso en `/logs`

Una regla demasiado ancha te esconde algo real. Ese riesgo no se elimina: se
mitiga con el conteo previo (Task 8), con esta lista y su match count, y con el
aviso en el encabezado. **Un filtro que te olvidaste que pusiste es peor que no
tener filtro.**

**Files:**
- Modify: `internal/web/panel.go`
- Modify: `internal/web/idiomas.go`
- Create: `internal/web/plantillas/reglas.html`
- Modify: `internal/web/plantillas/logs.html`
- Test: `internal/web/panel_test.go`

**Interfaces:**
- Consumes: `Datos.ReglasNivel`, `Datos.ContarPorPatron`, `Datos.BorrarReglaNivel` (Task 8).
- Produces: rutas `GET /logs/reglas` y `POST /logs/reglas/{id}/borrar`.

- [ ] **Step 1: Escribir los tests que fallan**

Primero, `datosFalsos.BuscarLogs` tiene que devolver un rowid, o la fila de la
vista no tiene con qué linkear:

```go
	return []model.LineaLog{{
		TS:        time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Container: "comm-tool", Stream: "stderr", Linea: "ERROR conexion rechazada",
		Nivel: "ERROR", Rowid: 99,
	}}, nil
```

Y los tests:

```go
// La lista es donde se ve una regla que creció en silencio: cuántas líneas
// matchea HOY, no cuántas matcheaba el día que se creó.
func TestLaListaDeReglasMuestraTodoLoQueHaceFaltaParaBorrarla(t *testing.T) {
	cuerpo := pedir(t, "/logs/reglas").Body.String()

	for _, quiero := range []string{
		"es nuestra propia sonda",           // el motivo
		"supabase-kong",                     // el scope
		"TRACE",                             // el nivel que impone
		"31/08/2026",                        // cuándo se creó, en fecha completa
		"42",                                // cuántas líneas matchea hoy
		`action="/logs/reglas/3/borrar"`,    // y cómo deshacerla
	} {
		if !strings.Contains(cuerpo, quiero) {
			t.Errorf("la lista no muestra %q", quiero)
		}
	}
}

func TestBorrarUnaReglaDesdeElPanel(t *testing.T) {
	e := &espiaReglas{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/logs/reglas/3/borrar",
		strings.NewReader("volver=%2Flogs%2Freglas"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	web.NuevoPanel(e, zonaDePrueba).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || e.borrada != 3 {
		t.Fatalf("código=%d borrada=%d, quería 303 y 3", rec.Code, e.borrada)
	}
	if loc := rec.Header().Get("Location"); loc != "/logs/reglas" {
		t.Errorf("Location = %q, quería /logs/reglas", loc)
	}

	// Un destino de afuera no se sigue: el panel es privado, pero un open
	// redirect privado sigue siendo un open redirect.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/logs/reglas/3/borrar",
		strings.NewReader("volver=%2F%2Fjadd.com.ar%2Frobo"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	web.NuevoPanel(e, zonaDePrueba).ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, quería /", loc)
	}

	// Un id que no es número es un 400, no un panic.
	rec = httptest.NewRecorder()
	web.NuevoPanel(e, zonaDePrueba).ServeHTTP(rec,
		httptest.NewRequest("POST", "/logs/reglas/basura/borrar", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("id inválido: código = %d, quería 400", rec.Code)
	}
}

// Un filtro que te olvidaste que pusiste es peor que no tener filtro.
func TestElEncabezadoDeLogsAvisaDeLasReglas(t *testing.T) {
	cuerpo := pedir(t, "/logs").Body.String()
	if !strings.Contains(cuerpo, "reglas activas: 1") {
		t.Error("el encabezado de /logs no dice cuántas reglas hay puestas")
	}
	if !strings.Contains(cuerpo, `href="/logs/reglas"`) {
		t.Error("el aviso no linkea a la lista")
	}
}

// El control es la celda del nivel: click en el nivel para cambiar el nivel.
// Así no hace falta una columna nueva ni una línea de JS.
func TestCadaLineaOfreceHacerUnaRegla(t *testing.T) {
	cuerpo := pedir(t, "/logs?horas=6").Body.String()
	if !strings.Contains(cuerpo, "/logs/reglas/nueva?rowid=99") {
		t.Error("la fila no linkea a crear una regla con esa línea")
	}
	if !strings.Contains(cuerpo, "volver=") {
		t.Error("el link no lleva a dónde volver")
	}
}

func TestLosTextosDeReglasEstanEnLosDosIdiomas(t *testing.T) {
	if cuerpo := pedir(t, "/logs/reglas?lang=en").Body.String(); !strings.Contains(cuerpo, "lines today") {
		t.Error("la lista de reglas no se traduce")
	}
	if cuerpo := pedir(t, "/logs?lang=en").Body.String(); !strings.Contains(cuerpo, "active rules: 1") {
		t.Error("el aviso del encabezado no se traduce")
	}
}
```

- [ ] **Step 2: Correr los tests y verlos fallar**

```bash
go test ./internal/web/ -run 'TestLaListaDeReglas|TestBorrarUnaReglaDesdeElPanel|TestElEncabezadoDeLogs|TestCadaLineaOfrece|TestLosTextosDeReglas' -v
```

Esperado: FAIL. Los dos de `/logs/reglas` con 404, los de `/logs` porque el
encabezado y la fila todavía no tienen nada de eso.

- [ ] **Step 3: Escribir el código mínimo**

En `internal/web/panel.go`:

```go
// filaRegla es una regla con lo que matchea HOY. El conteo del día que se creó
// no sirve para decidir si borrarla: lo que importa es si creció.
type filaRegla struct {
	model.ReglaNivel
	Coincidencias int
}

type vistaReglas struct {
	Nav    nav
	Zona   *time.Location
	Reglas []filaRegla
	Volver string
}

mux.HandleFunc("GET /logs/reglas", func(w http.ResponseWriter, r *http.Request) {
	idioma := idiomaDe(w, r)
	rs, err := d.ReglasNivel()
	if err != nil {
		http.Error(w, "no se pudieron leer las reglas", http.StatusInternalServerError)
		return
	}

	v := vistaReglas{
		Nav: armarNav("logs", horasDe(r), ""), Zona: zona,
		Volver: rutaPropia(r.URL.RequestURI()),
	}
	for _, regla := range rs {
		// Una consulta por regla, y cada una es un scan completo de la tabla
		// FTS5. Son unas pocas reglas y esta página se abre a mano: no vale la
		// pena una consulta agrupada que igual tendría que evaluar instr() por
		// fila y por regla.
		n, err := d.ContarPorPatron(regla.Patron, regla.Container)
		if err != nil {
			// Sin el conteo la fila igual sirve para borrar la regla, que es
			// lo urgente. Tirar la página entera abajo por esto sería peor.
			slog.Error("no se pudieron contar las coincidencias de la regla",
				"id", regla.ID, "err", err)
		}
		v.Reglas = append(v.Reglas, filaRegla{ReglaNivel: regla, Coincidencias: n})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := plantillasIdioma[idioma].ExecuteTemplate(w, "reglas.html", v); err != nil {
		slog.Error("no se pudo renderizar la lista de reglas", "err", err)
	}
})

mux.HandleFunc("POST /logs/reglas/{id}/borrar", func(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	// Devuelve las filas afectadas a Clasificar más las reglas que quedan: el
	// undo es exacto. Y puede tardar segundos, igual que crear.
	filas, err := d.BorrarReglaNivel(id)
	if err != nil {
		slog.Error("no se pudo borrar la regla de nivel", "id", id, "err", err)
		http.Error(w, "no se pudo borrar la regla", http.StatusInternalServerError)
		return
	}
	slog.Info("regla de nivel borrada", "id", id, "filas", filas)
	http.Redirect(w, r, rutaPropia(r.FormValue("volver")), http.StatusSeeOther)
})
```

En el handler de `GET /logs`, tres campos más en la struct de la vista:

```go
			// Reglas es cuántas hay puestas, para avisarlo en el encabezado:
			// un filtro que uno se olvidó que puso es peor que no tener
			// filtro. Volver es a dónde vuelve la creación de una regla.
			Reglas string
			Volver string
```

y antes de renderizar:

```go
		// Un error leyendo las reglas no puede tumbar el visor de logs, que es
		// justo lo que uno abre cuando algo se rompió.
		aviso := ""
		if rs, err := d.ReglasNivel(); err != nil {
			slog.Error("no se pudieron leer las reglas de nivel", "err", err)
		} else if len(rs) > 0 {
			aviso = fmt.Sprintf(tr(idioma, "reglas-activas"), len(rs))
		}
```

con `Reglas: aviso` y `Volver: rutaPropia(r.URL.RequestURI())` en la struct.

En `internal/web/plantillas/logs.html`, la celda del nivel pasa a ser el
control:

```html
<div class="l lv-{{ .Nivel }}"><span class="t">{{ (en .TS $.Zona).Format "02/01/2006 15:04:05" }}</span><a class="n n-{{ .Nivel }}" title="{{ t "cambiar-nivel" }}" href="/logs/reglas/nueva?rowid={{ .Rowid }}&amp;volver={{ $.Volver }}">{{ .Nivel }}</a><span class="c">{{ .Container }}</span><span class="m">{{ .Linea }}</span></div>
```

el encabezado avisa:

```html
<h1>logs{{ if .EnVivo }}<label class="vivo"><input type="checkbox" id="vivo"><span class="punto"></span>{{ t "en-vivo-toggle" }}</label>{{ end }}{{ with .Reglas }} <a class="reglas" href="/logs/reglas">{{ . }}</a>{{ end }}</h1>
```

y dos reglas de CSS:

```css
  /* La celda del nivel ES el control: click en el nivel para cambiarlo. Así no
     hace falta una columna más ni una línea de JS. Las líneas que pega el modo
     en vivo no lo traen hasta la próxima recarga, que es el precio de no tocar
     el JS. */
  a.n { text-decoration: none; }
  a.n:hover { text-decoration: underline; }
  .reglas { font-size: .8rem; margin-left: .75rem; opacity: .7; vertical-align: middle; }
```

`internal/web/plantillas/reglas.html`:

```html
<!doctype html>
<html lang="{{ lang }}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{ t "reglas-titulo" }} · server-status</title>
<style>
  :root { color-scheme: light dark; --borde:#8883; --err:#c0392b; --warn:#b7791f; --ok:#1a7f4b; }
  * { box-sizing: border-box; }
  body { font: 15px/1.5 system-ui, -apple-system, sans-serif; margin: 0; padding: 1.5rem; }
  h1 { font-size: 1.25rem; margin: 0 0 .5rem; }
  .nota { font-size: .85rem; opacity: .6; margin: 0 0 1.25rem; max-width: 46rem; }
  table { border-collapse: collapse; width: 100%; font-size: .9rem; }
  th, td { text-align: left; padding: .45rem .6rem; border-bottom: 1px solid var(--borde);
           vertical-align: top; }
  th { font-size: .78rem; text-transform: uppercase; letter-spacing: .04em; opacity: .55; }
  code { font: 12.5px/1.5 ui-monospace, Menlo, monospace; word-break: break-all; }
  button { font: inherit; font-size: .82rem; padding: .25rem .5rem; border-radius: .4rem;
           border: 1px solid var(--borde); background: transparent; color: inherit; cursor: pointer; }
  .vacio { opacity: .6; padding: 1rem 0; }
{{ template "nav-css" }}
</style>
</head>
<body>
{{ template "nav" .Nav }}

<h1>{{ t "reglas-titulo" }}</h1>
<p class="nota">{{ t "reglas-nota" }}</p>

<table>
  <tr>
    <th>{{ t "patron-col" }}</th><th>{{ t "container" }}</th><th>{{ t "nivel-col" }}</th>
    <th>{{ t "motivo" }}</th><th>{{ t "creada-col" }}</th><th>{{ t "coincidencias" }}</th><th></th>
  </tr>
  {{ range .Reglas }}
  <tr>
    <td><code>{{ .Patron }}</code></td>
    <td>{{ with .Container }}{{ . }}{{ else }}{{ t "todos" }}{{ end }}</td>
    <td>{{ .Nivel }}</td>
    <td>{{ .Motivo }}</td>
    <td>{{ hora .Creada $.Zona }}</td>
    <td>{{ .Coincidencias }}</td>
    <td>
      <form method="post" action="/logs/reglas/{{ .ID }}/borrar">
        <input type="hidden" name="volver" value="{{ $.Volver }}">
        <button type="submit">{{ t "borrar" }}</button>
      </form>
    </td>
  </tr>
  {{ else }}
  <tr><td colspan="7" class="vacio">{{ t "sin-reglas" }}</td></tr>
  {{ end }}
</table>
</body>
</html>
```

Textos nuevos en `internal/web/idiomas.go`:

```go
	"reglas-titulo": {"reglas de nivel", "level rules"},
	"reglas-nota": {
		"Cambian el nivel guardado de las líneas que contienen el patrón, las viejas y las que vengan. Borrar una devuelve esas líneas a su nivel original. Nada de esto toca los avisos de Telegram.",
		"They change the stored level of every line containing the pattern, old and new. Deleting one puts those lines back to their original level. None of this touches Telegram alerts.",
	},
	// Sin plural en el número: "1 reglas activas" se lee como un bug.
	"reglas-activas": {"reglas activas: %d", "active rules: %d"},
	"cambiar-nivel":  {"hacer una regla con esta línea", "make a rule from this line"},
	"patron-col":     {"Patrón", "Pattern"},
	"nivel-col":      {"Nivel", "Level"},
	"creada-col":     {"Creada", "Created"},
	"coincidencias":  {"líneas hoy", "lines today"},
	"borrar":         {"borrar", "delete"},
	"sin-reglas":     {"no hay ninguna regla puesta", "no rules set"},
```

- [ ] **Step 4: Correr los tests y verlos pasar**

```bash
go test ./internal/web/ -run 'TestLaListaDeReglas|TestBorrarUnaReglaDesdeElPanel|TestElEncabezadoDeLogs|TestCadaLineaOfrece|TestLosTextosDeReglas' -v
```

Esperado: PASS los cinco.

- [ ] **Step 5: Correr el paquete entero**

```bash
go test ./internal/web/ -race
```

Esperado: `ok`. El test que ya existe sobre el HTML de `/logs` cubre que la
fila no se rompió al cambiar el `span` por un `a`.

- [ ] **Step 6: Commit**

```bash
git add internal/web/panel.go internal/web/idiomas.go internal/web/plantillas/reglas.html internal/web/plantillas/logs.html internal/web/panel_test.go
git commit -m "feat(web): lista de reglas de nivel, borrar, y el aviso en el encabezado"
```

---

## Task 10: Verificación completa y documentación

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Correr todo, sin pipes**

Tres comandos sueltos. **Nada de `| tail`**: devuelve el exit code de `tail` y
tapa el fallo, ya se commiteó un test roto en este repo por eso.

```bash
make test
```

Esperado: `ok` en todos los paquetes.

```bash
make vet
```

Esperado: sin salida.

- [ ] **Step 2: Verificar la invariante 7 (sin cgo)**

```bash
make linux
```

Esperado: compila. Es el mismo `CGO_ENABLED=0` que corre CI; si alguna
dependencia nueva necesitara cgo, acá se cae.

- [ ] **Step 3: Verificar que la migración corre sobre una base que ya existe**

Que las migraciones apliquen sobre una base vacía no prueba que apliquen sobre
la del VPS, que está en la 11. Se simula con una base de dos pasadas: los tests
`TestOpenDosVecesNoRompe` y `TestBackfill*` ya abren dos veces, así que alcanza
con confirmarlo explícito:

```bash
go test ./internal/store/ -run 'TestOpen|TestUltimaMigracionAplicada' -v -count=1
```

Esperado: PASS. **La migración 12 solo crea una tabla nueva**, no toca `logs`
ni `log_niveles`, así que no reindexa FTS5 ni bloquea el arranque: es la
diferencia con la migración 9, que se diseñó como tabla lateral justamente para
no pagar ese costo.

- [ ] **Step 4: Actualizar CLAUDE.md**

Agregar, después de la tanda del 26/08, una entrada nueva:

```markdown
**Tanda del 31/08/2026: reglas de nivel desde una línea.** Plan en
`docs/superpowers/plans/2026-08-31-reglas-de-nivel.md`, diseño en
`docs/superpowers/specs/2026-08-31-reglas-de-nivel-design.md`. Nace de que el
99,5 % de los WARN de 24 h eran el 401 que Kong le devuelve a nuestra propia
sonda `egress-probe`: se arregló en el clasificador, y ese arreglo costó un
commit, un cross-compile, un deploy y un UPDATE a mano. Esto es para no volver
a pagarlo con el próximo container ruidoso.

- **`reglas_nivel` (migración 12)**: patrón, container opcional, nivel, motivo.
  Se aplican por orden de `id` y **gana la última que matchea**.
- **`logs.Clasificar` sigue siendo pura.** Las reglas son un tipo aparte
  (`logs.Reglas`) que se compone encima con `logs.Nivelar`, y esa composición
  es la ÚNICA forma en que se calcula el nivel guardado: la usan la ingesta, el
  backfill y el borrado de una regla.
- 🚨 **Una sola definición de "coincide"**: `strings.Contains` en Go e
  `instr()` en SQL, nunca `LIKE`, que es case-insensitive para ASCII mientras
  Go no lo es, y sin regex. `TestGoYSQLCuentanIgual` corre los dos caminos
  sobre el mismo corpus. Si divergieran, el número que uno confirma en el
  preview no sería el que le queda.
- **La aplicación retroactiva es un upsert y no un UPDATE**: una fila anterior
  a la migración 9 que el backfill no alcanzó no tiene fila en `log_niveles`, y
  un UPDATE la dejaría afuera aunque el preview la haya contado.
- ⚠️ **Crear o borrar una regla le saca la base al ciclo del minuto**: 43 679
  filas en 9,4 s medidos sobre la base del VPS, y el store abre UNA sola
  conexión. Es tolerable porque lo dispara una persona; no lo sería
  automatizado.
- **Las reglas se releen una vez por tick.** Sin mutex ni invalidación: la
  desactualización máxima es de un minuto, y la regla recién creada ya quedó
  aplicada sobre lo guardado.
- **Nada de esto toca los avisos.** Los niveles alimentan `/logs` y, solo los
  ERROR, la línea de tiempo de `/events`. Una regla mal puesta puede esconderte
  algo del visor; no puede callarte un aviso de Telegram. Ese límite es lo que
  hace que la función sea tolerable.
```

- [ ] **Step 5: Confirmar que no entró ninguna IP del VPS**

El repo es público. Las `172.19.x.x` de los corpus son de la red interna de
Docker y ya estaban en el repo; la pública y la de tailnet no entran nunca.

```bash
git diff main --stat
```

```bash
git diff main -- '*.go' '*.md' '*.html' | grep -nE '(^\+.*)([0-9]{1,3}\.){3}[0-9]{1,3}'
```

Esperado: solo líneas con `172.19.` o `127.0.0.1`. Cualquier otra IP se saca
antes de seguir.

- [ ] **Step 6: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: la tanda de reglas de nivel en CLAUDE.md"
```

- [ ] **Step 7: Parar**

**No hay deploy en este plan.** En el VPS hay una medición de egress corriendo
y producción es su control positivo: subir un binario nuevo la interrumpe. El
deploy lo decide Juan, después de revisar el código, y va con las dos cosas de
siempre: `make deploy` sube por `deploy/subir.sh` (que compara el sha256, porque
`scp` puede cortarse a mitad y devolver 0 igual) y el primer arranque aplica la
migración 12.

---

## Cobertura del spec

| Requisito del spec | Dónde |
|---|---|
| Migración 12 `reglas_nivel` | Task 4 |
| `Reglas.Aplicar`, conflictos por orden de id | Task 1 |
| Reglas releídas una vez por tick | Task 6 |
| Aplicación retroactiva con conteo previo == efecto | Tasks 3 y 4 |
| 🚨 Una sola definición de coincide (Go vs SQL, sin LIKE, sin regex) | Task 3 |
| `PatronSugerido` | Task 2 |
| Control en cada fila de `/logs` | Task 9 |
| Página de regla nueva con preview y recalcular | Task 8 |
| `/logs/reglas` con motivo, fecha y match count | Task 9 |
| Borrar re-corre `Clasificar` (undo exacto) | Task 5 |
| "N reglas activas" en el encabezado | Task 9 |
| Las dos direcciones (bajar a TRACE, subir a ERROR) | Tasks 1, 8 (el `<select>` ofrece los cuatro) |
| `TestUltimaMigracionAplicada` pasa a 12 | Task 4 |
| Panel bilingüe | Tasks 8 y 9 |

**Fuera de alcance, tal como dice el spec:** regex, reglas con vencimiento,
import/export, scope por `stream`, y aplicar una regla en background por lotes.

## Dos cosas que el plan resuelve y el spec no decía explícito

1. **Borrar re-aplica también las reglas que QUEDAN**, no solo `Clasificar`. El
   spec dice las dos cosas por separado ("gana la última que matchea" y "borrar
   re-corre Clasificar"); juntas, con dos reglas encima de la misma línea,
   borrar una se llevaría el efecto de la otra. Task 5 lo resuelve con
   `logs.Nivelar`, que es la misma composición de la ingesta, y va con test.
2. **El motivo es obligatorio.** El spec dice por qué importa ("una regla sin
   motivo es una regla que no te vas a animar a borrar") pero no que se valide.
   Se valida: un POST sin motivo es 400.

## Lo que este plan NO promete

- **El conteo previo es del momento en que se miró.** Entre el preview y el
  confirmar puede entrar un tick de ingesta y sumar líneas nuevas que también
  matcheen. El test afirma que las dos consultas usan la misma definición de
  coincidencia, que es lo único que se puede afirmar sin frenar la ingesta.
- **Las líneas que pega el modo en vivo no traen el control** hasta la próxima
  recarga: la celda del nivel la arma el JS y este plan no toca JS.
- **El pid queda en el patrón sugerido** de las líneas de Postgres. Es un punto
  de partida editable y el conteo previo avisa enseguida que un patrón con pid
  matchea muy poco.
