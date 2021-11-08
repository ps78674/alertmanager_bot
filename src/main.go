package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ReneKroon/ttlcache/v2"
	"github.com/go-openapi/strfmt"
	"github.com/kelseyhightower/envconfig"
	"github.com/prometheus/alertmanager/api/v2/client"
	"github.com/prometheus/alertmanager/api/v2/client/general"
	"github.com/prometheus/client_golang/api"
	"github.com/ps78674/docopt.go"
	"github.com/valyala/fasthttp"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
	"gopkg.in/yaml.v3"
)

var cfg struct {
	ConfigFile                 string        `envconfig:"CONFIG_PATH" docopt:"--config"`
	TelegramToken              string        `envconfig:"TELEGRAM_TOKEN" yaml:"telegram_token"`
	AlermanagerURL             string        `envconfig:"ALERTMANAGER_URL" yaml:"alertmanager_url" default:"http://localhost:9093"`
	PrometheusURL              string        `envconfig:"PROMETHEUS_URL" yaml:"prometheus_url" default:"http://localhost:9090"`
	APITimeout                 time.Duration `envconfig:"API_TIMEOUT" yaml:"api_timeout" default:"10s"`
	KeyboardRows               int           `envconfig:"KEYBOARD_ROWS" yaml:"keyboard_rows" default:"2"`
	WebhookAlertsTemplatePath  string        `envconfig:"WEBHOOK_ALERTS_TEMPLATE_PATH" yaml:"webhook_alerts_template_path"`
	GettableAlertsTemplatePath string        `envconfig:"GETTABLE_ALERTS_TEMPLATE_PATH" yaml:"gettable_alerts_template_path"`
	SilencesTemplatePath       string        `envconfig:"SILENCES_TEMPLATE_PATH" yaml:"silences_template_path"`
	BindAddress                string        `envconfig:"BIND_ADDRESS" yaml:"bind_address" default:"0.0.0.0"`
	BindPort                   int           `envconfig:"BIND_PORT" yaml:"bind_port" default:"8088"`
	DisableHTTP                bool          `envconfig:"DISABLE_HTTP" yaml:"disable_http" default:"false"`
	LogFile                    string        `envconfig:"LOGFILE_PATH" yaml:"logfile_path"`
	Users                      []string      `envconfig:"USERS" yaml:"users"`
	TimeFormat                 string        `envconfig:"TIMEFORMAT" yaml:"time_format" default:"02/01/2006 15:04:05"`
	TimeZone                   string        `envconfig:"TIMEZONE" yaml:"time_zone" default:"Europe/Moscow"`
	ButtonPrefixOK             string        `envconfig:"BUTTON_PREFIX_OK" yaml:"button_prefix_ok"`
	ButtonPrefixFail           string        `envconfig:"BUTTON_PREFIX_FAIL" yaml:"button_prefix_fail"`
	SendMessageRetryCount      int           `envconfig:"SEND_MESSAGE_RETRY_COUNT" yaml:"send_message_retry_count" default:"3"`
	SilenceDuration            time.Duration `envconfig:"SILENCE_DURATION" yaml:"silence_duration" default:"1h"`
}

var (
	versionString = "devel"
	programName   = filepath.Base(os.Args[0])
)

var usage = fmt.Sprintf(`%[1]s: telegram bot for alertmanager

Usage:
  %[1]s [ -c <CONFIGPATH> ]

Options:
  -c, --config <STRING>  config file path [env: CONFIG_PATH]

  -h, --help             show this screen
  --version              show version
`, programName)

func init() {
	// populate config from ENV first
	err := envconfig.Process("", &cfg)
	if err != nil {
		fmt.Printf("error processing env vars: %s\n", err)
		os.Exit(1)
	}

	// parse cli options
	opts, err := docopt.ParseArgs(usage, nil, versionString)
	if err != nil {
		fmt.Printf("error parsing options: %s\n", err)
		os.Exit(1)
	}

	if e := opts.Bind(&cfg); e != nil {
		fmt.Printf("error parsing options: %s\n", e)
		os.Exit(1)
	}

	// read config from file
	if len(cfg.ConfigFile) > 0 {
		f, err := os.Open(cfg.ConfigFile)
		if err != nil {
			fmt.Printf("error opening config file: %s\n", err)
			os.Exit(1)
		}
		defer f.Close()

		decoder := yaml.NewDecoder(f)
		err = decoder.Decode(&cfg)
		if err != nil {
			fmt.Printf("error parsing config file: %s\n", err)
			os.Exit(1)
		}
	}

	// telegram bot token must be set
	// either via env var, cli, or config file
	if len(cfg.TelegramToken) == 0 {
		fmt.Println("telegram token is not set, aborting")
		os.Exit(1)
	}
}

func main() {
	// setup logging
	log.SetFlags(0)
	if len(cfg.LogFile) > 0 {
		f, err := os.OpenFile(cfg.LogFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("error opening logfile: %s\n", err)
		}

		defer f.Close()
		log.SetOutput(f)
		log.SetFlags(log.LstdFlags)
	}

	log.Printf("starting telegram bot for alertmanager version %s", versionString)

	// telegram bot api client
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		log.Fatalf("error creating new BotAPI: %s\n", err)
	}

	url, err := url.Parse(cfg.AlermanagerURL)
	if err != nil {
		log.Fatalf("error parsing --url: %s\n", err)
	}

	alertmanagerPath := url.Path
	if !strings.HasSuffix(alertmanagerPath, "/api/v2") {
		alertmanagerPath = path.Join(alertmanagerPath, "/api/v2")
	}

	// alertmanager client
	alertCli := client.NewHTTPClientWithConfig(
		strfmt.Default,
		client.DefaultTransportConfig().
			WithSchemes([]string{url.Scheme}).
			WithHost(url.Host).
			WithBasePath(alertmanagerPath),
	)

	_, err = alertCli.General.GetStatus(general.NewGetStatusParams())
	if err != nil {
		log.Fatalf("error creating alertmanager client: %s\n", err)
	}

	promCli, err := api.NewClient(api.Config{
		Address: cfg.PrometheusURL,
	})
	if err != nil {
		fmt.Printf("Error creating client: %v\n", err)
		os.Exit(1)
	}

	cache := ttlcache.NewCache()
	defer cache.Close()

	tgBot := TelegramBot{
		BotAPI:       bot,
		Alertmanager: alertCli,
		Prometheus:   promCli,
		Cache:        cache,
		StartTime:    time.Now(),
	}
	go handleUpdates(&tgBot)

	// http server
	srv := fasthttp.Server{}
	if !cfg.DisableHTTP {
		listenOn := fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.BindPort)
		log.Printf("starting http server on '%s'", listenOn)

		srv.Handler = func(ctx *fasthttp.RequestCtx) {
			handleHTTP(ctx, &tgBot)
		}

		go func() {
			if err := srv.ListenAndServe(listenOn); err != nil {
				log.Fatalf("http server error: %s", err)
			}
		}()
	}

	// graceful stop on CTRL+C / SIGINT / SIGTERM
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	srv.Shutdown()
	close(ch)
}
