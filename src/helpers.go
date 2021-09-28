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
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/prometheus/alertmanager/api/v2/client/alert"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

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

func applyTemplate(in interface{}) (string, error) {
	tmpl, err := template.New(path.Base(cfg.TemplatePath)).Funcs(tmplFuncMap).ParseFiles(cfg.TemplatePath)
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

		r = append(r, tgbotapi.NewInlineKeyboardButtonData(btnLabel, "J,"+string(l)))
		if len(r) == cfg.KeyboardRows {
			kb.InlineKeyboard = append(kb.InlineKeyboard, r)
			r = tgbotapi.NewInlineKeyboardRow()
		}
	}

	if len(r) > 0 {
		kb.InlineKeyboard = append(kb.InlineKeyboard, r)
	}

	// button with request to delete message
	kb.InlineKeyboard = append(kb.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Close menu", "C,")))

	return
}

func newTargetsKB(bot *TelegramBot, jobName string) (kb tgbotapi.InlineKeyboardMarkup, e error) {
	// api call timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.APITimeout)
	defer cancel()

	v1api := v1.NewAPI(bot.Prometheus)
	targets, err := v1api.Targets(ctx)
	if err != nil {
		e = fmt.Errorf("error getting targets fo job '%s': %s", jobName, err)
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

		r = append(r, tgbotapi.NewInlineKeyboardButtonData(btnLabel, "T,"+string(t.Labels["instance"])))
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
