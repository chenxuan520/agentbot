#!/usr/bin/env python3

from __future__ import annotations

import json
import os
import re
import sys
import time
from urllib import error, parse, request


DUTY_MEMBER_CACHE_RELATIVE_PATH = ".agents/runtime/duty-member-openids.json"
DUTY_MEMBER_CACHE_TTL_SECONDS = 7 * 24 * 60 * 60
SPACE_RE = re.compile(r"\s+")
MENTION_NAME_RE = re.compile(r"@([^\s@`*_~|\"'“”‘’，,。:：;；!！?？()\[\]{}<>《》]+)")
MENTION_BOUNDARY_CHARS = "`*_~|\"'“”‘’，,。:：;；!！?？()[]{}<>《》"
MENTION_PLACEHOLDER_PREFIX = "__agent_bot_mention_"
MENTION_PLACEHOLDER_SUFFIX = "__"
HEADING_WITH_LEADING_MENTIONS_RE = re.compile(r'^\s*((?:<at user_id="[^"]+">[^<]+</at>\s*)+)(#{1,6}\s+.+)$')
PURE_MENTION_CODE_SPAN_RE = re.compile(r'`((?:<at user_id="[^"]+">[^<]+</at>\s*)+)`')


def compact_text(value: object) -> str:
    text = str(value or "")
    return SPACE_RE.sub(" ", text.replace("\n", " ")).strip()


def as_dict(value: object) -> dict[str, object]:
    if not isinstance(value, dict):
        return {}
    result: dict[str, object] = {}
    for key, item in value.items():
        result[str(key)] = item
    return result


def as_list(value: object) -> list[object]:
    if isinstance(value, list):
        return value
    return []


def workspace_path() -> str:
    return os.environ.get("AGENT_BOT_WORKSPACE", "").strip()


def duty_member_cache_path() -> str:
    workspace = workspace_path()
    if not workspace:
        return DUTY_MEMBER_CACHE_RELATIVE_PATH
    return os.path.join(workspace, DUTY_MEMBER_CACHE_RELATIVE_PATH)


def load_json_dict(path: str) -> dict[str, object]:
    try:
        with open(path, "r", encoding="utf-8") as f:
            data = json.load(f)
    except FileNotFoundError:
        return {}
    except (OSError, json.JSONDecodeError):
        return {}
    return as_dict(data)


def save_json_dict(path: str, data: dict[str, object]) -> None:
    parent = os.path.dirname(path)
    if parent:
        os.makedirs(parent, exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)


def prune_member_cache(cache: dict[str, object], now: float) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in cache.items():
        item = as_dict(value)
        expires_at = item.get("expiresAt")
        if isinstance(expires_at, (int, float)) and now < float(expires_at):
            result[str(key)] = item
    return result


def load_member_cache() -> dict[str, object]:
    cache = load_json_dict(duty_member_cache_path())
    return prune_member_cache(cache, time.time())


def save_member_cache(cache: dict[str, object]) -> None:
    save_json_dict(duty_member_cache_path(), prune_member_cache(cache, time.time()))


def member_cache_key(conversation_id: str, member_name: str) -> str:
    return conversation_id + "\n" + member_name


def load_cached_open_id(conversation_id: str, member_name: str) -> str:
    cache = load_member_cache()
    item = as_dict(cache.get(member_cache_key(conversation_id, member_name), {}))
    return compact_text(item.get("openId", ""))


def save_cached_open_id(conversation_id: str, member_name: str, open_id: str) -> None:
    if not conversation_id or not member_name or not open_id:
        return
    cache = load_member_cache()
    cache[member_cache_key(conversation_id, member_name)] = {
        "conversationId": conversation_id,
        "memberName": member_name,
        "openId": open_id,
        "expiresAt": time.time() + DUTY_MEMBER_CACHE_TTL_SECONDS,
    }
    save_member_cache(cache)


def unique_open_id_from_members(members: list[dict[str, object]], member_name: str) -> str:
    matches: list[str] = []
    for member in members:
        if compact_text(member.get("name", "")) != member_name:
            continue
        open_id = compact_text(member.get("memberId", ""))
        if open_id:
            matches.append(open_id)
    matches = sorted(set(matches))
    if len(matches) != 1:
        return ""
    return matches[0]


def unique_open_ids_from_members(members: list[dict[str, object]], member_names: list[str]) -> list[str]:
    open_ids: list[str] = []
    seen_names: set[str] = set()
    for member_name in member_names:
        member_name = compact_text(member_name)
        if not member_name or member_name in seen_names:
            continue
        open_id = unique_open_id_from_members(members, member_name)
        if not open_id:
            return []
        open_ids.append(open_id)
        seen_names.add(member_name)
    return open_ids


def local_api_base_url() -> str:
    base_url = os.environ.get("AGENT_BOT_API_BASE_URL", "").strip()
    return base_url.rstrip("/")


