"""Мини-бот для Telegram: переводит текст, голосовые и аудио из диалога.

Режим «пары языков»: задаёте, например, ru и en — бот сам определяет язык
сообщения и переводит его на другой язык пары. Удобно для переписки двух
людей, говорящих на разных языках (в личке с ботом или в группе).

Выжимка: перешлите боту пачку сообщений из любого чата — он молча их соберёт
и, когда пересылка закончится, сам пришлёт выжимку: суть, задачи, варианты
решения с выгодами и рисками. /summary — выжимка по текущему чату с ботом.
"""

import asyncio
import html
import logging
import os
import tempfile
from collections import defaultdict, deque
from functools import lru_cache

from aiogram import Bot, Dispatcher, F
from aiogram.client.default import DefaultBotProperties
from aiogram.filters import Command, CommandObject
from aiogram.types import Message
from deep_translator import GoogleTranslator
from faster_whisper import WhisperModel
from langdetect import DetectorFactory, LangDetectException, detect

from summary import Entry, SummaryError, render, split_message, summarize

DetectorFactory.seed = 0
logging.basicConfig(level=logging.INFO)

TOKEN = os.environ["BOT_TOKEN"]
WHISPER_MODEL = os.getenv("WHISPER_MODEL", "small")  # tiny/base/small/medium/large-v3
DEFAULT_PAIR = tuple(os.getenv("DEFAULT_PAIR", "ru,en").split(","))

# Пара языков на каждый чат (в памяти; при перезапуске сбрасывается к DEFAULT_PAIR).
chat_pairs: dict[int, tuple[str, str]] = {}

# История диалога на каждый чат — для /summary (тоже в памяти).
HISTORY_LIMIT = int(os.getenv("HISTORY_LIMIT", "500"))
history: dict[int, deque[Entry]] = defaultdict(lambda: deque(maxlen=HISTORY_LIMIT))

# Пересланные пачки: копим сообщения, пока они идут, и делаем выжимку,
# когда BATCH_IDLE секунд не приходит ничего нового.
BATCH_IDLE = float(os.getenv("BATCH_IDLE", "8"))
batches: dict[int, list[Entry]] = defaultdict(list)
batch_timers: dict[int, asyncio.Task] = {}
batch_inflight: dict[int, int] = defaultdict(int)  # голосовые, которые ещё распознаются

dp = Dispatcher()


@lru_cache(maxsize=1)
def whisper() -> WhisperModel:
    return WhisperModel(WHISPER_MODEL, device="auto", compute_type="int8")


def pair_for(chat_id: int) -> tuple[str, str]:
    return chat_pairs.get(chat_id, DEFAULT_PAIR)


def pick_target(src: str, pair: tuple[str, str]) -> str:
    """Язык сообщения из пары -> другой язык пары; любой другой -> первый язык."""
    a, b = pair
    return b if src == a else a


def author_of(message: Message) -> str:
    """Имя автора; для пересланных сообщений — исходного отправителя."""
    origin = message.forward_origin
    if origin is not None:
        if origin.type == "user":
            return origin.sender_user.full_name
        if origin.type == "hidden_user":
            return origin.sender_user_name
        if origin.type == "chat":
            return origin.sender_chat.title or "Чат"
        if origin.type == "channel":
            return origin.chat.title or "Канал"
    if message.from_user:
        return message.from_user.full_name
    return message.sender_chat.title if message.sender_chat else "Неизвестный"


def translate(text: str, target: str) -> str:
    return GoogleTranslator(source="auto", target=target).translate(text)


def transcribe(path: str) -> tuple[str, str]:
    segments, info = whisper().transcribe(path, vad_filter=True)
    return " ".join(s.text.strip() for s in segments).strip(), info.language


async def transcribe_message(message: Message, bot: Bot) -> tuple[str, str]:
    media = message.voice or message.audio or message.video_note
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "audio")
        await bot.download(media, destination=path)
        return await asyncio.to_thread(transcribe, path)


async def send_summary(bot: Bot, chat_id: int, entries: list[Entry]) -> None:
    await bot.send_chat_action(chat_id, "typing")
    status = await bot.send_message(chat_id, f"🧠 Анализирую диалог ({len(entries)} сообщ.)…")
    try:
        data = await summarize(entries, pair_for(chat_id)[0])
    except SummaryError as e:
        await status.edit_text(f"Не получилось: {html.escape(str(e))}")
        return
    except Exception:
        logging.exception("summary failed")
        await status.edit_text("Не получилось сделать выжимку, попробуйте ещё раз.")
        return
    chunks = split_message(render(data))
    await status.edit_text(chunks[0])
    for chunk in chunks[1:]:
        await bot.send_message(chat_id, chunk)


def arm_batch_timer(bot: Bot, chat_id: int) -> None:
    if timer := batch_timers.get(chat_id):
        timer.cancel()
    batch_timers[chat_id] = asyncio.create_task(flush_batch(bot, chat_id))


