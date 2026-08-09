package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/juanandresdavila/server-status/internal/clock"
	"github.com/juanandresdavila/server-status/internal/collector/docker"
	"github.com/juanandresdavila/server-status/internal/collector/host"
	"github.com/juanandresdavila/server-status/internal/config"
	"github.com/juanandresdavila/server-status/internal/model"
	"github.com/juanandresdavila/server-status/internal/notify"
	"github.com/juanandresdavila/server-status/internal/notify/commtool"
	"github.com/juanandresdavila/server-status/internal/notify/telegram"
	"github.com/juanandresdavila/server-status/internal/prober"
	"github.com/juanandresdavila/server-status/internal/rules"
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
	case "incidents":
		return listarIncidentes(cfg)
	case "run", "":
		return correr(cfg, col)
	default:
		return fmt.Errorf("comando desconocido %q: usá 'sample', 'containers', 'incidents' o 'run'", comando)
	}
}

func listarIncidentes(cfg config.Config) error {
	s, err := store.Open(cfg.Base)
	if err != nil {
		return err
	}
	defer s.Close()

	is, err := s.UltimosIncidentes(20)
	if err != nil {
		return err
	}
	if len(is) == 0 {
		fmt.Println("sin incidentes registrados")
		return nil
	}
	for _, i := range is {
		estado := "ABIERTO"
		if i.CerradoEn != nil {
			estado = "cerrado " + i.CerradoEn.Local().Format("15:04")
		}
		fmt.Printf("%-14s %-28s %-9s %s  (%s)\n",
			estado, i.Sujeto, i.Severidad,
			i.AbiertoEn.Local().Format("02/01 15:04"), i.Detalle)
	}
	return nil
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

	loc, err := time.LoadLocation(cfg.Zona)
	if err != nil {
		return err
	}

	cli := docker.New(cfg.DockerSocket)
	pr := prober.New(clock.Real{}, cfg.ProbeTimeout)
	motor := rules.NewMotor(s, clock.Real{}, rules.Defaults())

	// Los secretos vienen del entorno, nunca de la config ni de la base.
	// Invariante 8 del spec.
	canalCT := commtool.New(cfg.CommToolURL, os.Getenv("COMM_TOOL_API_KEY"), cfg.CommToolUserID)
	canalTG := telegram.New(cfg.TelegramAPI, os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID"))
	notificador := notify.NewNotificador(canalCT, canalTG, s, clock.Real{})

	// Arrancar sin canales es válido a propósito: un monitor que no levanta
	// porque no puede avisar es peor que uno que avisa a medias. Pero lo grita.
	switch {
	case !canalCT.Configurado() && !canalTG.Configurado():
		slog.Warn("NINGÚN canal de avisos configurado: los incidentes solo van al log")
	case !canalCT.Configurado():
		slog.Warn("comm-tool sin configurar: los avisos salen por Telegram directo")
	case !canalTG.Configurado():
		slog.Warn("sin respaldo directo: si comm-tool se cae, ese aviso no sale")
	}

	var a agregador
	var recordatorio recordatorioDiario
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

			// Probes: uno por servicio, todos en paralelo. Son cuatro requests
			// a internet y cada uno puede tardar hasta ProbeTimeout; en serie
			// no entrarían en el ciclo del minuto.
			resultados := make([]model.ProbeResult, len(cfg.Servicios))
			var wg sync.WaitGroup
			for i, srv := range cfg.Servicios {
				wg.Add(1)
				go func(i int, srv config.Servicio) {
					defer wg.Done()
					resultados[i] = pr.Probe(ctx, srv.Nombre, srv.Probe, srv.EstadoEsperado)
				}(i, srv)
			}
			wg.Wait()

			if err := s.InsertProbeResults(resultados); err != nil {
				slog.Error("no se pudieron guardar los probes", "err", err)
			}

			cambios, err := motor.EvaluarProbes(resultados)
			if err != nil {
				slog.Error("falló el motor de reglas sobre los probes", "err", err)
			}
			deHost, err := motor.EvaluarHost(m)
			if err != nil {
				slog.Error("falló el motor de reglas sobre el host", "err", err)
			}
			deContainers, err := motor.EvaluarContainers(ms)
			if err != nil {
				slog.Error("falló el motor de reglas sobre los containers", "err", err)
			}

			// Loguear los cambios sirve para diagnosticar sin abrir la base.
			// Los avisos NO salen de acá: se derivan de la base, así que una
			// caída entre abrir el incidente y mandar el mensaje se recupera
			// sola en el tick siguiente.
			todos := append(append(cambios, deHost...), deContainers...)
			for _, c := range todos {
				slog.Info("incidente",
					"transicion", c.Tipo.String(),
					"sujeto", c.Incidente.Sujeto,
					"severidad", c.Incidente.Severidad,
					"detalle", c.Incidente.Detalle)
			}

			// El aviso de flapeo no tiene fila en incidents, así que no lo
			// puede levantar AvisosPendientes: se manda acá.
			for _, c := range todos {
				if c.Incidente.Tipo != "flapping" {
					continue
				}
				id := fmt.Sprintf("flap:%s:%d", c.Incidente.Sujeto, c.Incidente.ID)
				if err := notificador.AvisarTexto(ctx, id, notify.Texto(model.Aviso{Incidente: c.Incidente})); err != nil {
					slog.Error("no se pudo avisar la inestabilidad", "err", err)
				}
			}

			pendientes, err := s.AvisosPendientes()
			if err != nil {
				slog.Error("no se pudieron leer los avisos pendientes", "err", err)
			}
			for _, av := range pendientes {
				if err := notificador.Avisar(ctx, av); err != nil {
					// No se marca nada: el tick siguiente lo reintenta.
					slog.Error("no se pudo entregar el aviso", "delivery", av.DeliveryID, "err", err)
				}
			}

			if recordatorio.toca(clock.Real{}.Now().In(loc), cfg.HoraResumen) {
				if err := mandarResumen(ctx, s, notificador, m, resultados); err != nil {
					slog.Error("no se pudo mandar el resumen diario", "err", err)
				}
			}
		}
	}
}

func mandarResumen(ctx context.Context, s *store.Store, n *notify.Notificador,
	h model.HostSample, probes []model.ProbeResult) error {

	ahora := clock.Real{}.Now()
	cuantos, err := s.IncidentesDesde(ahora.Add(-24 * time.Hour))
	if err != nil {
		return err
	}
	texto := notify.TextoResumen(armarResumen(h, probes, cuantos))

	// El resumen no es un incidente: no tiene fila en incidents y su id de
	// entrega es la fecha, que lo hace único por día.
	return n.AvisarTexto(ctx, "resumen:"+ahora.Format("2006-01-02"), texto)
}
