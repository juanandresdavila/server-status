package commtool_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/juanandresdavila/server-status/internal/notify/commtool"
)

const secreto = "secreto-de-entrega"

// firmar replica lo que hace comm-tool: HMAC-SHA256 sobre "<t>.<cuerpo>".
func firmar(cuerpo string, t time.Time) string {
	m := hmac.New(sha256.New, []byte(secreto))
	fmt.Fprintf(m, "%d.%s", t.Unix(), cuerpo)
	return fmt.Sprintf("t=%d,v1=%s", t.Unix(), hex.EncodeToString(m.Sum(nil)))
}

func TestFirmaValidaConElCuerpoExacto(t *testing.T) {
	ahora := time.Now()
	cuerpo := `{"messageId":"m1","userId":"u","channel":"telegram","text":"/status"}`

	if !commtool.FirmaValida(secreto, []byte(cuerpo), firmar(cuerpo, ahora), ahora) {
		t.Error("rechazó una firma correcta")
	}
}

// La firma es sobre los BYTES EXACTOS: volver a serializar el JSON parseado
// cambia el HMAC. Lo dice el propio cliente de comm-tool.
func TestUnByteDistintoEnElCuerpoInvalidaLaFirma(t *testing.T) {
	ahora := time.Now()
	cuerpo := `{"text":"/status"}`
	f := firmar(cuerpo, ahora)

	if commtool.FirmaValida(secreto, []byte(`{"text":"/status" }`), f, ahora) {
		t.Error("aceptó un cuerpo con un espacio de más")
	}
}

func TestFirmaViejaSeRechaza(t *testing.T) {
	viejo := time.Now().Add(-6 * time.Minute)
	cuerpo := `{"text":"/status"}`

	if commtool.FirmaValida(secreto, []byte(cuerpo), firmar(cuerpo, viejo), time.Now()) {
		t.Error("aceptó una firma de hace 6 minutos: la ventana es de 300 s")
	}
}

// La deriva se mide en valor absoluto: un reloj adelantado tampoco pasa.
func TestFirmaFuturaSeRechaza(t *testing.T) {
	futuro := time.Now().Add(6 * time.Minute)
	cuerpo := `{"text":"/status"}`

	if commtool.FirmaValida(secreto, []byte(cuerpo), firmar(cuerpo, futuro), time.Now()) {
		t.Error("aceptó una firma con 6 minutos de adelanto")
	}
}

func TestSecretoDistintoSeRechaza(t *testing.T) {
	ahora := time.Now()
	cuerpo := `{"text":"/status"}`
	m := hmac.New(sha256.New, []byte("otro-secreto"))
	fmt.Fprintf(m, "%d.%s", ahora.Unix(), cuerpo)
	f := fmt.Sprintf("t=%d,v1=%s", ahora.Unix(), hex.EncodeToString(m.Sum(nil)))

	if commtool.FirmaValida(secreto, []byte(cuerpo), f, ahora) {
		t.Error("aceptó una firma hecha con otro secreto")
	}
}

func TestHeaderMalFormadoSeRechaza(t *testing.T) {
	ahora := time.Now()
	cuerpo := `{"text":"/status"}`

	for _, h := range []string{"", "basura", "t=abc,v1=xx", "v1=solo", "t=123", "t=1,v1="} {
		if commtool.FirmaValida(secreto, []byte(cuerpo), h, ahora) {
			t.Errorf("aceptó el header %q", h)
		}
	}
}

// Sin secreto configurado no se puede validar nada: rechazar todo es lo
// correcto. Aceptar sería dejar el webhook abierto de par en par.
func TestSinSecretoRechazaTodo(t *testing.T) {
	ahora := time.Now()
	cuerpo := `{"text":"/status"}`
	if commtool.FirmaValida("", []byte(cuerpo), firmar(cuerpo, ahora), ahora) {
		t.Error("sin secreto aceptó una firma")
	}
}
