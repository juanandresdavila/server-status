# server-status — Plan de implementación, fase 8: logs

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Guardar 30 días de los logs de las apps, poder buscarlos, verlos en vivo, y que un loop de errores dispare un aviso.

**Architecture:** Ingesta por consulta una vez por minuto con cursor persistido; tail en vivo por stream, solo mientras alguien mira. Una sola tabla FTS5.

**Spec:** `docs/superpowers/specs/2026-08-09-fase-8-logs-design.md` — tiene los porqués y las mediciones que los justifican.

---

## Task 1: Demultiplexar el stream de Docker

El gotcha más caro de la fase. Con `Tty: false` —los 21 containers— Docker manda
bloques con 8 bytes de encabezado, no texto plano.

**Files:** `internal/collector/docker/logs.go`, `internal/collector/docker/logs_test.go`

- [ ] **Step 1: Escribir el test que falla**

```go
package docker_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/juanandresdavila/server-status/internal/collector/docker"
)

// bloque arma un frame como los que manda Docker: 1 byte de tipo de stream,
// 3 de relleno, 4 de tamaño big-endian, y el payload.
func bloque(tipo byte, payload string) []byte {
	var b bytes.Buffer
	b.WriteByte(tipo)
	b.Write([]byte{0, 0, 0})
	binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.WriteString(payload)
	return b.Bytes()
}

func TestDemuxSeparaStdoutDeStderr(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "2026-08-09T12:00:00.000000000Z arranque ok\n"))
	stream.Write(bloque(2, "2026-08-09T12:00:01.000000000Z ERROR se cayó\n"))

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatalf("DemuxLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d líneas, quería 2", len(got))
	}
	if got[0].Stream != "stdout" || got[1].Stream != "stderr" {
		t.Errorf("streams = %q, %q", got[0].Stream, got[1].Stream)
	}
	if got[0].Linea != "arranque ok" {
		t.Errorf("Linea = %q, quería 'arranque ok' — ¿quedó el timestamp adentro?", got[0].Linea)
	}
	if got[1].TS.Format("15:04:05") != "12:00:01" {
		t.Errorf("TS = %v", got[1].TS)
	}
}

// El payload de un bloque puede traer varias líneas de una.
func TestDemuxParteLasLineasDeUnMismoBloque(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "2026-08-09T12:00:00Z una\n2026-08-09T12:00:01Z dos\n"))

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d líneas, quería 2", len(got))
	}
}

// Si no se demultiplexa, la línea llega con basura binaria adelante. Este es
// el test que atrapa el error si alguien "simplifica" leyendo el stream crudo.
func TestDemuxNoDejaBytesDeEncabezadoEnElTexto(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "2026-08-09T12:00:00Z hola\n"))

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got[0].Linea {
		if c < 32 && c != '\t' {
			t.Fatalf("quedó un byte de control en el texto: %q", got[0].Linea)
		}
	}
}

// Un stream cortado a la mitad no puede hacer perder lo que ya se leyó: pasa
// cuando el container se muere mientras se lo está leyendo.
func TestDemuxDevuelveLoLeidoAunqueElStreamSeCorte(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "2026-08-09T12:00:00Z completa\n"))
	stream.Write([]byte{1, 0, 0, 0, 0, 0, 255}) // encabezado truncado

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatalf("no debería devolver error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("volvieron %d líneas, quería 1 (la completa)", len(got))
	}
}

// Sin timestamp la línea igual sirve: se le pone la hora de lectura.
func TestDemuxSinTimestampNoDescartaLaLinea(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "sin fecha adelante\n"))

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Linea, "sin fecha") {
		t.Errorf("got = %+v", got)
	}
}
```

- [ ] **Step 2: Verificar que falla** — `docker.DemuxLogs undefined`

- [ ] **Step 3: Implementar**

