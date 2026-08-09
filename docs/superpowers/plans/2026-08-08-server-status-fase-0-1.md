# server-status — Plan de implementación, fases 0 y 1

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Dejar el binario `server-status` corriendo como unit de systemd en el VPS, muestreando las métricas del host cada 15 segundos y persistiendo el agregado por minuto en SQLite.

**Architecture:** Un binario en Go, sin cgo, que corre en el host (no en Docker). Los parsers de `/proc` son funciones puras que reciben un `io.Reader`, así que se testean con fixtures y sin tocar el sistema. El store es SQLite con migraciones numeradas y un solo escritor. El reloj se inyecta en todos lados.

**Tech Stack:** Go (sin cgo), `modernc.org/sqlite`, `gopkg.in/yaml.v3`, systemd, gitleaks.

**Spec:** `docs/superpowers/specs/2026-08-08-server-status-design.md`

---

## Estructura de archivos

| Archivo | Responsabilidad |
|---|---|
| `go.mod`, `Makefile` | Módulo y tareas: test, vet, build, cross-compile a Linux, deploy |
| `.github/workflows/ci.yml` | vet + test + build linux + gitleaks |
| `.githooks/pre-push` | gitleaks **antes** de que nada salga de la máquina |
| `internal/clock/clock.go` | Reloj inyectable. Invariante 5 del spec |
| `internal/model/model.go` | `HostSample`. Tipos compartidos, sin dependencias |
| `internal/collector/host/proc.go` | Parsers puros de `/proc/{stat,meminfo,loadavg,uptime,net/dev}` |
| `internal/collector/host/disk.go` | `statfs`, con build tag para compilar en macOS y Linux |
| `internal/collector/host/host.go` | `Collector` que junta todo en un `model.HostSample` |
| `internal/store/store.go` | Abrir, migrar, insertar, consultar |
| `internal/config/config.go` | Leer el YAML, aplicar defaults, validar |
| `cmd/server-status/main.go` | Único lugar que lee el entorno y arma dependencias. Invariante 9 |
| `deploy/server-status.service` | Unit de systemd con el hardening del §12 del spec |
| `deploy/config.example.yaml` | Config de ejemplo, sin secretos |

**Por qué `internal/model` existe:** si `HostSample` viviera en `store`, el colector tendría que importar el store, y si viviera en `host`, el store tendría que importar el colector. Un paquete de tipos sin dependencias corta el nudo antes de que aparezca.

---

## Task 1: Scaffold del repo

**Files:**
- Create: `go.mod`, `Makefile`, `.github/workflows/ci.yml`, `.githooks/pre-push`, `README.md`

- [ ] **Step 1: Inicializar el módulo**

```bash
cd ~/Projects/server-status
go mod init github.com/juanandresdavila/server-status
```

- [ ] **Step 2: Crear el Makefile**

`Makefile`:

```makefile
BINARY := server-status
LINUX  := dist/$(BINARY)-linux-amd64

.PHONY: test vet build linux deploy hooks

test:
	go test ./... -race

vet:
	go vet ./...

build:
	go build -o $(BINARY) ./cmd/server-status

# CGO_ENABLED=0 no es decorativo: es la invariante 7 del spec.
# Si alguna dependencia necesitara cgo, este build falla y ahí nos enteramos.
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(LINUX) ./cmd/server-status

deploy: linux
	scp $(LINUX) vps:/tmp/server-status
	ssh vps 'sudo install -m 0755 /tmp/server-status /usr/local/bin/server-status && sudo systemctl restart server-status && rm -f /tmp/server-status'

hooks:
	git config core.hooksPath .githooks
```

- [ ] **Step 3: Crear el hook de pre-push**

`.githooks/pre-push`:

```sh
#!/bin/sh
# En un repo público, gitleaks en CI llega tarde: cuando CI falla, el commit
# ya está pusheado y ya es público. Esta es la primera red, no la segunda.
set -e

if ! command -v gitleaks >/dev/null 2>&1; then
	echo "falta gitleaks. Instalalo con: brew install gitleaks" >&2
	exit 1
fi

# gitleaks >= 8.19 usa 'git'; las versiones anteriores, 'detect'.
gitleaks git --no-banner --redact . 2>/dev/null \
	|| gitleaks detect --source . --no-banner --redact
```

```bash
chmod +x .githooks/pre-push
make hooks
```

- [ ] **Step 4: Crear el workflow de CI**

`.github/workflows/ci.yml`:

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: stable
      - run: go vet ./...
      - run: go test ./... -race
      - name: build linux (verifica que no entre cgo)
        run: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/server-status
      - uses: gitleaks/gitleaks-action@v2
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 5: Verificar que gitleaks corre**

```bash
gitleaks git --no-banner --redact . 2>/dev/null || gitleaks detect --source . --no-banner --redact
```

Esperado: termina con código 0 y sin hallazgos. Si dice que falta el comando, `brew install gitleaks` primero.

- [ ] **Step 6: Commit**

```bash
git add go.mod Makefile .github .githooks
git commit -m "chore: scaffold del módulo, CI y hook de pre-push con gitleaks"
```

---

## Task 2: Reloj inyectable

**Files:**
- Create: `internal/clock/clock.go`, `internal/clock/clock_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/clock/clock_test.go`:

```go
package clock_test

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
)

func TestFakeAvanzaSoloCuandoSeLoPide(t *testing.T) {
	inicio := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	f := clock.NewFake(inicio)

	if got := f.Now(); !got.Equal(inicio) {
		t.Fatalf("Now() = %v, quería %v", got, inicio)
	}

	f.Advance(90 * time.Second)

	quiero := inicio.Add(90 * time.Second)
	if got := f.Now(); !got.Equal(quiero) {
		t.Fatalf("después de Advance, Now() = %v, quería %v", got, quiero)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/clock/ -run TestFake -v`
Expected: FAIL — `no required module provides package .../internal/clock`

- [ ] **Step 3: Implementar**

`internal/clock/clock.go`:

```go
// Package clock aísla la lectura del tiempo para que se pueda simular en tests.
// Invariante 5 del spec: nadie llama a time.Now() fuera de este paquete.
package clock

import "time"

type Clock interface {
	Now() time.Time
}

// Real es el reloj del sistema.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Fake es un reloj que solo avanza cuando se lo pide.
type Fake struct{ t time.Time }

func NewFake(t time.Time) *Fake { return &Fake{t: t} }

func (f *Fake) Now() time.Time { return f.t }

func (f *Fake) Advance(d time.Duration) { f.t = f.t.Add(d) }
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/clock/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/clock
git commit -m "feat: reloj inyectable para poder simular el tiempo en tests"
```

---

## Task 3: Tipos compartidos

**Files:**
- Create: `internal/model/model.go`

- [ ] **Step 1: Implementar**

No lleva test propio: es una declaración de tipos sin comportamiento. Lo ejercitan los tests de las tareas 9, 10 y 11.

`internal/model/model.go`:

```go
// Package model tiene los tipos que cruzan paquetes. No importa nada del proyecto
// a propósito: si lo hiciera, volvería a atar el colector con el store.
package model

import "time"

// HostSample es una muestra de las métricas de la máquina, ya agregada al minuto.
type HostSample struct {
	TS time.Time

	CPUPctAvg float64
	CPUPctMax float64

	Load1  float64
	Load5  float64
	Load15 float64

	MemUsedBytes  uint64
	MemTotalBytes uint64

	SwapUsedBytes  uint64
	SwapTotalBytes uint64

	DiskUsedBytes  uint64
	DiskTotalBytes uint64

	NetRxBytes uint64
	NetTxBytes uint64

	Uptime time.Duration
}
```