def fetch_chat_members(provider_name: str, conversation_id: str) -> list[dict[str, object]]:
    base_url = local_api_base_url()
    if not base_url or not provider_name or not conversation_id:
        return []

    query = parse.urlencode({"provider": provider_name, "conversationId": conversation_id})
    url = f"{base_url}/api/v1/provider/chat-members?{query}"
    req = request.Request(url, method="GET")
    try:
        with request.urlopen(req, timeout=5) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
    except (error.URLError, TimeoutError, json.JSONDecodeError, OSError):
        return []

    items = as_list(as_dict(payload).get("items", []))
    result: list[dict[str, object]] = []
    for item in items:
        member = as_dict(item)
        if member:
            result.append(member)
    return result


def resolve_member_open_id(provider_name: str, conversation_id: str, member_name: str) -> str:
    member_name = compact_text(member_name)
    if not member_name:
        return ""

    cached = load_cached_open_id(conversation_id, member_name)
    if cached:
        return cached

    members = fetch_chat_members(provider_name, conversation_id)
    open_id = unique_open_id_from_members(members, member_name)
    if not open_id:
        return ""

    save_cached_open_id(conversation_id, member_name, open_id)
    return open_id


def is_mention_boundary(value: str) -> bool:
    if not value:
        return True
    return value.isspace() or value in MENTION_BOUNDARY_CHARS


def extract_mention_names(reply_text: str) -> list[str]:
    names: list[str] = []
    seen: set[str] = set()
    for matched in MENTION_NAME_RE.findall(str(reply_text or "")):
        name = compact_text(matched)
        if not name or name in seen:
            continue
        names.append(name)
        seen.add(name)
    return names


def match_member_names_in_reply(reply_text: str, members: list[dict[str, object]]) -> list[str]:
    text = str(reply_text or "")
    member_names = sorted(
        {
            compact_text(member.get("name", ""))
            for member in members
            if compact_text(member.get("name", ""))
        },
        key=len,
        reverse=True,
    )
    names: list[str] = []
    seen: set[str] = set()
    for index, char in enumerate(text):
        if char != "@":
            continue
        suffix = text[index + 1 :]
        for member_name in member_names:
            if not suffix.startswith(member_name):
                continue
            next_char = suffix[len(member_name) : len(member_name) + 1]
            if not is_mention_boundary(next_char):
                continue
            if member_name not in seen:
                names.append(member_name)
                seen.add(member_name)
            break
    return names


def replace_mentions_with_md_at(reply_text: str, member_names: list[str], open_ids: list[str]) -> str:
    rewritten = str(reply_text or "")
    for member_name, open_id in zip(member_names, open_ids):
        tag = f'<at user_id="{open_id}">{member_name}</at>'
        pattern = re.compile(r"@" + re.escape(member_name) + r"(?=$|[\s" + re.escape(MENTION_BOUNDARY_CHARS) + r"])")
        rewritten = pattern.sub(tag, rewritten)
        rewritten = rewritten.replace("`" + tag + "`", tag)
    rewritten = PURE_MENTION_CODE_SPAN_RE.sub(r"\1", rewritten)
    return rewritten.strip()


def normalize_heading_lines(reply_text: str) -> str:
    lines = str(reply_text or "").splitlines()
    normalized: list[str] = []
    for line in lines:
        matched = HEADING_WITH_LEADING_MENTIONS_RE.match(line.strip())
        if not matched:
            normalized.append(line)
            continue
        normalized.append(matched.group(2).strip())
        normalized.append(matched.group(1).strip())
    return "\n".join(normalized).strip()


def build_mention_result(reply_text: str, member_names: list[str], open_ids: list[str]) -> dict[str, object]:
    if not member_names or len(member_names) != len(open_ids):
        return {}
    rewritten = normalize_heading_lines(replace_mentions_with_md_at(reply_text, member_names, open_ids))
    if not rewritten or rewritten == reply_text.strip():
        return {}
    return {"replyText": rewritten}


def maybe_build_result(payload: dict[str, object]) -> dict[str, str]:
    provider_name = compact_text(payload.get("provider", ""))
    conversation_id = compact_text(payload.get("conversationId", ""))
    reply_text = str(payload.get("replyText", "") or "")
    if not provider_name or not conversation_id or not reply_text:
        return {}

    names = extract_mention_names(reply_text)
    member_name = names[0] if len(names) == 1 else ""
    if member_name:
        open_id = resolve_member_open_id(provider_name, conversation_id, member_name)
        if open_id:
            return build_mention_result(reply_text, [member_name], [open_id])

    members = fetch_chat_members(provider_name, conversation_id)
    names = match_member_names_in_reply(reply_text, members)
    if not names:
        return {}

    open_ids = unique_open_ids_from_members(members, names)
    if len(open_ids) != len(names):
        return {}
    for member_name, open_id in zip(names, open_ids):
        save_cached_open_id(conversation_id, member_name, open_id)
    return build_mention_result(reply_text, names, open_ids)


def main() -> int:
    raw = sys.stdin.read()
    if not raw.strip():
        return 0

    try:
        payload = as_dict(json.loads(raw))
    except json.JSONDecodeError:
        return 0

    result = maybe_build_result(payload)
    if result:
        sys.stdout.write(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
