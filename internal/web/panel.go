package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
)

// Datos es lo que el panel necesita de la persistencia, y nada más.
// Declarada acá y no importada de store, por la misma razón que en rules:
// deja el panel testeable sin base.
type Datos interface {
	BuscarLogs(texto, container string, desde, hasta time.Time, limite int) ([]model.LineaLog, error)
	UltimasHostSamples(n int) ([]model.HostSample, error)
	UltimoEstadoContainers() ([]model.ContainerSample, error)
	UltimoEstadoProbes() ([]model.ProbeResult, error)
	UltimosIncidentes(n int) ([]model.Incidente, error)
	SerieHost(desde, hasta time.Time) ([]model.HostSample, error)
}

var plantillaPanel = template.Must(template.New("panel").Funcs(template.FuncMap{
	"pct":  pct,
	"gib":  func(b uint64) float64 { return float64(b) / (1024 * 1024 * 1024) },
	"mib":  func(b uint64) float64 { return float64(b) / (1024 * 1024) },
	"hora": func(t time.Time) string { return t.Local().Format("02/01 15:04") },
}).ParseFS(plantillas, "plantillas/nav.html", "plantillas/panel.html",
	"plantillas/logs.html", "plantillas/tail.html"))

// nav es lo que el header necesita saber de la vista que lo está pintando.
// Container puede venir vacío: sin container elegido no hay tail al que ir.
type nav struct {
	Activo    string // panel | logs | tail
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

func NuevoPanel(d Datos) http.Handler {
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
		horas := horasDe(r)
		hasta := time.Now()

		lineas, err := d.BuscarLogs(q.Get("q"), q.Get("container"),
			hasta.Add(-time.Duration(horas)*time.Hour), hasta, 10000)
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
			fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("logs-%s-%dh.txt", nombre, horas)))
		// Vienen de la más nueva a la más vieja; se escriben al revés para que
		// el archivo se lea como una terminal.
		for i := len(lineas) - 1; i >= 0; i-- {
			l := lineas[i]
			fmt.Fprintf(w, "%s %s %s %s\n",
				l.TS.UTC().Format(time.RFC3339), l.Container, l.Stream, l.Linea)
		}
	})

	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		horas := horasDe(r)
		hasta := time.Now()

		lineas, err := d.BuscarLogs(q.Get("q"), q.Get("container"),
			hasta.Add(-time.Duration(horas)*time.Hour), hasta, 500)
		if err != nil {
			http.Error(w, "no se pudieron buscar los logs", http.StatusInternalServerError)
			return
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
		plantillaPanel.ExecuteTemplate(w, "logs.html", struct {
			Nav          nav
			Q, Container string
			Horas        int
			Containers   []string
			Lineas       []model.LineaLog
			Rangos       []struct {
				Valor int
				Texto string
			}
		}{
			Nav: nav{Activo: "logs", Horas: horas, Container: q.Get("container")},
			Q:   q.Get("q"), Container: q.Get("container"), Horas: horas,
			Containers: nombres, Lineas: lineas, Rangos: rangos,
		})
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
		}{
			TS:    make([]int64, 0, len(muestras)),
			CPU:   make([]float64, 0, len(muestras)),
			Mem:   make([]float64, 0, len(muestras)),
			Disco: make([]float64, 0, len(muestras)),
			Load:  make([]float64, 0, len(muestras)),
		}
		for _, m := range muestras {
			salida.TS = append(salida.TS, m.TS.Unix())
			salida.CPU = append(salida.CPU, m.CPUPctAvg)
			salida.Mem = append(salida.Mem, pct(m.MemUsedBytes, m.MemTotalBytes))
			salida.Disco = append(salida.Disco, pct(m.DiskUsedBytes, m.DiskTotalBytes))
			salida.Load = append(salida.Load, m.Load1)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(salida)
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		v := vistaPanel{Horas: horasDe(r), Rangos: rangos}
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

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := plantillaPanel.ExecuteTemplate(w, "panel.html", v); err != nil {
			// Ya se empezó a escribir el cuerpo: solo queda registrar.
			return
		}
	})

	return mux
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

func pct(usado, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(usado) * 100 / float64(total)
}
