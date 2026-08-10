# server-status — Plan de implementación, fases 5 a 7

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Las tres caras de lo que ya se recolecta: un panel privado por tailnet, la portada pública en `status.jadd.com.ar`, y un watchdog externo que avise si se muere todo.

**Architecture:** El panel es HTML renderizado por Go con `html/template`, con ECharts vendoreado y embebido en el binario — cero build de frontend, el deploy sigue siendo un `scp`. La portada pública **no la sirve el proceso**: la escribe como archivo y la sirve Caddy.

**Tech Stack:** Go, `html/template`, `go:embed`, ECharts (Apache-2.0, vendoreado), Caddy, Healthchecks.io.

**Spec:** `docs/superpowers/specs/2026-08-08-server-status-design.md`

---

## Un cambio de orden respecto del spec

El spec numeraba: 5 panel, 6 watchdog, 7 portada. **Acá van 5 panel, 6 portada, 7 watchdog**, porque el watchdog verifica que la portada pública responda y esté fresca: construirlo antes que la portada sería escribir un chequeo contra algo que no existe.

---

## Estructura de archivos

| Archivo | Responsabilidad |
|---|---|
| `internal/web/servidor.go` | El HTTP del panel, con reintento de bind |
| `internal/web/panel.go` | Handlers del panel y de la API de series |
| `internal/web/publica.go` | Render de la portada pública, por lista blanca |
| `internal/web/plantillas/*.html` | Las plantillas, embebidas con `go:embed` |
| `internal/web/assets/` | ECharts vendoreado + su LICENSE |
| `internal/watchdog/watchdog.go` | Auto-consulta pública y latido a Healthchecks |
| `internal/store/store.go` | Consultas de series para los gráficos |

---

## Task 1: Series para los gráficos

**Files:** `internal/store/store.go`, `internal/store/store_test.go`

- [ ] **Step 1: Escribir el test que falla**

```go
func TestSerieHostDevuelvePuntosOrdenados(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		if err := s.InsertHostSample(muestra(base.Add(time.Duration(i)*time.Minute), float64(i*10))); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.SerieHost(base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SerieHost: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("volvieron %d puntos, quería 5", len(got))
	}
	// De la más VIEJA a la más nueva: un gráfico se dibuja hacia adelante.
	if got[0].CPUPctAvg != 0 || got[4].CPUPctAvg != 40 {
		t.Errorf("orden equivocado: primero=%v último=%v", got[0].CPUPctAvg, got[4].CPUPctAvg)
	}
}

func TestSerieHostRecortaPorRango(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for i := range 10 {
		s.InsertHostSample(muestra(base.Add(time.Duration(i)*time.Minute), float64(i)))
	}

	got, err := s.SerieHost(base.Add(2*time.Minute), base.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("volvieron %d puntos, quería 4 (minutos 2 a 5 inclusive)", len(got))
	}
}
```

- [ ] **Step 2: Verificar que falla**

Run: `go test ./internal/store/ -run Serie` — FAIL, `s.SerieHost undefined`

- [ ] **Step 3: Implementar**

```go
// SerieHost devuelve las muestras de un rango, de la más vieja a la más nueva.
// El orden importa: un gráfico se dibuja hacia adelante en el tiempo.
func (s *Store) SerieHost(desde, hasta time.Time) ([]model.HostSample, error) {
	filas, err := s.db.Query(`
		SELECT ts, cpu_pct_avg, cpu_pct_max, load1, load5, load15,
		       mem_used_bytes, mem_total_bytes, swap_used_bytes, swap_total_bytes,
		       disk_used_bytes, disk_total_bytes, net_rx_bytes, net_tx_bytes,
		       uptime_seconds
		FROM host_samples WHERE ts >= ? AND ts <= ? ORDER BY ts ASC`,
		desde.Unix(), hasta.Unix())
	if err != nil {
		return nil, err
	}
	defer filas.Close()
	return escanearHostSamples(filas)
}
```

Y extraer el cuerpo del `for filas.Next()` de `UltimasHostSamples` a una función
`escanearHostSamples(filas *sql.Rows) ([]model.HostSample, error)`, usada por
las dos. Es el mismo escaneo de quince columnas: duplicarlo garantiza que un
día diverjan.

- [ ] **Step 4: Verificar y commitear**

Run: `go test ./internal/store/ -v` — PASS

```bash
git add internal/store
git commit -m "feat: consulta de series para los graficos del panel"
```

