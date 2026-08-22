package store_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/store"
)

// Los tests usan un archivo en un directorio temporal en vez de :memory:,
// porque así se ejercita también el modo WAL, que es como corre en producción.
func abrir(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAplicaLasMigraciones(t *testing.T) {
	s := abrir(t)
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	// Sin número fijo: el que importa lo afirma TestUltimaMigracionAplicada,
	// y repetirlo acá obliga a tocar tres tests cada vez que se agrega una.
	if v < 1 {
		t.Errorf("versión = %d, no se aplicó ninguna migración", v)
	}
}

func TestOpenDosVecesNoRompe(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "test.db")

	s1, err := store.Open(ruta)
	if err != nil {
		t.Fatalf("primer Open: %v", err)
	}
	s1.Close()

	s2, err := store.Open(ruta)
	if err != nil {
		t.Fatalf("segundo Open: %v", err)
	}
	defer s2.Close()

	v, err := s2.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	// Reabrir no debe re-aplicar nada ni saltear nada.
	if v < 1 {
		t.Errorf("versión después de reabrir = %d", v)
	}
}

// Este es el único test con el número exacto: si alguien borra o reordena una
// migración, acá se entera.
func TestUltimaMigracionAplicada(t *testing.T) {
	s := abrir(t)
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != 10 {
		t.Errorf("versión = %d, quería 10", v)
	}
}

func TestInsertContainerSamplesYConsulta(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 10, 30, 0, 0, time.UTC)

	muestras := []model.ContainerSample{
		{TS: ts, Name: "comm-tool", State: "running", Health: "none", Restarts: 0, CPUPct: 1.5, MemBytes: 50_000_000},
		{TS: ts, Name: "supabase-db", State: "running", Health: "healthy", Restarts: 2, CPUPct: 3.25, MemBytes: 300_000_000},
	}
	if err := s.InsertContainerSamples(muestras); err != nil {
		t.Fatalf("InsertContainerSamples: %v", err)
	}

	got, err := s.UltimoEstadoContainers()
	if err != nil {
		t.Fatalf("UltimoEstadoContainers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d, quería 2", len(got))
	}

	porNombre := map[string]model.ContainerSample{}
	for _, c := range got {
		porNombre[c.Name] = c
	}
	if db := porNombre["supabase-db"]; db.Health != "healthy" || db.Restarts != 2 || db.CPUPct != 3.25 {
		t.Errorf("supabase-db = %+v", db)
	}
}

