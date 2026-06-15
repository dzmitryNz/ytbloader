# YTLoader

Backend для скачивания YouTube видео в MP3 с поддержкой нескольких мессенджеров и REST API.

## Как это работает

```
Пользователь (Telegram/Discord/Mattermost/HTTP)
    │
    ▼
┌─────────────────────────────────────────────┐
│  Messenger Layer (pkg/messenger/)           │
│  Принимает сообщения, передаёт агенту       │
└─────────────────┬───────────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────────┐
│  Agent (pkg/agent/agent.go)                 │
│  Парсит команды, управляет бизнес-логикой  │
└─────────┬─────────────────────┬─────────────┘
          │                     │
          ▼                     ▼
┌──────────────────┐  ┌──────────────────────┐
│  Database         │  │  Downloader           │
│  (pkg/db/)       │  │  (pkg/downloader/)    │
│  SQLite           │  │  yt-dlp wrapper       │
│  downloads/tasks  │  │  YouTube → MP3        │
└──────────────────┘  └──────────────────────┘
```

**Поток скачивания:**
1. Пользователь отправляет `/download <url> --send` в мессенджере
2. Agent запрашивает название видео через `yt-dlp --print title --skip-download`
3. Создаёт запись в БД со статусом `pending` и флагом `send_file`
4. Запускает скачивание в горутине через `yt-dlp -x --audio-format mp3`
5. При завершении обновляет статус на `completed`
6. Если `--send` указан, отправляет MP3 файл в чат через callback
7. MP3 файл также сохраняется в `YTDLP_OUTPUT_DIR`

## Структура проекта

```
ytbloader/
├── cmd/server/main.go            # Точка входа, инициализация компонентов
├── pkg/
│   ├── config/config.go          # Конфигурация из переменных окружения
│   ├── db/db.go                  # SQLite: миграции, CRUD для downloads и tasks
│   ├── downloader/downloader.go  # Обёртка над yt-dlp
│   ├── agent/agent.go            # Парсер команд агента
│   ├── api/server.go             # REST API (net/http)
│   └── messenger/
│       ├── telegram/telegram.go  # Telegram Bot API (go-telegram-bot-api)
│       ├── discord/discord.go    # Discord Bot (HTTP API)
│       └── mattermost/mattermost.go  # Mattermost Bot (HTTP API)
├── .env.example
├── Dockerfile
├── docker-compose.yml
├── Makefile
└── go.mod
```

## Быстрый старт

### Зависимости

