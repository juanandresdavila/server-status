package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/juanandresdavila/server-status/internal/notify/commtool"
)

// Manejador procesa un comando entrante. Devuelve el texto a responder, o
// vacío si no hay nada que contestar.
type Manejador interface {
	Procesar(deliveryID, texto string) (string, error)
}

// entrega es el cuerpo que manda comm-tool. Solo se toman los campos que se
// usan: el resto —raw, channel, receivedAt— no le importa a los comandos.
type entrega struct {
	MessageID string `json:"messageId"`
	Text      string `json:"text"`
}

// NuevoWebhook recibe las entregas firmadas de comm-tool.
//
// Va en un LISTENER APARTE del panel, y no por gusto: el panel muestra todo
// —errores crudos, métricas, logs— y para que comm-tool lo alcance desde su
// container hace falta abrir el puerto a la subred de Docker. Abrir el del
// panel sería superficie regalada; este endpoint, en cambio, no expone nada:
// solo acepta cuerpos firmados.
func NuevoWebhook(secreto string, m Manejador) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// El cuerpo se lee CRUDO: la firma es sobre los bytes exactos, y
		// volver a serializar el JSON parseado cambiaría el HMAC.
		cuerpo, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "no se pudo leer el cuerpo", http.StatusBadRequest)
			return
		}

		if !commtool.FirmaValida(secreto, cuerpo, r.Header.Get("X-Comm-Signature"), time.Now()) {
			slog.Warn("entrega con firma inválida", "delivery", r.Header.Get("X-Comm-Delivery-Id"))
			http.Error(w, "firma inválida", http.StatusUnauthorized)
			return
		}

		var e entrega
		if err := json.Unmarshal(cuerpo, &e); err != nil {
			http.Error(w, "el cuerpo no es JSON", http.StatusBadRequest)
			return
		}

		deliveryID := r.Header.Get("X-Comm-Delivery-Id")
		if deliveryID == "" {
			deliveryID = e.MessageID
		}

		// Un error acá devuelve 500 a propósito: comm-tool reintenta, que es
		// lo que se quiere si la base estaba trabada un momento.
		if _, err := m.Procesar(deliveryID, e.Text); err != nil {
			slog.Error("no se pudo procesar el comando", "delivery", deliveryID, "err", err)
			http.Error(w, "error procesando", http.StatusInternalServerError)
			return
		}

		// 200 aunque no haya nada que responder: un mensaje que no es comando
		// se ignora, pero la ENTREGA fue exitosa. Devolver otra cosa haría que
		// comm-tool reintentara cinco veces por cada "hola".
		w.WriteHeader(http.StatusOK)
	})
}
