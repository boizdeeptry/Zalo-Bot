#!/usr/bin/env python3
"""SQLite evidence and fake-fixture helper for the Memory V2 deployment tools.

All inspection paths use SQLite's read-only immutable URI and verify DB/WAL/SHM
metadata before and after each read. Mutating commands are limited to the explicit
canary-copy fixture path supplied by the caller.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sqlite3
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Iterable


RUN_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{5,80}$")


def canonical_json(value: Any) -> bytes:
    return json.dumps(
        value, ensure_ascii=True, separators=(",", ":"), sort_keys=True
    ).encode("utf-8")


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest().upper()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while block := handle.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest().upper()


def companion_paths(db_path: Path) -> list[Path]:
    return [db_path, Path(str(db_path) + "-wal"), Path(str(db_path) + "-shm")]


def metadata_snapshot(db_path: Path) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for path in companion_paths(db_path):
        name = "db" if path == db_path else ("wal" if str(path).endswith("-wal") else "shm")
        if path.exists():
            stat = path.stat()
            result[name] = {
                "exists": True,
                "size": stat.st_size,
                "mtimeUtcNs": stat.st_mtime_ns,
                "sha256": sha256_file(path),
            }
        else:
            result[name] = {
                "exists": False,
                "size": 0,
                "mtimeUtcNs": 0,
                "sha256": "",
            }
    return result


def immutable_uri(path: Path) -> str:
    return path.resolve(strict=True).as_uri() + "?mode=ro&immutable=1"


def schema_version(connection: sqlite3.Connection) -> int:
    row = connection.execute(
        "SELECT value FROM app_meta WHERE key='schema_version'"
    ).fetchone()
    if row is None:
        raise RuntimeError("app_meta.schema_version is missing")
    return int(row[0])


def table_exists(connection: sqlite3.Connection, name: str) -> bool:
    return (
        connection.execute(
            "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", (name,)
        ).fetchone()[0]
        == 1
    )


def stable_rows(connection: sqlite3.Connection) -> dict[str, list[list[Any]]]:
    specs: list[tuple[str, str, str]] = [
        (
            "memory",
            "zalo_memory",
            "id,thread_id,uid,text,created_at,pinned,source,source_message_id,updated_at",
        ),
        (
            "lessons",
            "zalo_lessons",
            "id,thread_id,bot_text,better,note,created_at,pinned,updated_at",
        ),
        (
            "sessions",
            "app_zalo_cli_sessions",
            "thread_id,claude_session_id,generation,model,prompt_fingerprint,"
            "context_tokens,turn_count,message_cursor,rotate_before_next,last_error,"
            "created_at,updated_at,memory_revision,lessons_revision",
        ),
    ]
    output: dict[str, list[list[Any]]] = {}
    for label, table, columns in specs:
        if not table_exists(connection, table):
            output[label] = []
            continue
        order = "id" if table != "app_zalo_cli_sessions" else "thread_id"
        output[label] = [
            list(row)
            for row in connection.execute(
                f"SELECT {columns} FROM {table} ORDER BY {order}"
            ).fetchall()
        ]
    return output


def inspect_database(db_path: Path, include_stable: bool = False) -> dict[str, Any]:
    db_path = db_path.resolve(strict=True)
    before = metadata_snapshot(db_path)
    connection = sqlite3.connect(immutable_uri(db_path), uri=True)
    try:
        quick_rows = [row[0] for row in connection.execute("PRAGMA quick_check")]
        quick = "ok" if quick_rows == ["ok"] else "; ".join(str(x) for x in quick_rows)
        version = schema_version(connection)
        stable = stable_rows(connection) if include_stable else None
    finally:
        connection.close()
    after = metadata_snapshot(db_path)
    if before != after:
        raise RuntimeError("immutable SQLite read changed DB/WAL/SHM baseline")
    result: dict[str, Any] = {
        "dbPath": str(db_path),
        "dbSha256": before["db"]["sha256"],
        "schema": version,
        "quickCheck": quick,
        "baselineStable": True,
        "baseline": before,
        "baselineSha256": sha256_bytes(canonical_json(before)),
    }
    if stable is not None:
        result["stable"] = {
            label: {
                "count": len(rows),
                "digest": sha256_bytes(canonical_json(rows)),
            }
            for label, rows in stable.items()
        }
        result["stableRows"] = stable
    return result


def require_inspection(result: dict[str, Any], expected_schema: int) -> None:
    if result["quickCheck"] != "ok":
        raise RuntimeError(f"quick_check failed: {result['quickCheck']}")
    if result["schema"] != expected_schema:
        raise RuntimeError(
            f"schema mismatch: expected {expected_schema}, got {result['schema']}"
        )
    if not result["baselineStable"]:
        raise RuntimeError("DB/WAL/SHM baseline was not stable")


def command_inspect(args: argparse.Namespace) -> dict[str, Any]:
    result = inspect_database(Path(args.db), include_stable=args.include_stable)
    require_inspection(result, args.expected_schema)
    result.pop("stableRows", None)
    return result


def command_inspect_live(args: argparse.Namespace) -> dict[str, Any]:
    db_path = Path(args.db).resolve(strict=True)
    connection = sqlite3.connect(db_path.as_uri() + "?mode=ro", uri=True, timeout=5)
    try:
        quick_rows = [row[0] for row in connection.execute("PRAGMA quick_check")]
        quick = "ok" if quick_rows == ["ok"] else "; ".join(str(x) for x in quick_rows)
        version = schema_version(connection)
    finally:
        connection.close()
    result = {"dbPath": str(db_path), "schema": version, "quickCheck": quick}
    if quick != "ok" or version != args.expected_schema:
        raise RuntimeError(
            f"live database mismatch: quick_check={quick}, schema={version}, "
            f"expected={args.expected_schema}"
        )
    return result


def command_compare(args: argparse.Namespace) -> dict[str, Any]:
    source = inspect_database(Path(args.source_db), include_stable=True)
    migrated = inspect_database(Path(args.migrated_db), include_stable=True)
    require_inspection(source, args.expected_source_schema)
    require_inspection(migrated, args.expected_target_schema)
    equal = source["stableRows"] == migrated["stableRows"]
    if not equal:
        raise RuntimeError("stable Memory/lessons/session fields changed during migration")
    comparison = {
        "source": source["stable"],
        "migrated": migrated["stable"],
        "equal": True,
    }
    return {
        "sourceSchema": source["schema"],
        "migratedSchema": migrated["schema"],
        "sourceBaselineSha256": source["baselineSha256"],
        "migratedBaselineSha256": migrated["baselineSha256"],
        "stable": comparison,
        "comparisonSha256": sha256_bytes(canonical_json(comparison)),
    }


def iso_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def fixture_memory(
    connection: sqlite3.Connection,
    *,
    thread_id: str,
    uid: str,
    key: str,
    category: str,
    text: str,
    status: str,
    action: str = "",
    supersedes_id: int = 0,
    pinned: int = 0,
    source_message_id: int = 0,
    expired: bool = False,
) -> int:
    now = iso_now()
    expires = (
        (datetime.now(timezone.utc) - timedelta(days=1))
        .isoformat(timespec="microseconds")
        .replace("+00:00", "Z")
        if expired
        else (
            datetime.now(timezone.utc) + timedelta(days=30)
        ).isoformat(timespec="microseconds").replace("+00:00", "Z")
    )
    cursor = connection.execute(
        """INSERT INTO zalo_memory(
thread_id,uid,text,created_at,pinned,source,source_message_id,updated_at,
memory_key,category,confidence,status,proposal_action,supersedes_id,
last_confirmed_at,expires_at)
VALUES(?,?,?,?,?,'operator',?,?,?,?,1,?,?,?,?,?)""",
        (
            thread_id,
            uid,
            text,
            now,
            pinned,
            source_message_id,
            now,
            key,
            category,
            status,
            action,
            supersedes_id,
            now,
            None if pinned else expires,
        ),
    )
    return int(cursor.lastrowid)


def command_seed(args: argparse.Namespace) -> dict[str, Any]:
    if not RUN_ID_RE.fullmatch(args.run_id):
        raise RuntimeError("run-id must be an opaque lower-case identifier")
    db_path = Path(args.db).resolve(strict=True)
    # A stopped canary at the expected target schema is the only valid mutation target.
    before = inspect_database(db_path)
    require_inspection(before, args.expected_schema)
    group = f"{args.run_id}-group"
    member_a = f"{args.run_id}-a"
    member_b = f"{args.run_id}-b"
    private = f"{args.run_id}-private"
    now = iso_now()
    connection = sqlite3.connect(str(db_path))
    try:
        connection.execute("PRAGMA foreign_keys=ON")
        connection.execute("BEGIN IMMEDIATE")
        connection.execute(
            "INSERT INTO zalo_threads(id,name,status,thread_type,updated_at) VALUES(?,?,'manual','group',?)",
            (group, "Canary Group", now),
        )
        connection.execute(
            "INSERT INTO zalo_threads(id,name,status,thread_type,updated_at) VALUES(?,?,'manual','user',?)",
            (private, "Canary Private", now),
        )
        for uid in (member_a, member_b):
            connection.execute(
                """INSERT INTO zalo_group_members(
group_id,uid,role,first_seen_at,interaction_count,last_interaction,synced_at)
VALUES(?,?,'member',?,1,?,?)""",
                (group, uid, now, now, now),
            )
        common = fixture_memory(
            connection,
            thread_id=group,
            uid="",
            key="canary.common",
            category="preference",
            text="CANARY-COMMON",
            status="active",
        )
        old_a = fixture_memory(
            connection,
            thread_id=group,
            uid=member_a,
            key="canary.color",
            category="preference",
            text="CANARY-A-OLD",
            status="active",
        )
        pending_a = fixture_memory(
            connection,
            thread_id=group,
            uid=member_a,
            key="canary.color",
            category="preference",
            text="CANARY-A-NEW",
            status="pending",
            action="replace",
            supersedes_id=old_a,
        )
        active_b = fixture_memory(
            connection,
            thread_id=group,
            uid=member_b,
            key="canary.member",
            category="preference",
            text="CANARY-B-ACTIVE",
            status="active",
        )
        sensitive_b = fixture_memory(
            connection,
            thread_id=group,
            uid=member_b,
            key="canary.sensitive",
            category="health",
            text="CANARY-B-PENDING-SENSITIVE",
            status="pending",
            action="add",
        )
        expired_a = fixture_memory(
            connection,
            thread_id=group,
            uid=member_a,
            key="canary.expired",
            category="interest",
            text="CANARY-A-EXPIRED",
            status="expired",
            expired=True,
        )
        message_ids: list[int] = []
        for index in (1, 2):
            cursor = connection.execute(
                """INSERT INTO zalo_messages(
thread_id,direction,author,author_uid,zalo_msgid,body,created_at)
VALUES(?,'in','Canary',?,?,?,?)""",
                (private, private, f"{args.run_id}-msg-{index}", f"CANARY-MSG-{index}", now),
            )
            message_ids.append(int(cursor.lastrowid))
        lineage_old = fixture_memory(
            connection,
            thread_id=private,
            uid=private,
            key="canary.lineage",
            category="profile",
            text="CANARY-PRIVATE-OLD",
            status="superseded",
            source_message_id=message_ids[0],
        )
        lineage_active = fixture_memory(
            connection,
            thread_id=private,
            uid=private,
            key="canary.lineage",
            category="profile",
            text="CANARY-PRIVATE-ACTIVE",
            status="active",
            supersedes_id=lineage_old,
            source_message_id=message_ids[1],
        )
        for thread_id, uid, revision in (
            (group, "", 1),
            (group, member_a, 1),
            (group, member_b, 1),
            (private, private, 1),
        ):
            connection.execute(
                """INSERT INTO app_memory_subject_revisions(thread_id,uid,revision)
