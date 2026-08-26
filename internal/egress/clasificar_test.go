package egress_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/juanandresdavila/server-status/internal/egress"
)

// Las direcciones de los tests usan el prefijo de documentación 2001:db8::/32
// (RFC 3849) a propósito: la IPv6 real del VPS no entra al repo ni en un test.

func TestClasificarSeparaResetDeTimeout(t *testing.T) {
	casos := []struct {
		nombre string
		err    error
		quiero egress.Clase
	}{
		{"nil es ok", nil, egress.ClaseOK},
		{
			"reset crudo de Go",
			fmt.Errorf(`Get "https://jadd.com.ar/": read tcp [2001:db8::1]:43016->[2606:4700:130::1]:443: read: connection reset by peer`),
			egress.ClaseReset,
		},
		{
			"timeout del cliente",
			fmt.Errorf(`Get "https://jadd.com.ar/": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`),
			egress.ClaseTimeout,
		},
		{"deadline envuelto", fmt.Errorf("latido: %w", context.DeadlineExceeded), egress.ClaseTimeout},
		{"i/o timeout", errors.New("dial tcp 192.0.2.1:443: i/o timeout"), egress.ClaseTimeout},
		{"dns", &net.DNSError{Err: "no such host", Name: "nada.invalid"}, egress.ClaseDNS},
		{"rechazada", errors.New("dial tcp 192.0.2.1:443: connect: connection refused"), egress.ClaseRechazada},
		{"tls", errors.New(`x509: certificate signed by unknown authority`), egress.ClaseTLS},
		{"otra cosa", errors.New("algo raro"), egress.ClaseOtro},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := egress.Clasificar(c.err); got != c.quiero {
				t.Errorf("Clasificar(%v) = %q, quería %q", c.err, got, c.quiero)
			}
		})
	}
}

// Go no entrega el error como string plano sino como *net.OpError envuelto en
// *url.Error. Si la clasificación dependiera de un tipo concreto en vez del
// texto, esto pasaría a ClaseOtro y los resets desaparecerían del conteo.
func TestClasificarReconoceUnResetEstructurado(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://jadd.com.ar/",
		Err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("read: connection reset by peer")},
	}
	if got := egress.Clasificar(err); got != egress.ClaseReset {
		t.Fatalf("Clasificar = %q, quería %q", got, egress.ClaseReset)
	}
}

// Un mismo error puede traer las dos marcas: el reintento de Go concatena el
// intento que timeouteó con el que recibió el RST. Ahí decide el ORDEN de los
// checks, y tiene que ganar el reset — es la firma que distingue "conexión ya
// establecida que se cortó" de "los 10 s se agotaron". Este test falla si se
// invierten.
func TestElResetLeGanaAlTimeoutCuandoAparecenLosDos(t *testing.T) {
	err := errors.New(`read tcp [2001:db8::1]:1->[2606:4700::1]:443: read: connection reset by peer (tras i/o timeout)`)
	if got := egress.Clasificar(err); got != egress.ClaseReset {
		t.Fatalf("Clasificar = %q, quería %q: el orden de los checks se invirtió", got, egress.ClaseReset)
	}
}

func TestEsFallaSoloExcluyeOK(t *testing.T) {
	if egress.ClaseOK.EsFalla() {
		t.Error("ClaseOK.EsFalla() = true")
	}
	for _, c := range []egress.Clase{egress.ClaseReset, egress.ClaseTimeout, egress.ClaseDNS,
		egress.ClaseRechazada, egress.ClaseTLS, egress.ClaseOtro} {
		if !c.EsFalla() {
			t.Errorf("%q.EsFalla() = false", c)
		}
	}
}

// El repo es público: la IP de origen no puede quedar escrita en ningún lado.
func TestRedactarTachaElOrigenYConservaElDestino(t *testing.T) {
	crudo := `Get "https://jadd.com.ar/": read tcp [2001:db8::1]:43016->[2606:4700:130:436c:6f75:6466:6c61:7265]:443: read: connection reset by peer`
	got := egress.Redactar(crudo)

	if strings.Contains(got, "2001:db8::1") {
		t.Fatalf("la IP de origen sobrevivió a la redacción: %q", got)
	}
	if !strings.Contains(got, "2606:4700:130:436c:6f75:6466:6c61:7265") {
		t.Errorf("se perdió la IP de destino, que es la que hay que comparar entre fallas: %q", got)
	}
	if !strings.Contains(got, "[<origen>]:43016") {
		t.Errorf("se perdió el puerto de origen: %q", got)
	}
	if !strings.Contains(got, "connection reset by peer") {
		t.Errorf("se perdió el error: %q", got)
	}
}

func TestRedactarIPv4(t *testing.T) {
	got := egress.Redactar(`read tcp 192.0.2.10:5555->198.51.100.7:443: read: connection reset by peer`)
	if strings.Contains(got, "192.0.2.10") {
		t.Fatalf("la IP de origen sobrevivió: %q", got)
	}
	if !strings.Contains(got, "198.51.100.7:443") {
		t.Errorf("se perdió el destino: %q", got)
	}
	if !strings.Contains(got, "<origen>:5555") {
		t.Errorf("se perdió el puerto: %q", got)
	}
}

// Sin "->" lo único que hay es una dirección de destino. Tacharla perdería el
// dato y no protegería nada.
func TestRedactarDejaIntactoElDestinoSolo(t *testing.T) {
	casos := []string{
		`dial tcp [2606:4700:130::1]:443: connect: connection refused`,
		`dial tcp4 198.51.100.7:443: i/o timeout`,
		`Get "https://jadd.com.ar/": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
	}
	for _, c := range casos {
		if got := egress.Redactar(c); got != c {
			t.Errorf("Redactar(%q) = %q, quería que no lo tocara", c, got)
		}
	}
}
