package prober_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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
	got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL})

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
	if got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL}); !got.OK {
		t.Errorf("un 301 se tomó como caída: %+v", got)
	}
}

func TestProbeFallaConQuinientos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL})

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
	got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: "http://127.0.0.1:1/"})

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

	if got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL}); !got.TS.Equal(momento) {
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
	got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL})

	if got.OK {
		t.Error("OK = true contra un servidor que nunca responde")
	}
	if got.Error == "" {
		t.Error("Error vacío en un timeout")
	}
}

// Hay servicios cuyo endpoint sano no devuelve 2xx: el único que contesta sin
// autenticación devuelve un 4xx, y ese código concreto ES la señal de que está
// vivo. Declararlo explícito evita tener que elegir entre no monitorearlo o
// monitorearlo con una falla permanente.
func TestProbeConEstadoEsperadoExplicito(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)

	if got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL, Esperado: 400}); !got.OK {
		t.Errorf("un 400 esperado se tomó como caída: %+v", got)
	}
	// Y el mismo 400 sin declararlo sigue siendo una falla.
	if got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL}); got.OK {
		t.Error("un 400 no declarado se tomó como sano")
	}
}

// Con un estado esperado declarado, cualquier OTRO código es falla —
// incluido un 200. Si el servicio empieza a devolver 200 donde antes daba 400,
// algo cambió y hay que mirarlo.
func TestEstadoEsperadoRechazaCualquierOtroCodigo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	if got := p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL, Esperado: 400}); got.OK {
		t.Errorf("esperaba 400 y llegó 200, pero lo dio por sano: %+v", got)
	}
}

// El header `apikey` existe por los Supabase: su gateway rechaza con 401 todo
// lo que no lo traiga, y sin él el healthcheck de GoTrue es inalcanzable.
func TestProbeMandaLaAPIKeyCuandoEstaCargada(t *testing.T) {
	var recibida string
	var hubo bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recibida, hubo = r.Header.Get("apikey"), r.Header.Values("apikey") != nil
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL, APIKey: "sarasa"})

	if recibida != "sarasa" {
		t.Errorf("header apikey = %q, quería \"sarasa\"", recibida)
	}

	// Y sin APIKey el header no va: mandarlo vacío no es lo mismo que no
	// mandarlo, y algún gateway distingue.
	recibida, hubo = "", false
	p.Probe(context.Background(), prober.Objetivo{Servicio: "x", URL: srv.URL})
	if hubo {
		t.Errorf("mandó el header apikey sin tener una: %q", recibida)
	}
}

// --- Reintento por conexión nueva ---
//
// El 18/09/2026 el probe de comm-tool dio timeout cuatro minutos seguidos y
// abrió un incidente crítico con el servicio sano: el pedido nunca llegó a
// Caddy, mientras otra sonda del mismo VPS le pegaba al mismo host sin
// problema. Lo que se había trabado era la conexión HTTP/2 que el prober
// reusaba de un tick al otro. Estos tests montan eso: un servidor HTTP/2 con
// TLS, como el borde de Cloudflare, donde los pedidos que entran por una
// conexión marcada se cuelgan y los de una conexión nueva contestan.

type claveConexion struct{}

type servidorColgable struct {
	srv        *httptest.Server
	conexiones atomic.Int64
	// colgarDesde: los pedidos de las conexiones con número >= a este se
	// cuelgan. 0 = no se cuelga ninguna.
	colgarDesde atomic.Int64
	// colgarHasta: tope de ese rango. 0 = sin tope.
	colgarHasta atomic.Int64
	estado      atomic.Int64
	proto       atomic.Value
	fin         chan struct{}
}

func nuevoServidorColgable(t *testing.T) *servidorColgable {
	t.Helper()
	sc := &servidorColgable{fin: make(chan struct{})}
	sc.estado.Store(http.StatusOK)
	sc.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc.proto.Store(r.Proto)
		n := r.Context().Value(claveConexion{}).(int64)
		desde, hasta := sc.colgarDesde.Load(), sc.colgarHasta.Load()
		if desde != 0 && n >= desde && (hasta == 0 || n <= hasta) {
			select {
			case <-r.Context().Done():
			case <-sc.fin:
			}
			return
		}
		w.WriteHeader(int(sc.estado.Load()))
	}))
	sc.srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, claveConexion{}, sc.conexiones.Add(1))
	}
	sc.srv.EnableHTTP2 = true
	sc.srv.StartTLS()
	t.Cleanup(func() { close(sc.fin); sc.srv.Close() })
	return sc
}

