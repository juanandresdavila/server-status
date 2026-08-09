// Package prober pincha las URLs públicas de los servicios.
package prober

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
)

type Prober struct {
	clk  clock.Clock
	http *http.Client
}

func New(clk clock.Clock, timeout time.Duration) *Prober {
	return &Prober{
		clk: clk,
		http: &http.Client{
			Timeout: timeout,
			// No seguir redirecciones: un 301 ya prueba que el servicio está
			// vivo, y seguirlo puede terminar pegándole a un tercero.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Probe hace un GET y clasifica el resultado. Nunca devuelve error: una falla
// del probe ES el dato.
func (p *Prober) Probe(ctx context.Context, servicio, url string) model.ProbeResult {
	r := model.ProbeResult{TS: p.clk.Now(), Servicio: servicio}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.Error = fmt.Sprintf("url inválida: %v", err)
		return r
	}
	req.Header.Set("User-Agent", "server-status")

	// La latencia se mide con time.Since y no con el reloj inyectado a
	// propósito: es una duración real, no una marca de tiempo lógica.
	inicio := time.Now()
	resp, err := p.http.Do(req)
	r.Latencia = time.Since(inicio)

	if err != nil {
		// Sin respuesta: DNS, TCP, TLS o timeout.
		r.Error = err.Error()
		return r
	}
	defer resp.Body.Close()

	r.StatusCode = resp.StatusCode
	// 2xx y 3xx cuentan como vivo.
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		r.OK = true
		return r
	}
	r.Error = fmt.Sprintf("HTTP %s", resp.Status)
	return r
}
