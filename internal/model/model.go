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
	// ArchivadoEn es "ya lo vi": el panel lo esconde, la historia lo conserva.
	// Solo un incidente cerrado puede estar archivado.
	ArchivadoEn *time.Time
	Detalle     string
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
//
// StartedAt es CUÁNDO arrancó este container, y es la señal buena para saber
// que se reinició: Restarts no sirve —un arranque con el host no lo incrementa
// y una recreación lo resetea— pero StartedAt se mueve siempre.
type ContainerSample struct {
	TS        time.Time
	Name      string
	State     string
	Health    string
	Restarts  int
	StartedAt time.Time
	CPUPct    float64
	MemBytes  uint64
}

// LineaLog es una línea de log ya fechada y atribuida a su container.
//
// Nivel es un string y no un tipo propio porque este paquete no importa nada
// del proyecto: quien clasifica es internal/logs, que sí tiene el tipo Nivel.
type LineaLog struct {
	TS        time.Time
	Container string
	Stream    string // stdout | stderr
	Linea     string
	Nivel     string // TRACE | INFO | WARN | ERROR
}

// ReglaNivel es una corrección guardada del nivel de una línea: "todo lo que
// contiene este patrón es TRACE".
//
// Nivel y Container son strings por la misma razón que en LineaLog: este
// paquete no importa nada del proyecto. Quien las aplica es internal/logs.
//
// Motivo NO es decorativo y no puede quedar vacío: dentro de tres meses, una
// regla sin motivo es una regla que nadie se va a animar a borrar.
type ReglaNivel struct {
	ID        int64
	Patron    string
	Container string // "" = todos
	Nivel     string // TRACE | INFO | WARN | ERROR
	Motivo    string
	Creada    time.Time
}

// Evento es un hecho puntual: el host se reinició, unos containers volvieron.
// A diferencia de un Incidente no tiene ventana —no se "cierra"— y por eso
// vive en su propia tabla y no choca con incidentes_abierto_unico.
type Evento struct {
	ID         int64
	Tipo       string // reboot | container_restart | monitor_start
	Sujeto     string
	Severidad  string // critical | warning | info
	OcurridoEn time.Time
	Detalle    string
}
