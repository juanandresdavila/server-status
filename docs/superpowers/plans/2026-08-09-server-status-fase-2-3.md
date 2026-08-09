# server-status — Plan de implementación, fases 2 y 3

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Que el servicio sepa el estado de cada container y de cada servicio público, y que abra y cierre incidentes solo, con la máquina de estados del spec. Sin avisos todavía: eso es el plan 3.

**Architecture:** Un cliente HTTP mínimo contra el socket unix de Docker, un prober que pincha las URLs públicas, y un motor de reglas que traduce resultados en transiciones. Las políticas (por conteo y por umbral) son funciones puras sobre un estado explícito, así que se testean sin base y sin red.

**Tech Stack:** Go, `net/http` sobre socket unix, SQLite, reloj inyectado.

**Spec:** `docs/superpowers/specs/2026-08-08-server-status-design.md`
**Plan anterior:** `docs/superpowers/plans/2026-08-08-server-status-fase-0-1.md`

---

## Estructura de archivos

| Archivo | Responsabilidad |
|---|---|
| `internal/collector/docker/client.go` | Transporte HTTP sobre el socket unix y decodificación de errores |
| `internal/collector/docker/containers.go` | `List`, `Inspect`, `Stats` y la recolección concurrente |
| `internal/prober/prober.go` | Pinchar una URL y clasificar el resultado |
| `internal/rules/politicas.go` | `PorConteo` y `PorUmbral` — funciones puras |
| `internal/rules/motor.go` | Aplica las políticas contra el store y emite transiciones |
| `internal/store/store.go` | Migraciones 0002 y 0003, más los métodos nuevos |
| `internal/model/model.go` | `ContainerSample`, `ProbeResult`, `Incidente` |
| `internal/config/config.go` | La lista de `servicios` |

**Por qué las políticas son funciones puras:** la máquina de estados es lo único del sistema que puede mandar un mensaje a las 3 de la mañana. Si su lógica está enredada con la base y la red, no se puede testear el caso "rebota cinco veces" sin montar medio sistema. Separadas, ese test son diez líneas.

---

## Task 1: Cliente de Docker — transporte y listado

**Files:**
- Create: `internal/collector/docker/client.go`, `internal/collector/docker/containers.go`, `internal/collector/docker/docker_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/collector/docker/docker_test.go`:

```go
package docker_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/juanandresdavila/server-status/internal/collector/docker"
)

// servidorFalso levanta un httptest sobre un socket unix, que es como habla
// Docker de verdad.
//
// El directorio va en /tmp y no en t.TempDir(): la ruta de un socket unix no
// puede pasar los ~104 caracteres, y en macOS t.TempDir() devuelve algo como
// /var/folders/xx/.../T/TestNombre123 que se pasa sin avisar. El error que da
// es "invalid argument" en el bind, que no dice nada de longitudes.
func servidorFalso(t *testing.T, h http.Handler) (*docker.Client, *httptest.Server) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ss")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("escuchar en %s: %v", socket, err)
	}

	srv := httptest.NewUnstartedServer(h)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	return docker.New(socket), srv
}

func TestListSacaLaBarraDelNombre(t *testing.T) {
	c, _ := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			t.Errorf("path = %q, quería /containers/json", r.URL.Path)
		}
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("falta all=1, query = %q", r.URL.RawQuery)
		}
		w.Write([]byte(`[
			{"Id":"abc123","Names":["/comm-tool"],"State":"running"},
			{"Id":"def456","Names":["/supabase-db"],"State":"exited"}
		]`))
	}))

	got, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d containers, quería 2", len(got))
	}
	// Docker devuelve los nombres con barra inicial: "/comm-tool".
	if got[0].Name != "comm-tool" {
		t.Errorf("Name = %q, quería comm-tool", got[0].Name)
	}
	if got[0].ID != "abc123" {
		t.Errorf("ID = %q, quería abc123", got[0].ID)
	}
	if got[1].State != "exited" {
		t.Errorf("State = %q, quería exited", got[1].State)
	}
}

func TestListPropagaElErrorDeDocker(t *testing.T) {
	c, _ := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"algo se rompió"}`))
	}))

	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("quería error con un 500, no hubo")
	}
	// El mensaje de Docker tiene que llegar al log: sin él, diagnosticar
	// es adivinar.
	if !contiene(err.Error(), "algo se rompió") {
		t.Errorf("el error no incluye el cuerpo de Docker: %v", err)
	}
}

func contiene(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

⚠️ Las funciones `contiene`/`indexOf` son ruido: usar `strings.Contains` e importar `"strings"`. Están acá solo porque un ejecutor apurado las escribiría a mano.

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/docker/`
Expected: FAIL — el paquete no existe

- [ ] **Step 3: Implementar el transporte**

`internal/collector/docker/client.go`:

```go
// Package docker es un cliente mínimo de la API de Docker sobre su socket unix.
//
// No usa la SDK oficial a propósito: hacen falta cuatro endpoints y la SDK
// arrastra un árbol de dependencias enorme para eso.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct {
	http *http.Client
}

// New arma un cliente que habla HTTP sobre un socket unix. El host de la URL
// ("docker") es un placeholder: el DialContext ignora la dirección y siempre
// abre el socket.
func New(socket string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
		Timeout: 30 * time.Second,
	}}
}

func (c *Client) get(ctx context.Context, ruta string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+ruta, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", ruta, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		cuerpo, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %s: %s", ruta, resp.Status, bytes.TrimSpace(cuerpo))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
```

- [ ] **Step 4: Implementar el listado**

`internal/collector/docker/containers.go`:

```go
package docker

import (
	"context"
	"strings"
)

