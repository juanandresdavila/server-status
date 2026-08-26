// Package egress mide el egress del VPS por IPv4 y por IPv6 en paralelo, para
// construir el contrafactual que falta: "cero fallas por IPv4" no prueba nada
// mientras nunca se haya intentado por IPv4.
//
// El plan y la regla de decisión pre-registrada están en
// docs/superpowers/plans/2026-08-26-egress-ipv6-vs-ipv4.md.
package egress

import (
	"context"
	"errors"
	"net"
	"strings"
)

// Clase es el tipo de falla. Se separa `reset` de `timeout` porque son firmas
// distintas: el reset llega en milisegundos sobre una conexión ya establecida,
// el timeout agota los 10 s. Confundirlos mezcló dos fenómenos el 26/08/2026.
type Clase string

const (
	ClaseOK        Clase = "ok"
	ClaseReset     Clase = "reset"
	ClaseTimeout   Clase = "timeout"
	ClaseDNS       Clase = "dns"
	ClaseRechazada Clase = "rechazada"
	ClaseTLS       Clase = "tls"
	ClaseOtro      Clase = "otro"
)

// Clasificar reduce el error de un intento a una clase. Nunca devuelve error:
// la falla ES el dato, igual que en internal/prober.
func Clasificar(err error) Clase {
	if err == nil {
		return ClaseOK
	}
	s := err.Error()

	// El reset va primero: un RST también satisface net.Error, y clasificarlo
	// como timeout borraría justamente la distinción que interesa.
	if strings.Contains(s, "connection reset by peer") {
		return ClaseReset
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(s, "Client.Timeout") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "context deadline exceeded") {
		return ClaseTimeout
	}

	var dns *net.DNSError
	if errors.As(err, &dns) || strings.Contains(s, "no such host") {
		return ClaseDNS
	}
	if strings.Contains(s, "connection refused") {
		return ClaseRechazada
	}
	if strings.Contains(s, "tls:") || strings.Contains(s, "x509:") {
		return ClaseTLS
	}
	return ClaseOtro
}

// EsFalla dice si la clase cuenta como falla de transporte. Un status HTTP feo
// no cuenta: el 401 de un Supabase sin apikey recorrió la red entera igual.
func (c Clase) EsFalla() bool { return c != ClaseOK }

// Redactar tacha la IP de ORIGEN que Go mete en los errores de red.
//
// Existe porque el repo es público. Un error crudo viene así:
//
//	read tcp [<ip-del-vps>]:43016->[2606:4700:…]:443: read: connection reset by peer
//
// y esa primera dirección es la IPv6 del VPS. El puerto se conserva —no es un
// dato sensible y sirve para seguir una conexión—; la dirección se reemplaza.
// La de DESTINO se deja: es una IP pública de Cloudflare y es justamente lo que
// hay que poder comparar entre fallas.
func Redactar(s string) string {
	var b strings.Builder
	resto := s
	for {
		i := inicioDireccionOrigen(resto)
		if i < 0 {
			b.WriteString(resto)
			return b.String()
		}
		// Sin "->" no hay par origen→destino: lo que sigue es una dirección de
		// destino sola (`dial tcp <destino>: connection refused`) y no se toca.
		j := strings.Index(resto[i:], "->")
		if j < 0 {
			b.WriteString(resto)
			return b.String()
		}
		b.WriteString(resto[:i])
		b.WriteString(redactarDireccion(resto[i : i+j]))
		b.WriteString("->")
		resto = resto[i+j+2:]
	}
}

// inicioDireccionOrigen devuelve el índice donde arranca la dirección después
// de " tcp ", " tcp4 " o " tcp6 ", o -1.
func inicioDireccionOrigen(s string) int {
	for _, red := range []string{" tcp ", " tcp4 ", " tcp6 "} {
		if i := strings.Index(s, red); i >= 0 {
			return i + len(red)
		}
	}
	return -1
}

// redactarDireccion cambia el host por <origen> y deja el puerto.
func redactarDireccion(dir string) string {
	if dir == "" {
		return ""
	}
	if host, puerto, err := net.SplitHostPort(dir); err == nil {
		if strings.Contains(host, ":") { // IPv6 va entre corchetes
			return "[<origen>]:" + puerto
		}
		return "<origen>:" + puerto
	}
	return "<origen>"
}
