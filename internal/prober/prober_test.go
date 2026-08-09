package prober_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/prober"
)

func TestProbeOKConDoscientos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	got := p.Probe(context.Background(), "x", srv.URL)

	if !got.OK {
		t.Errorf("OK = false, quería true. Error: %q", got.Error)
	}
	if got.StatusCode != 200 {
		t.Errorf("StatusCode = %d", got.StatusCode)
	}
	if got.Servicio != "x" {
		t.Errorf("Servicio = %q", got.Servicio)
	}
}

// Un 3xx significa que el servicio está vivo y contestando. Tratarlo como
// caída daría falsos positivos en cualquier sitio que redirija.
func TestProbeAceptaRedirecciones(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://example.com/otro")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	if got := p.Probe(context.Background(), "x", srv.URL); !got.OK {
		t.Errorf("un 301 se tomó como caída: %+v", got)
	}
}

func TestProbeFallaConQuinientos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	got := p.Probe(context.Background(), "x", srv.URL)

	if got.OK {
		t.Error("OK = true con un 500")
	}
	if got.StatusCode != 500 {
		t.Errorf("StatusCode = %d, quería 500", got.StatusCode)
	}
	if got.Error == "" {
		t.Error("Error vacío: hay que poder saber qué pasó")
	}
}

func TestProbeFallaSiNoHayNadieEscuchando(t *testing.T) {
	p := prober.New(clock.NewFake(time.Now()), 2*time.Second)
	// Puerto cerrado del loopback: falla al conectar, sin respuesta HTTP.
	got := p.Probe(context.Background(), "x", "http://127.0.0.1:1/")

	if got.OK {
		t.Error("OK = true contra un puerto cerrado")
	}
	if got.StatusCode != 0 {
		t.Errorf("StatusCode = %d, quería 0 cuando no hubo respuesta", got.StatusCode)
	}
	if got.Error == "" {
		t.Error("Error vacío")
	}
}

// El TS sale del reloj inyectado, no de time.Now(): invariante 5 del spec.
func TestProbeUsaElRelojInyectado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	momento := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)
	p := prober.New(clock.NewFake(momento), 5*time.Second)

	if got := p.Probe(context.Background(), "x", srv.URL); !got.TS.Equal(momento) {
		t.Errorf("TS = %v, quería %v", got.TS, momento)
	}
}

// Un servicio que no responde no puede colgar el ciclo entero: el timeout
// tiene que cortar y devolver una falla.
func TestProbeCortaPorTimeout(t *testing.T) {
	bloqueado := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-bloqueado
	}))
	defer func() { close(bloqueado); srv.Close() }()

	p := prober.New(clock.NewFake(time.Now()), 100*time.Millisecond)
	got := p.Probe(context.Background(), "x", srv.URL)

	if got.OK {
		t.Error("OK = true contra un servidor que nunca responde")
	}
	if got.Error == "" {
		t.Error("Error vacío en un timeout")
	}
}
