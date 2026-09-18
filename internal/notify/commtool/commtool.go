// Package commtool manda por la API de communication-tool.
//
// Es el camino principal. La forma del request está tomada del código de
// comm-tool: POST /v1/messages, Authorization Bearer, y un cuerpo con userId,
// text y kind — los tres obligatorios en su schema.
package commtool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Canal struct {
	base   string
	apiKey string
	userID string
	http   *http.Client
}

// New recibe el userID que comm-tool usa para resolver el contacto.
// server-status no tiene tabla de usuarios: es un uuid fijo, generado una vez.
// comm-tool nunca lo interpreta — es una invariante suya.
//
// El cliente lleva su propio Transport, y con él su propio pool de conexiones.
// Con el http.DefaultTransport compartía la conexión a comm.jadd.com.ar con
// cualquier otro que le pegara al mismo host desde este proceso: el 18/09/2026
// esa conexión se quedó muda y el aviso de la caída se colgó junto con el
// probe que la había detectado.
func New(base, apiKey, userID string) *Canal {
	return &Canal{
		base: base, apiKey: apiKey, userID: userID,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
		},
	}
}

func (c *Canal) Nombre() string { return "commtool" }

func (c *Canal) Configurado() bool { return c.apiKey != "" && c.userID != "" }

// Mandar cumple notify.Canal. Sin clave de idempotencia comm-tool no deduplica
// nada, así que el camino normal es MandarCon.
func (c *Canal) Mandar(ctx context.Context, texto string) error {
	return c.MandarCon(ctx, texto, "")
}

func (c *Canal) MandarCon(ctx context.Context, texto, idempotencyKey string) error {
	payload := map[string]string{
		"userId": c.userID,
		"text":   texto,
		"kind":   "notification",
	}
	if idempotencyKey != "" {
		payload["idempotencyKey"] = idempotencyKey
	}
	cuerpo, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/messages", bytes.NewReader(cuerpo))
	if err != nil {
		return fmt.Errorf("comm-tool: armando el request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("comm-tool: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusConflict:
		// in_progress: la reserva anterior no se cerró y comm-tool no puede
		// saber si el mensaje salió. Reintentar arriesga un duplicado, así que
		// se da por entregado.
		return nil
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("comm-tool: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
}
