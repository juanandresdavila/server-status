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

// Aviso es un mensaje que hay que mandar. El DeliveryID es determinístico:
// '<incidenteID>:opened' o '<incidenteID>:closed'. Con un uuid nuevo por
// intento, un reintento mandaría el aviso dos veces — lección copiada de
// comm-tool.
type Aviso struct {
	DeliveryID string
	Incidente  Incidente
	Cierre     bool // false = se abrió, true = se cerró
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
