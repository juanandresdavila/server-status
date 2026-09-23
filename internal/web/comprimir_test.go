package web_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juanandresdavila/server-status/internal/web"
)

func pedirCon(t *testing.T, h http.Handler, metodo, ruta string, cabeceras map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(metodo, ruta, nil)
	for k, v := range cabeceras {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func descomprimir(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("el cuerpo no es gzip: %v", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip roto: %v", err)
	}
	return string(b)
}

var html = strings.Repeat("<div class=linea>una línea de log bastante repetida</div>\n", 200)

func TestComprimirMandaGzipAQuienLoAcepta(t *testing.T) {
	h := web.Comprimir(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Lo que hace http.FileServer: un Content-Length del cuerpo SIN
		// comprimir, que mentiría sobre el comprimido.
		w.Header().Set("Content-Length", "11400")
		io.WriteString(w, html)
	}))
	rec := pedirCon(t, h, "GET", "/logs", map[string]string{"Accept-Encoding": "gzip, deflate, br, zstd"})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, quería gzip", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("quedó el Content-Length del cuerpo sin comprimir: %s", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, quería Accept-Encoding", got)
	}
	if rec.Body.Len() >= len(html) {
		t.Errorf("el cuerpo mide %d, no achicó los %d originales", rec.Body.Len(), len(html))
	}
	if got := descomprimir(t, rec); got != html {
		t.Errorf("descomprimido no es el original")
	}
}

func TestComprimirNoTocaAQuienNoLoPide(t *testing.T) {
	h := web.Comprimir(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, html)
	}))
	rec := pedirCon(t, h, "GET", "/logs", nil)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q sin que lo pidieran", got)
	}
	if rec.Body.String() != html {
		t.Errorf("el cuerpo cambió")
	}
	// Vary va igual: una caché intermedia tiene que saber que hay dos
	// versiones de la misma URL.
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, quería Accept-Encoding", got)
	}
}

// Un gzip con q=0 es un "no" explícito.
func TestComprimirRespetaElQCero(t *testing.T) {
	h := web.Comprimir(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, html)
	}))
	rec := pedirCon(t, h, "GET", "/", map[string]string{"Accept-Encoding": "gzip;q=0, identity"})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q con gzip;q=0", got)
	}
}

// Si el handler no declara Content-Type, net/http lo adivina mirando los
// primeros bytes. Tiene que mirarlos ANTES de comprimir: sobre el gzip
// adivinaría un binario y el navegador bajaría la página como archivo.
func TestComprimirAdivinaElTipoSobreElCuerpoSinComprimir(t *testing.T) {
	h := web.Comprimir(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<!DOCTYPE html><html><body>hola</body></html>")
	}))
	rec := pedirCon(t, h, "GET", "/", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, quería text/html", got)
	}
}

// Un 304 no lleva cuerpo, y un gzip vacío igual son ~20 bytes de cabecera y
// cola: no se le puede poner Content-Encoding ni escribirle nada.
func TestUn304SaleSinCuerpoNiContentEncoding(t *testing.T) {
	h := web.Comprimir(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	rec := pedirCon(t, h, "GET", "/assets/echarts.min.js", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("código = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q en un 304", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("el 304 salió con %d bytes de cuerpo", rec.Body.Len())
	}
}

// Un pedido de rango se sirve sin comprimir: los bytes del rango son del
// archivo original, y comprimirlos daría un pedazo que no es de nada.
func TestUnRangoNoSeComprime(t *testing.T) {
	h := web.Comprimir(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, html)
	}))
	rec := pedirCon(t, h, "GET", "/assets/echarts.min.js", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-99"})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q en un pedido de rango", got)
	}
}

// El panel entero sale comprimido: la vista de logs de 7 días son 4,4 MB de
// HTML que comprimen a 250 KB, y con 180 ms de RTT hasta el VPS la diferencia
// es más de un segundo por página.
func TestElPanelSaleComprimido(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba, relojDePrueba)
	for _, ruta := range []string{"/", "/logs", "/events", "/assets/echarts.min.js"} {
		rec := pedirCon(t, h, "GET", ruta, map[string]string{"Accept-Encoding": "gzip"})
		if rec.Code != http.StatusOK {
			t.Errorf("%s: código %d", ruta, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Errorf("%s: Content-Encoding = %q, quería gzip", ruta, got)
			continue
		}
		descomprimir(t, rec)
	}
}
