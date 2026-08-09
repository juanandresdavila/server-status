// Package watchdog le late a un servicio externo para que alguien de afuera
// avise si se muere todo.
//
// Healthchecks.io funciona al revés que un monitor común: vos le latés y, si
// dejás de latir, avisa. Eso deja dos agujeros que este paquete cierra:
//
//  1. Si el túnel de Cloudflare se cae pero el proceso sigue vivo, el latido
//     saldría igual y nadie se entera. Por eso antes de latir se consulta a sí
//     mismo por la URL PÚBLICA, dando toda la vuelta.
//  2. Un 200 no alcanza: Caddy sirve feliz un archivo viejo si el proceso dejó
//     de escribirlo. Por eso se verifica la marca de frescura embebida.
package watchdog

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"context"

	"github.com/juanandresdavila/server-status/internal/clock"
)

// marca busca el comentario que la portada embebe: <!--generado:1786...-->
var marca = regexp.MustCompile(`<!--generado:(\d+)-->`)

type Watchdog struct {
	urlPublica string
	urlPing    string
	clk        clock.Clock
	frescura   time.Duration
	http       *http.Client
}

func New(urlPublica, urlPing string, clk clock.Clock, frescura time.Duration) *Watchdog {
	return &Watchdog{
		urlPublica: urlPublica,
		urlPing:    urlPing,
		clk:        clk,
		frescura:   frescura,
		http:       &http.Client{Timeout: 20 * time.Second},
	}
}

// Configurado dice si hay a dónde latir. Sin la URL de ping el watchdog no
// sirve de nada, pero tampoco molesta.
func (w *Watchdog) Configurado() bool { return w.urlPing != "" }

// Latir hace la vuelta completa y, solo si sale bien, le pega a Healthchecks.
func (w *Watchdog) Latir(ctx context.Context) error {
	if !w.Configurado() {
		return nil
	}

	if err := w.verificarPortada(ctx); err != nil {
		// No latir ES la señal: Healthchecks avisa al vencer la gracia.
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.urlPing, nil)
	if err != nil {
		return err
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("latido: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("latido: %s", resp.Status)
	}
	return nil
}

// verificarPortada consulta la URL pública —saliendo a internet, pasando por
// Cloudflare y volviendo por el túnel— y comprueba que lo que vuelve esté
// fresco.
func (w *Watchdog) verificarPortada(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.urlPublica, nil)
	if err != nil {
		return err
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("no me alcanzo a mí mismo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("la portada devolvió %s", resp.Status)
	}

	cuerpo, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return err
	}

	m := marca.FindSubmatch(cuerpo)
	if m == nil {
		// Una página que responde pero no se puede evaluar es una falla, no
		// un éxito: puede ser cualquier cosa sirviéndose en ese hostname.
		return errors.New("la portada no trae la marca de frescura")
	}
	unix, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return fmt.Errorf("marca de frescura ilegible: %w", err)
	}

	edad := w.clk.Now().Sub(time.Unix(unix, 0))
	if edad > w.frescura {
		return fmt.Errorf("la portada está congelada hace %s", edad.Round(time.Second))
	}
	return nil
}
