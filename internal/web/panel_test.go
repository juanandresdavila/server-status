package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/web"
)

// El VPS corre en Etc/UTC, así que time.Local ahí es UTC y el panel mostraba
// UTC creyendo mostrar hora argentina. Los tests fijan la zona a propósito para
// que el bug no pueda volver escondido detrás del huso de quien corra el test.
var zonaDePrueba = func() *time.Location {
	l, err := time.LoadLocation("America/Argentina/Buenos_Aires")
	if err != nil {
		panic(err)
	}
	return l
}()

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
	// A propósito con los rotos al final y los sanos desordenados por RAM:
	// si el handler no ordena, el test del orden por defecto lo ve.
	return []model.ContainerSample{
		{Name: "comm-tool", State: "running", Health: "none", CPUPct: 0.1, MemBytes: 29_000_000},
		{Name: "supabase-db", State: "running", Health: "healthy", Restarts: 2, MemBytes: 84_000_000},
		{Name: "muerto", State: "exited", Health: "none", MemBytes: 5_000_000},
		{Name: "enfermo", State: "running", Health: "unhealthy", MemBytes: 10_000_000},
	}, nil
}

func (datosFalsos) UltimoEstadoProbes() ([]model.ProbeResult, error) {
	// El caído al final a propósito: sin orden en el handler quedaría abajo.
	return []model.ProbeResult{
		{Servicio: "comm-tool", OK: true, StatusCode: 200, Latencia: 85 * time.Millisecond},
		{Servicio: "lento", OK: true, StatusCode: 200, Latencia: 900 * time.Millisecond},
		{Servicio: "sitio", OK: false, StatusCode: 0, Error: "dial tcp 127.0.0.1:8787: connect: connection refused"},
	}, nil
}

func (datosFalsos) UltimosIncidentes(int) ([]model.Incidente, error) {
	cerrado := time.Date(2026, 8, 9, 11, 30, 0, 0, time.UTC)
	archivado := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	return []model.Incidente{
		{
			ID: 1, Sujeto: "service:sitio", Tipo: "down", Severidad: "critical",
			AbiertoEn: time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC), Detalle: "HTTP 502",
		},
		{
			ID: 2, Sujeto: "service:cerrado", Tipo: "down", Severidad: "warning",
			AbiertoEn: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC), CerradoEn: &cerrado,
			Detalle: "HTTP 500",
		},
		{
			ID: 3, Sujeto: "service:viejo-archivado", Tipo: "down", Severidad: "warning",
			AbiertoEn: time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC), CerradoEn: &cerrado,
			ArchivadoEn: &archivado, Detalle: "HTTP 503",
		},
	}, nil
}

// Las acciones sobre incidentes no le importan a la mayoría de los tests.
func (datosFalsos) CerrarIncidente(int64, time.Time) error   { return nil }
func (datosFalsos) ArchivarIncidente(int64, time.Time) error { return nil }

func (datosFalsos) MaxRowidLogs() (int64, error) { return 4321, nil }

func (datosFalsos) LogsDesdeRowid(texto, container string, niveles []string, desde int64, limite int) ([]model.LineaLog, int64, error) {
	return []model.LineaLog{{
		TS:        time.Date(2026, 8, 9, 12, 0, 30, 0, time.UTC),
		Container: "comm-tool", Stream: "stderr", Linea: "ERROR recien llegada",
		Nivel: "ERROR",
	}}, desde + 1, nil
}

