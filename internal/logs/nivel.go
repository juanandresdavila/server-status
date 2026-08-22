// Package logs clasifica una línea de log cruda en un nivel de severidad.
//
// Existe porque los containers del VPS loguean en formatos que no se parecen
// en nada entre sí —Postgres, JSON de GoTrue, acceso estilo nginx de Kong, slog
// de Go— y sin un nivel común no hay forma de filtrar el ruido. Y el ruido no
// es un detalle estético: el 73 % de las 802 200 líneas guardadas son la
// continuación multilínea de un solo cron, y ese volumen hacía que una consulta
// de 24 h devolviera 5.
//
// Es un paquete de funciones puras: no toca base, ni red, ni reloj.
package logs

import (
	"regexp"
	"strconv"
	"strings"
)

// Nivel es la severidad de una línea, de menor a mayor: TRACE, INFO, WARN, ERROR.
type Nivel string

const (
	Trace Nivel = "TRACE"
	Info  Nivel = "INFO"
	Warn  Nivel = "WARN"
	Error Nivel = "ERROR"
)

// orden permite comparar niveles. No se expone: afuera se usa AlMenos.
var orden = map[Nivel]int{Trace: 0, Info: 1, Warn: 2, Error: 3}

// AlMenos dice si un nivel llega al mínimo pedido. Con mínimo TRACE pasa todo.
func AlMenos(n, minimo Nivel) bool { return orden[n] >= orden[minimo] }

// NivelValido convierte texto de afuera —una query string— en un nivel.
// Cualquier cosa que no reconozca cae a INFO, que es el default de la vista.
func NivelValido(s string) Nivel {
	switch Nivel(strings.ToUpper(strings.TrimSpace(s))) {
	case Trace:
		return Trace
	case Warn:
		return Warn
	case Error:
		return Error
	}
	return Info
}

var (
	// El campo level de un JSON es AUTORITATIVO y se mira antes que nada más.
	// GoTrue loguea un 400 como {"error":"...","level":"info"}: si el escaneo
	// de palabras le ganara a este campo, 125 000 líneas informativas de los
	// dos Supabase quedarían marcadas como ERROR.
	reJSON = regexp.MustCompile(`"level"\s*:\s*"([^"]+)"`)

	// Postgres: la severidad viene siempre después del pid entre corchetes.
	// Pedir \[\d+\] y no solo la palabra evita confundirla con el texto del
	// mensaje, y de paso descarta el [22/Aug/2026:...] de los logs de acceso.
	rePostgres = regexp.MustCompile(`\[\d+\]\s+(PANIC|FATAL|ERROR|WARNING|LOG|NOTICE|INFO|DEBUG\d*|DETAIL|STATEMENT|HINT|CONTEXT|QUERY):`)

	// Acceso estilo nginx/Kong: el código va justo después de la petición
	// entrecomillada. "GET /auth/v1/health HTTP/1.1" 200 107
	reAcceso = regexp.MustCompile(`"\s+(\d{3})\s`)

	// Un logger estructurado que trae método Y ruta está logueando un acceso,
	// igual que nginx pero en JSON. Pedir los dos campos evita confundir con
	// un mensaje común que apenas mencione una ruta.
	reJSONMetodo = regexp.MustCompile(`"method"\s*:\s*"[A-Za-z]+"`)
	reJSONRuta   = regexp.MustCompile(`"(?:path|url|uri)"\s*:\s*"`)

	// El código puede venir en su propio campo o al principio del mensaje,
	// que es como lo escribe GoTrue: "msg":"400: Unsupported provider".
	reJSONEstado = regexp.MustCompile(`"(?:status|status_code|response_code)"\s*:\s*"?(\d{3})"?`)
	reJSONMsgCod = regexp.MustCompile(`"msg"\s*:\s*"(\d{3})\b`)

	// level=error, lvl: warn, severity=info.
	reEtiqueta = regexp.MustCompile(`(?i)\b(?:level|lvl|severity)\s*[=:]\s*"?([a-z]+)"?`)

	// [ERROR], [warn].
	reCorchete = regexp.MustCompile(`(?i)\[(trace|debug|info|notice|warn|warning|err|error|fatal|panic)\]`)

	// Una palabra suelta, pero SOLO en mayúsculas: así "INFO" del slog de Go
	// cuenta y la palabra "error" en medio de una frase en prosa no.
	reSuelto = regexp.MustCompile(`\b(TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|FATAL|PANIC)\b`)
)

