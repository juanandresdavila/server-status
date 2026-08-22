package rules_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/rules"
)

var ahora = time.Date(2026, 8, 22, 5, 1, 0, 0, time.UTC)

func muestra(ts time.Time, uptime time.Duration) model.HostSample {
	return model.HostSample{TS: ts, Uptime: uptime}
}

// El caso que motivó todo esto: el host se reinició el 22/08 a las 05:00:24 y
// no se enteró nadie. El uptime bajó de 13 días a 36 segundos.
func TestReinicioDelHostSeDetectaPorElUptimeQueBaja(t *testing.T) {
	anterior := muestra(ahora.Add(-time.Minute), 13*24*time.Hour)
	actual := muestra(ahora, 36*time.Second)

	ev := rules.DetectarReinicioHost(anterior, actual)
	if ev == nil {
		t.Fatal("no detectó el reinicio")
	}
	if ev.Tipo != "reboot" {
		t.Errorf("tipo = %q, quería reboot", ev.Tipo)
	}
	if ev.Severidad != "critical" {
		t.Errorf("severidad = %q, quería critical", ev.Severidad)
	}
	// El arranque se calcula, no se adivina: actual.TS - uptime.
	quiero := ahora.Add(-36 * time.Second)
	if !ev.OcurridoEn.Equal(quiero) {
		t.Errorf("ocurrido en %v, quería %v", ev.OcurridoEn, quiero)
	}
}

// Un `make deploy` reinicia el PROCESO, no la máquina: el uptime del host sigue
// subiendo. Si esto avisara, cada deploy sería un falso positivo y en dos
// semanas el aviso de reinicio no lo miraría nadie.
func TestDeployDelProcesoNoEsReinicioDelHost(t *testing.T) {
	anterior := muestra(ahora.Add(-time.Minute), 13*24*time.Hour)
	actual := muestra(ahora, 13*24*time.Hour+time.Minute)

	if ev := rules.DetectarReinicioHost(anterior, actual); ev != nil {
		t.Errorf("detectó un reinicio que no existió: %+v", ev)
	}
}

// Sin muestra anterior —base recién creada— no hay con qué comparar. Inventar
// un reinicio en el primer arranque sería avisar de algo que no pasó.
func TestSinMuestraAnteriorNoHayReinicio(t *testing.T) {
	if ev := rules.DetectarReinicioHost(model.HostSample{}, muestra(ahora, time.Minute)); ev != nil {
		t.Errorf("detectó un reinicio sin muestra anterior: %+v", ev)
	}
}

func cs(nombre string, arranco time.Time) model.ContainerSample {
	return model.ContainerSample{Name: nombre, State: "running", StartedAt: arranco}
}

func TestContainerReiniciadoSeDetectaPorStartedAt(t *testing.T) {
	viejo := ahora.Add(-10 * 24 * time.Hour)
	antes := []model.ContainerSample{cs("gym-bot", viejo), cs("caddy", viejo)}
	despues := []model.ContainerSample{cs("gym-bot", ahora.Add(-time.Minute)), cs("caddy", viejo)}

	ev := rules.DetectarReinicioContainers(antes, despues, ahora)
	if ev == nil {
		t.Fatal("no detectó el reinicio del container")
	}
	if ev.Tipo != "container_restart" {
		t.Errorf("tipo = %q, quería container_restart", ev.Tipo)
	}
	if !strings.Contains(ev.Detalle, "gym-bot") {
		t.Errorf("el detalle no nombra al container: %q", ev.Detalle)
	}
	if strings.Contains(ev.Detalle, "caddy") {
		t.Errorf("el detalle nombra un container que no se reinició: %q", ev.Detalle)
	}
}

// Un reboot levanta los 21 containers a la vez. Veintiún mensajes por un solo
// hecho es la forma más rápida de que se silencie el bot.
func TestVariosContainersVanEnUnSoloEvento(t *testing.T) {
	viejo := ahora.Add(-10 * 24 * time.Hour)
	var antes, despues []model.ContainerSample
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		antes = append(antes, cs(n, viejo))
		despues = append(despues, cs(n, ahora))
	}

	ev := rules.DetectarReinicioContainers(antes, despues, ahora)
	if ev == nil {
		t.Fatal("no detectó nada")
	}
	if !strings.Contains(ev.Detalle, "5") {
		t.Errorf("el detalle no dice cuántos fueron: %q", ev.Detalle)
	}
}