// Container es el estado de un container en un momento dado.
type Container struct {
	ID       string
	Name     string
	State    string // running | exited | restarting | paused | dead
	Health   string // healthy | unhealthy | starting | none
	Restarts int
	CPUPct   float64
	MemBytes uint64
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
```

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/docker/ -v`
Expected: PASS, 2 tests

- [ ] **Step 6: Commit**

```bash
git add internal/collector/docker
git commit -m "feat: cliente mínimo de la API de Docker sobre el socket unix"
```

---

## Task 2: Health y reinicios

**Files:**
- Modify: `internal/collector/docker/containers.go`, `internal/collector/docker/docker_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `docker_test.go`:

```go
func TestInspectLeeHealthYReinicios(t *testing.T) {
	c, _ := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"RestartCount":3,"State":{"Health":{"Status":"unhealthy"}}}`))
	}))

	got, err := c.Inspect(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got.Health != "unhealthy" {
		t.Errorf("Health = %q, quería unhealthy", got.Health)
	}
	if got.Restarts != 3 {
		t.Errorf("Restarts = %d, quería 3", got.Restarts)
	}
}

// La mayoría de los containers no declaran healthcheck. Docker entonces no
// manda el objeto Health, y eso no es un error ni es "unhealthy".
func TestInspectSinHealthcheckDiceNone(t *testing.T) {
	c, _ := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"RestartCount":0,"State":{"Running":true}}`))
	}))

	got, err := c.Inspect(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got.Health != "none" {
		t.Errorf("Health = %q, quería none", got.Health)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/docker/ -run Inspect`
Expected: FAIL — `c.Inspect undefined`

- [ ] **Step 3: Implementar**

Agregar a `containers.go`:

```go
// Detalle es lo que solo aparece en el inspect: el listado no trae health
// como campo, solo embebido en un string tipo "Up 2 hours (healthy)" que sería
// frágil de parsear.
type Detalle struct {
	Health   string
	Restarts int
}

type inspectAPI struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
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
	return Detalle{Health: salud, Restarts: crudo.RestartCount}, nil
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/docker/ -v`
Expected: PASS, 4 tests

- [ ] **Step 5: Commit**

```bash
git add internal/collector/docker
git commit -m "feat: leer health y cantidad de reinicios por container"
```

---

## Task 3: CPU y memoria por container

**Files:**
- Modify: `internal/collector/docker/containers.go`, `internal/collector/docker/docker_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `docker_test.go`:

```go
func TestStatsCalculaCPUYMemoria(t *testing.T) {
	c, _ := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "false" {
			t.Errorf("falta stream=false: la API bloquea para siempre sin eso")
		}
		w.Write([]byte(`{
			"cpu_stats":    {"cpu_usage":{"total_usage":2000},"system_cpu_usage":100000,"online_cpus":6},
			"precpu_stats": {"cpu_usage":{"total_usage":1000},"system_cpu_usage":90000,"online_cpus":6},
			"memory_stats": {"usage":500000000,"stats":{"inactive_file":100000000}}
		}`))
	}))

	got, err := c.Stats(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	// deltaCPU=1000, deltaSys=10000 → 0.1 · 6 cores · 100 = 60%
	if got.CPUPct != 60 {
		t.Errorf("CPUPct = %v, quería 60", got.CPUPct)
	}
	// La memoria "real" descuenta el page cache reclamable, igual que
	// docker stats. Sin descontarlo, todo container que leyó archivos
	// parece estar comiéndose la RAM.
	if got.MemBytes != 400000000 {
		t.Errorf("MemBytes = %d, quería 400000000", got.MemBytes)
	}
}

// El primer stats de un container recién arrancado no tiene lectura previa:
// los deltas dan cero o negativo y hay que devolver 0, no NaN ni un número
// inventado.
func TestStatsSinLecturaPreviaDaCero(t *testing.T) {
	c, _ := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"cpu_stats":    {"cpu_usage":{"total_usage":1000},"system_cpu_usage":90000,"online_cpus":6},
			"precpu_stats": {"cpu_usage":{"total_usage":0},"system_cpu_usage":0,"online_cpus":0},
			"memory_stats": {"usage":1000,"stats":{"inactive_file":0}}
		}`))
	}))

	got, err := c.Stats(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got.CPUPct != 0 {
		t.Errorf("CPUPct = %v, quería 0 sin lectura previa", got.CPUPct)
	}
}
```

⚠️ El segundo test tiene `system_cpu_usage` previo en 0, así que `deltaSys` = 90000, positivo. El que da cero es otro caso. Corregir el fixture: poner `"system_cpu_usage":90000` también en `cpu_stats`, de modo que `deltaSys` sea 0 y se ejercite la guarda.

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/docker/ -run Stats`
Expected: FAIL — `c.Stats undefined`

- [ ] **Step 3: Implementar**

Agregar a `containers.go`:

```go
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
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/docker/ -v`
Expected: PASS, 6 tests

- [ ] **Step 5: Commit**

```bash
git add internal/collector/docker
git commit -m "feat: CPU y memoria por container, descontando el page cache"
```

---

## Task 4: Recolección concurrente

**Files:**
- Modify: `internal/collector/docker/containers.go`, `internal/collector/docker/docker_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `docker_test.go` (sumar `"sync"`, `"sync/atomic"` y `"strings"` a los imports):

```go
func TestRecolectarJuntaTodoYLimitaLaConcurrencia(t *testing.T) {
	var enVuelo, pico int64

	c, _ := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/json":
			w.Write([]byte(`[
				{"Id":"c1","Names":["/uno"],"State":"running"},
				{"Id":"c2","Names":["/dos"],"State":"running"},
				{"Id":"c3","Names":["/tres"],"State":"running"},
				{"Id":"c4","Names":["/cuatro"],"State":"running"}
			]`))
		case strings.HasSuffix(r.URL.Path, "/stats"):
			n := atomic.AddInt64(&enVuelo, 1)
			for {
				p := atomic.LoadInt64(&pico)
				if n <= p || atomic.CompareAndSwapInt64(&pico, p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&enVuelo, -1)
			w.Write([]byte(`{
				"cpu_stats":    {"cpu_usage":{"total_usage":2000},"system_cpu_usage":100000,"online_cpus":6},
				"precpu_stats": {"cpu_usage":{"total_usage":1000},"system_cpu_usage":90000,"online_cpus":6},
				"memory_stats": {"usage":500000000,"stats":{"inactive_file":100000000}}
			}`))
		default: // el inspect
			w.Write([]byte(`{"RestartCount":1,"State":{"Health":{"Status":"healthy"}}}`))
		}
	}))

	got, err := c.Recolectar(context.Background(), 2)
	if err != nil {
		t.Fatalf("Recolectar: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("volvieron %d containers, quería 4", len(got))
	}

	// El límite existe porque 19 containers × un stats que bloquea 1-2 s
	// no entran en un ciclo de 60 s si van en serie, pero tampoco queremos
	// 19 requests simultáneos contra el daemon.
	if p := atomic.LoadInt64(&pico); p > 2 {
		t.Errorf("hubo %d stats simultáneos, el límite era 2", p)
	}

	porNombre := map[string]docker.Container{}
	for _, ct := range got {
		porNombre[ct.Name] = ct
	}
	uno, ok := porNombre["uno"]
	if !ok {
		t.Fatal("falta el container 'uno' en el resultado")
	}
	if uno.Health != "healthy" || uno.Restarts != 1 || uno.CPUPct != 60 {
		t.Errorf("uno = %+v, esperaba health=healthy restarts=1 cpu=60", uno)
	}
}

// Un container que falla el inspect no puede tumbar la recolección entera:
// el resto de los datos siguen siendo útiles.
func TestRecolectarSobreviveAUnContainerQueFalla(t *testing.T) {
	c, _ := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/json":
			w.Write([]byte(`[
				{"Id":"bueno","Names":["/bueno"],"State":"running"},
				{"Id":"roto","Names":["/roto"],"State":"running"}
			]`))
		case strings.Contains(r.URL.Path, "roto"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"no such container"}`))
		case strings.HasSuffix(r.URL.Path, "/stats"):
			w.Write([]byte(`{
				"cpu_stats":    {"cpu_usage":{"total_usage":2000},"system_cpu_usage":100000,"online_cpus":6},
				"precpu_stats": {"cpu_usage":{"total_usage":1000},"system_cpu_usage":90000,"online_cpus":6},
				"memory_stats": {"usage":500000000,"stats":{"inactive_file":100000000}}
			}`))
		default:
			w.Write([]byte(`{"RestartCount":0,"State":{"Health":{"Status":"healthy"}}}`))
		}
	}))

	got, err := c.Recolectar(context.Background(), 4)
	if err != nil {
		t.Fatalf("Recolectar devolvió error por un container roto: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d containers, quería los 2", len(got))
	}
	_ = sync.OnceFunc(func() {}) // mantiene el import de sync si no se usa
}
```

⚠️ La última línea del segundo test es basura: borrarla y sacar `"sync"` de los imports si no quedó en uso.

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/docker/ -run Recolectar`
Expected: FAIL — `c.Recolectar undefined`

- [ ] **Step 3: Implementar**

Agregar a `containers.go` (sumar `"log/slog"` y `"sync"` a los imports):

```go
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
				slog.Warn("no se pudo inspeccionar", "container", cs[i].Name, "err", err)
			} else {
				cs[i].Health = d.Health
				cs[i].Restarts = d.Restarts
			}

			// Un container apagado no tiene stats y pedirlos da error.
			if cs[i].State != "running" {
				return
			}
			if u, err := c.Stats(ctx, cs[i].ID); err != nil {
				slog.Warn("no se pudieron leer los stats", "container", cs[i].Name, "err", err)
			} else {
				cs[i].CPUPct = u.CPUPct
				cs[i].MemBytes = u.MemBytes
			}
		}(i)
	}
	wg.Wait()
	return cs, nil
}
```

Cada goroutine escribe en `cs[i]`, un índice distinto por goroutine: no hay carrera y `-race` lo confirma.

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/docker/ -race -v`
Expected: PASS, 8 tests, sin avisos del detector de carreras

- [ ] **Step 5: Commit**

