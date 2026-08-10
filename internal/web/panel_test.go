package web_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/web"
)

type datosFalsos struct{}

func (datosFalsos) UltimasHostSamples(int) ([]model.HostSample, error) {
	return []model.HostSample{{
		TS:        time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		CPUPctAvg: 4.2, CPUPctMax: 18.0,
		Load1: 0.41, Load5: 0.6, Load15: 0.53,
		MemUsedBytes: 3_000_000_000, MemTotalBytes: 12_000_000_000,
		SwapUsedBytes: 0, SwapTotalBytes: 2_147_483_648,
		DiskUsedBytes: 15_700_000_000, DiskTotalBytes: 100_000_000_000,
		Uptime: 50 * time.Hour,
	}}, nil
}

func (datosFalsos) UltimoEstadoContainers() ([]model.ContainerSample, error) {
	return []model.ContainerSample{
		{Name: "comm-tool", State: "running", Health: "none", CPUPct: 0.1, MemBytes: 29_000_000},
		{Name: "supabase-db", State: "running", Health: "healthy", Restarts: 2, MemBytes: 84_000_000},
	}, nil
}

func (datosFalsos) UltimoEstadoProbes() ([]model.ProbeResult, error) {
	return []model.ProbeResult{
		{Servicio: "comm-tool", OK: true, StatusCode: 200, Latencia: 85 * time.Millisecond},
		{Servicio: "sitio", OK: false, StatusCode: 0, Error: "dial tcp 127.0.0.1:8787: connect: connection refused"},
	}, nil
}

func (datosFalsos) UltimosIncidentes(int) ([]model.Incidente, error) {
	return []model.Incidente{{
		ID: 1, Sujeto: "service:sitio", Tipo: "down", Severidad: "critical",
		AbiertoEn: time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC), Detalle: "HTTP 502",
	}}, nil
}

func (d datosFalsos) SerieHost(desde, hasta time.Time) ([]model.HostSample, error) {
	base := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	var out []model.HostSample
	for i := range 3 {
		out = append(out, model.HostSample{
			TS: base.Add(time.Duration(i) * time.Minute), CPUPctAvg: float64(i * 5),
			MemUsedBytes: 3_000_000_000, MemTotalBytes: 12_000_000_000,
			DiskUsedBytes: 15_000_000_000, DiskTotalBytes: 100_000_000_000,
			Load1: 0.4,
		})
	}
	return out, nil
}

func (datosFalsos) BuscarLogs(texto, container string, desde, hasta time.Time, limite int) ([]model.LineaLog, error) {
	return []model.LineaLog{{
		TS:        time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Container: "comm-tool", Stream: "stderr", Linea: "ERROR conexion rechazada",
	}}, nil
}

func TestPaginaDeLogsMuestraResultados(t *testing.T) {
	cuerpo := pedir(t, "/logs?q=ERROR").Body.String()
	if !strings.Contains(cuerpo, "conexion rechazada") {
		t.Errorf("la página de logs no muestra la línea: %q", cuerpo[:min(300, len(cuerpo))])
	}
	// El selector de container se arma del último estado, no de los logs.
	if !strings.Contains(cuerpo, "supabase-db") {
		t.Error("el selector no lista los containers")
	}
}

func TestTailSinContainerNoRenderiza(t *testing.T) {
	if rec := pedir(t, "/logs/tail"); rec.Code != 400 {
		t.Errorf("código = %d, quería 400", rec.Code)
	}
}

func pedir(t *testing.T, ruta string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	web.NuevoPanel(datosFalsos{}).ServeHTTP(rec, httptest.NewRequest("GET", ruta, nil))
	return rec
}

// Las tres vistas son una sola herramienta: sin header no se llega a los logs
// más que tipeando la URL, que es como estaba antes.
func TestLasTresVistasTraenElHeader(t *testing.T) {
	for _, ruta := range []string{"/", "/logs", "/logs/tail?container=comm-tool"} {
		cuerpo := pedir(t, ruta).Body.String()
		if !strings.Contains(cuerpo, `class="nav"`) {
			t.Errorf("%s no trae el header", ruta)
		}
		for _, link := range []string{`href="/?horas=`, `href="/logs?horas=`} {
			if !strings.Contains(cuerpo, link) {
				t.Errorf("%s no linkea a %s", ruta, link)
			}
		}
	}
}

func TestElHeaderMarcaLaVistaActiva(t *testing.T) {
	casos := map[string]string{
		"/":                              `href="/?horas=24" class="activo"`,
		"/logs":                          `href="/logs?horas=24" class="activo"`,
		"/logs/tail?container=comm-tool": `container=comm-tool" class="activo"`,
	}
	for ruta, quiero := range casos {
		if cuerpo := pedir(t, ruta).Body.String(); !strings.Contains(cuerpo, quiero) {
			t.Errorf("%s no marca la vista activa: falta %q", ruta, quiero)
		}
	}
}

