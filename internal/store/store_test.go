package store_test

import (
	"path/filepath"
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
	if v != 2 {
		t.Errorf("versión = %d, quería 2", v)
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