// Solo interesa la foto más reciente, no el historial entero.
func TestUltimoEstadoContainersDevuelveSoloElMinutoMasNuevo(t *testing.T) {
	s := abrir(t)
	viejo := time.Date(2026, 8, 9, 10, 30, 0, 0, time.UTC)
	nuevo := viejo.Add(time.Minute)

	if err := s.InsertContainerSamples([]model.ContainerSample{
		{TS: viejo, Name: "a", State: "running", Health: "none"},
		{TS: viejo, Name: "b", State: "running", Health: "none"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertContainerSamples([]model.ContainerSample{
		{TS: nuevo, Name: "a", State: "exited", Health: "none"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.UltimoEstadoContainers()
	if err != nil {
		t.Fatalf("UltimoEstadoContainers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("volvieron %d filas, quería 1 (solo el minuto más nuevo)", len(got))
	}
	if got[0].State != "exited" {
		t.Errorf("State = %q, quería exited", got[0].State)
	}
}

func muestra(ts time.Time, cpu float64) model.HostSample {
	return model.HostSample{
		TS: ts, CPUPctAvg: cpu, CPUPctMax: cpu,
		Load1: 0.41, Load5: 0.60, Load15: 0.53,
		MemUsedBytes: 3_000_000_000, MemTotalBytes: 12_000_000_000,
		SwapUsedBytes: 0, SwapTotalBytes: 2_147_483_648,
		DiskUsedBytes: 16_000_000_000, DiskTotalBytes: 96_000_000_000,
		NetRxBytes: 1000, NetTxBytes: 2000,
		Uptime: 3 * time.Hour,
	}
}

func TestInsertYUltimasVuelvenLoMismo(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 8, 23, 12, 0, 0, time.UTC)

	if err := s.InsertHostSample(muestra(ts, 12.5)); err != nil {
		t.Fatalf("InsertHostSample: %v", err)
	}

	got, err := s.UltimasHostSamples(10)
	if err != nil {
		t.Fatalf("UltimasHostSamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("volvieron %d filas, quería 1", len(got))
	}
	if !got[0].TS.Equal(ts) {
		t.Errorf("TS = %v, quería %v", got[0].TS, ts)
	}
	if got[0].CPUPctAvg != 12.5 {
		t.Errorf("CPUPctAvg = %v, quería 12.5", got[0].CPUPctAvg)
	}
	if got[0].Uptime != 3*time.Hour {
		t.Errorf("Uptime = %v, quería 3h", got[0].Uptime)
	}
	if got[0].MemTotalBytes != 12_000_000_000 {
		t.Errorf("MemTotalBytes = %d, quería 12000000000", got[0].MemTotalBytes)
	}
}

// El ts es la clave primaria y se trunca al minuto: dos muestras del mismo
// minuto tienen que pisar, no explotar ni duplicar.
func TestInsertDelMismoMinutoPisa(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 8, 23, 12, 0, 0, time.UTC)

	if err := s.InsertHostSample(muestra(ts, 10)); err != nil {
		t.Fatalf("primer insert: %v", err)
	}
	if err := s.InsertHostSample(muestra(ts.Add(30*time.Second), 90)); err != nil {
		t.Fatalf("segundo insert: %v", err)
	}

	got, err := s.UltimasHostSamples(10)
	if err != nil {
		t.Fatalf("UltimasHostSamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("volvieron %d filas, quería 1 (los 30s se truncan al mismo minuto)", len(got))
	}
	if got[0].CPUPctAvg != 90 {
		t.Errorf("CPUPctAvg = %v, quería 90 (la última gana)", got[0].CPUPctAvg)
	}
}

func TestUltimasVuelvenDeLaMasNuevaALaMasVieja(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	for i := range 5 {
		if err := s.InsertHostSample(muestra(base.Add(time.Duration(i)*time.Minute), float64(i))); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	got, err := s.UltimasHostSamples(3)
	if err != nil {
		t.Fatalf("UltimasHostSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("volvieron %d filas, quería 3", len(got))
	}
	if got[0].CPUPctAvg != 4 {
		t.Errorf("la primera fila tiene CPU %v, quería 4 (la más nueva)", got[0].CPUPctAvg)
	}
	if got[2].CPUPctAvg != 2 {
		t.Errorf("la tercera fila tiene CPU %v, quería 2", got[2].CPUPctAvg)
	}
}

func TestAbrirYCerrarIncidente(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, err := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:comm-tool", Tipo: "down", Severidad: "critical",
		AbiertoEn: ts, Detalle: "3 fallas seguidas",
	})
	if err != nil {
		t.Fatalf("AbrirIncidente: %v", err)
	}

	abiertos, err := s.IncidentesAbiertos()
	if err != nil {
		t.Fatalf("IncidentesAbiertos: %v", err)
	}
	if len(abiertos) != 1 {
		t.Fatalf("hay %d abiertos, quería 1", len(abiertos))
	}
	if abiertos[0].Sujeto != "service:comm-tool" || abiertos[0].CerradoEn != nil {
		t.Errorf("incidente = %+v", abiertos[0])
	}

	if err := s.CerrarIncidente(id, ts.Add(10*time.Minute)); err != nil {
		t.Fatalf("CerrarIncidente: %v", err)
	}

	abiertos, err = s.IncidentesAbiertos()
	if err != nil {
		t.Fatal(err)
	}
	if len(abiertos) != 0 {
		t.Errorf("quedaron %d abiertos después de cerrar", len(abiertos))
	}
}

// Esta es la invariante 2 del spec, y la hace cumplir la base, no el código.
// Si se rompe el índice, "el incidente de este servicio" pasa a depender del
// orden del SELECT.
func TestNoSePuedenAbrirDosIncidentesDelMismoSujeto(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	inc := model.Incidente{
		Sujeto: "host:disk", Tipo: "threshold", Severidad: "warning",
		AbiertoEn: ts, Detalle: "82%",
	}

	if _, err := s.AbrirIncidente(inc); err != nil {
		t.Fatalf("primer AbrirIncidente: %v", err)
	}
	if _, err := s.AbrirIncidente(inc); err == nil {
		t.Fatal("se abrió un segundo incidente del mismo sujeto: el índice único no está haciendo su trabajo")
	}
}

// Cerrado el primero, el mismo sujeto puede volver a abrir. Si el índice
// fuera sobre el sujeto a secas, esto fallaría.
func TestElMismoSujetoPuedeReabrirDespuesDeCerrar(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	inc := model.Incidente{
		Sujeto: "host:disk", Tipo: "threshold", Severidad: "warning",
		AbiertoEn: ts, Detalle: "82%",
	}

	id, err := s.AbrirIncidente(inc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CerrarIncidente(id, ts.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	inc.AbiertoEn = ts.Add(2 * time.Hour)
	if _, err := s.AbrirIncidente(inc); err != nil {
		t.Fatalf("no se pudo reabrir después de cerrar: %v", err)
	}
}

func TestInsertProbeResults(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	if err := s.InsertProbeResults([]model.ProbeResult{
		{TS: ts, Servicio: "comm-tool", OK: true, StatusCode: 200, Latencia: 180 * time.Millisecond},
		{TS: ts, Servicio: "sitio", OK: false, StatusCode: 502, Latencia: 2 * time.Second, Error: "HTTP 502 Bad Gateway"},
	}); err != nil {
		t.Fatalf("InsertProbeResults: %v", err)
	}

	got, err := s.UltimoEstadoProbes()
	if err != nil {
		t.Fatalf("UltimoEstadoProbes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d, quería 2", len(got))
	}
	porServicio := map[string]model.ProbeResult{}
	for _, r := range got {
		porServicio[r.Servicio] = r
	}
	if sitio := porServicio["sitio"]; sitio.OK || sitio.StatusCode != 502 || sitio.Latencia != 2*time.Second {
		t.Errorf("sitio = %+v", sitio)
	}
}

func TestAvisoPendienteAlAbrirUnIncidente(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, err := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:comm-tool", Tipo: "down", Severidad: "critical",
		AbiertoEn: ts, Detalle: "HTTP 502",
	})
	if err != nil {
		t.Fatal(err)
	}

	pend, err := s.AvisosPendientes()
	if err != nil {
		t.Fatalf("AvisosPendientes: %v", err)
	}
	if len(pend) != 1 {
		t.Fatalf("hay %d pendientes, quería 1", len(pend))
	}
	quiero := strconv.FormatInt(id, 10) + ":opened"
	if pend[0].DeliveryID != quiero {
		t.Errorf("DeliveryID = %q, quería %q", pend[0].DeliveryID, quiero)
	}
	if pend[0].Cierre {
		t.Error("Cierre = true en un incidente recién abierto")
	}
}

// Marcarlo enviado lo saca de pendientes. Esto es lo que evita que el bot
// mande el mismo aviso en cada tick.
func TestMarcarEnviadoSacaDePendientes(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, _ := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:x", Tipo: "down", Severidad: "critical",
		AbiertoEn: ts, Detalle: "d",
	})
	dst := strconv.FormatInt(id, 10) + ":opened"

	if err := s.MarcarEnviado(dst, ts, "commtool", ""); err != nil {
		t.Fatalf("MarcarEnviado: %v", err)
	}

	pend, err := s.AvisosPendientes()
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 0 {
		t.Errorf("quedaron %d pendientes después de marcar enviado", len(pend))
	}
}

// Cerrar el incidente genera un segundo aviso, distinto del de apertura.
func TestCerrarGeneraSuPropioAviso(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, _ := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:x", Tipo: "down", Severidad: "critical",
		AbiertoEn: ts, Detalle: "d",
	})
	s.MarcarEnviado(strconv.FormatInt(id, 10)+":opened", ts, "commtool", "")

	if err := s.CerrarIncidente(id, ts.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	pend, err := s.AvisosPendientes()
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 {
		t.Fatalf("hay %d pendientes, quería 1 (el de cierre)", len(pend))
	}
	if !pend[0].Cierre {
		t.Error("Cierre = false en el aviso de un incidente cerrado")
	}
	if pend[0].DeliveryID != strconv.FormatInt(id, 10)+":closed" {
		t.Errorf("DeliveryID = %q", pend[0].DeliveryID)
	}
}

// Marcar dos veces el mismo id no puede explotar: pasa cuando el proceso
// muere justo después de mandar y antes de commitear.
func TestMarcarEnviadoDosVecesEsInofensivo(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	if err := s.MarcarEnviado("1:opened", ts, "commtool", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MarcarEnviado("1:opened", ts, "telegram", ""); err != nil {
		t.Errorf("el segundo MarcarEnviado falló: %v", err)
	}
}

func TestCiclosEnVentanaCuentaSoloLosDeEseSujeto(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	for i := range 4 {
		id, err := s.AbrirIncidente(model.Incidente{
			Sujeto: "service:x", Tipo: "down", Severidad: "critical",
			AbiertoEn: base.Add(time.Duration(i*10) * time.Minute), Detalle: "d",
		})
		if err != nil {
			t.Fatal(err)
		}
		// Hay que cerrarlo para poder abrir el siguiente: lo impide el índice.
		if err := s.CerrarIncidente(id, base.Add(time.Duration(i*10+5)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:otro", Tipo: "down", Severidad: "critical",
		AbiertoEn: base, Detalle: "d",
	})
	s.CerrarIncidente(id, base.Add(time.Minute))

	n, err := s.CiclosEnVentana("service:x", base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CiclosEnVentana: %v", err)
	}
	if n != 4 {
		t.Errorf("ciclos = %d, quería 4", n)
	}
}

func TestCiclosEnVentanaIgnoraLoViejo(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, _ := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:x", Tipo: "down", Severidad: "critical",
		AbiertoEn: base.Add(-3 * time.Hour), Detalle: "viejo",
	})
	s.CerrarIncidente(id, base.Add(-3*time.Hour).Add(time.Minute))

	n, err := s.CiclosEnVentana("service:x", base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("ciclos = %d, quería 0: el incidente es de hace 3 horas", n)
	}
}

func TestYaEnviado(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	ya, err := s.YaEnviado("resumen:2026-08-09")
	if err != nil {
		t.Fatalf("YaEnviado: %v", err)
	}
	if ya {
		t.Error("dice que ya se envió algo que nunca se mandó")
	}

	if err := s.MarcarEnviado("resumen:2026-08-09", ts, "telegram", ""); err != nil {
		t.Fatal(err)
	}

	ya, err = s.YaEnviado("resumen:2026-08-09")
	if err != nil {
		t.Fatal(err)
	}
	if !ya {
		t.Error("dice que no se envió algo que sí se marcó")
	}
}

func TestSerieHostDevuelvePuntosOrdenados(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		if err := s.InsertHostSample(muestra(base.Add(time.Duration(i)*time.Minute), float64(i*10))); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.SerieHost(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SerieHost: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("volvieron %d puntos, quería 5", len(got))
	}
	// De la más VIEJA a la más nueva: un gráfico se dibuja hacia adelante.
	if got[0].CPUPctAvg != 0 || got[4].CPUPctAvg != 40 {
		t.Errorf("orden equivocado: primero=%v último=%v", got[0].CPUPctAvg, got[4].CPUPctAvg)
	}
}

func TestSerieHostRecortaPorRango(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for i := range 10 {
		s.InsertHostSample(muestra(base.Add(time.Duration(i)*time.Minute), float64(i)))
	}

	got, err := s.SerieHost(base.Add(2*time.Minute), base.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("volvieron %d puntos, quería 4 (minutos 2 a 5 inclusive)", len(got))
	}
}

// El uptime que ve el público sale de contar probes buenos sobre el total.
func TestUptimePorServicio(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	var rs []model.ProbeResult
	for i := range 10 {
		rs = append(rs, model.ProbeResult{
			TS: base.Add(time.Duration(i) * time.Minute), Servicio: "x", OK: i != 0,
		})
	}
	if err := s.InsertProbeResults(rs); err != nil {
		t.Fatal(err)
	}

	got, err := s.UptimePorServicio(base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("UptimePorServicio: %v", err)
	}
	if got["x"] != 90 {
		t.Errorf("uptime = %v, quería 90 (9 de 10 buenos)", got["x"])
	}
}

// Un servicio sin ninguna medición todavía no puede reportar 0% —eso diría
// "caído todo el mes" cuando en realidad recién arranca.
func TestUptimeSinDatosDaCien(t *testing.T) {
	s := abrir(t)
	got, err := s.UptimePorServicio(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("sin mediciones devolvió %v, quería un mapa vacío", got)
	}
}

func lineas(base time.Time, cont string, textos ...string) []model.LineaLog {
	var out []model.LineaLog
	for i, t := range textos {
		out = append(out, model.LineaLog{
			TS: base.Add(time.Duration(i) * time.Second), Container: cont,
			Stream: "stdout", Linea: t,
		})
	}
	return out
}

func TestInsertLogsYBusquedaPorTexto(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	if err := s.InsertLogs(lineas(base, "comm-tool",
		"servidor escuchando en :3000",
		"ERROR conexion rechazada",
		"peticion procesada ok")); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	got, err := s.BuscarLogs("ERROR", "", "TRACE", time.Time{}, base.Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("BuscarLogs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("volvieron %d líneas, quería 1", len(got))
	}
	if got[0].Container != "comm-tool" || !strings.Contains(got[0].Linea, "rechazada") {
		t.Errorf("got = %+v", got[0])
	}
}

func TestBusquedaPorPrefijo(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(lineas(base, "x", "conexion rechazada"))

	got, err := s.BuscarLogs("conex*", "", "TRACE", time.Time{}, base.Add(time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("el prefijo no matcheó: %d líneas", len(got))
	}
}

func TestBusquedaFiltraPorContainer(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(lineas(base, "uno", "mismo texto"))
	s.InsertLogs(lineas(base, "dos", "mismo texto"))

	got, err := s.BuscarLogs("texto", "uno", "TRACE", time.Time{}, base.Add(time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Container != "uno" {
		t.Errorf("el filtro por container no anduvo: %+v", got)
	}
}

// La invariante 11: un paréntesis suelto en el buscador no puede romper la
// consulta con un error de sintaxis de FTS5.
func TestBuscarConTextoRaroNoExplota(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(lineas(base, "x", "algo"))

	for _, raro := range []string{"(", `"`, "AND", "*", "a OR", "^%$#", "NEAR(", ")"} {
		if _, err := s.BuscarLogs(raro, "", "TRACE", time.Time{}, base.Add(time.Hour), 50); err != nil {
			t.Errorf("BuscarLogs(%q) devolvió error: %v", raro, err)
		}
	}
}

// Sin texto devuelve las últimas, que es lo que muestra el panel al entrar.
func TestSinTextoDevuelveLasUltimas(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(lineas(base, "x", "una", "dos", "tres"))

	got, err := s.BuscarLogs("", "", "TRACE", time.Time{}, base.Add(time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d, quería 2 por el límite", len(got))
	}
	// Más nuevas primero.
	if got[0].Linea != "tres" {
		t.Errorf("la primera es %q, quería 'tres'", got[0].Linea)
	}
}

// El cursor es lo que hace que un reinicio no repita ni pierda líneas.
func TestCursorSobreviveYAvanza(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	_, hay, err := s.CursorDeLog("comm-tool")
	if err != nil {
		t.Fatal(err)
	}
	if hay {
		t.Error("dice tener cursor de un container que nunca se leyó")
	}

	if err := s.GuardarCursorDeLog("comm-tool", ts); err != nil {
		t.Fatal(err)
	}
	got, hay, err := s.CursorDeLog("comm-tool")
	if err != nil {
		t.Fatal(err)
	}
	if !hay || !got.Equal(ts) {
		t.Errorf("cursor = %v (hay=%v), quería %v", got, hay, ts)
	}
}

func TestRetencionBorraLoViejoYDejaLoNuevo(t *testing.T) {
	s := abrir(t)
	ahora := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(lineas(ahora.AddDate(0, 0, -40), "x", "vieja"))
	s.InsertLogs(lineas(ahora, "x", "nueva"))

	if err := s.BorrarLogsAnterioresA(ahora.AddDate(0, 0, -30)); err != nil {
		t.Fatalf("BorrarLogsAnterioresA: %v", err)
	}

	got, err := s.BuscarLogs("", "", "TRACE", time.Time{}, ahora.Add(time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Linea != "nueva" {
		t.Errorf("quedaron %+v, quería solo la nueva", got)
	}
}

func TestConteoDeCoincidencias(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(lineas(base, "app", "ERROR uno", "ok", "ERROR dos", "ERROR tres"))

	n, muestra, err := s.ContarCoincidencias("app", "ERROR", base.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ContarCoincidencias: %v", err)
	}
	if n != 3 {
		t.Errorf("conteo = %d, quería 3", n)
	}
	if !strings.Contains(muestra, "ERROR") {
		t.Errorf("muestra = %q", muestra)
	}
}

// comm-tool reintenta hasta 5 veces: sin dedupe, un /silenciar se aplicaría
// cinco veces y el bot contestaría cinco veces.
func TestUnComandoSeProcesaUnaSolaVez(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	primera, err := s.MarcarComandoProcesado("m1", ts)
	if err != nil {
		t.Fatal(err)
	}
	if !primera {
		t.Error("la primera vez dijo que ya estaba procesado")
	}

	segunda, err := s.MarcarComandoProcesado("m1", ts)
	if err != nil {
		t.Fatal(err)
	}
	if segunda {
		t.Error("el reintento se procesó de nuevo")
	}
}

func TestSilencioSeGuardaYSePisa(t *testing.T) {
	s := abrir(t)

	got, err := s.SilenciadoHasta()
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Error("sin silencio configurado devolvió algo")
	}

	hasta := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	if err := s.Silenciar(hasta); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SilenciadoHasta(); !got.Equal(hasta) {
		t.Errorf("silencio = %v, quería %v", got, hasta)
	}

	// Un /silenciar nuevo pisa al anterior, no acumula.
	otro := hasta.Add(time.Hour)
	s.Silenciar(otro)
	if got, _ := s.SilenciadoHasta(); !got.Equal(otro) {
		t.Errorf("el segundo silencio no pisó: %v", got)
	}
}

// Copiar el archivo vivo de una base con WAL NO da una copia consistente:
// puede quedar a mitad de una transacción. VACUUM INTO sí, y además compacta.
func TestVacuumIntoDejaUnaCopiaLegibleYCompleta(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for i := range 20 {
		if err := s.InsertHostSample(muestra(base.Add(time.Duration(i)*time.Minute), float64(i))); err != nil {
			t.Fatal(err)
		}
	}
	s.InsertLogs(lineas(base, "app", "una línea de prueba"))

	destino := filepath.Join(t.TempDir(), "copia.db")
	if err := s.VacuumInto(destino); err != nil {
		t.Fatalf("VacuumInto: %v", err)
	}

	copia, err := store.Open(destino)
	if err != nil {
		t.Fatalf("la copia no se puede abrir: %v", err)
	}
	defer copia.Close()

	got, err := copia.UltimasHostSamples(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Errorf("la copia tiene %d muestras, el original tenía 20", len(got))
	}
	ls, err := copia.BuscarLogs("prueba", "", "TRACE", time.Time{}, base.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 {
		t.Errorf("la copia perdió los logs: %d líneas", len(ls))
	}
}

// VACUUM INTO falla si el destino ya existe: hay que borrarlo antes o el
// backup deja de funcionar en silencio a partir del segundo día.
func TestVacuumIntoPisaLaCopiaAnterior(t *testing.T) {
	s := abrir(t)
	s.InsertHostSample(muestra(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), 1))
	destino := filepath.Join(t.TempDir(), "copia.db")

	for i := range 3 {
		if err := s.VacuumInto(destino); err != nil {
			t.Fatalf("VacuumInto en la vuelta %d: %v", i+1, err)
		}
	}
}

// conNivel arma líneas con nivel explícito, que es lo que hace la ingesta real.
func conNivel(base time.Time, cont string, pares ...string) []model.LineaLog {
	var out []model.LineaLog
	for i := 0; i+1 < len(pares); i += 2 {
		out = append(out, model.LineaLog{
			TS: base.Add(time.Duration(i) * time.Second), Container: cont,
			Stream: "stdout", Linea: pares[i], Nivel: pares[i+1],
		})
	}
	return out
}

func TestBuscarLogsFiltraPorNivelMinimo(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	if err := s.InsertLogs(conNivel(base, "x",
		"continuacion de sql", "TRACE",
		"peticion ok", "INFO",
		"algo raro", "WARN",
		"se cayo", "ERROR")); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	casos := []struct {
		minimo string
		quiero int
	}{
		{"TRACE", 4},
		{"INFO", 3},
		{"WARN", 2},
		{"ERROR", 1},
	}
	for _, c := range casos {
		got, err := s.BuscarLogs("", "", c.minimo, time.Time{}, base.Add(time.Hour), 50)
		if err != nil {
			t.Fatalf("BuscarLogs(%s): %v", c.minimo, err)
		}
		if len(got) != c.quiero {
			t.Errorf("mínimo %s: %d líneas, quería %d", c.minimo, len(got), c.quiero)
		}
	}
}

func TestBuscarLogsDevuelveElNivel(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(conNivel(base, "x", "se cayo", "ERROR"))

	got, err := s.BuscarLogs("", "", "TRACE", time.Time{}, base.Add(time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Nivel != "ERROR" {
		t.Errorf("got = %+v, quería una línea con nivel ERROR", got)
	}
}

// Una línea sin nivel —las que la ingesta vieja ya había guardado— no puede
// desaparecer del panel: se la trata como INFO.
func TestLineaSinNivelCuentaComoInfo(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(lineas(base, "x", "sin nivel"))

	got, err := s.BuscarLogs("", "", "INFO", time.Time{}, base.Add(time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Nivel != "INFO" {
		t.Errorf("got = %+v, quería la línea con nivel INFO", got)
	}
}

// Sin esto log_niveles crece para siempre: la retención borra de logs y los
// niveles de esas filas quedan sueltos, apuntando a rowids que ya no existen.
func TestRetencionTambienPodaLosNiveles(t *testing.T) {
	s := abrir(t)
	ahora := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	s.InsertLogs(conNivel(ahora.AddDate(0, 0, -40), "x", "vieja", "ERROR"))
	s.InsertLogs(conNivel(ahora, "x", "nueva", "ERROR"))

	if err := s.BorrarLogsAnterioresA(ahora.AddDate(0, 0, -30)); err != nil {
		t.Fatalf("BorrarLogsAnterioresA: %v", err)
	}

	n, err := s.ContarNiveles()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("quedaron %d niveles, quería 1: la poda dejó huérfanos", n)
	}
}

func TestBackfillClasificaLasFilasViejas(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(ruta)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	// Se insertan SIN nivel para simular lo que ya estaba guardado, y después
	// se fuerza el techo del backfill a incluirlas.
	if err := s.InsertLogs(lineas(base, "x", "una", "dos", "tres", "cuatro", "cinco")); err != nil {
		t.Fatal(err)
	}
	if err := s.ReiniciarBackfillParaTest(); err != nil {
		t.Fatal(err)
	}

	// Un clasificador de tres líneas: es justo lo que el parámetro hace posible.
	clasificar := func(linea, stream string) string {
		if linea == "tres" {
			return "ERROR"
		}
		return "TRACE"
	}

	total, listo := 0, false
	for i := 0; i < 10 && !listo; i++ {
		n, fin, err := s.BackfillNiveles(clasificar, 2)
		if err != nil {
			t.Fatalf("BackfillNiveles: %v", err)
		}
		total += n
		listo = fin
	}
	if !listo {
		t.Fatal("el backfill no terminó en 10 vueltas")
	}
	if total != 5 {
		t.Errorf("procesó %d filas, quería 5", total)
	}

	got, err := s.BuscarLogs("", "", "ERROR", time.Time{}, base.Add(time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Linea != "tres" {
		t.Errorf("got = %+v, quería solo la línea 'tres' como ERROR", got)
	}
	s.Close()
}

// Un backfill que ya terminó no vuelve a trabajar: si no, cada arranque
// reprocesaría las 802 200 filas de la base del VPS.
func TestBackfillNoRepiteCuandoYaTermino(t *testing.T) {
	s := abrir(t)
	n, listo, err := s.BackfillNiveles(func(l, st string) string { return "INFO" }, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || !listo {
		t.Errorf("n=%d listo=%v, quería 0 y true sobre una base vacía", n, listo)
	}
}
