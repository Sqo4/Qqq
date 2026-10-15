# Translate Bot

Мини-бот для Telegram: переводит **текст, голосовые, аудио и кружочки** из диалога.

- Работает в паре языков (по умолчанию `ru ⇄ en`): бот сам определяет язык и переводит на второй.
  Удобно для переписки двух людей на разных языках — в личке или в группе.
- Речь распознаётся локально через [faster-whisper](https://github.com/SYSTRAN/faster-whisper) (бесплатно, без API-ключей).
- Перевод — Google Translate через [deep-translator](https://github.com/nidhaloff/deep-translator) (бесплатно, без ключа).

## Запуск

1. Создайте бота у [@BotFather](https://t.me/BotFather) и получите токен.
   Для групп: `/setprivacy` → `Disable`, чтобы бот видел все сообщения.
2. Нужны Python 3.10+ и `ffmpeg`.

```bash
python -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
export BOT_TOKEN=123456:ABC...
python bot.py
```

Переменные окружения (необязательные):

| Переменная      | По умолчанию | Описание                                             |
|-----------------|--------------|------------------------------------------------------|
| `WHISPER_MODEL` | `small`      | `tiny`/`base` — быстрее, `medium`/`large-v3` — точнее |
| `DEFAULT_PAIR`  | `ru,en`      | Пара языков по умолчанию                             |

## Команды

- `/start` — справка и текущая пара языков
- `/pair ru de` — сменить пару языков для этого чата