---

## Task 2: Servidor del panel, con reintento de bind

**Files:** `internal/web/servidor.go`, `internal/web/servidor_test.go`

- [ ] **Step 1: Escribir el test que falla**

```go
package web_test

import (
	"net"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/web"
)

// El panel escucha en la IP de tailnet, que al arrancar la máquina puede no
// existir todavía: tailscaled tarda en asignarla. Un bind que falla no puede
// tumbar el proceso — es un monitor, y el panel es su parte menos importante.
func TestEscucharReintentaYNuncaEsFatal(t *testing.T) {
	// 203.0.113.1 es de la red de documentación (RFC 5737): nunca va a existir.
	hecho := make(chan error, 1)
	go func() { hecho <- web.Escuchar("203.0.113.1:8090", nil, 300*time.Millisecond) }()

	select {
	case err := <-hecho:
		if err == nil {
			t.Fatal("Escuchar devolvió nil contra una IP inexistente")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Escuchar no volvió: se quedó reintentando para siempre")
	}
}

func TestEscucharSirveCuandoLaDireccionExiste(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dir := ln.Addr().String()
	ln.Close()

	go web.Escuchar(dir, http.NewServeMux(), 5*time.Second)

	// Espera activa corta: el bind tarda milisegundos.
	var c net.Conn
	for range 50 {
		if c, err = net.DialTimeout("tcp", dir, 100*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nunca escuchó en %s: %v", dir, err)
}
```

- [ ] **Step 2: Verificar que falla** — `web.Escuchar undefined`

- [ ] **Step 3: Implementar**

```go
// Package web sirve el panel privado y renderiza la portada pública.
package web

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Escuchar ata el panel a una dirección, reintentando mientras no exista.
//
// El panel escucha en la IP de tailnet, que al arrancar la máquina puede no
// estar asignada todavía: es la misma carrera que en el VPS se resolvió con
// FreeBind para Cockpit. Acá se resuelve reintentando, que es portable y no
// necesita build tags.
//
// Nunca es fatal para el proceso: quien lo llama lo hace en una goroutine y
// loguea el error. Un monitor sin panel sigue sirviendo; un monitor muerto no.
func Escuchar(direccion string, h http.Handler, plazo time.Duration) error {
	limite := time.Now().Add(plazo)
	var ultimo error

	for time.Now().Before(limite) {
		ln, err := net.Listen("tcp", direccion)
		if err == nil {
			slog.Info("panel escuchando", "direccion", direccion)
			srv := &http.Server{
				Handler:           h,
				ReadHeaderTimeout: 10 * time.Second,
			}
			return srv.Serve(ln)
		}
		ultimo = err
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("no se pudo escuchar en %s en %s: %w", direccion, plazo, ultimo)
}
```

- [ ] **Step 4: Verificar y commitear**

```bash
go test ./internal/web/ -race -v
git add internal/web && git commit -m "feat: servidor del panel con reintento de bind"
```

---

## Task 3: ECharts vendoreado

**Files:** `internal/web/assets/echarts.min.js`, `internal/web/assets/LICENSE-echarts.txt`, `internal/web/assets.go`

- [ ] **Step 1: Bajar la librería y su licencia**

```bash
mkdir -p internal/web/assets
curl -fsSL -o internal/web/assets/echarts.min.js \
  https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js
curl -fsSL -o internal/web/assets/LICENSE-echarts.txt \
  https://raw.githubusercontent.com/apache/echarts/master/LICENSE
ls -lh internal/web/assets/
```

**Se vendorea, no se carga de un CDN.** El panel vive en el tailnet: depender de
que el navegador llegue a internet para dibujar un gráfico sería absurdo, y
además le contaría a un tercero cuándo se mira el servidor.

Verificar que el archivo de licencia es el de Apache-2.0 y que el `.js` conserva
su encabezado de copyright — son las dos condiciones de la licencia.

- [ ] **Step 2: Embeber**

`internal/web/assets.go`:

```go
package web

import "embed"

// Los assets viajan adentro del binario: el deploy sigue siendo copiar un solo
// archivo, que es la premisa del proyecto.
//
//go:embed assets
var assets embed.FS

//go:embed plantillas
var plantillas embed.FS
```

- [ ] **Step 3: Verificar y commitear**

```bash
go build ./... && ls -lh $(go env GOCACHE) >/dev/null
git add internal/web && git commit -m "chore: vendorear ECharts con su licencia"
```

