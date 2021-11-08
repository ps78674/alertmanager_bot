package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/prometheus/alertmanager/api/v2/client/alert"
	"github.com/prometheus/alertmanager/api/v2/client/general"
	"github.com/prometheus/alertmanager/api/v2/client/silence"
	"github.com/prometheus/alertmanager/api/v2/models"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/segmentio/ksuid"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

const helpMsg = `
Available commands:
/status - show alertmanager & bot status
/alerts - show active alerts
/targets - show alerts per target
/silences - show active silences
`

func handleUpdates(bot *TelegramBot) {
	updates, err := bot.BotAPI.GetUpdatesChan(tgbotapi.NewUpdate(0))
	if err != nil {
		log.Printf("error getting updates channel: %s", err)
		return
	}

	for update := range updates {
		if update.Message != nil {
			log.Printf("new message from %s: %s", update.Message.From.String(), update.Message.Text)
			if err := processMessage(bot, update.Message); err != nil {
				log.Printf("error processing message: %s", err)
			}
			continue
		}
		if update.EditedMessage != nil {
			log.Printf("new edited message from %s: %s", update.EditedMessage.From.String(), update.EditedMessage.Text)
			if err := processMessage(bot, update.EditedMessage); err != nil {
				log.Printf("error processing edited message: %s", err)
			}
			continue
		}
		if update.CallbackQuery != nil {
			// get callback data from cache
			cacheData, err := bot.Cache.Get(update.CallbackQuery.Data)
			if err != nil {
				log.Printf("error getting callback data from cache: %s", err)
				continue
			}
			bot.Cache.Remove(update.CallbackQuery.Data)

			// marshall callback data for logging
			b, err := json.Marshal(cacheData)
			if err != nil {
				log.Printf("error marshalling cache data: %s", err)
				continue
			}

			// process callback query
			log.Printf("new callback query from %s: %s", update.CallbackQuery.From.String(), string(b))
			if err := processCallbackQuery(bot, update.CallbackQuery, cacheData.(Callback)); err != nil {
				log.Printf("error processing callback query: %s", err)
			}
			continue
		}
		log.Println("cannot parse update data")
	}
}

