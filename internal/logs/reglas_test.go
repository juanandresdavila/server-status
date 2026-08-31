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
