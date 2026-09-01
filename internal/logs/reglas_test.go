package logs

import "testing"

// La regla de conflictos es "gana la última que matchea", por orden de id. Es
// arbitraria pero predecible, y sin test se convierte en "depende del orden en
// que las devolvió el SELECT".
func TestReglasAplicar(t *testing.T) {
	const kong = `172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "curl/8.7.1"`

	casos := []struct {
		nombre string
		reglas Reglas
		quiero Nivel
	}{
		{"sin reglas devuelve lo que le pasaron", nil, Warn},
		{
			"una que matchea baja el nivel",
			Reglas{{Patron: `"GET /auth/v1/health HTTP/1.1" 401`, Nivel: Trace}},
			Trace,
		},
		{
			"container vacío es todos los containers",
			Reglas{{Patron: "401", Container: "", Nivel: Trace}},
			Trace,
		},
		{
			"una regla de otro container no toca esta línea",
			Reglas{{Patron: "401", Container: "otro-kong", Nivel: Trace}},
			Warn,
		},
		{
			"dos que se pisan: gana la última",
			Reglas{
				{Patron: "401", Nivel: Trace},
				{Patron: "401", Nivel: Error},
			},
			Error,
		},
		{
			"la que no matchea no le gana a la que sí",
			Reglas{
				{Patron: "401", Nivel: Trace},
				{Patron: "no-esta-en-la-linea", Nivel: Error},
			},
			Trace,
		},
		{
			// strings.Contains es case-sensitive y el LIKE de SQL no. Si esto
			// se relaja, el número del preview deja de ser el que queda.
			"el patrón es case-sensitive",
			Reglas{{Patron: "GET /AUTH/V1/HEALTH", Nivel: Trace}},
			Warn,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := c.reglas.Aplicar(Warn, kong, "supabase-kong"); got != c.quiero {
				t.Errorf("Aplicar = %q, quiero %q", got, c.quiero)
			}
		})
	}
}

// Nivelar es la ÚNICA composición de clasificador y reglas: la usan la ingesta,
// el backfill y el borrado de una regla. Si cada una compusiera por su cuenta,
// el nivel guardado dependería de por dónde entró la línea.
func TestNivelarComponeClasificarYReglas(t *testing.T) {
	const kong = `172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "curl/8.7.1"`

	if got := Nivelar(nil, kong, "stdout", "supabase-kong"); got != Warn {
		t.Fatalf("sin reglas Nivelar = %q, quiero WARN (lo que dice Clasificar)", got)
	}
	rs := Reglas{{Patron: `401 96 "-" "curl/8.7.1"`, Nivel: Trace}}
	if got := Nivelar(rs, kong, "stdout", "supabase-kong"); got != Trace {
		t.Errorf("con la regla puesta Nivelar = %q, quiero TRACE", got)
	}
}

// Las dos primeras líneas son reales: la de Kong es la misma que ya usa
// nivel_test.go, y la de Postgres tiene el formato exacto que loguean los dos
// Supabase. El acierto medido de la primera fue 8625 de 8625.
func TestPatronSugerido(t *testing.T) {
	casos := []struct {
		nombre, linea, quiero string
	}{
		{
			"acceso de Kong: se van la IP y el corchete de fecha",
			`172.19.0.2 - - [31/Aug/2026:19:50:30 +0000] "GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
			`"GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"`,
		},
		{
			// El pid entre corchetes SOBREVIVE, y está bien que se vea: el
			// campo es editable y el conteo previo dice enseguida que un
			// patrón con pid matchea muy poco.
			"Postgres: se va el timestamp con su zona",
			` 2026-08-22 07:38:00.012 UTC [38] LOG:  cron job 1 completed: 0 rows`,
			`[38] LOG:  cron job 1 completed: 0 rows`,
		},
		{
			"ISO-8601 con T y Z al principio",
			`2026-08-31T19:50:30.123Z Schema cache loaded 13 Relations, 24 Relationships`,
			`Schema cache loaded 13 Relations, 24 Relationships`,
		},
		{
			// Sin esto, "el proceso arrancó bien" quedaría en "arrancó bien":
			// tres palabras comidas de una frase común.
			"una línea en prosa vuelve entera",
			`el proceso arrancó bien y no hay nada que sacarle`,
			`el proceso arrancó bien y no hay nada que sacarle`,
		},
		{"una línea vacía no explota", "", ""},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := PatronSugerido(c.linea); got != c.quiero {
				t.Errorf("PatronSugerido = %q\nquiero          %q", got, c.quiero)
			}
		})
	}
}
