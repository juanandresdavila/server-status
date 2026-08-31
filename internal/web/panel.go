package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime"
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
	// El modo en vivo de /logs. El cursor es el rowid y no el ts: ver
	// LogsDesdeRowid en store para por qué eso no es un detalle.
	LogsDesdeRowid(texto, container string, niveles []string, desde int64, limite int) ([]model.LineaLog, int64, error)
	MaxRowidLogs() (int64, error)
	// Reinicios observados por container DENTRO de la ventana, que es otra
	// cosa que el RestartCount de Docker que muestra la columna de al lado.
	ReiniciosEntre(desde, hasta time.Time) (map[string]int, error)
	UltimasHostSamples(n int) ([]model.HostSample, error)
	UltimoEstadoContainers() ([]model.ContainerSample, error)
	UltimoEstadoProbes() ([]model.ProbeResult, error)
	UltimosIncidentes(n int) ([]model.Incidente, error)
	SerieHost(desde, hasta time.Time) ([]model.HostSample, error)
	EventosEntre(desde, hasta time.Time, limite int) ([]model.Evento, error)
	// Las dos acciones del panel sobre incidentes. Cerrar es el mismo método
	// que usa el motor: por la cola derivada eso manda el aviso de cierre, y
	// si el sujeto sigue mal el motor lo reabre en el próximo tick.
	CerrarIncidente(id int64, cuando time.Time) error
	ArchivarIncidente(id int64, cuando time.Time) error
	// Las reglas de nivel: la lista, el conteo previo, crear y borrar. El
	// conteo y el efecto salen de la MISMA definición de coincidencia en el
	// store, que es lo que hace que el número que se confirma sea el que
	// queda.
	ReglasNivel() ([]model.ReglaNivel, error)
	ContarPorPatron(patron, container string) (int, error)
	CrearReglaNivel(r model.ReglaNivel) (int64, int, error)
	BorrarReglaNivel(id int64) (int, error)
	// La línea que el usuario tocó en /logs, para prellenar la regla.
	LineaPorRowid(rowid int64) (model.LineaLog, bool, error)
}

// plantillasCon parsea el set completo con t cerrada sobre un idioma: las
// plantillas se escriben igual que antes y el idioma no viaja en cada dato.
// Se parsea una vez por idioma al arrancar — hacerlo por request sería tirar
// el trabajo 1440 veces por día por pestaña abierta.
func plantillasCon(idioma string) *template.Template {
	return template.Must(template.New("panel").Funcs(template.FuncMap{
		"pct": pct,
		"gib": gib,
		"mib": func(b uint64) float64 { return float64(b) / (1024 * 1024) },
		// Sin zona fija sería t.Local(), y el VPS corre en Etc/UTC: el panel venía
		// mostrando UTC mientras uno lo leía como hora argentina. La zona sale de
		// la config, que es la misma que usa el resumen diario.
		"hora": func(t time.Time, loc *time.Location) string { return enZona(t, loc).Format("02/01/2006 15:04") },
		"en":   enZona,
		"t":    func(clave string) string { return tr(idioma, clave) },
		"lang": func() string { return idioma },
	}).ParseFS(plantillas, "plantillas/nav.html", "plantillas/panel.html",
		"plantillas/logs.html", "plantillas/tail.html", "plantillas/eventos.html",
		"plantillas/regla-nueva.html"))
}

var plantillasIdioma = map[string]*template.Template{
	"es": plantillasCon("es"),
	"en": plantillasCon("en"),
}

// nav es lo que el header necesita saber de la vista que lo está pintando.
// Container puede venir vacío: sin container elegido no hay tail al que ir.
type nav struct {
	Activo    string // panel | eventos | logs | tail
	Horas     int
	Container string
	Enlaces   []Enlace
}

