package comandos_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/comandos"
	"github.com/juanandresdavila/server-status/internal/model"
)

func TestParsear(t *testing.T) {
	casos := []struct {
		texto  string
		nombre string
		args   []string
	}{
		{"/status", "status", nil},
		{"/logs comm-tool 50", "logs", []string{"comm-tool", "50"}},
		{"  /incidentes  ", "incidentes", nil},
		{"/SILENCIAR 2h", "silenciar", []string{"2h"}},
		// Telegram le agrega el sufijo del bot cuando el comando va en un grupo.
		{"/status@serverstatusjaddbot", "status", nil},
	}
	for _, c := range casos {
		got, ok := comandos.Parsear(c.texto)
		if !ok {
			t.Errorf("Parsear(%q) dijo que no es comando", c.texto)
			continue
		}
		if got.Nombre != c.nombre {
			t.Errorf("Parsear(%q).Nombre = %q, quería %q", c.texto, got.Nombre, c.nombre)
		}
		if len(got.Args) != len(c.args) {
			t.Errorf("Parsear(%q).Args = %v, quería %v", c.texto, got.Args, c.args)
		}
	}
}

func TestLoQueNoEsComando(t *testing.T) {
	for _, texto := range []string{"hola", "", "   ", "status", "no /status"} {
		if _, ok := comandos.Parsear(texto); ok {
			t.Errorf("Parsear(%q) lo tomó como comando", texto)
		}
	}
}

type datosFalsos struct{}

func (datosFalsos) UltimasHostSamples(int) ([]model.HostSample, error) {
	return []model.HostSample{{
		MemUsedBytes: 3_000_000_000, MemTotalBytes: 12_000_000_000,
		DiskUsedBytes: 16_000_000_000, DiskTotalBytes: 100_000_000_000,
		Uptime: 50 * time.Hour,
	}}, nil
}
func (datosFalsos) UltimoEstadoProbes() ([]model.ProbeResult, error) {
	return []model.ProbeResult{
		{Servicio: "comm-tool", OK: true},
		{Servicio: "sitio", OK: false, Error: "HTTP 502"},
	}, nil
}
func (datosFalsos) UltimosIncidentes(int) ([]model.Incidente, error) {
	return []model.Incidente{{
		Sujeto: "service:sitio", Severidad: "critical",
		AbiertoEn: time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC), Detalle: "HTTP 502",
	}}, nil
}
func (datosFalsos) BuscarLogs(texto, container string, desde, hasta time.Time, limite int) ([]model.LineaLog, error) {
	var out []model.LineaLog
	for i := 0; i < limite && i < 40; i++ {
		out = append(out, model.LineaLog{
			TS: time.Now(), Container: container, Stream: "stdout",
			Linea: strings.Repeat("línea larga de log ", 20),
		})
	}
	return out, nil
}
func (datosFalsos) Silenciar(time.Time) error { return nil }

func ejecutar(t *testing.T, texto string) string {
	t.Helper()
	c, ok := comandos.Parsear(texto)
	if !ok {
		t.Fatalf("no es comando: %q", texto)
	}
	r, err := comandos.Ejecutar(datosFalsos{}, c, time.Now())
	if err != nil {
		t.Fatalf("Ejecutar(%q): %v", texto, err)
	}
	return r
}

func TestStatusMuestraServiciosYRecursos(t *testing.T) {
	r := ejecutar(t, "/status")
	for _, q := range []string{"comm-tool", "sitio", "16", "disco"} {
		if !strings.Contains(strings.ToLower(r), strings.ToLower(q)) {
			t.Errorf("/status no menciona %q:\n%s", q, r)
		}
	}
}

func TestIncidentes(t *testing.T) {
	if r := ejecutar(t, "/incidentes"); !strings.Contains(r, "sitio") {
		t.Errorf("/incidentes no lista nada:\n%s", r)
	}
}

// Telegram corta a los 4096 caracteres y comm-tool valida ese largo ANTES de
// mandar: pasarse convierte la respuesta en un 400 y el bot queda mudo.
func TestNingunaRespuestaPasaElLimiteDeTelegram(t *testing.T) {
	for _, texto := range []string{"/status", "/incidentes", "/logs comm-tool 50", "/logs x 999"} {
		if n := len(ejecutar(t, texto)); n > 4096 {
			t.Errorf("%q respondió %d caracteres, el límite es 4096", texto, n)
		}
	}
}

func TestSilenciarParseaLaDuracion(t *testing.T) {
	if r := ejecutar(t, "/silenciar 2h"); !strings.Contains(r, "2h") && !strings.Contains(r, "2 h") {
		t.Errorf("/silenciar no confirma la duración:\n%s", r)
	}
	if r := ejecutar(t, "/silenciar 0"); !strings.Contains(strings.ToLower(r), "cancel") {
		t.Errorf("/silenciar 0 no cancela:\n%s", r)
	}
	// Una duración inválida se explica, no se ignora.
	if r := ejecutar(t, "/silenciar manzana"); !strings.Contains(strings.ToLower(r), "no entend") {
		t.Errorf("/silenciar con basura no avisa:\n%s", r)
	}
}

// Un comando desconocido responde con la lista, no con silencio: si no, uno
// se queda esperando una respuesta que nunca llega.
func TestComandoDesconocidoListaLosQueHay(t *testing.T) {
	r := ejecutar(t, "/reiniciar")
	for _, q := range []string{"/status", "/logs", "/incidentes", "/silenciar"} {
		if !strings.Contains(r, q) {
			t.Errorf("la ayuda no menciona %q:\n%s", q, r)
		}
	}
}
