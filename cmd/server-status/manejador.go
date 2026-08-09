package main

import (
	"context"
	"log/slog"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/comandos"
	"github.com/juanandresdavila/server-status/internal/notify"
	"github.com/juanandresdavila/server-status/internal/store"
)

// manejadorDeComandos une las tres piezas: deduplicar, ejecutar y responder.
type manejadorDeComandos struct {
	store       *store.Store
	notificador *notify.Notificador
}

// Procesar cumple web.Manejador.
//
// Devolver nil aunque no haya nada que responder es a propósito: un mensaje
// que no es comando se ignora, pero la ENTREGA fue exitosa. Un error haría
// que comm-tool reintente cinco veces por cada "hola".
func (m *manejadorDeComandos) Procesar(deliveryID, texto string) (string, error) {
	c, esComando := comandos.Parsear(texto)
	if !esComando {
		return "", nil
	}

	ahora := clock.Real{}.Now()

	// El dedupe va ANTES de ejecutar: comm-tool reintenta hasta 5 veces y sin
	// esto un "/silenciar 2h" se aplicaría cinco veces.
	nuevo, err := m.store.MarcarComandoProcesado(deliveryID, ahora)
	if err != nil {
		return "", err
	}
	if !nuevo {
		slog.Info("comando ya procesado, no se repite", "delivery", deliveryID)
		return "", nil
	}

	respuesta, err := comandos.Ejecutar(m.store, c, ahora)
	if err != nil {
		return "", err
	}
	if respuesta == "" {
		return "", nil
	}

	// La respuesta sale por el mismo camino que los avisos. El id de entrega
	// lleva el prefijo del comando para no chocar con los de incidentes.
	if err := m.notificador.AvisarTexto(
		context.Background(), "cmd:"+deliveryID, respuesta); err != nil {
		slog.Error("no se pudo responder el comando", "delivery", deliveryID, "err", err)
		return "", err
	}
	return respuesta, nil
}

// aplicarSilencio le pasa al notificador el silencio guardado en la base.
// Se llama en cada tick porque /silenciar lo cambia entre medio.
func aplicarSilencio(s *store.Store, n *notify.Notificador) {
	hasta, err := s.SilenciadoHasta()
	if err != nil {
		slog.Error("no se pudo leer el silencio", "err", err)
		return
	}
	n.SilenciarHasta(hasta)
}
