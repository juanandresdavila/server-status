package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/config"
)

func escribir(t *testing.T, contenido string) string {
	t.Helper()
	ruta := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(ruta, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}
	return ruta
}

func TestLoadAplicaDefaults(t *testing.T) {
	c, err := config.Load(escribir(t, "base: /tmp/x.db\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Base != "/tmp/x.db" {
		t.Errorf("Base = %q", c.Base)
	}
	if c.Proc != "/proc" {
		t.Errorf("Proc = %q, quería /proc por default", c.Proc)
	}
	if c.Disco != "/" {
		t.Errorf("Disco = %q, quería / por default", c.Disco)
	}
	if c.IntervaloMuestreo != 15*time.Second {
		t.Errorf("IntervaloMuestreo = %v, quería 15s", c.IntervaloMuestreo)
	}
	if c.IntervaloPersistencia != time.Minute {
		t.Errorf("IntervaloPersistencia = %v, quería 1m", c.IntervaloPersistencia)
	}
}

func TestLoadRespetaLoQueEstaEscrito(t *testing.T) {
	c, err := config.Load(escribir(t, "base: /var/lib/x.db\nproc: /fake/proc\nintervalo_muestreo: 5s\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Proc != "/fake/proc" {
		t.Errorf("Proc = %q", c.Proc)
	}
	if c.IntervaloMuestreo != 5*time.Second {
		t.Errorf("IntervaloMuestreo = %v, quería 5s", c.IntervaloMuestreo)
	}
}

func TestLoadSinBaseFalla(t *testing.T) {
	_, err := config.Load(escribir(t, "proc: /proc\n"))
	if err == nil {
		t.Fatal("quería error sin 'base', no hubo")
	}
}

func TestLoadArchivoInexistenteFalla(t *testing.T) {
	if _, err := config.Load("/no/existe.yaml"); err == nil {
		t.Fatal("quería error con archivo inexistente, no hubo")
	}
}

func TestLoadYamlInvalidoFalla(t *testing.T) {
	if _, err := config.Load(escribir(t, "base: [esto no cierra\n")); err == nil {
		t.Fatal("quería error con YAML roto, no hubo")
	}
}

func TestLoadDefaultsDeDocker(t *testing.T) {
	c, err := config.Load(escribir(t, "base: /tmp/x.db\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DockerSocket != "/var/run/docker.sock" {
		t.Errorf("DockerSocket = %q", c.DockerSocket)
	}
	if c.DockerConcurrencia != 8 {
		t.Errorf("DockerConcurrencia = %d, quería 8", c.DockerConcurrencia)
	}
}
