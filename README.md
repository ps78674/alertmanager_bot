## Telegram Bot for Alertmanager / Prometheus
Can send alerts via alermanager webhook and/or show as inline menu (by sending /targets command).
![](ps78674.github.com/alertmanager_bot/img/menu.png | width=50)

### Alertmanager configuration
```
receivers:
- name: telegram
  webhook_configs:
  - send_resolved: True
    url: http://127.0.0.1:9000/alerts?chatid=-123456789
```
ChatID is id for chat, where bot will send messages via webhook.

### Bot configuration
Telegram bot token must be set either via config.yaml or env var TELEGRAM_TOKEN
Parameter `alertmanager_url` is used for getting alerts from alertmanager, `prometheus_url` - for getting jobs / targets per job rom prometheus (for forming inline menu).

### Template
Default template consists from two sections.
```
{{ $kind := KindOf . }}
{{ if eq $kind "slice" }}
    ...
{{ else if eq $kind "struct" }}
    ...
{{ end -}}
```
Slice - GettableAlerts (from menu), struct - alert from webhook.
