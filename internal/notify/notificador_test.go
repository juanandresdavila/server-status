package notify_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/notify"
)

type canalFalso struct {
	nombre      string
	configurado bool
	falla       error
	mandados    []string
	claves      []string
}

func (c *canalFalso) Nombre() string    { return c.nombre }
func (c *canalFalso) Configurado() bool { return c.configurado }

func (c *canalFalso) Mandar(_ context.Context, texto string) error {
	if c.falla != nil {
		return c.falla
	}
	c.mandados = append(c.mandados, texto)
	return nil
}

// canalIdempotente agrega MandarCon, que el notificador detecta por type
// assertion para pasarle la clave de idempotencia.
type canalIdempotente struct{ canalFalso }

func (c *canalIdempotente) MandarCon(_ context.Context, texto, clave string) error {
	if c.falla != nil {
		return c.falla
	}
	c.mandados = append(c.mandados, texto)
	c.claves = append(c.claves, clave)
	return nil
}

type storeFalso struct {
	marcados map[string]string // deliveryID → via
}

func nuevoStore() *storeFalso { return &storeFalso{marcados: map[string]string{}} }

func (s *storeFalso) MarcarEnviado(id string, _ time.Time, via, _ string) error {
	s.marcados[id] = via
	return nil
}

func (s *storeFalso) YaEnviado(id string) (bool, error) {
	_, ok := s.marcados[id]
	return ok, nil
}

const abiertoEn = "2026-08-09T11:00:00Z"

func aviso(id int64, cierre bool) model.Aviso {
	sufijo := ":opened"
	if cierre {
		sufijo = ":closed"
	}
	abierto, _ := time.Parse(time.RFC3339, abiertoEn)
	inc := model.Incidente{
		ID: id, Sujeto: "service:comm-tool", Tipo: "down",
		Severidad: "critical", AbiertoEn: abierto, Detalle: "HTTP 502",
	}
	if cierre {
		cerrado := abierto.Add(4 * time.Minute)
		inc.CerradoEn = &cerrado
	}
	return model.Aviso{
		DeliveryID: strconv.FormatInt(id, 10) + sufijo,
		Incidente:  inc,
		Cierre:     cierre,
	}
}

func relojEn(hhmm string) *clock.Fake {
	t, _ := time.Parse(time.RFC3339, hhmm)
	return clock.NewFake(t)
}

func TestUsaElPrincipalCuandoAnda(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true}
	respaldo := &canalFalso{nombre: "telegram", configurado: true}
	st := nuevoStore()

	n := notify.NewNotificador(principal, respaldo, st, relojEn("2026-08-09T11:01:00Z"))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(principal.mandados) != 1 {
		t.Errorf("el principal recibió %d mensajes", len(principal.mandados))
	}
	if len(respaldo.mandados) != 0 {
		t.Error("el respaldo se usó sin necesidad")
	}
	if st.marcados["1:opened"] != "commtool" {
		t.Errorf("marcados = %v", st.marcados)
	}
}

// Este es el caso que justifica todo el respaldo: comm-tool caído es el aviso
// más importante que hay, y no puede llegar por comm-tool.
func TestCaeAlRespaldoSiElPrincipalFalla(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true, falla: errors.New("connection refused")}
	respaldo := &canalFalso{nombre: "telegram", configurado: true}
	st := nuevoStore()

	n := notify.NewNotificador(principal, respaldo, st, relojEn("2026-08-09T11:01:00Z"))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(respaldo.mandados) != 1 {
		t.Fatalf("el respaldo recibió %d mensajes, quería 1", len(respaldo.mandados))
	}
	if st.marcados["1:opened"] != "telegram" {
		t.Errorf("via = %q, quería telegram", st.marcados["1:opened"])
	}
}

// Si fallan los dos NO se marca nada: el aviso queda pendiente y el tick
// siguiente lo reintenta. Marcarlo sería perderlo para siempre.
func TestSiFallanLosDosNoSeMarca(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true, falla: errors.New("x")}
	respaldo := &canalFalso{nombre: "telegram", configurado: true, falla: errors.New("y")}
	st := nuevoStore()

	n := notify.NewNotificador(principal, respaldo, st, relojEn("2026-08-09T11:01:00Z"))

	if err := n.Avisar(context.Background(), aviso(1, false)); err == nil {
		t.Fatal("quería error cuando fallan los dos canales")
	}
	if len(st.marcados) != 0 {
		t.Errorf("se marcó algo con los dos canales caídos: %v", st.marcados)
	}
}