- [ ] **Step 2: Verificar que compila**

Run: `go build ./...`
Expected: sin salida

- [ ] **Step 3: Commit**

```bash
git add internal/model
git commit -m "feat: tipo HostSample compartido entre colector y store"
```

---

## Task 4: Parser de /proc/stat y cálculo de CPU

**Files:**
- Create: `internal/collector/host/proc.go`, `internal/collector/host/proc_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/collector/host/proc_test.go`:

```go
package host_test

import (
	"strings"
	"testing"

	"github.com/juanandresdavila/server-status/internal/collector/host"
)

// Formato real de /proc/stat. La primera línea agrega todos los cores;
// las que siguen son por core y hay que ignorarlas.
const statFixture = `cpu  100 20 30 800 40 0 10 0 0 0
cpu0 50 10 15 400 20 0 5 0 0 0
cpu1 50 10 15 400 20 0 5 0 0 0
intr 12345
ctxt 67890
`

func TestParseStatSumaTodoYSeparaElOcio(t *testing.T) {
	got, err := host.ParseStat(strings.NewReader(statFixture))
	if err != nil {
		t.Fatalf("ParseStat: %v", err)
	}

	// Total = 100+20+30+800+40+0+10+0+0+0
	if got.Total != 1000 {
		t.Errorf("Total = %d, quería 1000", got.Total)
	}
	// Idle = idle(800) + iowait(40). Un core esperando disco no está trabajando.
	if got.Idle != 840 {
		t.Errorf("Idle = %d, quería 840", got.Idle)
	}
}

func TestParseStatSinLineaCpuDaError(t *testing.T) {
	_, err := host.ParseStat(strings.NewReader("intr 1\nctxt 2\n"))
	if err == nil {
		t.Fatal("quería error cuando no hay línea 'cpu', no hubo")
	}
}

func TestPercentUsaLosDeltas(t *testing.T) {
	prev := host.CPUTimes{Total: 1000, Idle: 840}
	cur := host.CPUTimes{Total: 2000, Idle: 1640}

	// delta total = 1000, delta idle = 800 → ocupado 200 de 1000 = 20%
	if got := host.Percent(prev, cur); got != 20 {
		t.Errorf("Percent = %v, quería 20", got)
	}
}

func TestPercentSinTiempoTranscurridoDaCero(t *testing.T) {
	t.Parallel()
	same := host.CPUTimes{Total: 1000, Idle: 840}
	if got := host.Percent(same, same); got != 0 {
		t.Errorf("Percent = %v, quería 0", got)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/host/ -v`
Expected: FAIL — el paquete no existe todavía

- [ ] **Step 3: Implementar**

`internal/collector/host/proc.go`:

```go
// Package host lee las métricas de la máquina desde /proc y statfs.
// Los parsers son funciones puras sobre un io.Reader: se testean con fixtures
// y no tocan el sistema de archivos.
package host

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// CPUTimes son contadores acumulados del kernel, en jiffies.
// Solos no dicen nada: el porcentaje sale de comparar dos lecturas.
type CPUTimes struct {
	Total uint64
	Idle  uint64
}

// ParseStat lee la línea agregada "cpu" de /proc/stat.
func ParseStat(r io.Reader) (CPUTimes, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		campos := strings.Fields(sc.Text())
		// "cpu0", "cpu1"... son por core. Solo queremos el agregado.
		if len(campos) < 5 || campos[0] != "cpu" {
			continue
		}
		var t CPUTimes
		for i, c := range campos[1:] {
			v, err := strconv.ParseUint(c, 10, 64)
			if err != nil {
				return CPUTimes{}, fmt.Errorf("campo %d de la línea cpu: %w", i, err)
			}
			t.Total += v
			// Índices 3 y 4 son idle e iowait.
			if i == 3 || i == 4 {
				t.Idle += v
			}
		}
		return t, nil
	}
	if err := sc.Err(); err != nil {
		return CPUTimes{}, err
	}
	return CPUTimes{}, errors.New("no se encontró la línea 'cpu' en /proc/stat")
}

// Percent es el uso de CPU entre dos lecturas, de 0 a 100.
func Percent(prev, cur CPUTimes) float64 {
	deltaTotal := cur.Total - prev.Total
	deltaIdle := cur.Idle - prev.Idle
	if deltaTotal == 0 {
		return 0
	}
	return 100 * float64(deltaTotal-deltaIdle) / float64(deltaTotal)
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/host/ -v`
Expected: PASS, 4 tests

- [ ] **Step 5: Commit**

```bash
git add internal/collector/host
git commit -m "feat: parser de /proc/stat y cálculo de uso de CPU"
```

---

## Task 5: Parser de /proc/meminfo

**Files:**
- Modify: `internal/collector/host/proc.go`
- Modify: `internal/collector/host/proc_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/collector/host/proc_test.go`:

```go
const meminfoFixture = `MemTotal:       12000000 kB
MemFree:          260000 kB
MemAvailable:    8400000 kB
Buffers:          100000 kB
SwapTotal:       2097152 kB
SwapFree:        2097100 kB
`

func TestParseMeminfoConvierteAByutes(t *testing.T) {
	got, err := host.ParseMeminfo(strings.NewReader(meminfoFixture))
	if err != nil {
		t.Fatalf("ParseMeminfo: %v", err)
	}

	if got.TotalBytes != 12000000*1024 {
		t.Errorf("TotalBytes = %d, quería %d", got.TotalBytes, 12000000*1024)
	}
	// Usado se calcula contra MemAvailable, no contra MemFree: el page cache
	// figura como ocupado pero el kernel lo suelta cuando hace falta.
	if got.UsedBytes() != (12000000-8400000)*1024 {
		t.Errorf("UsedBytes = %d, quería %d", got.UsedBytes(), (12000000-8400000)*1024)
	}
	if got.SwapUsedBytes() != (2097152-2097100)*1024 {
		t.Errorf("SwapUsedBytes = %d, quería %d", got.SwapUsedBytes(), (2097152-2097100)*1024)
	}
}

func TestParseMeminfoAvisaQueCampoFalta(t *testing.T) {
	_, err := host.ParseMeminfo(strings.NewReader("MemTotal: 100 kB\n"))
	if err == nil {
		t.Fatal("quería error por campos faltantes, no hubo")
	}
	if !strings.Contains(err.Error(), "MemAvailable") {
		t.Errorf("el error debería nombrar el campo que falta, dijo: %v", err)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/host/ -run Meminfo -v`
Expected: FAIL — `host.ParseMeminfo undefined`

- [ ] **Step 3: Implementar**

Agregar a `internal/collector/host/proc.go` (y sumar `"sort"` a los imports):

