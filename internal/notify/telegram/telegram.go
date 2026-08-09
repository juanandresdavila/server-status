// Package telegram habla directo con api.telegram.org.
//
// Es el camino de respaldo: existe para el caso en que lo caído sea comm-tool,
// que es justo el aviso más importante que puede haber.
package telegram

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
	token  string
	chatID string
	http   *http.Client
}

// New recibe la base de la API como parámetro para poder apuntarla a un
// httptest en los tests. En producción es "https://api.telegram.org".
func New(base, token, chatID string) *Canal {
	return &Canal{
		base: base, token: token, chatID: chatID,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Canal) Nombre() string { return "telegram" }

func (c *Canal) Configurado() bool { return c.token != "" && c.chatID != "" }

func (c *Canal) Mandar(ctx context.Context, texto string) error {
	cuerpo, err := json.Marshal(map[string]string{
		"chat_id": c.chatID,
		"text":    texto,
	})
	if err != nil {
		return err
	}

	// El token va en la ruta. Ojo: nunca meter esta URL en un mensaje de error
	// — los errores terminan en el journal, que se lee y se comparte.
	url := fmt.Sprintf("%s/bot%s/sendMessage", c.base, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(cuerpo))
	if err != nil {
		return fmt.Errorf("telegram: armando el request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// El error de http.Client incluye la URL, y la URL tiene el token.
		// Por eso se reporta solo el tipo de falla, no el error crudo.
		return fmt.Errorf("telegram: no se pudo conectar")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// El cuerpo trae "description" con la causa real. Se lee acotado:
		// nunca confiar en el largo de una respuesta ajena.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return nil
}
