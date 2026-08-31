package web

import "net/http"

// texto es un rótulo del panel en los dos idiomas. El español es el original;
// el inglés existe porque el panel puede tener que mostrarse a alguien que no
// lee castellano, y las rutas ya quedaron en inglés.
type texto struct{ ES, EN string }

// textos: la clave es lo que las plantillas escriben en {{ t "clave" }}. Los
// textos que se arman en Go (truncados, títulos de eventos, export) también
// salen de acá, vía tr — un solo lugar que traducir.
var textos = map[string]texto{
	// nav
	"nav-eventos": {"eventos", "events"},
	// panel: la línea de arriba y las tarjetas
	"carga": {"carga", "load"},
	// El load average no tiene unidad: es el promedio de procesos corriendo o
	// esperando disco. Sin decir a qué intervalos corresponde cada número, y
	// contra cuántos núcleos se compara, "0.48 / 0.55 / 0.54" no se puede leer.
	"carga-detalle": {"1m/5m/15m · %d vCPU", "1m/5m/15m · %d vCPU"},
	"actualizado":   {"actualizado", "updated"},
	"cpu-pico":      {"CPU pico", "CPU peak"},
	"memoria":       {"Memoria", "Memory"},
	"disco":         {"Disco", "Disk"},

	// panel: secciones y tablas
	"historial":      {"Historial", "History"},
	"servicios":      {"Servicios", "Services"},
	"incidentes":     {"Incidentes", "Incidents"},
	"servicio":       {"Servicio", "Service"},
	"estado":         {"Estado", "Status"},
	"codigo":         {"Código", "Code"},
	"latencia":       {"Latencia", "Latency"},
	"detalle":        {"Detalle", "Detail"},
	"caido":          {"CAÍDO", "DOWN"},
	"nombre":         {"Nombre", "Name"},
	"salud":          {"Salud", "Health"},
	"reinicios":      {"Reinicios", "Restarts"},
	"sujeto":         {"Sujeto", "Subject"},
	"severidad":      {"Severidad", "Severity"},
	"desde-col":      {"Desde", "From"},
	"hasta-col":      {"Hasta", "Until"},
	"abierto":        {"abierto", "open"},
	"sin-incidentes": {"sin incidentes registrados", "no incidents recorded"},
	"resolver":       {"resolver", "resolve"},
	"archivar":       {"archivar", "archive"},
	"archivado":      {"archivado", "archived"},
	"ver-archivados": {"ver archivados", "show archived"},
	"ocultar-archiv": {"ocultar archivados", "hide archived"},
	// Dos columnas porque son dos preguntas distintas: cuántas veces arrancó
	// de nuevo en la ventana que estoy mirando, y qué dice el contador de
	// Docker (que solo cuenta reinicios por política y se resetea al recrear).
	"reinicios-vent": {"Reinicios (ventana)", "Restarts (window)"},
	"reinicios-dock": {"Docker", "Docker"},

	// panel: títulos de los gráficos (viven en el JS de la plantilla)
	"g-cpu":       {"CPU %", "CPU %"},
	"g-mem":       {"Memoria %", "Memory %"},
	"g-mem-gib":   {"Memoria (GiB)", "Memory (GiB)"},
	"g-disco":     {"Disco %", "Disk %"},
	"g-disco-gib": {"Disco (GiB)", "Disk (GiB)"},
	"g-load":      {"Carga (1 min)", "Load (1 min)"},

	// rangos: la clave lleva el valor en horas ({{ t (printf "rango-%d" .) }})
	"rango-1":   {"última hora", "last hour"},
	"rango-6":   {"6 horas", "6 hours"},
	"rango-24":  {"24 horas", "24 hours"},
	"rango-168": {"7 días", "7 days"},
	"rango-720": {"30 días", "30 days"},

	// logs
	"buscar-ph":      {"buscar… (probá conex* o error)", "search… (try conex* or error)"},
	"todos":          {"todos los containers", "all containers"},
	"desde":          {"desde", "from"},
	"hasta":          {"hasta", "until"},
	"exportar":       {"exportar", "export"},
	"sin-resultados": {"sin resultados", "no results"},
	"truncado-vista": {
		"Se alcanzó el tope de %d líneas: esto cubre desde %s, no desde %s. Achicá la ventana, elegí un container o apagá niveles ruidosos.",
		"Hit the %d-line cap: this covers from %s, not from %s. Narrow the window, pick a container or turn off noisy levels.",
	},

	// export (texto plano)
	"export-pedido":   {"# pedido: %s → %s (niveles %s)\n", "# requested: %s → %s (levels %s)\n"},
	"export-truncado": {"# TRUNCADO en %d líneas: el archivo cubre desde %s, no desde lo pedido.\n", "# TRUNCATED at %d lines: the file covers from %s, not from what was requested.\n"},
	"export-consejo":  {"# Achicá la ventana, filtrá por container o apagá niveles ruidosos.\n", "# Narrow the window, filter by container or turn off noisy levels.\n"},
	"export-lineas":   {"# %d líneas\n\n", "# %d lines\n\n"},

	// logs: modo en vivo y tope
	"en-vivo-toggle": {"en vivo", "live"},
	"tope":           {"tope", "cap"},

	// reglas de nivel
	"regla-nueva":      {"regla de nivel", "level rule"},
	"patron":           {"patrón (la línea tiene que contenerlo, tal cual, respetando mayúsculas)", "pattern (the line must contain it, exactly, case-sensitive)"},
	"container":        {"container", "container"},
	"nivel-nuevo":      {"nivel que le queda", "level it gets"},
	"nivel-actual":     {"hoy", "now"},
	"motivo":           {"motivo", "reason"},
	"motivo-ph":        {"por qué esto no es lo que el clasificador cree", "why this isn't what the classifier thinks"},
	"lineas-afectadas": {"líneas guardadas cambian de nivel al confirmar", "stored lines change level on confirm"},
	"recalcular":       {"recalcular", "recount"},
	"crear-regla":      {"crear la regla", "create the rule"},
	"cancelar":         {"cancelar", "cancel"},

	"reglas-titulo": {"reglas de nivel", "level rules"},
	"reglas-nota": {
		"Cambian el nivel guardado de las líneas que contienen el patrón, las viejas y las que vengan. Borrar una devuelve esas líneas a su nivel original. Nada de esto toca los avisos de Telegram.",
		"They change the stored level of every line containing the pattern, old and new. Deleting one puts those lines back to their original level. None of this touches Telegram alerts.",
	},
	// Sin plural en el número: "1 reglas activas" se lee como un bug.
	"reglas-activas": {"reglas activas: %d", "active rules: %d"},
	"cambiar-nivel":  {"hacer una regla con esta línea", "make a rule from this line"},
	"patron-col":     {"Patrón", "Pattern"},
	"nivel-col":      {"Nivel", "Level"},
	"creada-col":     {"Creada", "Created"},
	"coincidencias":  {"líneas hoy", "lines today"},
	"borrar":         {"borrar", "delete"},
	"sin-reglas":     {"no hay ninguna regla puesta", "no rules set"},

	// tail
	"conectando":   {"conectando…", "connecting…"},
	"en-vivo":      {"en vivo", "live"},
	"desconectado": {"desconectado — recargá para reintentar", "disconnected — reload to retry"},

	// eventos: títulos y orígenes que se arman en Go
	"sin-novedades":     {"Sin novedades en esta ventana. Es la respuesta que uno quiere.", "Nothing new in this window. That's the answer you want."},
	"se-abrio":          {"se abrió", "opened"},
	"se-cerro":          {"se cerró", "closed"},
	"estuvo-mal":        {"estuvo mal", "was down for"},
	"reboot":            {"el servidor se reinició", "the server rebooted"},
	"container_restart": {"containers reiniciados", "containers restarted"},
	"monitor_start":     {"el monitor arrancó", "the monitor started"},
	"origen-incidente":  {"incidente", "incident"},
	"origen-evento":     {"evento", "event"},
	"origen-log":        {"log", "log"},

	// sujetos legibles ('host:disk' → 'disco'/'disk')
	"sujeto-disk": {"disco", "disk"},
	"sujeto-mem":  {"memoria", "memory"},
	"sujeto-swap": {"swap", "swap"},
	"sujeto-load": {"carga", "load"},
}

// tr traduce una clave. Una clave sin texto vuelve tal cual: un rótulo raro a
// la vista es mejor que uno desaparecido en silencio.
func tr(idioma, clave string) string {
	t, ok := textos[clave]
	if !ok {
		return clave
	}
	if idioma == "en" {
		return t.EN
	}
	return t.ES
}

// idiomaDe resuelve el idioma del request: el query param manda —y se guarda
// en la cookie, porque el panel se recarga solo cada 60 s y los links no
// arrastran el parámetro—, después la cookie, y el default es español.
func idiomaDe(w http.ResponseWriter, r *http.Request) string {
	if l := r.URL.Query().Get("lang"); l == "es" || l == "en" {
		http.SetCookie(w, &http.Cookie{Name: "lang", Value: l, Path: "/", MaxAge: 365 * 24 * 3600})
		return l
	}
	if c, err := r.Cookie("lang"); err == nil && (c.Value == "es" || c.Value == "en") {
		return c.Value
	}
	return "es"
}
