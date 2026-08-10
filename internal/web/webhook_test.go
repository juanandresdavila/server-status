package web_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/web"
)

const secretoWH = "secreto-de-entrega"

func firmarWH(cuerpo string, t time.Time) string {
	m := hmac.New(sha256.New, []byte(secretoWH))
	fmt.Fprintf(m, "%d.%s", t.Unix(), cuerpo)
	return fmt.Sprintf("t=%d,v1=%s", t.Unix(), hex.EncodeToString(m.Sum(nil)))
}

type manejadorFalso struct {
	vistos    []string
	respuesta string
	yaVisto   map[string]bool
}

func (m *manejadorFalso) Procesar(deliveryID, texto string) (string, error) {
	if m.yaVisto == nil {
		m.yaVisto = map[string]bool{}
	}
	if m.yaVisto[deliveryID] {
		return "", nil // ya procesado
	}
	m.yaVisto[deliveryID] = true
	m.vistos = append(m.vistos, texto)
	return m.respuesta, nil
}

func postear(t *testing.T, h *manejadorFalso, cuerpo, firma, deliveryID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/webhooks/comm-tool", strings.NewReader(cuerpo))
	req.Header.Set("X-Comm-Signature", firma)
	req.Header.Set("X-Comm-Delivery-Id", deliveryID)
	rec := httptest.NewRecorder()
	web.NuevoWebhook(secretoWH, h).ServeHTTP(rec, req)
	return rec
}

func TestWebhookAceptaUnaEntregaFirmada(t *testing.T) {
	h := &manejadorFalso{respuesta: "todo bien"}
	cuerpo := `{"messageId":"m1","userId":"u","channel":"telegram","text":"/status"}`

	rec := postear(t, h, cuerpo, firmarWH(cuerpo, time.Now()), "m1")
	if rec.Code != 200 {
		t.Fatalf("código = %d, cuerpo = %q", rec.Code, rec.Body.String())
	}
	if len(h.vistos) != 1 || h.vistos[0] != "/status" {
		t.Errorf("vistos = %v", h.vistos)
	}
}

// Este es el control de acceso: sin firma válida no entra nada.
func TestWebhookRechazaSinFirmaValida(t *testing.T) {
	cuerpo := `{"text":"/status"}`
	casos := map[string]string{
		"sin firma":       "",
		"firma inventada": "t=1,v1=abc",
		"firma de otro":   firmarWH(`{"text":"otra cosa"}`, time.Now()),
	}
	for nombre, firma := range casos {
		h := &manejadorFalso{}
		rec := postear(t, h, cuerpo, firma, "m1")
		if rec.Code != 401 {
			t.Errorf("%s: código = %d, quería 401", nombre, rec.Code)
		}
		if len(h.vistos) != 0 {
			t.Errorf("%s: procesó el comando igual", nombre)
		}
	}
}

// comm-tool reintenta hasta 5 veces. El segundo intento tiene que dar 200
// —si no, reintenta para siempre— pero no ejecutar de nuevo.
func TestReintentoNoEjecutaDosVeces(t *testing.T) {
	h := &manejadorFalso{respuesta: "ok"}
	cuerpo := `{"messageId":"m1","text":"/silenciar 2h"}`
	firma := firmarWH(cuerpo, time.Now())

	postear(t, h, cuerpo, firma, "m1")
	rec := postear(t, h, cuerpo, firma, "m1")

	if rec.Code != 200 {
		t.Errorf("el reintento dio %d: comm-tool va a seguir reintentando", rec.Code)
	}
	if len(h.vistos) != 1 {
		t.Errorf("se ejecutó %d veces, quería 1", len(h.vistos))
	}
}

// El cuerpo se lee crudo para firmar. Si el JSON está roto, eso es un 400 y
// no un 500: el problema es del que manda.
func TestCuerpoNoJSONEsCuatrocientos(t *testing.T) {
	h := &manejadorFalso{}
	cuerpo := `esto no es json`
	rec := postear(t, h, cuerpo, firmarWH(cuerpo, time.Now()), "m1")
	if rec.Code != 400 {
		t.Errorf("código = %d, quería 400", rec.Code)
	}
}

// Un mensaje que no es comando no se responde: el bot no tiene que contestar
// cada cosa que se le escriba.
func TestTextoQueNoEsComandoSeIgnora(t *testing.T) {
	h := &manejadorFalso{}
	cuerpo := `{"messageId":"m1","text":"hola bot"}`
	rec := postear(t, h, cuerpo, firmarWH(cuerpo, time.Now()), "m1")
	if rec.Code != 200 {
		t.Errorf("código = %d", rec.Code)
	}
}