func processMessage(bot *TelegramBot, m *tgbotapi.Message) error {
	// api call timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.APITimeout)
	defer cancel()

	// accept messages only from configured users
	var updateUserIsAdmin bool
	for _, u := range cfg.Users {
		if u == m.From.String() {
			updateUserIsAdmin = true
			break
		}
	}
	if !updateUserIsAdmin {
		msg := tgbotapi.NewMessage(m.Chat.ID, "I can't talk to you, sorry.")
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
		return nil
	}

	// allow only commands (e.g. /alerts)
	if !m.IsCommand() {
		msg := tgbotapi.NewMessage(m.Chat.ID, "Message doesn't look like a command.\n"+helpMsg)
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
		return nil
	}

	// process commands
	switch m.Command() {
	case "help", "start":
		strMsg := fmt.Sprintf("Telegram Bot for Alertmanager\nVersion <b>%s</b>\n%s", versionString, helpMsg)
		msg := tgbotapi.NewMessage(m.Chat.ID, strMsg)
		msg.ParseMode = tgbotapi.ModeHTML
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "alerts":
		// check command arguments
		args := m.CommandArguments()
		argsArr := strings.Split(args, " ")
		if len(argsArr) > 1 {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Too many arguments.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
			return nil
		}
		if len(argsArr[0]) != 0 && argsArr[0] != "json" {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Unknown argument.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
			return nil
		}

		// get active alerts
		alerts, err := bot.Alertmanager.Alert.GetAlerts(&alert.GetAlertsParams{
			Context: ctx,
		})
		if err != nil {
			return fmt.Errorf("error getting alerts: %s", err)
		}

		if len(alerts.GetPayload()) == 0 {
			msg := tgbotapi.NewMessage(m.Chat.ID, "No active alerts found.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
			return nil
		}

		// send plain json if no template defined in config
		// or json send as first command argument
		// e.g. '/alerts json'
		if len(cfg.GettableAlertsTemplatePath) == 0 || argsArr[0] == "json" {
			bytes, err := json.MarshalIndent(alerts.GetPayload(), "", "  ")
			if err != nil {
				return fmt.Errorf("error marshalling alerts: %s", err)
			}

			msg := tgbotapi.NewMessage(m.Chat.ID, string(bytes))
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
			return nil
		}

		// send temlated message
		s, err := applyTemplate(alerts.GetPayload(), cfg.GettableAlertsTemplatePath)
		if err != nil {
			return fmt.Errorf("error applying template: %s", err)
		}

		msg := tgbotapi.NewMessage(m.Chat.ID, s)
		msg.ParseMode = tgbotapi.ModeHTML
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "targets":
		kb, err := newJobsKB(bot)
		if err != nil {
			return fmt.Errorf("error creating jobs menu: %s", err)
		}

		msg := tgbotapi.NewMessage(m.Chat.ID, "Select job:")
		msg.ReplyMarkup = kb

		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "status":
		aStatus, err := bot.Alertmanager.General.GetStatus(&general.GetStatusParams{
			Context: ctx,
		})
		if err != nil {
			return fmt.Errorf("error getting alertmanager status: %s", err)
		}

		v1api := v1.NewAPI(bot.Prometheus)
		pBuildInfo, err := v1api.Buildinfo(ctx)
		if err != nil {
			return fmt.Errorf("error getting prometheus status: %s", err)
		}
		pRTInfo, err := v1api.Runtimeinfo(ctx)
		if err != nil {
			return fmt.Errorf("error getting prometheus status: %s", err)
		}

		status := fmt.Sprintf(`
Alertmanager
Version: <b>%s</b>
Uptime: <b>%s</b>

Prometheus
Version: <b>%s</b>
Uptime: <b>%s</b>

Bot
Version: <b>%s</b>
Uptime: <b>%s</b>
		`,
			*aStatus.Payload.VersionInfo.Version,
			time.Since(time.Time(*aStatus.Payload.Uptime)).String(),
			pBuildInfo.Version,
			time.Since(pRTInfo.StartTime).String(),
			versionString,
			time.Since(bot.StartTime).String())
		msg := tgbotapi.NewMessage(m.Chat.ID, status)
		msg.ParseMode = tgbotapi.ModeHTML
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "silences":
		// check command arguments
		args := m.CommandArguments()
		argsArr := strings.Split(args, " ")
		if len(argsArr) > 1 {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Too many arguments.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
			return nil
		}
		if len(argsArr[0]) != 0 && argsArr[0] != "json" {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Unknown argument.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
			return nil
		}

		// get active silences
		silences, err := bot.Alertmanager.Silence.GetSilences(&silence.GetSilencesParams{
			Context: ctx,
		})
		if err != nil {
			return fmt.Errorf("error gettnig silences: %s", err)
		}

		// TODO: better filter for active silences ??
		var activeSilences models.GettableSilences
		for _, s := range silences.GetPayload() {
			if *s.Status.State == "active" {
				activeSilences = append(activeSilences, s)
			}
		}

		if len(activeSilences) == 0 {
			msg := tgbotapi.NewMessage(m.Chat.ID, "No active silences found.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
			return nil
		}

		// send plain json if no template defined in config
		// or json send as first command argument
		// e.g. '/silences json'
		if len(cfg.GettableAlertsTemplatePath) == 0 || argsArr[0] == "json" {
			bytes, err := json.MarshalIndent(activeSilences, "", "  ")
			if err != nil {
				return fmt.Errorf("error marshalling silences: %s", err)
			}

			msg := tgbotapi.NewMessage(m.Chat.ID, string(bytes))
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
			return nil
		}

		// send temlated message
		s, err := applyTemplate(activeSilences, cfg.SilencesTemplatePath)
		if err != nil {
			return fmt.Errorf("error applying template: %s", err)
		}

		msg := tgbotapi.NewMessage(m.Chat.ID, s)
		msg.ParseMode = tgbotapi.ModeHTML
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	default:
		msg := tgbotapi.NewMessage(m.Chat.ID, "Unknown command.\n"+helpMsg)
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	}

	return nil
}

func processCallbackQuery(bot *TelegramBot, cq *tgbotapi.CallbackQuery, cb Callback) error {
	// api call timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.APITimeout)
	defer cancel()

	switch cb.Type {
	case "job":
		// create inline keyboard with targets for requested job
		kb, err := newTargetsKB(bot, cb.Data["job_name"])
		if err != nil {
			return fmt.Errorf("error creating targets menu: %s", err)
		}

		// create new cache entry
		cacheID := ksuid.New().String()
		newCallback := Callback{
			Type: "jobs",
		}
		bot.Cache.Set(cacheID, newCallback)

		kb.InlineKeyboard = append(kb.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Go back", cacheID)))

		var msg tgbotapi.Chattable
		if cb.Data["leave_last_message"] == "yes" {
			// remove 'Go back' button in previous message
			newMarkup := tgbotapi.InlineKeyboardMarkup{
				InlineKeyboard: make([][]tgbotapi.InlineKeyboardButton, 0),
			}
			if err := sendMessage(bot, tgbotapi.NewEditMessageReplyMarkup(cq.Message.Chat.ID, cq.Message.MessageID, newMarkup)); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}

			m := tgbotapi.NewMessage(cq.Message.Chat.ID, "Select target:")
			m.ReplyMarkup = &kb
			msg = m
		} else {
			m := tgbotapi.NewEditMessageText(cq.Message.Chat.ID, cq.Message.MessageID, "Select target:")
			m.ReplyMarkup = &kb
			msg = m
		}

		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "target":
		al, err := bot.Alertmanager.Alert.GetAlerts(&alert.GetAlertsParams{
			Filter:  []string{"instance=" + cb.Data["target_name"]},
			Context: ctx,
		})
		if err != nil {
			return fmt.Errorf("error getting alerts for target '%s': %s", cb.Data["target_name"], err)
		}

		var msgText string
		if len(al.GetPayload()) > 0 {
			s, err := applyTemplate(al.GetPayload(), cfg.GettableAlertsTemplatePath)
			if err != nil {
				return fmt.Errorf("error applying template: %s", err)
			}
			msgText = s
		} else {
			msgText = "No active alerts for " + cb.Data["target_name"]
		}

		// create new cache entry
		cacheID := ksuid.New().String()
		newCallback := Callback{
			Type: "job",
			Data: make(map[string]string),
		}
		newCallback.Data["job_name"] = cb.Data["job_name"]
		newCallback.Data["leave_last_message"] = "yes"
		bot.Cache.Set(cacheID, newCallback)

		kb := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Go back", cacheID)))
		msg := tgbotapi.NewEditMessageText(cq.Message.Chat.ID, cq.Message.MessageID, msgText)
		msg.ParseMode = tgbotapi.ModeHTML
		msg.ReplyMarkup = &kb

		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "jobs":
		// create inline keyboard for all prometheus jobs
		kb, err := newJobsKB(bot)
		if err != nil {
			return fmt.Errorf("error creating jobs menu: %s", err)
		}

		msg := tgbotapi.NewEditMessageText(cq.Message.Chat.ID, cq.Message.MessageID, "Select job:")
		msg.ReplyMarkup = &kb

		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "targets":
		// create inline keyboard with targets for requested job
		kb, err := newTargetsKB(bot, cb.Data["job_name"])
		if err != nil {
			return fmt.Errorf("error creating targets menu: %s", err)
		}

		// create new cache entry
		cacheID := ksuid.New().String()
		newCallback := Callback{
			Type: "jobs",
		}
		bot.Cache.Set(cacheID, newCallback)

		kb.InlineKeyboard = append(kb.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Go back", cacheID)))

		msg := tgbotapi.NewEditMessageText(cq.Message.Chat.ID, cq.Message.MessageID, "Select target:")
		msg.ReplyMarkup = &kb

		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "close":
		msg := tgbotapi.NewDeleteMessage(cq.Message.Chat.ID, cq.Message.MessageID)
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "silence":
		// HTTPClient := http.Client{}

		ctx, cancel := context.WithTimeout(context.Background(), cfg.APITimeout)
		defer cancel()

		instance_name := "instance"
		instance_value := cb.Data["instance"]
		alertname_name := "alertname"
		alertname_value := cb.Data["alertname"]
		isRegex := false

		matchers := models.Matchers{
			&models.Matcher{
				IsRegex: &isRegex,
				Name:    &instance_name,
				Value:   &instance_value,
			},
			&models.Matcher{
				IsRegex: &isRegex,
				Name:    &alertname_name,
				Value:   &alertname_value,
			},
		}

		comment := ""
		createdBy := programName + " version " + versionString
		startsAt := strfmt.DateTime(time.Now())
		endsAt := strfmt.DateTime(time.Now().Add(cfg.SilenceDuration))

		params := silence.PostSilencesParams{
			Silence: &models.PostableSilence{
				Silence: models.Silence{
					Comment:   &comment,
					CreatedBy: &createdBy,
					Matchers:  matchers,
					StartsAt:  &startsAt,
					EndsAt:    &endsAt,
				},
			},
			Context: ctx,
		}

		// create new silence
		ok, err := bot.Alertmanager.Silence.PostSilences(&params)
		if err != nil {
			return fmt.Errorf("error posting new silence: %s", err)
		}

		// remove 'Silence' button
		newMarkup := tgbotapi.InlineKeyboardMarkup{
			InlineKeyboard: make([][]tgbotapi.InlineKeyboardButton, 0),
		}
		if err := sendMessage(bot, tgbotapi.NewEditMessageReplyMarkup(cq.Message.Chat.ID, cq.Message.MessageID, newMarkup)); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}

		m := fmt.Sprintf(`Created new silence:
ID: <b>%s</b>
StartsAt: <b>%s</b>
EndsAt: <b>%s</b>
Matchers: "[{instance="%s"},{alertname="%s"}]"`, ok.Payload.SilenceID, startsAt, endsAt, instance_value, alertname_value)

		msg := tgbotapi.NewMessage(cq.Message.Chat.ID, m)
		msg.ParseMode = tgbotapi.ModeHTML
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	}

	return nil
}
