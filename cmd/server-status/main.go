package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/collector/host"
	"github.com/juanandresdavila/server-status/internal/config"
	"github.com/juanandresdavila/server-status/internal/store"
)

func main() {
	rutaConfig := flag.String("config", "/etc/server-status/config.yaml", "ruta del archivo de configuración")
	flag.Parse()

	if err := run(*rutaConfig, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(rutaConfig, comando string) error {
	cfg, err := config.Load(rutaConfig)
	if err != nil {
		return err
	}
	col := host.NewCollector(cfg.Proc, cfg.Disco, clock.Real{})

	switch comando {
	case "sample":
		return sampleUnaVez(col)
	case "run", "":
		return correr(cfg, col)
	default:
		return fmt.Errorf("comando desconocido %q: usá 'sample' o 'run'", comando)
	}
}

// sampleUnaVez imprime una muestra por stdout. Sirve para comparar a ojo contra
// free -h, df -h y uptime en el servidor, sin tocar la base.
func sampleUnaVez(col *host.Collector) error {
	// La primera lectura no tiene con qué comparar la CPU, así que se descarta.
	if _, err := col.Sample(); err != nil {
		return err
	}
	time.Sleep(time.Second)

	m, err := col.Sample()
	if err != nil {
		return err
	}
	const gb = 1024 * 1024 * 1024
	fmt.Printf("cpu    %.1f%%\n", m.CPUPctAvg)
	fmt.Printf("load   %.2f %.2f %.2f\n", m.Load1, m.Load5, m.Load15)
	fmt.Printf("mem    %.1f / %.1f GB\n", float64(m.MemUsedBytes)/gb, float64(m.MemTotalBytes)/gb)
	fmt.Printf("swap   %.2f / %.1f GB\n", float64(m.SwapUsedBytes)/gb, float64(m.SwapTotalBytes)/gb)
	fmt.Printf("disco  %.1f / %.1f GB\n", float64(m.DiskUsedBytes)/gb, float64(m.DiskTotalBytes)/gb)
	fmt.Printf("red    rx %d  tx %d\n", m.NetRxBytes, m.NetTxBytes)
	fmt.Printf("uptime %s\n", m.Uptime.Round(time.Second))
	return nil
}

func correr(cfg config.Config, col *host.Collector) error {
	s, err := store.Open(cfg.Base)
	if err != nil {
		return err
	}
	defer s.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	muestreo := time.NewTicker(cfg.IntervaloMuestreo)
	defer muestreo.Stop()
	persistencia := time.NewTicker(cfg.IntervaloPersistencia)
	defer persistencia.Stop()

	var a agregador
	slog.Info("server-status arrancó", "base", cfg.Base, "muestreo", cfg.IntervaloMuestreo)

	for {
		select {
		case <-ctx.Done():
			slog.Info("saliendo")
			return nil

		case <-muestreo.C:
			m, err := col.Sample()
			if err != nil {
				// Un error de lectura no puede tumbar el proceso: es un
				// monitor, y morirse es justo lo que no tiene que hacer.
				slog.Error("no se pudo muestrear", "err", err)
				continue
			}
			a.add(m)

		case <-persistencia.C:
			m, ok := a.flush()
			if !ok {
				continue
			}
			if err := s.InsertHostSample(m); err != nil {
				slog.Error("no se pudo guardar la muestra", "err", err)
			}
		}
	}
}
