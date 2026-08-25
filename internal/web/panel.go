package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/juanandresdavila/server-status/internal/logs"
	"github.com/juanandresdavila/server-status/internal/model"
)

// Datos es lo que el panel necesita de la persistencia, y nada más.
// Declarada acá y no importada de store, por la misma razón que en rules:
// deja el panel testeable sin base.
type Datos interface {
	BuscarLogs(texto, container string, niveles []string, desde, hasta time.Time, limite int) ([]model.LineaLog, error)
	UltimasHostSamples(n int) ([]model.HostSample, error)
	UltimoEstadoContainers() ([]model.ContainerSample, error)
	UltimoEstadoProbes() ([]model.ProbeResult, error)
	UltimosIncidentes(n int) ([]model.Incidente, error)
	SerieHost(desde, hasta time.Time) ([]model.HostSample, error)
	EventosEntre(desde, hasta time.Time, limite int) ([]model.Evento, error)
}

var plantillaPanel = template.Must(template.New("panel").Funcs(template.FuncMap{
	"pct": pct,
	"gib": gib,
	"mib": func(b uint64) float64 { return float64(b) / (1024 * 1024) },
	// Sin zona fija sería t.Local(), y el VPS corre en Etc/UTC: el panel venía
	// mostrando UTC mientras uno lo leía como hora argentina. La zona sale de
	// la config, que es la misma que usa el resumen diario.
	"hora": func(t time.Time, loc *time.Location) string { return enZona(t, loc).Format("02/01/2006 15:04") },
	"en":   enZona,
}).ParseFS(plantillas, "plantillas/nav.html", "plantillas/panel.html",
	"plantillas/logs.html", "plantillas/tail.html", "plantillas/eventos.html"))

// nav es lo que el header necesita saber de la vista que lo está pintando.
// Container puede venir vacío: sin container elegido no hay tail al que ir.
type nav struct {
	Activo    string // panel | eventos | logs | tail
	Horas     int
	Container string
}

// rangos son las opciones de tiempo, iguales en las tres vistas a propósito:
// el header propaga ?horas= entre ellas, y un valor que el <select> de logs no
// tuviera se vería como "última hora" mientras filtra por otra cosa.
var rangos = []struct {
	Valor int
	Texto string
}{{1, "última hora"}, {6, "6 horas"}, {24, "24 horas"}, {168, "7 días"}, {720, "30 días"}}

type vistaPanel struct {
	Nav        nav
	Zona       *time.Location
	Host       model.HostSample
	Containers []model.ContainerSample
	Probes     []model.ProbeResult
	Incidentes []model.Incidente
	Horas      int
	Rangos     []struct {
		Valor int
		Texto string
	}
}

// enZona nunca recibe nil sin defenderse: t.In(nil) PANIQUEA, y un panic adentro
// de una plantilla deja media página escrita con un 200 arriba — un fallo que no
// se ve. Mostrar UTC es peor que mostrar la hora bien, pero muchísimo mejor que
// servir una página cortada por la mitad diciendo que salió todo bien.
func enZona(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		return t.UTC()
	}
	return t.In(loc)
}