```bash
git add internal/collector/docker
git commit -m "feat: recolección concurrente de containers con límite en vuelo"
```

---

## Task 5: Migración 0002 y persistencia de containers

**Files:**
- Modify: `internal/model/model.go`, `internal/store/store.go`, `internal/store/store_test.go`

- [ ] **Step 1: Agregar el tipo**

Agregar a `internal/model/model.go`:

```go
// ContainerSample es el estado de un container en un minuto dado.
type ContainerSample struct {
	TS       time.Time
	Name     string
	State    string
	Health   string
	Restarts int
	CPUPct   float64
	MemBytes uint64
}
```

- [ ] **Step 2: Escribir el test que falla**

Agregar a `internal/store/store_test.go`:

```go
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

func TestMigracion0002SubeLaVersion(t *testing.T) {
	s := abrir(t)
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Errorf("versión = %d, quería 2", v)
	}
}
```

⚠️ `TestOpenAplicaLasMigraciones` y `TestOpenDosVecesNoRompe` del plan anterior afirman `v != 1`. Actualizar los dos a `2`, o van a fallar.

- [ ] **Step 3: Correr el test y verificar que falla**

Run: `go test ./internal/store/`
Expected: FAIL — `s.InsertContainerSamples undefined` y la versión da 1

- [ ] **Step 4: Implementar**

Agregar la migración al final del slice `migraciones` en `internal/store/store.go`:

```go
	`CREATE TABLE container_samples (
		ts        INTEGER NOT NULL,
		name      TEXT    NOT NULL,
		state     TEXT    NOT NULL,
		health    TEXT    NOT NULL,
		restarts  INTEGER NOT NULL,
		cpu_pct   REAL    NOT NULL,
		mem_bytes INTEGER NOT NULL,
		PRIMARY KEY (ts, name)
	) STRICT;`,
```

Y los métodos:

```go
// InsertContainerSamples guarda la foto de un minuto, toda en una transacción:
// media foto es peor que ninguna.
func (s *Store) InsertContainerSamples(ms []model.ContainerSample) error {
	if len(ms) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO container_samples (ts, name, state, health, restarts, cpu_pct, mem_bytes)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(ts, name) DO UPDATE SET
			state=excluded.state, health=excluded.health, restarts=excluded.restarts,
			cpu_pct=excluded.cpu_pct, mem_bytes=excluded.mem_bytes`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range ms {
		if _, err := stmt.Exec(
			m.TS.Truncate(time.Minute).Unix(), m.Name, m.State, m.Health,
			m.Restarts, m.CPUPct, int64(m.MemBytes),
		); err != nil {
			return fmt.Errorf("insertar %s: %w", m.Name, err)
		}
	}
	return tx.Commit()
}

// UltimoEstadoContainers devuelve la foto del minuto más reciente.
func (s *Store) UltimoEstadoContainers() ([]model.ContainerSample, error) {
	filas, err := s.db.Query(`
		SELECT ts, name, state, health, restarts, cpu_pct, mem_bytes
		FROM container_samples
		WHERE ts = (SELECT MAX(ts) FROM container_samples)
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.ContainerSample
	for filas.Next() {
		var (
			c   model.ContainerSample
			ts  int64
			mem int64
		)
		if err := filas.Scan(&ts, &c.Name, &c.State, &c.Health, &c.Restarts, &c.CPUPct, &mem); err != nil {
			return nil, err
		}
		c.TS = time.Unix(ts, 0).UTC()
		c.MemBytes = uint64(mem)
		out = append(out, c)
	}
	return out, filas.Err()
}
```

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./internal/store/ -v`
Expected: PASS, 8 tests

- [ ] **Step 6: Commit**

```bash
git add internal/store internal/model
git commit -m "feat: persistir el estado de los containers"
```

---

## Task 6: Comando `containers` y wiring

**Files:**
- Modify: `cmd/server-status/main.go`, `internal/config/config.go`, `internal/config/config_test.go`, `deploy/config.example.yaml`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/config/ -run Docker`
Expected: FAIL — campo inexistente

- [ ] **Step 3: Implementar la config**

Agregar los campos al struct `Config`:

```go
	DockerSocket       string `yaml:"docker_socket"`
	DockerConcurrencia int    `yaml:"docker_concurrencia"`
```

Y los defaults en `Load`, antes de la validación de `Base`:

```go
	if c.DockerSocket == "" {
		c.DockerSocket = "/var/run/docker.sock"
	}
	if c.DockerConcurrencia == 0 {
		c.DockerConcurrencia = 8
	}
```

- [ ] **Step 4: Sumar el comando**

En `cmd/server-status/main.go`, agregar `"github.com/juanandresdavila/server-status/internal/collector/docker"` y `"context"` a los imports, y el caso al `switch`:

```go
	case "containers":
		return listarContainers(cfg)
```

Y la función:

```go
func listarContainers(cfg config.Config) error {
	cli := docker.New(cfg.DockerSocket)
	cs, err := cli.Recolectar(context.Background(), cfg.DockerConcurrencia)
	if err != nil {
		return err
	}
	const mib = 1024 * 1024
	fmt.Printf("%-24s %-12s %-10s %8s %10s %s\n", "NOMBRE", "ESTADO", "SALUD", "CPU%", "MEM", "REINICIOS")
	for _, c := range cs {
		fmt.Printf("%-24s %-12s %-10s %7.1f%% %8.0f M %d\n",
			c.Name, c.State, c.Health, c.CPUPct, float64(c.MemBytes)/mib, c.Restarts)
	}
	return nil
}
```

Actualizar también el mensaje de error del `default` del switch para que nombre los tres comandos.

- [ ] **Step 5: Sumar el loop de persistencia**

En `correr`, agregar el cliente antes del `for` y el guardado en el caso de persistencia:

```go
	cli := docker.New(cfg.DockerSocket)
```

Y dentro de `case <-persistencia.C:`, después de guardar la muestra del host:

```go
			cs, err := cli.Recolectar(ctx, cfg.DockerConcurrencia)
			if err != nil {
				slog.Error("no se pudieron recolectar los containers", "err", err)
				continue
			}
			ms := make([]model.ContainerSample, 0, len(cs))
			for _, c := range cs {
				ms = append(ms, model.ContainerSample{
					TS: m.TS, Name: c.Name, State: c.State, Health: c.Health,
					Restarts: c.Restarts, CPUPct: c.CPUPct, MemBytes: c.MemBytes,
				})
			}
			if err := s.InsertContainerSamples(ms); err != nil {
				slog.Error("no se pudieron guardar los containers", "err", err)
			}
```

Sumar `"github.com/juanandresdavila/server-status/internal/model"` a los imports.

- [ ] **Step 6: Actualizar el ejemplo de config**

Agregar a `deploy/config.example.yaml`:

```yaml
docker_socket: /var/run/docker.sock
docker_concurrencia: 8
```

- [ ] **Step 7: Verificar todo**

```bash
go vet ./...
go test ./... -race
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/server-status
```

Expected: todo en verde.

- [ ] **Step 8: Verificar contra el VPS**

```bash
make deploy
ssh vps '/usr/local/bin/server-status -config /etc/server-status/config.yaml containers'
ssh vps 'docker stats --no-stream --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}"'
```

Expected: los 19 containers en las dos salidas, con memoria del mismo orden. El CPU% puede diferir: son dos ventanas de medición distintas.

- [ ] **Step 9: Commit**

```bash
git add cmd internal/config deploy
git commit -m "feat: comando containers y persistencia en el loop"
```

---

## Task 7: Servicios en la config

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `deploy/config.example.yaml`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/config/ -run Servicio`
Expected: FAIL — campo inexistente

- [ ] **Step 3: Implementar**

Agregar a `internal/config/config.go`:

```go
// Servicio es una cosa que se puede caer, con la URL que lo prueba y los
// containers que lo componen.
type Servicio struct {
	Nombre     string   `yaml:"nombre"`
	Probe      string   `yaml:"probe"`
	Containers []string `yaml:"containers"`
}
```

Sumar el campo al `Config`:

```go
	Servicios []Servicio `yaml:"servicios"`
```

Y la validación en `Load`, después de la de `Base`:

```go
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
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/config/ -v`
Expected: PASS, 10 tests

- [ ] **Step 5: Actualizar el ejemplo**

Reemplazar el comentario sobre servicios en `deploy/config.example.yaml` por:

```yaml
servicios:
  - nombre: sitio
    probe: https://jadd.com.ar/
    containers: [caddy, cloudflared]
  - nombre: comm-tool
    probe: https://comm.jadd.com.ar/health
    containers: [comm-tool, comm-tool-db]
  - nombre: study-master
    probe: https://supabase-sm.jadd.com.ar/auth/v1/health
    containers: [supabase-kong, supabase-auth, supabase-rest, supabase-db,
                 supabase-storage, supabase-meta, supabase-imgproxy]
  - nombre: gym-tracker
    probe: https://supabase-gym.jadd.com.ar/auth/v1/health
    containers: [supabase-gym-kong, supabase-gym-auth, supabase-gym-rest,
                 supabase-gym-db, supabase-gym-meta, supabase-gym-studio]
```

- [ ] **Step 6: Commit**

```bash
git add internal/config deploy
git commit -m "feat: lista de servicios en la config, con nombres únicos"
```

---

## Task 8: Prober

**Files:**
- Create: `internal/prober/prober.go`, `internal/prober/prober_test.go`
- Modify: `internal/model/model.go`

- [ ] **Step 1: Agregar el tipo**

Agregar a `internal/model/model.go`:

```go
// ProbeResult es el resultado de pinchar un servicio una vez.
type ProbeResult struct {
	TS         time.Time
	Servicio   string
	OK         bool
	StatusCode int
	Latencia   time.Duration
	Error      string
}
```

- [ ] **Step 2: Escribir el test que falla**

`internal/prober/prober_test.go`:

```go
package prober_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/prober"
)

func TestProbeOKConDoscientos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	got := p.Probe(context.Background(), "x", srv.URL)

	if !got.OK {
		t.Errorf("OK = false, quería true. Error: %q", got.Error)
	}
	if got.StatusCode != 200 {
		t.Errorf("StatusCode = %d", got.StatusCode)
	}
	if got.Servicio != "x" {
		t.Errorf("Servicio = %q", got.Servicio)
	}
}

