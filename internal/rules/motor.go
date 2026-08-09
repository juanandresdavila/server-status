package rules

import (
	"fmt"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
)

// Store es lo que el motor necesita de la persistencia, y nada más.
// Declararlo acá y no importar el paquete store deja el motor testeable
// sin base y evita la dependencia circular.
type Store interface {
	IncidentesAbiertos() ([]model.Incidente, error)
	AbrirIncidente(model.Incidente) (int64, error)
	CerrarIncidente(id int64, cuando time.Time) error
}

// Cambio es una transición ya aplicada. El plan 3 lo convierte en un mensaje.
type Cambio struct {
	Tipo      Transicion
	Incidente model.Incidente
}

// Config son los umbrales y conteos. Defaults() trae los del spec.
type Config struct {
	Servicios  PorConteo
	Containers PorConteo
	Disco      PorUmbral
	Memoria    PorUmbral
	Swap       PorUmbral
	Carga      PorUmbral
}

func Defaults() Config {
	return Config{
		Servicios:  PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2},
		Containers: PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2},
		Disco:      PorUmbral{Abre: 80, Cierra: 75, Sostenido: 5 * time.Minute},
		Memoria:    PorUmbral{Abre: 90, Cierra: 85, Sostenido: 10 * time.Minute},
		Swap:       PorUmbral{Abre: 25, Cierra: 10, Sostenido: 10 * time.Minute},
		Carga:      PorUmbral{Abre: 6, Cierra: 4, Sostenido: 10 * time.Minute},
	}
}

// Motor aplica las políticas y persiste los incidentes.
//
// Los contadores viven en memoria a propósito: una racha de 1 o 2 fallas que
// todavía no es incidente no vale la pena persistirla, y perderla en un
// reinicio solo demora el aviso unos minutos. Lo que SÍ se persiste son los
// incidentes, que es lo que evita remandar avisos al arrancar.
type Motor struct {
	store    Store
	clk      clock.Clock
	cfg      Config
	conteos  map[string]Contador
	umbrales map[string]EstadoUmbral
}

func NewMotor(s Store, clk clock.Clock, cfg Config) *Motor {
	return &Motor{
		store:    s,
		clk:      clk,
		cfg:      cfg,
		conteos:  map[string]Contador{},
		umbrales: map[string]EstadoUmbral{},
	}
}

func (m *Motor) abiertosPorSujeto() (map[string]model.Incidente, error) {
	is, err := m.store.IncidentesAbiertos()
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.Incidente, len(is))
	for _, i := range is {
		out[i.Sujeto] = i
	}
	return out, nil
}

// EvaluarProbes aplica la política por conteo a cada servicio.
func (m *Motor) EvaluarProbes(rs []model.ProbeResult) ([]Cambio, error) {
	abiertos, err := m.abiertosPorSujeto()
	if err != nil {
		return nil, err
	}

	var cambios []Cambio
	for _, r := range rs {
		sujeto := "service:" + r.Servicio
		inc, estaAbierto := abiertos[sujeto]

		nuevo, tr := m.cfg.Servicios.Aplicar(m.conteos[sujeto], r.OK, estaAbierto)
		m.conteos[sujeto] = nuevo

		c, err := m.aplicar(tr, sujeto, "down", "critical", detalleProbe(r), inc)
		if err != nil {
			return nil, err
		}
		if c != nil {
			cambios = append(cambios, *c)
		}
	}
	return cambios, nil
}

// EvaluarHost aplica las políticas por umbral a las métricas de la máquina.
func (m *Motor) EvaluarHost(s model.HostSample) ([]Cambio, error) {
	abiertos, err := m.abiertosPorSujeto()
	if err != nil {
		return nil, err
	}

	medidas := []struct {
		sujeto  string
		umbral  PorUmbral
		valor   float64
		detalle string
	}{
		{"host:disk", m.cfg.Disco, pct(s.DiskUsedBytes, s.DiskTotalBytes), "disco"},
		{"host:mem", m.cfg.Memoria, pct(s.MemUsedBytes, s.MemTotalBytes), "memoria"},
		{"host:swap", m.cfg.Swap, pct(s.SwapUsedBytes, s.SwapTotalBytes), "swap"},
		{"host:load", m.cfg.Carga, s.Load1, "carga"},
	}

	ahora := m.clk.Now()
	var cambios []Cambio
	for _, md := range medidas {
		inc, estaAbierto := abiertos[md.sujeto]

		nuevo, tr := md.umbral.Aplicar(m.umbrales[md.sujeto], md.valor, ahora, estaAbierto)
		m.umbrales[md.sujeto] = nuevo

		detalle := fmt.Sprintf("%s en %.1f", md.detalle, md.valor)
		c, err := m.aplicar(tr, md.sujeto, "threshold", "warning", detalle, inc)
		if err != nil {
			return nil, err
		}
		if c != nil {
			cambios = append(cambios, *c)
		}
	}
	return cambios, nil
}

// aplicar persiste la transición. Devuelve nil si no hubo ninguna.
func (m *Motor) aplicar(tr Transicion, sujeto, tipo, severidad, detalle string, abierto model.Incidente) (*Cambio, error) {
	switch tr {
	case Abre:
		i := model.Incidente{
			Sujeto: sujeto, Tipo: tipo, Severidad: severidad,
			AbiertoEn: m.clk.Now(), Detalle: detalle,
		}
		id, err := m.store.AbrirIncidente(i)
		if err != nil {
			return nil, err
		}
		i.ID = id
		return &Cambio{Tipo: Abre, Incidente: i}, nil

	case Cierra:
		cuando := m.clk.Now()
		if err := m.store.CerrarIncidente(abierto.ID, cuando); err != nil {
			return nil, err
		}
		abierto.CerradoEn = &cuando
		return &Cambio{Tipo: Cierra, Incidente: abierto}, nil
	}
	return nil, nil
}

func pct(usado, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(usado) * 100 / float64(total)
}

func detalleProbe(r model.ProbeResult) string {
	if r.Error != "" {
		return r.Error
	}
	return fmt.Sprintf("HTTP %d", r.StatusCode)
}
