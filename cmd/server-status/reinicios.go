package main

import (
	"log/slog"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/rules"
	"github.com/juanandresdavila/server-status/internal/store"
)

// ventanaReinicios es hasta dónde para atrás se busca el último arranque
// conocido de un container, contada desde el último tick guardado.
//
// 60 minutos, decidido por Juan el 17/09/2026. La recreación dura segundos y
// cualquier ventana la cubre; lo que decide es cuánto cuesta equivocarse.
// Quedarse corto es un aviso perdido, que es justo el bug que esto arregla:
// con 10 minutos, un stack bajado 15 y vuelto a subir no avisaba nada.
// Pasarse cuesta como mucho un «arrancó de nuevo» para un container nuevo que
// reusa el nombre de uno viejo.
const ventanaReinicios = time.Hour

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
	// contra sí misma. Y no es la foto del minuto anterior sino el último
	// arranque conocido: si el tick anterior cayó en una recreación, su foto
	// tiene started_at en cero y el reinicio se pierde (17/09/2026).
	antes, err := s.UltimoArranqueConocido(ventanaReinicios)
	if err != nil {
		slog.Error("no se pudo leer el último arranque conocido de los containers", "err", err)
	}
	if err := s.InsertContainerSamples(ms); err != nil {
		slog.Error("no se pudieron guardar los containers", "err", err)
	}
	return rules.DetectarEventos(hostAntes, host, antes, ms, ahora)
}
