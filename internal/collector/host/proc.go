// Package host lee las métricas de la máquina desde /proc y statfs.
// Los parsers son funciones puras sobre un io.Reader: se testean con fixtures
// y no tocan el sistema de archivos.
package host

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
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
