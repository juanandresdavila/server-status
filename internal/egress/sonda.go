package egress

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
)

// Brazo es una condición del experimento. Cada uno mueve UNA sola variable
// respecto de `v6-ka`, que es la réplica de producción.
type Brazo struct {
	Nombre   string
	Red      string // "tcp4" o "tcp6"
	Reusa    bool   // false fuerza conexión nueva en cada pedido
	Cadencia time.Duration
}

// Destino es una URL pública a pinchar. Nunca localhost: invariante 6 del spec
// —un probe interno diría "todo verde" el día que se rompa el túnel—, y acá
// además el camino ES el objeto de estudio.
type Destino struct {
	Nombre string
	URL    string
}

// BrazosPorDefecto es el factorial 2×2 más el brazo de cadencia que desempata
// H2. El orden importa solo para la salida.
func BrazosPorDefecto() []Brazo {
	return []Brazo{
		{Nombre: "v6-ka", Red: "tcp6", Reusa: true, Cadencia: time.Minute},
		{Nombre: "v4-ka", Red: "tcp4", Reusa: true, Cadencia: time.Minute},
		{Nombre: "v6-fresh", Red: "tcp6", Reusa: false, Cadencia: time.Minute},
		{Nombre: "v4-fresh", Red: "tcp4", Reusa: false, Cadencia: time.Minute},
		{Nombre: "v6-ka-30s", Red: "tcp6", Reusa: true, Cadencia: 30 * time.Second},
	}
}

// DestinosPorDefecto son los cuatro que pasan por Cloudflare y vuelven por el
// túnel al mismo VPS, más uno FUERA de Cloudflare que separa "tramo hasta
// Cloudflare" de "egress del VPS en general".
//
// El de gym-tracker va sin `apikey`: contesta 401 y recorre la red igual, así
// que el experimento no necesita tocar ningún secreto.
func DestinosPorDefecto() []Destino {
	return []Destino{
		{Nombre: "jadd.com.ar", URL: "https://jadd.com.ar/"},
		{Nombre: "comm-tool", URL: "https://comm.jadd.com.ar/health"},
		{Nombre: "workshop", URL: "https://workshop.jadd.com.ar/"},
		{Nombre: "gym-tracker", URL: "https://supabase-gym.jadd.com.ar/auth/v1/health"},
		{Nombre: "externo-google", URL: "https://www.google.com/generate_204"},
	}
}

// Registro es una línea del JSONL: un intento.
type Registro struct {
	TS      string `json:"ts"`
	Brazo   string `json:"brazo"`
	Red     string `json:"red"`
	Destino string `json:"destino"`
	Clase   Clase  `json:"clase"`

	Status int    `json:"status,omitempty"`
	Proto  string `json:"proto,omitempty"`

	// Reusado y OcioMs son los que deciden H2: si los que fallan traen ~60 s de
	// ocio y los que andan no, está contestado.
	Reusado bool  `json:"reusado"`
	Ocioso  bool  `json:"ocioso"`
	OcioMs  int64 `json:"ocio_ms"`

	DNSMs      float64 `json:"dns_ms,omitempty"`
	ConexionMs float64 `json:"conexion_ms,omitempty"`
	TLSMs      float64 `json:"tls_ms,omitempty"`
	TotalMs    float64 `json:"total_ms"`

	// Remota es la IP del destino: pública, y es la que hay que poder comparar
	// entre fallas. Del lado local va solo el puerto — la IP del VPS no se
	// escribe en ningún lado.
	Remota      string `json:"remota,omitempty"`
	PuertoLocal string `json:"puerto_local,omitempty"`

	Error string `json:"error,omitempty"`
}

// FamiliaReal dice por qué familia salió el intento DE VERDAD, leída de la IP
// remota. Es el auto-chequeo del experimento: si un brazo `tcp6` registra
// intentos por v4, el brazo no está midiendo lo que dice su nombre.
func (r Registro) FamiliaReal() string {
	if r.Remota == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(r.Remota)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return ""
	case ip.To4() != nil:
		return "v4"
	default:
		return "v6"
	}
}

// Sonda mide un brazo. Cada brazo tiene la SUYA con su propio cliente: si
// compartieran cliente compartirían el pool de conexiones y los brazos se
// contaminarían entre sí, que es justo la variable del experimento.
type Sonda struct {
	clk   clock.Clock
	brazo Brazo
	cli   *http.Client
}

