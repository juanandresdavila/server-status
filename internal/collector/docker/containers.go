package docker

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Container es el estado de un container en un momento dado.
type Container struct {
	ID       string
	Name     string
	State    string // running | exited | restarting | paused | dead
	Health   string // healthy | unhealthy | starting | none
	Restarts int
	// StartedAt es cuándo arrancó ESTA encarnación del container, y es la única
	// señal confiable de que se reinició. Restarts no sirve: solo cuenta los
	// reinicios por política, así que un arranque con el host lo deja igual y
	// una recreación con compose lo resetea a cero.
	StartedAt time.Time
	CPUPct    float64
	MemBytes  uint64
}

type resumenAPI struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	State string   `json:"State"`
}

// List trae todos los containers, incluidos los apagados: uno que se murió
// es exactamente lo que hay que reportar.
func (c *Client) List(ctx context.Context) ([]Container, error) {
	var crudos []resumenAPI
	if err := c.get(ctx, "/containers/json?all=1", &crudos); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(crudos))
	for _, r := range crudos {
		out = append(out, Container{
			ID:    r.ID,
			Name:  primerNombre(r.Names),
			State: r.State,
		})
	}
	return out, nil
}

// primerNombre saca la barra inicial que agrega Docker: "/comm-tool".
func primerNombre(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// Detalle es lo que solo aparece en el inspect: el listado no trae health
// como campo, solo embebido en un string tipo "Up 2 hours (healthy)" que sería
// frágil de parsear.
type Detalle struct {
	Health    string
	Restarts  int
	StartedAt time.Time
}

type inspectAPI struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		StartedAt string `json:"StartedAt"`
		// Puntero a propósito: si el container no tiene healthcheck, Docker
		// omite el objeto entero y esto queda en nil.
		Health *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

func (c *Client) Inspect(ctx context.Context, id string) (Detalle, error) {
	var crudo inspectAPI
	if err := c.get(ctx, "/containers/"+id+"/json", &crudo); err != nil {
		return Detalle{}, err
	}
	salud := "none"
	if crudo.State.Health != nil && crudo.State.Health.Status != "" {
		salud = crudo.State.Health.Status
	}
	// Docker manda RFC3339 con nanosegundos, y para un container que nunca
	// arrancó manda el cero "0001-01-01T00:00:00Z". Si no parsea se deja el
	// cero de time.Time, que el detector lee como "no se sabe".
	var arranco time.Time
	if t, err := time.Parse(time.RFC3339Nano, crudo.State.StartedAt); err == nil && t.Year() > 1 {
		arranco = t.UTC()
	}
	return Detalle{Health: salud, Restarts: crudo.RestartCount, StartedAt: arranco}, nil
}

// Uso es el consumo instantáneo de un container.
type Uso struct {
	CPUPct   float64
	MemBytes uint64
}

type cpuStatsAPI struct {
	CPUUsage struct {
		TotalUsage uint64 `json:"total_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint64 `json:"online_cpus"`
}

type statsAPI struct {
	CPUStats    cpuStatsAPI `json:"cpu_stats"`
	PreCPUStats cpuStatsAPI `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Stats struct {
			InactiveFile uint64 `json:"inactive_file"`
		} `json:"stats"`
	} `json:"memory_stats"`
}

// Stats pide una foto del consumo.
//
// stream=false es obligatorio: sin él la API deja la conexión abierta mandando
// una muestra por segundo para siempre, y el request nunca termina.
func (c *Client) Stats(ctx context.Context, id string) (Uso, error) {
	var crudo statsAPI
	if err := c.get(ctx, "/containers/"+id+"/stats?stream=false", &crudo); err != nil {
		return Uso{}, err
	}
	return Uso{
		CPUPct:   cpuPct(crudo),
		MemBytes: memReal(crudo),
	}, nil
}

func cpuPct(s statsAPI) float64 {
	deltaCPU := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	deltaSys := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if deltaCPU <= 0 || deltaSys <= 0 {
		return 0
	}
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = 1
	}
	return deltaCPU / deltaSys * cpus * 100
}

// memReal descuenta el page cache reclamable, que es lo que hace docker stats
// en cgroup v2 — que es lo que corre el VPS.
func memReal(s statsAPI) uint64 {
	uso := s.MemoryStats.Usage
	cache := s.MemoryStats.Stats.InactiveFile
	if cache > uso {
		return 0
	}
	return uso - cache
}

// EsApagado dice si el contexto ya terminó, o sea si el proceso se está
// apagando. Un error de Docker durante el apagado es esperado y no merece
// una advertencia: con 21 containers son más de 40 líneas de ruido por
// reinicio, y eso es lo que hace que un WARN real pase desapercibido.
func EsApagado(ctx context.Context) bool { return ctx.Err() != nil }

// Recolectar arma la foto completa: lista, y para cada container su detalle y
// su uso, con como mucho `limite` requests en vuelo.
//
// Un container que falla no aborta la pasada: se loguea y se devuelve lo que
// sí se pudo leer. Un monitor que se calla entero porque un container se
// estaba reiniciando no sirve para nada.
func (c *Client) Recolectar(ctx context.Context, limite int) ([]Container, error) {
	cs, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	if limite < 1 {
		limite = 1
	}

	sem := make(chan struct{}, limite)
	var wg sync.WaitGroup

	for i := range cs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if d, err := c.Inspect(ctx, cs[i].ID); err != nil {
				if !EsApagado(ctx) {
					slog.Warn("no se pudo inspeccionar", "container", cs[i].Name, "err", err)
				}
			} else {
				cs[i].Health = d.Health
				cs[i].Restarts = d.Restarts
				cs[i].StartedAt = d.StartedAt
			}

			// Un container apagado no tiene stats y pedirlos da error.
			if cs[i].State != "running" {
				return
			}
			if u, err := c.Stats(ctx, cs[i].ID); err != nil {
				if !EsApagado(ctx) {
					slog.Warn("no se pudieron leer los stats", "container", cs[i].Name, "err", err)
				}
			} else {
				cs[i].CPUPct = u.CPUPct
				cs[i].MemBytes = u.MemBytes
			}
		}(i)
	}
	wg.Wait()
	return cs, nil
}