async def flush_batch(bot: Bot, chat_id: int) -> None:
    await asyncio.sleep(BATCH_IDLE)
    if batch_inflight[chat_id]:
        return  # голосовое ещё распознаётся — по готовности таймер перезапустится
    batch_timers.pop(chat_id, None)
    entries = batches.pop(chat_id, [])
    if entries:
        # Апдейты обрабатываются параллельно, поэтому восстанавливаем исходный порядок.
        entries.sort(key=lambda e: e.date)
        await send_summary(bot, chat_id, entries)


@dp.message(Command("start", "help"))
async def cmd_start(message: Message) -> None:
    a, b = pair_for(message.chat.id)
    await message.answer(
        "Привет! Вот что я умею:\n\n"
        "📋 <b>Выжимка переписки.</b> Выделите сообщения в любом чате (хоть 100), "
        "нажмите «Переслать» и выберите меня. Когда пересылка закончится, "
        "я сам пришлю суть, задачи и варианты решения с выгодами и рисками.\n\n"
        "🌐 <b>Перевод.</b> Напишите мне текст или голосовое — переведу.\n\n"
        "/summary — выжимка по нашему с вами чату\n"
        "/clear — начать новый диалог\n\n"
        f"Текущая пара языков: <b>{a} ⇄ {b}</b>\n"
        "Сменить: <code>/pair ru de</code>\n"
        "Языки — коды ISO 639-1: en, ru, de, es, fr, tr, zh-CN, uk, ...\n\n"
        "В группе дайте боту доступ к сообщениям (отключите privacy mode у @BotFather)."
    )


@dp.message(Command("pair"))
async def cmd_pair(message: Message, command: CommandObject) -> None:
    args = (command.args or "").split()
    supported = GoogleTranslator().get_supported_languages(as_dict=True).values()
    if len(args) != 2 or not all(x in supported for x in args):
        await message.answer("Использование: <code>/pair ru en</code> (два кода языка)")
        return
    chat_pairs[message.chat.id] = (args[0], args[1])
    await message.answer(f"Готово: <b>{args[0]} ⇄ {args[1]}</b>")


@dp.message(Command("clear"))
async def cmd_clear(message: Message) -> None:
    history.pop(message.chat.id, None)
    batches.pop(message.chat.id, None)
    await message.answer("История диалога очищена 🧹")


@dp.message(Command("summary"))
async def cmd_summary(message: Message, bot: Bot) -> None:
    entries = list(history.get(message.chat.id, ()))
    if not entries:
        await message.answer("Пока нечего подытоживать — перешлите мне сообщения из нужного чата.")
        return
    await send_summary(bot, message.chat.id, entries)


@dp.message(F.forward_origin)
async def on_forwarded(message: Message, bot: Bot) -> None:
    """Пересланное сообщение — тихо кладём в пачку, без перевода."""
    chat_id = message.chat.id
    if chat_id not in batches and chat_id not in batch_timers:
        await message.answer("📥 Собираю сообщения… Выжимка придёт, когда пересылка закончится.")
    arm_batch_timer(bot, chat_id)  # сдвигаем выжимку, пока идут новые сообщения
    date = message.forward_origin.date
    if message.voice or message.audio or message.video_note:
        batch_inflight[chat_id] += 1
        try:
            text, _ = await transcribe_message(message, bot)
        except Exception:
            logging.exception("transcription failed")
            text = ""
        finally:
            batch_inflight[chat_id] -= 1
        if text:
            batches[chat_id].append(Entry(author_of(message), text, "voice", date))
    elif body := message.text or message.caption:
        batches[chat_id].append(Entry(author_of(message), body, "text", date))
    arm_batch_timer(bot, chat_id)


@dp.message(F.voice | F.audio | F.video_note)
async def on_audio(message: Message, bot: Bot) -> None:
    await bot.send_chat_action(message.chat.id, "typing")
    text, lang = await transcribe_message(message, bot)
    if not text:
        await message.reply("Не удалось распознать речь 🤷")
        return
    history[message.chat.id].append(Entry(author_of(message), text, "voice"))
    target = pick_target(lang, pair_for(message.chat.id))
    translated = await asyncio.to_thread(translate, text, target)
    await message.reply(
        f"🎙 <i>{lang}:</i> {html.escape(text)}\n\n🌐 <i>{target}:</i> {html.escape(translated)}"
    )


@dp.message(F.text & ~F.text.startswith("/"))
async def on_text(message: Message) -> None:
    history[message.chat.id].append(Entry(author_of(message), message.text, "text"))
    try:
        src = detect(message.text)
    except LangDetectException:
        return  # эмодзи, цифры и т.п. — переводить нечего
    target = pick_target(src, pair_for(message.chat.id))
    translated = await asyncio.to_thread(translate, message.text, target)
    if translated and translated.strip() != message.text.strip():
        await message.reply(f"🌐 <i>{target}:</i> {html.escape(translated)}")


async def main() -> None:
    bot = Bot(TOKEN, default=DefaultBotProperties(parse_mode="HTML"))
    await dp.start_polling(bot)


if __name__ == "__main__":
    asyncio.run(main())
