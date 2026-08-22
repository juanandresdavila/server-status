// Package comandos parsea y ejecuta los comandos que llegan por Telegram.
//
// Ningún comando MODIFICA el servidor. `/silenciar` cambia estado del propio
// server-status y nada más: un `/reiniciar` por Telegram sería una puerta
// trasera a root con la seguridad de un HMAC y una regla de firewall.
package comandos

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"
)

// LargoMaximo es el límite de Telegram, que comm-tool además valida ANTES de
// mandar: pasarse convierte la respuesta en un 400 y el bot queda mudo.
const LargoMaximo = 4096

type Comando struct {
	Nombre string
	Args   []string
}

// Parsear devuelve el comando, o ok=false si el texto no es uno.
func Parsear(texto string) (Comando, bool) {
	campos := strings.Fields(strings.TrimSpace(texto))
	if len(campos) == 0 || !strings.HasPrefix(campos[0], "/") {
		return Comando{}, false
	}
	// Telegram le agrega "@nombredelbot" cuando el comando va en un grupo.
	nombre, _, _ := strings.Cut(strings.TrimPrefix(campos[0], "/"), "@")
	if nombre == "" {
		return Comando{}, false
	}
	return Comando{Nombre: strings.ToLower(nombre), Args: campos[1:]}, true
}

// Datos es lo que los comandos necesitan leer.
type Datos interface {
	UltimasHostSamples(n int) ([]model.HostSample, error)
	UltimoEstadoProbes() ([]model.ProbeResult, error)
	UltimosIncidentes(n int) ([]model.Incidente, error)
	BuscarLogs(texto, container, nivelMinimo string, desde, hasta time.Time, limite int) ([]model.LineaLog, error)
	Silenciar(hasta time.Time) error
}

func Ejecutar(d Datos, c Comando, ahora time.Time) (string, error) {
	switch c.Nombre {
	case "status":
		return status(d)
	case "logs":
		return logs(d, c.Args, ahora)
	case "incidentes":
		return incidentes(d)
	case "silenciar":
		return silenciar(d, c.Args, ahora)
	default:
		return ayuda(), nil
	}
}

func ayuda() string {
	return "No conozco ese comando. Los que hay:\n" +
		"/status — cómo está todo ahora\n" +
		"/logs <container> [n] — últimas n líneas (default 20)\n" +
		"/incidentes — los últimos 10\n" +
		"/silenciar <duración> — por ejemplo 2h; con 0 se cancela"
}

func status(d Datos) (string, error) {
	probes, err := d.UltimoEstadoProbes()
	if err != nil {
		return "", err
	}
	hs, err := d.UltimasHostSamples(1)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("📊 Estado\n")
	for _, p := range probes {
		icono := "🟢"
		if !p.OK {
			icono = "🔴"
		}
		fmt.Fprintf(&b, "%s %s\n", icono, p.Servicio)
	}
	if len(hs) > 0 {
		h := hs[0]
		fmt.Fprintf(&b, "\ncpu %.0f%% · disco %.0f%% · memoria %.0f%% · uptime %.0f h",
			h.CPUPctAvg,
			pct(h.DiskUsedBytes, h.DiskTotalBytes),
			pct(h.MemUsedBytes, h.MemTotalBytes),
			h.Uptime.Hours())
	}
	return acortar(b.String()), nil
}

func incidentes(d Datos) (string, error) {
	is, err := d.UltimosIncidentes(10)
	if err != nil {
		return "", err
	}
	if len(is) == 0 {
		return "Sin incidentes registrados.", nil
	}
	var b strings.Builder
	b.WriteString("🗒 Últimos incidentes\n")
	for _, i := range is {
		estado := "abierto"
		if i.CerradoEn != nil {
			estado = "cerrado"
		}
		fmt.Fprintf(&b, "%s · %s · %s · %s\n",
			i.AbiertoEn.Format("02/01 15:04"), i.Sujeto, estado, i.Detalle)
	}
	return acortar(b.String()), nil
}

func logs(d Datos, args []string, ahora time.Time) (string, error) {
	if len(args) == 0 {
		return "Falta el container: /logs <container> [n]", nil
	}
	container := args[0]

	n := 20
	if len(args) > 1 {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			n = v
		}
	}
	// Tope duro: con más, el mensaje se pasa del límite de Telegram y se
	// convierte en un 400 en vez de una respuesta.
	if n > 50 {
		n = 50
	}

	// Mínimo INFO: el /logs del bot manda unas pocas líneas a un chat, y
	// gastarlas en la continuación de un SQL no le sirve a nadie.
	ls, err := d.BuscarLogs("", container, "INFO", ahora.AddDate(0, 0, -30), ahora, n)
	if err != nil {
		return "", err
	}
	if len(ls) == 0 {
		return "Sin líneas de " + container + " en los últimos 30 días.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📜 %s · últimas %d\n", container, len(ls))
	// Vienen de la más nueva a la más vieja; se dan vuelta para leerlas en
	// orden, como en una terminal.
	sort.Slice(ls, func(i, j int) bool { return ls[i].TS.Before(ls[j].TS) })
	for _, l := range ls {
		fmt.Fprintf(&b, "%s %s\n", l.TS.Local().Format("15:04:05"), l.Linea)
	}
	return acortar(b.String()), nil
}

func silenciar(d Datos, args []string, ahora time.Time) (string, error) {
	if len(args) == 0 {
		return "Falta la duración: /silenciar 2h — con 0 se cancela.", nil
	}
	if args[0] == "0" {
		if err := d.Silenciar(ahora); err != nil {
			return "", err
		}
		return "Silencio cancelado: vuelvo a avisar de todo.", nil
	}

	dur, err := time.ParseDuration(args[0])
	if err != nil || dur <= 0 {
		return "No entendí la duración " + args[0] + ". Probá con 30m, 2h o 1h30m.", nil
	}
	if err := d.Silenciar(ahora.Add(dur)); err != nil {
		return "", err
	}
	return fmt.Sprintf("Callado por %s. Los críticos igual pasan.", dur), nil
}

// acortar deja el mensaje dentro del límite de Telegram y avisa que cortó:
// truncar en silencio es peor que decirlo.
func acortar(s string) string {
	if len(s) <= LargoMaximo {
		return s
	}
	const marca = "\n[…recortado]"
	return s[:LargoMaximo-len(marca)] + marca
}

func pct(usado, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(usado) * 100 / float64(total)
}