- Go 1.19+
- [yt-dlp](https://github.com/yt-dlp/yt-dlp) (для скачивания YouTube)
- ffmpeg (нужен yt-dlp для конвертации в MP3)

```bash
# Ubuntu/Debian
sudo apt install ffmpeg
pip install yt-dlp

# macOS
brew install ffmpeg yt-dlp
```

### Установка

```bash
git clone https://github.com/mitry/ytbloader.git
cd ytbloader
cp .env.example .env
# Заполните .env своими токенами
make deps
make run
```

### Docker

```bash
docker-compose up -d
```

## Конфигурация (.env)

| Переменная | Описание | По умолчанию |
|------------|----------|--------------|
| `SERVER_PORT` | Порт API сервера | `8080` |
| `DB_PATH` | Путь к SQLite файлу | `ytbloader.db` |
| `TELEGRAM_BOT_TOKEN` | Токен Telegram бота | (пусто) |
| `DISCORD_BOT_TOKEN` | Токен Discord бота | (пусто) |
| `MATTERMOST_URL` | URL Mattermost сервера | (пусто) |
| `MATTERMOST_BOT_TOKEN` | Токен Mattermost бота | (пусто) |
| `YTDLP_PATH` | Путь к бинарнику yt-dlp | `yt-dlp` |
| `YTDLP_OUTPUT_DIR` | Директория для MP3 файлов | `./downloads` |

## REST API

Базовый URL: `http://localhost:8080`

### Health Check

```
GET /api/health
```

**Response:**
```json
{
  "success": true
}
```

### Downloads

#### Создать скачивание

```
POST /api/downloads
Content-Type: application/json
```

**Request:**
```json
{
  "url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
  "output_dir": "./downloads"
}
```

**Response (200):**
```json
{
  "success": true,
  "data": {
    "ID": 1,
    "URL": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
    "Title": "Rick Astley - Never Gonna Give You Up",
    "Status": "pending",
    "OutputPath": "",
    "CreatedAt": "2025-01-15T10:30:00Z",
    "UpdatedAt": "2025-01-15T10:30:00Z",
    "MessengerID": "",
    "UserID": 0,
    "ChatID": 0,
    "SendFile": false
  }
}
```

**Response (400 - ошибка):**
```json
{
  "success": false,
  "error": "Invalid request body"
}
```

#### Получить скачивание по ID

```
GET /api/downloads/:id
```

**Response (200):**
```json
{
  "success": true,
  "data": {
    "ID": 1,
    "URL": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
    "Title": "Rick Astley - Never Gonna Give You Up",
    "Status": "completed",
    "OutputPath": "./downloads/Rick Astley - Never Gonna Give You Up.mp3",
    "CreatedAt": "2025-01-15T10:30:00Z",
    "UpdatedAt": "2025-01-15T10:31:00Z",
    "MessengerID": "telegram",
    "UserID": 123456789,
    "ChatID": 987654321,
    "SendFile": true
  }
}
```

**Response (404):**
```json
{
  "success": false,
  "error": "Download not found"
}
```

### Tasks

#### Создать задачу

```
POST /api/tasks
Content-Type: application/json
```

**Request:**
```json
{
  "title": "Скачать альбом",
  "description": "Скачать все треки из плейлиста"
}
```

**Response (200):**
```json
{
  "success": true,
  "data": {
    "ID": 1,
    "Title": "Скачать альбом",
    "Description": "Скачать все треки из плейлиста",
    "Status": "open",
    "Priority": 0,
    "MessengerID": "",
    "UserID": 0,
    "CreatedAt": "2025-01-15T10:30:00Z",
    "UpdatedAt": "2025-01-15T10:30:00Z"
  }
}
```

#### Получить задачу по ID

```
GET /api/tasks/:id
```

**Response (200):**
```json
{
  "success": true,
  "data": {
    "ID": 1,
    "Title": "Скачать альбом",
    "Description": "Скачать все треки из плейлиста",
    "Status": "open",
    "Priority": 0,
    "MessengerID": "discord",
    "UserID": 987654321,
    "CreatedAt": "2025-01-15T10:30:00Z",
    "UpdatedAt": "2025-01-15T10:30:00Z"
  }
}
```

### Статусы скачиваний

| Статус | Описание |
|--------|----------|
| `pending` | Ожидает начала скачивания |
| `downloading` | Скачивается |
| `completed` | Успешно скачано |
| `failed` | Ошибка при скачивании |

### Статусы задач

| Статус | Описание |
|--------|----------|
| `open` | Открыта |
| `done` | Выполнена |

### Схема БД

| Таблица | Описание |
|---------|----------|
| `downloads` | Скачивания (URL, статус, путь к файлу) |
| `tasks` | Задачи пользователя |
| `channels` | Подписки на каналы (URL, название, last_check) |
| `videos` | Видео из каналов (URL, длительность, дата публикации) |

## Отправка файлов

Бот поддерживает отправку MP3 файлов обратно в чат. Используйте флаг `--send` или `-s`:

```
/download https://youtube.com/watch?v=... --send
```

**Как это работает:**
1. После скачивания MP3, бот отправляет файл через API мессенджера
2. Telegram - отправляет документ через `sendDocument`
3. Discord - отправляет файл через multipart upload
4. Mattermost - загружает файл через `/api/v4/files/upload`

**Примечание:** Файлы также сохраняются в локальную директорию `./downloads/` независимо от флага `--send`.

## Подписки на каналы

### Как это работает

1. Пользователь подписывается на канал: `/sub https://www.youtube.com/@channel`
2. Бот сохраняет канал в БД с меткой времени
3. При запросе `/videos <id>` бот загружает список видео через yt-dlp
4. При запросе `/new` бот показывает видео, добавленные после последнего просмотра
5. После просмотра `/new` время обновляется

### Пример использования

```
/sub https://www.youtube.com/@RickAstleyYT
→ Subscribed to: Rick Astley
  ID: 1
  Use /videos 1 to see videos

/videos 1
→ Videos in Rick Astley (page 1/5):

  1. Never Gonna Give You Up [3:33]
     /dl https://www.youtube.com/watch?v=... --send
  2. Together Forever [3:24]
     /dl https://www.youtube.com/watch?v=... --send
  ...

/new
→ New videos:

  Rick Astley:
    - Never Gonna Give You Up [3:33]
      /dl https://www.youtube.com/watch?v=... --send

  Use /mark-read to mark all as read
```

## Команды бота

### Скачивание

| Команда | Описание | Пример |
|---------|----------|--------|
| `/download <url> [--send]` | Скачать YouTube видео как MP3 | `/download https://youtube.com/watch?v=... --send` |
| `/dl <url> [--send]` | То же что /download | `/dl https://youtube.com/watch?v=... -s` |

**Флаг `--send` / `-s`:** Если указан, бот отправит MP3 файл обратно в чат после скачивания.

### Подписки на каналы

| Команда | Описание | Пример |
|---------|----------|--------|
| `/sub <url>` | Подписаться на канал | `/sub https://www.youtube.com/@channel` |
| `/subs` | Список подписок | `/subs` |
| `/unsub <id>` | Отписаться от канала | `/unsub 1` |
| `/videos <id> [page]` | Список видео канала (пагинация) | `/videos 1 2` |
| `/new [page]` | Новые видео из подписок | `/new` |

### Задачи

| Команда | Описание | Пример |
|---------|----------|--------|
| `/tasks` | Список задач | `/tasks` |
| `/t` | То же что /tasks | `/t` |
| `/task <title>` | Создать задачу | `/task Купить молоко` |
| `/done <id>` | Завершить задачу | `/done 1` |

### Прочее

| Команда | Описание |
|---------|----------|
| `/help` | Показать справку |

**Пример ответа бота на /download --send:**
```
Download started: Rick Astley - Never Gonna Give You Up
ID: 1
File will be sent to chat when ready
```

**Пример ответа бота на /tasks:**
```
Your tasks:

○ 1. Купить молоко
● 2. Позвонить маме
○ 3. Скачать альбом
```

## Полезные пути

| Путь | Описание |
|------|----------|
| `./downloads/` | Директория с скачанными MP3 файлами |
| `./ytbloader.db` | SQLite база данных |
| `./bin/ytbloader` | Собранный бинарник |
| `cmd/server/main.go` | Точка входа приложения |
| `pkg/config/config.go` | Конфигурация (все переменные окружения) |
| `pkg/db/db.go` | Схема БД и миграции (downloads, tasks, channels, videos) |
| `pkg/agent/agent.go` | Логика обработки команд |
| `pkg/api/server.go` | REST API эндпоинты |
| `pkg/downloader/downloader.go` | Логика скачивания через yt-dlp |
| `pkg/messenger/telegram/telegram.go` | Telegram интеграция |
| `.env.example` | Пример конфигурации |

## Примеры curl

```bash
# Health check
curl http://localhost:8080/api/health

# Скачать видео
curl -X POST http://localhost:8080/api/downloads \
  -H "Content-Type: application/json" \
  -d '{"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}'

# Получить статус скачивания
curl http://localhost:8080/api/downloads/1

# Создать задачу
curl -X POST http://localhost:8080/api/tasks \
  -H "Content-Type: application/json" \
  -d '{"title": "Новая задача"}'

# Получить задачу
curl http://localhost:8080/api/tasks/1
```

## Технологии

- **Go 1.19** - основной язык
- **SQLite** - база данных (go-sqlite3)
- **yt-dlp** - скачивание YouTube
- **net/http** - REST API
- **go-telegram-bot-api** - Telegram
- **HTTP API** - Discord и Mattermost