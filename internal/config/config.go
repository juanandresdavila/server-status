// Package config lee el YAML de configuración. Los secretos NO viven acá:
// vienen de variables de entorno, por el §12 del spec.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Base                  string        `yaml:"base"`
	Proc                  string        `yaml:"proc"`
	Disco                 string        `yaml:"disco"`
	IntervaloMuestreo     time.Duration `yaml:"intervalo_muestreo"`
	IntervaloPersistencia time.Duration `yaml:"intervalo_persistencia"`

	DockerSocket       string `yaml:"docker_socket"`
	DockerConcurrencia int    `yaml:"docker_concurrencia"`

	ProbeTimeout time.Duration `yaml:"probe_timeout"`
	Servicios    []Servicio    `yaml:"servicios"`

	// Los secretos NO están acá: vienen del entorno. Esto es solo la parte
	// no sensible de la configuración de avisos.
	TelegramAPI    string `yaml:"telegram_api"`
	CommToolURL    string `yaml:"comm_tool_url"`
	CommToolUserID string `yaml:"comm_tool_user_id"`
	HoraResumen    int    `yaml:"hora_resumen"`
	Zona           string `yaml:"zona"`

	// Caras web. PanelAddr en vacío apaga el panel; PortadaPath en vacío
	// apaga la portada. Que se puedan apagar por separado importa: son las
	// dos partes menos críticas y ninguna puede impedir que el monitor
	// recolecte y avise.
	PanelAddr   string `yaml:"panel_addr"`
	PortadaPath string `yaml:"portada_path"`
	URLPublica  string `yaml:"url_publica"`
	// WebhookAddr es donde comm-tool entrega los comandos. Va en un puerto
	// distinto al del panel a propósito: ver el comentario en main.go.
	WebhookAddr string `yaml:"webhook_addr"`

	// Logs. Patrón vacío apaga las alertas por patrón; retención en 0 usa 30 días.
	LogsPatron        string `yaml:"logs_patron"`
	LogsVentanaMin    int    `yaml:"logs_ventana_min"`
	LogsRetencionDias int    `yaml:"logs_retencion_dias"`

	// BackupPath es la copia consistente que se lleva el restic de la Mac
	// mini. Vacío la desactiva.
	BackupPath string `yaml:"backup_path"`

	// Enlaces son accesos externos que el panel OFRECE en el nav y no atiende:
	// la terminal de Cockpit, la pantalla de Guacamole. Salen de la config y no
	// del código por dos razones: la URL lleva la IP de tailnet —que no entra a
	// un repo público— y porque el día que uno de esos servicios no exista, la
	// forma de sacarlo del panel tiene que ser borrar una línea de config y no
	// recompilar.
	Enlaces []Enlace `yaml:"enlaces"`
}

// Enlace es un acceso externo del nav: un nombre y adónde va.
type Enlace struct {
	Nombre string `yaml:"nombre"`
	URL    string `yaml:"url"`
}

// Servicio es una cosa que se puede caer, con la URL que lo prueba y los
// containers que lo componen.
type Servicio struct {
	Nombre string `yaml:"nombre"`
	Probe  string `yaml:"probe"`
	// EstadoEsperado en 0 significa "cualquier 2xx o 3xx". Con un código
	// explícito, ese y solo ese cuenta como sano.
	EstadoEsperado int `yaml:"estado_esperado"`
	// APIKeyEnv es el NOMBRE de la variable de entorno con la apikey del
	// probe, nunca su valor: la config no lleva secretos (invariante 8) y
	// además este archivo se copia del ejemplo, que vive en un repo público.
	APIKeyEnv  string   `yaml:"apikey_env"`
	Containers []string `yaml:"containers"`
}

func Load(ruta string) (Config, error) {
	b, err := os.ReadFile(ruta)
	if err != nil {
		return Config{}, fmt.Errorf("leer la config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parsear la config: %w", err)
	}

	if c.Proc == "" {
		c.Proc = "/proc"
	}
	if c.Disco == "" {
		c.Disco = "/"
	}
	if c.IntervaloMuestreo == 0 {
		c.IntervaloMuestreo = 15 * time.Second
	}
	if c.IntervaloPersistencia == 0 {
		c.IntervaloPersistencia = time.Minute
	}
	if c.DockerSocket == "" {
		c.DockerSocket = "/var/run/docker.sock"
	}
	if c.DockerConcurrencia == 0 {
		c.DockerConcurrencia = 8
	}

	if c.ProbeTimeout == 0 {
		c.ProbeTimeout = 10 * time.Second
	}
	if c.TelegramAPI == "" {
		c.TelegramAPI = "https://api.telegram.org"
	}
	if c.HoraResumen == 0 {
		c.HoraResumen = 8
	}
	if c.LogsPatron == "" {
		c.LogsPatron = "error OR panic OR fatal"
	}
	if c.LogsVentanaMin == 0 {
		c.LogsVentanaMin = 5
	}
	if c.LogsRetencionDias == 0 {
		c.LogsRetencionDias = 30
	}
	if c.BackupPath == "" {
		c.BackupPath = "/var/lib/server-status/backup/status.db"
	}
	if c.Zona == "" {
		c.Zona = "America/Argentina/Buenos_Aires"
	}
	// Se valida al cargar: una zona inválida haría que el resumen salga a una
	// hora al azar, y eso se descubriría recién al día siguiente.
	if _, err := time.LoadLocation(c.Zona); err != nil {
		return Config{}, fmt.Errorf("zona horaria %q inválida: %w", c.Zona, err)
	}

	if c.Base == "" {
		return Config{}, errors.New("falta 'base' en la config: es la ruta del archivo SQLite")
	}

	vistos := map[string]bool{}
	for i, s := range c.Servicios {
		if s.Nombre == "" {
			return Config{}, fmt.Errorf("el servicio %d no tiene 'nombre'", i)
		}
		if s.Probe == "" {
			return Config{}, fmt.Errorf("el servicio %q no tiene 'probe'", s.Nombre)
		}
		// El nombre es la identidad del sujeto del incidente: repetirlo haría
		// que dos servicios compartan incidente sin que se note.
		if vistos[s.Nombre] {
			return Config{}, fmt.Errorf("hay dos servicios llamados %q", s.Nombre)
		}
		vistos[s.Nombre] = true
	}

	return c, nil
}
