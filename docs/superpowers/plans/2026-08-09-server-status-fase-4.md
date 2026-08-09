# server-status — Plan de implementación, fase 4: avisos por Telegram

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Que un incidente que se abre o se cierra llegue como mensaje de Telegram, por comm-tool y con respaldo directo, sin repetir y sin volverse ruido.

**Architecture:** Los avisos pendientes **no se guardan en una cola**: se derivan de comparar `incidents` contra `notifications`. Un incidente sin su fila `<id>:opened` es un aviso que falta mandar. Eso hace que una caída del proceso entre abrir el incidente y mandar el mensaje se resuelva sola en el tick siguiente, sin código de recuperación.

**Tech Stack:** Go, `net/http`, SQLite, reloj inyectado.

**Spec:** `docs/superpowers/specs/2026-08-08-server-status-design.md`
**Planes anteriores:** fases 0-1 y 2-3, en esta misma carpeta.

---

## Lo que hace falta de tu lado

Tres cosas que no puedo hacer yo y bloquean **solo la verificación final**, no el código:

1. **Crear el bot** con `@BotFather` y guardar el token.
2. **Dar de alta la app en comm-tool** con `scripts/registrar-app.ts` — el comando exacto está en la Task 11.
3. **Vincular tu chat** mandándole `/vincular <código>` al bot nuevo.

Hasta que eso exista, el servicio arranca igual: si faltan las credenciales **loguea el aviso y sigue**, con una advertencia al inicio. Es a propósito — un monitor que no arranca porque no puede avisar es peor que uno que avisa a medias.

---

## Estructura de archivos

| Archivo | Responsabilidad |
|---|---|
| `internal/notify/canal.go` | La interfaz `Canal` y el tipo `Aviso` |
| `internal/notify/telegram/telegram.go` | Cliente directo de `api.telegram.org` |
| `internal/notify/commtool/commtool.go` | Cliente de `POST /v1/messages` |
| `internal/notify/notificador.go` | Respaldo, deduplicación y vencimiento |
| `internal/notify/texto.go` | Redacción de cada tipo de mensaje |
| `internal/rules/motor.go` | Se le suman containers y amortiguación de rebotes |
| `internal/store/store.go` | Migración 0004 y las consultas de avisos |

---

## Task 1: Migración 0004 y estado de los avisos

**Files:**
- Modify: `internal/model/model.go`, `internal/store/store.go`, `internal/store/store_test.go`

- [ ] **Step 1: Agregar el tipo**

Agregar a `internal/model/model.go`:

```go
// Aviso es un mensaje que hay que mandar. El DeliveryID es determinístico:
// '<incidenteID>:opened' o '<incidenteID>:closed'. Con un uuid nuevo por
// intento, un reintento mandaría el aviso dos veces — lección copiada de
// comm-tool.
type Aviso struct {
	DeliveryID string
	Incidente  Incidente
	Cierre     bool // false = se abrió, true = se cerró
}
```

- [ ] **Step 2: Escribir el test que falla**

Agregar a `internal/store/store_test.go`:

```go
func TestAvisoPendienteAlAbrirUnIncidente(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, err := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:comm-tool", Tipo: "down", Severidad: "critical",
		AbiertoEn: ts, Detalle: "HTTP 502",
	})
	if err != nil {
		t.Fatal(err)
	}

	pend, err := s.AvisosPendientes()
	if err != nil {
		t.Fatalf("AvisosPendientes: %v", err)
	}
	if len(pend) != 1 {
		t.Fatalf("hay %d pendientes, quería 1", len(pend))
	}
	quiero := strconv.FormatInt(id, 10) + ":opened"
	if pend[0].DeliveryID != quiero {
		t.Errorf("DeliveryID = %q, quería %q", pend[0].DeliveryID, quiero)
	}
	if pend[0].Cierre {
		t.Error("Cierre = true en un incidente recién abierto")
	}
}

// Marcarlo enviado lo saca de pendientes. Esto es lo que evita que el bot
// mande el mismo aviso en cada tick.
func TestMarcarEnviadoSacaDePendientes(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, _ := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:x", Tipo: "down", Severidad: "critical",
		AbiertoEn: ts, Detalle: "d",
	})
	dst := strconv.FormatInt(id, 10) + ":opened"

	if err := s.MarcarEnviado(dst, ts, "commtool", ""); err != nil {
		t.Fatalf("MarcarEnviado: %v", err)
	}

	pend, err := s.AvisosPendientes()
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 0 {
		t.Errorf("quedaron %d pendientes después de marcar enviado", len(pend))
	}
}

// Cerrar el incidente genera un segundo aviso, distinto del de apertura.
func TestCerrarGeneraSuPropioAviso(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, _ := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:x", Tipo: "down", Severidad: "critical",
		AbiertoEn: ts, Detalle: "d",
	})
	s.MarcarEnviado(strconv.FormatInt(id, 10)+":opened", ts, "commtool", "")

	if err := s.CerrarIncidente(id, ts.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	pend, err := s.AvisosPendientes()
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 {
		t.Fatalf("hay %d pendientes, quería 1 (el de cierre)", len(pend))
	}
	if !pend[0].Cierre {
		t.Error("Cierre = false en el aviso de un incidente cerrado")
	}
	if pend[0].DeliveryID != strconv.FormatInt(id, 10)+":closed" {
		t.Errorf("DeliveryID = %q", pend[0].DeliveryID)
	}
}

// Marcar dos veces el mismo id no puede explotar: pasa cuando el proceso
// muere justo después de mandar y antes de commitear.
func TestMarcarEnviadoDosVecesEsInofensivo(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	if err := s.MarcarEnviado("1:opened", ts, "commtool", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MarcarEnviado("1:opened", ts, "telegram", ""); err != nil {
		t.Errorf("el segundo MarcarEnviado falló: %v", err)
	}
}
```

Sumar `"strconv"` a los imports del test.

- [ ] **Step 3: Correr el test y verificar que falla**

Run: `go test ./internal/store/`
Expected: FAIL — `s.AvisosPendientes undefined`

- [ ] **Step 4: Implementar**

Agregar la migración al final del slice `migraciones`:

```go
	`CREATE TABLE notifications (
		delivery_id TEXT    PRIMARY KEY,
		sent_at     INTEGER NOT NULL,
		via         TEXT    NOT NULL,
		error       TEXT    NOT NULL
	) STRICT;`,
```

Y los métodos:

