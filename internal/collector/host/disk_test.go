package host_test

import (
	"testing"

	"github.com/juanandresdavila/server-status/internal/collector/host"
)

// statfs habla con el kernel, así que este test mide el disco de verdad de la
// máquina donde corre. No se puede afirmar un número: se afirman las relaciones
// que tienen que valer siempre.
func TestDiskUsageDevuelveValoresCoherentes(t *testing.T) {
	got, err := host.DiskUsage("/")
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if got.TotalBytes == 0 {
		t.Fatal("TotalBytes = 0, un filesystem montado no puede tener tamaño cero")
	}
	if got.UsedBytes > got.TotalBytes {
		t.Errorf("UsedBytes (%d) > TotalBytes (%d)", got.UsedBytes, got.TotalBytes)
	}
}

func TestDiskUsageRutaInexistenteDaError(t *testing.T) {
	if _, err := host.DiskUsage("/no/existe/esta/ruta"); err == nil {
		t.Fatal("quería error con una ruta inexistente, no hubo")
	}
}
