package notify

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
)

// LargoMaximo es el límite de Telegram. comm-tool además lo valida antes de
// mandar, así que pasarse convierte un aviso en un 400.
const LargoMaximo = 4096

// NombreDeSujeto traduce la etiqueta interna a algo legible.
func NombreDeSujeto(sujeto string) string {
	prefijo, resto, ok := strings.Cut(sujeto, ":")
	if !ok {
		return sujeto
	}
	if prefijo == "host" {
		switch resto {
		case "disk":
			return "disco"
		case "mem":
			return "memoria"
		case "swap":
			return "swap"
		case "load":
			return "carga"
		}
	}
	return resto
}

func Texto(a model.Aviso) string {
	nombre := NombreDeSujeto(a.Incidente.Sujeto)

	var b strings.Builder
	if a.Cierre {
		fmt.Fprintf(&b, "🟢 %s se recuperó", nombre)
		if a.Incidente.CerradoEn != nil {
			dur := a.Incidente.CerradoEn.Sub(a.Incidente.AbiertoEn).Round(time.Minute)
			fmt.Fprintf(&b, "\nestuvo mal %s", dur)
		}
	} else {
		// El ícono es lo único que se ve en la notificación del celular sin
		// abrirla: crítico y advertencia no pueden mirarse igual.
		icono := "🟡"
		if a.Incidente.Severidad == "critical" {
			icono = "🔴"
		}
		fmt.Fprintf(&b, "%s %s\n%s", icono,
			tituloDeApertura(a.Incidente, nombre), a.Incidente.Detalle)
	}
	return acortar(b.String())
}

func tituloDeApertura(i model.Incidente, nombre string) string {
	switch i.Tipo {
	case "down":
		return nombre + " no responde"
	case "unhealthy":
		return nombre + " está unhealthy"
	case "flapping":
		return nombre + " está inestable"
	default:
		return nombre + " fuera de rango"
	}
}

// acortar deja el mensaje dentro del límite. Corta por el final y avisa que
// cortó: un mensaje truncado en silencio es peor que uno que lo dice.
func acortar(s string) string {
	if len(s) <= LargoMaximo {
		return s
	}
	const marca = "\n[…]"
	return s[:LargoMaximo-len(marca)] + marca
}

// Resumen es la foto diaria.
type Resumen struct {
	Uptime     time.Duration
	DiscoPct   float64
	MemPct     float64
	Incidentes int
	Servicios  map[string]bool
}

func TextoResumen(r Resumen) string {
	var b strings.Builder
	b.WriteString("📊 Resumen diario\n")
	fmt.Fprintf(&b, "uptime %s · disco %.1f%% · memoria %.1f%%\n",
		r.Uptime.Round(time.Hour), r.DiscoPct, r.MemPct)

	if r.Incidentes == 0 {
		b.WriteString("sin incidentes en 24 h\n")
	} else {
		fmt.Fprintf(&b, "%d incidentes en 24 h\n", r.Incidentes)
	}

	// Ordenado para que el mensaje sea estable entre días y se pueda comparar
	// de un vistazo.
	nombres := make([]string, 0, len(r.Servicios))
	for n := range r.Servicios {
		nombres = append(nombres, n)
	}
	sort.Strings(nombres)
	for _, n := range nombres {
		icono := "🔴"
		if r.Servicios[n] {
			icono = "🟢"
		}
		fmt.Fprintf(&b, "%s %s\n", icono, n)
	}
	return acortar(b.String())
}

// TextoEvento arma el mensaje de un hecho puntual.
//
// Los eventos no abren ni cierran, así que no llevan el par 🔴/🟢 de los
// incidentes: un reinicio ya terminó cuando uno se entera. Lo que importa es
// cuándo pasó y qué se llevó puesto.
func TextoEvento(e model.Evento) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n%s", iconoDeEvento(e), tituloDeEvento(e), e.Detalle)
	return acortar(b.String())
}

func iconoDeEvento(e model.Evento) string {
	if e.Severidad == "critical" {
		return "🔁"
	}
	return "♻️"
}

func tituloDeEvento(e model.Evento) string {
	switch e.Tipo {
	case "reboot":
		return "El servidor se reinició"
	case "container_restart":
		return "Containers reiniciados"
	case "monitor_start":
		return "El monitor arrancó"
	}
	return "Evento en " + NombreDeSujeto(e.Sujeto)
}