// Un container que aparece por primera vez no se "reinició": es nuevo. Avisar
// de eso convertiría cada `compose up` de un servicio nuevo en una alarma.
func TestContainerNuevoNoEsReinicio(t *testing.T) {
	antes := []model.ContainerSample{cs("viejo", ahora.Add(-time.Hour))}
	despues := []model.ContainerSample{cs("viejo", ahora.Add(-time.Hour)), cs("recien-creado", ahora)}

	if ev := rules.DetectarReinicioContainers(antes, despues, ahora); ev != nil {
		t.Errorf("detectó un reinicio de un container nuevo: %+v", ev)
	}
}

// Las filas anteriores a la migración 10 tienen started_at en cero. Leerlas
// como un arranque en 1970 haría que TODO container pareciera recién
// reiniciado en el primer tick después del deploy.
func TestStartedAtDesconocidoNoDisparaNada(t *testing.T) {
	antes := []model.ContainerSample{cs("x", time.Time{})}
	despues := []model.ContainerSample{cs("x", ahora)}

	if ev := rules.DetectarReinicioContainers(antes, despues, ahora); ev != nil {
		t.Errorf("detectó un reinicio desde un started_at desconocido: %+v", ev)
	}
}

func TestSinCambiosNoHayEvento(t *testing.T) {
	viejo := ahora.Add(-10 * 24 * time.Hour)
	antes := []model.ContainerSample{cs("a", viejo)}
	despues := []model.ContainerSample{cs("a", viejo)}

	if ev := rules.DetectarReinicioContainers(antes, despues, ahora); ev != nil {
		t.Errorf("detectó un evento sin cambios: %+v", ev)
	}
}

// Si el host se reinició, que los containers hayan vuelto NO es noticia
// aparte: es la consecuencia obvia. Se pliega adentro del reboot.
func TestLosContainersSePlieganDentroDelReboot(t *testing.T) {
	viejo := ahora.Add(-10 * 24 * time.Hour)
	var antes, despues []model.ContainerSample
	for _, n := range []string{"a", "b", "c"} {
		antes = append(antes, cs(n, viejo))
		despues = append(despues, cs(n, ahora))
	}
	hostAntes := muestra(ahora.Add(-time.Minute), 13*24*time.Hour)
	hostAhora := muestra(ahora, 36*time.Second)

	evs := rules.DetectarEventos(hostAntes, hostAhora, antes, despues, ahora)
	if len(evs) != 1 {
		t.Fatalf("dio %d eventos, quería 1 solo: %+v", len(evs), evs)
	}
	if evs[0].Tipo != "reboot" {
		t.Errorf("tipo = %q, quería reboot", evs[0].Tipo)
	}
	if !strings.Contains(evs[0].Detalle, "3") {
		t.Errorf("el reboot no menciona los containers que volvieron: %q", evs[0].Detalle)
	}
}

// Sin reboot, un reinicio de containers sí es noticia propia: es el caso del
// bot de gymtracker reiniciado a mano.
func TestSinRebootElReinicioDeContainersEsSuPropioEvento(t *testing.T) {
	viejo := ahora.Add(-10 * 24 * time.Hour)
	antes := []model.ContainerSample{cs("gym-bot", viejo)}
	despues := []model.ContainerSample{cs("gym-bot", ahora)}
	host := muestra(ahora, 13*24*time.Hour)
	hostAntes := muestra(ahora.Add(-time.Minute), 13*24*time.Hour-time.Minute)

	evs := rules.DetectarEventos(hostAntes, host, antes, despues, ahora)
	if len(evs) != 1 || evs[0].Tipo != "container_restart" {
		t.Fatalf("dio %+v, quería un solo container_restart", evs)
	}
}
