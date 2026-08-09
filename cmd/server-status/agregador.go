package main

import "github.com/juanandresdavila/server-status/internal/model"

// agregador junta las muestras de 15 segundos y emite una por minuto.
// Guarda promedio y máximo de CPU porque un pico corto se pierde en el promedio.
type agregador struct {
	ultima model.HostSample
	suma   float64
	max    float64
	n      int
}

func (a *agregador) add(m model.HostSample) {
	a.ultima = m
	a.suma += m.CPUPctAvg
	if m.CPUPctAvg > a.max {
		a.max = m.CPUPctAvg
	}
	a.n++
}

// flush devuelve la muestra del minuto y se limpia. ok es false si no hubo nada.
func (a *agregador) flush() (model.HostSample, bool) {
	if a.n == 0 {
		return model.HostSample{}, false
	}
	m := a.ultima
	m.CPUPctAvg = a.suma / float64(a.n)
	m.CPUPctMax = a.max
	*a = agregador{}
	return m, true
}
