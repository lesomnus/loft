#!/usr/bin/env python3
"""The Grafana dashboards for Rove (rove#38): what Rove sends over OTLP and
what the platform says of itself, read from ClickHouse's OpenTelemetry tables
(the collector's `clickhouse` exporter) and Loki.

    python3 gen.py OUTDIR

Two dashboards, `rove-service.json` and `rove-platform.json`. Each table is
named `${db}.otel_<signal>${suffix}`: a cluster of ClickHouse nodes reads
through its Distributed tables (`_dist`), one node through the tables
themselves (no suffix).
"""

import json
import os
import sys

CH = {"type": "grafana-clickhouse-datasource", "uid": "clickhouse"}
LOKI = {"type": "loki", "uid": "loki"}

LOGS = "${db}.otel_logs${suffix}"
GAUGE = "${db}.otel_metrics_gauge${suffix}"
SUM = "${db}.otel_metrics_sum${suffix}"

# The request log: the second of the two lines every call leaves, for calls
# to Rove's own services (the roster in this process answers on the same
# server, and is not what anybody asked).
CALLS = (f"FROM {LOGS} WHERE $__timeFilter(Timestamp) AND ServiceName = 'rove' AND Body = 'gRPC out' "
         "AND NOT startsWith(LogAttributes.rpc.service::String, 'roster.')")
METHOD = "concat(LogAttributes.rpc.service::String, '/', LogAttributes.rpc.method::String)"
STATUS = "LogAttributes.rpc.grpc.status::String"
ELAPSED_MS = "LogAttributes.server.elapsed_ns::UInt64 / 1e6"
SERVER_ERRORS = "('Internal', 'Unknown', 'Unavailable', 'DeadlineExceeded', 'DataLoss', 'Unimplemented')"

# The app's own warnings and errors: not the request log, whose lines for a
# call that did not end OK are warnings too (they are "OK가 아닌 응답").
WARNINGS = ("ServiceName = 'rove' AND SeverityText IN ('WARN', 'ERROR') "
            "AND Body NOT IN ('gRPC in', 'gRPC out')")


def target(sql, fmt="timeseries", ref="A"):
    return {
        "refId": ref,
        "datasource": CH,
        "editorType": "sql",
        "format": {"timeseries": 0, "table": 1}[fmt],
        "queryType": fmt,
        "rawSql": sql,
    }


def latest(table, metric, by=None, value="argMax(Value, TimeUnix)"):
    """The newest value of a metric in the range, per series if `by`."""
    cols = f"{by} AS series, " if by else ""
    group = " GROUP BY series ORDER BY series" if by else ""
    return f"SELECT {cols}{value} AS value FROM {table} WHERE $__timeFilter(TimeUnix) AND MetricName = '{metric}'{group}"


def series(table, metric, by=None, value="argMax(Value, TimeUnix)", where=""):
    """A metric over time, per interval, per series if `by`."""
    cols = f", {by} AS series" if by else ""
    group = ", series" if by else ""
    return (f"SELECT $__timeInterval(TimeUnix) AS time{cols}, {value} AS value FROM {table} "
            f"WHERE $__timeFilter(TimeUnix) AND MetricName = '{metric}'{where} GROUP BY time{group} ORDER BY time")


class Board:
    def __init__(self, uid, title, description):
        self.uid, self.title, self.description = uid, title, description
        self.panels, self.y, self.x, self.row_h, self.next_id = [], 0, 0, 0, 1

    def row(self, title):
        self.newline()
        self.panels.append({"id": self.next_id, "type": "row", "title": title, "collapsed": False,
                            "gridPos": {"h": 1, "w": 24, "x": 0, "y": self.y}, "panels": []})
        self.next_id += 1
        self.y += 1

    def newline(self):
        if self.x:
            self.y += self.row_h
            self.x, self.row_h = 0, 0

    def add(self, panel, w, h):
        if self.x + w > 24:
            self.newline()
        panel["id"] = self.next_id
        panel["gridPos"] = {"h": h, "w": w, "x": self.x, "y": self.y}
        self.next_id += 1
        self.x += w
        self.row_h = max(self.row_h, h)
        self.panels.append(panel)

    def json(self):
        self.newline()
        return {
            "uid": self.uid,
            "title": self.title,
            "description": self.description,
            "tags": ["rove"],
            "timezone": "browser",
            "editable": True,
            "graphTooltip": 1,
            "refresh": "1m",
            "time": {"from": "now-6h", "to": "now"},
            "schemaVersion": 41,
            "templating": {"list": [
                {"name": "db", "label": "ClickHouse 데이터베이스", "type": "custom", "query": "otel",
                 "current": {"text": "otel", "value": "otel"}, "options": [{"text": "otel", "value": "otel", "selected": True}],
                 "hide": 2},
                {"name": "suffix", "label": "테이블 접미사", "type": "custom", "query": "_dist,none",
                 "current": {"text": "_dist", "value": "_dist"},
                 "options": [{"text": "_dist", "value": "_dist", "selected": True}, {"text": "none", "value": "", "selected": False}],
                 "description": "ClickHouse 노드 여럿이면 분산 테이블(_dist), 하나면 접미사 없음"},
            ]},
            "panels": self.panels,
        }


