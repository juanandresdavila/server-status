package web_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/web"
)

type tailFalso struct {
	lineas  chan model.LineaLog
	cerrado chan struct{}
}

func (t *tailFalso) Seguir(ctx context.Context, container string, out chan<- model.LineaLog) error {
	defer close(t.cerrado)
	for {
		select {
		case <-ctx.Done():
			// Que el stream de Docker se cierre cuando el cliente se va es
			// lo que evita una conexión colgada por cada pestaña abierta.
			return ctx.Err()
		case l := <-t.lineas:
			out <- l
		}
	}
}

func TestTailEmiteEventosSSE(t *testing.T) {
	falso := &tailFalso{lineas: make(chan model.LineaLog, 4), cerrado: make(chan struct{})}
	falso.lineas <- model.LineaLog{
		TS:        time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Container: "comm-tool", Stream: "stdout", Linea: "hola mundo",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/tail?container=comm-tool", nil).WithContext(ctx)
	web.NuevoTail(falso).ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, quería text/event-stream", ct)
	}
	cuerpo := rec.Body.String()
	if !strings.Contains(cuerpo, "data: ") {
		t.Errorf("no emitió eventos SSE: %q", cuerpo)
	}
	if !strings.Contains(cuerpo, "hola mundo") {
		t.Errorf("no emitió la línea: %q", cuerpo)
	}
}

// Cuando el cliente se va, el stream de Docker tiene que cerrarse. Sin esto
// queda una conexión colgada por cada pestaña que alguien abrió y cerró.
func TestTailCierraElStreamCuandoSeVaElCliente(t *testing.T) {
	falso := &tailFalso{lineas: make(chan model.LineaLog), cerrado: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/tail?container=x", nil).WithContext(ctx)
	web.NuevoTail(falso).ServeHTTP(rec, req)

	select {
	case <-falso.cerrado:
	case <-time.After(2 * time.Second):
		t.Fatal("el stream de Docker no se cerró al irse el cliente")
	}
}

// Sin container no hay nada que seguir: 400, no un stream vacío eterno.
func TestTailSinContainerEsError(t *testing.T) {
	falso := &tailFalso{lineas: make(chan model.LineaLog), cerrado: make(chan struct{})}
	rec := httptest.NewRecorder()
	web.NuevoTail(falso).ServeHTTP(rec, httptest.NewRequest("GET", "/api/tail", nil))

	if rec.Code != 400 {
		t.Errorf("código = %d, quería 400", rec.Code)
	}
}
