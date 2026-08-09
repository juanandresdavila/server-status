package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/juanandresdavila/server-status/internal/watchdog"
	"github.com/juanandresdavila/server-status/internal/web"
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

	// El panel se ata en su propia goroutine y nunca es fatal: la IP de
	// tailnet puede no existir todavía al arrancar la máquina, y un monitor
	// sin panel sigue sirviendo — uno muerto no.
	// El webhook va en un listener APARTE del panel: para que comm-tool lo
	// alcance desde su container hay que abrir el puerto a la subred de
	// Docker, y abrir el del panel —que muestra todo— sería regalar superficie.
	if cfg.WebhookAddr != "" {
		wh := web.NuevoWebhook(os.Getenv("DELIVERY_SECRET_STATUS"),
			&manejadorDeComandos{store: s, notificador: notificador})
		mux := http.NewServeMux()
		mux.Handle("POST /webhooks/comm-tool", wh)
		go func() {
			if err := web.EscucharComo("webhook", cfg.WebhookAddr, mux, 2*time.Minute); err != nil {
				slog.Error("el webhook no pudo levantar", "err", err)
			}
		}()
		if os.Getenv("DELIVERY_SECRET_STATUS") == "" {
			slog.Warn("webhook SIN secreto: va a rechazar todo hasta que se cargue DELIVERY_SECRET_STATUS")
		}
	}

	if cfg.PanelAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/api/tail", web.NuevoTail(&seguidorDocker{cli: cli}))
		mux.Handle("/", web.NuevoPanel(s))
		go func() {
			if err := web.Escuchar(cfg.PanelAddr, mux, 2*time.Minute); err != nil {
				slog.Error("el panel no pudo levantar", "err", err)
			}
		}()
	} else {
		slog.Warn("panel apagado: falta panel_addr en la config")
	}

	wd := watchdog.New(cfg.URLPublica, os.Getenv("HEALTHCHECKS_PING_URL"), clock.Real{}, 3*time.Minute)
	if !wd.Configurado() {
		slog.Warn("watchdog apagado: falta HEALTHCHECKS_PING_URL")
	}

	portada := time.NewTicker(30 * time.Second)
	defer portada.Stop()
	latido := time.NewTicker(5 * time.Minute)
	defer latido.Stop()

	var a agregador
	var recordatorio recordatorioDiario
	var limpieza recordatorioDiario
	slog.Info("server-status arrancó", "base", cfg.Base, "muestreo", cfg.IntervaloMuestreo)

	for {
		select {
		case <-ctx.Done():
			slog.Info("saliendo")
			return nil

		case <-portada.C:
			if cfg.PortadaPath == "" {
				continue
			}
			if err := escribirPortada(s, cfg.PortadaPath); err != nil {
				slog.Error("no se pudo escribir la portada pública", "err", err)
			}

		case <-latido.C:
			if err := wd.Latir(ctx); err != nil {
				// No latir ES la señal: Healthchecks avisa al vencer la gracia.
				slog.Error("no se pudo latir", "err", err)
			}

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
			ingerirLogs(ctx, cli, s, cs)

			conteos := map[string]rules.ConteoLog{}
			if cfg.LogsPatron != "" {
				desde := m.TS.Add(-time.Duration(cfg.LogsVentanaMin) * time.Minute)
				for _, c := range cs {
					n, muestra, err := s.ContarCoincidencias(c.Name, cfg.LogsPatron, desde)
					if err != nil {
						slog.Error("no se pudieron contar coincidencias", "container", c.Name, "err", err)
						continue
					}
					conteos[c.Name] = rules.ConteoLog{Coincidencias: n, Muestra: muestra}
				}
			}
			deLogs, err := motor.EvaluarLogs(conteos)
			if err != nil {
				slog.Error("falló el motor de reglas sobre los logs", "err", err)
			}

			deContainers, err := motor.EvaluarContainers(ms)
			if err != nil {
				slog.Error("falló el motor de reglas sobre los containers", "err", err)
			}

			// Loguear los cambios sirve para diagnosticar sin abrir la base.
			// Los avisos NO salen de acá: se derivan de la base, así que una
			// caída entre abrir el incidente y mandar el mensaje se recupera
			// sola en el tick siguiente.
			todos := append(append(append(cambios, deHost...), deContainers...), deLogs...)
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

			// /silenciar cambia esto entre ticks.
			aplicarSilencio(s, notificador)

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

			if limpieza.toca(clock.Real{}.Now().In(loc), 4) {
				corte := clock.Real{}.Now().AddDate(0, 0, -cfg.LogsRetencionDias)
				if err := s.BorrarLogsAnterioresA(corte); err != nil {
					slog.Error("no se pudo aplicar la retención de logs", "err", err)
				} else {
					slog.Info("retención de logs aplicada", "corte", corte.Format(time.DateOnly))
				}

				// Después de la retención y ANTES de que el restic de la Mac
				// mini corra a las 04:30: la copia tiene que ser de la base ya
				// podada, y consistente.
				if cfg.BackupPath != "" {
					if err := s.VacuumInto(cfg.BackupPath); err != nil {
						slog.Error("no se pudo dejar la copia para el backup", "err", err)
					} else {
						slog.Info("copia consistente lista para el backup", "ruta", cfg.BackupPath)
					}
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

// escribirPortada arma el EstadoPublico y lo deja en disco para que lo sirva
// Caddy. Los tipos de web son deliberadamente pobres: la lista blanca se hace
// con el tipo, no con la disciplina de quien escribe la plantilla.
func escribirPortada(s *store.Store, destino string) error {
	probes, err := s.UltimoEstadoProbes()
	if err != nil {
		return err
	}
	ahora := clock.Real{}.Now()
	uptime, err := s.UptimePorServicio(ahora.AddDate(0, 0, -30))
	if err != nil {
		return err
	}

	e := web.EstadoPublico{Generado: ahora.UTC()}
	for _, p := range probes {
		pct, hayDatos := uptime[p.Servicio]
		if !hayDatos {
			pct = 100
		}
		e.Servicios = append(e.Servicios, web.ServicioPublico{
			Nombre: p.Servicio, OK: p.OK, UptimePct: pct,
		})
	}
	return web.EscribirPublica(destino, e)
}

// ingerirLogs trae lo nuevo de cada container y avanza su cursor.
//
// Un container sin cursor arranca desde AHORA y no desde su historia: traer
// los días que Docker conserve en el primer tick sería un pico inútil.
// Es la invariante 10 del spec de la fase 8.
func ingerirLogs(ctx context.Context, cli *docker.Client, s *store.Store, cs []docker.Container) {
	ahora := clock.Real{}.Now()

	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		desde, hay, err := s.CursorDeLog(c.Name)
		if err != nil {
			slog.Error("no se pudo leer el cursor de logs", "container", c.Name, "err", err)
			continue
		}
		if !hay {
			if err := s.GuardarCursorDeLog(c.Name, ahora); err != nil {
				slog.Error("no se pudo inicializar el cursor", "container", c.Name, "err", err)
			}
			continue
		}

		lineas, err := cli.Logs(ctx, c.ID, desde)
		if err != nil {
			if !docker.EsApagado(ctx) {
				slog.Warn("no se pudieron leer los logs", "container", c.Name, "err", err)
			}
			continue
		}
		if len(lineas) == 0 {
			continue
		}

		ms, ultima := nuevasLineas(lineas, desde, c.Name)
		if len(ms) == 0 {
			continue
		}
		if err := s.InsertLogs(ms); err != nil {
			slog.Error("no se pudieron guardar los logs", "container", c.Name, "err", err)
			continue
		}
		if err := s.GuardarCursorDeLog(c.Name, ultima); err != nil {
			slog.Error("no se pudo avanzar el cursor", "container", c.Name, "err", err)
		}
	}
}

// seguidorDocker adapta el cliente de Docker a lo que el tail necesita.
//
// Traduce nombre → id, porque el panel trabaja con nombres (que son estables)
// y la API de Docker con ids (que cambian en cada recreación del container).
type seguidorDocker struct{ cli *docker.Client }

func (s *seguidorDocker) Seguir(ctx context.Context, nombre string, out chan<- model.LineaLog) error {
	cs, err := s.cli.List(ctx)
	if err != nil {
		return err
	}
	var id string
	for _, c := range cs {
		if c.Name == nombre {
			id = c.ID
			break
		}
	}
	if id == "" {
		return fmt.Errorf("no hay ningún container llamado %q", nombre)
	}

	crudas := make(chan docker.LineaLog, 64)
	errc := make(chan error, 1)
	go func() { errc <- s.cli.SeguirLogs(ctx, id, crudas) }()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case l := <-crudas:
			select {
			case out <- model.LineaLog{TS: l.TS, Container: nombre, Stream: l.Stream, Linea: l.Linea}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// nuevasLineas filtra lo que ya se ingirió y devuelve el cursor nuevo.
//
// La comparación es con precisión de NANOSEGUNDOS, y por eso el cursor se
// persiste igual: guardarlo en segundos hacía que toda línea con fracción
// —12:00:00.5 contra un cursor 12:00:00— quedara "después" y se re-ingiriera
// en cada tick, para siempre.
//
// Docker igual filtra `since` con resolución de segundo, así que la última
// tanda vuelve a llegar: este filtro es el que la descarta.
func nuevasLineas(crudas []docker.LineaLog, desde time.Time, container string) ([]model.LineaLog, time.Time) {
	out := make([]model.LineaLog, 0, len(crudas))
	ultima := desde
	for _, l := range crudas {
		if !l.TS.After(desde) {
			continue
		}
		out = append(out, model.LineaLog{
			TS: l.TS, Container: container, Stream: l.Stream, Linea: l.Linea,
		})
		if l.TS.After(ultima) {
			ultima = l.TS
		}
	}
	return out, ultima
}