// recreado se reinició una vez en la ventana con RestartCount en CERO: es lo
// que deja `compose up -d`, y es el caso que la columna vieja no veía.
func (datosFalsos) ReiniciosEntre(desde, hasta time.Time) (map[string]int, error) {
	return map[string]int{"supabase-db": 3, "comm-tool": 1}, nil
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

func (datosFalsos) BuscarLogs(texto, container string, niveles []string, desde, hasta time.Time, limite int) ([]model.LineaLog, error) {
	return []model.LineaLog{{
		TS:        time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Container: "comm-tool", Stream: "stderr", Linea: "ERROR conexion rechazada",
		Nivel: "ERROR",
	}}, nil
}

func (datosFalsos) EventosEntre(desde, hasta time.Time, limite int) ([]model.Evento, error) {
	return []model.Evento{{
		ID: 1, Tipo: "reboot", Sujeto: "host", Severidad: "critical",
		OcurridoEn: time.Date(2026, 8, 22, 5, 0, 31, 0, time.UTC),
		Detalle:    "la máquina se reinició: arrancó 22/08 02:00:31, sin datos durante 1m20s",
	}}, nil
}

// El export baja lo mismo que muestra la vista, pero como archivo de texto
// plano: es la forma de llevarse los logs de un container a otra herramienta.
func TestExportDeLogsDescargaTextoPlano(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/logs/export?container=comm-tool&horas=6", nil))

	if w.Code != 200 {
		t.Fatalf("status = %d, quería 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, quería text/plain", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "comm-tool") {
		t.Errorf("Content-Disposition = %q: tiene que ser attachment y nombrar el container", cd)
	}
	cuerpo := w.Body.String()
	for _, q := range []string{"ERROR conexion rechazada", "comm-tool", "stderr"} {
		if !strings.Contains(cuerpo, q) {
			t.Errorf("el export no trae %q:\n%s", q, cuerpo)
		}
	}
}

// Sin container el archivo se llama "todos": el filtro vacío es válido en la
// vista y el export tiene que aceptar lo mismo que ella.
func TestExportDeLogsSinContainer(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/logs/export", nil))

	if w.Code != 200 {
		t.Fatalf("status = %d, quería 200", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "todos") {
		t.Errorf("Content-Disposition = %q, sin container el nombre es 'todos'", cd)
	}
}

// La vista de logs ofrece el export: un endpoint que solo se conoce por la
// documentación es un endpoint que no se usa.
func TestLaVistaDeLogsOfreceElExport(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/logs?container=comm-tool", nil))

	if !strings.Contains(w.Body.String(), "/logs/export") {
		t.Errorf("la vista de logs no linkea /logs/export:\n%s", w.Body.String())
	}
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
	web.NuevoPanel(datosFalsos{}, zonaDePrueba).ServeHTTP(rec, httptest.NewRequest("GET", ruta, nil))
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

// Memoria y disco también viajan en GiB: el panel deja alternar la unidad y
// los gráficos necesitan la serie ya convertida, con el total para la escala.
func TestApiSeriesTraeLasSeriesEnGiB(t *testing.T) {
	rec := pedir(t, "/api/series?horas=24")
	var p struct {
		TS            []int64   `json:"ts"`
		MemGiB        []float64 `json:"mem_gib"`
		DiscoGiB      []float64 `json:"disco_gib"`
		MemTotalGiB   float64   `json:"mem_total_gib"`
		DiscoTotalGiB float64   `json:"disco_total_gib"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("no es JSON válido: %v", err)
	}
	for nombre, serie := range map[string][]float64{"mem_gib": p.MemGiB, "disco_gib": p.DiscoGiB} {
		if len(serie) != len(p.TS) {
			t.Errorf("%s tiene %d puntos y ts tiene %d", nombre, len(serie), len(p.TS))
		}
	}
	// 3 GB decimales son ~2.794 GiB. Tolerancia y no ==: gotcha conocido del repo.
	cerca := func(got, quiere float64) bool { return got > quiere-0.01 && got < quiere+0.01 }
	if len(p.MemGiB) == 0 || !cerca(p.MemGiB[0], 2.79) {
		t.Errorf("mem_gib[0] = %v, quería ~2.79 (3e9 bytes)", p.MemGiB)
	}
	if !cerca(p.MemTotalGiB, 11.18) {
		t.Errorf("mem_total_gib = %v, quería ~11.18 (12e9 bytes)", p.MemTotalGiB)
	}
	if !cerca(p.DiscoTotalGiB, 93.13) {
		t.Errorf("disco_total_gib = %v, quería ~93.13 (100e9 bytes)", p.DiscoTotalGiB)
	}
}

// Las tarjetas de memoria y disco llevan las dos unidades en el HTML: el
// toggle es del navegador y no puede depender de otro request al server.
func TestLasTarjetasDeMemoriaYDiscoAlternanUnidad(t *testing.T) {
	cuerpo := pedir(t, "/").Body.String()
	for _, q := range []string{"data-gib", "data-pct", "GiB"} {
		if !strings.Contains(cuerpo, q) {
			t.Errorf("el panel no trae %q: sin eso no hay toggle de unidad", q)
		}
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

// La vista que faltaba: el reinicio del 22/08 estaba en la base y no había
// ninguna pantalla donde se viera.
func TestVistaEventosMuestraElReinicio(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/events?horas=720", nil))

	if w.Code != 200 {
		t.Fatalf("código = %d, quería 200", w.Code)
	}
	cuerpo := w.Body.String()
	for _, quiero := range []string{"el servidor se reinició", "sin datos durante 1m20s"} {
		if !strings.Contains(cuerpo, quiero) {
			t.Errorf("la vista no contiene %q", quiero)
		}
	}
}

// El filtro de severidad es un toggle por ítem: pidiendo solo critical, un
// warning no va — y un conjunto no contiguo (info+critical) también se puede.
func TestVistaEventosFiltraPorSeveridad(t *testing.T) {
	cuerpo := pedir(t, "/events?horas=720&sev=info&sev=critical").Body.String()
	if strings.Contains(cuerpo, "conexion rechazada") {
		t.Error("con warning apagado no va un error de log, que es warning")
	}
	if !strings.Contains(cuerpo, "el servidor se reinició") {
		t.Error("el reboot es critical y tenía que estar")
	}

	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba)
	w := httptest.NewRecorder()
	// Los errores de log son warning: pidiendo solo críticos no van.
	h.ServeHTTP(w, httptest.NewRequest("GET", "/events?horas=720&sev=critical", nil))

	if strings.Contains(w.Body.String(), "conexion rechazada") {
		t.Error("con sev=critical no tendría que aparecer un error de log, que es warning")
	}
	if !strings.Contains(w.Body.String(), "el servidor se reinició") {
		t.Error("el reboot es critical y tendría que aparecer")
	}
}

func TestNovedadesOrdenaDeLoMasNuevoALoMasViejo(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/events?horas=720", nil))

	cuerpo := w.Body.String()
	reboot := strings.Index(cuerpo, "el servidor se reinició") // 22/08
	logErr := strings.Index(cuerpo, "conexion rechazada")      // 09/08
	if reboot < 0 || logErr < 0 {
		t.Fatal("faltan novedades en la vista")
	}
	if reboot > logErr {
		t.Error("el reinicio del 22/08 tiene que ir ANTES que el error del 09/08")
	}
}

// La zona la manda la CONFIG, no time.Local: el VPS corre en Etc/UTC. Sin esto,
// tecleando 01:50 en el campo "desde" el server buscaba las 01:50 UTC, que son
// las 22:50 del día anterior en Argentina. Tres horas de corrimiento sobre lo
// que uno quiso pedir, y nada que lo indicara.
func TestElRangoSeInterpretaEnLaZonaConfigurada(t *testing.T) {
	// A propósito NO se usa Buenos Aires acá: en la máquina de desarrollo
	// time.Local ES Buenos Aires, así que un test con esa zona pasaría igual
	// con el bug puesto y no mediría nada. Con Tokio (+9) las dos respuestas
	// se separan quince horas y el test no se puede engañar.
	tokio, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("sin tzdata para Asia/Tokyo")
	}

	var visto struct{ desde, hasta time.Time }
	h := web.NuevoPanel(espia{cb: func(d, ha time.Time) { visto.desde, visto.hasta = d, ha }}, tokio)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET",
		"/logs?desde=2026-08-22T01:50&hasta=2026-08-22T02:10", nil))

	// 01:50 del 22 en Tokio (UTC+9) son las 16:50 UTC del 21.
	if got := visto.desde.UTC().Format("2006-01-02 15:04"); got != "2026-08-21 16:50" {
		t.Errorf("desde = %s UTC, quería 2026-08-21 16:50 (01:50 en Tokio)", got)
	}
	if got := visto.hasta.UTC().Format("2006-01-02 15:04"); got != "2026-08-21 17:10" {
		t.Errorf("hasta = %s UTC, quería 2026-08-21 17:10 (02:10 en Tokio)", got)
	}
}

// Y lo que se muestra también va en esa zona, no en la del proceso.
func TestLasHorasSeMuestranEnLaZonaConfigurada(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/events?horas=720", nil))

	// El evento falso ocurrió 05:00:31 UTC → 02:00:31 en Buenos Aires.
	if !strings.Contains(w.Body.String(), "02:00:31") {
		t.Error("la vista no muestra la hora en la zona configurada (esperaba 02:00:31)")
	}
	if strings.Contains(w.Body.String(), "05:00:31") {
		t.Error("la vista está mostrando UTC crudo")
	}
}

// El mismo control que el del rango, pero sobre lo que se MUESTRA: con Tokio
// la respuesta correcta no puede coincidir con la de time.Local.
func TestLoQueSeMuestraNoSaleDeTimeLocal(t *testing.T) {
	tokio, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("sin tzdata para Asia/Tokyo")
	}
	h := web.NuevoPanel(datosFalsos{}, tokio)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/events?horas=720", nil))

	// 05:00:31 UTC → 14:00:31 en Tokio.
	if !strings.Contains(w.Body.String(), "14:00:31") {
		t.Error("con zona Tokio la vista tendría que mostrar 14:00:31")
	}
}

// El panel habla español por defecto; ?lang=en lo cambia y lo recuerda en una
// cookie, así el auto-reload de 60 s no lo devuelve al castellano.
func TestElPanelEsBilingue(t *testing.T) {
	if cuerpo := pedir(t, "/").Body.String(); !strings.Contains(cuerpo, "Servicios") {
		t.Error("sin elegir nada el panel habla español")
	}

	rec := pedir(t, "/?lang=en")
	if !strings.Contains(rec.Body.String(), "Services") {
		t.Error("?lang=en no cambia el idioma")
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "lang=en") {
		t.Error("?lang=en no persiste en la cookie")
	}

	// La cookie sola alcanza, sin query param.
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	w := httptest.NewRecorder()
	web.NuevoPanel(datosFalsos{}, zonaDePrueba).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "Services") {
		t.Error("la cookie lang=en no aplica")
	}

	// Y los textos armados en Go también cambian: el título de un evento.
	req = httptest.NewRequest("GET", "/events?horas=720", nil)
	req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
	w = httptest.NewRecorder()
	web.NuevoPanel(datosFalsos{}, zonaDePrueba).ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "the server rebooted") {
		t.Error("los títulos de eventos no se traducen")
	}
}

// /eventos era la única ruta en castellano; pasa a /events con redirect
// permanente para los marcadores viejos, query incluida.
func TestEventosRedirigeAEvents(t *testing.T) {
	rec := pedir(t, "/eventos?horas=720")
	if rec.Code != 301 {
		t.Fatalf("código = %d, quería 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/events?horas=720" {
		t.Errorf("Location = %q, quería /events?horas=720", loc)
	}
}

// Resolver cierra por el mismo camino que el motor —y por la cola derivada eso
// manda el aviso de cierre, que es la verdad—; archivar lo saca del panel.
func TestResolverYArchivarIncidentes(t *testing.T) {
	var cerrado, archivado int64
	d := espiaIncidentes{
		cerrar:   func(id int64) { cerrado = id },
		archivar: func(id int64) { archivado = id },
	}
	h := web.NuevoPanel(d, zonaDePrueba)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/incidents/7/resolve", nil))
	if w.Code != 303 || cerrado != 7 {
		t.Errorf("resolve: código=%d cerrado=%d, quería 303 y 7", w.Code, cerrado)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/incidents/9/archive", nil))
	if w.Code != 303 || archivado != 9 {
		t.Errorf("archive: código=%d archivado=%d, quería 303 y 9", w.Code, archivado)
	}

	// Un id que no es número es un 400, no un panic ni un redirect.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/incidents/basura/resolve", nil))
	if w.Code != 400 {
		t.Errorf("id inválido: código=%d, quería 400", w.Code)
	}
}

// El panel esconde los archivados; /eventos los sigue mostrando: es historia.
func TestElPanelEscondeArchivadosYEventosNo(t *testing.T) {
	if cuerpo := pedir(t, "/").Body.String(); strings.Contains(cuerpo, "service:viejo-archivado") {
		t.Error("el panel muestra un incidente archivado")
	}
	if cuerpo := pedir(t, "/events?horas=720&desde=2026-08-01T00:00&hasta=2026-08-31T00:00").Body.String(); !strings.Contains(cuerpo, "viejo-archivado") {
		t.Error("/eventos tiene que seguir mostrando la historia archivada")
	}
}

// Cada incidente ofrece la acción que corresponde: abierto → resolver;
// cerrado → archivar. Al revés no tiene sentido.
func TestLosBotonesDeIncidentes(t *testing.T) {
	cuerpo := pedir(t, "/").Body.String()
	if !strings.Contains(cuerpo, `action="/incidents/1/resolve"`) {
		t.Error("el incidente abierto no ofrece resolver")
	}
	if strings.Contains(cuerpo, `action="/incidents/1/archive"`) {
		t.Error("un incidente abierto no se puede archivar")
	}
	if !strings.Contains(cuerpo, `action="/incidents/2/archive"`) {
		t.Error("el incidente cerrado no ofrece archivar")
	}
	if strings.Contains(cuerpo, `action="/incidents/2/resolve"`) {
		t.Error("un incidente cerrado no se puede resolver de nuevo")
	}
}

// espiaIncidentes captura las acciones y delega el resto en datosFalsos.
type espiaIncidentes struct {
	datosFalsos
	cerrar   func(int64)
	archivar func(int64)
}

func (e espiaIncidentes) CerrarIncidente(id int64, _ time.Time) error {
	e.cerrar(id)
	return nil
}

func (e espiaIncidentes) ArchivarIncidente(id int64, _ time.Time) error {
	e.archivar(id)
	return nil
}

// Los carteles de ayuda se van (pedido del 25/08) y los filtros se aplican
// solos: no hay botón "buscar" ni "ver" que apretar.
func TestSinCartelDeAyudaYSinBotonBuscar(t *testing.T) {
	for _, ruta := range []string{"/logs", "/events?horas=720"} {
		cuerpo := pedir(t, ruta).Body.String()
		if strings.Contains(cuerpo, "La búsqueda es por palabra completa") ||
			strings.Contains(cuerpo, "Todo lo que pasó, en orden") {
			t.Errorf("%s todavía muestra el cartel de ayuda", ruta)
		}
		if strings.Contains(cuerpo, ">buscar</button>") || strings.Contains(cuerpo, ">ver</button>") {
			t.Errorf("%s todavía tiene botón de submit manual", ruta)
		}
		if !strings.Contains(cuerpo, "form.submit()") {
			t.Errorf("%s no auto-aplica los filtros", ruta)
		}
	}
	// El export sigue: es la única acción que no puede ser automática.
	if cuerpo := pedir(t, "/logs").Body.String(); !strings.Contains(cuerpo, "/logs/export") {
		t.Error("/logs perdió el botón de exportar")
	}
}

// El orden por defecto pone lo roto arriba: para eso existe el panel. Después,
// servicios por latencia y containers por RAM, los más pesados primero. El
// orden por columna del navegador queda para todo lo demás.
func TestServiciosYContainersOrdenanPorEstado(t *testing.T) {
	cuerpo := pedir(t, "/").Body.String()

	caido := strings.Index(cuerpo, ">sitio<")
	lento := strings.Index(cuerpo, ">lento<")
	rapido := strings.Index(cuerpo, ">comm-tool<")
	if caido < 0 || lento < 0 || rapido < 0 {
		t.Fatalf("faltan servicios en el panel: caido=%d lento=%d rapido=%d", caido, lento, rapido)
	}
	if !(caido < lento && lento < rapido) {
		t.Errorf("servicios: quería caído(%d) < lento(%d) < rápido(%d)", caido, lento, rapido)
	}

	muerto := strings.Index(cuerpo, "container=muerto")
	enfermo := strings.Index(cuerpo, "container=enfermo")
	pesado := strings.Index(cuerpo, "container=supabase-db")
	liviano := strings.Index(cuerpo, "container=comm-tool")
	if muerto < 0 || enfermo < 0 || pesado < 0 || liviano < 0 {
		t.Fatal("faltan containers en el panel")
	}
	if !(muerto < pesado && enfermo < pesado && pesado < liviano) {
		t.Errorf("containers: los rotos van arriba y después por RAM: muerto=%d enfermo=%d pesado=%d liviano=%d",
			muerto, enfermo, pesado, liviano)
	}
}

// La hora sola obliga a adivinar el día: con 30 días de rango, "02:00:42"
// puede ser cualquiera de treinta madrugadas. Fecha completa, DD/MM/YYYY.
func TestLasFechasLlevanDiaMesYAnio(t *testing.T) {
	// La línea falsa de logs es 12:00 UTC del 09/08 → 09:00 en Buenos Aires.
	if cuerpo := pedir(t, "/logs").Body.String(); !strings.Contains(cuerpo, "09/08/2026 09:00:00") {
		t.Error("/logs no muestra la fecha completa (esperaba 09/08/2026 09:00:00)")
	}
	// El evento falso es 05:00:31 UTC del 22/08 → 02:00:31 en Buenos Aires.
	if cuerpo := pedir(t, "/events?horas=720").Body.String(); !strings.Contains(cuerpo, "22/08/2026 02:00:31") {
		t.Error("/events no muestra la fecha completa")
	}
	// El incidente falso abrió el 09/08.
	if cuerpo := pedir(t, "/").Body.String(); !strings.Contains(cuerpo, "09/08/2026") {
		t.Error("el panel no muestra la fecha completa en incidentes")
	}
}

// El filtro es un toggle por ítem, no un "mínimo": WARN apagado con ERROR
// prendido no se puede decir con un piso.
func TestElFiltroDeNivelEsPorConjunto(t *testing.T) {
	var visto []string
	h := web.NuevoPanel(espiaNiveles{cb: func(ns []string) { visto = ns }}, zonaDePrueba)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/logs?nivel=TRACE&nivel=ERROR", nil))

	if !reflect.DeepEqual(visto, []string{"TRACE", "ERROR"}) {
		t.Errorf("niveles = %v, quería [TRACE ERROR]", visto)
	}
	// Y la vista pinta los cuatro pills con los elegidos marcados.
	cuerpo := w.Body.String()
	if !strings.Contains(cuerpo, `value="TRACE" checked`) || strings.Contains(cuerpo, `value="INFO" checked`) {
		t.Error("los toggles no reflejan la selección")
	}
}

// espiaNiveles captura el conjunto de niveles pedido al store.
type espiaNiveles struct {
	datosFalsos
	cb func([]string)
}

func (e espiaNiveles) BuscarLogs(texto, container string, niveles []string, desde, hasta time.Time, limite int) ([]model.LineaLog, error) {
	e.cb(niveles)
	return e.datosFalsos.BuscarLogs(texto, container, niveles, desde, hasta, limite)
}

// espia captura el rango con el que se consultó, y delega el resto en datosFalsos.
type espia struct {
	datosFalsos
	cb func(desde, hasta time.Time)
}

func (e espia) BuscarLogs(texto, container string, niveles []string, desde, hasta time.Time, limite int) ([]model.LineaLog, error) {
	e.cb(desde, hasta)
	return e.datosFalsos.BuscarLogs(texto, container, niveles, desde, hasta, limite)
}

// ── Tanda del 26/08/2026 ────────────────────────────────────────────────────

// espiaLimite captura el tope con el que se consultó el store.
type espiaLimite struct {
	datosFalsos
	cb func(int)
}

func (e espiaLimite) BuscarLogs(texto, container string, niveles []string, desde, hasta time.Time, limite int) ([]model.LineaLog, error) {
	e.cb(limite)
	return e.datosFalsos.BuscarLogs(texto, container, niveles, desde, hasta, limite)
}

// El tope de 500 recortaba una ventana de 24 h a menos de cinco horas cuando
// había un container ruidoso. Ahora se elige, y lo que se elige llega al store.
func TestElTopeElegidoLlegaAlStore(t *testing.T) {
	casos := []struct {
		ruta   string
		quiero int
	}{
		{"/logs", 5000},                   // default
		{"/logs?limite=25000", 25000},     // el máximo del selector
		{"/logs?limite=10000", 10000},     //
		{"/logs?limite=999999", 5000},     // fuera de la lista: cae al default
		{"/logs?limite=cualquiera", 5000}, // basura: idem
		{"/logs?limite=500", 5000},        // el tope viejo ya no es una opción
	}
	for _, c := range casos {
		var visto int
		h := web.NuevoPanel(espiaLimite{cb: func(n int) { visto = n }}, zonaDePrueba)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", c.ruta, nil))
		if visto != c.quiero {
			t.Errorf("%s: tope %d, quería %d", c.ruta, visto, c.quiero)
		}
	}
}

// El export nunca baja de su piso aunque la vista pida menos: es un archivo que
// se abre en otro lado y aguanta más que un navegador renderizando divs.
func TestElExportNoBajaDeSuPiso(t *testing.T) {
	var visto int
	h := web.NuevoPanel(espiaLimite{cb: func(n int) { visto = n }}, zonaDePrueba)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/logs/export?limite=5000", nil))
	if visto != 10000 {
		t.Errorf("tope del export = %d, quería 10000", visto)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/logs/export?limite=25000", nil))
	if visto != 25000 {
		t.Errorf("tope del export = %d, quería 25000: el selector le gana al piso", visto)
	}
}

// El poll del modo en vivo devuelve la hora YA formateada en la zona del panel.
// Si la formateara el navegador saldría la del cliente, y las líneas nuevas
// mostrarían una hora distinta a las de abajo, que las formatea el server.
func TestElPollEnVivoFormateaLaHoraEnLaZonaDelPanel(t *testing.T) {
	rec := pedir(t, "/api/logs/nuevos?cursor=10")

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	var got struct {
		Cursor int64 `json:"cursor"`
		Lineas []struct {
			Hora, Nivel, Container, Linea string
		} `json:"lineas"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v — %s", err, rec.Body.String())
	}
	if len(got.Lineas) != 1 {
		t.Fatalf("got = %+v, quería una línea", got.Lineas)
	}
	// La línea falsa ocurrió 12:00:30 UTC → 09:00:30 en Buenos Aires.
	if got.Lineas[0].Hora != "09/08/2026 09:00:30" {
		t.Errorf("hora = %q, quería 09/08/2026 09:00:30 (12:00:30 UTC en Buenos Aires)", got.Lineas[0].Hora)
	}
	if got.Cursor != 11 {
		t.Errorf("cursor = %d, quería 11: tiene que avanzar", got.Cursor)
	}
}

// El modo en vivo solo tiene sentido con ventana relativa: con desde/hasta
// puestos uno mira el pasado, y pegar líneas nuevas arriba mentiría sobre la
// ventana que pidió.
func TestElToggleEnVivoSoloApareceConVentanaRelativa(t *testing.T) {
	if !strings.Contains(pedir(t, "/logs").Body.String(), `id="vivo"`) {
		t.Error("/logs sin rango explícito tiene que ofrecer el modo en vivo")
	}
	conRango := pedir(t, "/logs?desde=2026-08-22T01:50&hasta=2026-08-22T02:10").Body.String()
	if strings.Contains(conRango, `id="vivo"`) {
		t.Error("con desde/hasta explícitos NO puede ofrecer el modo en vivo")
	}
}

// La columna de reinicios de la ventana sale de started_at. La de Docker sigue
// al lado porque es otra cosa: RestartCount no se mueve cuando un container se
// recrea, que es exactamente lo que pasó con cloudflared el 26/08/2026.
func TestLasDosColumnasDeReiniciosMuestranNumerosDistintos(t *testing.T) {
	cuerpo := pedir(t, "/").Body.String()

	// supabase-db: 3 reinicios en la ventana, RestartCount de Docker en 2.
	fila := cuerpo[strings.Index(cuerpo, "supabase-db"):]
	fila = fila[:strings.Index(fila, "</tr>")]
	if !strings.Contains(fila, ">3</td>") {
		t.Errorf("falta el 3 de la ventana en la fila de supabase-db:\n%s", fila)
	}
	if !strings.Contains(fila, ">2</td>") {
		t.Errorf("falta el 2 de RestartCount en la fila de supabase-db:\n%s", fila)
	}
}

// Archivar es "ya lo vi", no "que no haya pasado": el panel los esconde por
// default y hay un toggle para verlos.
func TestLosArchivadosSeEscondenPeroSePuedenVer(t *testing.T) {
	sinToggle := pedir(t, "/").Body.String()
	if strings.Contains(sinToggle, "service:viejo-archivado") {
		t.Error("el panel no puede mostrar incidentes archivados sin pedirlo")
	}
	if !strings.Contains(sinToggle, "archivados=1") {
		t.Error("falta el link para ver los archivados")
	}

	conToggle := pedir(t, "/?archivados=1").Body.String()
	if !strings.Contains(conToggle, "service:viejo-archivado") {
		t.Error("con ?archivados=1 el archivado tiene que aparecer")
	}
	// Y marcado como tal: sin la marca no se distingue del resto.
	fila := conToggle[strings.Index(conToggle, "service:viejo-archivado"):]
	if !strings.Contains(fila[:strings.Index(fila, "</tr>")], "archivado</span>") {
		t.Error("el incidente archivado tiene que verse marcado")
	}
}

// Resolver y archivar mandaban a "/" y te expulsaban de la vista en la que
// estabas — justo la de archivados, que es donde uno archiva de a varios.
func TestLasAccionesVuelvenADondeEstabas(t *testing.T) {
	casos := []struct {
		nombre, volver, quiero string
	}{
		{"vuelve a donde estaba", "/?horas=6&archivados=1", "/?horas=6&archivados=1"},
		{"sin volver cae al inicio", "", "/"},
		// "//otro.sitio" es una URL ABSOLUTA para el navegador: sin el filtro,
		// el redirect se convierte en un open redirect. Privado sigue siendo uno.
		{"rechaza un destino de afuera", "//jadd.com.ar/robo", "/"},
		{"rechaza una URL absoluta", "https://jadd.com.ar/robo", "/"},
	}
	for _, c := range casos {
		h := web.NuevoPanel(datosFalsos{}, zonaDePrueba)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/incidents/2/archive",
			strings.NewReader("volver="+url.QueryEscape(c.volver)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: código %d, quería 303", c.nombre, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != c.quiero {
			t.Errorf("%s: Location = %q, quería %q", c.nombre, got, c.quiero)
		}
	}
}

// El idioma SÍ cambiaba; lo que no se veía era el control. Los dos idiomas
// siempre visibles, con el activo marcado.
func TestElToggleDeIdiomaMuestraLosDosYCualEstaActivo(t *testing.T) {
	for _, c := range []struct{ ruta, activo, otro string }{
		{"/", "es", "en"},
		{"/?lang=en", "en", "es"},
	} {
		cuerpo := pedir(t, c.ruta).Body.String()
		nav := cuerpo[strings.Index(cuerpo, `class="idiomas"`):]
		nav = nav[:strings.Index(nav, "</span>")]

		for _, l := range []string{"ES", "EN"} {
			if !strings.Contains(nav, ">"+l+"<") {
				t.Errorf("%s: falta la opción %s en el toggle", c.ruta, l)
			}
		}
		// El activo es el que tiene la clase, y solo uno la tiene. Se mira
		// anchor por anchor: contar clases sueltas no diría CUÁL quedó marcada.
		var activos []string
		for _, a := range strings.Split(nav, "<a ")[1:] {
			for _, l := range []string{"es", "en"} {
				if strings.Contains(a, `'lang','`+l+`'`) && strings.Contains(a, `class="activo"`) {
					activos = append(activos, l)
				}
			}
		}
		if len(activos) != 1 || activos[0] != c.activo {
			t.Errorf("%s: marcados como activos %v, quería solo [%s]", c.ruta, activos, c.activo)
		}
	}
}

// "carga 0.48 / 0.55 / 0.54" sin decir qué es no se puede leer: son procesos,
// a 1/5/15 minutos, y se comparan contra la cantidad de núcleos.
func TestLaCargaDiceAQueIntervalosYContraCuantosNucleos(t *testing.T) {
	cuerpo := pedir(t, "/").Body.String()
	if !strings.Contains(cuerpo, "1m/5m/15m") {
		t.Error("la carga no dice a qué intervalos corresponde cada número")
	}
	if !strings.Contains(cuerpo, "vCPU") {
		t.Error("la carga no dice contra cuántos núcleos se compara")
	}
}

// La terminal y la pantalla NO viven acá: este proceso no pide contraseña y
// habla con el socket de Docker. El nav las ofrece como enlaces externos, y la
// URL sale de la config porque lleva la IP de tailnet.
func TestElNavOfreceLosEnlacesExternos(t *testing.T) {
	h := web.NuevoPanel(datosFalsos{}, zonaDePrueba,
		web.Enlace{Nombre: "terminal", URL: "https://ejemplo.invalid:9090"})

	for _, ruta := range []string{"/", "/logs", "/events"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", ruta, nil))
		cuerpo := rec.Body.String()
		if !strings.Contains(cuerpo, "https://ejemplo.invalid:9090") {
			t.Errorf("%s: falta el enlace externo en el nav", ruta)
		}
		if !strings.Contains(cuerpo, `rel="noopener"`) {
			t.Errorf("%s: el enlace externo tiene que llevar rel=noopener", ruta)
		}
	}

	// Sin enlaces configurados el nav no inventa ninguno. Se busca la clase y
	// no la palabra: "externo" aparece igual en el CSS del nav, que va siempre.
	if strings.Contains(pedir(t, "/").Body.String(), `class="externo"`) {
		t.Error("sin enlaces en la config el nav no puede mostrar ninguno")
	}
}
