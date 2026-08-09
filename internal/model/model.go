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

// ProbeResult es el resultado de pinchar un servicio una vez.
type ProbeResult struct {
	TS         time.Time
	Servicio   string
	OK         bool
	StatusCode int
	Latencia   time.Duration
	Error      string
}

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