```go
package docker

import (
	"bufio"
	"encoding/binary"
	"io"
	"strings"
	"time"
)

// LineaLog es una línea ya demultiplexada y fechada.
type LineaLog struct {
	TS     time.Time
	Stream string // stdout | stderr
	Linea  string
}

// DemuxLogs parsea el formato multiplexado de Docker.
//
// Con Tty:false —el caso de los 21 containers— Docker no manda texto plano
// sino bloques con 8 bytes de encabezado:
//
//	byte 0     tipo: 1 = stdout, 2 = stderr
//	bytes 1-3  cero
//	bytes 4-7  tamaño del payload, big-endian
//
// Un stream cortado no es un error: se devuelve lo que se alcanzó a leer.
// Pasa cuando el container se muere mientras se lo lee, y perder las líneas
// buenas por eso sería peor que el corte.
func DemuxLogs(r io.Reader) ([]LineaLog, error) {
	br := bufio.NewReader(r)
	var out []LineaLog

	for {
		var cab [8]byte
		if _, err := io.ReadFull(br, cab[:]); err != nil {
			return out, nil // EOF o corte: lo leído vale
		}
		tam := binary.BigEndian.Uint32(cab[4:8])
		if tam == 0 {
			continue
		}
		payload := make([]byte, tam)
		if _, err := io.ReadFull(br, payload); err != nil {
			return out, nil
		}

		stream := "stdout"
		if cab[0] == 2 {
			stream = "stderr"
		}
		for _, cruda := range strings.Split(strings.TrimRight(string(payload), "\n"), "\n") {
			if cruda == "" {
				continue
			}
			ts, linea := partirTimestamp(cruda)
			out = append(out, LineaLog{TS: ts, Stream: stream, Linea: linea})
		}
	}
}

// partirTimestamp separa el prefijo RFC3339Nano que agrega ?timestamps=1.
// Sin prefijo, la línea igual sirve: se le pone la hora de lectura.
func partirTimestamp(cruda string) (time.Time, string) {
	fecha, resto, ok := strings.Cut(cruda, " ")
	if !ok {
		return time.Now().UTC(), cruda
	}
	ts, err := time.Parse(time.RFC3339Nano, fecha)
	if err != nil {
		return time.Now().UTC(), cruda
	}
	return ts.UTC(), resto
}
```

- [ ] **Step 4: Verificar y commitear**

---

## Task 2: Pedir los logs a Docker

**Files:** `internal/collector/docker/logs.go`, test en el mismo archivo

- [ ] **Step 1: Test** — un `servidorFalso` que verifica los parámetros de la
query y devuelve bloques multiplexados:

```go
func TestLogsPideDesdeElCursorYConTimestamps(t *testing.T) {
	var q url.Values
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		w.Write(bloque(1, "2026-08-09T12:00:00Z hola\n"))
	}))

	desde := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	got, err := c.Logs(context.Background(), "abc", desde)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("since") != strconv.FormatInt(desde.Unix(), 10) {
		t.Errorf("since = %q, quería %d", q.Get("since"), desde.Unix())
	}
	// Sin timestamps=1 no hay forma de saber CUÁNDO pasó cada línea.
	if q.Get("timestamps") != "1" {
		t.Errorf("falta timestamps=1")
	}
	if q.Get("stdout") != "1" || q.Get("stderr") != "1" {
		t.Errorf("faltan stdout/stderr")
	}
	if q.Get("follow") == "1" {
		t.Error("la ingesta NO debe usar follow: son 21 conexiones vivas para 2 líneas por minuto")
	}
	if len(got) != 1 {
		t.Errorf("volvieron %d líneas", len(got))
	}
}
```

- [ ] **Step 2-4: Implementar `(*Client).Logs(ctx, id, desde)`**, que arma la
query, hace el GET —**usando el mismo transporte sobre el socket unix**— y pasa
el cuerpo por `DemuxLogs`. Verificar y commitear.

---

## Task 3: Migración 0005 y persistencia

**Files:** `internal/store/store.go`, `internal/store/store_test.go`, `internal/model/model.go`

- [ ] **Step 1: Tests**

```go
func TestInsertLogsYBusqueda(t *testing.T) { ... }

// El cursor es lo que hace que un reinicio no repita ni pierda.
func TestCursorSobreviveYAvanza(t *testing.T) {
	s := abrir(t)
	if _, err := s.CursorDeLog("comm-tool"); err == nil {
		// sin cursor previo devuelve "no hay"
	}
	ts := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	if err := s.GuardarCursorDeLog("comm-tool", ts); err != nil { t.Fatal(err) }
	got, err := s.CursorDeLog("comm-tool")
	if err != nil { t.Fatal(err) }
	if !got.Equal(ts) { t.Errorf("cursor = %v, quería %v", got, ts) }
}

// La invariante 11: un paréntesis suelto en el buscador no puede romper la
// consulta con un error de sintaxis de FTS5.
func TestBuscarConTextoRaroNoExplota(t *testing.T) {
	s := abrir(t)
	s.InsertLogs([]model.LineaLog{{TS: time.Now(), Container: "x", Stream: "stdout", Linea: "algo"}})

	for _, raro := range []string{"(", `"`, "AND", "*", "a OR", "^%$#"} {
		if _, err := s.BuscarLogs(raro, "", time.Time{}, time.Now(), 50); err != nil {
			t.Errorf("BuscarLogs(%q) devolvió error: %v", raro, err)
		}
	}
}

