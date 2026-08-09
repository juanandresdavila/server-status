package main

import (
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/notify"
)

// recordatorioDiario dispara una vez por día, a partir de cierta hora.
//
// "A partir de" y no "a las": si el proceso estuvo caído a las 8, el resumen
// tiene que salir igual cuando vuelva. Su valor no es la puntualidad, es
// confirmar que el circuito de avisos sigue vivo — el silencio de un monitor
// roto es idéntico al de un servidor sano.
type recordatorioDiario struct {
	ultimo time.Time
}

func (r *recordatorioDiario) toca(ahora time.Time, hora int) bool {
	if ahora.Hour() < hora {
		return false
	}
	if mismoDia(r.ultimo, ahora) {
		return false
	}
	r.ultimo = ahora
	return true
}

func mismoDia(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func armarResumen(h model.HostSample, probes []model.ProbeResult, incidentes int) notify.Resumen {
	servicios := make(map[string]bool, len(probes))
	for _, p := range probes {
		servicios[p.Servicio] = p.OK
	}
	return notify.Resumen{
		Uptime:     h.Uptime,
		DiscoPct:   porcentaje(h.DiskUsedBytes, h.DiskTotalBytes),
		MemPct:     porcentaje(h.MemUsedBytes, h.MemTotalBytes),
		Incidentes: incidentes,
		Servicios:  servicios,
	}
}

func porcentaje(usado, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(usado) * 100 / float64(total)
}
