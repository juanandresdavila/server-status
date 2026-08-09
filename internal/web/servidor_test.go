package web_test

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/web"
)

// El panel escucha en la IP de tailnet, que al arrancar la máquina puede no
// existir todavía: tailscaled tarda en asignarla. Es la misma carrera que en
// el VPS se resolvió con FreeBind para Cockpit; acá se resuelve reintentando,
// que es portable y no necesita build tags.
//
// Lo que este test blinda es que el reintento TERMINE: uno infinito dejaría
// una goroutine viva para siempre contra una config equivocada.
func TestEscucharReintentaPeroSeRinde(t *testing.T) {
	// 203.0.113.1 es de la red de documentación (RFC 5737): nunca va a existir.
	hecho := make(chan error, 1)
	go func() { hecho <- web.Escuchar("203.0.113.1:8090", nil, 300*time.Millisecond) }()

	select {
	case err := <-hecho:
		if err == nil {
			t.Fatal("Escuchar devolvió nil contra una IP inexistente")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Escuchar no volvió: se quedó reintentando para siempre")
	}
}

func TestEscucharSirveCuandoLaDireccionExiste(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dir := ln.Addr().String()
	ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	go web.Escuchar(dir, mux, 5*time.Second)

	for range 50 {
		c, err := net.DialTimeout("tcp", dir, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nunca escuchó en %s", dir)
}