func TestRetencionBorraLoViejoYDejaLoNuevo(t *testing.T) { ... }
```

- [ ] **Step 2-4: Implementar**

```sql
CREATE VIRTUAL TABLE logs USING fts5(
  linea, container UNINDEXED, stream UNINDEXED, ts UNINDEXED
);
CREATE TABLE log_cursors (
  container TEXT PRIMARY KEY, ultimo_ts INTEGER NOT NULL
) STRICT;
```

El escape del texto de búsqueda:

```go
// escaparMatch envuelve el texto entre comillas dobles para que FTS5 lo tome
// literal. Sin esto, un paréntesis suelto rompe la consulta con un error de
// sintaxis — invariante 11 del spec.
//
// Se respeta el asterisco final, que es lo que hace útil una búsqueda
// incremental: "conex*" matchea "conexion".
func escaparMatch(texto string) string {
	texto = strings.TrimSpace(texto)
	prefijo := strings.HasSuffix(texto, "*")
	texto = strings.TrimSuffix(texto, "*")
	texto = `"` + strings.ReplaceAll(texto, `"`, `""`) + `"`
	if prefijo {
		texto += "*"
	}
	return texto
}
```

---

## Task 4: Ingesta en el loop

**Files:** `cmd/server-status/main.go`

- [ ] **Step 1:** Por cada container, en el tick del minuto: leer cursor,
pedir logs desde ahí, insertar, guardar el cursor nuevo.

**Un container sin cursor arranca desde AHORA**, no desde su historia: traer los
30 días que Docker conserve en el primer tick sería un pico inútil. Es la
invariante 10.

- [ ] **Step 2:** Verificar que el loop del minuto sigue cerrando en tiempo con
los 21 containers.

---

## Task 5: Alertas por patrón

**Files:** `internal/rules/motor.go`, `internal/config/config.go`

- [ ] **Step 1: Test** — 10 coincidencias en 5 minutos abren; 2 cierran.

- [ ] **Step 2: Implementar `EvaluarLogs(conteos map[string]int)`**, que aplica
`PorUmbral{Abre: 10, Cierra: 2}` con sujeto `logs:<container>`. **No hace falta
código nuevo de reglas**: la política por umbral de la fase 3 sirve tal cual.

El mensaje lleva el conteo y **una** línea de muestra, nunca las diez.

---

## Task 6: Página de logs en el panel

**Files:** `internal/web/panel.go`, `internal/web/plantillas/logs.html`

- [ ] Filtros por container, texto y rango. Resultados paginados, más nuevos
primero. El texto va por `escaparMatch`.

---

## Task 7: Tail en vivo por SSE

**Files:** `internal/web/tail.go`, `internal/web/tail_test.go`

- [ ] **Step 1: Test** — que el handler emita `data: ` por línea y que **cierre
el stream de Docker cuando el cliente se va** (`ctx.Done`). Sin eso queda una
conexión colgada por cada pestaña que alguien abrió y cerró.

- [ ] **Step 2: Implementar** con `follow=1`, `http.Flusher` y
`Content-Type: text/event-stream`.

---

## Task 8: Retención

**Files:** `cmd/server-status/main.go`, `internal/store/store.go`

- [ ] Pasada diaria a las 04:00: borrar logs de más de 30 días. Tope duro por
cantidad de filas como red de contención, por si alguna app se pone a loguear
en loop.

---

## Task 9: Verificación contra el VPS

- [ ] `make deploy`, esperar unos minutos, y confirmar que hay líneas de varios
containers en la base.
- [ ] Buscar algo real desde el panel y que devuelva.
- [ ] Abrir el tail de `comm-tool` y ver una línea aparecer en vivo.
- [ ] **Confirmar que las líneas no tienen basura binaria adelante** — es el
síntoma de que el demux falló, y el que más fácil pasa desapercibido.
- [ ] Reiniciar el servicio y confirmar que **no se duplican** líneas: es el
cursor haciendo su trabajo.

---

## Autorevisión

**Cobertura del spec de la fase 8:** fuentes §2 → task 2; los dos mecanismos §3
→ tasks 4 y 7; formato multiplexado §4 → task 1; almacenamiento §5 → task 3;
alertas §6 → task 5; panel §7 → tasks 6 y 7; invariantes 10 y 11 → tasks 4 y 3.

**Riesgo más alto: la task 1.** Si el demux está mal, todo lo demás guarda
basura y el síntoma —unos bytes raros al principio de cada línea— es fácil de
pasar por alto en una tabla HTML. Por eso tiene cinco tests, incluido uno que
falla si alguien "simplifica" leyendo el stream crudo.

**Fuera de este plan:** los access logs de Caddy y el comando `/logs` de
Telegram, los dos por decisión explícita del spec. Y sigue pendiente el
`VACUUM INTO` del backup, que ya arrastra cuatro planes — va en el próximo, sí
o sí, antes de que la base crezca con los logs.