// NuevaSonda arma el cliente del brazo. El transporte replica al
// http.DefaultTransport que usa internal/prober —mismos timeouts, mismo
// ForceAttemptHTTP2— y cambia solo la familia de direcciones.
func NuevaSonda(clk clock.Clock, b Brazo, timeout time.Duration) *Sonda {
	d := &net.Dialer{
		Timeout: 30 * time.Second,
		// El keepalive de TCP también va igual que en producción: son los 30 s
		// que podrían estar refrescando el estado de un middlebox, así que
		// cambiarlos acá invalidaría la comparación.
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		// La red que pasa el transporte ("tcp") se descarta a propósito: es
		// el único punto donde el experimento fuerza la familia.
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, b.Red, addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Sonda{
		clk:   clk,
		brazo: b,
		cli: &http.Client{
			Timeout:   timeout,
			Transport: tr,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (s *Sonda) Brazo() Brazo { return s.brazo }

// Medir hace un intento. Nunca devuelve error: la falla ES el dato.
func (s *Sonda) Medir(ctx context.Context, d Destino) Registro {
	r := Registro{
		TS:      s.clk.Now().UTC().Format(time.RFC3339Nano),
		Brazo:   s.brazo.Nombre,
		Red:     s.brazo.Red,
		Destino: d.Nombre,
	}

	if !s.brazo.Reusa {
		// Cerrar el pool es lo que hace "nueva" a la conexión. No se usa
		// Transport.DisableKeepAlives porque en Go eso además apaga HTTP/2:
		// movería dos variables y el brazo dejaría de tener un solo eje.
		s.cli.CloseIdleConnections()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		r.Clase = ClaseOtro
		r.Error = Redactar(err.Error())
		return r
	}
	req.Header.Set("User-Agent", "egress-probe")

	t := &traza{}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), t.hooks()))

	// Igual que en internal/prober: la latencia se mide con time.Since y no con
	// el reloj inyectado porque es una duración real, no una marca lógica.
	// Excepción explícita de la invariante 5.
	t.inicio = time.Now()
	resp, err := s.cli.Do(req)
	total := time.Since(t.inicio)

	r.TotalMs = enMs(total)
	r.Reusado, r.Ocioso = t.reusado, t.ocioso
	r.OcioMs = t.ocio.Milliseconds()
	r.Remota, r.PuertoLocal = t.remota, t.puertoLocal
	r.DNSMs, r.ConexionMs, r.TLSMs = enMs(t.dns), enMs(t.conexion), enMs(t.tls)

	if err != nil {
		r.Clase = Clasificar(err)
		r.Error = Redactar(err.Error())
		return r
	}
	// Igual que producción: se cierra sin drenar el cuerpo. Drenarlo cambiaría
	// la política de reuso sobre HTTP/1.1 y el brazo dejaría de replicar al
	// prober.
	defer resp.Body.Close()

	r.Clase = ClaseOK
	r.Status = resp.StatusCode
	r.Proto = resp.Proto
	return r
}

func enMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

type traza struct {
	inicio      time.Time
	dns         time.Duration
	conexion    time.Duration
	tls         time.Duration
	reusado     bool
	ocioso      bool
	ocio        time.Duration
	remota      string
	puertoLocal string
}

func (t *traza) hooks() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSDone: func(httptrace.DNSDoneInfo) {
			t.dns = time.Since(t.inicio)
		},
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				t.conexion = time.Since(t.inicio)
			}
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				t.tls = time.Since(t.inicio)
			}
		},
		GotConn: func(i httptrace.GotConnInfo) {
			t.reusado, t.ocioso, t.ocio = i.Reused, i.WasIdle, i.IdleTime
			if i.Conn == nil {
				return
			}
			t.remota = i.Conn.RemoteAddr().String()
			if _, p, err := net.SplitHostPort(i.Conn.LocalAddr().String()); err == nil {
				t.puertoLocal = p
			}
		},
	}
}

// EsperaHastaAlinear devuelve cuánto falta para el próximo múltiplo redondo de
// la cadencia. Los brazos arrancan alineados al reloj de pared para que los de
// 60 s disparen en el mismo segundo, como hace producción: si se desfasaran, un
// blip que le pega a todos a la vez se vería como fallas independientes.
func EsperaHastaAlinear(ahora time.Time, cadencia time.Duration) time.Duration {
	if cadencia <= 0 {
		return 0
	}
	resto := time.Duration(ahora.UnixNano()) % cadencia
	if resto == 0 {
		return cadencia
	}
	return cadencia - resto
}

// NombresDeBrazos es para los mensajes de error de la línea de comandos.
func NombresDeBrazos(bs []Brazo) string {
	ns := make([]string, len(bs))
	for i, b := range bs {
		ns[i] = b.Nombre
	}
	return strings.Join(ns, ", ")
}
