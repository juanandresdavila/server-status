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

// Objetivo es lo que hay que pinchar. Va como struct y no como una lista de
// parámetros sueltos porque ya son cuatro y dos son strings: en el orden
// posicional, confundir URL con APIKey compila igual.
type Objetivo struct {
	Servicio string
	URL      string
	// Esperado en 0 significa "cualquier 2xx o 3xx". Con un código explícito,
	// ese y solo ese cuenta como sano.
	Esperado int
	// APIKey, si no está vacía, viaja en el header `apikey`. Existe por los
	// Supabase: su gateway rechaza con 401 todo lo que no la traiga, así que
	// sin esto el único endpoint alcanzable sería uno que NO es un healthcheck.
	// El valor sale del entorno, nunca de la config — invariante 8 del spec.
	APIKey string
}

// Probe hace un GET y clasifica el resultado. Nunca devuelve error: una falla
// del probe ES el dato.
func (p *Prober) Probe(ctx context.Context, o Objetivo) model.ProbeResult {
	r := model.ProbeResult{TS: p.clk.Now(), Servicio: o.Servicio}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		r.Error = fmt.Sprintf("url inválida: %v", err)
		return r
	}
	req.Header.Set("User-Agent", "server-status")
	if o.APIKey != "" {
		req.Header.Set("apikey", o.APIKey)
	}

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

	if o.Esperado != 0 {
		if resp.StatusCode == o.Esperado {
			r.OK = true
			return r
		}
		r.Error = fmt.Sprintf("HTTP %s (esperaba %d)", resp.Status, o.Esperado)
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