---

## Task 4: Página del panel y API de series

**Files:** `internal/web/panel.go`, `internal/web/plantillas/panel.html`, `internal/web/panel_test.go`

- [ ] **Step 1: Escribir el test que falla**

```go
func TestPanelMuestraLoRecolectado(t *testing.T) {
	h := web.NuevoPanel(datosFalsos())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != 200 {
		t.Fatalf("código = %d", rec.Code)
	}
	cuerpo := rec.Body.String()
	for _, quiero := range []string{"comm-tool", "supabase-db", "15.7", "server-status"} {
		if !strings.Contains(cuerpo, quiero) {
			t.Errorf("el panel no muestra %q", quiero)
		}
	}
}

// A diferencia de la portada pública, el panel SÍ puede mostrar todo: vive en
// el tailnet y es para el dueño del servidor.
func TestPanelMuestraLosDatosQueLaPortadaEsconde(t *testing.T) {
	h := web.NuevoPanel(datosFalsos())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if !strings.Contains(rec.Body.String(), "connection refused") {
		t.Error("el panel debería mostrar el error crudo del probe")
	}
}

func TestApiSeriesDevuelveJSON(t *testing.T) {
	h := web.NuevoPanel(datosFalsos())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/series?horas=24", nil))

	if rec.Code != 200 {
		t.Fatalf("código = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var payload struct {
		TS  []int64   `json:"ts"`
		CPU []float64 `json:"cpu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("no es JSON válido: %v", err)
	}
	if len(payload.TS) != len(payload.CPU) {
		t.Errorf("ts y cpu tienen largos distintos: %d vs %d", len(payload.TS), len(payload.CPU))
	}
}

func TestHorasInvalidasCaenAlDefault(t *testing.T) {
	h := web.NuevoPanel(datosFalsos())
	for _, q := range []string{"?horas=abc", "?horas=-5", "?horas=99999", ""} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/series"+q, nil))
		if rec.Code != 200 {
			t.Errorf("con %q el código fue %d, quería 200", q, rec.Code)
		}
	}
}
```

Más un helper `datosFalsos()` que devuelve una implementación de la interfaz
`Datos` con un host sample, dos containers, dos probes (uno con error crudo
`connection refused`) y una serie de tres puntos.

- [ ] **Step 2: Verificar que falla**

- [ ] **Step 3: Implementar**

`internal/web/panel.go` define la interfaz de lo que el panel necesita —
declarada acá y no importada de `store`, por la misma razón que en `rules`:

```go
type Datos interface {
	UltimasHostSamples(n int) ([]model.HostSample, error)
	UltimoEstadoContainers() ([]model.ContainerSample, error)
	UltimoEstadoProbes() ([]model.ProbeResult, error)
	UltimosIncidentes(n int) ([]model.Incidente, error)
	SerieHost(desde, hasta time.Time) ([]model.HostSample, error)
}

func NuevoPanel(d Datos) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", http.FileServerFS(assets))
	mux.HandleFunc("GET /api/series", func(w http.ResponseWriter, r *http.Request) { ... })
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { ... })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { ... })
	return mux
}
```

El parseo de `horas` acota a `[1, 720]` y cae a 24 ante cualquier cosa rara: un
parámetro de query es entrada de afuera aunque el panel sea privado.

La plantilla `panel.html` arma cuatro bloques —host, containers, servicios,
incidentes— y deja cuatro `<div>` vacíos donde ECharts dibuja.

- [ ] **Step 4: Verificar y commitear**

---

## Task 5: Gráficos con ECharts

**Files:** `internal/web/plantillas/panel.html`

- [ ] **Step 1: Sumar el script y la inicialización**

Al final de la plantilla, JavaScript suelto — sin framework y sin build:

```html
<script src="/assets/assets/echarts.min.js"></script>
<script>
  const graficos = [
    { id: 'g-cpu',   titulo: 'CPU %',        campo: 'cpu',   max: 100 },
    { id: 'g-mem',   titulo: 'Memoria %',    campo: 'mem',   max: 100 },
    { id: 'g-disco', titulo: 'Disco %',      campo: 'disco', max: 100 },
    { id: 'g-load',  titulo: 'Carga (1 min)', campo: 'load' },
  ]

  async function dibujar(horas) {
    const r = await fetch(`/api/series?horas=${horas}`)
    const d = await r.json()
    const tiempos = d.ts.map(s => new Date(s * 1000))

    for (const g of graficos) {
      const chart = echarts.init(document.getElementById(g.id))
      chart.setOption({
        title: { text: g.titulo, left: 8, top: 4, textStyle: { fontSize: 13 } },
        grid: { left: 48, right: 16, top: 36, bottom: 48 },
        xAxis: { type: 'time' },
        yAxis: { type: 'value', min: 0, max: g.max },
        tooltip: { trigger: 'axis' },
        // dataZoom es exactamente lo que a mano habría costado caro:
        // acá es una línea.
        dataZoom: [{ type: 'inside' }, { type: 'slider', height: 20, bottom: 8 }],
        series: [{
          type: 'line', showSymbol: false, smooth: true, areaStyle: {},
          data: tiempos.map((t, i) => [t, d[g.campo][i]]),
        }],
      })
      window.addEventListener('resize', () => chart.resize())
    }
  }

  const horas = new URLSearchParams(location.search).get('horas') || 24
  dibujar(horas)
  // El panel se recarga solo: sirve para dejarlo abierto en una pestaña.
  setInterval(() => location.reload(), 60000)
