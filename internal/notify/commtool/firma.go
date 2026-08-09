package commtool

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// VentanaDeFirma es la deriva máxima aceptada entre el reloj de comm-tool y el
// nuestro. Es el mismo valor que usa comm-tool del otro lado; cambiarlo acá sin
// cambiarlo allá hace que las entregas se rechacen sin ninguna pista.
const VentanaDeFirma = 300 * time.Second

// FirmaValida verifica el header X-Comm-Signature de una entrega de comm-tool.
//
// Formato: "t=<unix>,v1=<hex>", donde el hex es HMAC-SHA256 sobre "<t>.<cuerpo>".
//
// El cuerpo son los BYTES EXACTOS que llegaron. Volver a serializar el JSON
// parseado cambia el HMAC: hasta un espacio de más lo invalida. Por eso quien
// llama tiene que leer el body crudo ANTES de decodificarlo.
//
// Es el control de acceso entero del webhook: sin firma válida, cualquiera que
// alcance el puerto podría mandar comandos.
func FirmaValida(secreto string, cuerpo []byte, header string, ahora time.Time) bool {
	// Sin secreto no se puede validar nada, y aceptar dejaría el webhook
	// abierto de par en par.
	if secreto == "" {
		return false
	}

	t, v1, ok := parsearHeaderDeFirma(header)
	if !ok {
		return false
	}

	// Valor absoluto: un reloj adelantado tampoco pasa.
	deriva := ahora.Unix() - t
	if deriva < 0 {
		deriva = -deriva
	}
	if time.Duration(deriva)*time.Second > VentanaDeFirma {
		return false
	}

	m := hmac.New(sha256.New, []byte(secreto))
	m.Write([]byte(strconv.FormatInt(t, 10)))
	m.Write([]byte("."))
	m.Write(cuerpo)
	esperado := hex.EncodeToString(m.Sum(nil))

	// hmac.Equal y no ==: la comparación tiene que ser de tiempo constante.
	return hmac.Equal([]byte(esperado), []byte(v1))
}

func parsearHeaderDeFirma(header string) (t int64, v1 string, ok bool) {
	for _, parte := range strings.Split(header, ",") {
		clave, valor, hay := strings.Cut(strings.TrimSpace(parte), "=")
		if !hay {
			continue
		}
		switch clave {
		case "t":
			n, err := strconv.ParseInt(valor, 10, 64)
			if err != nil {
				return 0, "", false
			}
			t = n
		case "v1":
			v1 = valor
		}
	}
	if t == 0 || v1 == "" {
		return 0, "", false
	}
	return t, v1, true
}
