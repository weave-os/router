"""Pull ``/v1/analytics/routing-decisions`` rows for a run (docs/ANALYTICS_EXPORT.md).

The export is the router-side evidence: one row per upstream model call with the
served ``decision_model`` and the provider-billed ``actual_*_cost_usd``. Rows
are attributed to trials by ``session_id`` (Codex's thread id, which Codex sends
as ``Session-Id``), never by wall-clock overlap, so concurrent arms don't
double-count. Requires an ``ra_`` analytics key for the installation the run
used; without one, ``--analytics-ndjson`` replays a saved dump.
"""

from __future__ import annotations

import json
import time
import urllib.parse
import urllib.request
from collections import defaultdict
from collections.abc import Callable, Iterable
from datetime import UTC, datetime, timedelta
from enum import StrEnum
from pathlib import Path

DECISIONS_PATH = "/v1/analytics/routing-decisions"
PAGE_LIMIT = 10000
MAX_PAGES = 500
REQUEST_TIMEOUT_S = 120.0
# Rows are filtered on ingest time but a trial's window is event time, and
# telemetry lands after a turn completes; pad generously, over-fetching is free.
INGEST_LAG_PAD = timedelta(minutes=30)
# Published holdback plus clock-skew margin.
EXPORT_HOLDBACK = timedelta(seconds=90)
NEXT_CURSOR_HEADER = "X-Weave-Next-Cursor"
HAS_MORE_HEADER = "X-Weave-Has-More"
# ``decision_provider`` value whose ``input_tokens`` already excludes cached
# tokens; every other upstream reports a cache-inclusive prompt count
# (mirrors ``catalog.EffectiveInputCost``).
FRESH_INPUT_PROVIDER = "anthropic"


class AnalyticsColumn(StrEnum):
    ID = "id"
    SESSION_ID = "session_id"
    REQUESTED_AT = "requested_at"
    DECISION_MODEL = "decision_model"
    DECISION_PROVIDER = "decision_provider"
    INPUT_TOKENS = "input_tokens"
    OUTPUT_TOKENS = "output_tokens"
    CACHE_CREATION_TOKENS = "cache_creation_tokens"
    CACHE_READ_TOKENS = "cache_read_tokens"
    ACTUAL_INPUT_COST_USD = "actual_input_cost_usd"
    ACTUAL_OUTPUT_COST_USD = "actual_output_cost_usd"


DecisionRow = dict[str, object]


class AnalyticsUnavailable(RuntimeError):
    """The export could not be read; router-billed figures stay blank."""


def _as_utc(value: datetime) -> datetime:
    return value.replace(tzinfo=UTC) if value.tzinfo is None else value


def parse_rfc3339(value: str) -> datetime:
    return _as_utc(datetime.fromisoformat(value[:-1] + "+00:00" if value.endswith("Z") else value))


def row_cost_usd(row: DecisionRow) -> float:
    total = 0.0
    for column in (AnalyticsColumn.ACTUAL_INPUT_COST_USD, AnalyticsColumn.ACTUAL_OUTPUT_COST_USD):
        value = row.get(column)
        if isinstance(value, (int, float)):
            total += float(value)
    return total


def row_int(row: DecisionRow, column: AnalyticsColumn) -> int:
    value = row.get(column)
    return int(value) if isinstance(value, (int, float)) else 0


def row_fresh_input_tokens(row: DecisionRow) -> int:
    """Input tokens billed at the base rate, i.e. net of cache writes and reads."""
    input_tokens = row_int(row, AnalyticsColumn.INPUT_TOKENS)
    if row.get(AnalyticsColumn.DECISION_PROVIDER) == FRESH_INPUT_PROVIDER:
        return input_tokens
    cached = row_int(row, AnalyticsColumn.CACHE_CREATION_TOKENS) + row_int(row, AnalyticsColumn.CACHE_READ_TOKENS)
    return max(input_tokens - cached, 0)


def parse_ndjson(lines: Iterable[str]) -> list[DecisionRow]:
    rows: list[DecisionRow] = []
    for line in lines:
        if not line.strip():
            continue
        parsed = json.loads(line)
        if isinstance(parsed, dict):
            rows.append(parsed)
    return rows


def _page_url(base_url: str, query: dict[str, str]) -> str:
    return f"{base_url.rstrip('/')}{DECISIONS_PATH}?{urllib.parse.urlencode(query)}"


def _fetch_page(url: str, api_key: str) -> tuple[list[DecisionRow], str, bool]:
    request = urllib.request.Request(url, headers={"Authorization": f"Bearer {api_key}"})
    try:
        with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT_S) as response:
            body = response.read().decode("utf-8")
            cursor = response.headers.get(NEXT_CURSOR_HEADER, "")
            has_more = response.headers.get(HAS_MORE_HEADER, "") == "true"
    except (OSError, UnicodeDecodeError) as exc:
        raise AnalyticsUnavailable(f"analytics export request failed: {exc}") from exc
    try:
        return parse_ndjson(body.splitlines()), cursor, has_more
    except json.JSONDecodeError as exc:
        raise AnalyticsUnavailable(f"analytics export returned malformed NDJSON: {exc}") from exc


def fetch_rows(
    *,
    base_url: str,
    api_key: str,
    window_start: datetime,
    window_end: datetime,
    now: Callable[[], datetime] = lambda: datetime.now(UTC),
    sleep: Callable[[float], None] = time.sleep,
) -> list[DecisionRow]:
    """Every row whose ``requested_at`` lies in ``[window_start, window_end]``."""
    window_start, window_end = _as_utc(window_start), _as_utc(window_end)
    remaining_holdback = (window_end + EXPORT_HOLDBACK - now()).total_seconds()
    if remaining_holdback > 0:
        sleep(remaining_holdback)
    url = _page_url(
        base_url,
        {
            "since": (window_start - INGEST_LAG_PAD).isoformat(),
            "until": (window_end + INGEST_LAG_PAD).isoformat(),
            "limit": str(PAGE_LIMIT),
        },
    )
    matched: list[DecisionRow] = []
    for _ in range(MAX_PAGES):
        rows, cursor, has_more = _fetch_page(url, api_key)
        for row in rows:
            requested_at = row.get(AnalyticsColumn.REQUESTED_AT)
            if not isinstance(requested_at, str):
                continue
            if window_start <= parse_rfc3339(requested_at) <= window_end:
                matched.append(row)
        if not has_more or not cursor:
            return matched
        url = _page_url(base_url, {"cursor": cursor, "limit": str(PAGE_LIMIT)})
    raise AnalyticsUnavailable(f"analytics export did not finish paging within {MAX_PAGES} pages")


def by_session(rows: Iterable[DecisionRow]) -> dict[str, list[DecisionRow]]:
    grouped: dict[str, list[DecisionRow]] = defaultdict(list)
    seen_ids: set[object] = set()
    for row in rows:
        row_id = row.get(AnalyticsColumn.ID)
        if row_id in seen_ids:
            continue
        seen_ids.add(row_id)
        grouped[str(row.get(AnalyticsColumn.SESSION_ID, ""))].append(row)
    return grouped


def write_ndjson(rows: Iterable[DecisionRow], path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w") as handle:
        for row in rows:
            handle.write(json.dumps(row, sort_keys=True) + "\n")