VALUES(?,?,?) ON CONFLICT(thread_id,uid) DO UPDATE SET revision=excluded.revision""",
                (thread_id, uid, revision),
            )
        connection.execute(
            """INSERT INTO app_zalo_cli_sessions(
thread_id,claude_session_id,generation,model,prompt_fingerprint,created_at,updated_at,
memory_subject_uid,memory_subject_revision,memory_common_revision)
VALUES(?, ?,1,'canary','canary',?,?,?,1,1)""",
            (group, f"{args.run_id}-session", now, now, member_a),
        )
        connection.commit()
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()
    return {
        "runId": args.run_id,
        "group": group,
        "memberA": member_a,
        "memberB": member_b,
        "private": private,
        "ids": {
            "common": common,
            "oldA": old_a,
            "pendingA": pending_a,
            "activeB": active_b,
            "sensitiveB": sensitive_b,
            "expiredA": expired_a,
            "lineageOld": lineage_old,
            "lineageActive": lineage_active,
            "messages": message_ids,
        },
    }


def scalar(connection: sqlite3.Connection, sql: str, values: Iterable[Any]) -> Any:
    row = connection.execute(sql, tuple(values)).fetchone()
    if row is None:
        raise RuntimeError("assertion query returned no row")
    return row[0]


def command_assert_final(args: argparse.Namespace) -> dict[str, Any]:
    fixture = json.loads(Path(args.fixture).read_text(encoding="utf-8"))
    db_path = Path(args.db).resolve(strict=True)
    before = metadata_snapshot(db_path)
    connection = sqlite3.connect(immutable_uri(db_path), uri=True)
    try:
        require = []
        ids = fixture["ids"]
        group = fixture["group"]
        member_a = fixture["memberA"]
        member_b = fixture["memberB"]
        private = fixture["private"]
        require.append(
            scalar(connection, "SELECT status FROM zalo_memory WHERE id=?", [ids["oldA"]])
            == "superseded"
        )
        require.append(
            scalar(connection, "SELECT status FROM zalo_memory WHERE id=?", [ids["pendingA"]])
            == "active"
        )
        require.append(
            scalar(connection, "SELECT COUNT(*) FROM zalo_memory WHERE id=?", [ids["sensitiveB"]])
            == 0
        )
        restored = connection.execute(
            "SELECT status,pinned,expires_at FROM zalo_memory WHERE id=?", (ids["expiredA"],)
        ).fetchone()
        require.append(restored == ("active", 1, None))
        require.append(
            scalar(
                connection,
                "SELECT COUNT(*) FROM zalo_memory WHERE thread_id=? AND memory_key='canary.lineage'",
                [private],
            )
            == 0
        )
        placeholders = ",".join("?" for _ in ids["messages"])
        require.append(
            scalar(
                connection,
                f"SELECT COUNT(*) FROM zalo_messages WHERE id IN ({placeholders})",
                ids["messages"],
            )
            == 2
        )
        revisions = {
            "A": scalar(
                connection,
                "SELECT revision FROM app_memory_subject_revisions WHERE thread_id=? AND uid=?",
                [group, member_a],
            ),
            "B": scalar(
                connection,
                "SELECT revision FROM app_memory_subject_revisions WHERE thread_id=? AND uid=?",
                [group, member_b],
            ),
            "private": scalar(
                connection,
                "SELECT revision FROM app_memory_subject_revisions WHERE thread_id=? AND uid=?",
                [private, private],
            ),
        }
        require.append(revisions == {"A": 4, "B": 1, "private": 2})
        quick_rows = [row[0] for row in connection.execute("PRAGMA quick_check")]
        quick = "ok" if quick_rows == ["ok"] else "; ".join(quick_rows)
        version = schema_version(connection)
    finally:
        connection.close()
    after = metadata_snapshot(db_path)
    if before != after:
        raise RuntimeError("immutable final assertion changed DB/WAL/SHM baseline")
    if not all(require):
        raise RuntimeError("one or more final J1-J6 database assertions failed")
    if quick != "ok" or version != args.expected_schema:
        raise RuntimeError(f"final database failed: quick_check={quick}, schema={version}")
    return {
        "all": True,
        "quickCheck": quick,
        "schema": version,
        "revisions": revisions,
        "dbSha256": before["db"]["sha256"],
        "baselineStable": True,
        "baselineSha256": sha256_bytes(canonical_json(before)),
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    inspect = sub.add_parser("inspect")
    inspect.add_argument("--db", required=True)
    inspect.add_argument("--expected-schema", required=True, type=int)
    inspect.add_argument("--include-stable", action="store_true")
    inspect.set_defaults(handler=command_inspect)

    inspect_live = sub.add_parser("inspect-live")
    inspect_live.add_argument("--db", required=True)
    inspect_live.add_argument("--expected-schema", required=True, type=int)
    inspect_live.set_defaults(handler=command_inspect_live)

    compare = sub.add_parser("compare")
    compare.add_argument("--source-db", required=True)
    compare.add_argument("--migrated-db", required=True)
    compare.add_argument("--expected-source-schema", required=True, type=int)
    compare.add_argument("--expected-target-schema", required=True, type=int)
    compare.set_defaults(handler=command_compare)

    seed = sub.add_parser("seed")
    seed.add_argument("--db", required=True)
    seed.add_argument("--run-id", required=True)
    seed.add_argument("--expected-schema", required=True, type=int)
    seed.set_defaults(handler=command_seed)

    final = sub.add_parser("assert-final")
    final.add_argument("--db", required=True)
    final.add_argument("--fixture", required=True)
    final.add_argument("--expected-schema", required=True, type=int)
    final.set_defaults(handler=command_assert_final)
    return parser


def main() -> int:
    try:
        args = build_parser().parse_args()
        result = args.handler(args)
        sys.stdout.write(json.dumps(result, ensure_ascii=True, separators=(",", ":")))
        sys.stdout.write("\n")
        return 0
    except Exception as exc:  # concise, redacted machine failure; no row contents
        sys.stderr.write(f"memory_v2_sqlite: {type(exc).__name__}: {exc}\n")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
