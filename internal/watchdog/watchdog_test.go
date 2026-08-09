package watchdog_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/watchdog"
)

const ahoraISO = "2026-08-09T12:00:00Z"

func momento(t *testing.T) time.Time {
	t.Helper()
	x, err := time.Parse(time.RFC3339, ahoraISO)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// paginaCon devuelve un servidor que sirve una portada con la marca de
// frescura del momento indicado.
func paginaCon(generado time.Time) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<html><!--generado:%d--><body>ok</body></html>", generado.Unix())
	}))
}

// El agujero que este diseño cierra: Healthchecks funciona al revés —vos latís
// y si dejás de latir avisa—, así que si el túnel de Cloudflare se cae pero el
// proceso sigue vivo, el latido saldría igual y nadie se entera. Por eso el
// latido depende de poder alcanzarse a sí mismo por la URL pública.
func TestNoLateSiNoSeAlcanzaASiMismo(t *testing.T) {
	var lati bool
	hc := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { lati = true }))
	defer hc.Close()

	// Puerto cerrado del loopback: la vuelta completa falla.
	w := watchdog.New("http://127.0.0.1:1/", hc.URL, clock.NewFake(momento(t)), 3*time.Minute)
	if err := w.Latir(context.Background()); err == nil {
		t.Error("no devolvió error con la URL pública caída")
	}
	if lati {
		t.Error("le latió a Healthchecks sin poder alcanzarse a sí mismo")
	}
}

// Y el segundo agujero, más sutil: un 200 no alcanza. Caddy sirve feliz un
// archivo viejo si el proceso dejó de escribirlo, así que la página puede
// responder perfecto y estar congelada hace horas.
func TestNoLateSiLaPaginaEstaVieja(t *testing.T) {
	ahora := momento(t)
	pub := paginaCon(ahora.Add(-30 * time.Minute))
	defer pub.Close()

	var lati bool
	hc := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { lati = true }))
	defer hc.Close()

	w := watchdog.New(pub.URL, hc.URL, clock.NewFake(ahora), 3*time.Minute)
	if err := w.Latir(context.Background()); err == nil {
		t.Error("no devolvió error con la página de hace media hora")
	}
	if lati {
		t.Error("latió con una página vieja")
	}
}

func TestLateCuandoTodoEstaBien(t *testing.T) {
	ahora := momento(t)
	pub := paginaCon(ahora.Add(-30 * time.Second))
	defer pub.Close()

	var lati bool
	hc := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { lati = true }))
	defer hc.Close()

	w := watchdog.New(pub.URL, hc.URL, clock.NewFake(ahora), 3*time.Minute)
	if err := w.Latir(context.Background()); err != nil {
		t.Fatalf("Latir: %v", err)
	}
	if !lati {
		t.Error("no latió con todo en orden")
	}
}

// Una página que responde 200 pero sin la marca no se puede evaluar: hay que
// tratarla como falla, no como éxito.
func TestSinMarcaDeFrescuraEsFalla(t *testing.T) {
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>hola</html>"))
	}))
	defer pub.Close()

	var lati bool
	hc := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { lati = true }))
	defer hc.Close()

	w := watchdog.New(pub.URL, hc.URL, clock.NewFake(momento(t)), 3*time.Minute)
	if err := w.Latir(context.Background()); err == nil {
		t.Error("una página sin marca de frescura se tomó como sana")
	}
	if lati {
		t.Error("latió con una página sin marca")
	}
}

// Sin URL de Healthchecks el watchdog no hace nada y no molesta: mismo patrón
// que los canales de aviso sin credenciales.
func TestSinURLDeHealthchecksNoHaceNada(t *testing.T) {
	w := watchdog.New("http://127.0.0.1:1/", "", clock.NewFake(momento(t)), 3*time.Minute)
	if w.Configurado() {
		t.Error("sin URL de ping dice estar configurado")
	}
	if err := w.Latir(context.Background()); err != nil {
		t.Errorf("sin configurar devolvió error: %v", err)
	}
}
