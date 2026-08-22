package rules

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
)

// Los eventos son la contraparte de los incidentes: hechos PUNTUALES.
//
// El motor de reglas de este archivo vecino solo sabe de estados sostenidos —3
// muestras malas seguidas, o un umbral aguantado 10 minutos— y eso deja un
// agujero entero: un reinicio dura segundos. El del host del 22/08/2026 duró 18
// y no lo vio nadie, porque mientras la máquina está abajo el proceso está
// muerto y no puede observar su propia ausencia.
//
// La única forma de detectarlo es al volver, comparando contra lo último que sí
// se llegó a guardar. Por eso estas funciones son puras y toman el "antes" y el
// "después" explícitos: se testean enteras sin base y sin Docker.

// DetectarReinicioHost devuelve un evento si la máquina se reinició entre las
// dos muestras. Nil si no.
//
// La señal es el uptime que BAJA. Un `make deploy` reinicia el proceso pero no
// la máquina, así que el uptime sigue subiendo y no dispara nada: si disparara,
// cada deploy sería un falso positivo y en dos semanas nadie miraría el aviso.
func DetectarReinicioHost(anterior, actual model.HostSample) *model.Evento {
	// Sin muestra anterior no hay con qué comparar. Inventar un reinicio en el
	// primer arranque sería avisar de algo que no pasó.
	if anterior.TS.IsZero() || anterior.Uptime == 0 {
		return nil
	}
	if actual.Uptime >= anterior.Uptime {
		return nil
	}

	arranque := actual.TS.Add(-actual.Uptime)

	// Cuánto tiempo estuvo sin observarse. No es exactamente el corte —la
	// máquina pudo bajar en cualquier momento después de la última muestra—
	// así que se reporta como lo que es: un hueco sin datos.
	hueco := arranque.Sub(anterior.TS)
	if hueco < 0 {
		hueco = 0
	}

	return &model.Evento{
		Tipo:       "reboot",
		Sujeto:     "host",
		Severidad:  "critical",
		OcurridoEn: arranque,
		Detalle: fmt.Sprintf("la máquina se reinició: arrancó %s, sin datos durante %s",
			arranque.Local().Format("02/01 15:04:05"), redondear(hueco)),
	}
}

// DetectarReinicioContainers compara el StartedAt de cada container y devuelve
// UN evento con todos los que arrancaron de nuevo. Nil si ninguno.
//
// Uno solo y no uno por container: un reboot levanta los 21 a la vez, y 21
// mensajes por un solo hecho es la forma más rápida de que el bot se silencie.
func DetectarReinicioContainers(antes, despues []model.ContainerSample, ahora time.Time) *model.Evento {
	previo := make(map[string]time.Time, len(antes))
	for _, c := range antes {
		previo[c.Name] = c.StartedAt
	}

	var reiniciados []string
	for _, c := range despues {
		anterior, estaba := previo[c.Name]
		switch {
		// Un container que aparece por primera vez no se reinició: es nuevo.
		case !estaba:
			continue
		// started_at en cero es "no se sabe" —las filas anteriores a la
		// migración 10 no lo tienen—. Leerlo como un arranque en 1970 haría
		// que TODO container pareciera recién reiniciado en el primer tick
		// después del deploy.
		case anterior.IsZero() || c.StartedAt.IsZero():
			continue
		case c.StartedAt.After(anterior):
			reiniciados = append(reiniciados, c.Name)
		}
	}
	if len(reiniciados) == 0 {
		return nil
	}
	sort.Strings(reiniciados)

	return &model.Evento{
		Tipo:       "container_restart",
		Sujeto:     "containers",
		Severidad:  "warning",
		OcurridoEn: ahora,
		Detalle: fmt.Sprintf("%s: %s",
			plural(len(reiniciados), "container arrancó de nuevo", "containers arrancaron de nuevo"),
			listar(reiniciados, 8)),
	}
}

// DetectarEventos junta las dos detecciones y decide qué merece mensaje propio.
//
// Si el host se reinició, que los containers hayan vuelto NO es noticia aparte:
// es la consecuencia obvia, y se pliega adentro del reboot.
func DetectarEventos(hostAntes, hostAhora model.HostSample,
	antes, despues []model.ContainerSample, ahora time.Time) []model.Evento {

	reboot := DetectarReinicioHost(hostAntes, hostAhora)
	containers := DetectarReinicioContainers(antes, despues, ahora)

	if reboot != nil {
		if containers != nil {
			reboot.Detalle += ". " + containers.Detalle
		}
		return []model.Evento{*reboot}
	}
	if containers != nil {
		return []model.Evento{*containers}
	}
	return nil
}

// listar nombra hasta n y resume el resto: el detalle va a un mensaje de
// Telegram, y veintiún nombres no se leen.
func listar(nombres []string, n int) string {
	if len(nombres) <= n {
		return strings.Join(nombres, ", ")
	}
	return strings.Join(nombres[:n], ", ") + fmt.Sprintf(" y %d más", len(nombres)-n)
}

func plural(n int, uno, varios string) string {
	if n == 1 {
		return "1 " + uno
	}
	return fmt.Sprintf("%d %s", n, varios)
}

// redondear deja una duración legible: los nanosegundos de un uptime no le
// sirven a nadie a las tres de la mañana.
func redondear(d time.Duration) time.Duration {
	if d >= time.Hour {
		return d.Round(time.Minute)
	}
	return d.Round(time.Second)
}