</script>
```

- [ ] **Step 2: Verificar a ojo en el navegador** tras el deploy de la Task 10.

- [ ] **Step 3: Commitear**

---

## Task 6: Portada pública, por lista blanca

**Files:** `internal/web/publica.go`, `internal/web/plantillas/publica.html`, `internal/web/publica_test.go`

- [ ] **Step 1: Escribir el test que falla — este es el test importante del plan**

```go
// La invariante 4 del spec. Se arma por lista blanca y no por lista negra: el
// test alimenta el render con TODO lo que no debe salir y verifica que no
// aparezca. Si alguien mañana agrega un campo a la plantilla, este test lo
// atrapa.
func TestLaPortadaNoFiltraNadaSensible(t *testing.T) {
	estado := web.EstadoPublico{
		Generado: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Servicios: []web.ServicioPublico{
			{Nombre: "comm-tool", OK: true, UptimePct: 99.9},
			{Nombre: "study-master", OK: false, UptimePct: 97.2},
		},
	}

	var b strings.Builder
	if err := web.RenderPublica(&b, estado); err != nil {
		t.Fatalf("RenderPublica: %v", err)
	}
	salida := b.String()

	prohibido := []string{
		"<ip-publica>", "<ip-tailnet>", // IPs (placeholders: los valores reales no entran al repo)
		"supabase-gym-kong", "comm-tool-db", // nombres de containers
		"connection refused", "dial tcp", // errores crudos de probe
		"15.7", "GiB", "load", // métricas del host
		"/var/lib", "/opt/stacks", // rutas
	}
	for _, p := range prohibido {
		if strings.Contains(salida, p) {
			t.Errorf("la portada pública filtra %q", p)
		}
	}
}

func TestLaPortadaMuestraLoQueSiCorresponde(t *testing.T) {
	// nombre del servicio, estado y uptime — nada más.
	...
}

// El watchdog necesita saber si la página está fresca: Caddy sirve feliz un
// archivo viejo si el proceso dejó de escribirlo, y eso daría 200 igual.
func TestLaPortadaLlevaLaMarcaDeFrescura(t *testing.T) {
	momento := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var b strings.Builder
	web.RenderPublica(&b, web.EstadoPublico{Generado: momento})

	marca := fmt.Sprintf("<!--generado:%d-->", momento.Unix())
	if !strings.Contains(b.String(), marca) {
		t.Errorf("falta la marca de frescura %q", marca)
	}
}
```

- [ ] **Step 2: Verificar que falla**

- [ ] **Step 3: Implementar**

`EstadoPublico` y `ServicioPublico` son **tipos propios y chicos**, no
`model.ProbeResult`. Esa es la lista blanca: la plantilla no puede filtrar lo
que no recibe.

```go
// ServicioPublico es lo ÚNICO que sale a internet de cada servicio.
// No es model.ProbeResult a propósito: la lista blanca se hace con el tipo,
// no con la disciplina de quien escribe la plantilla.
type ServicioPublico struct {
	Nombre    string
	OK        bool
	UptimePct float64
}