```go
// Mem son los campos de /proc/meminfo que nos importan, ya en bytes.
type Mem struct {
	TotalBytes     uint64
	AvailableBytes uint64
	SwapTotalBytes uint64
	SwapFreeBytes  uint64
}

func (m Mem) UsedBytes() uint64     { return m.TotalBytes - m.AvailableBytes }
func (m Mem) SwapUsedBytes() uint64 { return m.SwapTotalBytes - m.SwapFreeBytes }

// ParseMeminfo lee /proc/meminfo, que viene en kB.
func ParseMeminfo(r io.Reader) (Mem, error) {
	var m Mem
	falta := map[string]*uint64{
		"MemTotal":     &m.TotalBytes,
		"MemAvailable": &m.AvailableBytes,
		"SwapTotal":    &m.SwapTotalBytes,
		"SwapFree":     &m.SwapFreeBytes,
	}

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		campos := strings.Fields(sc.Text())
		if len(campos) < 2 {
			continue
		}
		clave := strings.TrimSuffix(campos[0], ":")
		destino, queremos := falta[clave]
		if !queremos {
			continue
		}
		v, err := strconv.ParseUint(campos[1], 10, 64)
		if err != nil {
			return Mem{}, fmt.Errorf("%s: %w", clave, err)
		}
		*destino = v * 1024
		delete(falta, clave)
	}
	if err := sc.Err(); err != nil {
		return Mem{}, err
	}
	if len(falta) > 0 {
		nombres := make([]string, 0, len(falta))
		for k := range falta {
			nombres = append(nombres, k)
		}
		sort.Strings(nombres)
		return Mem{}, fmt.Errorf("faltan campos en /proc/meminfo: %s", strings.Join(nombres, ", "))
	}
	return m, nil
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/host/ -v`
Expected: PASS, 6 tests

- [ ] **Step 5: Commit**

```bash
git add internal/collector/host
git commit -m "feat: parser de /proc/meminfo"
```

---

## Task 6: Parsers de /proc/loadavg y /proc/uptime

**Files:**
- Modify: `internal/collector/host/proc.go`
- Modify: `internal/collector/host/proc_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/collector/host/proc_test.go` (y sumar `"time"` a los imports):

```go
func TestParseLoadavg(t *testing.T) {
	got, err := host.ParseLoadavg(strings.NewReader("0.41 0.60 0.53 2/512 12345\n"))
	if err != nil {
		t.Fatalf("ParseLoadavg: %v", err)
	}
	if got.One != 0.41 || got.Five != 0.60 || got.Fifteen != 0.53 {
		t.Errorf("Load = %+v, quería {0.41 0.60 0.53}", got)
	}
}

func TestParseLoadavgTruncadoDaError(t *testing.T) {
	if _, err := host.ParseLoadavg(strings.NewReader("0.41 0.60\n")); err == nil {
		t.Fatal("quería error con menos de 3 campos, no hubo")
	}
}

func TestParseUptime(t *testing.T) {
	got, err := host.ParseUptime(strings.NewReader("13620.50 98765.43\n"))
	if err != nil {
		t.Fatalf("ParseUptime: %v", err)
	}
	quiero := 13620500 * time.Millisecond
	if got != quiero {
		t.Errorf("Uptime = %v, quería %v", got, quiero)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/host/ -run "Loadavg|Uptime" -v`
Expected: FAIL — `host.ParseLoadavg undefined`

- [ ] **Step 3: Implementar**

Agregar a `internal/collector/host/proc.go` (y sumar `"time"` a los imports):

```go
// Load son los tres promedios de carga del kernel.
type Load struct {
	One     float64
	Five    float64
	Fifteen float64
}

// ParseLoadavg lee /proc/loadavg: "0.41 0.60 0.53 2/512 12345".
func ParseLoadavg(r io.Reader) (Load, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return Load{}, err
	}
	campos := strings.Fields(string(b))
	if len(campos) < 3 {
		return Load{}, fmt.Errorf("/proc/loadavg trajo %d campos, esperaba al menos 3", len(campos))
	}
	var l Load
	destinos := []*float64{&l.One, &l.Five, &l.Fifteen}
	for i, d := range destinos {
		v, err := strconv.ParseFloat(campos[i], 64)
		if err != nil {
			return Load{}, fmt.Errorf("campo %d de /proc/loadavg: %w", i, err)
		}
		*d = v
	}
	return l, nil
}

// ParseUptime lee /proc/uptime, cuyo primer campo son segundos con decimales.
func ParseUptime(r io.Reader) (time.Duration, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	campos := strings.Fields(string(b))
	if len(campos) < 1 {
		return 0, errors.New("/proc/uptime vino vacío")
	}
	segundos, err := strconv.ParseFloat(campos[0], 64)
	if err != nil {
		return 0, fmt.Errorf("/proc/uptime: %w", err)
	}
	return time.Duration(segundos * float64(time.Second)), nil
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/host/ -v`
Expected: PASS, 9 tests

- [ ] **Step 5: Commit**

```bash
git add internal/collector/host
git commit -m "feat: parsers de /proc/loadavg y /proc/uptime"
```

---

## Task 7: Parser de /proc/net/dev

**Files:**
- Modify: `internal/collector/host/proc.go`
- Modify: `internal/collector/host/proc_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/collector/host/proc_test.go`:

```go
// Formato real: dos líneas de encabezado y después "iface: rx... tx...".
// El VPS tiene ens3, tailscale0, docker0 y un montón de veth.
const netDevFixture = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 5000 50 0 0 0 0 0 0 6000 60 0 0 0 0 0 0
  ens3: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0
tailscale0: 300 3 0 0 0 0 0 0 400 4 0 0 0 0 0 0
docker0: 999999 99 0 0 0 0 0 0 999999 99 0 0 0 0 0 0
vethabc123: 777 7 0 0 0 0 0 0 777 7 0 0 0 0 0 0
`

