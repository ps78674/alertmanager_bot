package main

import (
	"encoding/json"
	"log"
	"strconv"

	alerttmpl "github.com/prometheus/alertmanager/template"
	"github.com/segmentio/ksuid"
	"github.com/valyala/fasthttp"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

func handleHTTP(ctx *fasthttp.RequestCtx, bot *TelegramBot) {
	log.Printf("new http connection from %s", ctx.RemoteAddr())

	switch ctxPath := string(ctx.Path()); ctxPath {
	case "/alerts":
		// only POST supported
		if !ctx.IsPost() {
			log.Printf("wrong http method %s", ctx.Method())
			return
		}

		log.Printf("new post data: %s", string(ctx.PostBody()))

		// get chat id from ?chaid=<INT>
		chatID, err := strconv.ParseInt(string(ctx.QueryArgs().Peek("chatid")), 10, 64)
		if err != nil {
			log.Printf("wrong chatid: %s", err)
			return
		}

		var msg tgbotapi.MessageConfig
		data := alerttmpl.Data{}
		err = json.Unmarshal(ctx.PostBody(), &data)
		if err != nil {
			log.Printf("error unmarshalling post data: %s", err)
			return
		}

		// send plain json if no template defined in config
		if len(cfg.WebhookAlertsTemplatePath) == 0 {
			msg = tgbotapi.NewMessage(chatID, string(ctx.PostBody()))
		} else {
			s, err := applyTemplate(data, cfg.WebhookAlertsTemplatePath)
			if err != nil {
				log.Println(err)
				return
			}

			msg = tgbotapi.NewMessage(chatID, s)
			msg.ParseMode = tgbotapi.ModeHTML
		}

		// add silence buttons
		// we want silence alerts by matching instance and alertname
		// so alertmanager grouping must be configured
		//     group_by: ['instance','alertname'])
		//
		// if neither 'instance' nor 'alertname' is found in alert.GroupLabels.Names()
		// the button will not be visible
		if data.Status == "firing" && len(data.GroupLabels["instance"]) > 0 && len(data.GroupLabels["alertname"]) > 0 {
			// create new cache entry
			cacheID := ksuid.New().String()
			newCallback := Callback{
				Type: "silence",
				Data: make(map[string]string),
			}
			newCallback.Data["instance"] = data.GroupLabels["instance"]
			newCallback.Data["alertname"] = data.GroupLabels["alertname"]
			bot.Cache.Set(cacheID, newCallback)

			row := tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Silence", cacheID))
			kb := tgbotapi.NewInlineKeyboardMarkup(row)

			msg.ReplyMarkup = &kb
		}

		if e := sendMessage(bot, msg); e != nil {
			log.Printf("error sending message: %s", e)
		}
	default:
		log.Printf("wrong path %s", ctxPath)
	}
}