// Un 3xx significa que el servicio está vivo y contestando. Tratarlo como
// caída daría falsos positivos en cualquier sitio que redirija.
func TestProbeAceptaRedirecciones(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://example.com/otro")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	if got := p.Probe(context.Background(), "x", srv.URL); !got.OK {
		t.Errorf("un 301 se tomó como caída: %+v", got)
	}
}

func TestProbeFallaConQuinientos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := prober.New(clock.NewFake(time.Now()), 5*time.Second)
	got := p.Probe(context.Background(), "x", srv.URL)

	if got.OK {
		t.Error("OK = true con un 500")
	}
	if got.StatusCode != 500 {
		t.Errorf("StatusCode = %d, quería 500", got.StatusCode)
	}
	if got.Error == "" {
		t.Error("Error vacío: hay que poder saber qué pasó")
	}
}

func TestProbeFallaSiNoHayNadieEscuchando(t *testing.T) {
	p := prober.New(clock.NewFake(time.Now()), 2*time.Second)
	// Puerto cerrado del loopback: falla al conectar, sin respuesta HTTP.
	got := p.Probe(context.Background(), "x", "http://127.0.0.1:1/")

	if got.OK {
		t.Error("OK = true contra un puerto cerrado")
	}
	if got.StatusCode != 0 {
		t.Errorf("StatusCode = %d, quería 0 cuando no hubo respuesta", got.StatusCode)
	}
	if got.Error == "" {
		t.Error("Error vacío")
	}
}

// El TS sale del reloj inyectado, no de time.Now(): invariante 5 del spec.
func TestProbeUsaElRelojInyectado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	momento := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)
	p := prober.New(clock.NewFake(momento), 5*time.Second)

	if got := p.Probe(context.Background(), "x", srv.URL); !got.TS.Equal(momento) {
		t.Errorf("TS = %v, quería %v", got.TS, momento)
	}
}
```

- [ ] **Step 3: Correr el test y verificar que falla**

Run: `go test ./internal/prober/`
Expected: FAIL — el paquete no existe

- [ ] **Step 4: Implementar**

`internal/prober/prober.go`:

```go
// Package prober pincha las URLs públicas de los servicios.
package prober

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
)

type Prober struct {
	clk  clock.Clock
	http *http.Client
}

