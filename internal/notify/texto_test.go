package notify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/notify"
)

func TestTextoDeApertura(t *testing.T) {
	got := notify.Texto(aviso(1, false))

	if !strings.Contains(got, "comm-tool") {
		t.Errorf("el mensaje no nombra el servicio: %q", got)
	}
	if !strings.Contains(got, "HTTP 502") {
		t.Errorf("el mensaje no dice la causa: %q", got)
	}
	// El sujeto crudo ('service:comm-tool') es una etiqueta interna:
	// en el mensaje va el nombre a secas.
	if strings.Contains(got, "service:") {
		t.Errorf("el mensaje filtra el prefijo interno del sujeto: %q", got)
	}
}

func TestTextoDeCierreDiceCuantoDuro(t *testing.T) {
	got := notify.Texto(aviso(1, true))

	if !strings.Contains(got, "4m") {
		t.Errorf("el cierre no dice la duración: %q", got)
	}
	if !strings.Contains(got, "comm-tool") {
		t.Errorf("el cierre no nombra el servicio: %q", got)
	}
}

// Telegram corta a los 4096 caracteres y comm-tool valida ese largo antes de
// mandar: un mensaje largo se convertiría en un 400 en vez de un aviso.
func TestElTextoNuncaPasaElLimiteDeTelegram(t *testing.T) {
	a := aviso(1, false)
	a.Incidente.Detalle = strings.Repeat("x", 9000)

	if n := len(notify.Texto(a)); n > 4096 {
		t.Errorf("el mensaje mide %d caracteres, el límite es 4096", n)
	}
}

func TestNombreDeSujetoSacaElPrefijo(t *testing.T) {
	casos := map[string]string{
		"service:comm-tool":     "comm-tool",
		"container:supabase-db": "supabase-db",
		"host:disk":             "disco",
		"host:mem":              "memoria",
		"host:swap":             "swap",
		"host:load":             "carga",
		"sin-prefijo":           "sin-prefijo",
	}
	for sujeto, quiero := range casos {
		if got := notify.NombreDeSujeto(sujeto); got != quiero {
			t.Errorf("NombreDeSujeto(%q) = %q, quería %q", sujeto, got, quiero)
		}
	}
}

// La severidad se ve de un vistazo: crítico y advertencia no pueden mirarse
// igual en la lista de notificaciones del celular.
func TestCriticoYAdvertenciaSeDistinguen(t *testing.T) {
	critico := aviso(1, false)
	advertencia := aviso(2, false)
	advertencia.Incidente.Severidad = "warning"

	if notify.Texto(critico)[:4] == notify.Texto(advertencia)[:4] {
		t.Error("un crítico y una advertencia arrancan igual")
	}
}

func TestTextoDeResumen(t *testing.T) {
	r := notify.Resumen{
		Uptime:     50 * time.Hour,
		DiscoPct:   15.7,
		MemPct:     28.0,
		Incidentes: 2,
		Servicios:  map[string]bool{"comm-tool": true, "sitio": false},
	}
	got := notify.TextoResumen(r)

	if !strings.Contains(got, "15.7") {
		t.Errorf("el resumen no dice el disco: %q", got)
	}
	if !strings.Contains(got, "sitio") {
		t.Errorf("el resumen no lista los servicios: %q", got)
	}
	if !strings.Contains(got, "2 incidentes") {
		t.Errorf("el resumen no dice los incidentes: %q", got)
	}
}

func TestResumenSinIncidentesLoDiceAsi(t *testing.T) {
	got := notify.TextoResumen(notify.Resumen{Incidentes: 0})
	if !strings.Contains(got, "sin incidentes") {
		t.Errorf("el resumen de un día tranquilo no lo dice: %q", got)
	}
}

func TestTextoEventoDiceQuePasoYCuando(t *testing.T) {
	e := model.Evento{
		Tipo: "reboot", Sujeto: "host", Severidad: "critical",
		OcurridoEn: time.Date(2026, 8, 22, 5, 0, 31, 0, time.UTC),
		Detalle:    "la máquina se reinició: arrancó 22/08 02:00:31, sin datos durante 1m20s",
	}
	got := notify.TextoEvento(e)
	if !strings.Contains(got, "se reinició") {
		t.Errorf("el mensaje no dice qué pasó: %q", got)
	}
	if !strings.Contains(got, "1m20s") {
		t.Errorf("el mensaje no lleva el detalle: %q", got)
	}
}

func TestTextoEventoDeContainersEsDistintoDelReboot(t *testing.T) {
	reboot := notify.TextoEvento(model.Evento{Tipo: "reboot", Severidad: "critical", Detalle: "x"})
	cont := notify.TextoEvento(model.Evento{Tipo: "container_restart", Severidad: "warning", Detalle: "x"})
	if reboot == cont {
		t.Error("un reboot y un reinicio de containers no pueden leerse igual")
	}
}
