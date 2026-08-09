package host_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/collector/host"
)

// procFalso arma un directorio con la misma forma que /proc, para que el
// colector se pueda testear sin tocar el /proc de verdad.
func procFalso(t *testing.T, stat string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	archivos := map[string]string{
		"stat":    stat,
		"meminfo": meminfoFixture,
		"loadavg": "0.41 0.60 0.53 2/512 12345\n",
		"uptime":  "13620.50 98765.43\n",
		"net/dev": netDevFixture,
	}
	for nombre, contenido := range archivos {
		if err := os.WriteFile(filepath.Join(dir, nombre), []byte(contenido), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPrimeraMuestraNoTieneCPU(t *testing.T) {
	c := host.NewCollector(procFalso(t, statFixture), "/", clock.NewFake(time.Now()))

	got, err := c.Sample()
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	// Sin lectura anterior no hay delta, y sin delta no hay porcentaje.
	// Reportar 0 es correcto; inventar un número, no.
	if got.CPUPctAvg != 0 {
		t.Errorf("CPUPctAvg de la primera muestra = %v, quería 0", got.CPUPctAvg)
	}
	if got.MemTotalBytes != 12000000*1024 {
		t.Errorf("MemTotalBytes = %d, quería %d", got.MemTotalBytes, 12000000*1024)
	}
	if got.Load1 != 0.41 {
		t.Errorf("Load1 = %v, quería 0.41", got.Load1)
	}
	if got.Uptime != 13620500*time.Millisecond {
		t.Errorf("Uptime = %v, quería %v", got.Uptime, 13620500*time.Millisecond)
	}
	if got.NetRxBytes != 1300 {
		t.Errorf("NetRxBytes = %d, quería 1300", got.NetRxBytes)
	}
}

func TestSegundaMuestraCalculaCPUContraLaPrimera(t *testing.T) {
	dir := procFalso(t, statFixture)
	reloj := clock.NewFake(time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	c := host.NewCollector(dir, "/", reloj)

	if _, err := c.Sample(); err != nil {
		t.Fatalf("primera Sample: %v", err)
	}

	// Segunda lectura: total 2000 (era 1000) e idle+iowait 1640 (era 840).
	// Delta total 1000, delta de ocio 800 → 200 de trabajo sobre 1000 = 20%.
	segundo := "cpu  300 20 30 1600 40 0 10 0 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(segundo), 0o644); err != nil {
		t.Fatal(err)
	}
	reloj.Advance(15 * time.Second)

	got, err := c.Sample()
	if err != nil {
		t.Fatalf("segunda Sample: %v", err)
	}
	if got.CPUPctAvg != 20 {
		t.Errorf("CPUPctAvg = %v, quería 20", got.CPUPctAvg)
	}
	if !got.TS.Equal(reloj.Now()) {
		t.Errorf("TS = %v, quería %v", got.TS, reloj.Now())
	}
}

func TestSampleFallaSiFaltaUnArchivo(t *testing.T) {
	c := host.NewCollector(t.TempDir(), "/", clock.NewFake(time.Now()))
	if _, err := c.Sample(); err == nil {
		t.Fatal("quería error con un /proc vacío, no hubo")
	}
}
