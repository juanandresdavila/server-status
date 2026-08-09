// Package rules decide cuándo algo pasa de sano a caído y al revés.
//
// Las políticas son funciones puras sobre un estado explícito: es lo único del
// sistema que puede mandar un mensaje a las 3 de la mañana, así que tiene que
// poder testearse sin base y sin red.
package rules

import "time"

type Transicion int

const (
	SinCambio Transicion = iota
	Abre
	Cierra
)

func (t Transicion) String() string {
	switch t {
	case Abre:
		return "Abre"
	case Cierra:
		return "Cierra"
	default:
		return "SinCambio"
	}
}

// Contador lleva las rachas de un sujeto.
type Contador struct {
	Fallas int
	Exitos int
}

// PorConteo se usa con los sujetos que se prueban: servicios y containers.
type PorConteo struct {
	FallasParaAbrir  int
	ExitosParaCerrar int
}

// Aplicar consume un resultado y devuelve el contador nuevo más la transición.
// `abierto` dice si el sujeto ya tiene un incidente abierto.
func (p PorConteo) Aplicar(c Contador, ok bool, abierto bool) (Contador, Transicion) {
	if ok {
		// Una racha se corta con un solo resultado del otro signo:
		// son fallas SEGUIDAS, no fallas acumuladas.
		c.Fallas = 0
		c.Exitos++
		if abierto && c.Exitos >= p.ExitosParaCerrar {
			return Contador{}, Cierra
		}
		return c, SinCambio
	}

	c.Exitos = 0
	c.Fallas++
	if !abierto && c.Fallas >= p.FallasParaAbrir {
		return Contador{}, Abre
	}
	return c, SinCambio
}

// PorUmbral se usa con las métricas del host, donde "más alto es peor".
// Abre y Cierra son distintos a propósito: sin esa histéresis, un valor
// parado en el borde abre y cierra sin parar.
type PorUmbral struct {
	Abre      float64
	Cierra    float64
	Sostenido time.Duration
}

// EstadoUmbral recuerda desde cuándo el valor está por encima del umbral.
// En cero significa que no está cruzado.
type EstadoUmbral struct {
	DesdeCuando time.Time
}

func (p PorUmbral) Aplicar(e EstadoUmbral, valor float64, ahora time.Time, abierto bool) (EstadoUmbral, Transicion) {
	if abierto {
		if valor <= p.Cierra {
			return EstadoUmbral{}, Cierra
		}
		return e, SinCambio
	}

	if valor < p.Abre {
		// Volvió a la normalidad: se descarta el cronómetro para que dos
		// picos separados en el tiempo no se sumen.
		return EstadoUmbral{}, SinCambio
	}
	// El cronómetro arranca en el primer cruce y se evalúa en el mismo paso:
	// con Sostenido en 0 eso abre de una, que es lo que "cero" tiene que
	// significar. Anotar y salir obligaba a una segunda observación siempre,
	// y para una ráfaga de errores —que no se sostiene, pasa— eso era perderla.
	if e.DesdeCuando.IsZero() {
		e.DesdeCuando = ahora
	}
	if ahora.Sub(e.DesdeCuando) >= p.Sostenido {
		return EstadoUmbral{}, Abre
	}
	return e, SinCambio
}
