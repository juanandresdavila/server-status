package web_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/juanandresdavila/server-status/internal/web"
)

var reEcharts = regexp.MustCompile(`src="(/assets/echarts\.min\.js\?v=[0-9a-f]+)"`)

// echarts.min.js es 1 MB y se servía sin ETag, sin Last-Modified y sin
// Cache-Control: el navegador no tenía cómo revalidarlo y lo bajaba entero en
// cada visita a /. Medido el 22/09/2026 en el navegador: la segunda visita
// volvió a transferir 1 034 402 bytes y tardó 1,08 s en eso.
//
// La página lo pide con la versión del contenido en la URL, y esa URL se
// cachea para siempre: un deploy que cambie el archivo cambia la URL.
func TestElPanelPideEchartsVersionadoYSeCacheaParaSiempre(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba, relojDePrueba)

	pagina := pedirCon(t, h, "GET", "/", nil)
	m := reEcharts.FindStringSubmatch(pagina.Body.String())
	if m == nil {
		t.Fatalf("la página no pide echarts con versión en la URL")
	}

	rec := pedirCon(t, h, "GET", m[1], nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: código %d", m[1], rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("Cache-Control = %q, quería inmutable por un año", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Errorf("sin ETag")
	}
}

// Sin versión, o con una que no es la de este binario —una pestaña abierta
// desde antes del deploy—, se revalida en cada uso en vez de fijarse para
// siempre: si no, una URL vieja quedaría pegada a un contenido nuevo.
func TestUnAssetSinLaVersionActualSeRevalida(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba, relojDePrueba)

	for _, ruta := range []string{"/assets/echarts.min.js", "/assets/echarts.min.js?v=0000"} {
		rec := pedirCon(t, h, "GET", ruta, nil)
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, quería no-cache", ruta, cc)
		}
		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: sin ETag no hay cómo revalidar", ruta)
		}
		// La revalidación tiene que dar 304, sin volver a mandar el MB.
		rev := pedirCon(t, h, "GET", ruta, map[string]string{"If-None-Match": etag, "Accept-Encoding": "gzip"})
		if rev.Code != http.StatusNotModified {
			t.Errorf("%s con If-None-Match: código %d, quería 304", ruta, rev.Code)
		}
		if rev.Body.Len() != 0 {
			t.Errorf("%s: el 304 trajo %d bytes", ruta, rev.Body.Len())
		}
	}
}