// Un canal sin credenciales no se intenta: fallaría siempre y llenaría el log.
func TestCanalSinConfigurarSeSaltea(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: false}
	respaldo := &canalFalso{nombre: "telegram", configurado: true}
	st := nuevoStore()

	n := notify.NewNotificador(principal, respaldo, st, relojEn("2026-08-09T11:01:00Z"))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(principal.mandados) != 0 {
		t.Error("se intentó mandar por un canal sin configurar")
	}
	if st.marcados["1:opened"] != "telegram" {
		t.Errorf("via = %q", st.marcados["1:opened"])
	}
}

// Un aviso viejo no se manda: si el proceso estuvo caído medio día, al volver
// no puede vomitar los avisos de la mañana. Se marca vencido para que no se
// reintente para siempre.
func TestAvisoVencidoNoSeMandaPeroSeMarca(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true}
	respaldo := &canalFalso{nombre: "telegram", configurado: true}
	st := nuevoStore()

	// El incidente es de las 11:00 y el reloj marca 6 horas después.
	n := notify.NewNotificador(principal, respaldo, st, relojEn("2026-08-09T17:00:00Z"))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(principal.mandados) != 0 || len(respaldo.mandados) != 0 {
		t.Error("se mandó un aviso de hace 6 horas")
	}
	if st.marcados["1:opened"] != "vencido" {
		t.Errorf("via = %q, quería vencido", st.marcados["1:opened"])
	}
}

// La vigencia de un aviso de cierre se mide contra el cierre, no contra la
// apertura: un incidente que duró tres horas y se acaba de resolver tiene que
// avisarse igual.
func TestElCierreSeMideContraElCierre(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true}
	st := nuevoStore()

	a := aviso(1, true)
	// Abierto a las 11:00, cerrado a las 14:00, y son las 14:01.
	cerrado, _ := time.Parse(time.RFC3339, "2026-08-09T14:00:00Z")
	a.Incidente.CerradoEn = &cerrado

	n := notify.NewNotificador(principal, nil, st, relojEn("2026-08-09T14:01:00Z"))

	if err := n.Avisar(context.Background(), a); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if len(principal.mandados) != 1 {
		t.Error("no se mandó el cierre de un incidente largo recién resuelto")
	}
}

// La clave de idempotencia tiene que llegar al canal que la soporta.
func TestLePasaLaClaveDeIdempotenciaAlCanalQueLaSoporta(t *testing.T) {
	principal := &canalIdempotente{canalFalso{nombre: "commtool", configurado: true}}
	st := nuevoStore()

	n := notify.NewNotificador(principal, nil, st, relojEn("2026-08-09T11:01:00Z"))
	if err := n.Avisar(context.Background(), aviso(7, false)); err != nil {
		t.Fatal(err)
	}

	if len(principal.claves) != 1 || principal.claves[0] != "7:opened" {
		t.Errorf("claves = %v, quería [7:opened]", principal.claves)
	}
}

// Sin ningún canal configurado se registra y se sigue: el servicio no puede
// quedarse reintentando para siempre algo que no tiene por dónde salir.
func TestSinNingunCanalSeRegistraYSigue(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: false}
	respaldo := &canalFalso{nombre: "telegram", configurado: false}
	st := nuevoStore()

	n := notify.NewNotificador(principal, respaldo, st, relojEn("2026-08-09T11:01:00Z"))

	if err := n.Avisar(context.Background(), aviso(1, false)); err != nil {
		t.Fatalf("Avisar: %v", err)
	}
	if st.marcados["1:opened"] != "sin-canal" {
		t.Errorf("via = %q, quería sin-canal", st.marcados["1:opened"])
	}
}

// Reiniciar el servicio no puede remandar el resumen del día: su delivery id
// es la fecha, así que sin este chequeo cinco reinicios son cinco resúmenes.
// comm-tool lo taparía con la idempotencyKey; el camino directo no.
func TestNoRemandaLoQueYaSalio(t *testing.T) {
	principal := &canalFalso{nombre: "commtool", configurado: true}
	st := nuevoStore()
	st.marcados["resumen:2026-08-09"] = "commtool"

	n := notify.NewNotificador(principal, nil, st, relojEn("2026-08-09T11:01:00Z"))

	if err := n.AvisarTexto(context.Background(), "resumen:2026-08-09", "hola"); err != nil {
		t.Fatalf("AvisarTexto: %v", err)
	}
	if len(principal.mandados) != 0 {
		t.Errorf("remandó un aviso ya entregado: %v", principal.mandados)
	}
}
