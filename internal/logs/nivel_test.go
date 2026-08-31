package logs

import (
	"fmt"
	"reflect"
	"testing"
)

func TestNivelPostgres(t *testing.T) {
	casos := []struct {
		nombre string
		linea  string
		quiero Nivel
	}{
		{"panic", ` 2026-08-22 07:38:00.012 UTC [38] PANIC:  se acabó`, Error},
		{"fatal", ` 2026-08-22 07:38:00.012 UTC [38] FATAL:  no arranca`, Error},
		{"error", ` 2026-08-22 07:38:00.012 UTC [38] ERROR:  relation no existe`, Error},
		{"warning", ` 2026-08-22 07:38:00.012 UTC [38] WARNING:  algo raro`, Warn},
		{"log", ` 2026-08-22 07:38:00.012 UTC [38] LOG:  cron job 1 completed: 0 rows`, Info},
		{"notice", ` 2026-08-22 07:38:00.012 UTC [38] NOTICE:  ya existe`, Info},
		// DETAIL y STATEMENT son el cuerpo de otra línea, no un hecho nuevo.
		{"detail", ` 2026-08-22 07:38:00.012 UTC [38] DETAIL:  clave (id)=(1)`, Trace},
		{"statement", ` 2026-08-22 07:38:00.012 UTC [38] STATEMENT:  select 1`, Trace},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := Clasificar(c.linea, "stderr"); got != c.quiero {
				t.Errorf("Nivel(%q) = %q, quiero %q", c.linea, got, c.quiero)
			}
		})
	}
}

// La continuación multilínea es el 73 % de lo que guarda la base: el cron de
// pomodoro de study-master vuelca su SQL de 33 líneas por minuto. Es lo que
// hacía que un export de 24 h cubriera 5 horas.
func TestNivelContinuacionEsTrace(t *testing.T) {
	casos := []string{
		"\t  from con_subs;",
		"\t    update public.study_sessions s",
		"\t  ",
		"    select v.planned_minutes,",
	}
	for _, l := range casos {
		if got := Clasificar(l, "stderr"); got != Trace {
			t.Errorf("Nivel(%q) = %q, quiero TRACE", l, got)
		}
	}
}

// GoTrue loguea un 400 con "level":"info" y además mete una clave "error" en el
// payload. Si el escaneo de palabras le ganara al campo level, cientos de miles
// de líneas informativas se marcarían como ERROR.
func TestNivelJSONElCampoLevelGana(t *testing.T) {
	casos := []struct {
		nombre string
		linea  string
		quiero Nivel
	}{
		{"info con clave error", `{"component":"api","error":"Provider  could not be found","level":"info","method":"GET","msg":"400: Unsupported provider"}`, Info},
		{"info con error_code", `{"component":"api","duration":364023,"error_code":"validation_failed","level":"info","msg":"request completed"}`, Info},
		{"error de verdad", `{"component":"api","level":"error","msg":"no se pudo conectar"}`, Error},
		{"warning", `{"level":"warning","msg":"ojo"}`, Warn},
		{"debug es trace", `{"level":"debug","msg":"detalle"}`, Trace},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := Clasificar(c.linea, "stdout"); got != c.quiero {
				t.Errorf("Nivel(%q) = %q, quiero %q", c.linea, got, c.quiero)
			}
		})
	}
}

// Un 200 de healthcheck por minuto no es información: son 590 líneas por día
// por cada Kong, y hay dos.
//
// El User-Agent del fixture es de un cliente CUALQUIERA a propósito: el de
// nuestras propias sondas cambia el resultado en el rango 4xx, y ese caso lo
// cubre TestNivelAccesoDeSondasPropias.
func TestNivelAccesoHTTPPorCodigo(t *testing.T) {
	const base = `172.19.0.2 - - [22/Aug/2026:07:37:48 +0000] "GET /auth/v1/health HTTP/1.1" %d 107 "-" "curl/8.7.1"`
	casos := []struct {
		codigo int
		quiero Nivel
	}{
		{200, Trace},
		{301, Trace},
		{404, Warn},
		{401, Warn},
		{500, Error},
		{502, Error},
	}
	for _, c := range casos {
		linea := fmt.Sprintf(base, c.codigo)
		if got := Clasificar(linea, "stdout"); got != c.quiero {
			t.Errorf("código %d: Nivel = %q, quiero %q", c.codigo, got, c.quiero)
		}
	}
}

func TestNivelEtiquetasSueltas(t *testing.T) {
	casos := []struct {
		linea  string
		quiero Nivel
	}{
		{"[ERROR] se cayó todo", Error},
		{"[WARN] ojo con esto", Warn},
		{"level=error msg=chau", Error},
		{"level=warn msg=ojo", Warn},
		{"2026/08/22 05:00:42 INFO server-status arrancó", Info},
		{"time=... level=trace msg=detalle", Trace},
	}
	for _, c := range casos {
		if got := Clasificar(c.linea, "stdout"); got != c.quiero {
			t.Errorf("Nivel(%q) = %q, quiero %q", c.linea, got, c.quiero)
		}
	}
}

// Sin ninguna marca reconocible, el stream es lo único que queda. No es una
// gran señal —muchas apps escriben todo por stderr— pero es mejor que tirar
// todo a INFO y perder la distinción.
func TestNivelSinMarcaCaeAlStream(t *testing.T) {
	if got := Clasificar("algo pasó", "stdout"); got != Info {
		t.Errorf("stdout sin marca = %q, quiero INFO", got)
	}
	if got := Clasificar("algo pasó", "stderr"); got != Warn {
		t.Errorf("stderr sin marca = %q, quiero WARN", got)
	}
}