// Clasificar devuelve el nivel de una línea. El stream es el último recurso:
// solo decide cuando la línea no trae ninguna marca reconocible.
func Clasificar(linea, stream string) Nivel {
	// 1. Continuación de una línea anterior. Un tab, o dos espacios o más:
	//    Postgres arranca sus líneas propias con UN espacio antes de la fecha,
	//    así que pedir dos las deja afuera y no se las traga como continuación.
	if strings.HasPrefix(linea, "\t") || strings.HasPrefix(linea, "  ") {
		return Trace
	}

	// 2. El campo level de un JSON le gana a todo lo demás.
	if m := reJSON.FindStringSubmatch(linea); m != nil {
		n := dePalabra(m[1], Info)
		// Salvo por una cosa: un log de ACCESO es acceso lo escriba nginx o un
		// logger estructurado, y el 200 de Kong ya va a TRACE. Degradar solo lo
		// que el propio logger consideró informativo — si dijo error, se le cree.
		if n == Info && esAccesoJSON(linea) {
			if c, ok := estadoJSON(linea); ok {
				return deCodigoHTTP(c)
			}
			return Trace
		}
		return n
	}

	// 3. Severidad de Postgres.
	if m := rePostgres.FindStringSubmatch(linea); m != nil {
		return dePostgres(m[1])
	}

	// 4. Código de respuesta de un log de acceso. Un 200 de healthcheck por
	//    minuto no es información: son 590 líneas por día por cada Kong.
	if m := reAcceso.FindStringSubmatch(linea); m != nil {
		if c, err := strconv.Atoi(m[1]); err == nil {
			return deCodigoHTTP(c)
		}
	}

	// 5. Etiquetas sueltas, de la más explícita a la más ambigua.
	if m := reEtiqueta.FindStringSubmatch(linea); m != nil {
		return dePalabra(m[1], porStream(stream))
	}
	if m := reCorchete.FindStringSubmatch(linea); m != nil {
		return dePalabra(m[1], porStream(stream))
	}
	if m := reSuelto.FindStringSubmatch(linea); m != nil {
		return dePalabra(m[1], porStream(stream))
	}

	return porStream(stream)
}

// dePostgres mapea la severidad de Postgres.
//
// DETAIL, STATEMENT, HINT, CONTEXT y QUERY son el cuerpo de otra línea, no un
// hecho nuevo: van a TRACE para que no dupliquen el evento que ya reportó su
// línea principal.
func dePostgres(sev string) Nivel {
	switch sev {
	case "PANIC", "FATAL", "ERROR":
		return Error
	case "WARNING":
		return Warn
	case "LOG", "NOTICE", "INFO":
		return Info
	}
	return Trace
}

// esAccesoJSON dice si la línea reporta una petición HTTP servida.
func esAccesoJSON(linea string) bool {
	return reJSONMetodo.MatchString(linea) && reJSONRuta.MatchString(linea)
}

// estadoJSON saca el código de respuesta, de su campo propio o del principio
// del mensaje. Devuelve false si la línea no lo trae.
func estadoJSON(linea string) (int, bool) {
	for _, re := range []*regexp.Regexp{reJSONEstado, reJSONMsgCod} {
		if m := re.FindStringSubmatch(linea); m != nil {
			if c, err := strconv.Atoi(m[1]); err == nil {
				return c, true
			}
		}
	}
	return 0, false
}

func deCodigoHTTP(c int) Nivel {
	switch {
	case c >= 500:
		return Error
	case c >= 400:
		return Warn
	}
	return Trace
}

// dePalabra traduce el nombre de un nivel. Si no lo reconoce devuelve el
// default que le pasen, que según el caso es INFO o lo que diga el stream.
func dePalabra(s string, porDefecto Nivel) Nivel {
	switch strings.ToLower(s) {
	case "panic", "fatal", "critical", "crit", "err", "error", "severe":
		return Error
	case "warn", "warning":
		return Warn
	case "info", "notice", "information":
		return Info
	case "trace", "debug", "verbose":
		return Trace
	}
	return porDefecto
}

// porStream es el último recurso. No es una gran señal —muchas apps escriben
// todo por stderr— pero distingue algo, y tirar todo a INFO no distinguiría nada.
func porStream(stream string) Nivel {
	if stream == "stderr" {
		return Warn
	}
	return Info
}
