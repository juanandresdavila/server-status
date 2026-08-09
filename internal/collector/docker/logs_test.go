package docker_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/collector/docker"
)

// bloque arma un frame como los que manda Docker: 1 byte de tipo de stream,
// 3 de relleno, 4 de tamaño big-endian, y el payload.
func bloque(tipo byte, payload string) []byte {
	var b bytes.Buffer
	b.WriteByte(tipo)
	b.Write([]byte{0, 0, 0})
	binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.WriteString(payload)
	return b.Bytes()
}

func TestDemuxSeparaStdoutDeStderr(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "2026-08-09T12:00:00.000000000Z arranque ok\n"))
	stream.Write(bloque(2, "2026-08-09T12:00:01.000000000Z ERROR se cayó\n"))

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatalf("DemuxLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d líneas, quería 2", len(got))
	}
	if got[0].Stream != "stdout" || got[1].Stream != "stderr" {
		t.Errorf("streams = %q, %q", got[0].Stream, got[1].Stream)
	}
	if got[0].Linea != "arranque ok" {
		t.Errorf("Linea = %q, quería 'arranque ok' — ¿quedó el timestamp adentro?", got[0].Linea)
	}
	if got[1].TS.UTC().Format("15:04:05") != "12:00:01" {
		t.Errorf("TS = %v", got[1].TS)
	}
}

// El payload de un bloque puede traer varias líneas de una.
func TestDemuxParteLasLineasDeUnMismoBloque(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "2026-08-09T12:00:00Z una\n2026-08-09T12:00:01Z dos\n"))

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("volvieron %d líneas, quería 2", len(got))
	}
}

// Si no se demultiplexa, la línea llega con basura binaria adelante. Este es
// el test que atrapa el error si alguien "simplifica" leyendo el stream crudo.
func TestDemuxNoDejaBytesDeEncabezadoEnElTexto(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "2026-08-09T12:00:00Z hola\n"))

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got[0].Linea {
		if c < 32 && c != '\t' {
			t.Fatalf("quedó un byte de control en el texto: %q", got[0].Linea)
		}
	}
}

// Un stream cortado a la mitad no puede hacer perder lo que ya se leyó: pasa
// cuando el container se muere mientras se lo está leyendo.
func TestDemuxDevuelveLoLeidoAunqueElStreamSeCorte(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "2026-08-09T12:00:00Z completa\n"))
	stream.Write([]byte{1, 0, 0, 0, 0, 0, 255}) // encabezado truncado

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatalf("no debería devolver error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("volvieron %d líneas, quería 1 (la completa)", len(got))
	}
}

// Sin timestamp la línea igual sirve: se le pone la hora de lectura.
func TestDemuxSinTimestampNoDescartaLaLinea(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(bloque(1, "sin fecha adelante\n"))

	got, err := docker.DemuxLogs(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Linea, "sin fecha") {
		t.Errorf("got = %+v", got)
	}
}

func TestLogsPideDesdeElCursorYConTimestamps(t *testing.T) {
	var q url.Values
	c := servidorFalso(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		w.Write(bloque(1, "2026-08-09T12:00:00Z hola\n"))
	}))

	desde := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	got, err := c.Logs(context.Background(), "abc", desde)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("since") != strconv.FormatInt(desde.Unix(), 10) {
		t.Errorf("since = %q, quería %d", q.Get("since"), desde.Unix())
	}
	// Sin timestamps=1 no hay forma de saber CUÁNDO pasó cada línea.
	if q.Get("timestamps") != "1" {
		t.Error("falta timestamps=1")
	}
	if q.Get("stdout") != "1" || q.Get("stderr") != "1" {
		t.Error("faltan stdout/stderr")
	}
	// follow abriría 21 conexiones vivas para transportar 2 líneas por minuto.
	if q.Get("follow") == "1" {
		t.Error("la ingesta NO debe usar follow")
	}
	if len(got) != 1 {
		t.Errorf("volvieron %d líneas", len(got))
	}
}
