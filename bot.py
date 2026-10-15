"""Мини-бот для Telegram: переводит текст, голосовые и аудио из диалога.

Режим «пары языков»: задаёте, например, ru и en — бот сам определяет язык
сообщения и переводит его на другой язык пары. Удобно для переписки двух
людей, говорящих на разных языках (в личке с ботом или в группе).
"""

import asyncio
import html
import logging
import os
import tempfile
from functools import lru_cache

from aiogram import Bot, Dispatcher, F
from aiogram.client.default import DefaultBotProperties
from aiogram.filters import Command, CommandObject
from aiogram.types import Message
from deep_translator import GoogleTranslator
from faster_whisper import WhisperModel
from langdetect import DetectorFactory, LangDetectException, detect

DetectorFactory.seed = 0
logging.basicConfig(level=logging.INFO)

TOKEN = os.environ["BOT_TOKEN"]
WHISPER_MODEL = os.getenv("WHISPER_MODEL", "small")  # tiny/base/small/medium/large-v3
DEFAULT_PAIR = tuple(os.getenv("DEFAULT_PAIR", "ru,en").split(","))

# Пара языков на каждый чат (в памяти; при перезапуске сбрасывается к DEFAULT_PAIR).
chat_pairs: dict[int, tuple[str, str]] = {}

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


def translate(text: str, target: str) -> str:
    return GoogleTranslator(source="auto", target=target).translate(text)


def transcribe(path: str) -> tuple[str, str]:
    segments, info = whisper().transcribe(path, vad_filter=True)
    return " ".join(s.text.strip() for s in segments).strip(), info.language


@dp.message(Command("start", "help"))
async def cmd_start(message: Message) -> None:
    a, b = pair_for(message.chat.id)
    await message.answer(
        "Привет! Пришлите текст, голосовое или аудио — я переведу.\n\n"
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


@dp.message(F.voice | F.audio | F.video_note)
async def on_audio(message: Message, bot: Bot) -> None:
    media = message.voice or message.audio or message.video_note
    await bot.send_chat_action(message.chat.id, "typing")
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "audio")
        await bot.download(media, destination=path)
        text, lang = await asyncio.to_thread(transcribe, path)
    if not text:
        await message.reply("Не удалось распознать речь 🤷")
        return
    target = pick_target(lang, pair_for(message.chat.id))
    translated = await asyncio.to_thread(translate, text, target)
    await message.reply(
        f"🎙 <i>{lang}:</i> {html.escape(text)}\n\n🌐 <i>{target}:</i> {html.escape(translated)}"
    )


@dp.message(F.text & ~F.text.startswith("/"))
async def on_text(message: Message) -> None:
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