// Elegir "7 días" en el panel y que /logs arranque de nuevo en 24 h es perder
// el contexto en el momento en que más se lo necesita.
func TestElRangoSePropagaEntreVistas(t *testing.T) {
	if cuerpo := pedir(t, "/?horas=168").Body.String(); !strings.Contains(cuerpo, `href="/logs?horas=168`) {
		t.Error("el panel no le pasa el rango a /logs")
	}
	if cuerpo := pedir(t, "/logs?horas=720").Body.String(); !strings.Contains(cuerpo, `href="/?horas=720"`) {
		t.Error("/logs no le devuelve el rango al panel")
	}
}

// Ver un container en unhealthy y tener que ir a copiar el nombre a mano es
// justo la fricción que el header viene a sacar.
func TestElContainerLinkeaASusLogs(t *testing.T) {
	cuerpo := pedir(t, "/").Body.String()
	if !strings.Contains(cuerpo, `href="/logs?container=supabase-db`) {
		t.Error("el nombre del container en la tabla no lleva a sus logs")
	}
}

// El tail necesita un container sí o sí: ofrecerlo sin uno elegido es un 400
// esperando a pasar.
func TestElTailEnElHeaderPideContainer(t *testing.T) {
	if cuerpo := pedir(t, "/logs").Body.String(); strings.Contains(cuerpo, "/logs/tail") {
		t.Error("sin container elegido el header no debería ofrecer el tail")
	}
	if cuerpo := pedir(t, "/logs?container=comm-tool").Body.String(); !strings.Contains(cuerpo, "/logs/tail?container=comm-tool") {
		t.Error("con container elegido el header debería ofrecer el tail")
	}
}

func TestPanelMuestraLoRecolectado(t *testing.T) {
	rec := pedir(t, "/")
	if rec.Code != 200 {
		t.Fatalf("código = %d", rec.Code)
	}
	cuerpo := rec.Body.String()
	for _, quiero := range []string{"comm-tool", "supabase-db", "sitio", "server-status"} {
		if !strings.Contains(cuerpo, quiero) {
			t.Errorf("el panel no muestra %q", quiero)
		}
	}
}

// A diferencia de la portada pública, el panel SÍ muestra todo: vive en el
// tailnet y es para el dueño del servidor. Si acá escondiéramos el error crudo
// del probe, diagnosticar sería adivinar.
func TestPanelMuestraLoQueLaPortadaEsconde(t *testing.T) {
	cuerpo := pedir(t, "/").Body.String()
	if !strings.Contains(cuerpo, "connection refused") {
		t.Error("el panel debería mostrar el error crudo del probe")
	}
}

func TestApiSeriesDevuelveJSONConLargosParejos(t *testing.T) {
	rec := pedir(t, "/api/series?horas=24")
	if rec.Code != 200 {
		t.Fatalf("código = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	var p struct {
		TS    []int64   `json:"ts"`
		CPU   []float64 `json:"cpu"`
		Mem   []float64 `json:"mem"`
		Disco []float64 `json:"disco"`
		Load  []float64 `json:"load"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("no es JSON válido: %v", err)
	}
	if len(p.TS) != 3 {
		t.Fatalf("ts tiene %d puntos, quería 3", len(p.TS))
	}
	// Si los largos no coinciden, ECharts dibuja cualquier cosa sin quejarse.
	for nombre, serie := range map[string][]float64{"cpu": p.CPU, "mem": p.Mem, "disco": p.Disco, "load": p.Load} {
		if len(serie) != len(p.TS) {
			t.Errorf("%s tiene %d puntos y ts tiene %d", nombre, len(serie), len(p.TS))
		}
	}
	if p.Mem[0] != 25 {
		t.Errorf("mem = %v, quería 25 (3 de 12 GB)", p.Mem[0])
	}
}

// El parámetro es entrada de afuera aunque el panel sea privado: cualquier
// basura tiene que caer al default en vez de romper.
func TestHorasInvalidasCaenAlDefault(t *testing.T) {
	for _, q := range []string{"?horas=abc", "?horas=-5", "?horas=0", "?horas=99999", ""} {
		rec := pedir(t, "/api/series"+q)
		if rec.Code != 200 {
			t.Errorf("con %q el código fue %d, quería 200", q, rec.Code)
		}
	}
}

func TestHealthDelPanel(t *testing.T) {
	if rec := pedir(t, "/health"); rec.Code != 200 {
		t.Errorf("código = %d", rec.Code)
	}
}

// ECharts se sirve desde el binario, no desde un CDN: el panel vive en el
// tailnet y no puede depender de que el navegador llegue a internet.
func TestEChartsSeSirveDesdeElBinario(t *testing.T) {
	rec := pedir(t, "/assets/echarts.min.js")
	if rec.Code != 200 {
		t.Fatalf("código = %d: ECharts no se está sirviendo", rec.Code)
	}
	if rec.Body.Len() < 100_000 {
		t.Errorf("el archivo mide %d bytes, parece incompleto", rec.Body.Len())
	}
}
