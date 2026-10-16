"""Выжимка диалога через Claude: суть, задачи, варианты решений с выгодами и рисками."""

import html
import json
from dataclasses import dataclass
from datetime import datetime

import anthropic

MODEL = "claude-opus-5-5"

SYSTEM = """Ты — ассистент, который разбирает переписку и готовит по ней деловую выжимку.
Переписка может быть на разных языках и содержать расшифровки голосовых (с ошибками распознавания).
Пиши на языке, указанном пользователем. Опирайся только на то, что есть в диалоге, ничего не выдумывай.

- summary: суть диалога в 3–6 предложениях — о чём говорили, к чему пришли.
- participants: каждый участник и его позиция/интерес одной фразой.
- tasks: конкретные задачи, которые вытекают из диалога. Формулируй как действие («Подготовить…»).
  owner — кто делает (если понятно из диалога, иначе пустая строка), deadline — срок (или пустая строка).
- options: варианты решения, которые обсуждались или явно напрашиваются из диалога.
  Для каждого — выгоды и риски. Если вариант предложил ты, а не участники, начни description с «(предложение)».
- decision: что решили; пустая строка, если решения нет.
- open_questions: что осталось нерешённым или требует уточнения."""

SCHEMA = {
    "type": "object",
    "properties": {
        "summary": {"type": "string"},
        "participants": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {"name": {"type": "string"}, "position": {"type": "string"}},
                "required": ["name", "position"],
                "additionalProperties": False,
            },
        },
        "tasks": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "task": {"type": "string"},
                    "owner": {"type": "string"},
                    "deadline": {"type": "string"},
                },
                "required": ["task", "owner", "deadline"],
                "additionalProperties": False,
            },
        },
        "options": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "title": {"type": "string"},
                    "description": {"type": "string"},
                    "benefits": {"type": "array", "items": {"type": "string"}},
                    "risks": {"type": "array", "items": {"type": "string"}},
                },
                "required": ["title", "description", "benefits", "risks"],
                "additionalProperties": False,
            },
        },
        "decision": {"type": "string"},
        "open_questions": {"type": "array", "items": {"type": "string"}},
    },
    "required": ["summary", "participants", "tasks", "options", "decision", "open_questions"],
    "additionalProperties": False,
}


@dataclass
class Entry:
    author: str
    text: str
    kind: str  # "text" | "voice"
    date: datetime | None = None


class SummaryError(Exception):
    pass


client = anthropic.AsyncAnthropic()


def format_dialog(entries: list[Entry]) -> str:
    lines = []
    for e in entries:
        mark = " [голосовое]" if e.kind == "voice" else ""
        lines.append(f"{e.author}{mark}: {e.text}")
    return "\n".join(lines)


async def summarize(entries: list[Entry], language: str) -> dict:
    prompt = (
        f"Язык ответа: {language}.\n\n"
        f"<dialog>\n{format_dialog(entries)}\n</dialog>"
    )
    try:
        async with client.beta.messages.stream(
            model=MODEL,
            max_tokens=16000,
            system=SYSTEM,
            messages=[{"role": "user", "content": prompt}],
            output_config={"effort": "medium", "format": {"type": "json_schema", "schema": SCHEMA}},
            betas=["server-side-fallback-2026-07-01"],
            fallbacks="default",
        ) as stream:
            response = await stream.get_final_message()
    except anthropic.RateLimitError as e:
        raise SummaryError("Слишком много запросов к Claude, попробуйте через минуту.") from e
    except anthropic.AuthenticationError as e:
        raise SummaryError("Неверный ANTHROPIC_API_KEY.") from e
    except anthropic.APIStatusError as e:
        raise SummaryError(f"Ошибка Claude API ({e.status_code}).") from e
    except anthropic.APIConnectionError as e:
        raise SummaryError("Нет связи с Claude API.") from e

    if response.stop_reason == "refusal":
        raise SummaryError("Claude отказался обрабатывать этот диалог.")
    if response.stop_reason == "max_tokens":
        raise SummaryError("Диалог слишком длинный для одной выжимки — сделайте /clear и начните заново.")
    text = next(b.text for b in response.content if b.type == "text")
    return json.loads(text)


def render(data: dict) -> str:
    """Telegram-HTML. Весь текст от модели экранируется."""
    e = html.escape
    out = ["📋 <b>Выжимка диалога</b>", "", e(data["summary"])]

    if data["participants"]:
        out += ["", "👥 <b>Участники</b>"]
        out += [f"• <b>{e(p['name'])}</b> — {e(p['position'])}" for p in data["participants"]]

    if data["tasks"]:
        out += ["", "✅ <b>Задачи</b>"]
        for i, t in enumerate(data["tasks"], 1):
            meta = [m for m in (t["owner"] and f"👤 {e(t['owner'])}",
                                t["deadline"] and f"⏰ {e(t['deadline'])}") if m]
            line = f"{i}. {e(t['task'])}"
            if meta:
                line += f"\n    <i>{' · '.join(meta)}</i>"
            out.append(line)

    if data["options"]:
        out += ["", "🧭 <b>Варианты решения</b>"]
        for i, o in enumerate(data["options"], 1):
            out += ["", f"<b>{i}. {e(o['title'])}</b>", e(o["description"])]
            out += [f"  ➕ {e(b)}" for b in o["benefits"]]
            out += [f"  ⚠️ {e(r)}" for r in o["risks"]]

    if data["decision"]:
        out += ["", "🏁 <b>Решение</b>", e(data["decision"])]

    if data["open_questions"]:
        out += ["", "❓ <b>Открытые вопросы</b>"]
        out += [f"• {e(q)}" for q in data["open_questions"]]

    return "\n".join(out)


def split_message(text: str, limit: int = 4000) -> list[str]:
    """Режет по строкам, чтобы не превысить лимит Telegram и не разорвать HTML-теги."""
    chunks, cur = [], ""
    for line in text.split("\n"):
        if cur and len(cur) + len(line) + 1 > limit:
            chunks.append(cur)
            cur = ""
        cur = f"{cur}\n{line}" if cur else line
    if cur:
        chunks.append(cur)
    return chunks
