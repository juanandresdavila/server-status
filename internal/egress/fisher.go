package egress

import "math"

// FisherExacto devuelve el p de DOS COLAS de la tabla 2×2
//
//	a  b
//	c  d
//
// con márgenes fijos. Es el test de la regla pre-registrada: dos brazos se
// declaran distintos con ≥10 eventos en el brazo alto y p < 0,01.
//
// Va exacto y no chi-cuadrado porque el caso que interesa tiene celdas en cero
// —"v4 limpio"— y ahí la aproximación asintótica no vale. Dos colas y no una
// aunque la hipótesis tenga dirección: es el criterio más conservador, y acá
// equivocarse hacia "concluí de más" es peor que hacia "no concluye".
func FisherExacto(a, b, c, d int) float64 {
	if a < 0 || b < 0 || c < 0 || d < 0 {
		return math.NaN()
	}
	fila1, fila2 := a+b, c+d
	col1, col2 := a+c, b+d
	n := fila1 + fila2
	if n == 0 || fila1 == 0 || fila2 == 0 || col1 == 0 || col2 == 0 {
		return 1
	}

	// Tolerancia relativa: las probabilidades se comparan después de pasar por
	// exp(), y sin holgura una tabla simétrica de la misma probabilidad que la
	// observada queda afuera de la suma por un error de redondeo.
	const holgura = 1 + 1e-9
	obs := math.Exp(lnHipergeometrica(a, b, c, d))

	desde := max(0, col1-fila2)
	hasta := min(fila1, col1)

	var p float64
	for x := desde; x <= hasta; x++ {
		q := math.Exp(lnHipergeometrica(x, fila1-x, col1-x, fila2-col1+x))
		if q <= obs*holgura {
			p += q
		}
	}
	return math.Min(p, 1)
}

// lnHipergeometrica es el log de la probabilidad de una tabla con esos
// márgenes. Va en logaritmos porque con 5.000 intentos por brazo los
// factoriales desbordan un float64 mucho antes de que se cancelen.
func lnHipergeometrica(a, b, c, d int) float64 {
	return lnFactorial(a+b) + lnFactorial(c+d) + lnFactorial(a+c) + lnFactorial(b+d) -
		lnFactorial(a+b+c+d) - lnFactorial(a) - lnFactorial(b) - lnFactorial(c) - lnFactorial(d)
}

func lnFactorial(n int) float64 {
	v, _ := math.Lgamma(float64(n) + 1)
	return v
}