// Enlace es un acceso externo que el nav ofrece y este proceso NO atiende: la
// terminal de Cockpit, la pantalla por Guacamole.
//
// Que sean externos no es una comodidad, es la decisión. El panel no tiene
// autenticación de ninguna clase —toda su seguridad es "estás en el tailnet"—
// y este proceso habla con el socket de Docker, que equivale a root en el host.
// Una terminal servida desde acá convertiría "llegué al puerto del panel" en
// "tengo root", sin una sola contraseña de por medio. Cockpit y Guacamole
// tienen su propio login, y por eso el acceso remoto vive afuera.
type Enlace struct {
	Nombre string
	URL    string
}

// rangos son las opciones de tiempo en horas, iguales en las tres vistas a
// propósito: el header propaga ?horas= entre ellas, y un valor que el <select>
// de logs no tuviera se vería como "última hora" mientras filtra por otra
// cosa. El rótulo de cada una sale de textos, clave "rango-<horas>".
var rangos = []int{1, 6, 24, 168, 720}

type vistaPanel struct {
	Nav        nav
	Zona       *time.Location
	Host       model.HostSample
	Containers []model.ContainerSample
	Probes     []model.ProbeResult
	Incidentes []model.Incidente
	Horas      int
	Rangos     []int
	// Reinicios observados en la ventana, por nombre de container. Es OTRA
	// COSA que ContainerSample.Restarts, que es el RestartCount de Docker:
	// ese solo cuenta los reinicios por política y una recreación lo pone en
	// cero. Las dos columnas se muestran juntas porque responden preguntas
	// distintas.
	Reinicios map[string]int
	// Archivados dice si la tabla de incidentes está mostrando también los
	// archivados. Es un toggle y no una vista aparte: archivar es "ya lo vi",
	// no "que no haya pasado".
	Archivados bool
	// Cores es contra qué se lee el load average. runtime.NumCPU() sirve porque
	// este proceso corre como unit EN EL HOST y no adentro de un container: ahí
	// vería los cores de la máquina igual, pero el número dejaría de significar
	// lo mismo que el load average que se muestra al lado.
	Cores int
	// Volver es la URL a la que vuelven resolver y archivar. Sin esto las dos
	// acciones mandaban a "/" y te sacaban de la vista en la que estabas —
	// justo la de archivados, que es donde uno archiva de a varios.
	Volver string
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
func NuevoPanel(d Datos, zona *time.Location, enlaces ...Enlace) http.Handler {
	if zona == nil {
		zona = time.UTC
	}
	mux := http.NewServeMux()

	// Los enlaces externos van en TODOS los navs, y el nav se arma en cinco
	// handlers: pasar el slice a mano en cada uno es la forma segura de que
	// uno quede sin ellos y parezca que el enlace desapareció.
	armarNav := func(activo string, horas int, container string) nav {
		return nav{Activo: activo, Horas: horas, Container: container, Enlaces: enlaces}
	}

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
		plantillasIdioma[idiomaDe(w, r)].ExecuteTemplate(w, "tail.html", struct {
			Nav       nav
			Container string
		}{armarNav("tail", horasDe(r), c), c})
	})

	// El export acepta los mismos filtros que la vista y devuelve texto plano
	// para descargar. El tope es más alto que el de la vista: acá no hay
	// navegador renderizando 10 000 divs, es un archivo que se abre en otro lado.
	mux.HandleFunc("GET /logs/export", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		idioma := idiomaDe(w, r)
		v := ventanaDe(r, time.Now(), zona)
		niveles := nivelesDe(r)

		tope := max(limiteDe(r), topeExport)
		lineas, err := d.BuscarLogs(q.Get("q"), q.Get("container"), niveles,
			v.Desde, v.Hasta, tope)
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
		fmt.Fprintf(w, tr(idioma, "export-pedido"),
			v.en(v.Desde).Format(time.DateTime), v.en(v.Hasta).Format(time.DateTime),
			strings.Join(niveles, ","))
		if len(lineas) == tope {
			fmt.Fprintf(w, tr(idioma, "export-truncado"),
				tope, v.en(lineas[len(lineas)-1].TS).Format(time.DateTime))
			fmt.Fprint(w, tr(idioma, "export-consejo"))
		}
		fmt.Fprintf(w, tr(idioma, "export-lineas"), len(lineas))

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
		idioma := idiomaDe(w, r)
		v := ventanaDe(r, time.Now(), zona)
		niveles := nivelesDe(r)

		limite := limiteDe(r)
		lineas, err := d.BuscarLogs(q.Get("q"), q.Get("container"), niveles,
			v.Desde, v.Hasta, limite)
		if err != nil {
			http.Error(w, "no se pudieron buscar los logs", http.StatusInternalServerError)
			return
		}

		// El cursor del modo en vivo se siembra con lo último que hay AHORA, no
		// con la línea más nueva que se está mostrando: si la vista quedó
		// truncada por el tope, arrancar desde la más nueva mostrada haría que
		// el primer poll trajera de golpe todo lo que el tope dejó afuera.
		cursor, err := d.MaxRowidLogs()
		if err != nil {
			slog.Error("no se pudo sembrar el cursor del modo en vivo", "err", err)
		}

		// Truncar en silencio es lo que hizo que una consulta de 24 h se leyera
		// como 24 h cubriendo 5. Si se llegó al tope hay que decirlo, y decir
		// hasta dónde llega de verdad lo que se está mostrando.
		truncado := ""
		if len(lineas) == limite {
			truncado = fmt.Sprintf(tr(idioma, "truncado-vista"),
				limite,
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
		errPlantilla := plantillasIdioma[idioma].ExecuteTemplate(w, "logs.html", struct {
			Nav          nav
			Zona         *time.Location
			Q, Container string
			Horas        int
			Ventana      Ventana
			Truncado     string
			Containers   []string
			Lineas       []model.LineaLog
			Niveles      []toggleNivel
			Rangos       []int
			Limite       int
			Topes        []int
			Cursor       int64
			// EnVivo dice si el modo en vivo TIENE SENTIDO en esta vista, no si
			// está prendido: con desde/hasta explícitos uno mira el pasado, y
			// pegar líneas nuevas arriba mentiría sobre la ventana que pidió.
			EnVivo bool
		}{
			Nav:  armarNav("logs", v.Horas, q.Get("container")),
			Zona: zona,
			Q:    q.Get("q"), Container: q.Get("container"),
			Horas: v.Horas, Ventana: v, Truncado: truncado,
			Containers: nombres, Lineas: lineas, Niveles: togglesDe(niveles), Rangos: rangos,
			Limite: limite, Topes: topesVista, Cursor: cursor, EnVivo: !v.Explicita,
		})
		if errPlantilla != nil {
			// El cuerpo ya se empezó a escribir, así que no se puede devolver un
			// 500: lo único que queda es dejar rastro. Sin esto, una plantilla
			// rota se ve como una página a medias con status 200.
			slog.Error("no se pudo renderizar los logs", "err", errPlantilla)
		}
	})

	// El poll del modo en vivo. Es JSON y no SSE a propósito: SSE deja una
	// conexión viva por pestaña abierta —el tail la necesita porque sigue a
	// Docker en tiempo real, esto no— y se muere en cada suspensión del
	// portátil sin reconectar solo. Un fetch cada tantos segundos se recupera
	// de eso sin código.
	//
	// El piso real de frescura es el tick de un minuto de la ingesta: los logs
	// entran a la base cuando el ciclo los vuelca, así que pollear cada un
	// segundo serían sesenta consultas para un dato que se mueve una vez.
	mux.HandleFunc("GET /api/logs/nuevos", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cursor, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)

		lineas, siguiente, err := d.LogsDesdeRowid(q.Get("q"), q.Get("container"),
			nivelesDe(r), cursor, topeVivo)
		if err != nil {
			http.Error(w, "no se pudieron buscar los logs", http.StatusInternalServerError)
			return
		}

		// La hora se formatea acá y no en el navegador porque la zona del panel
		// sale de la config y NO es la del proceso ni la del cliente: el VPS
		// corre en Etc/UTC. Que el modo en vivo pinte una hora distinta a la de
		// las líneas de abajo sería peor que no tener modo en vivo.
		type lineaJSON struct {
			Hora      string `json:"hora"`
			Nivel     string `json:"nivel"`
			Container string `json:"container"`
			Linea     string `json:"linea"`
		}
		salida := struct {
			Cursor int64       `json:"cursor"`
			Lineas []lineaJSON `json:"lineas"`
		}{Cursor: siguiente, Lineas: make([]lineaJSON, 0, len(lineas))}
		for _, l := range lineas {
			salida.Lineas = append(salida.Lineas, lineaJSON{
				Hora:      enZona(l.TS, zona).Format("02/01/2006 15:04:05"),
				Nivel:     l.Nivel,
				Container: l.Container,
				Linea:     l.Linea,
			})
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(salida)
	})

	// /events es la vista que faltaba: qué pasó y cuándo, en una sola línea de
	// tiempo. Hasta ahora los incidentes estaban abajo del panel, los reinicios
	// no se registraban en ningún lado y los errores de log había que ir a
	// buscarlos a mano con el filtro puesto.
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		idioma := idiomaDe(w, r)
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
		errPlantilla := plantillasIdioma[idioma].ExecuteTemplate(w, "eventos.html", struct {
			Nav       nav
			Zona      *time.Location
			Ventana   Ventana
			Horas     int
			Sevs      []toggleNivel
			Novedades []Novedad
			Rangos    []int
		}{
			Nav: armarNav("events", v.Horas, ""), Zona: zona,
			Ventana: v, Horas: v.Horas, Sevs: togglesSeveridad(sevs),
			Novedades: armarNovedades(incidentes, eventos, errores, v.Desde, v.Hasta, sevs, idioma),
			Rangos:    rangos,
		})
		if errPlantilla != nil {
			slog.Error("no se pudo renderizar los eventos", "err", errPlantilla)
		}
	})

	// La ruta vieja en castellano. Redirect permanente con la query intacta:
	// los marcadores y los links de los avisos viejos siguen funcionando.
	mux.HandleFunc("GET /eventos", func(w http.ResponseWriter, r *http.Request) {
		destino := "/events"
		if r.URL.RawQuery != "" {
			destino += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, destino, http.StatusMovedPermanently)
	})

	// Las acciones son POST y redirigen al panel: un GET que muta estado
	// termina disparado por el prefetch de un navegador.
	accion := func(nombre string, aplicar func(int64, time.Time) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
			if err != nil {
				http.Error(w, "id inválido", http.StatusBadRequest)
				return
			}
			if err := aplicar(id, time.Now()); err != nil {
				slog.Error("no se pudo "+nombre+" el incidente", "id", id, "err", err)
				http.Error(w, "no se pudo "+nombre, http.StatusInternalServerError)
				return
			}
			// Se vuelve a donde se estaba, no a "/": archivar de a varios desde
			// la vista de archivados te expulsaba de la vista en cada click.
			http.Redirect(w, r, rutaPropia(r.FormValue("volver")), http.StatusSeeOther)
		}
	}
	mux.HandleFunc("GET /logs/reglas/nueva", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		idioma := idiomaDe(w, r)
		v := vistaRegla{
			Niveles: nivelesConocidos,
			Volver:  rutaPropia(q.Get("volver")),
			// TRACE por default: silenciar ruido es el caso que motivó todo esto.
			// Subir a ERROR sigue estando a un click.
			Nivel: string(logs.Trace),
		}

		if q.Has("patron") {
			// Segunda vuelta: recalcular es re-submitear este mismo GET, así que
			// el conteo tiene que salir de lo que el usuario editó y no de lo que
			// se sugirió al abrir.
			v.Patron, v.Container = q.Get("patron"), q.Get("container")
			v.Motivo, v.Linea, v.Actual = q.Get("motivo"), q.Get("linea"), q.Get("actual")
			if esNivel(q.Get("nivel")) {
				v.Nivel = q.Get("nivel")
			}
		} else {
			rowid, err := strconv.ParseInt(q.Get("rowid"), 10, 64)
			if err != nil {
				http.Error(w, "rowid inválido", http.StatusBadRequest)
				return
			}
			l, hay, err := d.LineaPorRowid(rowid)
			if err != nil {
				http.Error(w, "no se pudo leer la línea", http.StatusInternalServerError)
				return
			}
			// La retención pudo llevarse la línea entre que se pintó la vista y se
			// hizo click. Es un 404 y no un 500: no se rompió nada.
			if !hay {
				http.Error(w, "esa línea ya no está guardada", http.StatusNotFound)
				return
			}
			v.Linea, v.Container, v.Actual = l.Linea, l.Container, l.Nivel
			v.Patron = logs.PatronSugerido(l.Linea)
		}
		v.Nav = armarNav("logs", horasDe(r), v.Container)

		if strings.TrimSpace(v.Patron) != "" {
			n, err := d.ContarPorPatron(v.Patron, v.Container)
			if err != nil {
				http.Error(w, "no se pudieron contar las coincidencias", http.StatusInternalServerError)
				return
			}
			v.Afectadas, v.HayConteo = n, true
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := plantillasIdioma[idioma].ExecuteTemplate(w, "regla-nueva.html", v); err != nil {
			slog.Error("no se pudo renderizar la regla nueva", "err", err)
		}
	})

	mux.HandleFunc("POST /logs/reglas", func(w http.ResponseWriter, r *http.Request) {
		patron := r.FormValue("patron")
		nivel := r.FormValue("nivel")
		// El motivo NO es decorativo: dentro de tres meses, una regla sin motivo
		// es una regla que nadie se anima a borrar. Por eso es obligatorio.
		motivo := strings.TrimSpace(r.FormValue("motivo"))
		if strings.TrimSpace(patron) == "" || motivo == "" || !esNivel(nivel) {
			http.Error(w, "la regla necesita patrón, nivel y motivo", http.StatusBadRequest)
			return
		}

		// ⚠️ Esto puede tardar segundos: aplica la regla a todo lo guardado sobre
		// la única conexión a SQLite. Ver CrearReglaNivel en el store.
		id, filas, err := d.CrearReglaNivel(model.ReglaNivel{
			Patron: patron, Container: r.FormValue("container"), Nivel: nivel,
			Motivo: motivo, Creada: time.Now(),
		})
		if err != nil {
			slog.Error("no se pudo crear la regla de nivel", "patron", patron, "err", err)
			http.Error(w, "no se pudo crear la regla", http.StatusInternalServerError)
			return
		}
		// Queda en el journal cuántas filas cambió: es el número que el usuario
		// confirmó, y el único rastro de una acción que reescribe datos guardados.
		slog.Info("regla de nivel creada", "id", id, "patron", patron,
			"container", r.FormValue("container"), "nivel", nivel, "filas", filas)

		http.Redirect(w, r, rutaPropia(r.FormValue("volver")), http.StatusSeeOther)
	})

	mux.HandleFunc("POST /incidents/{id}/resolve", accion("resolver", d.CerrarIncidente))
	mux.HandleFunc("POST /incidents/{id}/archive", accion("archivar", d.ArchivarIncidente))

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
		v.Nav = armarNav("panel", v.Horas, "")
		v.Archivados = r.URL.Query().Get("archivados") == "1"
		v.Cores = runtime.NumCPU()
		v.Volver = rutaPropia(r.URL.RequestURI())

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
		// Se piden de más porque los archivados se filtran acá: el panel los
		// esconde, /eventos los sigue mostrando como historia.
		incidentes, err := d.UltimosIncidentes(50)
		if err != nil {
			http.Error(w, "no se pudieron leer los incidentes", http.StatusInternalServerError)
			return
		}
		for _, i := range incidentes {
			if (v.Archivados || i.ArchivadoEn == nil) && len(v.Incidentes) < 15 {
				v.Incidentes = append(v.Incidentes, i)
			}
		}

		// Los reinicios de la MISMA ventana que eligió el select de arriba: la
		// pregunta "¿esto se reinició hoy?" no se contesta con un contador que
		// arranca en el arranque del container y se resetea al recrearlo.
		hasta := time.Now()
		if v.Reinicios, err = d.ReiniciosEntre(hasta.Add(-time.Duration(v.Horas)*time.Hour), hasta); err != nil {
			// No es motivo para tirar el panel entero abajo: sin el mapa la
			// columna muestra cero, que es lo mismo que mostraba antes.
			slog.Error("no se pudieron contar los reinicios", "err", err)
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
		if err := plantillasIdioma[idiomaDe(w, r)].ExecuteTemplate(w, "panel.html", v); err != nil {
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

// vistaRegla es el form de una regla nueva con su conteo previo.
type vistaRegla struct {
	Nav       nav
	Linea     string // la línea que la originó, como contexto
	Actual    string // el nivel que tiene hoy esa línea
	Patron    string
	Container string
	Nivel     string
	Motivo    string
	Afectadas int
	HayConteo bool
	Volver    string
	// Niveles son las cuatro opciones del <select>. Salen de
	// nivelesConocidos, la misma lista que valida el POST: si la vista
	// ofreciera una que la validación rechaza, el form sería una trampa.
	Niveles []string
}

// nivelesConocidos es la lista canónica: los pills del filtro y la validación
// del form salen de acá, para que no puedan divergir.
var nivelesConocidos = []string{"TRACE", "INFO", "WARN", "ERROR"}

// esNivel valida lo que llega del form. NivelValido de logs no sirve acá: cae
// a INFO ante cualquier basura, que es lo correcto para un filtro de la vista
// y lo contrario de lo que hace falta para guardar una regla.
func esNivel(s string) bool {
	for _, n := range nivelesConocidos {
		if s == n {
			return true
		}
	}
	return false
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
	out := make([]toggleNivel, 0, len(nivelesConocidos))
	for _, n := range nivelesConocidos {
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

// rutaPropia acepta solo rutas de este mismo servidor. Un "volver" que venga
// del form es entrada de afuera: sin este filtro, "//otro.sitio" es una URL
// absoluta para el navegador y el redirect se convierte en un open redirect.
// El panel es privado, pero un open redirect privado sigue siendo uno.
func rutaPropia(destino string) string {
	if !strings.HasPrefix(destino, "/") || strings.HasPrefix(destino, "//") {
		return "/"
	}
	return destino
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

// topesVista son las opciones del selector de tope en /logs. El de 500 que
// había antes tapaba justo lo que uno iba a buscar: una ventana de 24 h de un
// host con un container ruidoso se recortaba a menos de cinco horas.
//
// ⚠️ `ts` es UNINDEXED en la tabla FTS5 `logs`, así que una búsqueda sin texto
// es un scan completo de ~800 000 filas y subir el tope lo empeora. Está
// medido y asumido; el arreglo de fondo es otro índice, no un tope más bajo.
var topesVista = []int{5000, 10000, 25000}

// topeExport es un PISO, no un techo: el export es un archivo que se abre en
// otro lado y aguanta más que un navegador renderizando divs. Si el selector
// pide más que esto, manda el selector.
const topeExport = 10000

// topeVivo acota cuánto trae un solo poll del modo en vivo. Lo que no entra no
// se pierde —el cursor avanza pegado— y entra en el poll siguiente.
const topeVivo = 500

// limiteDe lee el selector de tope. Cualquier valor fuera de la lista cae al
// default, igual que horas: es entrada de afuera aunque el panel sea privado.
func limiteDe(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limite"))
	if err != nil {
		return topesVista[0]
	}
	for _, t := range topesVista {
		if t == n {
			return n
		}
	}
	return topesVista[0]
}

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
