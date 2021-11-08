package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/prometheus/alertmanager/api/v2/client/alert"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/segmentio/ksuid"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
	"gopkg.in/tucnak/telebot.v2"
)

const maxMessageTextLength = 4096

var tmplFuncMap = template.FuncMap{
	"ToUpper":    strings.ToUpper,
	"ToLower":    strings.ToLower,
	"KindOf":     KindOf,
	"FormatDate": FormatDate,
}

func KindOf(in interface{}) string {
	return reflect.TypeOf(in).Kind().String()
}

func FormatDate(in interface{}) (out string) {
	var t time.Time
	switch in := in.(type) {
	case time.Time:
		t = in
	case *strfmt.DateTime:
		t = time.Time(*in)
	}

	loc, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		log.Printf("error loading timezone %s: %s\n", cfg.TimeZone, err)
		return
	}

	out = t.In(loc).Format(cfg.TimeFormat)
	return
}

func applyTemplate(in interface{}, templatePath string) (string, error) {
	tmpl, err := template.New(path.Base(templatePath)).Funcs(tmplFuncMap).ParseFiles(templatePath)
	if err != nil {
		log.Printf("error loading template file: %s", err)
		return "", err
	}

	b := bytes.Buffer{}
	w := io.Writer(&b)
	err = tmpl.Execute(w, in)
	if err != nil {
		log.Printf("error executing template: %s", err)
		return "", err
	}

	return b.String(), nil
}

