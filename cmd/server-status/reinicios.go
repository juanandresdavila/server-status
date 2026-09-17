package main

import (
	"log/slog"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/rules"
	"github.com/juanandresdavila/server-status/internal/store"
)

// guardarContainersYDetectar guarda la foto de containers del minuto y
// devuelve los hechos puntuales del tick: reinicio del host y de containers.
//
// Vive afuera del ciclo de correr() para que la base contra la que se compara
// se pueda testear pasando por SQLite, que es donde se escondieron el bug del
// 22/08 (los nanosegundos) y el del 17/09 (el cero del Inspect fallido).
//
// Un error de lectura o de escritura se loguea y la detección sigue con lo que
// haya: es un monitor, y callarse entero por una consulta es lo peor que puede
// hacer.
func guardarContainersYDetectar(s *store.Store, hostAntes, host model.HostSample,
	ms []model.ContainerSample, ahora time.Time) []model.Evento {

	// La base se lee ANTES de insertar, o la foto nueva termina comparada
	// contra sí misma.
	antes, err := s.UltimoEstadoContainers()
	if err != nil {
		slog.Error("no se pudo leer el estado anterior de los containers", "err", err)
	}
	if err := s.InsertContainerSamples(ms); err != nil {
		slog.Error("no se pudieron guardar los containers", "err", err)
	}
	return rules.DetectarEventos(hostAntes, host, antes, ms, ahora)
}
