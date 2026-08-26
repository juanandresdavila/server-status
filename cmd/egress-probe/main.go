// Command egress-probe mide el egress del VPS por IPv4 y por IPv6 en paralelo.
//
// Existe porque "cero fallas por IPv4" no prueba nada mientras nunca se haya
// intentado por IPv4: el VPS sale siempre por IPv6 y no hay contrafactual. Esto
// lo construye.
//
// Corre AL LADO de server-status, no adentro: binario aparte, unit aparte y
// archivo aparte. El monitoreo de producción no se toca — y además es el
// control positivo de esta medición.
//
// El plan y la regla de decisión pre-registrada están en
// docs/superpowers/plans/2026-08-26-egress-ipv6-vs-ipv4.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/egress"
)

func main() {
	var (
		salida   = flag.String("salida", "/var/lib/egress-probe/medicion.jsonl", "archivo JSONL donde se escribe cada intento")
		analizar = flag.String("analizar", "", "en vez de medir, lee ese JSONL y aplica la regla pre-registrada")
		duracion = flag.Duration("duracion", 0, "cuánto medir (0 = hasta que lo paren)")
		timeout  = flag.Duration("timeout", 10*time.Second, "timeout por pedido; el de producción son 10s")
		soloStr  = flag.String("brazos", "", "correr solo estos brazos, separados por coma")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *analizar != "" {
		if err := correrAnalisis(*analizar); err != nil {
			log.Error("no se pudo analizar", "err", err)
			os.Exit(1)
		}
		return
	}

	brazos, err := elegirBrazos(*soloStr)
	if err != nil {
		log.Error("brazos inválidos", "err", err)
		os.Exit(1)
	}

	if err := medir(brazos, *salida, *duracion, *timeout, log); err != nil {
		log.Error("la medición falló", "err", err)
		os.Exit(1)
	}
}

func elegirBrazos(solo string) ([]egress.Brazo, error) {
	todos := egress.BrazosPorDefecto()
	if strings.TrimSpace(solo) == "" {
		return todos, nil
	}
	var elegidos []egress.Brazo
	for _, n := range strings.Split(solo, ",") {
		n = strings.TrimSpace(n)
		i := slices.IndexFunc(todos, func(b egress.Brazo) bool { return b.Nombre == n })
		if i < 0 {
			return nil, fmt.Errorf("no existe el brazo %q; hay: %s", n, egress.NombresDeBrazos(todos))
		}
		elegidos = append(elegidos, todos[i])
	}
	return elegidos, nil
}

func medir(brazos []egress.Brazo, salida string, duracion, timeout time.Duration, log *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(salida), 0o755); err != nil {
		return fmt.Errorf("creando el directorio de salida: %w", err)
	}
	f, err := os.OpenFile(salida, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("abriendo %s: %w", salida, err)
	}
	defer f.Close()

	esc := &escritor{enc: json.NewEncoder(f)}
	destinos := egress.DestinosPorDefecto()
	clk := clock.Real{}

	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer parar()
	if duracion > 0 {
		var cancelar context.CancelFunc
		ctx, cancelar = context.WithTimeout(ctx, duracion)
		defer cancelar()
	}

	log.Info("arranca la medición",
		"salida", salida, "brazos", egress.NombresDeBrazos(brazos),
		"destinos", len(destinos), "duracion", duracion)

	var wg sync.WaitGroup
	for _, b := range brazos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			correrBrazo(ctx, clk, b, destinos, timeout, esc, log)
		}()
	}
	wg.Wait()

	log.Info("medición terminada", "intentos", esc.escritos(), "salida", salida)
	return nil
}

func correrBrazo(ctx context.Context, clk clock.Clock, b egress.Brazo, destinos []egress.Destino,
	timeout time.Duration, esc *escritor, log *slog.Logger) {

	s := egress.NuevaSonda(clk, b, timeout)

	// Alinear al reloj de pared: los brazos de 60 s tienen que disparar en el
	// mismo segundo, como producción. Desfasados, un blip que le pega a todos a
	// la vez se leería como fallas independientes.
	select {
	case <-ctx.Done():
		return
	case <-time.After(egress.EsperaHastaAlinear(clk.Now(), b.Cadencia)):
	}

	tick := time.NewTicker(b.Cadencia)
	defer tick.Stop()
	for {
		tanda(ctx, s, destinos, esc, log)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// tanda mide los destinos en paralelo para que todos salgan en el mismo
// instante, igual que el tick de producción.
func tanda(ctx context.Context, s *egress.Sonda, destinos []egress.Destino, esc *escritor, log *slog.Logger) {
	var wg sync.WaitGroup
	for _, d := range destinos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := s.Medir(ctx, d)

			// Si nos están parando, el error es nuestro y no de la red:
			// escribirlo metería fallas inventadas en el experimento.
			if ctx.Err() != nil {
				return
			}
			if err := esc.escribir(r); err != nil {
				log.Error("no se pudo escribir el registro", "err", err)
			}
			if r.Clase.EsFalla() {
				log.Warn("falla",
					"brazo", r.Brazo, "destino", r.Destino, "clase", string(r.Clase),
					"total_ms", r.TotalMs, "reusado", r.Reusado, "ocio_ms", r.OcioMs,
					"remota", r.Remota, "err", r.Error)
			}
		}()
	}
	wg.Wait()
}

type escritor struct {
	mu sync.Mutex
	n  int
	// El encoder escribe directo al archivo y se hace flush en cada línea: son
	// 25 intentos por minuto, y perder la cola por un buffer sin vaciar es
	// perder justo las fallas del momento en que algo se rompió.
	enc *json.Encoder
}

func (e *escritor) escribir(r egress.Registro) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n++
	return e.enc.Encode(r)
}

func (e *escritor) escritos() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

func correrAnalisis(ruta string) error {
	f, err := os.Open(ruta)
	if err != nil {
		return err
	}
	defer f.Close()

	rs, rotas, err := egress.LeerJSONL(f)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		return fmt.Errorf("%s no tiene ni un registro legible", ruta)
	}
	fmt.Print(egress.Informe(rs, rotas))
	return nil
}
