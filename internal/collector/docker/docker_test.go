package docker_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/collector/docker"
)

// servidorFalso levanta un httptest sobre un socket unix, que es como habla
// Docker de verdad.
//
// El directorio va en /tmp y no en t.TempDir(): la ruta de un socket unix no
// puede pasar los ~104 caracteres, y en macOS t.TempDir() devuelve algo como
// /var/folders/xx/.../T/TestNombre123 que se pasa sin avisar. El error que da
// es "invalid argument" en el bind, que no dice nada de longitudes.
func servidorFalso(t *testing.T, h http.Handler) *docker.Client {
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

	return docker.New(socket)
}

// casiIgual compara porcentajes con tolerancia. El cálculo de CPU encadena
// divisiones y multiplicaciones sobre float64, así que 60 sale como
// 60.00000000000001: comparar con != acá es un test que falla por el redondeo
// del hardware y no por la lógica.
func casiIgual(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

const statsOK = `{
	"cpu_stats":    {"cpu_usage":{"total_usage":2000},"system_cpu_usage":100000,"online_cpus":6},
	"precpu_stats": {"cpu_usage":{"total_usage":1000},"system_cpu_usage":90000,"online_cpus":6},
	"memory_stats": {"usage":500000000,"stats":{"inactive_file":100000000}}
}`

func TestListSacaLaBarraDelNombre(t *testing.T) {
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"algo se rompió"}`))
	}))

	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("quería error con un 500, no hubo")
	}
	// El mensaje de Docker tiene que llegar al log: sin él, diagnosticar
	// es adivinar.
	if !strings.Contains(err.Error(), "algo se rompió") {
		t.Errorf("el error no incluye el cuerpo de Docker: %v", err)
	}
}

func TestInspectLeeHealthYReinicios(t *testing.T) {
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestStatsCalculaCPUYMemoria(t *testing.T) {
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "false" {
			t.Errorf("falta stream=false: la API bloquea para siempre sin eso")
		}
		w.Write([]byte(statsOK))
	}))

	got, err := c.Stats(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	// deltaCPU=1000, deltaSys=10000 → 0.1 · 6 cores · 100 = 60%
	if !casiIgual(got.CPUPct, 60) {
		t.Errorf("CPUPct = %v, quería 60", got.CPUPct)
	}
	// La memoria "real" descuenta el page cache reclamable, igual que
	// docker stats. Sin descontarlo, todo container que leyó archivos
	// parece estar comiéndose la RAM.
	if got.MemBytes != 400000000 {
		t.Errorf("MemBytes = %d, quería 400000000", got.MemBytes)
	}
}

// Un container recién arrancado no tiene lectura previa del reloj del sistema:
// deltaSys da 0 y hay que devolver 0, no dividir por cero.
func TestStatsSinDeltaDelSistemaDaCero(t *testing.T) {
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"cpu_stats":    {"cpu_usage":{"total_usage":1000},"system_cpu_usage":90000,"online_cpus":6},
			"precpu_stats": {"cpu_usage":{"total_usage":0},"system_cpu_usage":90000,"online_cpus":0},
			"memory_stats": {"usage":1000,"stats":{"inactive_file":0}}
		}`))
	}))

	got, err := c.Stats(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got.CPUPct != 0 {
		t.Errorf("CPUPct = %v, quería 0 cuando deltaSys es cero", got.CPUPct)
	}
}

func TestRecolectarJuntaTodoYLimitaLaConcurrencia(t *testing.T) {
	var enVuelo, pico int64

	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			w.Write([]byte(statsOK))
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
	if uno.Health != "healthy" || uno.Restarts != 1 || !casiIgual(uno.CPUPct, 60) {
		t.Errorf("uno = %+v, esperaba health=healthy restarts=1 cpu=60", uno)
	}
}

// Un container que falla el inspect no puede tumbar la recolección entera:
// el resto de los datos siguen siendo útiles.
func TestRecolectarSobreviveAUnContainerQueFalla(t *testing.T) {
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			w.Write([]byte(statsOK))
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
}

// Un container apagado no tiene stats y pedirlos da error: hay que saltearlos.
func TestRecolectarNoPideStatsDeContainersApagados(t *testing.T) {
	var statsPedidos int64

	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/json":
			w.Write([]byte(`[{"Id":"c1","Names":["/muerto"],"State":"exited"}]`))
		case strings.HasSuffix(r.URL.Path, "/stats"):
			atomic.AddInt64(&statsPedidos, 1)
			w.Write([]byte(statsOK))
		default:
			w.Write([]byte(`{"RestartCount":7,"State":{}}`))
		}
	}))

	got, err := c.Recolectar(context.Background(), 4)
	if err != nil {
		t.Fatalf("Recolectar: %v", err)
	}
	if n := atomic.LoadInt64(&statsPedidos); n != 0 {
		t.Errorf("se pidieron %d stats de un container apagado", n)
	}
	// El inspect sí se hace: los reinicios son justamente el dato interesante
	// de algo que se murió.
	if got[0].Restarts != 7 {
		t.Errorf("Restarts = %d, quería 7", got[0].Restarts)
	}
}
