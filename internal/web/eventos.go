package web

import (
	"sort"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
)

// Novedad es una fila de la línea de tiempo, venga de donde venga.
//
// Existe porque las tres fuentes —incidentes, eventos y errores de log— viven
// en tablas distintas y con formas distintas, pero la pregunta que se les hace
// es la misma: "qué pasó anoche". Tenerlas en tres pantallas obliga a cruzarlas
// a mano justo cuando uno tiene menos paciencia para hacerlo.
type Novedad struct {
	Cuando    time.Time
	Severidad string // critical | warning | info
	Origen    string // incidente | evento | log
	Titulo    string
	Detalle   string
}

// severidades conocidas, en orden. El filtro es un toggle por ítem, igual que
// los niveles de log: "info y critical sin warning" tiene que poder pedirse.
var severidades = []string{"info", "warning", "critical"}

// severidadesValidas normaliza los toggles de la query string: filtra basura y
// dedup. Sin ninguna elegida, todas: el default de la vista es no esconder nada.
func severidadesValidas(ss []string) []string {
	conocida := map[string]bool{"info": true, "warning": true, "critical": true}
	elegidas := map[string]bool{}
	for _, s := range ss {
		if conocida[s] {
			elegidas[s] = true
		}
	}
	if len(elegidas) == 0 {
		return severidades
	}
	out := make([]string, 0, len(elegidas))
	for _, s := range severidades {
		if elegidas[s] {
			out = append(out, s)
		}
	}
	return out
}

// armarNovedades funde las tres fuentes en una sola línea de tiempo.
//
// Es una función pura sobre los tres slices: se testea sin base, que es la
// misma razón por la que rules declara su propia interfaz Store.
func armarNovedades(incidentes []model.Incidente, eventos []model.Evento,
	errores []model.LineaLog, desde, hasta time.Time, mostrar []string) []Novedad {

	var out []Novedad
	dentro := func(t time.Time) bool {
		return !t.Before(desde) && !t.After(hasta)
	}

	for _, i := range incidentes {
		// Apertura y cierre son DOS hechos en la línea de tiempo: colapsarlos
		// en uno perdería justo el dato de cuánto duró la cosa.
		if dentro(i.AbiertoEn) {
			out = append(out, Novedad{
				Cuando: i.AbiertoEn, Severidad: i.Severidad, Origen: "incidente",
				Titulo: "se abrió · " + NombreLegible(i.Sujeto), Detalle: i.Detalle,
			})
		}
		if i.CerradoEn != nil && dentro(*i.CerradoEn) {
			out = append(out, Novedad{
				Cuando: *i.CerradoEn, Severidad: "info", Origen: "incidente",
				Titulo:  "se cerró · " + NombreLegible(i.Sujeto),
				Detalle: "estuvo mal " + i.CerradoEn.Sub(i.AbiertoEn).Round(time.Second).String(),
			})
		}
	}

	for _, e := range eventos {
		if dentro(e.OcurridoEn) {
			out = append(out, Novedad{
				Cuando: e.OcurridoEn, Severidad: e.Severidad, Origen: "evento",
				Titulo: tituloDeEvento(e), Detalle: e.Detalle,
			})
		}
	}

	for _, l := range errores {
		if dentro(l.TS) {
			out = append(out, Novedad{
				Cuando: l.TS, Severidad: "warning", Origen: "log",
				Titulo: l.Container, Detalle: l.Linea,
			})
		}
	}

	pasa := map[string]bool{}
	for _, s := range mostrar {
		pasa[s] = true
	}
	filtradas := out[:0]
	for _, n := range out {
		if pasa[n.Severidad] {
			filtradas = append(filtradas, n)
		}
	}

	// Más nuevo primero, que es como se lee cuando algo acaba de pasar.
	sort.SliceStable(filtradas, func(i, j int) bool {
		return filtradas[i].Cuando.After(filtradas[j].Cuando)
	})
	return filtradas
}

func tituloDeEvento(e model.Evento) string {
	switch e.Tipo {
	case "reboot":
		return "el servidor se reinició"
	case "container_restart":
		return "containers reiniciados"
	case "monitor_start":
		return "el monitor arrancó"
	}
	return e.Tipo
}

// NombreLegible traduce 'host:disk' a 'disco'. Duplica lo que hace
// notify.NombreDeSujeto a propósito: el paquete web no importa notify, y atar
// el panel a los textos de los mensajes de Telegram sería atar dos cosas que
// cambian por razones distintas.
func NombreLegible(sujeto string) string {
	prefijo, resto, ok := cortar(sujeto)
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

func cortar(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