func TestParseNetDevSumaSoloLasInterfacesReales(t *testing.T) {
	got, err := host.ParseNetDev(strings.NewReader(netDevFixture))
	if err != nil {
		t.Fatalf("ParseNetDev: %v", err)
	}

	// lo, docker0 y veth* quedan afuera: son tráfico interno y lo contarían doble.
	// Quedan ens3 (1000/2000) y tailscale0 (300/400).
	if got.RxBytes != 1300 {
		t.Errorf("RxBytes = %d, quería 1300", got.RxBytes)
	}
	if got.TxBytes != 2400 {
		t.Errorf("TxBytes = %d, quería 2400", got.TxBytes)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/host/ -run NetDev -v`
Expected: FAIL — `host.ParseNetDev undefined`

- [ ] **Step 3: Implementar**

Agregar a `internal/collector/host/proc.go`:

```go
// Net son los contadores acumulados de tráfico del kernel.
type Net struct {
	RxBytes uint64
	TxBytes uint64
}

// interfazCuenta decide si una interfaz suma al tráfico de la máquina.
// Quedan afuera el loopback y todo lo que crea Docker: ese tráfico ya está
// contado del lado de la interfaz real y contarlo de nuevo infla el número.
func interfazCuenta(nombre string) bool {
	if nombre == "lo" {
		return false
	}
	for _, prefijo := range []string{"veth", "docker", "br-"} {
		if strings.HasPrefix(nombre, prefijo) {
			return false
		}
	}
	return true
}

// ParseNetDev lee /proc/net/dev y suma las interfaces que cuentan.
func ParseNetDev(r io.Reader) (Net, error) {
	var n Net
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		// Las dos líneas de encabezado no tienen ":", así que se caen solas acá.
		nombre, resto, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		nombre = strings.TrimSpace(nombre)
		if !interfazCuenta(nombre) {
			continue
		}
		campos := strings.Fields(resto)
		if len(campos) < 9 {
			continue
		}
		rx, err := strconv.ParseUint(campos[0], 10, 64)
		if err != nil {
			return Net{}, fmt.Errorf("rx de %s: %w", nombre, err)
		}
		tx, err := strconv.ParseUint(campos[8], 10, 64)
		if err != nil {
			return Net{}, fmt.Errorf("tx de %s: %w", nombre, err)
		}
		n.RxBytes += rx
		n.TxBytes += tx
	}
	return n, sc.Err()
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/host/ -v`
Expected: PASS, 10 tests

- [ ] **Step 5: Commit**

```bash
git add internal/collector/host
git commit -m "feat: parser de /proc/net/dev, sin contar interfaces de Docker"
```

---

## Task 8: Uso de disco con statfs

**Files:**
- Create: `internal/collector/host/disk.go`, `internal/collector/host/disk_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/collector/host/disk_test.go`:

```go
package host_test

import (
	"testing"

	"github.com/juanddavila/server-status/internal/collector/host"
)

// statfs habla con el kernel, así que este test mide el disco de verdad de la
// máquina donde corre. No se puede afirmar un número: se afirman las relaciones
// que tienen que valer siempre.
func TestDiskUsageDevuelveValoresCoherentes(t *testing.T) {
	got, err := host.DiskUsage("/")
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if got.TotalBytes == 0 {
		t.Fatal("TotalBytes = 0, un filesystem montado no puede tener tamaño cero")
	}
	if got.UsedBytes > got.TotalBytes {
		t.Errorf("UsedBytes (%d) > TotalBytes (%d)", got.UsedBytes, got.TotalBytes)
	}
}

func TestDiskUsageRutaInexistenteDaError(t *testing.T) {
	if _, err := host.DiskUsage("/no/existe/esta/ruta"); err == nil {
		t.Fatal("quería error con una ruta inexistente, no hubo")
	}
}
```

⚠️ Corregir el import a `github.com/juanandresdavila/server-status/internal/collector/host` — el path del módulo es el de `go.mod` de la Task 1.

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/host/ -run Disk -v`
Expected: FAIL — `host.DiskUsage undefined`

- [ ] **Step 3: Implementar**

`internal/collector/host/disk.go`:

```go
//go:build linux || darwin

package host

import "syscall"

// Disk es el uso del filesystem donde vive una ruta.
type Disk struct {
	TotalBytes uint64
	UsedBytes  uint64
}

// DiskUsage pregunta al kernel por statfs.
//
// El build tag de arriba es lo que permite correr los tests en la Mac: los
// campos de Statfs_t tienen tipos distintos en Linux y en Darwin, y las
// conversiones explícitas a uint64 compilan en los dos.
//
// Usado se calcula como Blocks-Bfree, igual que df. Eso incluye los bloques
// reservados para root, así que el porcentaje puede diferir un punto o dos
// del "Use%" de df, que los descuenta.
func DiskUsage(ruta string) (Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(ruta, &st); err != nil {
		return Disk{}, err
	}
	tam := uint64(st.Bsize)
	total := uint64(st.Blocks) * tam
	libre := uint64(st.Bfree) * tam
	return Disk{TotalBytes: total, UsedBytes: total - libre}, nil
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/host/ -v`
Expected: PASS, 12 tests

Verificar además que cross-compila:

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...`
Expected: sin salida

- [ ] **Step 5: Commit**

```bash
git add internal/collector/host
git commit -m "feat: uso de disco por statfs, compilando en macOS y Linux"
```

---

## Task 9: El colector que junta todo

**Files:**
- Create: `internal/collector/host/host.go`, `internal/collector/host/host_test.go`

- [ ] **Step 1: Escribir el test que falla**

`internal/collector/host/host_test.go`:

```go
package host_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/collector/host"
)

// procFalso arma un directorio con la misma forma que /proc, para que el
// colector se pueda testear sin tocar el /proc de verdad.
func procFalso(t *testing.T, stat string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	archivos := map[string]string{
		"stat":    stat,
		"meminfo": meminfoFixture,
		"loadavg": "0.41 0.60 0.53 2/512 12345\n",
		"uptime":  "13620.50 98765.43\n",
		"net/dev": netDevFixture,
	}
	for nombre, contenido := range archivos {
		if err := os.WriteFile(filepath.Join(dir, nombre), []byte(contenido), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPrimeraMuestraNoTieneCPU(t *testing.T) {
	c := host.NewCollector(procFalso(t, statFixture), "/", clock.NewFake(time.Now()))

	got, err := c.Sample()
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	// Sin lectura anterior no hay delta, y sin delta no hay porcentaje.
	// Reportar 0 es correcto; inventar un número, no.
	if got.CPUPctAvg != 0 {
		t.Errorf("CPUPctAvg de la primera muestra = %v, quería 0", got.CPUPctAvg)
	}
	if got.MemTotalBytes != 12000000*1024 {
		t.Errorf("MemTotalBytes = %d, quería %d", got.MemTotalBytes, 12000000*1024)
	}
	if got.Load1 != 0.41 {
		t.Errorf("Load1 = %v, quería 0.41", got.Load1)
	}
	if got.Uptime != 13620500*time.Millisecond {
		t.Errorf("Uptime = %v, quería %v", got.Uptime, 13620500*time.Millisecond)
	}
}

func TestSegundaMuestraCalculaCPUContraLaPrimera(t *testing.T) {
	dir := procFalso(t, statFixture)
	reloj := clock.NewFake(time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	c := host.NewCollector(dir, "/", reloj)

	if _, err := c.Sample(); err != nil {
		t.Fatalf("primera Sample: %v", err)
	}

	// Segunda lectura: +1000 de total y +800 de ocio → 20% ocupado.
	segundo := "cpu  600 20 30 1600 40 0 10 0 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(segundo), 0o644); err != nil {
		t.Fatal(err)
	}
	reloj.Advance(15 * time.Second)

	got, err := c.Sample()
	if err != nil {
		t.Fatalf("segunda Sample: %v", err)
	}
	if got.CPUPctAvg != 20 {
		t.Errorf("CPUPctAvg = %v, quería 20", got.CPUPctAvg)
	}
	if !got.TS.Equal(reloj.Now()) {
		t.Errorf("TS = %v, quería %v", got.TS, reloj.Now())
	}
}

func TestSampleFallaSiFaltaUnArchivo(t *testing.T) {
	c := host.NewCollector(t.TempDir(), "/", clock.NewFake(time.Now()))
	if _, err := c.Sample(); err == nil {
		t.Fatal("quería error con un /proc vacío, no hubo")
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/collector/host/ -run Muestra -v`
Expected: FAIL — `host.NewCollector undefined`

- [ ] **Step 3: Implementar**

`internal/collector/host/host.go`:

```go
package host

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
)

// Collector lee una muestra completa de la máquina.
// Guarda la lectura anterior de CPU porque el porcentaje sale de un delta.
type Collector struct {
	procDir  string
	discoDir string
	clk      clock.Clock
	prevCPU  *CPUTimes
}

// NewCollector recibe la raíz de /proc como parámetro para que los tests
// puedan armar una falsa. En producción es "/proc".
func NewCollector(procDir, discoDir string, clk clock.Clock) *Collector {
	return &Collector{procDir: procDir, discoDir: discoDir, clk: clk}
}

// Sample lee todo de una. La primera llamada devuelve CPU en 0: sin lectura
// anterior no hay delta que calcular.
func (c *Collector) Sample() (model.HostSample, error) {
	cpu, err := leer(c.procDir, "stat", ParseStat)
	if err != nil {
		return model.HostSample{}, err
	}
	mem, err := leer(c.procDir, "meminfo", ParseMeminfo)
	if err != nil {
		return model.HostSample{}, err
	}
	load, err := leer(c.procDir, "loadavg", ParseLoadavg)
	if err != nil {
		return model.HostSample{}, err
	}
	uptime, err := leer(c.procDir, "uptime", ParseUptime)
	if err != nil {
		return model.HostSample{}, err
	}
	net, err := leer(c.procDir, filepath.Join("net", "dev"), ParseNetDev)
	if err != nil {
		return model.HostSample{}, err
	}
	disco, err := DiskUsage(c.discoDir)
	if err != nil {
		return model.HostSample{}, fmt.Errorf("statfs de %s: %w", c.discoDir, err)
	}

	var pct float64
	if c.prevCPU != nil {
		pct = Percent(*c.prevCPU, cpu)
	}
	c.prevCPU = &cpu

	return model.HostSample{
		TS:             c.clk.Now(),
		CPUPctAvg:      pct,
		CPUPctMax:      pct,
		Load1:          load.One,
		Load5:          load.Five,
		Load15:         load.Fifteen,
		MemUsedBytes:   mem.UsedBytes(),
		MemTotalBytes:  mem.TotalBytes,
		SwapUsedBytes:  mem.SwapUsedBytes(),
		SwapTotalBytes: mem.SwapTotalBytes,
		DiskUsedBytes:  disco.UsedBytes,
		DiskTotalBytes: disco.TotalBytes,
		NetRxBytes:     net.RxBytes,
		NetTxBytes:     net.TxBytes,
		Uptime:         uptime,
	}, nil
}

// leer abre un archivo de /proc y se lo pasa al parser correspondiente.
func leer[T any](dir, nombre string, parse func(r *os.File) (T, error)) (T, error) {
	var cero T
	f, err := os.Open(filepath.Join(dir, nombre))
	if err != nil {
		return cero, fmt.Errorf("abrir %s: %w", nombre, err)
	}
	defer f.Close()
	v, err := parse(f)
	if err != nil {
		return cero, fmt.Errorf("parsear %s: %w", nombre, err)
	}
	return v, nil
}
```

⚠️ La firma genérica de `leer` no compila así: los parsers reciben `io.Reader`, no `*os.File`. Cambiar la declaración por:

```go
func leer[T any](dir, nombre string, parse func(r io.Reader) (T, error)) (T, error) {
```

y agregar `"io"` a los imports. Go infiere `T` de cada parser sin problema.

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/collector/host/ -v`
Expected: PASS, 15 tests

- [ ] **Step 5: Commit**

```bash
git add internal/collector/host
git commit -m "feat: colector que arma una muestra completa del host"
```

---

## Task 10: Store — abrir y migrar

**Files:**
- Create: `internal/store/store.go`, `internal/store/store_test.go`

- [ ] **Step 1: Agregar la dependencia**

```bash
go get modernc.org/sqlite
```

- [ ] **Step 2: Escribir el test que falla**

`internal/store/store_test.go`:

```go
package store_test

import (
	"path/filepath"
	"testing"

	"github.com/juanandresdavila/server-status/internal/store"
)

// Los tests usan un archivo en un directorio temporal en vez de :memory:,
// porque así se ejercita también el modo WAL, que es como corre en producción.
func abrir(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAplicaLasMigraciones(t *testing.T) {
	s := abrir(t)
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 1 {
		t.Errorf("versión = %d, quería 1", v)
	}
}

func TestOpenDosVecesNoRompe(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "test.db")

	s1, err := store.Open(ruta)
	if err != nil {
		t.Fatalf("primer Open: %v", err)
	}
	s1.Close()

	s2, err := store.Open(ruta)
	if err != nil {
		t.Fatalf("segundo Open: %v", err)
	}
	defer s2.Close()

	v, err := s2.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 1 {
		t.Errorf("versión después de reabrir = %d, quería 1", v)
	}
}
```

- [ ] **Step 3: Correr el test y verificar que falla**

Run: `go test ./internal/store/ -v`
Expected: FAIL — el paquete no existe

- [ ] **Step 4: Implementar**

`internal/store/store.go`:

```go
// Package store guarda las muestras en SQLite.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // driver puro Go: sin cgo, invariante 7 del spec
)

// migraciones se aplican en orden y nunca se editan una vez aplicadas.
// Para cambiar el esquema se agrega una nueva al final.
var migraciones = []string{
	`CREATE TABLE host_samples (
		ts               INTEGER PRIMARY KEY,
		cpu_pct_avg      REAL    NOT NULL,
		cpu_pct_max      REAL    NOT NULL,
		load1            REAL    NOT NULL,
		load5            REAL    NOT NULL,
		load15           REAL    NOT NULL,
		mem_used_bytes   INTEGER NOT NULL,
		mem_total_bytes  INTEGER NOT NULL,
		swap_used_bytes  INTEGER NOT NULL,
		swap_total_bytes INTEGER NOT NULL,
		disk_used_bytes  INTEGER NOT NULL,
		disk_total_bytes INTEGER NOT NULL,
		net_rx_bytes     INTEGER NOT NULL,
		net_tx_bytes     INTEGER NOT NULL,
		uptime_seconds   INTEGER NOT NULL
	) STRICT;`,
}

type Store struct{ db *sql.DB }

func Open(ruta string) (*Store, error) {
	dsn := ruta + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Un solo proceso y un solo escritor: con una conexión no hay SQLITE_BUSY
	// que resolver, y el costo es nulo para el volumen que maneja esto.
	db.SetMaxOpenConns(1)

	if err := migrar(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func migrar(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	) STRICT;`); err != nil {
		return fmt.Errorf("crear schema_migrations: %w", err)
	}

	var aplicadas int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&aplicadas); err != nil {
		return err
	}
	// Una base más nueva que el binario significa que alguien deployó para
	// atrás. Mejor negarse que escribir con un esquema que no se conoce.
	if aplicadas > len(migraciones) {
		return fmt.Errorf("la base está en la migración %d y el binario conoce %d", aplicadas, len(migraciones))
	}

	for i := aplicadas; i < len(migraciones); i++ {
		version := i + 1
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migraciones[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migración %d: %w", version, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, unixepoch())`,
			version,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("registrar migración %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./internal/store/ -v`
Expected: PASS, 2 tests

- [ ] **Step 6: Commit**

```bash
git add internal/store go.mod go.sum
git commit -m "feat: store SQLite con migraciones numeradas"
```

---

## Task 11: Store — insertar y consultar muestras

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Escribir el test que falla**

Agregar a `internal/store/store_test.go` (y sumar `"time"` y el import de `model`):

```go
func muestra(ts time.Time, cpu float64) model.HostSample {
	return model.HostSample{
		TS: ts, CPUPctAvg: cpu, CPUPctMax: cpu,
		Load1: 0.41, Load5: 0.60, Load15: 0.53,
		MemUsedBytes: 3_000_000_000, MemTotalBytes: 12_000_000_000,
		SwapUsedBytes: 0, SwapTotalBytes: 2_147_483_648,
		DiskUsedBytes: 16_000_000_000, DiskTotalBytes: 96_000_000_000,
		NetRxBytes: 1000, NetTxBytes: 2000,
		Uptime: 3 * time.Hour,
	}
}

func TestInsertYUltimasVuelvenLoMismo(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 8, 23, 12, 0, 0, time.UTC)

	if err := s.InsertHostSample(muestra(ts, 12.5)); err != nil {
		t.Fatalf("InsertHostSample: %v", err)
	}

	got, err := s.UltimasHostSamples(10)
	if err != nil {
		t.Fatalf("UltimasHostSamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("volvieron %d filas, quería 1", len(got))
	}
	if !got[0].TS.Equal(ts) {
		t.Errorf("TS = %v, quería %v", got[0].TS, ts)
	}
	if got[0].CPUPctAvg != 12.5 {
		t.Errorf("CPUPctAvg = %v, quería 12.5", got[0].CPUPctAvg)
	}
	if got[0].Uptime != 3*time.Hour {
		t.Errorf("Uptime = %v, quería 3h", got[0].Uptime)
	}
}

// El ts es la clave primaria y se trunca al minuto: dos muestras del mismo
// minuto tienen que pisar, no explotar ni duplicar.
func TestInsertDelMismoMinutoPisa(t *testing.T) {
	s := abrir(t)
	ts := time.Date(2026, 8, 8, 23, 12, 0, 0, time.UTC)

	if err := s.InsertHostSample(muestra(ts, 10)); err != nil {
		t.Fatalf("primer insert: %v", err)
	}
	if err := s.InsertHostSample(muestra(ts, 90)); err != nil {
		t.Fatalf("segundo insert: %v", err)
	}

	got, err := s.UltimasHostSamples(10)
	if err != nil {
		t.Fatalf("UltimasHostSamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("volvieron %d filas, quería 1", len(got))
	}
	if got[0].CPUPctAvg != 90 {
		t.Errorf("CPUPctAvg = %v, quería 90 (la última gana)", got[0].CPUPctAvg)
	}
}

func TestUltimasVuelvenDeLaMasNuevaALaMasVieja(t *testing.T) {
	s := abrir(t)
	base := time.Date(2026, 8, 8, 23, 0, 0, 0, time.UTC)
	for i := range 5 {
		if err := s.InsertHostSample(muestra(base.Add(time.Duration(i)*time.Minute), float64(i))); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	got, err := s.UltimasHostSamples(3)
	if err != nil {
		t.Fatalf("UltimasHostSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("volvieron %d filas, quería 3", len(got))
	}
	if got[0].CPUPctAvg != 4 {
		t.Errorf("la primera fila tiene CPU %v, quería 4 (la más nueva)", got[0].CPUPctAvg)
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./internal/store/ -run Insert -v`
Expected: FAIL — `s.InsertHostSample undefined`

- [ ] **Step 3: Implementar**

Agregar a `internal/store/store.go` (y sumar `"time"` y el import de `model`):

```go
// InsertHostSample guarda una muestra. El ts se trunca al minuto, así que dos
// muestras del mismo minuto se pisan en vez de duplicarse.
func (s *Store) InsertHostSample(m model.HostSample) error {
	_, err := s.db.Exec(`
		INSERT INTO host_samples (
			ts, cpu_pct_avg, cpu_pct_max, load1, load5, load15,
			mem_used_bytes, mem_total_bytes, swap_used_bytes, swap_total_bytes,
			disk_used_bytes, disk_total_bytes, net_rx_bytes, net_tx_bytes,
			uptime_seconds
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(ts) DO UPDATE SET
			cpu_pct_avg=excluded.cpu_pct_avg, cpu_pct_max=excluded.cpu_pct_max,
			load1=excluded.load1, load5=excluded.load5, load15=excluded.load15,
			mem_used_bytes=excluded.mem_used_bytes, mem_total_bytes=excluded.mem_total_bytes,
			swap_used_bytes=excluded.swap_used_bytes, swap_total_bytes=excluded.swap_total_bytes,
			disk_used_bytes=excluded.disk_used_bytes, disk_total_bytes=excluded.disk_total_bytes,
			net_rx_bytes=excluded.net_rx_bytes, net_tx_bytes=excluded.net_tx_bytes,
			uptime_seconds=excluded.uptime_seconds`,
		m.TS.Truncate(time.Minute).Unix(),
		m.CPUPctAvg, m.CPUPctMax,
		m.Load1, m.Load5, m.Load15,
		int64(m.MemUsedBytes), int64(m.MemTotalBytes),
		int64(m.SwapUsedBytes), int64(m.SwapTotalBytes),
		int64(m.DiskUsedBytes), int64(m.DiskTotalBytes),
		int64(m.NetRxBytes), int64(m.NetTxBytes),
		int64(m.Uptime.Seconds()),
	)
	return err
}

// UltimasHostSamples devuelve las n más recientes, de la más nueva a la más vieja.
func (s *Store) UltimasHostSamples(n int) ([]model.HostSample, error) {
	filas, err := s.db.Query(`
		SELECT ts, cpu_pct_avg, cpu_pct_max, load1, load5, load15,
		       mem_used_bytes, mem_total_bytes, swap_used_bytes, swap_total_bytes,
		       disk_used_bytes, disk_total_bytes, net_rx_bytes, net_tx_bytes,
		       uptime_seconds
		FROM host_samples ORDER BY ts DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.HostSample
	for filas.Next() {
		var (
			m                                    model.HostSample
			ts, uptime                           int64
			memU, memT, swU, swT, dkU, dkT, rx, tx int64
		)
		if err := filas.Scan(
			&ts, &m.CPUPctAvg, &m.CPUPctMax, &m.Load1, &m.Load5, &m.Load15,
			&memU, &memT, &swU, &swT, &dkU, &dkT, &rx, &tx, &uptime,
		); err != nil {
			return nil, err
		}
		m.TS = time.Unix(ts, 0).UTC()
		m.MemUsedBytes, m.MemTotalBytes = uint64(memU), uint64(memT)
		m.SwapUsedBytes, m.SwapTotalBytes = uint64(swU), uint64(swT)
		m.DiskUsedBytes, m.DiskTotalBytes = uint64(dkU), uint64(dkT)
		m.NetRxBytes, m.NetTxBytes = uint64(rx), uint64(tx)
		m.Uptime = time.Duration(uptime) * time.Second
		out = append(out, m)
	}
	return out, filas.Err()
}
```

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./internal/store/ -v`
Expected: PASS, 5 tests

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat: insertar y consultar muestras del host"
```

---

## Task 12: Configuración

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`, `deploy/config.example.yaml`

- [ ] **Step 1: Agregar la dependencia**

```bash
go get gopkg.in/yaml.v3
```

- [ ] **Step 2: Escribir el test que falla**

`internal/config/config_test.go`:

```go
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
```

- [ ] **Step 3: Correr el test y verificar que falla**

Run: `go test ./internal/config/ -v`
Expected: FAIL — el paquete no existe

- [ ] **Step 4: Implementar**

`internal/config/config.go`:

```go
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

	if c.Base == "" {
		return Config{}, errors.New("falta 'base' en la config: es la ruta del archivo SQLite")
	}
	return c, nil
}
```

- [ ] **Step 5: Correr el test y verificar que pasa**

Run: `go test ./internal/config/ -v`
Expected: PASS, 4 tests

- [ ] **Step 6: Crear el ejemplo**

`deploy/config.example.yaml`:

```yaml
# Copiar a /etc/server-status/config.yaml en el servidor.
# Este archivo NO lleva secretos: los tokens van en /etc/server-status/env (modo 0600).

base: /var/lib/server-status/status.db

proc: /proc
disco: /

intervalo_muestreo: 15s
intervalo_persistencia: 1m

# La lista de 'servicios' con sus probes llega en el plan 2 (fases 2 a 4).
```

- [ ] **Step 7: Commit**

```bash
git add internal/config deploy go.mod go.sum
git commit -m "feat: configuración en YAML con defaults y validación"
```

---

## Task 13: El binario y el loop de muestreo

**Files:**
- Create: `cmd/server-status/main.go`, `cmd/server-status/agregador.go`, `cmd/server-status/agregador_test.go`

- [ ] **Step 1: Escribir el test que falla**

`cmd/server-status/agregador_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
)

func TestAgregadorPromediaYGuardaElPico(t *testing.T) {
	var a agregador
	base := time.Date(2026, 8, 8, 23, 12, 0, 0, time.UTC)

	a.add(model.HostSample{TS: base, CPUPctAvg: 10})
	a.add(model.HostSample{TS: base.Add(15 * time.Second), CPUPctAvg: 90})
	a.add(model.HostSample{TS: base.Add(30 * time.Second), CPUPctAvg: 20})

	got, ok := a.flush()
	if !ok {
		t.Fatal("flush devolvió ok=false con 3 muestras adentro")
	}
	if got.CPUPctAvg != 40 {
		t.Errorf("CPUPctAvg = %v, quería 40", got.CPUPctAvg)
	}
	// El máximo es el punto: un pico de 15 segundos desaparece en el promedio.
	if got.CPUPctMax != 90 {
		t.Errorf("CPUPctMax = %v, quería 90", got.CPUPctMax)
	}
	// Los valores instantáneos (disco, uptime, contadores) se toman de la última.
	if !got.TS.Equal(base.Add(30 * time.Second)) {
		t.Errorf("TS = %v, quería el de la última muestra", got.TS)
	}
}

func TestAgregadorVacioNoEmiteNada(t *testing.T) {
	var a agregador
	if _, ok := a.flush(); ok {
		t.Fatal("flush devolvió ok=true sin muestras")
	}
}

func TestFlushDejaElAgregadorLimpio(t *testing.T) {
	var a agregador
	a.add(model.HostSample{CPUPctAvg: 50})
	a.flush()
	if _, ok := a.flush(); ok {
		t.Fatal("el segundo flush devolvió ok=true: no se limpió")
	}
}
```

- [ ] **Step 2: Correr el test y verificar que falla**

Run: `go test ./cmd/server-status/ -v`
Expected: FAIL — `undefined: agregador`

- [ ] **Step 3: Implementar el agregador**

`cmd/server-status/agregador.go`:

```go
package main

import "github.com/juanddavila/server-status/internal/model"

// agregador junta las muestras de 15 segundos y emite una por minuto.
// Guarda promedio y máximo de CPU porque un pico corto se pierde en el promedio.
type agregador struct {
	ultima model.HostSample
	suma   float64
	max    float64
	n      int
}

func (a *agregador) add(m model.HostSample) {
	a.ultima = m
	a.suma += m.CPUPctAvg
	if m.CPUPctAvg > a.max {
		a.max = m.CPUPctAvg
	}
	a.n++
}

// flush devuelve la muestra del minuto y se limpia. ok es false si no hubo nada.
func (a *agregador) flush() (model.HostSample, bool) {
	if a.n == 0 {
		return model.HostSample{}, false
	}
	m := a.ultima
	m.CPUPctAvg = a.suma / float64(a.n)
	m.CPUPctMax = a.max
	*a = agregador{}
	return m, true
}
```

⚠️ Corregir el import a `github.com/juanandresdavila/server-status/internal/model`.

- [ ] **Step 4: Correr el test y verificar que pasa**

Run: `go test ./cmd/server-status/ -v`
Expected: PASS, 3 tests

- [ ] **Step 5: Implementar el binario**

`cmd/server-status/main.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/collector/host"
	"github.com/juanandresdavila/server-status/internal/config"
	"github.com/juanandresdavila/server-status/internal/store"
)

func main() {
	rutaConfig := flag.String("config", "/etc/server-status/config.yaml", "ruta del archivo de configuración")
	flag.Parse()

	if err := run(*rutaConfig, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(rutaConfig, comando string) error {
	cfg, err := config.Load(rutaConfig)
	if err != nil {
		return err
	}
	col := host.NewCollector(cfg.Proc, cfg.Disco, clock.Real{})

	switch comando {
	case "sample":
		return sampleUnaVez(col)
	case "run", "":
		return correr(cfg, col)
	default:
		return fmt.Errorf("comando desconocido %q: usá 'sample' o 'run'", comando)
	}
}

// sampleUnaVez imprime una muestra por stdout. Sirve para comparar a ojo contra
// free -h, df -h y uptime en el servidor, sin tocar la base.
func sampleUnaVez(col *host.Collector) error {
	// La primera lectura no tiene con qué comparar la CPU, así que se descarta.
	if _, err := col.Sample(); err != nil {
		return err
	}
	time.Sleep(time.Second)

	m, err := col.Sample()
	if err != nil {
		return err
	}
	const gb = 1024 * 1024 * 1024
	fmt.Printf("cpu    %.1f%%\n", m.CPUPctAvg)
	fmt.Printf("load   %.2f %.2f %.2f\n", m.Load1, m.Load5, m.Load15)
	fmt.Printf("mem    %.1f / %.1f GB\n", float64(m.MemUsedBytes)/gb, float64(m.MemTotalBytes)/gb)
	fmt.Printf("swap   %.2f / %.1f GB\n", float64(m.SwapUsedBytes)/gb, float64(m.SwapTotalBytes)/gb)
	fmt.Printf("disco  %.1f / %.1f GB\n", float64(m.DiskUsedBytes)/gb, float64(m.DiskTotalBytes)/gb)
	fmt.Printf("red    rx %d  tx %d\n", m.NetRxBytes, m.NetTxBytes)
	fmt.Printf("uptime %s\n", m.Uptime.Round(time.Second))
	return nil
}

func correr(cfg config.Config, col *host.Collector) error {
	s, err := store.Open(cfg.Base)
	if err != nil {
		return err
	}
	defer s.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	muestreo := time.NewTicker(cfg.IntervaloMuestreo)
	defer muestreo.Stop()
	persistencia := time.NewTicker(cfg.IntervaloPersistencia)
	defer persistencia.Stop()

	var a agregador
	slog.Info("server-status arrancó", "base", cfg.Base, "muestreo", cfg.IntervaloMuestreo)

	for {
		select {
		case <-ctx.Done():
			slog.Info("saliendo")
			return nil

		case <-muestreo.C:
			m, err := col.Sample()
			if err != nil {
				// Un error de lectura no puede tumbar el proceso: es un
				// monitor, y morirse es justo lo que no tiene que hacer.
				slog.Error("no se pudo muestrear", "err", err)
				continue
			}
			a.add(m)

		case <-persistencia.C:
			m, ok := a.flush()
			if !ok {
				continue
			}
			if err := s.InsertHostSample(m); err != nil {
				slog.Error("no se pudo guardar la muestra", "err", err)
			}
		}
	}
}
```

- [ ] **Step 6: Verificar que todo compila y pasa**

```bash
go vet ./...
go test ./... -race
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/server-status
```

Expected: vet sin salida, todos los tests en PASS, build sin salida.

- [ ] **Step 7: Commit**

```bash
git add cmd
git commit -m "feat: binario con muestreo cada 15s y persistencia por minuto"
```

---

## Task 14: Unit de systemd

**Files:**
- Create: `deploy/server-status.service`

- [ ] **Step 1: Escribir la unit**

`deploy/server-status.service`:

```ini
[Unit]
Description=server-status — monitoreo del VPS
Documentation=https://github.com/juanandresdavila/server-status
After=network-online.target docker.service
Wants=network-online.target

# OJO: After, pero NUNCA Requires=docker.service.
# Si Docker se cae, este servicio tiene que seguir vivo para poder avisarlo.
# Con Requires, systemd lo mataría junto con Docker y el aviso nunca saldría.
# Es la razón entera por la que esto no corre en un container.

[Service]
Type=simple
User=server-status
Group=server-status
SupplementaryGroups=docker
ExecStart=/usr/local/bin/server-status -config /etc/server-status/config.yaml run
EnvironmentFile=-/etc/server-status/env
Restart=always
RestartSec=5

NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
ReadWritePaths=/var/lib/server-status /opt/status/public
MemoryMax=256M

[Install]
WantedBy=multi-user.target
```

El `-` de `EnvironmentFile=-` hace el archivo opcional: en la fase 1 todavía no hay secretos y sin el guion la unit no arrancaría.

- [ ] **Step 2: Commit**

```bash
git add deploy/server-status.service
git commit -m "feat: unit de systemd con hardening y sin Requires de docker"
```

---

## Task 15: Instalar y verificar contra el VPS real

**Files:** ninguno — es verificación en el servidor.

- [ ] **Step 1: Crear usuario y directorios en el VPS**

```bash
ssh vps 'sudo useradd --system --no-create-home --shell /usr/sbin/nologin server-status || true
sudo usermod -aG docker server-status
sudo mkdir -p /etc/server-status /var/lib/server-status/backup /opt/status/public
sudo chown -R server-status:server-status /var/lib/server-status /opt/status/public'
```

- [ ] **Step 2: Copiar config y unit**

```bash
scp deploy/config.example.yaml vps:/tmp/config.yaml
scp deploy/server-status.service vps:/tmp/server-status.service
ssh vps 'sudo install -m 0644 -o root -g root /tmp/config.yaml /etc/server-status/config.yaml
sudo install -m 0644 /tmp/server-status.service /etc/systemd/system/server-status.service
sudo systemctl daemon-reload && rm -f /tmp/config.yaml /tmp/server-status.service'
```

- [ ] **Step 3: Compilar, copiar y comparar contra el sistema**

```bash
make linux
scp dist/server-status-linux-amd64 vps:/tmp/server-status
ssh vps 'sudo install -m 0755 /tmp/server-status /usr/local/bin/server-status && rm /tmp/server-status'
ssh vps '/usr/local/bin/server-status -config /etc/server-status/config.yaml sample'
```

Después, para comparar:

```bash
ssh vps 'free -h; df -h /; uptime'
```

Expected: la RAM total y el disco total del binario tienen que coincidir con `free -h` y `df -h`. El disco usado puede diferir uno o dos puntos porcentuales por los bloques reservados para root — está explicado en el comentario de `DiskUsage`. El uptime tiene que coincidir con el de `uptime`.

**Si algo no coincide, el problema es el parser y no se sigue.**

- [ ] **Step 4: Arrancar el servicio y ver que persiste**

```bash
ssh vps 'sudo systemctl enable --now server-status && sleep 90 && systemctl status server-status --no-pager'
```

Expected: `active (running)`, sin errores en el log.

```bash
ssh vps 'sudo -u server-status sqlite3 /var/lib/server-status/status.db "SELECT datetime(ts,\"unixepoch\"), round(cpu_pct_avg,1), round(cpu_pct_max,1), load1 FROM host_samples ORDER BY ts DESC LIMIT 3;"'
```

Expected: al menos una fila, con timestamp del minuto actual. Si `sqlite3` no está instalado: `sudo apt install -y sqlite3`.

- [ ] **Step 5: Verificar que sobrevive a un reinicio del proceso**

```bash
ssh vps 'sudo systemctl restart server-status && sleep 70 && sudo -u server-status sqlite3 /var/lib/server-status/status.db "SELECT count(*) FROM host_samples;"'
```

Expected: el conteo creció respecto del paso anterior, y la unit sigue `active`.

- [ ] **Step 6: Commit del estado y cierre de la fase**

```bash
git add -A
git commit --allow-empty -m "chore: fases 0 y 1 verificadas contra el VPS"
```

---

## Autorevisión del plan

**Cobertura del spec (fases 0 y 1):**

| Requisito del spec | Tarea |
|---|---|
| Módulo Go, layout `cmd`/`internal` | 1 |
| CI: vet, test, build linux, gitleaks | 1 |
| Hook de pre-push con gitleaks (§12) | 1 |
| Invariante 5 — reloj inyectado | 2, usado en 9 |
| Invariante 7 — sin cgo | 1 (`CGO_ENABLED=0` en Makefile y CI), 8, 10 |
| Invariante 9 — solo `cmd` arma dependencias | 13 |
| Parsers de `/proc` (§16, golden inline) | 4, 5, 6, 7 |
| Disco por statfs | 8 |
| `host_samples` con avg y max (§5) | 3, 10, 11, 13 |
| Migraciones numeradas, rechazo de base más nueva | 10 |
| Muestreo 15 s → persistencia 1 min (§3) | 12, 13 |
| Config sin secretos, env aparte (§12, §13) | 12, 14 |
| Unit con hardening del §12 | 14 |
| **`After` pero no `Requires` de docker** — la razón de la topología B | 14 |
| Verificación contra `free`/`df`/`uptime` (§15, fase 1) | 15 |

**Fuera de este plan, a propósito:** retención y `VACUUM INTO` (necesitan datos de varios días; van al plan 2 junto con las tablas de containers y probes), y todo lo de las fases 2 a 9.

**Tres correcciones aplicadas durante la revisión** — quedaron como avisos `⚠️` dentro de las tareas en vez de borrarse, porque son errores que un ejecutor distraído volvería a cometer:

1. Task 8 y 13: el path del módulo en algunos imports decía `juanddavila` en vez de `juanandresdavila`.
2. Task 9: la firma genérica de `leer` recibía `*os.File` cuando los parsers toman `io.Reader`.

**Consistencia de tipos:** `model.HostSample` se define en la Task 3 y lo usan 9, 11 y 13 con los mismos nombres de campo. `CPUTimes`, `Mem`, `Load`, `Net` y `Disk` viven en el paquete `host` y solo los usa el `Collector` de la Task 9. `Store` expone `Open`, `Close`, `SchemaVersion`, `InsertHostSample` y `UltimasHostSamples`, y no aparece ningún otro nombre en el resto del plan.