def timeseries(title, sql, unit="short", desc="", legend="table", stack=False, decimals=None):
    p = {
        "type": "timeseries", "title": title, "description": desc, "datasource": CH,
        "targets": [target(sql)],
        "fieldConfig": {"defaults": {"unit": unit, "custom": {"lineWidth": 1, "fillOpacity": 10,
                                                              "stacking": {"mode": "normal" if stack else "none"}}},
                        "overrides": []},
        "options": {"legend": {"displayMode": legend, "placement": "bottom", "calcs": ["mean", "max", "lastNotNull"]},
                    "tooltip": {"mode": "multi", "sort": "desc"}},
    }
    if decimals is not None:
        p["fieldConfig"]["defaults"]["decimals"] = decimals
    return p


def stat(title, sql, unit="short", desc="", thresholds=None, decimals=None):
    p = {
        "type": "stat", "title": title, "description": desc, "datasource": CH,
        "targets": [target(sql, "table")],
        "fieldConfig": {"defaults": {"unit": unit, "thresholds": {"mode": "absolute", "steps": thresholds or [{"color": "text", "value": None}]}},
                        "overrides": []},
        "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                    "colorMode": "value", "graphMode": "none", "textMode": "auto", "justifyMode": "auto"},
    }
    if decimals is not None:
        p["fieldConfig"]["defaults"]["decimals"] = decimals
    return p


def table(title, sql, desc="", overrides=None):
    return {
        "type": "table", "title": title, "description": desc, "datasource": CH,
        "targets": [target(sql, "table")],
        "fieldConfig": {"defaults": {"custom": {"align": "auto"}}, "overrides": overrides or []},
        "options": {"showHeader": True, "cellHeight": "sm"},
    }


def unit_of(field, unit, decimals=None):
    props = [{"id": "unit", "value": unit}]
    if decimals is not None:
        props.append({"id": "decimals", "value": decimals})
    return {"matcher": {"id": "byName", "options": field}, "properties": props}


