// Package clock aísla la lectura del tiempo para que se pueda simular en tests.
// Invariante 5 del spec: nadie llama a time.Now() fuera de este paquete.
package clock

import "time"

type Clock interface {
	Now() time.Time
}

// Real es el reloj del sistema.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Fake es un reloj que solo avanza cuando se lo pide.
type Fake struct{ t time.Time }

func NewFake(t time.Time) *Fake { return &Fake{t: t} }

func (f *Fake) Now() time.Time { return f.t }

func (f *Fake) Advance(d time.Duration) { f.t = f.t.Add(d) }