type EstadoPublico struct {
	Generado  time.Time
	Servicios []ServicioPublico
}
```

- [ ] **Step 4: Verificar y commitear**

---

## Task 7: Escritura atómica y wiring de la portada

**Files:** `internal/web/publica.go`, `cmd/server-status/main.go`, `internal/config/config.go`

- [ ] **Step 1: Escribir el test que falla**

```go
// Caddy lee el archivo mientras el proceso lo escribe. Sin rename atómico,
// un visitante puede recibir media página.
func TestEscribirEsAtomico(t *testing.T) {
	dir := t.TempDir()
	destino := filepath.Join(dir, "index.html")

	if err := web.EscribirPublica(destino, estadoDePrueba()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destino); err != nil {
		t.Fatalf("no se creó el archivo: %v", err)
	}
	// No puede quedar el temporal dando vueltas.
	entradas, _ := os.ReadDir(dir)
	if len(entradas) != 1 {
		t.Errorf("quedaron %d archivos en el directorio, quería 1", len(entradas))
	}
}

func TestEscribirDosVecesPisaSinRomper(t *testing.T) { ... }
```

- [ ] **Step 2: Implementar**

```go
// EscribirPublica renderiza a un temporal y renombra. El rename es atómico
// dentro del mismo filesystem: Caddy nunca ve media página.
func EscribirPublica(destino string, e EstadoPublico) error {
	dir := filepath.Dir(destino)
	tmp, err := os.CreateTemp(dir, ".index-*.html")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op si el rename salió bien

	if err := RenderPublica(tmp, e); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Caddy lo sirve, así que tiene que poder leerlo.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), destino)
}
```

- [ ] **Step 3: Wiring** — un ticker de 30 s en `correr` que arma el
`EstadoPublico` desde el último estado de probes y el uptime del mes, y llama a
`EscribirPublica(cfg.PortadaPath, e)`. Config nueva: `portada_path`,
`url_publica`.

- [ ] **Step 4: Verificar y commitear**

---

## Task 8: Bloque de Caddy en vps-stacks

**Files:** ninguno de este repo — toca `/opt/stacks/edge/` del VPS.

⚠️ **El stack `edge` es por donde entra TODO el tráfico público del VPS.** Un
Caddyfile roto deja `jadd.com.ar`, `comm.jadd.com.ar` y los dos Supabase
afuera. Avisar antes y validar la config antes de recargar.

- [ ] **Step 1: Bind mount** en `/opt/stacks/edge/compose.yaml`, servicio `caddy`:

```yaml
    volumes:
      - /opt/status/public:/srv/status:ro
```

- [ ] **Step 2: Bloque de sitio** en el `Caddyfile`:

```caddyfile
http://status.jadd.com.ar {
	root * /srv/status
	file_server
	# La portada se reescribe cada 30 s; que no la cachee el navegador.
	header Cache-Control "no-store"
}
```

`http://` y no `https://` por el `auto_https off` del stack: el TLS lo termina
Cloudflare.

- [ ] **Step 3: Validar ANTES de aplicar**

```bash
ssh vps 'cd /opt/stacks/edge && sudo docker compose exec caddy caddy validate --config /etc/caddy/Caddyfile'
```

Expected: `Valid configuration`. **Si no valida, no seguir.**

- [ ] **Step 4: Aplicar y verificar que lo viejo sigue vivo**

```bash
ssh vps 'cd /opt/stacks/edge && sudo docker compose up -d caddy && sleep 5
for u in https://jadd.com.ar/ https://comm.jadd.com.ar/health https://status.jadd.com.ar/; do
  printf "%-40s %s\n" "$u" "$(curl -s -o /dev/null -w "%{http_code}" --max-time 10 "$u")"
done'
```

Expected: los tres en 200. **El primero y el segundo importan tanto como el
tercero**: confirman que no se rompió nada.

- [ ] **Step 5: Commitear el cambio en el repo vps-stacks** (es otro repo).

---

## Task 9: Watchdog

**Files:** `internal/watchdog/watchdog.go`, `internal/watchdog/watchdog_test.go`

- [ ] **Step 1: Escribir el test que falla**