def service():
    b = Board("rove-service", "Rove — 서비스",
              "Rove가 OTLP로 보낸 요청 로그와 백그라운드 작업. 무엇을 보는지는 docs/runbook/observe.md")
    green_red = lambda warn, bad: [{"color": "green", "value": None}, {"color": "orange", "value": warn}, {"color": "red", "value": bad}]

    b.row("요청")
    b.add(stat("초당 요청", f"SELECT count() / dateDiff('second', $__fromTime, $__toTime) AS value {CALLS}", "reqps",
               "기간 전체의 평균. 내장 roster로 가는 호출은 뺍니다", decimals=1), 6, 4)
    b.add(stat("서버 쪽 실패",
               f"SELECT countIf({STATUS} IN {SERVER_ERRORS}) / count() AS value {CALLS}", "percentunit",
               "Internal, Unknown, Unavailable, DeadlineExceeded, DataLoss, Unimplemented로 끝난 요청의 비율",
               green_red(0.001, 0.01), 2), 6, 4)
    b.add(stat("p95 지연", f"SELECT quantile(0.95)({ELAPSED_MS}) AS value {CALLS}", "ms",
               "서버가 요청을 처리한 시간(server.elapsed_ns)의 p95. design 1.2: 단건·목록 300ms", green_red(200, 300), 1), 6, 4)
    b.add(stat("경고·오류 로그",
               f"SELECT count() AS value FROM {LOGS} WHERE $__timeFilter(Timestamp) AND {WARNINGS}",
               "short", "기간 안의 WARN·ERROR 로그 수(요청 로그는 뺌). 아래 표와 로그에서 무엇인지 봅니다", green_red(1, 50)), 6, 4)

    b.add(timeseries("메서드별 요청",
                     f"SELECT $__timeInterval(Timestamp) AS time, {METHOD} AS method, count() / $__interval_s AS calls {CALLS} GROUP BY time, method ORDER BY time",
                     "reqps", "초당 요청, 메서드별", stack=True), 12, 9)
    b.add(timeseries("메서드별 p95 지연",
                     f"SELECT $__timeInterval(Timestamp) AS time, {METHOD} AS method, quantile(0.95)({ELAPSED_MS}) AS p95 {CALLS} GROUP BY time, method ORDER BY time",
                     "ms", "서버 처리 시간의 p95. design 1.2: 단건·목록 300ms, 시점 조회(1,000노드) 1초"), 12, 9)
    b.add(timeseries("OK가 아닌 응답",
                     f"SELECT $__timeInterval(Timestamp) AS time, {STATUS} AS status, count() AS calls {CALLS} AND {STATUS} != 'OK' GROUP BY time, status ORDER BY time",
                     "short", "응답 상태별 수. NotFound·PermissionDenied·InvalidArgument는 대개 호출하는 쪽의 일입니다", stack=True), 12, 8)
    b.add(table("메서드별 요약",
                f"SELECT {METHOD} AS method, count() AS calls, quantile(0.5)({ELAPSED_MS}) AS p50_ms, quantile(0.95)({ELAPSED_MS}) AS p95_ms, "
                f"quantile(0.99)({ELAPSED_MS}) AS p99_ms, countIf({STATUS} != 'OK') AS not_ok, countIf({STATUS} IN {SERVER_ERRORS}) AS server_errors "
                f"{CALLS} GROUP BY method ORDER BY calls DESC LIMIT 50",
                "기간 전체",
                [unit_of("p50_ms", "ms", 1), unit_of("p95_ms", "ms", 1), unit_of("p99_ms", "ms", 1)]), 12, 8)

    b.row("백그라운드 작업")
    b.add(timeseries("경고·오류 (내용별)",
                     f"SELECT $__timeInterval(Timestamp) AS time, concat(SeverityText, ' ', Body) AS log, count() AS lines FROM {LOGS} "
                     f"WHERE $__timeFilter(Timestamp) AND {WARNINGS} GROUP BY time, log ORDER BY time",
                     "short", "`spin failed`는 프로세스가 멈춘 것, `tidy`는 주기 작업 한 번의 실패, `` `trail verify` ``는 감사 기록(docs/runbook/observe.md)", stack=True), 12, 8)
    b.add(table("경고·오류 (최근)",
                f"SELECT Timestamp AS time, SeverityText AS level, Body AS log, LogAttributes.spin::String AS spin, "
                f"coalesce(nullIf(LogAttributes.error::String, ''), nullIf(LogAttributes.err::String, ''), LogAttributes.finding::String) AS detail FROM {LOGS} "
                f"WHERE $__timeFilter(Timestamp) AND {WARNINGS} ORDER BY Timestamp DESC LIMIT 100",
                "가장 최근 100줄(요청 로그는 뺌)"), 12, 8)
    b.add(timeseries("리스를 쥔 루프",
                     series(GAUGE, "cnpg_rove_spin_leases", "ResourceAttributes['k8s.namespace.name']"),
                     "short", "payday spin의 리스(advisory lock) 수 = 돌고 있는 루프 수. 0이면 아무 루프도 돌지 않습니다(DB를 기다리는 중)", legend="list", decimals=0), 12, 7)
    b.add(timeseries("검색 대기열",
                     series(GAUGE, "cnpg_rove_search_stale", "ResourceAttributes['k8s.namespace.name']"),
                     "short", "색인기를 기다리는 SearchStale 행(추정치). 가져오기 직후 잠깐 늘고 1초쯤 안에 줄어야 합니다", legend="list", decimals=0), 12, 7)
    b.add({
        "type": "logs", "title": "로그 (WARN·ERROR, 요청 로그는 뺌, Loki)", "datasource": LOKI,
        "targets": [{"refId": "A", "datasource": LOKI, "expr": '{service_name="rove"} != "gRPC in" != "gRPC out" | severity_text=~"WARN|ERROR"'}],
        "options": {"showTime": True, "wrapLogMessage": True, "sortOrder": "Descending", "enableLogDetails": True},
    }, 24, 10)
    return b.json()


