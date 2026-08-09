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
	"github.com/juanandresdavila/server-status/internal/collector/docker"
	"github.com/juanandresdavila/server-status/internal/collector/host"
	"github.com/juanandresdavila/server-status/internal/config"
	"github.com/juanandresdavila/server-status/internal/model"
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
	case "containers":
		return listarContainers(cfg)
	case "run", "":
		return correr(cfg, col)
	default:
		return fmt.Errorf("comando desconocido %q: usá 'sample', 'containers' o 'run'", comando)
	}
}

func listarContainers(cfg config.Config) error {
	cli := docker.New(cfg.DockerSocket)
	cs, err := cli.Recolectar(context.Background(), cfg.DockerConcurrencia)
	if err != nil {
		return err
	}
	const mib = 1024 * 1024
	fmt.Printf("%-24s %-12s %-10s %8s %10s %s\n", "NOMBRE", "ESTADO", "SALUD", "CPU%", "MEM", "REINICIOS")
	for _, c := range cs {
		fmt.Printf("%-24s %-12s %-10s %7.1f%% %8.0f M %d\n",
			c.Name, c.State, c.Health, c.CPUPct, float64(c.MemBytes)/mib, c.Restarts)
	}
	return nil
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
	// GiB, no GB: son 1024³. df -h y free -h usan la misma unidad, así que los
	// números se comparan directo. Ojo que df -h redondea para arriba —
	// 15,01 GiB lo muestra como "16G" — y eso no es una diferencia real.
	const gib = 1024 * 1024 * 1024
	fmt.Printf("cpu    %.1f%%\n", m.CPUPctAvg)
	fmt.Printf("load   %.2f %.2f %.2f\n", m.Load1, m.Load5, m.Load15)
	fmt.Printf("mem    %.1f / %.1f GiB\n", float64(m.MemUsedBytes)/gib, float64(m.MemTotalBytes)/gib)
	fmt.Printf("swap   %.2f / %.1f GiB\n", float64(m.SwapUsedBytes)/gib, float64(m.SwapTotalBytes)/gib)
	fmt.Printf("disco  %.1f / %.1f GiB\n", float64(m.DiskUsedBytes)/gib, float64(m.DiskTotalBytes)/gib)
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

	cli := docker.New(cfg.DockerSocket)

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

			cs, err := cli.Recolectar(ctx, cfg.DockerConcurrencia)
			if err != nil {
				slog.Error("no se pudieron recolectar los containers", "err", err)
				continue
			}
			ms := make([]model.ContainerSample, 0, len(cs))
			for _, c := range cs {
				ms = append(ms, model.ContainerSample{
					TS: m.TS, Name: c.Name, State: c.State, Health: c.Health,
					Restarts: c.Restarts, CPUPct: c.CPUPct, MemBytes: c.MemBytes,
				})
			}
			if err := s.InsertContainerSamples(ms); err != nil {
				slog.Error("no se pudieron guardar los containers", "err", err)
			}
		}
	}
}
