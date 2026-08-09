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
//
// `esperado` en 0 significa "cualquier 2xx o 3xx". Con un código explícito, ese
// y solo ese cuenta como sano. Existe porque hay servicios sin ningún endpoint
// que devuelva 2xx sin autenticación: los Supabase del VPS responden 400 en
// /auth/v1/authorize, y ese 400 prueba que el request atravesó el gateway y
// llegó a GoTrue — bastante más de lo que prueba un 401, que el gateway puede
// emitir solo.
func (p *Prober) Probe(ctx context.Context, servicio, url string, esperado int) model.ProbeResult {
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

	if esperado != 0 {
		if resp.StatusCode == esperado {
			r.OK = true
			return r
		}
		r.Error = fmt.Sprintf("HTTP %s (esperaba %d)", resp.Status, esperado)
		return r
	}

	// 2xx y 3xx cuentan como vivo.
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		r.OK = true
		return r
	}
	r.Error = fmt.Sprintf("HTTP %s", resp.Status)
	return r
}