```go
// MarcarEnviado es idempotente a propósito: el proceso puede morirse entre
// mandar el mensaje y registrar que lo mandó, y el reintento tiene que poder
// escribir sin explotar.
func (s *Store) MarcarEnviado(deliveryID string, cuando time.Time, via, errMsg string) error {
	_, err := s.db.Exec(`
		INSERT INTO notifications (delivery_id, sent_at, via, error)
		VALUES (?,?,?,?)
		ON CONFLICT(delivery_id) DO NOTHING`,
		deliveryID, cuando.Unix(), via, errMsg)
	return err
}

// AvisosPendientes deriva la cola de comparar incidents con notifications:
// un incidente sin su fila de aviso ES un aviso pendiente. Sin tabla de cola,
// una caída entre abrir el incidente y mandar el mensaje se resuelve sola.
func (s *Store) AvisosPendientes() ([]model.Aviso, error) {
	filas, err := s.db.Query(`
		SELECT i.id, i.subject, i.kind, i.severity, i.opened_at, i.closed_at, i.detail, 0 AS cierre
		FROM incidents i
		LEFT JOIN notifications n ON n.delivery_id = CAST(i.id AS TEXT) || ':opened'
		WHERE n.delivery_id IS NULL

		UNION ALL

		SELECT i.id, i.subject, i.kind, i.severity, i.opened_at, i.closed_at, i.detail, 1 AS cierre
		FROM incidents i
		LEFT JOIN notifications n ON n.delivery_id = CAST(i.id AS TEXT) || ':closed'
		WHERE i.closed_at IS NOT NULL AND n.delivery_id IS NULL

		ORDER BY 5, 8`)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.Aviso
	for filas.Next() {
		var (
			i       model.Incidente
			abierto int64
			cerrado sql.NullInt64
			cierre  int
		)
		if err := filas.Scan(&i.ID, &i.Sujeto, &i.Tipo, &i.Severidad,
			&abierto, &cerrado, &i.Detalle, &cierre); err != nil {
			return nil, err
		}
		i.AbiertoEn = time.Unix(abierto, 0).UTC()
		if cerrado.Valid {
			t := time.Unix(cerrado.Int64, 0).UTC()
			i.CerradoEn = &t
		}
		sufijo := ":opened"
		if cierre == 1 {
			sufijo = ":closed"
		}
		out = append(out, model.Aviso{
			DeliveryID: strconv.FormatInt(i.ID, 10) + sufijo,
			Incidente:  i,
			Cierre:     cierre == 1,
		})
	}
	return out, filas.Err()
}
```

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/store internal/model
git commit -m "feat: cola de avisos derivada de incidents contra notifications"
```

---

## Task 2: Contar ciclos para la amortiguación de rebotes

**Files:**
- Modify: `internal/store/store.go`, `internal/store/store_test.go`

- [ ] **Step 1: Escribir el test que falla**

```go
func TestCiclosEnVentanaCuentaSoloLosDeEseSujeto(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	for i := range 4 {
		id, err := s.AbrirIncidente(model.Incidente{
			Sujeto: "service:x", Tipo: "down", Severidad: "critical",
			AbiertoEn: base.Add(time.Duration(i*10) * time.Minute), Detalle: "d",
		})
		if err != nil {
			t.Fatal(err)
		}
		// Hay que cerrarlo para poder abrir el siguiente: lo impide el índice.
		if err := s.CerrarIncidente(id, base.Add(time.Duration(i*10+5)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:otro", Tipo: "down", Severidad: "critical",
		AbiertoEn: base, Detalle: "d",
	})
	s.CerrarIncidente(id, base.Add(time.Minute))

	n, err := s.CiclosEnVentana("service:x", base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CiclosEnVentana: %v", err)
	}
	if n != 4 {
		t.Errorf("ciclos = %d, quería 4", n)
	}
}

func TestCiclosEnVentanaIgnoraLoViejo(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, _ := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:x", Tipo: "down", Severidad: "critical",
		AbiertoEn: base.Add(-3 * time.Hour), Detalle: "viejo",
	})
	s.CerrarIncidente(id, base.Add(-3*time.Hour).Add(time.Minute))

	n, err := s.CiclosEnVentana("service:x", base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("ciclos = %d, quería 0: el incidente es de hace 3 horas", n)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/store/ -run Ciclos`
Expected: FAIL — `s.CiclosEnVentana undefined`

- [ ] **Step 3: Implementar**

```go
// CiclosEnVentana cuenta cuántos incidentes de un sujeto se abrieron desde un
// momento dado. Es la medida de "esto está rebotando".
func (s *Store) CiclosEnVentana(sujeto string, desde time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM incidents WHERE subject = ? AND opened_at >= ?`,
		sujeto, desde.Unix()).Scan(&n)
	return n, err
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat: contar ciclos de un sujeto en una ventana de tiempo"
```

---

## Task 3: Canal y cliente directo de Telegram

**Files:**
- Create: `internal/notify/canal.go`, `internal/notify/telegram/telegram.go`, `internal/notify/telegram/telegram_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/notify/telegram/telegram_test.go`:

```go
package telegram_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juanandresdavila/server-status/internal/notify/telegram"
)

func TestMandarPosteaChatIdYTexto(t *testing.T) {
	var cuerpo map[string]any
	var ruta string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ruta = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &cuerpo)
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	c := telegram.New(srv.URL, "TOKEN123", "456789")
	if err := c.Mandar(context.Background(), "hola"); err != nil {
		t.Fatalf("Mandar: %v", err)
	}

	// El token va en la ruta, que es como funciona la API de Telegram.
	if !strings.Contains(ruta, "TOKEN123") || !strings.HasSuffix(ruta, "/sendMessage") {
		t.Errorf("ruta = %q", ruta)
	}
	if cuerpo["chat_id"] != "456789" {
		t.Errorf("chat_id = %v", cuerpo["chat_id"])
	}
	if cuerpo["text"] != "hola" {
		t.Errorf("text = %v", cuerpo["text"])
	}
}

func TestMandarFallaConErrorDeTelegram(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	c := telegram.New(srv.URL, "T", "1")
	err := c.Mandar(context.Background(), "hola")
	if err == nil {
		t.Fatal("quería error, no hubo")
	}
	// La descripción de Telegram tiene que llegar al log: sin ella,
	// diagnosticar por qué no llega un aviso es adivinar.
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("el error no incluye la descripción: %v", err)
	}
}

// Sin credenciales el canal no está configurado y hay que poder saberlo
// sin intentar mandar.
func TestSinTokenNoEstaConfigurado(t *testing.T) {
	if telegram.New("https://x", "", "1").Configurado() {
		t.Error("sin token dice estar configurado")
	}
	if telegram.New("https://x", "T", "").Configurado() {
		t.Error("sin chat id dice estar configurado")
	}
	if !telegram.New("https://x", "T", "1").Configurado() {
		t.Error("con token y chat id dice NO estar configurado")
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/notify/telegram/`
Expected: FAIL — el paquete no existe

- [ ] **Step 3: Implementar la interfaz**

`internal/notify/canal.go`:

```go
// Package notify manda los avisos.
package notify

import "context"

// Canal es una vía de salida. Las implementaciones viven en subpaquetes.
type Canal interface {
	// Nombre es lo que se guarda en notifications.via.
	Nombre() string
	// Configurado dice si tiene credenciales. Un canal sin configurar no se
	// intenta: fallaría siempre y ensuciaría el log.
	Configurado() bool
	Mandar(ctx context.Context, texto string) error
}
```

- [ ] **Step 4: Implementar el cliente**

`internal/notify/telegram/telegram.go`:

```go
// Package telegram habla directo con api.telegram.org.
//
// Es el camino de respaldo: existe para el caso en que lo caído sea comm-tool,
// que es justo el aviso más importante que puede haber.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Canal struct {
	base   string
	token  string
	chatID string
	http   *http.Client
}

// New recibe la base de la API como parámetro para poder apuntarla a un
// httptest en los tests. En producción es "https://api.telegram.org".
func New(base, token, chatID string) *Canal {
	return &Canal{
		base: base, token: token, chatID: chatID,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Canal) Nombre() string { return "telegram" }

func (c *Canal) Configurado() bool { return c.token != "" && c.chatID != "" }

func (c *Canal) Mandar(ctx context.Context, texto string) error {
	cuerpo, err := json.Marshal(map[string]string{
		"chat_id": c.chatID,
		"text":    texto,
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", c.base, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(cuerpo))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// El cuerpo trae "description" con la causa real. Se lee acotado:
		// nunca confiar en el largo de una respuesta ajena.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return nil
}
```

⚠️ El error de la URL nunca puede incluir el token: `fmt.Errorf` acá usa `resp.Status` y el cuerpo, no `url`. Si alguien agrega la URL al mensaje de error, el token termina en el journal.

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./internal/notify/... -v`
Expected: PASS, 3 tests

- [ ] **Step 6: Commit**

```bash
git add internal/notify
git commit -m "feat: canal de Telegram directo, el camino de respaldo"
```

---

## Task 4: Cliente de comm-tool

**Files:**
- Create: `internal/notify/commtool/commtool.go`, `internal/notify/commtool/commtool_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/notify/commtool/commtool_test.go`:

```go
package commtool_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juanandresdavila/server-status/internal/notify/commtool"
)

func TestMandarUsaBearerYElCuerpoQueEsperaLaAPI(t *testing.T) {
	var auth, ruta string
	var cuerpo map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		ruta = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &cuerpo)
		w.Write([]byte(`{"messageId":"m1","providerMessageId":"p1","status":"sent"}`))
	}))
	defer srv.Close()

	c := commtool.New(srv.URL, "KEY", "uuid-de-juan")
	if err := c.MandarCon(context.Background(), "hola", "7:opened"); err != nil {
		t.Fatalf("MandarCon: %v", err)
	}

	if auth != "Bearer KEY" {
		t.Errorf("Authorization = %q, quería 'Bearer KEY'", auth)
	}
	if ruta != "/v1/messages" {
		t.Errorf("ruta = %q", ruta)
	}
	if cuerpo["userId"] != "uuid-de-juan" {
		t.Errorf("userId = %v", cuerpo["userId"])
	}
	if cuerpo["text"] != "hola" {
		t.Errorf("text = %v", cuerpo["text"])
	}
	// kind es obligatorio en el schema de comm-tool y esto no es una respuesta.
	if cuerpo["kind"] != "notification" {
		t.Errorf("kind = %v, quería notification", cuerpo["kind"])
	}
	// La clave de idempotencia es el delivery id: si el aviso se reintenta,
	// comm-tool lo deduplica del otro lado también.
	if cuerpo["idempotencyKey"] != "7:opened" {
		t.Errorf("idempotencyKey = %v", cuerpo["idempotencyKey"])
	}
}

