// Package prober pincha las URLs públicas de los servicios.
package prober

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
)

// Prober reusa las conexiones de un tick al siguiente, con un pool por
// servicio. Reusar es lo normal y lo barato (~50 ms contra ~140 ms de una
// conexión nueva, medido en el VPS), pero tiene un modo de falla que el
// servicio no tiene: la conexión se queda muda y todo lo que viaje por ella
// espera hasta el timeout, con el servicio sano del otro lado. Pasó el
// 01/09/2026 con workshop y el 18/09/2026 con comm-tool, y en los dos casos
// abrió un incidente crítico. Por eso una falla sobre una conexión reusada se
// confirma por una nueva antes de contarla (ver Probe).
type Prober struct {
	clk     clock.Clock
	timeout time.Duration
	// molde es el Transport del que sale el pool de cada servicio. Nunca se
	// usa directo: se clona.
	molde *http.Transport

	mu    sync.Mutex
	pools map[string]*http.Transport
}

func New(clk clock.Clock, timeout time.Duration) *Prober {
	return NewConTransporte(clk, timeout, http.DefaultTransport.(*http.Transport).Clone())
}

// NewConTransporte existe para los tests: un servidor HTTP/2 de prueba
// necesita que el cliente confíe en su certificado.
func NewConTransporte(clk clock.Clock, timeout time.Duration, molde *http.Transport) *Prober {
	return &Prober{
		clk:     clk,
		timeout: timeout,
		molde:   molde,
		pools:   make(map[string]*http.Transport),
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
//
// Si el pedido falla sin respuesta HTTP y había salido por una conexión
// reusada, se tira el pool de ese servicio y se reintenta UNA vez por una
// conexión nueva; lo que diga ese segundo intento es el resultado. Solo en ese
// caso: una falla sobre una conexión que ya era nueva no se reintenta (sería
// un reintento a ciegas, y duplicaría el tiempo del probe justo con el
// servicio caído), y una respuesta HTTP, aunque sea un 500, es el servicio
// contestando. En el peor caso un probe tarda dos veces el timeout.
func (p *Prober) Probe(ctx context.Context, o Objetivo) model.ProbeResult {
	r := model.ProbeResult{TS: p.clk.Now(), Servicio: o.Servicio}

	tr := p.pool(o.Servicio)
	resp, reusada, latencia, err := p.intento(ctx, tr, o)
	if err != nil && reusada && ctx.Err() == nil {
		slog.Warn("probe: falló sobre una conexión reusada, reintento por una nueva",
			"servicio", o.Servicio, "err", err)
		tr = p.renovar(o.Servicio, tr)
		resp, _, latencia, err = p.intento(ctx, tr, o)
	}
	r.Latencia = latencia

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

// intento hace un GET por el Transport dado y dice si salió por una conexión
// reusada. La latencia se mide con time.Since y no con el reloj inyectado a
// propósito: es una duración real, no una marca de tiempo lógica.
func (p *Prober) intento(ctx context.Context, tr *http.Transport, o Objetivo) (
	resp *http.Response, reusada bool, latencia time.Duration, err error) {

	var fueReusada atomic.Bool
	traza := &httptrace.ClientTrace{
		GotConn: func(i httptrace.GotConnInfo) { fueReusada.Store(i.Reused) },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, traza),
		http.MethodGet, o.URL, nil)
	if err != nil {
		return nil, false, 0, fmt.Errorf("url inválida: %w", err)
	}
	req.Header.Set("User-Agent", "server-status")
	if o.APIKey != "" {
		req.Header.Set("apikey", o.APIKey)
	}

	cli := &http.Client{
		Timeout:   p.timeout,
		Transport: tr,
		// No seguir redirecciones: un 301 ya prueba que el servicio está
		// vivo, y seguirlo puede terminar pegándole a un tercero.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	inicio := time.Now()
	resp, err = cli.Do(req)
	return resp, fueReusada.Load(), time.Since(inicio), err
}

// pool devuelve el Transport del servicio, y lo crea la primera vez. Un pool
// por servicio y no uno compartido: la conexión trabada de uno no tiene por
// qué arrastrar a los demás, y tirarla no le cierra nada a nadie más.
func (p *Prober) pool(servicio string) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	tr, ok := p.pools[servicio]
	if !ok {
		tr = p.molde.Clone()
		p.pools[servicio] = tr
	}
	return tr
}

// renovar cambia el pool del servicio por uno vacío, así el reintento y los
// ticks siguientes salen por una conexión nueva. Una conexión colgada con un
// pedido en vuelo no se puede cerrar desde afuera: CloseIdleConnections cierra
// lo que pueda y la colgada queda huérfana hasta que TCP la dé por muerta.
func (p *Prober) renovar(servicio string, viejo *http.Transport) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if actual := p.pools[servicio]; actual != viejo {
		// Otro probe del mismo servicio ya lo renovó.
		return actual
	}
	nuevo := p.molde.Clone()
	p.pools[servicio] = nuevo
	viejo.CloseIdleConnections()
	return nuevo
}