func New(clk clock.Clock, timeout time.Duration) *Prober {
	return &Prober{
		clk: clk,
		http: &http.Client{
			Timeout: timeout,
			// No seguir redirecciones: un 301 ya prueba que el servicio está
			// vivo, y seguirlo puede terminar pegándole a un tercero.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Probe hace un GET y clasifica el resultado. Nunca devuelve error: una falla
// del probe ES el dato.
func (p *Prober) Probe(ctx context.Context, servicio, url string) model.ProbeResult {
	r := model.ProbeResult{TS: p.clk.Now(), Servicio: servicio}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.Error = fmt.Sprintf("url inválida: %v", err)
		return r
	}
	req.Header.Set("User-Agent", "server-status")

	inicio := time.Now()
	resp, err := p.http.Do(req)
	r.Latencia = time.Since(inicio)

	if err != nil {
		// Sin respuesta: DNS, TCP, TLS o timeout.
		r.Error = err.Error()
		return r
	}
	defer resp.Body.Close()

	r.StatusCode = resp.StatusCode
	// 2xx y 3xx cuentan como vivo.
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		r.OK = true
		return r
	}
	r.Error = fmt.Sprintf("HTTP %s", resp.Status)
	return r
}
```

La latencia sale de `time.Since` y no del reloj inyectado a propósito: es una medición de duración real, no una marca de tiempo lógica. El `TS` sí usa el reloj.

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./internal/prober/ -v`
Expected: PASS, 5 tests

- [ ] **Step 6: Commit**

```bash
git add internal/prober internal/model
git commit -m "feat: prober HTTP que clasifica el estado de un servicio"
```

---

## Task 9: Migración 0003 — incidentes y resultados de probes

**Files:**
- Modify: `internal/model/model.go`, `internal/store/store.go`, `internal/store/store_test.go`

- [ ] **Step 1: Agregar el tipo**

Agregar a `internal/model/model.go`:

```go
// Incidente es algo que está mal, con su sujeto y su ventana de tiempo.
// Es el único estado persistente del sistema de reglas.
type Incidente struct {
	ID        int64
	Sujeto    string // 'service:comm-tool' | 'host:disk' | 'container:supabase-db'
	Tipo      string // down | unhealthy | threshold | flapping
	Severidad string // critical | warning
	AbiertoEn time.Time
	CerradoEn *time.Time // nil mientras siga abierto
	Detalle   string
}
```

- [ ] **Step 2: Escribir el test que falla**

Agregar a `internal/store/store_test.go`:

```go
func TestAbrirYCerrarIncidente(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	id, err := s.AbrirIncidente(model.Incidente{
		Sujeto: "service:comm-tool", Tipo: "down", Severidad: "critical",
		AbiertoEn: ts, Detalle: "3 fallas seguidas",
	})
	if err != nil {
		t.Fatalf("AbrirIncidente: %v", err)
	}

	abiertos, err := s.IncidentesAbiertos()
	if err != nil {
		t.Fatalf("IncidentesAbiertos: %v", err)
	}
	if len(abiertos) != 1 {
		t.Fatalf("hay %d abiertos, quería 1", len(abiertos))
	}
	if abiertos[0].Sujeto != "service:comm-tool" || abiertos[0].CerradoEn != nil {
		t.Errorf("incidente = %+v", abiertos[0])
	}

	if err := s.CerrarIncidente(id, ts.Add(10*time.Minute)); err != nil {
		t.Fatalf("CerrarIncidente: %v", err)
	}

	abiertos, err = s.IncidentesAbiertos()
	if err != nil {
		t.Fatal(err)
	}
	if len(abiertos) != 0 {
		t.Errorf("quedaron %d abiertos después de cerrar", len(abiertos))
	}
}

// Esta es la invariante 2 del spec, y la hace cumplir la base, no el código.
// Si se rompe el índice, "el incidente de este servicio" pasa a depender del
// orden del SELECT.
func TestNoSePuedenAbrirDosIncidentesDelMismoSujeto(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	inc := model.Incidente{
		Sujeto: "host:disk", Tipo: "threshold", Severidad: "warning",
		AbiertoEn: ts, Detalle: "82%",
	}

	if _, err := s.AbrirIncidente(inc); err != nil {
		t.Fatalf("primer AbrirIncidente: %v", err)
	}
	if _, err := s.AbrirIncidente(inc); err == nil {
		t.Fatal("se abrió un segundo incidente del mismo sujeto: el índice único no está haciendo su trabajo")
	}
}

// Cerrado el primero, el mismo sujeto puede volver a abrir. Si el índice
// fuera sobre el sujeto a secas, esto fallaría.
func TestElMismoSujetoPuedeReabrirDespuesDeCerrar(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	inc := model.Incidente{
		Sujeto: "host:disk", Tipo: "threshold", Severidad: "warning",
		AbiertoEn: ts, Detalle: "82%",
	}

	id, err := s.AbrirIncidente(inc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CerrarIncidente(id, ts.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	inc.AbiertoEn = ts.Add(2 * time.Hour)
	if _, err := s.AbrirIncidente(inc); err != nil {
		t.Fatalf("no se pudo reabrir después de cerrar: %v", err)
	}
}

func TestInsertProbeResults(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)

	if err := s.InsertProbeResults([]model.ProbeResult{
		{TS: ts, Servicio: "comm-tool", OK: true, StatusCode: 200, Latencia: 180 * time.Millisecond},
		{TS: ts, Servicio: "sitio", OK: false, StatusCode: 502, Latencia: 2 * time.Second, Error: "HTTP 502 Bad Gateway"},
	}); err != nil {
		t.Fatalf("InsertProbeResults: %v", err)
	}

	got, err := s.UltimoEstadoProbes()
	if err != nil {
		t.Fatalf("UltimoEstadoProbes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d, quería 2", len(got))
	}
	porServicio := map[string]model.ProbeResult{}
	for _, r := range got {
		porServicio[r.Servicio] = r
	}
	if s := porServicio["sitio"]; s.OK || s.StatusCode != 502 || s.Latencia != 2*time.Second {
		t.Errorf("sitio = %+v", s)
	}
}
```

⚠️ Actualizar los tests de versión de esquema a `3`.

- [ ] **Step 3: Correr el test y verificar que falla**

Run: `go test ./internal/store/`
Expected: FAIL — `s.AbrirIncidente undefined`

- [ ] **Step 4: Implementar**

Agregar al final del slice `migraciones`:

```go
	`CREATE TABLE probe_results (
		ts          INTEGER NOT NULL,
		service     TEXT    NOT NULL,
		ok          INTEGER NOT NULL,
		status_code INTEGER NOT NULL,
		latency_ms  INTEGER NOT NULL,
		error       TEXT    NOT NULL,
		PRIMARY KEY (ts, service)
	) STRICT;

	CREATE TABLE incidents (
		id        INTEGER PRIMARY KEY,
		subject   TEXT    NOT NULL,
		kind      TEXT    NOT NULL,
		severity  TEXT    NOT NULL,
		opened_at INTEGER NOT NULL,
		closed_at INTEGER,
		detail    TEXT    NOT NULL
	) STRICT;

	CREATE UNIQUE INDEX incidentes_abierto_unico
		ON incidents(subject) WHERE closed_at IS NULL;`,
```

Una migración puede tener varias sentencias: el runner las corre con un solo `Exec` dentro de su transacción.

Y los métodos:

```go
// AbrirIncidente falla si ya hay uno abierto para el mismo sujeto — lo impide
// incidentes_abierto_unico, y esa negativa es una feature.
func (s *Store) AbrirIncidente(i model.Incidente) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO incidents (subject, kind, severity, opened_at, closed_at, detail)
		VALUES (?,?,?,?,NULL,?)`,
		i.Sujeto, i.Tipo, i.Severidad, i.AbiertoEn.Unix(), i.Detalle)
	if err != nil {
		return 0, fmt.Errorf("abrir incidente de %s: %w", i.Sujeto, err)
	}
	return res.LastInsertId()
}

func (s *Store) CerrarIncidente(id int64, cuando time.Time) error {
	_, err := s.db.Exec(
		`UPDATE incidents SET closed_at = ? WHERE id = ? AND closed_at IS NULL`,
		cuando.Unix(), id)
	return err
}

func (s *Store) IncidentesAbiertos() ([]model.Incidente, error) {
	return s.consultarIncidentes(`
		SELECT id, subject, kind, severity, opened_at, closed_at, detail
		FROM incidents WHERE closed_at IS NULL ORDER BY opened_at`)
}

func (s *Store) UltimosIncidentes(n int) ([]model.Incidente, error) {
	return s.consultarIncidentes(`
		SELECT id, subject, kind, severity, opened_at, closed_at, detail
		FROM incidents ORDER BY opened_at DESC LIMIT `+strconv.Itoa(n), )
}

func (s *Store) consultarIncidentes(q string) ([]model.Incidente, error) {
	filas, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.Incidente
	for filas.Next() {
		var (
			i        model.Incidente
			abierto  int64
			cerrado  sql.NullInt64
		)
		if err := filas.Scan(&i.ID, &i.Sujeto, &i.Tipo, &i.Severidad, &abierto, &cerrado, &i.Detalle); err != nil {
			return nil, err
		}
		i.AbiertoEn = time.Unix(abierto, 0).UTC()
		if cerrado.Valid {
			t := time.Unix(cerrado.Int64, 0).UTC()
			i.CerradoEn = &t
		}
		out = append(out, i)
	}
	return out, filas.Err()
}

