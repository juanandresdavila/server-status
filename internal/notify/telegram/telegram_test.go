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

	c := telegram.New(srv.URL, "TOKENSECRETO", "1")
	err := c.Mandar(context.Background(), "hola")
	if err == nil {
		t.Fatal("quería error, no hubo")
	}
	// La descripción de Telegram tiene que llegar al log: sin ella,
	// diagnosticar por qué no llega un aviso es adivinar.
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("el error no incluye la descripción: %v", err)
	}
	// Y el token NUNCA puede estar en el error: los errores van al journal,
	// que se lee, se pega en un chat y se manda por mail.
	if strings.Contains(err.Error(), "TOKENSECRETO") {
		t.Errorf("el error filtra el token: %v", err)
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
