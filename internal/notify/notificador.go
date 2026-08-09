package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/model"
)

// VentanaDeVigencia es cuánto puede tener un aviso antes de darse por vencido.
// Si el proceso estuvo caído medio día, al volver no puede vomitar los avisos
// de la mañana: ya no le sirven a nadie y taparían lo que está pasando ahora.
const VentanaDeVigencia = time.Hour

// Store es lo único que el notificador necesita de la persistencia. La cola
// de pendientes la lee el loop, no él: con esta interfaz de un método el
// notificador se testea con un doble de tres líneas.
type Store interface {
	MarcarEnviado(deliveryID string, cuando time.Time, via, errMsg string) error
}

type Notificador struct {
	principal Canal
	respaldo  Canal
	store     Store
	clk       clock.Clock
}

func NewNotificador(principal, respaldo Canal, s Store, clk clock.Clock) *Notificador {
	return &Notificador{principal: principal, respaldo: respaldo, store: s, clk: clk}
}

// Avisar manda el aviso de un incidente. Descarta los vencidos y delega el
// resto en AvisarTexto.
func (n *Notificador) Avisar(ctx context.Context, a model.Aviso) error {
	ahora := n.clk.Now()

	if ahora.Sub(momentoDe(a)) > VentanaDeVigencia {
		slog.Warn("aviso vencido, no se manda", "delivery", a.DeliveryID)
		return n.store.MarcarEnviado(a.DeliveryID, ahora, "vencido",
			"más viejo que la ventana de vigencia")
	}
	return n.AvisarTexto(ctx, a.DeliveryID, Texto(a))
}

// AvisarTexto manda un mensaje suelto. Lo usa el resumen diario, que no tiene
// incidente detrás.
//
// No marcar nada cuando fallan los dos canales es deliberado: así el aviso
// sigue pendiente y el tick siguiente lo reintenta.
func (n *Notificador) AvisarTexto(ctx context.Context, deliveryID, texto string) error {
	ahora := n.clk.Now()
	var fallas []error

	for _, c := range []Canal{n.principal, n.respaldo} {
		if c == nil || !c.Configurado() {
			continue
		}
		if err := n.mandar(ctx, c, deliveryID, texto); err != nil {
			slog.Warn("no se pudo avisar", "canal", c.Nombre(), "err", err)
			fallas = append(fallas, fmt.Errorf("%s: %w", c.Nombre(), err))
			continue
		}
		return n.store.MarcarEnviado(deliveryID, ahora, c.Nombre(), "")
	}

	if len(fallas) == 0 {
		// Ningún canal configurado. Se registra para no reintentar para
		// siempre, pero se grita en el log.
		slog.Error("no hay ningún canal de avisos configurado", "delivery", deliveryID)
		return n.store.MarcarEnviado(deliveryID, ahora, "sin-canal", "ningún canal configurado")
	}
	return errors.Join(fallas...)
}

// mandar usa MandarCon si el canal lo soporta, para pasarle la clave de
// idempotencia. Es lo que hace que un reintento no duplique del otro lado.
func (n *Notificador) mandar(ctx context.Context, c Canal, deliveryID, texto string) error {
	if idem, ok := c.(interface {
		MandarCon(context.Context, string, string) error
	}); ok {
		return idem.MandarCon(ctx, texto, deliveryID)
	}
	return c.Mandar(ctx, texto)
}

// momentoDe es contra qué se mide la vigencia. Para un cierre es el cierre y
// no la apertura: un incidente que duró tres horas y se acaba de resolver
// tiene que avisarse igual.
func momentoDe(a model.Aviso) time.Time {
	if a.Cierre && a.Incidente.CerradoEn != nil {
		return *a.Incidente.CerradoEn
	}
	return a.Incidente.AbiertoEn
}