func (s *Store) InsertProbeResults(rs []model.ProbeResult) error {
	if len(rs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO probe_results (ts, service, ok, status_code, latency_ms, error)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(ts, service) DO UPDATE SET
			ok=excluded.ok, status_code=excluded.status_code,
			latency_ms=excluded.latency_ms, error=excluded.error`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rs {
		ok := 0
		if r.OK {
			ok = 1
		}
		if _, err := stmt.Exec(
			r.TS.Truncate(time.Minute).Unix(), r.Servicio, ok,
			r.StatusCode, r.Latencia.Milliseconds(), r.Error,
		); err != nil {
			return fmt.Errorf("insertar probe de %s: %w", r.Servicio, err)
		}
	}
	return tx.Commit()
}

func (s *Store) UltimoEstadoProbes() ([]model.ProbeResult, error) {
	filas, err := s.db.Query(`
		SELECT ts, service, ok, status_code, latency_ms, error
		FROM probe_results
		WHERE ts = (SELECT MAX(ts) FROM probe_results)
		ORDER BY service`)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.ProbeResult
	for filas.Next() {
		var (
			r      model.ProbeResult
			ts, ms int64
			ok     int
		)
		if err := filas.Scan(&ts, &r.Servicio, &ok, &r.StatusCode, &ms, &r.Error); err != nil {
			return nil, err
		}
		r.TS = time.Unix(ts, 0).UTC()
		r.OK = ok == 1
		r.Latencia = time.Duration(ms) * time.Millisecond
		out = append(out, r)
	}
	return out, filas.Err()
}
```

Sumar `"strconv"` a los imports de `store.go` (`database/sql` ya está).

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./internal/store/ -v`
Expected: PASS, 13 tests

- [ ] **Step 6: Commit**

```bash
git add internal/store internal/model
git commit -m "feat: tabla de incidentes con índice único parcial y probe_results"
```

---

## Task 10: Política por conteo

**Files:**
- Create: `internal/rules/politicas.go`, `internal/rules/politicas_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/rules/politicas_test.go`:

```go
package rules_test

import (
	"testing"

	"github.com/juanandresdavila/server-status/internal/rules"
)

func TestDosFallasNoAbrenIncidente(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	for i := range 2 {
		var tr rules.Transicion
		c, tr = p.Aplicar(c, false, false)
		if tr != rules.SinCambio {
			t.Fatalf("falla %d disparó %v, el VPS está a 179 ms y un hipo no es una caída", i+1, tr)
		}
	}
}

func TestLaTerceraFallaAbre(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	c, _ = p.Aplicar(c, false, false)
	c, _ = p.Aplicar(c, false, false)
	_, tr := p.Aplicar(c, false, false)

	if tr != rules.Abre {
		t.Errorf("transición = %v, quería Abre", tr)
	}
}

// Un éxito en el medio resetea: tres fallas SEGUIDAS, no tres fallas.
func TestUnExitoEnElMedioResetea(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	c, _ = p.Aplicar(c, false, false)
	c, _ = p.Aplicar(c, false, false)
	c, _ = p.Aplicar(c, true, false) // se recuperó
	c, _ = p.Aplicar(c, false, false)
	_, tr := p.Aplicar(c, false, false)

	if tr != rules.Abre {
		t.Logf("ok: dos fallas después del éxito todavía no abren")
	}
	if tr == rules.Abre {
		t.Error("abrió con dos fallas seguidas: el éxito no reseteó el contador")
	}
}

func TestUnSoloExitoNoCierra(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	_, tr := p.Aplicar(c, true, true) // abierto = true
	if tr != rules.SinCambio {
		t.Errorf("un solo éxito cerró el incidente: un servicio que rebota no se recuperó")
	}
}

func TestElSegundoExitoCierra(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	c, _ = p.Aplicar(c, true, true)
	_, tr := p.Aplicar(c, true, true)

	if tr != rules.Cierra {
		t.Errorf("transición = %v, quería Cierra", tr)
	}
}

// Con el incidente ya abierto, seguir fallando no vuelve a abrir:
// es "silencio en el medio", la política que eligió el usuario.
func TestConIncidenteAbiertoLasFallasNoDicenNada(t *testing.T) {
	p := rules.PorConteo{FallasParaAbrir: 3, ExitosParaCerrar: 2}
	var c rules.Contador

	for range 10 {
		var tr rules.Transicion
		c, tr = p.Aplicar(c, false, true)
		if tr != rules.SinCambio {
			t.Fatalf("una falla con el incidente abierto disparó %v", tr)
		}
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/rules/`
Expected: FAIL — el paquete no existe

- [ ] **Step 3: Implementar**

`internal/rules/politicas.go`:

```go
// Package rules decide cuándo algo pasa de sano a caído y al revés.
//
// Las políticas son funciones puras sobre un estado explícito: es lo único del
// sistema que puede mandar un mensaje a las 3 de la mañana, así que tiene que
// poder testearse sin base y sin red.
package rules

import "time"

type Transicion int

const (
	SinCambio Transicion = iota
	Abre
	Cierra
)

func (t Transicion) String() string {
	switch t {
	case Abre:
		return "Abre"
	case Cierra:
		return "Cierra"
	default:
		return "SinCambio"
	}
}

// Contador lleva las rachas de un sujeto.
type Contador struct {
	Fallas int
	Exitos int
}

// PorConteo se usa con los sujetos que se prueban: servicios y containers.
type PorConteo struct {
	FallasParaAbrir  int
	ExitosParaCerrar int
}

// Aplicar consume un resultado y devuelve el contador nuevo más la transición.
// `abierto` dice si el sujeto ya tiene un incidente abierto.
func (p PorConteo) Aplicar(c Contador, ok bool, abierto bool) (Contador, Transicion) {
	if ok {
		// Una racha se corta con un solo resultado del otro signo:
		// son fallas SEGUIDAS, no fallas acumuladas.
		c.Fallas = 0
		c.Exitos++
		if abierto && c.Exitos >= p.ExitosParaCerrar {
			return Contador{}, Cierra
		}
		return c, SinCambio
	}

	c.Exitos = 0
	c.Fallas++
	if !abierto && c.Fallas >= p.FallasParaAbrir {
		return Contador{}, Abre
	}
	return c, SinCambio
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/rules/ -v`
Expected: PASS, 6 tests

- [ ] **Step 5: Commit**

```bash
git add internal/rules
git commit -m "feat: política por conteo, 3 fallas para abrir y 2 éxitos para cerrar"
```

---

## Task 11: Política por umbral con histéresis

**Files:**
- Modify: `internal/rules/politicas.go`, `internal/rules/politicas_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `politicas_test.go` (sumar `"time"`):

```go
func umbralDisco() rules.PorUmbral {
	return rules.PorUmbral{Abre: 80, Cierra: 75, Sostenido: 5 * time.Minute}
}

func TestCruzarElUmbralNoAlcanzaSiNoSeSostiene(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	e, tr := p.Aplicar(e, 82, t0, false)
	if tr != rules.SinCambio {
		t.Fatalf("abrió apenas cruzó el umbral, sin esperar los 5 min")
	}
	_, tr = p.Aplicar(e, 82, t0.Add(4*time.Minute), false)
	if tr != rules.SinCambio {
		t.Errorf("abrió a los 4 min, el mínimo son 5")
	}
}

func TestSostenidoElTiempoSuficienteAbre(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	e, _ = p.Aplicar(e, 82, t0, false)
	_, tr := p.Aplicar(e, 82, t0.Add(5*time.Minute), false)

	if tr != rules.Abre {
		t.Errorf("transición = %v, quería Abre", tr)
	}
}

// Bajar del umbral resetea el cronómetro: si no, un pico de disco a las 9 y
// otro a las 18 se sumarían y abrirían un incidente que nunca existió.
func TestBajarDelUmbralReseteaElCronometro(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	e, _ = p.Aplicar(e, 82, t0, false)
	e, _ = p.Aplicar(e, 60, t0.Add(time.Minute), false) // bajó
	e, _ = p.Aplicar(e, 82, t0.Add(2*time.Minute), false)
	_, tr := p.Aplicar(e, 82, t0.Add(6*time.Minute), false)

	if tr == rules.Abre {
		t.Error("abrió a los 4 min del segundo cruce: el cronómetro no se reseteó")
	}
}

// Esta es la razón de ser de la histéresis: un disco parado en 79,8% no puede
// mandar cuarenta mensajes por noche.
func TestQuedarseJustoDebajoDelUmbralNoCierra(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	_, tr := p.Aplicar(e, 79.8, t0, true) // incidente abierto
	if tr == rules.Cierra {
		t.Error("cerró con 79,8%: el cierre es al 75%, justamente para no flapear")
	}
}

func TestBajarDelUmbralDeCierreCierra(t *testing.T) {
	p := umbralDisco()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var e rules.EstadoUmbral

	_, tr := p.Aplicar(e, 74, t0, true)
	if tr != rules.Cierra {
		t.Errorf("transición = %v, quería Cierra con 74%% contra un cierre de 75%%", tr)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/rules/ -run Umbral`
Expected: FAIL — `rules.PorUmbral undefined`

- [ ] **Step 3: Implementar**

Agregar a `internal/rules/politicas.go`:

```go
// PorUmbral se usa con las métricas del host, donde "más alto es peor".
// Abre y Cierra son distintos a propósito: sin esa histéresis, un valor
// parado en el borde abre y cierra sin parar.
type PorUmbral struct {
	Abre      float64
	Cierra    float64
	Sostenido time.Duration
}

// EstadoUmbral recuerda desde cuándo el valor está por encima del umbral.
// En cero significa que no está cruzado.
type EstadoUmbral struct {
	DesdeCuando time.Time
}

func (p PorUmbral) Aplicar(e EstadoUmbral, valor float64, ahora time.Time, abierto bool) (EstadoUmbral, Transicion) {
	if abierto {
		if valor <= p.Cierra {
			return EstadoUmbral{}, Cierra
		}
		return e, SinCambio
	}

	if valor < p.Abre {
		// Volvió a la normalidad: se descarta el cronómetro para que dos
		// picos separados en el tiempo no se sumen.
		return EstadoUmbral{}, SinCambio
	}
	if e.DesdeCuando.IsZero() {
		return EstadoUmbral{DesdeCuando: ahora}, SinCambio
	}
	if ahora.Sub(e.DesdeCuando) >= p.Sostenido {
		return EstadoUmbral{}, Abre
	}
	return e, SinCambio
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/rules/ -v`
Expected: PASS, 11 tests

- [ ] **Step 5: Commit**

```bash
git add internal/rules
git commit -m "feat: política por umbral con histéresis y tiempo sostenido"
```

---

## Task 12: Motor de reglas

**Files:**
- Create: `internal/rules/motor.go`, `internal/rules/motor_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/rules/motor_test.go`:

```go
package rules_test

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/rules"
)

// storeFalso implementa lo mínimo que el motor necesita, para testear la
// lógica sin base.
type storeFalso struct {
	abiertos map[string]model.Incidente
	proximo  int64
	cerrados []int64
}

func nuevoStoreFalso() *storeFalso {
	return &storeFalso{abiertos: map[string]model.Incidente{}, proximo: 1}
}

func (s *storeFalso) IncidentesAbiertos() ([]model.Incidente, error) {
	var out []model.Incidente
	for _, i := range s.abiertos {
		out = append(out, i)
	}
	return out, nil
}

func (s *storeFalso) AbrirIncidente(i model.Incidente) (int64, error) {
	i.ID = s.proximo
	s.proximo++
	s.abiertos[i.Sujeto] = i
	return i.ID, nil
}

func (s *storeFalso) CerrarIncidente(id int64, cuando time.Time) error {
	s.cerrados = append(s.cerrados, id)
	for k, v := range s.abiertos {
		if v.ID == id {
			delete(s.abiertos, k)
		}
	}
	return nil
}

func TestMotorAbreALaTerceraFalla(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	caido := []model.ProbeResult{{Servicio: "comm-tool", OK: false, Error: "HTTP 502"}}

	for range 2 {
		trs, err := m.EvaluarProbes(caido)
		if err != nil {
			t.Fatal(err)
		}
		if len(trs) != 0 {
			t.Fatalf("hubo transiciones antes de la tercera falla: %+v", trs)
		}
		reloj.Advance(time.Minute)
	}

	trs, err := m.EvaluarProbes(caido)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 {
		t.Fatalf("hubo %d transiciones, quería 1", len(trs))
	}
	if trs[0].Tipo != rules.Abre || trs[0].Incidente.Sujeto != "service:comm-tool" {
		t.Errorf("transición = %+v", trs[0])
	}
	if len(st.abiertos) != 1 {
		t.Errorf("no quedó el incidente abierto en el store")
	}
}

func TestMotorCierraCuandoSeRecupera(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	m := rules.NewMotor(st, reloj, rules.Defaults())

	caido := []model.ProbeResult{{Servicio: "comm-tool", OK: false, Error: "HTTP 502"}}
	sano := []model.ProbeResult{{Servicio: "comm-tool", OK: true, StatusCode: 200}}

	for range 3 {
		m.EvaluarProbes(caido)
		reloj.Advance(time.Minute)
	}
	m.EvaluarProbes(sano)
	reloj.Advance(time.Minute)

	trs, err := m.EvaluarProbes(sano)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 || trs[0].Tipo != rules.Cierra {
		t.Fatalf("transiciones = %+v, quería un Cierra", trs)
	}
	if len(st.abiertos) != 0 {
		t.Errorf("el incidente quedó abierto en el store")
	}
}

// Invariante del spec: reiniciar el proceso no reabre nada ni remanda nada.
// Un motor nuevo tiene que ver el incidente que ya está en la base y callarse.
func TestUnMotorNuevoNoReabreLoQueYaEstabaAbierto(t *testing.T) {
	st := nuevoStoreFalso()
	reloj := clock.NewFake(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))

	primero := rules.NewMotor(st, reloj, rules.Defaults())
	caido := []model.ProbeResult{{Servicio: "comm-tool", OK: false, Error: "HTTP 502"}}
	for range 3 {
		primero.EvaluarProbes(caido)
		reloj.Advance(time.Minute)
	}
	if len(st.abiertos) != 1 {
		t.Fatalf("preparación: esperaba 1 incidente abierto, hay %d", len(st.abiertos))
	}

	// "Reinicio": motor nuevo, mismo store.
	segundo := rules.NewMotor(st, reloj, rules.Defaults())
	for range 5 {
		trs, err := segundo.EvaluarProbes(caido)
		if err != nil {
			t.Fatal(err)
		}
		if len(trs) != 0 {
			t.Fatalf("el motor nuevo emitió %+v sobre un incidente que ya estaba abierto", trs)
		}
		reloj.Advance(time.Minute)
	}
}

func TestMotorAbrePorUmbralDeDisco(t *testing.T) {
	st := nuevoStoreFalso()
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	reloj := clock.NewFake(t0)
	m := rules.NewMotor(st, reloj, rules.Defaults())

	lleno := model.HostSample{
		DiskUsedBytes: 85, DiskTotalBytes: 100,
		MemUsedBytes: 1, MemTotalBytes: 100,
		SwapUsedBytes: 0, SwapTotalBytes: 100,
		Load1: 0.1,
	}

	if trs, _ := m.EvaluarHost(lleno); len(trs) != 0 {
		t.Fatalf("abrió sin esperar el sostenido: %+v", trs)
	}
	reloj.Advance(6 * time.Minute)

	trs, err := m.EvaluarHost(lleno)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 || trs[0].Incidente.Sujeto != "host:disk" {
		t.Fatalf("transiciones = %+v, quería un Abre de host:disk", trs)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/rules/ -run Motor`
Expected: FAIL — `rules.NewMotor undefined`

- [ ] **Step 3: Implementar**

`internal/rules/motor.go`:

```go
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
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/rules/ -race -v`
Expected: PASS, 15 tests

- [ ] **Step 5: Commit**

```bash
git add internal/rules
git commit -m "feat: motor que aplica las políticas y persiste los incidentes"
```

---

## Task 13: Wiring y comando `incidents`

**Files:**
- Modify: `cmd/server-status/main.go`, `internal/config/config.go`, `deploy/config.example.yaml`

- [ ] **Step 1: Sumar el timeout de probe a la config**

Agregar el campo al `Config`:

```go
	ProbeTimeout time.Duration `yaml:"probe_timeout"`
```

Y el default en `Load`:

```go
	if c.ProbeTimeout == 0 {
		c.ProbeTimeout = 10 * time.Second
	}
```

- [ ] **Step 2: Sumar los probes y la evaluación al loop**

En `cmd/server-status/main.go`, agregar los imports de `prober` y `rules`, y en `correr`, antes del `for`:

```go
	pr := prober.New(clock.Real{}, cfg.ProbeTimeout)
	motor := rules.NewMotor(s, clock.Real{}, rules.Defaults())
```

Dentro de `case <-persistencia.C:`, después de guardar los containers:

```go
			// Probes: uno por servicio, en paralelo.
			resultados := make([]model.ProbeResult, len(cfg.Servicios))
			var wg sync.WaitGroup
			for i, srv := range cfg.Servicios {
				wg.Add(1)
				go func(i int, srv config.Servicio) {
					defer wg.Done()
					resultados[i] = pr.Probe(ctx, srv.Nombre, srv.Probe)
				}(i, srv)
			}
			wg.Wait()

			if err := s.InsertProbeResults(resultados); err != nil {
				slog.Error("no se pudieron guardar los probes", "err", err)
			}

			cambios, err := motor.EvaluarProbes(resultados)
			if err != nil {
				slog.Error("fallo el motor de reglas sobre los probes", "err", err)
			}
			deHost, err := motor.EvaluarHost(m)
			if err != nil {
				slog.Error("fallo el motor de reglas sobre el host", "err", err)
			}
			// El plan 3 reemplaza este log por el aviso de Telegram.
			for _, c := range append(cambios, deHost...) {
				slog.Info("incidente", "transicion", c.Tipo.String(),
					"sujeto", c.Incidente.Sujeto, "detalle", c.Incidente.Detalle)
			}
```

Sumar `"sync"` a los imports.

- [ ] **Step 3: Sumar el comando**

Agregar al `switch`:

```go
	case "incidents":
		return listarIncidentes(cfg)
```

Y la función:

```go
func listarIncidentes(cfg config.Config) error {
	s, err := store.Open(cfg.Base)
	if err != nil {
		return err
	}
	defer s.Close()

	is, err := s.UltimosIncidentes(20)
	if err != nil {
		return err
	}
	if len(is) == 0 {
		fmt.Println("sin incidentes registrados")
		return nil
	}
	for _, i := range is {
		estado := "ABIERTO"
		if i.CerradoEn != nil {
			estado = "cerrado " + i.CerradoEn.Local().Format("15:04")
		}
		fmt.Printf("%-8s %-28s %-10s %s  (%s)\n",
			estado, i.Sujeto, i.Severidad,
			i.AbiertoEn.Local().Format("02/01 15:04"), i.Detalle)
	}
	return nil
}
```

Actualizar el mensaje del `default` del switch con los cuatro comandos.

- [ ] **Step 4: Verificar todo**

```bash
go vet ./...
go test ./... -race
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/server-status
```

Expected: todo en verde.

- [ ] **Step 5: Commit**

```bash
git add cmd internal/config deploy
git commit -m "feat: probes y motor de reglas en el loop, más el comando incidents"
```

---

## Task 14: Verificar contra el VPS

**Files:** ninguno — es verificación en el servidor.

- [ ] **Step 1: Actualizar la config del servidor con los servicios**

```bash
scp deploy/config.example.yaml vps:/tmp/config.yaml
ssh vps 'sudo install -m 0644 -o root -g root /tmp/config.yaml /etc/server-status/config.yaml && rm /tmp/config.yaml'
```

- [ ] **Step 2: Confirmar que cada probe devuelve 200**

Esto es lo que el spec marcó como pendiente: los paths de Supabase están sin confirmar.

```bash
ssh vps 'for u in https://jadd.com.ar/ https://comm.jadd.com.ar/health https://supabase-sm.jadd.com.ar/auth/v1/health https://supabase-gym.jadd.com.ar/auth/v1/health; do printf "%-52s %s\n" "$u" "$(curl -s -o /dev/null -w "%{http_code}" --max-time 10 "$u")"; done'
```

Expected: los cuatro en 2xx o 3xx. **Si alguno da 404, se corrige el path en la config, no en el código.**

- [ ] **Step 3: Deployar y mirar**

```bash
make deploy
ssh vps 'sleep 75; sudo journalctl -u server-status --no-pager -n 20 -o cat'
ssh vps '/usr/local/bin/server-status -config /etc/server-status/config.yaml containers'
```

Expected: el servicio activo y los containers listados.

- [ ] **Step 4: Provocar un incidente de verdad**

`supabase-gym-studio` es el más seguro de tocar: es la interfaz de administración, no está en el camino de ninguna app.

```bash
ssh vps 'docker stop supabase-gym-studio'
```

Esperar cuatro minutos y mirar:

```bash
ssh vps 'sleep 240; /usr/local/bin/server-status -config /etc/server-status/config.yaml incidents'
```

Expected: nada todavía, porque `supabase-gym-studio` no tiene probe propio — el probe de `gym-tracker` pega contra Kong, que sigue arriba. **Eso es correcto y hay que verificarlo así**: confirma que un container caído que no afecta el servicio no dispara un incidente. La detección por container llega con el plan 3.

Para provocar un incidente de servicio de verdad, parar Kong:

```bash
ssh vps 'docker stop supabase-gym-kong && sleep 240 && /usr/local/bin/server-status -config /etc/server-status/config.yaml incidents'
```

Expected: un incidente `ABIERTO` de `service:gym-tracker`, severidad `critical`.

- [ ] **Step 5: Verificar que cierra solo**

```bash
ssh vps 'docker start supabase-gym-kong supabase-gym-studio && sleep 180 && /usr/local/bin/server-status -config /etc/server-status/config.yaml incidents'
```

Expected: el incidente de `service:gym-tracker` figura `cerrado HH:MM`.

⚠️ **Si el incidente no cierra**, mirar `sudo journalctl -u server-status -n 50` antes de tocar código: lo más probable es que Kong tarde más de lo esperado en volver a responder, no que la máquina de estados esté mal.

- [ ] **Step 6: Verificar que un reinicio no reabre nada**

```bash
ssh vps 'sudo systemctl restart server-status && sleep 90 && /usr/local/bin/server-status -config /etc/server-status/config.yaml incidents'
```

Expected: la misma lista, sin incidentes nuevos.

- [ ] **Step 7: Commit de cierre**

```bash
git commit --allow-empty -m "chore: fases 2 y 3 verificadas contra el VPS"
```

---

## Autorevisión del plan

**Cobertura del spec (fases 2 y 3):**

| Requisito | Tarea |
|---|---|
| Cliente de Docker a mano, cuatro endpoints (§4) | 1, 2, 3 |
| `stream=false` obligatorio en stats | 3 |
| Memoria descontando page cache, cgroup v2 (§4) | 3 |
| Concurrencia con límite 8 (§4) | 4, 6 |
| `container_samples` (§5) | 5 |
| Probes por URL pública, nunca localhost (§3) | 7, 8, 14 |
| `probe_results` (§5) | 9 |
| `incidents` + `incidentes_abierto_unico` (§5, invariante 2) | 9 |
| Familia por conteo: 3 fallas / 2 éxitos (§6) | 10 |
| Familia por umbral con histéresis y sostenido (§6) | 11 |
| Umbrales del spec: 80/75, 90/85, 25/10, 6/4 | 12 (`Defaults()`) |
| Reinicio no reabre ni remanda (§6) | 12, 14 |
| Reloj inyectado, invariante 5 | 8, 12 |
| Confirmar los paths de probe de Supabase (§13, §19) | 14 |

**Fuera de este plan, a propósito:** la amortiguación de rebotes y los incidentes por container van al plan 3, junto con los avisos — son las tres cosas que solo tienen sentido cuando hay un mensaje que mandar. La retención y el `VACUUM INTO` también siguen pendientes.

**Dos correcciones aplicadas durante la revisión**, dejadas como avisos `⚠️` dentro de las tareas porque son errores que un ejecutor distraído repetiría:

1. Task 1: los helpers `contiene`/`indexOf` reinventan `strings.Contains`.
2. Task 3: el fixture de "sin lectura previa" tenía `deltaSys` positivo, así que no ejercitaba la guarda que decía probar.
3. Tasks 5 y 9: los tests de versión de esquema del plan anterior afirman `1` y hay que subirlos a `2` y `3`.

**Consistencia de tipos:** `docker.Container` (tasks 1-4) y `model.ContainerSample` (task 5) son distintos a propósito — el primero es lo que devuelve la API, el segundo es la fila. `rules.Store` (task 12) es un subconjunto de los métodos que `*store.Store` ya expone tras la task 9: `IncidentesAbiertos`, `AbrirIncidente` y `CerrarIncidente`, con las mismas firmas. `Transicion`, `Contador`, `EstadoUmbral`, `PorConteo` y `PorUmbral` se definen en las tasks 10 y 11 y solo los usa el motor.