func TestAlMenosOrdena(t *testing.T) {
	casos := []struct {
		nivel, minimo Nivel
		quiero        bool
	}{
		{Error, Info, true},
		{Warn, Info, true},
		{Info, Info, true},
		{Trace, Info, false},
		{Trace, Trace, true},
		{Error, Error, true},
		{Warn, Error, false},
	}
	for _, c := range casos {
		if got := AlMenos(c.nivel, c.minimo); got != c.quiero {
			t.Errorf("AlMenos(%q, %q) = %v, quiero %v", c.nivel, c.minimo, got, c.quiero)
		}
	}
}

func TestNivelValidoRechazaBasura(t *testing.T) {
	if NivelValido("ERROR") != Error {
		t.Error("ERROR tiene que sobrevivir")
	}
	if NivelValido("cualquiera") != Info {
		t.Error("una basura tiene que caer al default INFO")
	}
	if NivelValido("") != Info {
		t.Error("vacío tiene que caer al default INFO")
	}
}

// Un log de acceso es un log de acceso lo escriba nginx o un logger
// estructurado. GoTrue emite 1282 "request completed" por muestra de 25 000 con
// level=info; dejarlos en INFO solo porque están en JSON, mientras el 200 de
// Kong va a TRACE, sería una inconsistencia de formato disfrazada de criterio.
func TestNivelAccesoEnJSONTambienEsTrace(t *testing.T) {
	casos := []struct {
		nombre string
		linea  string
		quiero Nivel
	}{
		{
			"request completed sin código",
			`{"component":"api","duration":83526078,"level":"info","method":"GET","msg":"request completed","path":"/user"}`,
			Trace,
		},
		{
			"el código del msg manda",
			`{"component":"api","error":"Provider  could not be found","level":"info","method":"GET","msg":"400: Unsupported provider","path":"/authorize"}`,
			Warn,
		},
		{
			"campo status explícito",
			`{"level":"info","method":"POST","path":"/token","status":500,"msg":"request completed"}`,
			Error,
		},
		{
			"un 200 explícito sigue siendo trace",
			`{"level":"info","method":"GET","path":"/health","status":200,"msg":"request completed"}`,
			Trace,
		},
		// Si el logger dijo error, se le cree: no se degrada por tener forma
		// de acceso.
		{
			"level=error no se degrada",
			`{"level":"error","method":"GET","path":"/user","msg":"request completed"}`,
			Error,
		},
		// Sin method+path no es un acceso, es un mensaje común.
		{
			"info comun no se toca",
			`{"level":"info","msg":"Request received external host in X-Forwarded-Host"}`,
			Info,
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := Clasificar(c.linea, "stdout"); got != c.quiero {
				t.Errorf("Nivel(%q) = %q, quiero %q", c.linea, got, c.quiero)
			}
		})
	}
}

// Conjunto valida lo que viene de la query string: filtra basura, dedup, y con
// nada elegido cae al default de la vista (INFO+WARN+ERROR — todo menos TRACE).
// Existe porque el filtro dejó de ser "mínimo" y pasó a ser un toggle por ítem.
func TestConjunto(t *testing.T) {
	casos := []struct {
		entrada []string
		quiero  []Nivel
	}{
		{nil, []Nivel{Info, Warn, Error}},
		{[]string{"basura"}, []Nivel{Info, Warn, Error}},
		{[]string{"ERROR"}, []Nivel{Error}},
		{[]string{"error", " warn ", "ERROR"}, []Nivel{Warn, Error}},
		{[]string{"TRACE", "ERROR"}, []Nivel{Trace, Error}},
		{[]string{"TRACE", "INFO", "WARN", "ERROR"}, []Nivel{Trace, Info, Warn, Error}},
	}
	for _, c := range casos {
		if got := Conjunto(c.entrada); !reflect.DeepEqual(got, c.quiero) {
			t.Errorf("Conjunto(%v) = %v, quería %v", c.entrada, got, c.quiero)
		}
	}
}

// Un 401 contra nuestra propia sonda es un artefacto de la medición, no un
// hecho del sistema: el destino de gym-tracker de egress-probe va SIN apikey a
// propósito (internal/egress/sonda.go), así que Kong lo rechaza ~8600 veces por
// día. Con eso en WARN, la sonda tapa el log que uno mira cuando algo se rompe.
// El 401 de un cliente de verdad, en cambio, sigue siendo WARN: es una sesión
// vencida en la app y hay que verla.
func TestNivelAccesoDeSondasPropias(t *testing.T) {
	casos := []struct {
		nombre string
		linea  string
		quiero Nivel
	}{
		{
			"401 de egress-probe es artefacto",
			`172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
			Trace,
		},
		{
			"404 del prober de producción es artefacto",
			`172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /no-existe HTTP/1.1" 404 96 "-" "server-status"`,
			Trace,
		},
		{
			"pero un 500 contra la sonda es noticia",
			`172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 500 96 "-" "egress-probe"`,
			Error,
		},
		{
			"el 401 de un navegador de verdad sigue siendo WARN",
			`172.19.0.2 - - [31/Aug/2026:15:34:35 +0000] "GET /rest/v1/workouts?select=id HTTP/1.1" 401 79 "https://gym-tracker-brown-one.vercel.app/" "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"`,
			Warn,
		},
		{
			"el nombre de la sonda en la RUTA no la disfraza de sonda",
			`172.19.0.2 - - [31/Aug/2026:15:34:35 +0000] "GET /egress-probe HTTP/1.1" 401 79 "-" "curl/8.7.1"`,
			Warn,
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := Clasificar(c.linea, "stdout"); got != c.quiero {
				t.Errorf("Nivel = %q, quiero %q", got, c.quiero)
			}
		})
	}
}
