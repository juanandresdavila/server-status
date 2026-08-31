package logs

import (
	"regexp"
	"strings"
)

// Regla baja o sube el nivel de las líneas que contienen un patrón.
//
// Vive acá y no adentro de Clasificar a propósito: Clasificar es una función
// pura, se testea sin base, sin red y sin reloj, y meterle una consulta la
// convertiría en otra cosa. Las reglas se COMPONEN encima, con Nivelar.
//
// Container vacío significa "todos los containers".
type Regla struct {
	Patron    string
	Container string
	Nivel     Nivel
}

// Coincide es la definición de "esta línea matchea", en Go.
//
// strings.Contains y nada más: substring, case-sensitive, sin regex. El
// equivalente en SQL es instr(linea, ?) > 0, en store.dondeCoincide, y hay un
// test que corre los dos caminos sobre el mismo corpus. Si divergen, el número
// que el usuario confirma en el preview no es el que le queda aplicado.
//
// LIKE queda descartado por eso mismo: es case-insensitive para ASCII y esto
// no. Regex también: invita a patrones catastróficos y abre entre Go y SQL un
// abismo de semántica que no se puede cerrar con un test.
func (r Regla) Coincide(linea, container string) bool {
	if r.Container != "" && r.Container != container {
		return false
	}
	return strings.Contains(linea, r.Patron)
}

// Reglas es el conjunto en el orden en que se aplican: el de la columna id.
type Reglas []Regla

// Aplicar devuelve el nivel que corresponde después de las reglas. Si ninguna
// matchea devuelve el que le pasaron.
//
// Gana la ÚLTIMA que matchea. Es arbitrario pero predecible, que es lo único
// que se le puede pedir a un desempate entre dos reglas que alguien escribió
// para la misma línea.
func (rs Reglas) Aplicar(n Nivel, linea, container string) Nivel {
	for _, r := range rs {
		if r.Coincide(linea, container) {
			n = r.Nivel
		}
	}
	return n
}

// Nivelar es la composición completa: lo que dice el clasificador, corregido
// por las reglas. Es la única forma en que se calcula el nivel guardado de una
// línea, la ingiera el tick, la reprocese el backfill o la re-nivele el
// borrado de una regla.
func Nivelar(rs Reglas, linea, stream, container string) Nivel {
	return rs.Aplicar(Clasificar(linea, stream), linea, container)
}

var (
	// El "172.19.0.2 - - " del formato de acceso común. Se pide que lo siga el
	// corchete de la fecha, y por eso el corchete se captura y se devuelve: sin
	// esa condición, la regla se comería las tres primeras palabras de
	// cualquier frase en prosa.
	rePrefijoAcceso = regexp.MustCompile(`^\S+ \S+ \S+ (\[)`)

	// [31/Aug/2026:19:50:30 +0000]
	reFechaCorchete = regexp.MustCompile(`^\[\d{1,2}/[A-Za-z]{3}/\d{4}(?::\d{2}){3} [+-]\d{4}\]\s*`)

	// 2026-08-31T19:50:30.123Z, 2026-08-22 07:38:00.012 UTC, con o sin zona.
	reISOInicial = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?(?: [A-Z]{2,4})?\s*`)
)

// PatronSugerido saca de la línea la parte que se repite, tirando lo que la
// hace única. En este orden: la IP inicial del formato de acceso, el corchete
// de fecha, y un timestamp ISO-8601 al principio.
//
// Es un PUNTO DE PARTIDA EDITABLE, no una promesa. Que el adivinador falle no
// rompe nada: el campo se corrige a mano y el conteo previo dice si el patrón
// quedó demasiado ancho o demasiado angosto. Que el conteo mienta sí rompería
// algo, y por eso ahí no hay heurística ninguna.
func PatronSugerido(linea string) string {
	p := strings.TrimSpace(linea)
	p = rePrefijoAcceso.ReplaceAllString(p, "$1")
	p = reFechaCorchete.ReplaceAllString(p, "")
	p = reISOInicial.ReplaceAllString(p, "")
	return strings.TrimSpace(p)
}