// 409 in_progress significa que comm-tool no puede saber si el mensaje salió.
// Reintentar arriesga un duplicado, así que se trata como terminal.
func TestConflictoSeTomaComoEnviado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"code":"in_progress"}`))
	}))
	defer srv.Close()

	c := commtool.New(srv.URL, "KEY", "u")
	if err := c.MandarCon(context.Background(), "hola", "1:opened"); err != nil {
		t.Errorf("un 409 devolvió error: %v", err)
	}
}

func TestNotLinkedEsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"code":"not_linked"}`))
	}))
	defer srv.Close()

	c := commtool.New(srv.URL, "KEY", "u")
	err := c.MandarCon(context.Background(), "hola", "1:opened")
	if err == nil {
		t.Fatal("un 404 not_linked no devolvió error, y tiene que caer al respaldo")
	}
	if !strings.Contains(err.Error(), "not_linked") {
		t.Errorf("el error no dice la causa: %v", err)
	}
}

func TestSinCredencialesNoEstaConfigurado(t *testing.T) {
	if commtool.New("https://x", "", "u").Configurado() {
		t.Error("sin API key dice estar configurado")
	}
	if commtool.New("https://x", "K", "").Configurado() {
		t.Error("sin userId dice estar configurado")
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/notify/commtool/`
Expected: FAIL — el paquete no existe

- [ ] **Step 3: Implementar**

`internal/notify/commtool/commtool.go`:

```go
// Package commtool manda por la API de communication-tool.
//
// Es el camino principal. La forma del request está tomada del código de
// comm-tool: POST /v1/messages, Authorization Bearer, y un cuerpo con userId,
// text y kind — los tres obligatorios en su schema.
package commtool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Canal struct {
	base   string
	apiKey string
	userID string
	http   *http.Client
}

// New recibe el userID que comm-tool usa para resolver el contacto. server-status
// no tiene tabla de usuarios: es un uuid fijo, generado una vez. comm-tool
// nunca lo interpreta — es una invariante suya.
func New(base, apiKey, userID string) *Canal {
	return &Canal{
		base: base, apiKey: apiKey, userID: userID,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Canal) Nombre() string { return "commtool" }

func (c *Canal) Configurado() bool { return c.apiKey != "" && c.userID != "" }

// Mandar cumple notify.Canal. Sin clave de idempotencia comm-tool no deduplica
// nada, así que el camino normal es MandarCon.
func (c *Canal) Mandar(ctx context.Context, texto string) error {
	return c.MandarCon(ctx, texto, "")
}

func (c *Canal) MandarCon(ctx context.Context, texto, idempotencyKey string) error {
	payload := map[string]string{
		"userId": c.userID,
		"text":   texto,
		"kind":   "notification",
	}
	if idempotencyKey != "" {
		payload["idempotencyKey"] = idempotencyKey
	}
	cuerpo, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/messages", bytes.NewReader(cuerpo))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("comm-tool: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusConflict:
		// in_progress: la reserva anterior no se cerró y comm-tool no puede
		// saber si el mensaje salió. Reintentar arriesga un duplicado, así que
		// se da por entregado.
		return nil
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("comm-tool: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/notify/... -v`
Expected: PASS, 7 tests

- [ ] **Step 5: Commit**

```bash
git add internal/notify
git commit -m "feat: canal de comm-tool con clave de idempotencia"
```

---

## Task 5: Notificador con respaldo y deduplicación

**Files:**
- Create: `internal/notify/notificador.go`, `internal/notify/notificador_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/notify/notificador_test.go`:

```go
package notify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/notify"
)

type canalFalso struct {
	nombre      string
	configurado bool
	falla       error
	mandados    []string
}

func (c *canalFalso) Nombre() string    { return c.nombre }
func (c *canalFalso) Configurado() bool { return c.configurado }
func (c *canalFalso) Mandar(_ context.Context, texto string) error {
	if c.falla != nil {
		return c.falla
	}
	c.mandados = append(c.mandados, texto)
	return nil
}

type storeFalso struct {
	marcados map[string]string // deliveryID → via
}

func (s *storeFalso) MarcarEnviado(id string, _ time.Time, via, _ string) error {
	s.marcados[id] = via
	return nil
}

func aviso(id int64, cierre bool) model.Aviso {
	sufijo := ":opened"
	if cierre {
		sufijo = ":closed"
	}
	abierto := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	inc := model.Incidente{
		ID: id, Sujeto: "service:comm-tool", Tipo: "down",
		Severidad: "critical", AbiertoEn: abierto, Detalle: "HTTP 502",
	}
	if cierre {
		cerrado := abierto.Add(4 * time.Minute)
		inc.CerradoEn = &cerrado
	}
	return model.Aviso{
		DeliveryID: string(rune('0'+id)) + sufijo,
		Incidente:  inc,
		Cierre:     cierre,
	}
}

func TestUsaElPrincipalCuandoAnda(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true}
	respaldo := &canalFalso{nombre: "telegram", configurado: true}
	st := &storeFalso{marcados: map[string]string{}}

	n := notify.NewNotificador(principal, respaldo, st,
		clock.NewFake(time.Date(2026, 8, 9, 11, 1, 0, 0, time.UTC)))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(principal.mandados) != 1 {
		t.Errorf("el principal recibió %d mensajes", len(principal.mandados))
	}
	if len(respaldo.mandados) != 0 {
		t.Errorf("el respaldo se usó sin necesidad")
	}
	if st.marcados["1:opened"] != "commtool" {
		t.Errorf("marcados = %v", st.marcados)
	}
}

// Este es el caso que justifica todo el respaldo: comm-tool caído es el aviso
// más importante que hay, y no puede llegar por comm-tool.
func TestCaeAlRespaldoSiElPrincipalFalla(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true, falla: errors.New("connection refused")}
	respaldo := &canalFalso{nombre: "telegram", configurado: true}
	st := &storeFalso{marcados: map[string]string{}}

	n := notify.NewNotificador(principal, respaldo, st,
		clock.NewFake(time.Date(2026, 8, 9, 11, 1, 0, 0, time.UTC)))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(respaldo.mandados) != 1 {
		t.Fatalf("el respaldo recibió %d mensajes, quería 1", len(respaldo.mandados))
	}
	if st.marcados["1:opened"] != "telegram" {
		t.Errorf("via = %q, quería telegram", st.marcados["1:opened"])
	}
}

// Si fallan los dos NO se marca nada: el aviso queda pendiente y el tick
// siguiente lo reintenta. Marcarlo sería perderlo para siempre.
func TestSiFallanLosDosNoSeMarca(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true, falla: errors.New("x")}
	respaldo := &canalFalso{nombre: "telegram", configurado: true, falla: errors.New("y")}
	st := &storeFalso{marcados: map[string]string{}}

	n := notify.NewNotificador(principal, respaldo, st,
		clock.NewFake(time.Date(2026, 8, 9, 11, 1, 0, 0, time.UTC)))

	if err := n.Avisar(context.Background(), aviso(1, false)); err == nil {
		t.Fatal("quería error cuando fallan los dos canales")
	}
	if len(st.marcados) != 0 {
		t.Errorf("se marcó algo con los dos canales caídos: %v", st.marcados)
	}
}

// Un canal sin credenciales no se intenta: fallaría siempre y llenaría el log.
func TestCanalSinConfigurarSeSaltea(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: false}
	respaldo := &canalFalso{nombre: "telegram", configurado: true}
	st := &storeFalso{marcados: map[string]string{}}

	n := notify.NewNotificador(principal, respaldo, st,
		clock.NewFake(time.Date(2026, 8, 9, 11, 1, 0, 0, time.UTC)))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(principal.mandados) != 0 {
		t.Error("se intentó mandar por un canal sin configurar")
	}
	if st.marcados["1:opened"] != "telegram" {
		t.Errorf("via = %q", st.marcados["1:opened"])
	}
}

// Un aviso viejo no se manda: si el proceso estuvo caído un día, al volver no
// puede vomitar los avisos de ayer. Se marca como vencido para que no se
// reintente para siempre.
func TestAvisoVencidoNoSeMandaPeroSeMarca(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true}
	respaldo := &canalFalso{nombre: "telegram", configurado: true}
	st := &storeFalso{marcados: map[string]string{}}

	// El incidente es de las 11:00 y el reloj marca 6 horas después.
	n := notify.NewNotificador(principal, respaldo, st,
		clock.NewFake(time.Date(2026, 8, 9, 17, 0, 0, 0, time.UTC)))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(principal.mandados) != 0 || len(respaldo.mandados) != 0 {
		t.Error("se mandó un aviso de hace 6 horas")
	}
	if st.marcados["1:opened"] != "vencido" {
		t.Errorf("via = %q, quería vencido", st.marcados["1:opened"])
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/notify/`
Expected: FAIL — `notify.NewNotificador undefined`

- [ ] **Step 3: Implementar**

`internal/notify/notificador.go`:

```go
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
)

// VentanaDeVigencia es cuánto puede tener un aviso antes de darse por vencido.
// Si el proceso estuvo caído medio día, al volver no puede vomitar los avisos
// de la mañana: ya no le sirven a nadie y taparían lo que está pasando ahora.
const VentanaDeVigencia = time.Hour

type Store interface {
	MarcarEnviado(deliveryID string, cuando time.Time, via, errMsg string) error
}

type Notificador struct {
	principal Canal
	respaldo  Canal
	store     Store
	clk       clock.Clock
}

func NewNotificador(principal, respaldo Canal, s Store, clk clock.Clock) *Notificador {
	return &Notificador{principal: principal, respaldo: respaldo, store: s, clk: clk}
}

// Avisar manda el aviso y registra por dónde salió. No marcar nada cuando
// fallan los dos canales es deliberado: así el aviso sigue pendiente y el
// tick siguiente lo reintenta.
func (n *Notificador) Avisar(ctx context.Context, a model.Aviso) error {
	ahora := n.clk.Now()

	if ahora.Sub(momentoDe(a)) > VentanaDeVigencia {
		slog.Warn("aviso vencido, no se manda", "delivery", a.DeliveryID)
		return n.store.MarcarEnviado(a.DeliveryID, ahora, "vencido", "más viejo que la ventana de vigencia")
	}

	texto := Texto(a)
	var fallas []error

	for _, c := range []Canal{n.principal, n.respaldo} {
		if c == nil || !c.Configurado() {
			continue
		}
		if err := n.mandar(ctx, c, a, texto); err != nil {
			slog.Warn("no se pudo avisar", "canal", c.Nombre(), "err", err)
			fallas = append(fallas, fmt.Errorf("%s: %w", c.Nombre(), err))
			continue
		}
		return n.store.MarcarEnviado(a.DeliveryID, ahora, c.Nombre(), "")
	}

	if len(fallas) == 0 {
		// Ningún canal configurado. Se registra para no reintentar para
		// siempre, pero se avisa fuerte en el log.
		slog.Error("no hay ningún canal de avisos configurado", "delivery", a.DeliveryID)
		return n.store.MarcarEnviado(a.DeliveryID, ahora, "sin-canal", "ningún canal configurado")
	}
	return errors.Join(fallas...)
}

// mandar usa MandarCon si el canal lo soporta, para pasarle la clave de
// idempotencia. Es lo que hace que un reintento no duplique del otro lado.
func (n *Notificador) mandar(ctx context.Context, c Canal, a model.Aviso, texto string) error {
	if idem, ok := c.(interface {
		MandarCon(context.Context, string, string) error
	}); ok {
		return idem.MandarCon(ctx, texto, a.DeliveryID)
	}
	return c.Mandar(ctx, texto)
}

func momentoDe(a model.Aviso) time.Time {
	if a.Cierre && a.Incidente.CerradoEn != nil {
		return *a.Incidente.CerradoEn
	}
	return a.Incidente.AbiertoEn
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/notify/... -race -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/notify
git commit -m "feat: notificador con respaldo, vigencia y registro de entrega"
```

---

## Task 6: Redacción de los mensajes

**Files:**
- Create: `internal/notify/texto.go`, `internal/notify/texto_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/notify/texto_test.go`:

```go
package notify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/notify"
)

func TestTextoDeApertura(t *testing.T) {
	got := notify.Texto(aviso(1, false))

	if !strings.Contains(got, "comm-tool") {
		t.Errorf("el mensaje no nombra el servicio: %q", got)
	}
	if !strings.Contains(got, "HTTP 502") {
		t.Errorf("el mensaje no dice la causa: %q", got)
	}
	// El sujeto crudo ('service:comm-tool') es una etiqueta interna:
	// en el mensaje va el nombre a secas.
	if strings.Contains(got, "service:") {
		t.Errorf("el mensaje filtra el prefijo interno del sujeto: %q", got)
	}
}

func TestTextoDeCierreDiceCuantoDuro(t *testing.T) {
	got := notify.Texto(aviso(1, true))

	if !strings.Contains(got, "4m") && !strings.Contains(got, "4 min") {
		t.Errorf("el cierre no dice la duración: %q", got)
	}
	if !strings.Contains(got, "comm-tool") {
		t.Errorf("el cierre no nombra el servicio: %q", got)
	}
}

// Telegram corta a los 4096 caracteres y comm-tool valida ese largo antes de
// mandar: un mensaje largo se convertiría en un 400 en vez de un aviso.
func TestElTextoNuncaPasaElLimiteDeTelegram(t *testing.T) {
	a := aviso(1, false)
	a.Incidente.Detalle = strings.Repeat("x", 9000)

	if n := len(notify.Texto(a)); n > 4096 {
		t.Errorf("el mensaje mide %d caracteres, el límite es 4096", n)
	}
}

func TestNombreDeSujetoSacaElPrefijo(t *testing.T) {
	casos := map[string]string{
		"service:comm-tool":       "comm-tool",
		"container:supabase-db":   "supabase-db",
		"host:disk":               "disco",
		"host:mem":                "memoria",
		"host:load":               "carga",
		"sin-prefijo":             "sin-prefijo",
	}
	for sujeto, quiero := range casos {
		if got := notify.NombreDeSujeto(sujeto); got != quiero {
			t.Errorf("NombreDeSujeto(%q) = %q, quería %q", sujeto, got, quiero)
		}
	}
}

func TestTextoDeResumen(t *testing.T) {
	r := notify.Resumen{
		Uptime:     50 * time.Hour,
		DiscoPct:   15.7,
		MemPct:     28.0,
		Incidentes: 2,
		Servicios:  map[string]bool{"comm-tool": true, "sitio": false},
	}
	got := notify.TextoResumen(r)

	if !strings.Contains(got, "15.7") {
		t.Errorf("el resumen no dice el disco: %q", got)
	}
	if !strings.Contains(got, "sitio") {
		t.Errorf("el resumen no lista los servicios: %q", got)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/notify/ -run Texto`
Expected: FAIL — `notify.Texto undefined`

- [ ] **Step 3: Implementar**

`internal/notify/texto.go`:

```go
package notify

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
)

// LargoMaximo es el límite de Telegram. comm-tool además lo valida antes de
// mandar, así que pasarse convierte un aviso en un 400.
const LargoMaximo = 4096

// NombreDeSujeto traduce la etiqueta interna a algo legible.
func NombreDeSujeto(sujeto string) string {
	prefijo, resto, ok := strings.Cut(sujeto, ":")
	if !ok {
		return sujeto
	}
	if prefijo == "host" {
		switch resto {
		case "disk":
			return "disco"
		case "mem":
			return "memoria"
		case "swap":
			return "swap"
		case "load":
			return "carga"
		}
	}
	return resto
}

func Texto(a model.Aviso) string {
	nombre := NombreDeSujeto(a.Incidente.Sujeto)

	var b strings.Builder
	if a.Cierre {
		fmt.Fprintf(&b, "🟢 %s se recuperó", nombre)
		if a.Incidente.CerradoEn != nil {
			dur := a.Incidente.CerradoEn.Sub(a.Incidente.AbiertoEn).Round(time.Minute)
			fmt.Fprintf(&b, "\nestuvo mal %s", dur)
		}
	} else {
		icono := "🟡"
		if a.Incidente.Severidad == "critical" {
			icono = "🔴"
		}
		fmt.Fprintf(&b, "%s %s", icono, tituloDeApertura(a.Incidente, nombre))
		fmt.Fprintf(&b, "\n%s", a.Incidente.Detalle)
	}
	return acortar(b.String())
}

func tituloDeApertura(i model.Incidente, nombre string) string {
	switch i.Tipo {
	case "down":
		return nombre + " no responde"
	case "unhealthy":
		return nombre + " está unhealthy"
	case "flapping":
		return nombre + " está inestable"
	default:
		return nombre + " fuera de rango"
	}
}

// acortar deja el mensaje dentro del límite. Corta por el final y avisa que
// cortó: un mensaje truncado en silencio es peor que uno que lo dice.
func acortar(s string) string {
	if len(s) <= LargoMaximo {
		return s
	}
	const marca = "\n[…]"
	return s[:LargoMaximo-len(marca)] + marca
}

// Resumen es la foto diaria.
type Resumen struct {
	Uptime     time.Duration
	DiscoPct   float64
	MemPct     float64
	Incidentes int
	Servicios  map[string]bool
}

func TextoResumen(r Resumen) string {
	var b strings.Builder
	b.WriteString("📊 Resumen diario\n")
	fmt.Fprintf(&b, "uptime %s · disco %.1f%% · memoria %.1f%%\n",
		r.Uptime.Round(time.Hour), r.DiscoPct, r.MemPct)

	if r.Incidentes == 0 {
		b.WriteString("sin incidentes en 24 h\n")
	} else {
		fmt.Fprintf(&b, "%d incidentes en 24 h\n", r.Incidentes)
	}

	// Ordenado para que el mensaje sea estable entre días y se pueda comparar
	// de un vistazo.
	nombres := make([]string, 0, len(r.Servicios))
	for n := range r.Servicios {
		nombres = append(nombres, n)
	}
	sort.Strings(nombres)
	for _, n := range nombres {
		icono := "🔴"
		if r.Servicios[n] {
			icono = "🟢"
		}
		fmt.Fprintf(&b, "%s %s\n", icono, n)
	}
	return acortar(b.String())
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/notify/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/notify
git commit -m "feat: redacción de los mensajes de aviso y del resumen diario"
```

---

## Task 7: Incidentes por container

**Files:**
- Modify: `internal/rules/motor.go`, `internal/rules/motor_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/rules/motor_test.go`:

```go
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
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/rules/ -run Container`
Expected: FAIL — `m.EvaluarContainers undefined`

- [ ] **Step 3: Implementar**

Agregar a `internal/rules/motor.go`:

```go
// EvaluarContainers aplica la política por conteo a cada container.
//
// Un container sano es uno corriendo cuyo healthcheck no falla. 'none' cuenta
// como sano: la mayoría no declara healthcheck y no tenerlo no es una falla.
// 'starting' también, porque es transitorio.
func (m *Motor) EvaluarContainers(cs []model.ContainerSample) ([]Cambio, error) {
	abiertos, err := m.abiertosPorSujeto()
	if err != nil {
		return nil, err
	}

	var cambios []Cambio
	for _, c := range cs {
		sujeto := "container:" + c.Name
		inc, estaAbierto := abiertos[sujeto]

		ok, tipo, detalle := saludContainer(c)
		nuevo, tr := m.cfg.Containers.Aplicar(m.conteos[sujeto], ok, estaAbierto)
		m.conteos[sujeto] = nuevo

		cb, err := m.aplicar(tr, sujeto, tipo, "warning", detalle, inc)
		if err != nil {
			return nil, err
		}
		if cb != nil {
			cambios = append(cambios, *cb)
		}
	}
	return cambios, nil
}

func saludContainer(c model.ContainerSample) (ok bool, tipo, detalle string) {
	if c.State != "running" {
		return false, "down", "estado " + c.State
	}
	if c.Health == "unhealthy" {
		return false, "unhealthy", "healthcheck fallando"
	}
	return true, "down", "corriendo"
}
```

⚠️ El `tipo` que se le pasa a `aplicar` en un cierre no importa —`aplicar` usa el incidente que ya estaba— pero en una apertura sí. Por eso `saludContainer` devuelve el tipo correcto en cada rama.

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/rules/ -race -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/rules
git commit -m "feat: incidentes por container, separando caído de unhealthy"
```

---

## Task 8: Amortiguación de rebotes

**Files:**
- Modify: `internal/rules/motor.go`, `internal/rules/motor_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar al `storeFalso` de `motor_test.go` el método nuevo y sus tests:

```go
func (s *storeFalso) CiclosEnVentana(sujeto string, desde time.Time) (int, error) {
	n := 0
	for _, i := range s.historial {
		if i.Sujeto == sujeto && !i.AbiertoEn.Before(desde) {
			n++
		}
	}
	return n, nil
}
```

Y agregar `historial []model.Incidente` al struct, poblado en `AbrirIncidente`:

```go
	s.historial = append(s.historial, i)
```

Tests:

```go
// El escenario que rompe la política "uno al caer, uno al recuperarse":
// un servicio que rebota cinco veces son diez mensajes.
func TestRebotarSeisVecesTerminaEnSilencio(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	caido := []model.ProbeResult{{Servicio: "x", OK: false, Error: "502"}}
	sano := []model.ProbeResult{{Servicio: "x", OK: true, StatusCode: 200}}

	mensajes := 0
	huboFlapping := false

	for ciclo := range 6 {
		for range 3 {
			trs, err := m.EvaluarProbes(caido)
			if err != nil {
				t.Fatalf("ciclo %d: %v", ciclo, err)
			}
			mensajes += len(trs)
			for _, c := range trs {
				if c.Incidente.Tipo == "flapping" {
					huboFlapping = true
				}
			}
			reloj.Advance(time.Minute)
		}
		for range 2 {
			trs, _ := m.EvaluarProbes(sano)
			mensajes += len(trs)
			for _, c := range trs {
				if c.Incidente.Tipo == "flapping" {
					huboFlapping = true
				}
			}
			reloj.Advance(time.Minute)
		}
	}

	if !huboFlapping {
		t.Error("seis ciclos en media hora y nunca emitió el aviso de inestabilidad")
	}
	// Sin amortiguación serían 12. Con ella, unos pocos más el de flapping.
	if mensajes > 9 {
		t.Errorf("se emitieron %d mensajes en 6 rebotes: la amortiguación no está frenando", mensajes)
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
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/rules/ -run Flapping -run Rebotar`
Expected: FAIL — el `storeFalso` no compila sin `CiclosEnVentana` en la interfaz, y no hay amortiguación

- [ ] **Step 3: Implementar**

Sumar el método a la interfaz `Store` de `motor.go`:

```go
	CiclosEnVentana(sujeto string, desde time.Time) (int, error)
```

Sumar la configuración a `Config` y a `Defaults()`:

```go
	// CiclosParaFlapear es cuántas aperturas en VentanaDeFlapeo hacen falta
	// para declarar a un sujeto inestable y callarse.
	CiclosParaFlapear int
	VentanaDeFlapeo   time.Duration
	SilencioPorFlapeo time.Duration
```

```go
		CiclosParaFlapear: 4,
		VentanaDeFlapeo:   time.Hour,
		SilencioPorFlapeo: time.Hour,
```

Sumar el mapa de silenciados al `Motor` y a `NewMotor`:

```go
	silenciados map[string]time.Time
```

```go
		silenciados: map[string]time.Time{},
```

Y reescribir `aplicar` para que consulte y actualice el silencio:

```go
// aplicar persiste la transición. Devuelve nil si no hubo ninguna, o si el
// sujeto está silenciado por rebotar demasiado.
//
// Importante: el incidente se abre y se cierra en la base IGUAL cuando el
// sujeto está silenciado. El estado tiene que seguir siendo verdadero; lo que
// se suprime es el mensaje, no el registro.
func (m *Motor) aplicar(tr Transicion, sujeto, tipo, severidad, detalle string, abierto model.Incidente) (*Cambio, error) {
	ahora := m.clk.Now()
	silenciado := ahora.Before(m.silenciados[sujeto])

	switch tr {
	case Abre:
		i := model.Incidente{
			Sujeto: sujeto, Tipo: tipo, Severidad: severidad,
			AbiertoEn: ahora, Detalle: detalle,
		}
		id, err := m.store.AbrirIncidente(i)
		if err != nil {
			return nil, err
		}
		i.ID = id
		if silenciado {
			return nil, nil
		}
		return &Cambio{Tipo: Abre, Incidente: i}, nil

	case Cierra:
		if err := m.store.CerrarIncidente(abierto.ID, ahora); err != nil {
			return nil, err
		}
		abierto.CerradoEn = &ahora

		// Recién al cerrar se puede saber si el sujeto está rebotando: es el
		// momento en que se completó un ciclo.
		if !silenciado {
			flap, err := m.chequearFlapeo(sujeto, ahora)
			if err != nil {
				return nil, err
			}
			if flap != nil {
				return flap, nil
			}
			return &Cambio{Tipo: Cierra, Incidente: abierto}, nil
		}
		return nil, nil
	}
	return nil, nil
}

// chequearFlapeo devuelve un aviso de inestabilidad —y silencia al sujeto— si
// abrió demasiadas veces en la ventana. Devuelve nil si está todo normal.
func (m *Motor) chequearFlapeo(sujeto string, ahora time.Time) (*Cambio, error) {
	n, err := m.store.CiclosEnVentana(sujeto, ahora.Add(-m.cfg.VentanaDeFlapeo))
	if err != nil {
		return nil, err
	}
	if n < m.cfg.CiclosParaFlapear {
		return nil, nil
	}

	m.silenciados[sujeto] = ahora.Add(m.cfg.SilencioPorFlapeo)

	i := model.Incidente{
		Sujeto: sujeto, Tipo: "flapping", Severidad: "warning",
		AbiertoEn: ahora,
		Detalle: fmt.Sprintf("%d caídas en %s — silencio por %s",
			n, m.cfg.VentanaDeFlapeo, m.cfg.SilencioPorFlapeo),
	}
	// No se persiste como incidente: el sujeto ya tiene su historial de
	// incidentes reales, y abrir otro chocaría con el índice único.
	// El aviso se identifica por el momento en que se emitió.
	i.ID = ahora.Unix()
	return &Cambio{Tipo: Abre, Incidente: i}, nil
}
```

⚠️ Ese `i.ID = ahora.Unix()` hace que el `delivery_id` del aviso de flapeo sea `<unix>:opened`, que no colisiona con ningún id de incidente real porque los ids reales son autoincrementales chicos. Es feo pero explícito; la alternativa —persistir un incidente de flapeo— choca con `incidentes_abierto_unico`.

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/rules/ -race -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/rules
git commit -m "feat: amortiguacion de rebotes, el freno de mano de la politica anti-ruido"
```

---

## Task 9: Resumen diario

**Files:**
- Create: `cmd/server-status/resumen.go`, `cmd/server-status/resumen_test.go`
- Modify: `internal/store/store.go`

- [ ] **Step 1: Escribir el test que falla**

`cmd/server-status/resumen_test.go`:

```go
package main

import (
	"testing"
	"time"
)

func TestTocaResumenSoloUnaVezPorDia(t *testing.T) {
	loc := time.UTC
	var r recordatorioDiario

	ayer := time.Date(2026, 8, 8, 8, 0, 0, 0, loc)
	hoyTemprano := time.Date(2026, 8, 9, 7, 59, 0, 0, loc)
	hoyEnHora := time.Date(2026, 8, 9, 8, 0, 30, 0, loc)
	hoyMasTarde := time.Date(2026, 8, 9, 9, 0, 0, 0, loc)

	if !r.toca(ayer, 8) {
		t.Fatal("no disparó la primera vez")
	}
	if r.toca(hoyTemprano, 8) {
		t.Error("disparó antes de la hora")
	}
	if !r.toca(hoyEnHora, 8) {
		t.Error("no disparó en la hora del día siguiente")
	}
	// Ya salió hoy: no puede volver a salir.
	if r.toca(hoyMasTarde, 8) {
		t.Error("disparó dos veces el mismo día")
	}
}

// Si el proceso estuvo caído durante la hora del resumen, al volver más tarde
// tiene que mandarlo igual: es la señal de que el circuito está vivo.
func TestSiSePierdeLaHoraLoMandaIgualEseDia(t *testing.T) {
	var r recordatorioDiario
	r.toca(time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC), 8)

	tarde := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	if !r.toca(tarde, 8) {
		t.Error("no mandó el resumen del día por haberse perdido la hora exacta")
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./cmd/server-status/ -run Resumen`
Expected: FAIL — `undefined: recordatorioDiario`

- [ ] **Step 3: Implementar**

`cmd/server-status/resumen.go`:

```go
package main

import (
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/notify"
)

// recordatorioDiario dispara una vez por día, a partir de cierta hora.
//
// "A partir de" y no "a las": si el proceso estuvo caído a las 8, el resumen
// tiene que salir igual cuando vuelva. Su valor no es la puntualidad, es
// confirmar que el circuito de avisos sigue vivo.
type recordatorioDiario struct {
	ultimo time.Time
}

func (r *recordatorioDiario) toca(ahora time.Time, hora int) bool {
	if ahora.Hour() < hora {
		return false
	}
	if mismoDia(r.ultimo, ahora) {
		return false
	}
	r.ultimo = ahora
	return true
}

func mismoDia(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func armarResumen(h model.HostSample, probes []model.ProbeResult, incidentes int) notify.Resumen {
	servicios := make(map[string]bool, len(probes))
	for _, p := range probes {
		servicios[p.Servicio] = p.OK
	}
	return notify.Resumen{
		Uptime:     h.Uptime,
		DiscoPct:   porcentaje(h.DiskUsedBytes, h.DiskTotalBytes),
		MemPct:     porcentaje(h.MemUsedBytes, h.MemTotalBytes),
		Incidentes: incidentes,
		Servicios:  servicios,
	}
}

func porcentaje(usado, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(usado) * 100 / float64(total)
}
```

- [ ] **Step 4: Sumar la consulta de incidentes del día**

Agregar a `internal/store/store.go`:

```go
// IncidentesDesde cuenta los incidentes abiertos a partir de un momento.
func (s *Store) IncidentesDesde(desde time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM incidents WHERE opened_at >= ?`, desde.Unix()).Scan(&n)
	return n, err
}
```

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./cmd/server-status/ ./internal/store/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd internal/store
git commit -m "feat: resumen diario, que ademas confirma que el circuito de avisos vive"
```

---

## Task 10: Wiring, secretos y arranque sin credenciales

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `cmd/server-status/main.go`, `deploy/config.example.yaml`, `deploy/env.example`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/config/config_test.go`:

```go
func TestDefaultsDeAvisos(t *testing.T) {
	c, err := config.Load(escribir(t, "base: /tmp/x.db\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.TelegramAPI != "https://api.telegram.org" {
		t.Errorf("TelegramAPI = %q", c.TelegramAPI)
	}
	if c.HoraResumen != 8 {
		t.Errorf("HoraResumen = %d, quería 8", c.HoraResumen)
	}
	if c.Zona != "America/Argentina/Buenos_Aires" {
		t.Errorf("Zona = %q", c.Zona)
	}
}

// La zona horaria se valida al cargar: una zona inválida haría que el resumen
// salga a una hora al azar, y eso se descubriría recién al día siguiente.
func TestZonaInvalidaFalla(t *testing.T) {
	_, err := config.Load(escribir(t, "base: /tmp/x.db\nzona: No/Existe\n"))
	if err == nil {
		t.Fatal("quería error con una zona inválida, no hubo")
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/config/ -run Avisos`
Expected: FAIL — campos inexistentes

- [ ] **Step 3: Implementar la config**

Sumar al struct `Config`:

```go
	TelegramAPI string `yaml:"telegram_api"`
	CommToolURL string `yaml:"comm_tool_url"`
	CommToolUserID string `yaml:"comm_tool_user_id"`
	HoraResumen int    `yaml:"hora_resumen"`
	Zona        string `yaml:"zona"`
```

Y en `Load`, antes de la validación de `Base`:

```go
	if c.TelegramAPI == "" {
		c.TelegramAPI = "https://api.telegram.org"
	}
	if c.HoraResumen == 0 {
		c.HoraResumen = 8
	}
	if c.Zona == "" {
		c.Zona = "America/Argentina/Buenos_Aires"
	}
	if _, err := time.LoadLocation(c.Zona); err != nil {
		return Config{}, fmt.Errorf("zona horaria %q inválida: %w", c.Zona, err)
	}
```

- [ ] **Step 4: Armar los canales en `main.go`**

En `correr`, después de abrir el store:

```go
	loc, err := time.LoadLocation(cfg.Zona)
	if err != nil {
		return err
	}

	// Los secretos vienen del entorno, nunca de la config ni de la base.
	// Invariante 8 del spec.
	canalCT := commtool.New(cfg.CommToolURL, os.Getenv("COMM_TOOL_API_KEY"), cfg.CommToolUserID)
	canalTG := telegram.New(cfg.TelegramAPI, os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID"))
	notificador := notify.NewNotificador(canalCT, canalTG, s, clock.Real{})

	// Arrancar sin canales es válido a propósito: un monitor que no levanta
	// porque no puede avisar es peor que uno que avisa a medias. Pero tiene
	// que gritarlo.
	if !canalCT.Configurado() && !canalTG.Configurado() {
		slog.Warn("NINGÚN canal de avisos configurado: los incidentes solo van al log")
	}
```

- [ ] **Step 5: Mandar los pendientes en cada tick**

Al final del bloque de persistencia, reemplazando el `for` que solo logueaba:

```go
			// Los cambios ya quedaron en la base; los avisos se derivan de ahí.
			// Loguearlos igual sirve para diagnosticar sin abrir la base.
			for _, c := range append(cambios, deHost...) {
				slog.Info("incidente", "transicion", c.Tipo.String(),
					"sujeto", c.Incidente.Sujeto, "detalle", c.Incidente.Detalle)
			}

			pendientes, err := s.AvisosPendientes()
			if err != nil {
				slog.Error("no se pudieron leer los avisos pendientes", "err", err)
			}
			for _, a := range pendientes {
				if err := notificador.Avisar(ctx, a); err != nil {
					// No se marca nada: el tick siguiente lo reintenta.
					slog.Error("no se pudo entregar el aviso", "delivery", a.DeliveryID, "err", err)
				}
			}

			if recordatorio.toca(clock.Real{}.Now().In(loc), cfg.HoraResumen) {
				if err := mandarResumen(ctx, s, notificador, m, resultados); err != nil {
					slog.Error("no se pudo mandar el resumen diario", "err", err)
				}
			}
```

Declarar `var recordatorio recordatorioDiario` antes del `for`, y sumar la función:

```go
func mandarResumen(ctx context.Context, s *store.Store, n *notify.Notificador,
	h model.HostSample, probes []model.ProbeResult) error {

	ahora := clock.Real{}.Now()
	cuantos, err := s.IncidentesDesde(ahora.Add(-24 * time.Hour))
	if err != nil {
		return err
	}
	texto := notify.TextoResumen(armarResumen(h, probes, cuantos))

	// El resumen no es un incidente: no tiene fila en incidents y su id de
	// entrega es la fecha, que lo hace único por día.
	id := "resumen:" + ahora.Format("2006-01-02")
	return n.AvisarTexto(ctx, id, texto)
}
```

Y sumar `AvisarTexto` al notificador, que es `Avisar` sin la parte de vigencia:

```go
// AvisarTexto manda un mensaje suelto, sin incidente detrás. Lo usa el resumen
// diario, cuyo id de entrega es la fecha.
func (n *Notificador) AvisarTexto(ctx context.Context, deliveryID, texto string) error {
	ahora := n.clk.Now()
	var fallas []error

	for _, c := range []Canal{n.principal, n.respaldo} {
		if c == nil || !c.Configurado() {
			continue
		}
		if err := n.mandarTexto(ctx, c, deliveryID, texto); err != nil {
			fallas = append(fallas, fmt.Errorf("%s: %w", c.Nombre(), err))
			continue
		}
		return n.store.MarcarEnviado(deliveryID, ahora, c.Nombre(), "")
	}
	if len(fallas) == 0 {
		return n.store.MarcarEnviado(deliveryID, ahora, "sin-canal", "ningún canal configurado")
	}
	return errors.Join(fallas...)
}

func (n *Notificador) mandarTexto(ctx context.Context, c Canal, deliveryID, texto string) error {
	if idem, ok := c.(interface {
		MandarCon(context.Context, string, string) error
	}); ok {
		return idem.MandarCon(ctx, texto, deliveryID)
	}
	return c.Mandar(ctx, texto)
}
```

Refactorizar `Avisar` para que use `AvisarTexto` después de resolver vigencia y texto.

- [ ] **Step 6: Sumar los containers al motor en el loop**

Justo después de `motor.EvaluarProbes`:

```go
			deContainers, err := motor.EvaluarContainers(ms)
			if err != nil {
				slog.Error("falló el motor de reglas sobre los containers", "err", err)
			}
```

Y sumar `deContainers` al `append` de los cambios logueados.

- [ ] **Step 7: Crear el ejemplo de entorno**

`deploy/env.example`:

```sh
# Copiar a /etc/server-status/env con modo 0600 y dueño server-status.
# ESTE ARCHIVO NO VA AL REPO CON VALORES REALES.

# API key de la app 'server-status' en comm-tool.
COMM_TOOL_API_KEY=

# Token del bot, de BotFather. Se usa para el camino de respaldo directo.
TELEGRAM_BOT_TOKEN=

# Chat id propio. Lo dice @userinfobot con un /start.
TELEGRAM_CHAT_ID=
```

Y sumar a `deploy/config.example.yaml`:

```yaml
comm_tool_url: https://comm.jadd.com.ar
# uuid fijo que identifica al destinatario en comm-tool. comm-tool nunca lo
# interpreta: es una invariante suya.
comm_tool_user_id: PONER-UN-UUID-GENERADO-UNA-VEZ

hora_resumen: 8
zona: America/Argentina/Buenos_Aires
```

- [ ] **Step 8: Verificar todo**

```bash
go vet ./...
go test ./... -race
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/server-status
```

Expected: todo en verde.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "feat: avisos en el loop, secretos por entorno y arranque sin credenciales"
```

---

## Task 11: Alta del bot y verificación real

**Files:** ninguno — es configuración en Telegram, comm-tool y el VPS.

⚠️ **Los pasos 1 a 3 los tiene que hacer el usuario**: requieren su cuenta de Telegram.

- [ ] **Step 1: Crear el bot** *(usuario)*

Hablarle a `@BotFather`, `/newbot`, elegir nombre y usuario. Guardar el token.

- [ ] **Step 2: Sacar el chat id** *(usuario)*

`/start` a `@userinfobot`. Anotar el número.

- [ ] **Step 3: Dar de alta la app en comm-tool** *(usuario)*

Generar el uuid del destinatario y la API key, y registrar:

```bash
cd ~/Projects/communication-tool
UUID="$(uuidgen | tr 'A-Z' 'a-z')" && echo "comm_tool_user_id: $UUID"
KEY="$(openssl rand -hex 24)" && echo "COMM_TOOL_API_KEY=$KEY"
bun run scripts/registrar-app.ts \
  --slug server-status --name "server-status" --api-key "$KEY" \
  --delivery-url https://ejemplo.invalid/no-usado \
  --delivery-secret-env DELIVERY_SECRET_STATUS \
  --bot-slug status --token-env TELEGRAM_TOKEN_STATUS
```

El `--delivery-url` no se usa hasta la fase 9 (comandos entrantes), pero el
script lo pide. Cargar `TELEGRAM_TOKEN_STATUS` en el entorno de comm-tool en el
VPS y reiniciar su container.

Después, registrar el webhook del bot nuevo y vincular el chat mandándole
`/vincular <código>` desde Telegram.

- [ ] **Step 4: Cargar los secretos en el VPS**

```bash
ssh vps 'sudo install -m 0600 -o server-status -g server-status /dev/null /etc/server-status/env'
```

Y escribir las tres variables adentro con `sudo -e`.

- [ ] **Step 5: Poner el uuid en la config**

Reemplazar `comm_tool_user_id` en `/etc/server-status/config.yaml` por el uuid
generado, y desplegar.

- [ ] **Step 6: Verificar el circuito completo**

```bash
make deploy
```

Provocar un incidente sin tocar producción, con el mismo truco del plan 2:
agregar un servicio con `estado_esperado: 500` contra una URL que devuelve 200,
esperar cuatro minutos y confirmar que **llega el mensaje al Telegram**.
Después revertirlo a 200 y confirmar que llega el de recuperación.

```bash
ssh vps 'sudo -u server-status sqlite3 -header -column /var/lib/server-status/status.db \
  "SELECT delivery_id, datetime(sent_at,\"unixepoch\"), via, error FROM notifications ORDER BY sent_at DESC LIMIT 5;"'
```

Expected: las filas con `via = commtool`.

- [ ] **Step 7: Verificar el respaldo**

Parar comm-tool un momento y provocar otro incidente: el aviso tiene que llegar
igual, con `via = telegram`.

```bash
ssh vps 'cd /opt/stacks/comm-tool && docker compose stop app'
# provocar el incidente, esperar, confirmar el mensaje
ssh vps 'cd /opt/stacks/comm-tool && docker compose start app'
```

⚠️ Esto corta comm-tool, que está en el camino crítico de GymTracker. Hacerlo
corto y confirmar que volvió.

---

## Autorevisión del plan

**Cobertura del spec (fase 4):**

| Requisito | Tarea |
|---|---|
| comm-tool como camino principal (§7) | 4, 10 |
| Fallback directo con el mismo bot (§7) | 3, 5 |
| `delivery_id` determinístico (§5, invariante 3) | 1, 4 |
| No marcar cuando fallan los dos (§7) | 5 |
| Un aviso al abrir, uno al cerrar, silencio en el medio (§6) | ya estaba en el plan 2 |
| Amortiguación de rebotes (§6, regla 2) | 8 |
| Incidentes por container (§5) | 7 |
| Resumen diario a las 08:00 ART (§7) | 9, 10 |
| Secretos por entorno, nunca en la base (§12, invariante 8) | 10 |
| Alta en comm-tool con uuid fijo (§7) | 11 |

**Fuera de este plan:** el watchdog externo (fase 6), el panel (fase 5), la portada (fase 7), los logs (fase 8) y los comandos entrantes (fase 9). También siguen pendientes la retención y el `VACUUM INTO`.

**Correcciones aplicadas en la revisión:**

1. Task 8: el aviso de flapeo **no** se persiste como incidente — chocaría con `incidentes_abierto_unico`, que impide dos abiertos del mismo sujeto. Se emite como `Cambio` con un id derivado del timestamp.
2. Task 10: `Avisar` y `AvisarTexto` compartían código; se factorizó para que la lógica de canales viva en un solo lugar.
3. Task 5: la interfaz `Store` del notificador es solo `MarcarEnviado`. `AvisosPendientes` la llama el loop, no el notificador — si no, el notificador tendría que conocer la cola y dejaría de ser testeable con un doble de tres líneas.

**Consistencia de tipos:** `notify.Canal` lo implementan `commtool.Canal` y `telegram.Canal`, ambos con `Nombre`/`Configurado`/`Mandar`. `commtool.Canal` además tiene `MandarCon`, que el notificador detecta con un type assertion. `model.Aviso` se crea en `store.AvisosPendientes` (task 1) y lo consumen `notify.Texto` (task 6) y `Notificador.Avisar` (task 5), con los mismos campos.