// prober arma uno que confía en el certificado de prueba y habla HTTP/2.
func (sc *servidorColgable) prober(timeout time.Duration) *prober.Prober {
	pool := x509.NewCertPool()
	pool.AddCert(sc.srv.Certificate())
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	return prober.NewConTransporte(clock.NewFake(time.Now()), timeout, tr)
}

func (sc *servidorColgable) objetivo() prober.Objetivo {
	return prober.Objetivo{Servicio: "comm-tool", URL: sc.srv.URL + "/health"}
}

// Lo que pasó en producción: la conexión que venía andando se queda muda.
// Una conexión nueva contesta, así que el servicio está vivo y el probe tiene
// que decir OK.
func TestUnaConexionReusadaColgadaSeReintentaPorUnaNueva(t *testing.T) {
	sc := nuevoServidorColgable(t)
	p := sc.prober(500 * time.Millisecond)

	if got := p.Probe(context.Background(), sc.objetivo()); !got.OK {
		t.Fatalf("el primer probe falló contra un servidor sano: %+v", got)
	}
	if proto, _ := sc.proto.Load().(string); proto != "HTTP/2.0" {
		t.Fatalf("el test habló %q: tiene que ser HTTP/2, como Cloudflare", proto)
	}

	sc.colgarDesde.Store(1)
	sc.colgarHasta.Store(1)
	got := p.Probe(context.Background(), sc.objetivo())

	if !got.OK {
		t.Errorf("la conexión reusada se colgó y el probe la contó como caída: %+v", got)
	}
	if n := sc.conexiones.Load(); n != 2 {
		t.Errorf("el servidor vio %d conexiones, quería 2 (la colgada y la nueva)", n)
	}
}

// Después del reintento, el tick siguiente no puede volver a la conexión
// colgada: si volviera, cada minuto pagaría un timeout entero antes de
// reintentar.
func TestDespuesDelReintentoNoSeVuelveALaConexionColgada(t *testing.T) {
	sc := nuevoServidorColgable(t)
	p := sc.prober(500 * time.Millisecond)

	p.Probe(context.Background(), sc.objetivo())
	sc.colgarDesde.Store(1)
	sc.colgarHasta.Store(1)
	p.Probe(context.Background(), sc.objetivo())

	got := p.Probe(context.Background(), sc.objetivo())
	if !got.OK {
		t.Fatalf("el tercer probe falló: %+v", got)
	}
	if n := sc.conexiones.Load(); n != 2 {
		t.Errorf("el servidor vio %d conexiones, quería 2: el tercer probe volvió a la conexión colgada", n)
	}
}

// Si la conexión nueva tampoco contesta, el servicio no contesta: es falla.
func TestSiLaConexionNuevaTambienFallaEsFalla(t *testing.T) {
	sc := nuevoServidorColgable(t)
	p := sc.prober(500 * time.Millisecond)

	p.Probe(context.Background(), sc.objetivo())
	sc.colgarDesde.Store(1)
	got := p.Probe(context.Background(), sc.objetivo())

	if got.OK {
		t.Error("OK = true con la conexión reusada Y la nueva colgadas")
	}
	if got.Error == "" {
		t.Error("Error vacío: hay que poder saber qué pasó")
	}
	if n := sc.conexiones.Load(); n != 2 {
		t.Errorf("el servidor vio %d conexiones, quería 2: un reintento, no más", n)
	}
}

// Si la conexión ya era nueva, reintentar no discrimina nada: sería un
// reintento a ciegas que tapa fallas reales y duplica el tiempo del probe
// justo cuando el servicio está caído.
func TestUnaFallaSobreConexionNuevaNoSeReintenta(t *testing.T) {
	sc := nuevoServidorColgable(t)
	sc.colgarDesde.Store(1)
	p := sc.prober(500 * time.Millisecond)

	if got := p.Probe(context.Background(), sc.objetivo()); got.OK {
		t.Error("OK = true contra un servidor que nunca contesta")
	}
	if n := sc.conexiones.Load(); n != 1 {
		t.Errorf("el servidor vio %d conexiones, quería 1: se reintentó una falla que no era de reuso", n)
	}
}

