package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

const runtimeCPULimit = 3

func main() {
	// Keenetic MT7621 exposes four hardware threads. Keep the process limit
	// explicit and leave one thread available for ndm, networking and the UI.
	runtime.GOMAXPROCS(runtimeCPULimit)

	// Single combined process: protect from OOM killer at highest priority
	// (same as ndm, the router's own daemon). With sing-box embedded there is
	// only one Go runtime to protect instead of two.
	_ = os.WriteFile("/proc/self/oom_score_adj", []byte("-1000"), 0644)

	// Cap heap to 50 MB. The GC will stay aggressive to fit within this limit.
	// Two Go runtimes would have needed ~75 MB total; one runtime fits in ~50 MB.
	debug.SetMemoryLimit(50 * 1024 * 1024)

	// Raise fd limit — TPROXY opens two sockets per LAN connection.
	_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{
		Cur: 65536, Max: 65536,
	})

	// Raise kernel-wide fs.file-max. Keenetic default (~12 435) is too small
	// for a transparent proxy under load.
	_ = os.WriteFile("/proc/sys/fs/file-max", []byte("131072\n"), 0644)

	dataDir := flag.String("data-dir", "/opt/etc/vless-manager", "Config/data directory")
	flag.Parse()

	cfgPath := filepath.Join(*dataDir, "config.json")
	subPath := filepath.Join(*dataDir, "subscriptions.json")

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := initializeSubscriptionDeviceID(*dataDir); err != nil {
		log.Fatalf("initialize subscription device ID: %v", err)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	pm := NewProcessManager(*dataDir)
	pm.SetServiceLogLevel(cfg.Settings.ServiceLogLevel)

	subs, err := loadSubscriptions(subPath)
	if err != nil {
		pm.event(serviceLogWarn, "subscription", "load.failed",
			"файл подписок не загружен, используется пустой список",
			field("path", subPath),
			field("error", err))
		subs = nil
	}

	unsupported := pruneUnsupportedServers(cfg)
	stale := pruneStaleServers(cfg, subs)
	if unsupported > 0 || stale > 0 {
		pm.event(serviceLogInfo, "manager", "servers.pruned",
			"устаревшие или неподдерживаемые серверы удалены из конфигурации",
			field("unsupported", unsupported),
			field("stale", stale))
		if err := saveConfig(cfgPath, cfg); err != nil {
			pm.event(serviceLogError, "manager", "config.persist_failed",
				"не удалось сохранить очищенную конфигурацию",
				field("path", cfgPath),
				field("error", err))
		}
	}
	// loadSubscriptions also migrates IDs and excludes unsupported transports.
	if err := saveSubscriptions(subPath, subs); err != nil {
		pm.event(serviceLogError, "subscription", "persist.failed",
			"не удалось сохранить нормализованный список подписок",
			field("path", subPath),
			field("error", err))
	}
	if cfg.ActiveServer != "" && serverDisabledInSubscriptions(subs, cfg.ActiveServer) {
		pm.event(serviceLogWarn, "manager", "active_server.disabled",
			"выбранный сервер выключен в подписке, выбор очищен",
			field("server_id", cfg.ActiveServer))
		cfg.ActiveServer = ""
		if err := saveConfig(cfgPath, cfg); err != nil {
			pm.event(serviceLogError, "manager", "config.persist_failed",
				"не удалось сохранить сброс активного сервера",
				field("path", cfgPath),
				field("error", err))
		}
	}

	api := newAPIServer(pm, cfg, subs, cfgPath, subPath)
	go api.updater.runAutoChecks()

	go func() {
		st := api.settingsSnapshot()
		// Tunnel health is independent from operator-policy automation, so the
		// controller must stay alive even when automatic VPN on/off is disabled.
		go api.failover.Run()
		if cfg.AutoFailover {
			pm.event(serviceLogInfo, "manager", "failover.enabled",
				"автоматическое включение VPN по ограничениям оператора включено")
			return
		}
		if !cfg.Autostart {
			return
		}
		wanTimeout := st.WaitForWANTimeout()
		if !WaitForWAN(wanTimeout) {
			pm.event(serviceLogWarn, "manager", "autostart.wan_timeout",
				"WAN не появился, автоматический запуск пропущен",
				field("timeout_ms", wanTimeout.Milliseconds()))
			return
		}
		if _, err := api.prepareServerForStart(); err != nil {
			pm.event(serviceLogWarn, "manager", "autostart.no_server",
				"автоматический запуск пропущен: рабочий сервер не выбран",
				field("error", err))
			return
		}
		api.mu.RLock()
		cfgSnap := cloneConfig(api.cfg)
		api.mu.RUnlock()
		if err := api.startManagedVPN(cfgSnap); err != nil {
			pm.event(serviceLogError, "manager", "autostart.failed",
				"автоматический запуск VPN завершился ошибкой",
				field("error", err))
		}
	}()

	go func() {
		// Initial delay so manager finishes booting before first subscription
		// fetch. Settings.SubscriptionFirstDelayMin == 0 skips the wait.
		st := api.settingsSnapshot()
		if d := time.Duration(st.SubscriptionFirstDelayMin) * time.Minute; d > 0 {
			time.Sleep(d)
		}
		for {
			pm.event(serviceLogInfo, "subscription", "refresh_all.start",
				"начато фоновое обновление подписок")
			api.refreshAllSubscriptions()
			// Re-read settings every loop so a PATCH lands without restart.
			sleep := api.settingsSnapshot().SubscriptionRefreshInterval()
			if sleep <= 0 {
				sleep = time.Hour
			}
			time.Sleep(sleep)
		}
	}()

	go func() {
		time.Sleep(30 * time.Second)
		for {
			api.checkInternet("background")
			sleep := api.settingsSnapshot().InternetCheckInterval()
			if sleep <= 0 {
				sleep = time.Hour
			}
			time.Sleep(sleep)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/api/", api)

	webSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(webSub)))

	addr := fmt.Sprintf(":%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}

	pm.event(serviceLogInfo, "manager", "service.started",
		"веб-интерфейс готов",
		field("address", "0.0.0.0"+addr),
		field("data_dir", *dataDir))
	log.Printf("vless-manager listening on http://0.0.0.0%s", addr)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	if err := server.Serve(ln); err != nil {
		log.Fatal(err)
	}
}
