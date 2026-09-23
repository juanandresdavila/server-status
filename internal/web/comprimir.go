package web

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// Comprimir manda la respuesta en gzip a quien lo acepte.
//
// El panel se mira por el tailnet desde Argentina contra un VPS en Canadá, con
// 180 ms de RTT, y sus páginas son texto muy repetido: la vista de logs de 7
// días son 4,4 MB de HTML que comprimen a 250 KB, y echarts.min.js pasa de
// 1 MB a 330 KB. Medido el 22/09/2026, bajar el HTML de /logs sin comprimir se
// llevaba 1,1 s de los 4,4 que tardaba la página. Comprimir 4,4 MB le cuesta
// 35 ms de CPU al VPS.
//
// Envuelve al panel y NO al tail: el tail es SSE, necesita Flush línea por
// línea, y un gzip en el medio las retendría en su buffer.
func Comprimir(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		// Un rango son bytes del original: comprimirlos daría un pedazo que
		// no es de nada. HEAD no lleva cuerpo, y su Content-Length tiene que
		// seguir diciendo el del GET.
		if !aceptaGzip(r.Header.Get("Accept-Encoding")) || r.Header.Get("Range") != "" || r.Method == http.MethodHead {
			h.ServeHTTP(w, r)
			return
		}
		c := &comprimido{ResponseWriter: w}
		defer c.cerrar()
		h.ServeHTTP(c, r)
	})
}

// aceptaGzip lee el Accept-Encoding. Un "gzip;q=0" es un no explícito.
func aceptaGzip(cabecera string) bool {
	for _, parte := range strings.Split(cabecera, ",") {
		codif, params, _ := strings.Cut(parte, ";")
		if !strings.EqualFold(strings.TrimSpace(codif), "gzip") {
			continue
		}
		_, q, hayQ := strings.Cut(strings.ReplaceAll(params, " ", ""), "q=")
		if !hayQ {
			return true
		}
		v, err := strconv.ParseFloat(q, 64)
		return err == nil && v > 0
	}
	return false
}

// comprimido decide si comprime recién al escribir la cabecera, porque hasta
// ahí no se sabe el status: un 304 o un 204 no llevan cuerpo, y un gzip vacío
// igual son ~20 bytes de cabecera y cola.
type comprimido struct {
	http.ResponseWriter
	gz       *gzip.Writer
	decidido bool
}

func (c *comprimido) WriteHeader(code int) {
	if c.decidido {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	c.decidido = true
	h := c.Header()
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified || h.Get("Content-Encoding") != "" {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	// El Content-Length que haya puesto el handler es el del cuerpo sin
	// comprimir: mandarlo cortaría la respuesta o dejaría al navegador
	// esperando bytes que no vienen.
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	c.gz = gzip.NewWriter(c.ResponseWriter)
	c.ResponseWriter.WriteHeader(code)
}

func (c *comprimido) Write(b []byte) (int, error) {
	if !c.decidido {
		// net/http adivina el tipo mirando los primeros bytes. Tiene que ser
		// sobre el cuerpo SIN comprimir: sobre el gzip adivinaría un binario y
		// el navegador bajaría la página como archivo.
		if c.Header().Get("Content-Type") == "" {
			c.Header().Set("Content-Type", http.DetectContentType(b))
		}
		c.WriteHeader(http.StatusOK)
	}
	if c.gz == nil {
		return c.ResponseWriter.Write(b)
	}
	return c.gz.Write(b)
}

func (c *comprimido) cerrar() {
	if c.gz != nil {
		c.gz.Close()
	}
}

// Unwrap deja que http.ResponseController llegue al writer de abajo.
func (c *comprimido) Unwrap() http.ResponseWriter { return c.ResponseWriter }
