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
}

// Servicio es una cosa que se puede caer, con la URL que lo prueba y los
// containers que lo componen.
type Servicio struct {
	Nombre     string   `yaml:"nombre"`
	Probe      string   `yaml:"probe"`
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
