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

func TestLoadLeeLosServicios(t *testing.T) {
	yaml := `
base: /tmp/x.db
servicios:
  - nombre: comm-tool
    probe: https://comm.example.com/health
    containers: [comm-tool, comm-tool-db]
  - nombre: sitio
    probe: https://example.com/
`
	c, err := config.Load(escribir(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Servicios) != 2 {
		t.Fatalf("hay %d servicios, quería 2", len(c.Servicios))
	}
	if c.Servicios[0].Nombre != "comm-tool" {
		t.Errorf("Nombre = %q", c.Servicios[0].Nombre)
	}
	if len(c.Servicios[0].Containers) != 2 {
		t.Errorf("Containers = %v", c.Servicios[0].Containers)
	}
	if len(c.Servicios[1].Containers) != 0 {
		t.Errorf("un servicio sin containers debería quedar con la lista vacía")
	}
}

func TestServicioSinNombreFalla(t *testing.T) {
	_, err := config.Load(escribir(t, "base: /tmp/x.db\nservicios:\n  - probe: https://example.com/\n"))
	if err == nil {
		t.Fatal("quería error con un servicio sin nombre, no hubo")
	}
}

func TestServicioSinProbeFalla(t *testing.T) {
	_, err := config.Load(escribir(t, "base: /tmp/x.db\nservicios:\n  - nombre: x\n"))
	if err == nil {
		t.Fatal("quería error con un servicio sin probe, no hubo")
	}
}

// Dos servicios con el mismo nombre harían que el sujeto del incidente
// ('service:x') sea ambiguo y que uno pise al otro en la base.
func TestServiciosConNombreRepetidoFalla(t *testing.T) {
	yaml := `
base: /tmp/x.db
servicios:
  - nombre: x
    probe: https://a.example.com/
  - nombre: x
    probe: https://b.example.com/
`
	if _, err := config.Load(escribir(t, yaml)); err == nil {
		t.Fatal("quería error con nombres repetidos, no hubo")
	}
}

func TestProbeTimeoutPorDefecto(t *testing.T) {
	c, err := config.Load(escribir(t, "base: /tmp/x.db\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ProbeTimeout != 10*time.Second {
		t.Errorf("ProbeTimeout = %v, quería 10s", c.ProbeTimeout)
	}
}

func TestDefaultsDeAvisos(t *testing.T) {
	c, err := config.Load(escribir(t, "base: /tmp/x.db\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.TelegramAPI != "https://api.telegram.org" {
		t.Errorf("TelegramAPI = %q", c.TelegramAPI)
	}
	if c.HoraResumen != 8 {
		t.Errorf("HoraResumen = %d, quería 8", c.HoraResumen)
	}
	if c.Zona != "America/Argentina/Buenos_Aires" {
		t.Errorf("Zona = %q", c.Zona)
	}
}

// La zona horaria se valida al cargar: una zona inválida haría que el resumen
// salga a una hora al azar, y eso se descubriría recién al día siguiente.
func TestZonaInvalidaFalla(t *testing.T) {
	_, err := config.Load(escribir(t, "base: /tmp/x.db\nzona: No/Existe\n"))
	if err == nil {
		t.Fatal("quería error con una zona inválida, no hubo")
	}
}

// La config guarda el NOMBRE de la variable, nunca la apikey: este archivo se
// copia del ejemplo, que vive en un repo público.
func TestServicioLeeElNombreDeLaVariableDeLaAPIKey(t *testing.T) {
	c, err := config.Load(escribir(t, `base: /tmp/x.db
servicios:
  - nombre: study-master
    probe: https://ejemplo.invalid/auth/v1/health
    apikey_env: SUPABASE_SM_ANON_KEY
  - nombre: sitio
    probe: https://ejemplo.invalid/
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Servicios[0].APIKeyEnv; got != "SUPABASE_SM_ANON_KEY" {
		t.Errorf("APIKeyEnv = %q", got)
	}
	if got := c.Servicios[1].APIKeyEnv; got != "" {
		t.Errorf("APIKeyEnv = %q en un servicio que no la declara, quería vacío", got)
	}
}
