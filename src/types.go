package main

import (
	"time"

	"github.com/ReneKroon/ttlcache/v2"
	"github.com/prometheus/alertmanager/api/v2/client"
	"github.com/prometheus/client_golang/api"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

type TelegramBot struct {
	BotAPI       *tgbotapi.BotAPI
	Alertmanager *client.Alertmanager
	Prometheus   api.Client
	Cache        ttlcache.SimpleCache
	StartTime    time.Time
}

type Callback struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}
