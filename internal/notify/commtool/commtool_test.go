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

// El error tampoco puede filtrar la API key.
func TestElErrorNoFiltraLaApiKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"unauthorized"}`))
	}))
	defer srv.Close()

	c := commtool.New(srv.URL, "CLAVESECRETA", "u")
	err := c.MandarCon(context.Background(), "hola", "1:opened")
	if err == nil {
		t.Fatal("quería error con un 401")
	}
	if strings.Contains(err.Error(), "CLAVESECRETA") {
		t.Errorf("el error filtra la API key: %v", err)
	}
}

func TestSinCredencialesNoEstaConfigurado(t *testing.T) {
	if commtool.New("https://x", "", "u").Configurado() {
		t.Error("sin API key dice estar configurado")
	}
	if commtool.New("https://x", "K", "").Configurado() {
		t.Error("sin userId dice estar configurado")
	}
	if !commtool.New("https://x", "K", "u").Configurado() {
		t.Error("con las dos cosas dice NO estar configurado")
	}
}