func newJobsKB(bot *TelegramBot) (kb tgbotapi.InlineKeyboardMarkup, e error) {
	// api call timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.APITimeout)
	defer cancel()

	v1api := v1.NewAPI(bot.Prometheus)
	labels, _, err := v1api.LabelValues(ctx, "job", []string{}, time.Now().Add(-time.Minute), time.Now())
	if err != nil {
		e = fmt.Errorf("error getting jobs: %s", err)
		return
	}

	r := tgbotapi.NewInlineKeyboardRow()
	for _, l := range labels {
		al, err := bot.Alertmanager.Alert.GetAlerts(&alert.GetAlertsParams{
			Filter:  []string{"job=" + string(l)},
			Context: ctx,
		})
		if err != nil {
			// e = fmt.Errorf("error getting alerts for job '%s': %s", string(l), e)
			// return
			log.Printf("error getting alerts for job '%s': %s", string(l), err)
			continue
		}

		var btnLabel string
		if len(al.GetPayload()) == 0 {
			btnLabel = cfg.ButtonPrefixOK + string(l)
		} else {
			btnLabel = cfg.ButtonPrefixFail + string(l)
		}

		// create new cache entry
		cacheID := ksuid.New().String()
		newCallback := Callback{
			Type: "job",
			Data: make(map[string]string),
		}
		newCallback.Data["job_name"] = string(l)
		bot.Cache.Set(cacheID, newCallback)

		r = append(r, tgbotapi.NewInlineKeyboardButtonData(btnLabel, cacheID))
		if len(r) == cfg.KeyboardRows {
			kb.InlineKeyboard = append(kb.InlineKeyboard, r)
			r = tgbotapi.NewInlineKeyboardRow()
		}
	}

	if len(r) > 0 {
		kb.InlineKeyboard = append(kb.InlineKeyboard, r)
	}

	// create new cache entry
	cacheID := ksuid.New().String()
	newCallback := Callback{
		Type: "close",
	}
	bot.Cache.Set(cacheID, newCallback)

	// button with request to delete message (close menu)
	kb.InlineKeyboard = append(kb.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Close menu", cacheID)))

	return
}

func newTargetsKB(bot *TelegramBot, jobName string) (kb tgbotapi.InlineKeyboardMarkup, e error) {
	// api call timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.APITimeout)
	defer cancel()

	v1api := v1.NewAPI(bot.Prometheus)
	targets, err := v1api.Targets(ctx)
	if err != nil {
		e = fmt.Errorf("error getting targets data: %s", err)
		return
	}

	// sort targets by instance label
	sort.Slice(targets.Active, func(i, j int) bool {
		return targets.Active[i].Labels["instance"] < targets.Active[j].Labels["instance"]
	})

	r := tgbotapi.NewInlineKeyboardRow()
	for _, t := range targets.Active {
		if len(t.Labels["job"]) == 0 || len(t.Labels["instance"]) == 0 {
			b, err := json.Marshal(t)
			if err != nil {
				// return fmt.Errorf("could not get labels [job, instance] for target '%v'", t)
				log.Printf("could not get labels [job, instance] for target '%v'", t)
				continue
			}
			// return fmt.Errorf("could not get labels [job, instance] for target '%s'", string(b))
			log.Printf("could not get labels [job, instance] for target '%s'", string(b))
		}

		// target job differs from the one requested in callback, skipping
		if string(t.Labels["job"]) != jobName {
			continue
		}

		al, err := bot.Alertmanager.Alert.GetAlerts(&alert.GetAlertsParams{
			Filter:  []string{"instance=" + string(t.Labels["instance"])},
			Context: ctx,
		})
		if err != nil {
			// return fmt.Errorf("error getting alerts for target '%s': %s", string(t.Labels["instance"]), err)
			log.Printf("error getting alerts for target '%s': %s", string(t.Labels["instance"]), err)
			continue
		}

		var btnLabel string
		if len(al.GetPayload()) == 0 {
			btnLabel = cfg.ButtonPrefixOK + string(t.Labels["instance"])
		} else {
			btnLabel = cfg.ButtonPrefixFail + string(t.Labels["instance"])
		}

		// create new cache entry
		cacheID := ksuid.New().String()
		newCallback := Callback{
			Type: "target",
			Data: make(map[string]string),
		}
		newCallback.Data["job_name"] = jobName
		newCallback.Data["target_name"] = string(t.Labels["instance"])
		bot.Cache.Set(cacheID, newCallback)

		r = append(r, tgbotapi.NewInlineKeyboardButtonData(btnLabel, cacheID))
		if len(r) == cfg.KeyboardRows {
			kb.InlineKeyboard = append(kb.InlineKeyboard, r)
			r = tgbotapi.NewInlineKeyboardRow()
		}
	}

	if len(r) > 0 {
		kb.InlineKeyboard = append(kb.InlineKeyboard, r)
	}

	return
}

func splitStringIntoChunks(text string) (chunks []string) {
	var chunk string
	splitted := strings.Split(text, "\n")

	for _, s := range splitted {
		if len(chunk+"\n"+s) > maxMessageTextLength {
			chunks = append(chunks, chunk)
			chunk = ""
		}
		chunk = chunk + "\n" + s
	}
	chunks = append(chunks, chunk)
	return
}

func sendMessage(bot *TelegramBot, c tgbotapi.Chattable) (err error) {
	send := func(m tgbotapi.Chattable) (err error) {
		for i := 0; i < cfg.SendMessageRetryCount; i++ {
			_, err = bot.BotAPI.Send(m)
			if err != nil {
				e, ok := err.(telebot.FloodError)
				if !ok {
					break
				}

				log.Printf("got FloodError, retrying in %d", e.RetryAfter)
				time.Sleep(time.Second * time.Duration(e.RetryAfter))
				continue
			}
			break
		}

		return
	}

	switch m := c.(type) {
	case tgbotapi.MessageConfig:
		chunks := splitStringIntoChunks(m.Text)
		for i, c := range chunks {
			msg := tgbotapi.NewMessage(m.ChatID, c)
			msg.ParseMode = m.ParseMode
			if i == len(chunks)-1 {
				msg.ReplyMarkup = m.ReplyMarkup
			}
			if err = send(msg); err != nil {
				return
			}
		}
	case tgbotapi.EditMessageTextConfig:
		chunks := splitStringIntoChunks(m.Text)
		if len(chunks) > 1 {
			msg := tgbotapi.NewDeleteMessage(m.ChatID, m.MessageID)
			if err = send(msg); err != nil {
				return
			}
			for i, c := range chunks {
				msg := tgbotapi.NewMessage(m.ChatID, c)
				msg.ParseMode = m.ParseMode
				if i == len(chunks)-1 {
					msg.ReplyMarkup = m.ReplyMarkup
				}
				if err = send(msg); err != nil {
					return
				}
			}
		} else {
			if err = send(m); err != nil {
				return
			}
		}
	case tgbotapi.DeleteMessageConfig:
		if err = send(m); err != nil {
			return
		}
	case tgbotapi.EditMessageReplyMarkupConfig:
		if err = send(m); err != nil {
			return
		}
	default:
		return fmt.Errorf("unsupported tgbotapi.Chattable type %T", c)
	}

	return
}
