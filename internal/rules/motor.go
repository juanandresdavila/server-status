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
	CiclosEnVentana(sujeto string, desde time.Time) (int, error)
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

	// CiclosParaFlapear es cuántas aperturas en VentanaDeFlapeo hacen falta
	// para declarar a un sujeto inestable y callarse.
	CiclosParaFlapear int
	VentanaDeFlapeo   time.Duration
	SilencioPorFlapeo time.Duration
}

func Defaults() Config {
	return Config{
		Servicios:  PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2},
		Containers: PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2},
		Disco:      PorUmbral{Abre: 80, Cierra: 75, Sostenido: 5 * time.Minute},
		Memoria:    PorUmbral{Abre: 90, Cierra: 85, Sostenido: 10 * time.Minute},
		Swap:       PorUmbral{Abre: 25, Cierra: 10, Sostenido: 10 * time.Minute},
		Carga:      PorUmbral{Abre: 6, Cierra: 4, Sostenido: 10 * time.Minute},

		CiclosParaFlapear: 4,
		VentanaDeFlapeo:   time.Hour,
		SilencioPorFlapeo: time.Hour,
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
	// silenciados dice hasta cuándo un sujeto está callado por rebotar.
	silenciados map[string]time.Time
}

func NewMotor(s Store, clk clock.Clock, cfg Config) *Motor {
	return &Motor{
		store:       s,
		clk:         clk,
		cfg:         cfg,
		conteos:     map[string]Contador{},
		umbrales:    map[string]EstadoUmbral{},
		silenciados: map[string]time.Time{},
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

// aplicar persiste la transición. Devuelve nil si no hubo ninguna, o si el
// sujeto está silenciado por rebotar demasiado.
//
// Importante: el incidente se abre y se cierra en la base IGUAL cuando el
// sujeto está silenciado. El estado tiene que seguir siendo verdadero; lo que
// se suprime es el mensaje, no el registro.
func (m *Motor) aplicar(tr Transicion, sujeto, tipo, severidad, detalle string, abierto model.Incidente) (*Cambio, error) {
	ahora := m.clk.Now()
	silenciado := ahora.Before(m.silenciados[sujeto])

	switch tr {
	case Abre:
		i := model.Incidente{
			Sujeto: sujeto, Tipo: tipo, Severidad: severidad,
			AbiertoEn: ahora, Detalle: detalle,
		}
		id, err := m.store.AbrirIncidente(i)
		if err != nil {
			return nil, err
		}
		i.ID = id
		if silenciado {
			return nil, nil
		}
		return &Cambio{Tipo: Abre, Incidente: i}, nil

	case Cierra:
		if err := m.store.CerrarIncidente(abierto.ID, ahora); err != nil {
			return nil, err
		}
		abierto.CerradoEn = &ahora
		if silenciado {
			return nil, nil
		}

		// Recién al cerrar se puede saber si el sujeto está rebotando: es el
		// momento en que se completó un ciclo.
		flap, err := m.chequearFlapeo(sujeto, ahora)
		if err != nil {
			return nil, err
		}
		if flap != nil {
			return flap, nil
		}
		return &Cambio{Tipo: Cierra, Incidente: abierto}, nil
	}
	return nil, nil
}

// chequearFlapeo devuelve un aviso de inestabilidad —y silencia al sujeto— si
// abrió demasiadas veces en la ventana. Devuelve nil si está todo normal.
func (m *Motor) chequearFlapeo(sujeto string, ahora time.Time) (*Cambio, error) {
	n, err := m.store.CiclosEnVentana(sujeto, ahora.Add(-m.cfg.VentanaDeFlapeo))
	if err != nil {
		return nil, err
	}
	if n < m.cfg.CiclosParaFlapear {
		return nil, nil
	}

	m.silenciados[sujeto] = ahora.Add(m.cfg.SilencioPorFlapeo)

	// El aviso de flapeo NO se persiste como incidente: el sujeto ya tiene su
	// historial de incidentes reales y abrir otro chocaría con
	// incidentes_abierto_unico. El id de entrega sale del timestamp, que no
	// colisiona con los ids autoincrementales de la tabla.
	return &Cambio{Tipo: Abre, Incidente: model.Incidente{
		ID: ahora.Unix(), Sujeto: sujeto, Tipo: "flapping", Severidad: "warning",
		AbiertoEn: ahora,
		Detalle: fmt.Sprintf("%d caídas en %s — me callo por %s",
			n, m.cfg.VentanaDeFlapeo, m.cfg.SilencioPorFlapeo),
	}}, nil
}

// EvaluarContainers aplica la política por conteo a cada container.
//
// Un container sano es uno corriendo cuyo healthcheck no falla. 'none' cuenta
// como sano: la mayoría no declara healthcheck y no tenerlo no es una falla.
// 'starting' también, porque es transitorio.
func (m *Motor) EvaluarContainers(cs []model.ContainerSample) ([]Cambio, error) {
	abiertos, err := m.abiertosPorSujeto()
	if err != nil {
		return nil, err
	}

	var cambios []Cambio
	for _, c := range cs {
		sujeto := "container:" + c.Name
		inc, estaAbierto := abiertos[sujeto]

		ok, tipo, detalle := saludContainer(c)
		nuevo, tr := m.cfg.Containers.Aplicar(m.conteos[sujeto], ok, estaAbierto)
		m.conteos[sujeto] = nuevo

		cb, err := m.aplicar(tr, sujeto, tipo, "warning", detalle, inc)
		if err != nil {
			return nil, err
		}
		if cb != nil {
			cambios = append(cambios, *cb)
		}
	}
	return cambios, nil
}

func saludContainer(c model.ContainerSample) (ok bool, tipo, detalle string) {
	if c.State != "running" {
		return false, "down", "estado " + c.State
	}
	if c.Health == "unhealthy" {
		return false, "unhealthy", "healthcheck fallando"
	}
	return true, "down", "corriendo"
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
