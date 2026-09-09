from __future__ import annotations

import json
import threading
from datetime import UTC, datetime, timedelta
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

import pytest

from weave_bench.analytics import (
    AnalyticsUnavailable,
    by_session,
    fetch_rows,
    parse_ndjson,
    parse_rfc3339,
    row_cost_usd,
    row_fresh_input_tokens,
    write_ndjson,
)

WINDOW_START = datetime(2026, 9, 6, 12, 0, tzinfo=UTC)
WINDOW_END = WINDOW_START + timedelta(hours=1)


def _row(row_id: int, requested_at: datetime, session: str = "s1") -> dict[str, object]:
    return {
        "id": row_id,
        "session_id": session,
        "requested_at": requested_at.strftime("%Y-%m-%dT%H:%M:%S.%fZ"),
        "decision_model": "gpt-5.6-sol",
        "actual_input_cost_usd": 0.5,
        "actual_output_cost_usd": 0.25,
    }


def test_rfc3339_with_and_without_trailing_z() -> None:
    zulu = parse_rfc3339("2026-09-06T12:00:00.123456Z")
    offset = parse_rfc3339("2026-09-06T14:00:00.123456+02:00")
    naive = parse_rfc3339("2026-09-06T12:00:00.123456")
    assert zulu == offset == naive
    assert zulu.tzinfo is not None


def test_ndjson_round_trip_and_cost(tmp_path: Path) -> None:
    rows = [_row(1, WINDOW_START), {"id": 2, "session_id": "s2", "actual_input_cost_usd": "n/a"}]
    path = tmp_path / "out" / "analytics.ndjson"
    write_ndjson(rows, path)
    parsed = parse_ndjson(path.read_text().splitlines() + ["", "   "])
    assert parsed == rows
    assert row_cost_usd(parsed[0]) == pytest.approx(0.75)
    assert row_cost_usd(parsed[1]) == 0.0


def test_fresh_input_tokens_follow_the_provider_usage_convention() -> None:
    usage = {"input_tokens": 1000, "cache_read_tokens": 600, "cache_creation_tokens": 100}
    assert row_fresh_input_tokens({**usage, "decision_provider": "openai"}) == 300
    assert row_fresh_input_tokens({**usage, "decision_provider": "google"}) == 300
    assert row_fresh_input_tokens({**usage, "decision_provider": "anthropic"}) == 1000
    assert row_fresh_input_tokens({"input_tokens": 10, "cache_read_tokens": 50, "decision_provider": "openai"}) == 0


def test_by_session_dedupes_rows_repeated_across_pages() -> None:
    first, second = _row(1, WINDOW_START), _row(2, WINDOW_START, session="s2")
    grouped = by_session([first, second, first])
    assert grouped == {"s1": [first], "s2": [second]}


class _PagedExport(BaseHTTPRequestHandler):
    """Two pages: the first needs ``since``/``until``, the second only ``cursor``."""

    requests: list[dict[str, list[str]]] = []
    authorizations: list[str] = []

    def do_GET(self) -> None:  # noqa: N802 (http.server API)
        query = parse_qs(urlsplit(self.path).query)
        type(self).requests.append(query)
        type(self).authorizations.append(self.headers.get("Authorization", ""))
        if "cursor" not in query:
            rows = [_row(1, WINDOW_START + timedelta(minutes=5)), _row(2, WINDOW_START - timedelta(minutes=1))]
            next_cursor, has_more = "page-2", "true"
        else:
            rows = [_row(3, WINDOW_END), _row(4, WINDOW_END + timedelta(seconds=1))]
            next_cursor, has_more = "", "false"
        body = "".join(json.dumps(row) + "\n" for row in rows).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/x-ndjson")
        self.send_header("X-Weave-Next-Cursor", next_cursor)
        self.send_header("X-Weave-Has-More", has_more)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_: object) -> None:
        return


@pytest.fixture
def export_server():
    _PagedExport.requests = []
    _PagedExport.authorizations = []
    server = HTTPServer(("127.0.0.1", 0), _PagedExport)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{server.server_port}"
    server.shutdown()


def test_fetch_rows_pages_with_cursor_and_filters_to_window(export_server: str) -> None:
    slept: list[float] = []
    rows = fetch_rows(
        base_url=export_server,
        api_key="ra_test",
        window_start=WINDOW_START,
        window_end=WINDOW_END,
        now=lambda: WINDOW_END + timedelta(seconds=30),
        sleep=slept.append,
    )
    assert [row["id"] for row in rows] == [1, 3]
    assert slept == [pytest.approx(60.0)]
    first, second = _PagedExport.requests
    assert set(first) == {"since", "until", "limit"}
    assert parse_rfc3339(first["since"][0]) < WINDOW_START
    assert parse_rfc3339(first["until"][0]) > WINDOW_END
    assert second == {"cursor": ["page-2"], "limit": first["limit"]}
    assert _PagedExport.authorizations == ["Bearer ra_test", "Bearer ra_test"]


def test_fetch_rows_unreachable_is_reported_not_fabricated() -> None:
    with pytest.raises(AnalyticsUnavailable):
        fetch_rows(
            base_url="http://127.0.0.1:9",
            api_key="ra_test",
            window_start=WINDOW_START,
            window_end=WINDOW_END,
            now=lambda: WINDOW_END + timedelta(hours=1),
            sleep=lambda _: None,
        )
