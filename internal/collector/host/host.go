package host

import (
	"fmt"
	"io"
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
func leer[T any](dir, nombre string, parse func(r io.Reader) (T, error)) (T, error) {
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