// Un 500 es el servicio contestando: la conexión anda. Reintentarlo por otra
// no cambia nada y escondería el error si el segundo pedido saliera bien.
func TestUnaRespuestaHTTPSobreConexionReusadaNoSeReintenta(t *testing.T) {
	sc := nuevoServidorColgable(t)
	p := sc.prober(500 * time.Millisecond)

	p.Probe(context.Background(), sc.objetivo())
	sc.estado.Store(http.StatusInternalServerError)
	got := p.Probe(context.Background(), sc.objetivo())

	if got.OK || got.StatusCode != 500 {
		t.Errorf("quería la falla con 500, llegó %+v", got)
	}
	if n := sc.conexiones.Load(); n != 1 {
		t.Errorf("el servidor vio %d conexiones, quería 1: se reintentó una respuesta HTTP", n)
	}
}

// En producción los probes corren en paralelo, uno por servicio, y todos
// comparten el mapa de pools. Este test solo tiene dientes con -race, que es
// como corre `make test`.
func TestProbesEnParaleloSobreElMismoProber(t *testing.T) {
	sc := nuevoServidorColgable(t)
	p := sc.prober(2 * time.Second)

	var wg sync.WaitGroup
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		wg.Add(1)
		go func(servicio string) {
			defer wg.Done()
			o := sc.objetivo()
			o.Servicio = servicio
			if got := p.Probe(context.Background(), o); !got.OK {
				t.Errorf("%s: %+v", servicio, got)
			}
		}(s)
	}
	wg.Wait()
}

// El pool es por servicio: renovar el de uno no le puede cerrar las conexiones
// a los otros. Si todos compartieran pool, la conexión trabada de un servicio
// mandaría a los demás a rehacer handshake cada vez que se renueva.
func TestRenovarUnServicioNoLeTocaElPoolDeOtro(t *testing.T) {
	sc := nuevoServidorColgable(t)
	p := sc.prober(500 * time.Millisecond)

	a, b := sc.objetivo(), sc.objetivo()
	a.Servicio, b.Servicio = "a", "b"

	p.Probe(context.Background(), a) // conexión 1
	p.Probe(context.Background(), b) // conexión 2
	if n := sc.conexiones.Load(); n != 2 {
		t.Fatalf("el servidor vio %d conexiones, quería 2: los dos servicios comparten pool", n)
	}

	// Se cuelga la de "a": su probe reintenta por la conexión 3.
	sc.colgarDesde.Store(1)
	sc.colgarHasta.Store(1)
	if got := p.Probe(context.Background(), a); !got.OK {
		t.Fatalf("a: %+v", got)
	}

	// Y "b" tiene que seguir usando la suya, sin abrir ninguna.
	if got := p.Probe(context.Background(), b); !got.OK {
		t.Fatalf("b: %+v", got)
	}
	if n := sc.conexiones.Load(); n != 3 {
		t.Errorf("el servidor vio %d conexiones, quería 3: renovar el pool de \"a\" le cerró la conexión a \"b\"", n)
	}
}

// Al apagar, el ctx cancelado corta antes de reintentar: no se gasta una
// conexión nueva para volver a fallar por lo mismo, y sobre todo no se tira el
// pool del servicio. Se mide por lo segundo: si el pool se hubiera renovado, el
// probe siguiente tendría que abrir una conexión.
func TestConElContextoCanceladoNoSeReintenta(t *testing.T) {
	sc := nuevoServidorColgable(t)
	p := sc.prober(5 * time.Second)

	if got := p.Probe(context.Background(), sc.objetivo()); !got.OK {
		t.Fatalf("el primer probe falló: %+v", got)
	}

	sc.colgarDesde.Store(1)
	ctx, cancelar := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancelar() }()
	if got := p.Probe(ctx, sc.objetivo()); got.OK {
		t.Fatal("OK = true con el contexto cancelado")
	}

	sc.colgarDesde.Store(0)
	if got := p.Probe(context.Background(), sc.objetivo()); !got.OK {
		t.Fatalf("el probe de después falló: %+v", got)
	}
	if n := sc.conexiones.Load(); n != 1 {
		t.Errorf("el servidor vio %d conexiones, quería 1: se renovó el pool con el contexto ya cancelado", n)
	}
}
