package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/juanandresdavila/server-status/internal/model"
)

// Seguidor es la fuente del tail en vivo. Declarada acá para que el handler
// se pueda testear sin Docker.
type Seguidor interface {
	// Seguir manda líneas a out hasta que el contexto se cancele.
	Seguir(ctx context.Context, container string, out chan<- model.LineaLog) error
}

// NuevoTail sirve el tail en vivo por SSE.
//
// El stream contra Docker se abre SOLO mientras hay alguien mirando y se cierra
// al irse: si no, quedaría una conexión colgada por cada pestaña que alguien
// abrió y cerró. Por eso la ingesta normal no usa follow y esto sí.
func NuevoTail(s Seguidor) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		container := r.URL.Query().Get("container")
		if container == "" {
			http.Error(w, "falta el parámetro container", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flush := func() {}
		if f, ok := w.(http.Flusher); ok {
			flush = f.Flush
		}

		// El contexto del request se cancela cuando el cliente cierra la
		// pestaña: eso es lo que corta el stream de Docker río arriba.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		lineas := make(chan model.LineaLog, 64)
		errores := make(chan error, 1)
		go func() { errores <- s.Seguir(ctx, container, lineas) }()

		for {
			select {
			case <-ctx.Done():
				return
			case <-errores:
				return
			case l := <-lineas:
				// Una línea de log puede traer saltos: SSE los usa como
				// separador de campos, así que cada uno va en su propio "data:".
				fmt.Fprintf(w, "event: linea\ndata: %s [%s] %s\n\n",
					l.TS.Format("15:04:05"), l.Stream, sinSaltos(l.Linea))
				flush()
			}
		}
	})
}

// sinSaltos aplasta los saltos de línea: en SSE un "\n" corta el campo y una
// línea de log con saltos rompería el evento en pedazos.
func sinSaltos(s string) string {
	out := make([]rune, 0, len(s))
	for _, c := range s {
		if c == '\n' || c == '\r' {
			c = ' '
		}
		out = append(out, c)
	}
	return string(out)
}
