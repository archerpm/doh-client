Исходники прочитал, файлы не менял. Go-тесты не запускал: shell недоступен. Коллизии ID воспроизведены в памяти.

**A. Подтверждённые проблемы**

- [daemon.go:206](D:/AI/DoH/internal/daemon/daemon.go:206): при смене режима/listen ошибка `disableLocked()` возвращается **после** сохранения нового config и `router.Apply()`, без отката. Например, hosts→proxy при запрете записи hosts: фактически остаётся hosts, config/status уже показывают proxy, API сообщает отказ. Откатывать router/store/cfg и на этой ветке.
- [console_windows.go:22](D:/AI/DoH/console_windows.go:22): после успешного attach безусловно заменяются stdout/stderr. В консольном родителе `status --json > out.json` и pipe теряют вывод: он уходит в `CONOUT$`. Сохранять исходные валидные handles файлов/pipe; восстанавливать только отсутствующие консольные потоки. [`CONOUT$` обходит перенаправление](https://learn.microsoft.com/en-us/windows/console/console-handles).
- [app.js:375](D:/AI/DoH/frontend/app.js:375): ID всё ещё неоднозначен: `/ProfileA` и `/profilea`, `/a/b` и `/a-b` совпадают. Path/query чувствительны к регистру. Использовать UUID либо хеш нормализованного URL без потери регистра path/query.
- [bootstrap.go:87](D:/AI/DoH/internal/doh/bootstrap.go:87): 1,2 с ограничивают отдельный обмен, **не весь сервер**. UDP→TCP начинает новый таймаут; TCP dial и чтение тоже получают отдельные бюджеты. Медленный truncated-ответ способен снова лишить резерв рабочего времени. Один дочерний context на сервер, общий для A/AAAA и TCP fallback.
- [ctl.go:152](D:/AI/DoH/ctl.go:152): единственный `Decode` принимает валидный config с последующим мусором/вторым JSON. `LimitReader` также не проверяет полный размер файла. Перед `SaveConfig` требовать EOF и явно отклонять превышение лимита.
- [ctl.go:74](D:/AI/DoH/ctl.go:74): лишние аргументы управляющих команд игнорируются: `disable --jsno` действительно выключит обход вместо exit 2. Проверять аргументы до обращения к API.

Прямое выключение при ошибке hosts, порядок unregister→удаление файлов, возврат radios и отмена tween исправлены. `os.NewFile` для `CONOUT$` корректен: Go распознаёт консольный handle. Обычный запуск из Explorer attach не вызывает. Путь импорта открывается в процессе пользователя; дополнительной привилегированной записи по нему нет, содержимое проходит `Normalize`/`router.Apply`.

**B. По продукту**

- **#8 — согласен.** Предложение после диагностики, применение пользователем. Доступность TLS и принадлежность прокси-пулу ещё не доказывают успешную работу сайта.
- **#5 — согласен.** Предложенные стадии подходят; успешный TLS должен означать «соединение исправно, причина выше TLS», а не «сайт работает».
- **#10 — согласен для текущего продукта.** Уведомление и ссылка достаточны; полноценное обновление оправдано только вместе с подписью, согласованием версий и откатом.
- **#1 — elevation уже есть** в [gui.go:108](D:/AI/DoH/gui.go:108). Не хватает единой операции: выбрать preset IDs → установить/дождаться API → получить config → добавить отсутствующие правила → сохранить → включить → проверить результат. [presets.go:14](D:/AI/DoH/presets.go:14) уже содержит данные; сборка правил сейчас только во frontend. Нужны backend-оркестратор и показ стадии/ошибки, плюс предупреждение о браузерном DoH.

**C. Минимальный `/v1/diagnose`**

`POST {"domain":"claude.ai"}`, существующий guard, общий бюджет 12 с, частичные результаты при таймауте. Фиксированный порт 443.

1. **rule:** `NormalizeDomain`; снимок режима/active, самое специфичное правило, подавление AAAA/HTTPS. В hosts отдельно проверить наличие **точного** домена.
2. **chain:** заданная цепочка → enabled-фильтр → порядок `health.Order`. Экспортировать `Router.Explain`, переиспользующий `matchRule` и текущую логику.
3. **dns:** A/AAAA по провайдерам параллельно через `ResolveProvider`; сохранить rcode, адреса, время и ошибку. `AddrsOf` переиспользовать; canary/pool-классификация для MVP необязательна.
4. **os_dns:** ожидаемые и фактические DNS адаптеров + ответы системного resolver. Новый read-only `sysdns.Inspect`; `Status().Applied` сейчас проверяет наличие backup. Переиспользовать Windows `adapters`, Linux `resolvectlList`, macOS `services/getDNS`.
5. **connect:** `DialContext(IP:443)` → `tls.Client(ServerName:domain)` → `HandshakeContext` с обычной проверкой сертификата. До двух адресов каждого семейства на провайдера, concurrency 4; отдельно TCP/TLS ошибки.
6. **verdict:** код, стадия-доказательство, одно действие. При недостаточных данных — `inconclusive`; конфигурацию автоматически не менять.

```json
{
  "domain": "claude.ai",
  "stages": [
    {"id": "rule", "status": "ok", "data": {"matched": "claude.ai"}},
    {"id": "chain", "status": "ok", "data": {"configured": ["malw", "comss"], "effective": ["comss", "malw"]}},
    {"id": "dns", "status": "ok", "data": {"providers": []}},
    {"id": "os_dns", "status": "failed", "data": {"expected": ["127.0.0.1"], "actual": ["192.168.1.1"]}},
    {"id": "connect", "status": "ok", "data": {"probes": []}}
  ],
  "verdict": {
    "code": "os_dns_mismatch",
    "stage": "os_dns",
    "action": {"code": "reapply_dns", "text": "Повторно примените DNS клиента"}
  }
}
```

Связка: `Daemon.Diagnose` → новый `internal/probe` → API/Client → `App.DiagnoseDomain`. Для успешных TCP/TLS итог: `transport_ok`, действие — проверить Secure DNS браузера; его обход служба достоверно определить не может.