// NuevoPanel arma el panel. La zona NO puede ser time.Local: el VPS corre en
// Etc/UTC, así que el panel mostraba UTC mientras uno lo leía como hora
// argentina, y los campos desde/hasta interpretaban lo tecleado como UTC —
// tres horas de corrimiento sobre lo que uno quiso pedir, sin nada que lo
// indicara. Sale de la config, la misma zona que usa el resumen diario.
func NuevoPanel(d Datos, zona *time.Location) http.Handler {
	if zona == nil {
		zona = time.UTC
	}
	mux := http.NewServeMux()

	// ECharts sale del binario, no de un CDN: el panel vive en el tailnet y no
	// puede depender de que el navegador llegue a internet para dibujar.
	//
	// fs.Sub baja un nivel para que la URL sea /assets/echarts.min.js y no
	// /assets/assets/echarts.min.js, que es lo que da servir el embed crudo.
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("los assets embebidos no tienen el subdirectorio 'assets': " + err.Error())
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(sub)))

	mux.HandleFunc("GET /logs/tail", func(w http.ResponseWriter, r *http.Request) {
		c := r.URL.Query().Get("container")
		if c == "" {
			http.Error(w, "falta el parámetro container", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		plantillaPanel.ExecuteTemplate(w, "tail.html", struct {
			Nav       nav
			Container string
		}{nav{Activo: "tail", Horas: horasDe(r), Container: c}, c})
	})

	// El export acepta los mismos filtros que la vista y devuelve texto plano
	// para descargar. El tope es más alto que el de la vista: acá no hay
	// navegador renderizando 10 000 divs, es un archivo que se abre en otro lado.
	mux.HandleFunc("GET /logs/export", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v := ventanaDe(r, time.Now(), zona)
		niveles := nivelesDe(r)

		lineas, err := d.BuscarLogs(q.Get("q"), q.Get("container"), niveles,
			v.Desde, v.Hasta, topeExport)
		if err != nil {
			http.Error(w, "no se pudieron buscar los logs", http.StatusInternalServerError)
			return
		}

		nombre := q.Get("container")
		if nombre == "" {
			nombre = "todos"
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", nombreExport(nombre, v)))

		// La cabecera va SIEMPRE, no solo al truncar: un archivo que no dice
		// qué ventana cubre se lee como si cubriera la que uno pidió. Este
		// export salió una vez con "24h" en el nombre cubriendo 4 h 54 m, y el
		// reinicio que se buscaba estaba en las horas que faltaban.
		fmt.Fprintf(w, "# pedido: %s → %s (niveles %s)\n",
			v.en(v.Desde).Format(time.DateTime), v.en(v.Hasta).Format(time.DateTime),
			strings.Join(niveles, ","))
		if len(lineas) == topeExport {
			fmt.Fprintf(w, "# TRUNCADO en %d líneas: el archivo cubre desde %s, no desde lo pedido.\n",
				topeExport, v.en(lineas[len(lineas)-1].TS).Format(time.DateTime))
			fmt.Fprintf(w, "# Achicá la ventana, filtrá por container o subí el nivel mínimo.\n")
		}
		fmt.Fprintf(w, "# %d líneas\n\n", len(lineas))

		// Vienen de la más nueva a la más vieja; se escriben al revés para que
		// el archivo se lea como una terminal.
		for i := len(lineas) - 1; i >= 0; i-- {
			l := lineas[i]
			fmt.Fprintf(w, "%s %-5s %s %s %s\n",
				l.TS.UTC().Format(time.RFC3339), l.Nivel, l.Container, l.Stream, l.Linea)
		}
	})

	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v := ventanaDe(r, time.Now(), zona)
		niveles := nivelesDe(r)

		lineas, err := d.BuscarLogs(q.Get("q"), q.Get("container"), niveles,
			v.Desde, v.Hasta, topeVista)
		if err != nil {
			http.Error(w, "no se pudieron buscar los logs", http.StatusInternalServerError)
			return
		}

		// Truncar en silencio es lo que hizo que una consulta de 24 h se leyera
		// como 24 h cubriendo 5. Si se llegó al tope hay que decirlo, y decir
		// hasta dónde llega de verdad lo que se está mostrando.
		truncado := ""
		if len(lineas) == topeVista {
			truncado = fmt.Sprintf(
				"Se alcanzó el tope de %d líneas: esto cubre desde %s, no desde %s. "+
					"Achicá la ventana, elegí un container o subí el nivel mínimo.",
				topeVista,
				v.en(lineas[len(lineas)-1].TS).Format(time.DateTime),
				v.en(v.Desde).Format(time.DateTime))
		}

		// La lista de containers sale del último estado, no de los logs:
		// así aparece también uno que todavía no logueó nada.
		var nombres []string
		if cs, err := d.UltimoEstadoContainers(); err == nil {
			for _, c := range cs {
				nombres = append(nombres, c.Name)
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		errPlantilla := plantillaPanel.ExecuteTemplate(w, "logs.html", struct {
			Nav          nav
			Zona         *time.Location
			Q, Container string
			Horas        int
			Ventana      Ventana
			Truncado     string
			Containers   []string
			Lineas       []model.LineaLog
			Niveles      []toggleNivel
			Rangos       []struct {
				Valor int
				Texto string
			}
		}{
			Nav:  nav{Activo: "logs", Horas: v.Horas, Container: q.Get("container")},
			Zona: zona,
			Q:    q.Get("q"), Container: q.Get("container"),
			Horas: v.Horas, Ventana: v, Truncado: truncado,
			Containers: nombres, Lineas: lineas, Niveles: togglesDe(niveles), Rangos: rangos,
		})
		if errPlantilla != nil {
			// El cuerpo ya se empezó a escribir, así que no se puede devolver un
			// 500: lo único que queda es dejar rastro. Sin esto, una plantilla
			// rota se ve como una página a medias con status 200.
			slog.Error("no se pudo renderizar los logs", "err", errPlantilla)
		}
	})

	// /eventos es la vista que faltaba: qué pasó y cuándo, en una sola línea de
	// tiempo. Hasta ahora los incidentes estaban abajo del panel, los reinicios
	// no se registraban en ningún lado y los errores de log había que ir a
	// buscarlos a mano con el filtro puesto.
	mux.HandleFunc("GET /eventos", func(w http.ResponseWriter, r *http.Request) {
		v := ventanaDe(r, time.Now(), zona)
		sevs := severidadesValidas(r.URL.Query()["sev"])

		incidentes, err := d.UltimosIncidentes(200)
		if err != nil {
			http.Error(w, "no se pudieron leer los incidentes", http.StatusInternalServerError)
			return
		}
		eventos, err := d.EventosEntre(v.Desde, v.Hasta, 200)
		if err != nil {
			http.Error(w, "no se pudieron leer los eventos", http.StatusInternalServerError)
			return
		}
		// Solo ERROR: un WARN por container y por minuto llenaría la línea de
		// tiempo y la volvería tan inútil como el visor de logs sin filtro.
		errores, err := d.BuscarLogs("", "", []string{"ERROR"}, v.Desde, v.Hasta, 200)
		if err != nil {
			http.Error(w, "no se pudieron leer los errores", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		errPlantilla := plantillaPanel.ExecuteTemplate(w, "eventos.html", struct {
			Nav       nav
			Zona      *time.Location
			Ventana   Ventana
			Horas     int
			Sevs      []toggleNivel
			Novedades []Novedad
			Rangos    []struct {
				Valor int
				Texto string
			}
		}{
			Nav: nav{Activo: "eventos", Horas: v.Horas}, Zona: zona,
			Ventana: v, Horas: v.Horas, Sevs: togglesSeveridad(sevs),
			Novedades: armarNovedades(incidentes, eventos, errores, v.Desde, v.Hasta, sevs),
			Rangos:    rangos,
		})
		if errPlantilla != nil {
			slog.Error("no se pudo renderizar los eventos", "err", errPlantilla)
		}
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /api/series", func(w http.ResponseWriter, r *http.Request) {
		horas := horasDe(r)
		hasta := time.Now()
		muestras, err := d.SerieHost(hasta.Add(-time.Duration(horas)*time.Hour), hasta)
		if err != nil {
			http.Error(w, "no se pudo leer la serie", http.StatusInternalServerError)
			return
		}

		// Arrays paralelos y no objetos: es lo que ECharts consume directo y
		// pesa la mitad. Los cuatro tienen que tener el mismo largo o el
		// gráfico dibuja cualquier cosa sin quejarse.
		salida := struct {
			TS    []int64   `json:"ts"`
			CPU   []float64 `json:"cpu"`
			Mem   []float64 `json:"mem"`
			Disco []float64 `json:"disco"`
			Load  []float64 `json:"load"`
			// Memoria y disco van también en GiB: el toggle de unidad del
			// panel cambia el gráfico sin otro round-trip. Los totales son
			// para la escala del eje y salen de la última muestra.
			MemGiB        []float64 `json:"mem_gib"`
			DiscoGiB      []float64 `json:"disco_gib"`
			MemTotalGiB   float64   `json:"mem_total_gib"`
			DiscoTotalGiB float64   `json:"disco_total_gib"`
		}{
			TS:       make([]int64, 0, len(muestras)),
			CPU:      make([]float64, 0, len(muestras)),
			Mem:      make([]float64, 0, len(muestras)),
			Disco:    make([]float64, 0, len(muestras)),
			Load:     make([]float64, 0, len(muestras)),
			MemGiB:   make([]float64, 0, len(muestras)),
			DiscoGiB: make([]float64, 0, len(muestras)),
		}
		for _, m := range muestras {
			salida.TS = append(salida.TS, m.TS.Unix())
			salida.CPU = append(salida.CPU, m.CPUPctAvg)
			salida.Mem = append(salida.Mem, pct(m.MemUsedBytes, m.MemTotalBytes))
			salida.Disco = append(salida.Disco, pct(m.DiskUsedBytes, m.DiskTotalBytes))
			salida.Load = append(salida.Load, m.Load1)
			salida.MemGiB = append(salida.MemGiB, gib(m.MemUsedBytes))
			salida.DiscoGiB = append(salida.DiscoGiB, gib(m.DiskUsedBytes))
		}
		if n := len(muestras); n > 0 {
			salida.MemTotalGiB = gib(muestras[n-1].MemTotalBytes)
			salida.DiscoTotalGiB = gib(muestras[n-1].DiskTotalBytes)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(salida)
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		v := vistaPanel{Horas: horasDe(r), Rangos: rangos, Zona: zona}
		v.Nav = nav{Activo: "panel", Horas: v.Horas}

		hs, err := d.UltimasHostSamples(1)
		if err != nil {
			http.Error(w, "no se pudo leer el host", http.StatusInternalServerError)
			return
		}
		if len(hs) > 0 {
			v.Host = hs[0]
		}
		if v.Containers, err = d.UltimoEstadoContainers(); err != nil {
			http.Error(w, "no se pudieron leer los containers", http.StatusInternalServerError)
			return
		}
		if v.Probes, err = d.UltimoEstadoProbes(); err != nil {
			http.Error(w, "no se pudieron leer los servicios", http.StatusInternalServerError)
			return
		}
		if v.Incidentes, err = d.UltimosIncidentes(15); err != nil {
			http.Error(w, "no se pudieron leer los incidentes", http.StatusInternalServerError)
			return
		}

		// Lo roto arriba, que es para lo que uno abre el panel; entre lo sano,
		// el servicio más lento y el container más pesado primero. El orden por
		// columna del navegador sigue disponible para todo lo demás.
		sort.SliceStable(v.Probes, func(i, j int) bool {
			if v.Probes[i].OK != v.Probes[j].OK {
				return !v.Probes[i].OK
			}
			return v.Probes[i].Latencia > v.Probes[j].Latencia
		})
		mal := func(c model.ContainerSample) bool {
			return c.State != "running" || c.Health == "unhealthy"
		}
		sort.SliceStable(v.Containers, func(i, j int) bool {
			if mal(v.Containers[i]) != mal(v.Containers[j]) {
				return mal(v.Containers[i])
			}
			return v.Containers[i].MemBytes > v.Containers[j].MemBytes
		})

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := plantillaPanel.ExecuteTemplate(w, "panel.html", v); err != nil {
			// Ya se empezó a escribir el cuerpo: solo queda registrar.
			return
		}
	})

	return mux
}

// Ventana es el rango de tiempo pedido, ya resuelto.
//
// desde/hasta explícitos le ganan al ?horas=, que queda como atajo. Antes solo
// existía el atajo, anclado siempre a time.Now(): no había forma de mirar una
// franja del pasado, que es justo lo que hace falta cuando algo ya pasó.
type Ventana struct {
	Desde, Hasta time.Time
	Horas        int // 0 cuando el rango es explícito
	Explicita    bool
	Zona         *time.Location
}

// en devuelve la hora en la zona del panel. Existe para que ningún formateo de
// esta capa vuelva a caer en time.Local por descuido: el VPS corre en Etc/UTC.
func (v Ventana) en(t time.Time) time.Time {
	if v.Zona == nil {
		return t.UTC()
	}
	return t.In(v.Zona)
}

// formato de un <input type="datetime-local">. El navegador lo manda SIN zona,
// con la hora que el usuario tecleó, así que se interpreta en la zona
// configurada del panel y no en la del proceso.
const formLocal = "2006-01-02T15:04"

// DesdeHasta de los campos del form; si no vienen o no parsean, cae al ?horas=.
func ventanaDe(r *http.Request, ahora time.Time, zona *time.Location) Ventana {
	q := r.URL.Query()
	desde, errD := time.ParseInLocation(formLocal, q.Get("desde"), zona)
	hasta, errH := time.ParseInLocation(formLocal, q.Get("hasta"), zona)

	switch {
	case errD == nil && errH == nil:
	case errD == nil && errH != nil:
		hasta = ahora // "desde tal hora hasta ahora"
	case errD != nil && errH == nil:
		// Solo un final: se toman las horas del atajo hacia atrás desde ahí.
		desde = hasta.Add(-time.Duration(horasDe(r)) * time.Hour)
	default:
		h := horasDe(r)
		return Ventana{Desde: ahora.Add(-time.Duration(h) * time.Hour), Hasta: ahora, Horas: h, Zona: zona}
	}

	// Al revés se corrige en vez de devolver vacío: es un error de tipeo obvio
	// y mostrar "sin resultados" no ayuda a nadie a darse cuenta.
	if hasta.Before(desde) {
		desde, hasta = hasta, desde
	}
	return Ventana{Desde: desde, Hasta: hasta, Explicita: true, Zona: zona}
}

// Valor para rellenar el input. Vacío si el rango salió del atajo, así los
// campos quedan libres y se ve que manda el <select>.
func (v Ventana) ValorDesde() string {
	if !v.Explicita {
		return ""
	}
	return v.en(v.Desde).Format(formLocal)
}

func (v Ventana) ValorHasta() string {
	if !v.Explicita {
		return ""
	}
	return v.en(v.Hasta).Format(formLocal)
}

// toggleNivel es un pill del filtro: el nivel y si está prendido.
type toggleNivel struct {
	Valor  string
	Activo bool
}

// togglesDe arma los cuatro pills con los elegidos marcados: la vista siempre
// muestra el filtro completo, no solo lo que quedó prendido.
func togglesDe(elegidos []string) []toggleNivel {
	activo := map[string]bool{}
	for _, n := range elegidos {
		activo[n] = true
	}
	out := make([]toggleNivel, 0, 4)
	for _, n := range []string{"TRACE", "INFO", "WARN", "ERROR"} {
		out = append(out, toggleNivel{Valor: n, Activo: activo[n]})
	}
	return out
}

// togglesSeveridad, ídem para /eventos: info, warning y critical.
func togglesSeveridad(elegidas []string) []toggleNivel {
	activo := map[string]bool{}
	for _, s := range elegidas {
		activo[s] = true
	}
	out := make([]toggleNivel, 0, len(severidades))
	for _, s := range severidades {
		out = append(out, toggleNivel{Valor: s, Activo: activo[s]})
	}
	return out
}

// horasDe acota el rango. El parámetro es entrada de afuera aunque el panel
// sea privado: cualquier basura cae al default en vez de romper.
func horasDe(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("horas"))
	if err != nil || n < 1 || n > 720 {
		return 24
	}
	return n
}

// Los topes son distintos a propósito: la vista los renderiza como divs en un
// navegador y el export es un archivo que se abre en otro lado.
const (
	topeVista  = 500
	topeExport = 10000
)

// nivelesDe lee los toggles de nivel. Param repetido (?nivel=WARN&nivel=ERROR);
// sin ninguno vale el default de la vista, que es todo menos TRACE — el que
// hace desaparecer el ruido sin esconder nada que importe.
func nivelesDe(r *http.Request) []string {
	conjunto := logs.Conjunto(r.URL.Query()["nivel"])
	out := make([]string, len(conjunto))
	for i, n := range conjunto {
		out[i] = string(n)
	}
	return out
}

// nombreExport arma un nombre que dice qué ventana cubre. El viejo decía
// "logs-todos-24h.txt" sobre un archivo de 5 horas.
func nombreExport(container string, v Ventana) string {
	if v.Explicita {
		return fmt.Sprintf("logs-%s-%s_a_%s.txt", container,
			v.en(v.Desde).Format("2006-01-02T1504"), v.en(v.Hasta).Format("2006-01-02T1504"))
	}
	return fmt.Sprintf("logs-%s-%dh.txt", container, v.Horas)
}

func pct(usado, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(usado) * 100 / float64(total)
}

func gib(b uint64) float64 { return float64(b) / (1024 * 1024 * 1024) }
