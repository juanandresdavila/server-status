package logs

import "strings"

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
