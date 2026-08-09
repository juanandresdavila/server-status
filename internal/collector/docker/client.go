// Package docker es un cliente mínimo de la API de Docker sobre su socket unix.
//
// No usa la SDK oficial a propósito: hacen falta cuatro endpoints y la SDK
// arrastra un árbol de dependencias enorme para eso.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct {
	http *http.Client
}

// New arma un cliente que habla HTTP sobre un socket unix. El host de la URL
// ("docker") es un placeholder: el DialContext ignora la dirección y siempre
// abre el socket.
func New(socket string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
		Timeout: 30 * time.Second,
	}}
}

func (c *Client) get(ctx context.Context, ruta string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+ruta, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", ruta, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		cuerpo, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %s: %s", ruta, resp.Status, bytes.TrimSpace(cuerpo))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
