# Simple Finance

Personal asset tracker: T-Bank, Bybit Spot, Bitcoin cold wallets, Monero.

## Быстрый старт

```bash
# 1. Скопируй .env
cp .env.example .env

# 2. Заполни .env:
#    - ADMIN_USERNAME / ADMIN_PASSWORD  — логин в веб-интерфейс
#    - TBANK_SESSION_ID                 — сессия из Burp/браузера
#    - BYBIT_API_KEY / BYBIT_API_SECRET — read-only ключ с Bybit
#    - BTC_ADDRESSES                    — адреса через запятую
#    - APP_SECRET                       — любая случайная строка 32+ символов

# 3. Запуск
docker compose up -d

# Открой http://your-vps-ip:8080
```

## Обновление T-Bank сессии

Сессия T-Bank живёт ~24 часа. Когда протухнет:

1. Открой [www.tbank.ru](https://www.tbank.ru) в браузере
2. Перехвати запрос к `/api/common/v1/accounts_light_ib` (DevTools → Network)
3. Скопируй значение `sessionid=` из URL
4. Обнови `.env`:
   ```
   TBANK_SESSION_ID=новый_session_id
   ```
5. Перезапусти: `docker compose restart app`

> Планируется: автообновление через cookie refresh endpoint.

## Структура проекта

```
cmd/server/         — точка входа
config/             — загрузка .env
internal/
  auth/             — JWT авторизация
  handlers/         — HTTP хендлеры + агрегатор
  integrations/
    tbank/          — T-Bank API (session-based)
    bybit/          — Bybit v5 API (HMAC signed)
    bitcoin/        — mempool.space (публичный, без ключей)
    monero/         — monero-wallet-rpc (опционально)
  models/           — доменные модели
  storage/          — PostgreSQL через sqlx
migrations/         — SQL миграции (goose)
web/templates/      — HTML + HTMX
```

## Добавление Steam

Steam интеграция планируется следующим шагом:
- API: `https://api.steampowered.com/IEconService/GetInventory/v1/`
- Нужен: Steam API Key + SteamID64
- Цены: через Steam Market Price Overview API
