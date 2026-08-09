package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LineaLog es una línea ya demultiplexada y fechada.
type LineaLog struct {
	TS     time.Time
	Stream string // stdout | stderr
	Linea  string
}

// Logs pide las líneas de un container desde un momento dado.
//
// Sin follow a propósito: con ~2 líneas por minuto entre 21 containers, un
// stream permanente por container serían 21 conexiones vivas y toda la lógica
// de reconexión que eso arrastra, para transportar casi nada. El tail en vivo
// sí usa follow, pero solo mientras alguien está mirando.
func (c *Client) Logs(ctx context.Context, id string, desde time.Time) ([]LineaLog, error) {
	ruta := fmt.Sprintf(
		"/containers/%s/logs?stdout=1&stderr=1&timestamps=1&since=%s",
		id, strconv.FormatInt(desde.Unix(), 10))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+ruta, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("logs de %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		cuerpo, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("logs de %s: %s: %s", id, resp.Status, strings.TrimSpace(string(cuerpo)))
	}
	return DemuxLogs(resp.Body)
}

// DemuxLogs parsea el formato multiplexado de Docker.
//
// Con Tty:false —el caso de los 21 containers del VPS— Docker no manda texto
// plano sino bloques con 8 bytes de encabezado:
//
//	byte 0     tipo: 1 = stdout, 2 = stderr
//	bytes 1-3  cero
//	bytes 4-7  tamaño del payload, big-endian
//
// Sin demultiplexar, cada línea llega con basura binaria adelante — y eso pasa
// desapercibido con facilidad en una tabla HTML.
//
// Un stream cortado NO es un error: se devuelve lo que se alcanzó a leer. Pasa
// cuando el container se muere mientras se lo lee, y perder las líneas buenas
// por eso sería peor que el corte.
func DemuxLogs(r io.Reader) ([]LineaLog, error) {
	br := bufio.NewReader(r)
	var out []LineaLog

	for {
		var cab [8]byte
		if _, err := io.ReadFull(br, cab[:]); err != nil {
			return out, nil
		}
		tam := binary.BigEndian.Uint32(cab[4:8])
		if tam == 0 {
			continue
		}
		payload := make([]byte, tam)
		if _, err := io.ReadFull(br, payload); err != nil {
			return out, nil
		}

		stream := "stdout"
		if cab[0] == 2 {
			stream = "stderr"
		}
		for _, cruda := range strings.Split(strings.TrimRight(string(payload), "\n"), "\n") {
			if cruda == "" {
				continue
			}
			ts, linea := partirTimestamp(cruda)
			out = append(out, LineaLog{TS: ts, Stream: stream, Linea: linea})
		}
	}
}

// partirTimestamp separa el prefijo RFC3339Nano que agrega ?timestamps=1.
// Sin prefijo la línea igual sirve: se le pone la hora de lectura, que es
// mejor que descartarla.
func partirTimestamp(cruda string) (time.Time, string) {
	fecha, resto, ok := strings.Cut(cruda, " ")
	if !ok {
		return time.Now().UTC(), cruda
	}
	ts, err := time.Parse(time.RFC3339Nano, fecha)
	if err != nil {
		return time.Now().UTC(), cruda
	}
	return ts.UTC(), resto
}

// SeguirLogs abre un stream con follow y emite cada línea a medida que llega.
//
// No reusa DemuxLogs a propósito: esa lee hasta EOF, y un follow no termina
// nunca — colgaría para siempre sin emitir una sola línea. Acá el demux es
// incremental, bloque por bloque.
//
// Vuelve cuando se cancela el contexto, que es lo que pasa cuando el cliente
// cierra la pestaña del tail.
func (c *Client) SeguirLogs(ctx context.Context, id string, out chan<- LineaLog) error {
	ruta := fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&timestamps=1&follow=1&tail=20", id)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+ruta, nil)
	if err != nil {
		return err
	}
	// Sin timeout: un follow dura lo que dure la pestaña abierta. El corte lo
	// da el contexto.
	cliente := &http.Client{Transport: c.http.Transport}
	resp, err := cliente.Do(req)
	if err != nil {
		return fmt.Errorf("seguir logs de %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("seguir logs de %s: %s", id, resp.Status)
	}

	br := bufio.NewReader(resp.Body)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var cab [8]byte
		if _, err := io.ReadFull(br, cab[:]); err != nil {
			return err
		}
		tam := binary.BigEndian.Uint32(cab[4:8])
		if tam == 0 {
			continue
		}
		payload := make([]byte, tam)
		if _, err := io.ReadFull(br, payload); err != nil {
			return err
		}

		stream := "stdout"
		if cab[0] == 2 {
			stream = "stderr"
		}
		for _, cruda := range strings.Split(strings.TrimRight(string(payload), "\n"), "\n") {
			if cruda == "" {
				continue
			}
			ts, linea := partirTimestamp(cruda)
			select {
			case out <- LineaLog{TS: ts, Stream: stream, Linea: linea}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
