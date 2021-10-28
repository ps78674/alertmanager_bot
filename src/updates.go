package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/prometheus/alertmanager/api/v2/client/alert"
	"github.com/prometheus/alertmanager/api/v2/client/general"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

func handleUpdates(bot *TelegramBot) {
	updates, err := bot.BotAPI.GetUpdatesChan(tgbotapi.NewUpdate(0))
	if err != nil {
		log.Fatalf("error getting updates channel: %s\n", err)
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
			log.Printf("new callback query from %s: %s", update.CallbackQuery.From.String(), update.CallbackQuery.Data)
			if err := processCallbackQuery(bot, update.CallbackQuery); err != nil {
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
		strMsg := fmt.Sprintf("Telegram Bot for Alertmanager\nVersion %s\n%s", versionString, helpMsg)
		msg := tgbotapi.NewMessage(m.Chat.ID, strMsg)
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "alerts":
		// TODO: support multiple cmd arguments
		args := m.CommandArguments()
		argsArr := strings.Split(args, " ")
		if len(argsArr) > 1 {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Too many arguments.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
		}
		if len(argsArr[0]) != 0 && argsArr[0] != "json" {
			msg := tgbotapi.NewMessage(m.Chat.ID, "Unknown argument.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
		}

		// get active alerts
		alerts, _ := bot.Alertmanager.Alert.GetAlerts(&alert.GetAlertsParams{
			Context: ctx,
		})
		if len(alerts.GetPayload()) == 0 {
			msg := tgbotapi.NewMessage(m.Chat.ID, "No active alerts found.")
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
		}

		// send message as json
		if len(cfg.TemplatePath) == 0 || argsArr[0] == "json" {
			bytes, err := json.MarshalIndent(alerts.GetPayload(), "", "  ")
			if err != nil {
				return fmt.Errorf("error marshalling alerts: %s", err)
			}

			msg := tgbotapi.NewMessage(m.Chat.ID, string(bytes))
			if err := sendMessage(bot, msg); err != nil {
				return fmt.Errorf("error sending message: %s", err)
			}
		}

		// send temlated message
		s, err := applyTemplate(alerts.GetPayload())
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
Version: %s
Uptime: %s

Prometheus
Version: %s
Uptime: %s

Bot
Version: %s
Uptime: %s
		`,
			*aStatus.Payload.VersionInfo.Version,
			time.Since(time.Time(*aStatus.Payload.Uptime)).String(),
			pBuildInfo.Version,
			time.Since(pRTInfo.StartTime).String(),
			versionString,
			time.Since(bot.StartTime).String())
		msg := tgbotapi.NewMessage(m.Chat.ID, status)
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

func processCallbackQuery(bot *TelegramBot, c *tgbotapi.CallbackQuery) error {
	// api call timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.APITimeout)
	defer cancel()

	// callback data limited to 64 bytes
	// so we can't send large data like json etc
	//
	// callback data format: "<TYPE>,<NAME>", where
	// <TYPE> - callback type
	//          J - job
	//          T - target
	//          JJ - back menu for targets / print all jobs menu
	//          TT - back menu for alerts / print all targets for job
	//          C - close menu
	// <NAME> - callback name (name for J, T or BT)
	//
	// Ex:
	//     J,test_job
	//     T,server1:9100
	//     JJ,
	//     TT,test_job
	//     C,
	//
	// callbackData[0] - type
	// callbackData[1] - name
	callbackData := strings.SplitN(c.Data, ",", 2)
	if len(callbackData) != 2 { // splitted data must be length of 2
		return fmt.Errorf("got wrong data '%s'", c.Data)
	}

	switch callbackData[0] {
	case "J":
		kb, err := newTargetsKB(bot, callbackData[1])
		if err != nil {
			return fmt.Errorf("error creating targets menu: %s", err)
		}

		kb.InlineKeyboard = append(kb.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Go back", "JJ,")))

		// msg := tgbotapi.NewMessage(c.Message.Chat.ID, "Select target:")
		msg := tgbotapi.NewEditMessageText(c.Message.Chat.ID, c.Message.MessageID, "Select target:")
		msg.ReplyMarkup = &kb

		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "T":
		al, err := bot.Alertmanager.Alert.GetAlerts(&alert.GetAlertsParams{
			Filter:  []string{"instance=" + callbackData[1]},
			Context: ctx,
		})
		if err != nil {
			return fmt.Errorf("error getting alerts for target '%s': %s", callbackData[1], err)
			// log.Printf("error getting alerts for target '%s': %s", callbackData[1], err)
			// continue
		}

		var msgText string
		if len(al.GetPayload()) > 0 {
			s, err := applyTemplate(al.GetPayload())
			if err != nil {
				return fmt.Errorf("error applying template: %s", err)
			}
			msgText = s
		} else {
			msgText = "No active alerts for " + callbackData[1]
		}

		// get job name for target
		v1api := v1.NewAPI(bot.Prometheus)
		targets, err := v1api.Targets(ctx)
		if err != nil {
			return fmt.Errorf("error getting targets data: %s", err)
		}

		for _, t := range targets.Active {
			if string(t.Labels["instance"]) == callbackData[1] {
				kb := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Go back", "TT,"+string(t.Labels["job"]))))
				msg := tgbotapi.NewEditMessageText(c.Message.Chat.ID, c.Message.MessageID, msgText)
				msg.ParseMode = tgbotapi.ModeHTML
				msg.ReplyMarkup = &kb

				if err := sendMessage(bot, msg); err != nil {
					return fmt.Errorf("error sending message: %s", err)
				}

				break
			}
		}
	case "JJ":
		kb, err := newJobsKB(bot)
		if err != nil {
			return fmt.Errorf("error creating jobs menu: %s", err)
		}

		msg := tgbotapi.NewEditMessageText(c.Message.Chat.ID, c.Message.MessageID, "Select job:")
		msg.ReplyMarkup = &kb

		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "TT":
		kb, err := newTargetsKB(bot, callbackData[1])
		if err != nil {
			return fmt.Errorf("error creating targets menu: %s", err)
		}

		kb.InlineKeyboard = append(kb.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Go back", "JJ,")))

		msg := tgbotapi.NewEditMessageText(c.Message.Chat.ID, c.Message.MessageID, "Select target:")
		msg.ReplyMarkup = &kb

		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	case "C":
		msg := tgbotapi.NewDeleteMessage(c.Message.Chat.ID, c.Message.MessageID)
		if err := sendMessage(bot, msg); err != nil {
			return fmt.Errorf("error sending message: %s", err)
		}
	}

	return nil
}
