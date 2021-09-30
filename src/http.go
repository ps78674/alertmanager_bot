package main

import (
	"encoding/json"
	"log"
	"strconv"

	alerttmpl "github.com/prometheus/alertmanager/template"
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

		// send message as json
		if len(cfg.TemplatePath) == 0 {
			msg := tgbotapi.NewMessage(chatID, string(ctx.PostBody()))
			if e := sendMessage(bot, msg); e != nil {
				log.Printf("error sending message: %s", e)
			}

			return
		}

		data := alerttmpl.Data{}
		err = json.Unmarshal(ctx.PostBody(), &data)
		if err != nil {
			log.Printf("error unmarshalling post data: %s", err)
			return
		}

		// send templated message
		s, err := applyTemplate(data)
		if err != nil {
			log.Println(err)
			return
		}

		msg := tgbotapi.NewMessage(chatID, s)
		msg.ParseMode = tgbotapi.ModeHTML
		if e := sendMessage(bot, msg); e != nil {
			log.Printf("error sending message: %s", e)
		}
	default:
		log.Printf("wrong path %s", ctxPath)
	}
}