def platform():
    b = Board("rove-platform", "Rove — 플랫폼",
              "Rove 밖에서 보는 것: 백업(CNPG), DB, 인증서(cert-manager), 쿠버네티스, 노드. 클러스터 안 수집기와 노드 수집기가 보낸 지표")
    ok_warn_bad = lambda warn, bad: [{"color": "green", "value": None}, {"color": "orange", "value": warn}, {"color": "red", "value": bad}]

    b.row("백업")
    b.add(stat("마지막 베이스 백업 뒤",
               latest(GAUGE, "barman_cloud_cloudnative_pg_io_last_available_backup_timestamp", value="toUnixTimestamp(now()) - argMax(Value, TimeUnix)"),
               "s", "barman-cloud가 마지막으로 끝낸 베이스 백업에서 지금까지. 매일 하면 26시간을 넘지 않아야 합니다", ok_warn_bad(93600, 129600)), 6, 4)
    b.add(stat("마지막 WAL 보관 뒤", latest(GAUGE, "cnpg_pg_stat_archiver_seconds_since_last_archival"), "s",
               "쓰기가 없으면 WAL 조각이 차지 않아 보관하지 않습니다(archive_timeout은 쓰기가 있을 때만). 쓰기가 있는데 길어지면 문제입니다"), 6, 4)
    b.add(stat("WAL 보관 실패 (누적)", latest(SUM, "cnpg_pg_stat_archiver_failed_count"), "short",
               "pg_stat_archiver.failed_count. 늘어나면 버킷이나 자격 증명을 봅니다", ok_warn_bad(1, 1)), 6, 4)
    b.add(stat("복구할 수 있는 가장 이른 시점",
               latest(GAUGE, "barman_cloud_cloudnative_pg_io_first_recoverability_point", value="argMax(Value, TimeUnix) * 1000"),
               "dateTimeAsLocal", "보존 기간(retentionPolicy) 안에서 시점 복원이 닿는 가장 이른 시각"), 6, 4)
    b.add(timeseries("보관한 WAL 조각 (누적)", series(SUM, "cnpg_pg_stat_archiver_archived_count", "Attributes['cluster']", value="max(Value)"),
                     "short", "pg_stat_archiver.archived_count", legend="list"), 12, 7)
    b.add(timeseries("DB 크기", series(GAUGE, "cnpg_pg_database_size_bytes", "Attributes['datname']",
                                      where=" AND Attributes['datname'] NOT IN ('template0', 'template1', 'postgres')"),
                     "bytes", "rove와 내장 roster의 데이터베이스", legend="list"), 12, 7)

    b.row("데이터베이스")
    b.add(timeseries("연결", series(GAUGE, "cnpg_backends_total", "concat(Attributes['datname'], ' ', if(Attributes['application_name'] = '', '-', Attributes['application_name']), ' ', Attributes['state'])",
                                   value="sum(Value)"),
                     "short", "데이터베이스·애플리케이션·상태별. payday.spin은 리스를 쥔 연결입니다", stack=True), 12, 8)
    b.add(timeseries("초당 커밋",
                     "SELECT time, db, greatest(value - lagInFrame(value) OVER (PARTITION BY db ORDER BY time), 0) / $__interval_s AS commits FROM ("
                     f"SELECT $__timeInterval(TimeUnix) AS time, Attributes['datname'] AS db, max(Value) AS value FROM {SUM} "
                     "WHERE $__timeFilter(TimeUnix) AND MetricName = 'cnpg_pg_stat_database_xact_commit' AND Attributes['datname'] IN ('rove', 'roster') "
                     "GROUP BY time, db) ORDER BY time",
                     "ops", "pg_stat_database.xact_commit의 증가"), 12, 8)

    b.row("인증서와 쿠버네티스")
    b.add(table("인증서 만료",
                f"SELECT Attributes['namespace'] AS namespace, Attributes['name'] AS certificate, Attributes['issuer_name'] AS issuer, "
                f"(argMax(Value, TimeUnix) - toUnixTimestamp(now())) / 86400 AS days_left FROM {GAUGE} "
                "WHERE $__timeFilter(TimeUnix) AND MetricName = 'certmanager_certificate_expiration_timestamp_seconds' "
                "GROUP BY namespace, certificate, issuer ORDER BY days_left",
                "cert-manager가 관리하는 인증서. 14일 안에 끝나는 것이 있으면 갱신을 봅니다",
                [unit_of("days_left", "d", 1)]), 12, 8)
    b.add(table("재시작한 컨테이너",
                "SELECT ns, pod, container, max(v) - min(v) AS restarts_in_range, max(v) AS restarts FROM ("
                f"SELECT ResourceAttributes['k8s.namespace.name'] AS ns, ResourceAttributes['k8s.pod.name'] AS pod, ResourceAttributes['k8s.container.name'] AS container, Value AS v FROM {GAUGE} "
                "WHERE $__timeFilter(TimeUnix) AND MetricName = 'k8s.container.restarts') GROUP BY ns, pod, container HAVING restarts > 0 ORDER BY restarts_in_range DESC, restarts DESC",
                "재시작 횟수가 0이 아닌 컨테이너. `spin failed`나 메모리 초과(OOMKilled)가 여기서 보입니다"), 12, 8)
    b.add(table("Running이 아닌 파드",
                "SELECT ns, pod, multiIf(phase = 1, 'Pending', phase = 4, 'Failed', phase = 5, 'Unknown', toString(phase)) AS phase FROM ("
                f"SELECT ResourceAttributes['k8s.namespace.name'] AS ns, ResourceAttributes['k8s.pod.name'] AS pod, argMax(Value, TimeUnix) AS phase FROM {GAUGE} "
                "WHERE TimeUnix > now() - INTERVAL 5 MINUTE AND MetricName = 'k8s.pod.phase' GROUP BY ns, pod) WHERE phase NOT IN (2, 3) ORDER BY ns, pod",
                "지금(최근 5분) Pending, Failed, Unknown인 파드. 끝난 Job의 파드(Succeeded)는 뺍니다"), 12, 8)
    b.add(table("실패한 Job",
                f"SELECT ResourceAttributes['k8s.namespace.name'] AS ns, ResourceAttributes['k8s.job.name'] AS job, argMax(Value, TimeUnix) AS failed_pods FROM {GAUGE} "
                "WHERE $__timeFilter(TimeUnix) AND MetricName = 'k8s.job.failed' GROUP BY ns, job HAVING failed_pods > 0 ORDER BY ns, job",
                "볼륨 백업(rove-volumes-backup) 같은 Job의 실패한 파드 수"), 12, 8)

    b.row("노드")
    node = "getMacro('nickname')"
    b.add(timeseries("디스크 사용률", series(GAUGE, "system.filesystem.utilization", f"concat({node}, ' ', Attributes['mountpoint'])", value="max(Value)"),
                     "percentunit", "노드 수집기(hostmetrics)의 파일 시스템 사용률. local-path 볼륨은 노드 디스크에 있습니다", legend="list"), 8, 8)
    b.add(timeseries("메모리 사용률", series(GAUGE, "system.memory.utilization", node, value="avgIf(Value, Attributes['state'] = 'used')"),
                     "percentunit", "노드별 used 비율", legend="list"), 8, 8)
    b.add(timeseries("CPU 사용률",
                     series(GAUGE, "system.cpu.utilization", node, value="1 - avgIf(Value, Attributes['state'] = 'idle')"),
                     "percentunit", "노드별 1 - idle", legend="list"), 8, 8)
    return b.json()


def main():
    out = sys.argv[1]
    os.makedirs(out, exist_ok=True)
    for name, board in (("rove-service.json", service()), ("rove-platform.json", platform())):
        with open(os.path.join(out, name), "w") as f:
            json.dump(board, f, ensure_ascii=False, indent=2)
            f.write("\n")
        print(name, len(board["panels"]), "panels")


if __name__ == "__main__":
    main()