```go
// El agujero que este diseño cierra: Healthchecks funciona al revés —vos latís
// y si dejás de latir avisa—, así que si el túnel se cae pero el proceso sigue
// vivo, el latido sale igual y nadie se entera. Por eso el latido depende de
// poder alcanzarse a sí mismo por la URL pública.
func TestNoLateSiNoSeAlcanzaASiMismo(t *testing.T) {
	var lati bool
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { lati = true }))
	defer hc.Close()

	w := watchdog.New("http://127.0.0.1:1/", hc.URL, clock.NewFake(time.Now()), 3*time.Minute)
	if err := w.Latir(context.Background()); err == nil {
		t.Error("no devolvió error con la URL pública caída")
	}
	if lati {
		t.Error("le latió a Healthchecks sin poder alcanzarse a sí mismo")
	}
}

// Y el segundo agujero: un 200 no alcanza. Caddy sirve feliz un archivo viejo
// si el proceso dejó de escribirlo.
func TestNoLateSiLaPaginaEstaVieja(t *testing.T) {
	ahora := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	vieja := ahora.Add(-30 * time.Minute)

	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<html><!--generado:%d--></html>", vieja.Unix())
	}))
	defer pub.Close()

	var lati bool
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { lati = true }))
	defer hc.Close()

	w := watchdog.New(pub.URL, hc.URL, clock.NewFake(ahora), 3*time.Minute)
	if err := w.Latir(context.Background()); err == nil {
		t.Error("no devolvió error con la página vieja")
	}
	if lati {
		t.Error("latió con una página de hace media hora")
	}
}

func TestLateCuandoTodoEstaBien(t *testing.T) { ... }
func TestSinURLDeHealthchecksNoHaceNada(t *testing.T) { ... }
```

- [ ] **Step 2: Implementar** — `Latir` hace el GET de la URL pública, busca
`<!--generado:N-->`, compara contra el reloj inyectado, y solo entonces hace el
GET a la URL de ping.

- [ ] **Step 3: Wiring** — ticker de 5 min en `correr`, con
`HEALTHCHECKS_PING_URL` del entorno. Sin la variable, se saltea con un warning
(mismo patrón que los canales de aviso).

- [ ] **Step 4: Verificar y commitear**

---

## Task 10: Verificación final contra el VPS

- [ ] **Step 1: Config y deploy**

Sumar a `/etc/server-status/config.yaml`:

```yaml
panel_addr: "<ip-tailnet>:8090"
portada_path: /opt/status/public/index.html
url_publica: https://status.jadd.com.ar/
```

```bash
make deploy
```

- [ ] **Step 2: Panel** — abrir `http://<ip-tailnet>:8090` desde una máquina del
tailnet. Verificar los cuatro gráficos, el zoom con el slider, y que los
números coincidan con `server-status sample`.

- [ ] **Step 3: Portada** — `curl -s https://status.jadd.com.ar/ | grep generado`
y confirmar que la marca tiene menos de un minuto.

- [ ] **Step 4: La verificación de la lista blanca, contra la página real**

```bash
curl -s https://status.jadd.com.ar/ | grep -E '192\.99|100\.125|supabase-|dial tcp|GiB' && echo "❌ FILTRA" || echo "✅ limpia"
```

- [ ] **Step 5: Watchdog** — apuntar `url_publica` a una URL rota, confirmar en
el log que **no** late, y restaurar. Es el mismo truco que en los planes 2 y 3:
probar el modo de falla sin romper nada de verdad.

- [ ] **Step 6: Alta en Healthchecks.io** *(usuario)* — crear el check, período
5 min, gracia 15, integración de Telegram, y cargar `HEALTHCHECKS_PING_URL` en
`/etc/server-status/env`. Sin eso el watchdog no sirve de nada.

---

## Autorevisión del plan

**Cobertura del spec:** panel por tailnet (§9) tasks 2, 4, 5; portada por lista
blanca (§9, invariante 4) tasks 6, 7; marca de frescura y auto-consulta (§8)
tasks 6, 9; bloque de Caddy y bind mount (§14) task 8.

**Fuera de este plan:** logs (fase 8) y comandos entrantes (fase 9). También
siguen pendientes la retención y el `VACUUM INTO`, que ya arrastran tres planes
— conviene meterlos en el próximo antes de que la base crezca de más.

**Riesgo más alto: la Task 8.** Es la única que toca el stack por donde entra
todo el tráfico público del VPS. Por eso valida la config antes de recargar y
verifica los sitios viejos además del nuevo.

**Consistencia:** `web.Datos` (task 4) es un subconjunto de lo que `*store.Store`
ya expone tras la task 1. `EstadoPublico`/`ServicioPublico` (task 6) son tipos
nuevos y deliberadamente pobres — son la lista blanca.
