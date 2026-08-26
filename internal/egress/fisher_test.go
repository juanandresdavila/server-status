package egress_test

import (
	"math"
	"testing"

	"github.com/juanandresdavila/server-status/internal/egress"
)

// Los valores de referencia salen de tablas conocidas, no de esta
// implementación: un test contra su propia salida no verifica nada.
func TestFisherExactoContraValoresConocidos(t *testing.T) {
	casos := []struct {
		nombre     string
		a, b, c, d int
		quiero     float64
	}{
		// El experimento de la catadora de té de Fisher.
		{"catadora de té", 3, 1, 1, 3, 0.4857142857142857},
		// Separación total con márgenes 5/5: p = 2 / C(10,5) = 2/252.
		{"separación total 5×5", 0, 5, 5, 0, 2.0 / 252.0},
		{"separación total al revés", 5, 0, 0, 5, 2.0 / 252.0},
		// Sin efecto ninguno.
		{"tabla simétrica", 5, 5, 5, 5, 1},
		// Márgenes degenerados: no hay nada que comparar.
		{"una fila vacía", 0, 0, 4, 4, 1},
		{"una columna vacía", 0, 4, 0, 4, 1},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got := egress.FisherExacto(c.a, c.b, c.c, c.d)
			if math.Abs(got-c.quiero) > 1e-12 {
				t.Errorf("FisherExacto(%d,%d,%d,%d) = %.15f, quería %.15f",
					c.a, c.b, c.c, c.d, got, c.quiero)
			}
		})
	}
}

// La forma que va a tener el experimento de verdad: ~5.000 intentos por brazo,
// unas decenas de fallas de un lado y cero del otro. Tiene que cruzar el umbral
// pre-registrado de 0,01 con holgura, y no desbordar por los factoriales.
func TestFisherExactoConLaFormaDelExperimento(t *testing.T) {
	p := egress.FisherExacto(20, 4980, 0, 5000)
	if math.IsNaN(p) || math.IsInf(p, 0) {
		t.Fatalf("p = %v: los factoriales desbordaron", p)
	}
	if p >= 0.01 {
		t.Errorf("p = %g, quería < 0,01: 20 fallas contra 0 tiene que separar", p)
	}
}

// Y el caso contrario, que es el que protege de concluir de más: una diferencia
// chica sobre la misma cantidad de intentos NO tiene que cruzar el umbral.
func TestFisherExactoNoSeparaUnaDiferenciaChica(t *testing.T) {
	p := egress.FisherExacto(3, 4997, 1, 4999)
	if p < 0.01 {
		t.Errorf("p = %g, quería ≥ 0,01: 3 contra 1 no alcanza para concluir", p)
	}
}

func TestFisherExactoEsSimetricoAlTrasponer(t *testing.T) {
	directo := egress.FisherExacto(12, 300, 2, 310)
	traspuesto := egress.FisherExacto(12, 2, 300, 310)
	if math.Abs(directo-traspuesto) > 1e-12 {
		t.Errorf("directo = %g, traspuesto = %g", directo, traspuesto)
	}
}
