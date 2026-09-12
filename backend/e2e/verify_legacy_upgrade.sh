#!/usr/bin/env bash
# 旧库升级演练：用旧表结构（reviews 无 credit_delta 列）造存量数据，
# 再以新二进制的生产启动序列（AutoMigrate + 数据迁移回填）打开，
# 最后调用真实 HTTP 接口复核：旧差评申诉通过后恢复"评价发生前"的信誉分；
# 完全触底/触顶的评价回滚为 no-op。
#
# 用法（backend/e2e 目录下，需要先 go build 过依赖）：
#   bash verify_legacy_upgrade.sh
set -u
export PATH=${PATH}:/tmp/godl/go/bin
export GOPROXY=https://goproxy.cn,direct GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomod CGO_ENABLED=1
BASE=http://localhost:29514/api/v1
DB=/tmp/e2e_legacy.db
J=/tmp/legacy_verify
mkdir -p $J
pass=0; fail=0
check() { if [ "$2" == "$3" ]; then echo "PASS: $1 ($3)"; pass=$((pass+1)); else echo "FAIL: $1 expected=$2 actual=$3"; fail=$((fail+1)); fi; }
api() { # method path token [json]
  local method=$1 path=$2 token=$3 data=${4-}
  if [ -n "$data" ]; then
    curl -s -o $J/last.json -w '%{http_code}' -X "$method" "$BASE$path" -H "Content-Type: application/json" ${token:+-H "Authorization: Bearer $token"} -d "$data"
  else
    curl -s -o $J/last.json -w '%{http_code}' -X "$method" "$BASE$path" ${token:+-H "Authorization: Bearer $token"}
  fi
}
jqr() { jq -r "$1" $J/last.json; }
login() { api POST /users/login '' "{\"phone\":\"$1\",\"password\":\"$2\"}" >/dev/null; jqr '.data.token'; }
mycredit() { api GET /users/me "$1" >/dev/null; jqr '.data.credit_score'; }
approve_appeal() { # token review_id expected_delta
  local token=$1 rid=$2 wantDelta=$3
  api POST /appeals "$token" "{\"review_id\":$rid,\"reason\":\"旧库存量评价申诉理由需要足够长\"}" >/dev/null
  local aid=$(jqr '.data.id')
  api POST /admin/appeals/$aid/review "$ADMIN" '{"action":"approve"}' >/dev/null
  check "旧评价#$rid 回滚 delta=$wantDelta" "$wantDelta" "$(jqr '.data.credit_delta')"
}

echo "======== 步骤 1：用旧表结构造存量库（reviews 无 credit_delta 列）========"
rm -f $DB $DB-wal $DB-shm
go build -tags realserver -o /tmp/realserver_bin ./cmd/realserver
LEGACY_SEED=1 DB_PATH=$DB /tmp/realserver_bin | tee $J/seed.out
IDS_JSON=$(sed -n 's/^LEGACY_SEED_READY reviews=//p' $J/seed.out)
echo "存量评价ID: $IDS_JSON"
NORMAL=$(echo "$IDS_JSON" | jq -r .normal_bad)
FLOOR10=$(echo "$IDS_JSON" | jq -r .floor_10th_effective_minus10)
FLOOR11=$(echo "$IDS_JSON" | jq -r .floor_11th_clamped_zero)
CEIL40=$(echo "$IDS_JSON" | jq -r .ceil_40th_effective_plus5)
CEIL41=$(echo "$IDS_JSON" | jq -r .ceil_41st_clamped_zero)
HAS_COL=$(python3 -c "import sqlite3;print(1 if any(r[1]=='credit_delta' for r in sqlite3.connect('$DB').execute('PRAGMA table_info(reviews)').fetchall()) else 0)")
check "旧库确实没有 credit_delta 列" "0" "$HAS_COL"

echo "======== 步骤 2：新二进制启动同一库（AutoMigrate 加列 + 一次性回填）========"
( DB_PATH=$DB /tmp/realserver_bin >$J/server.log 2>&1 ) &
SERVER_PID=$!
trap 'kill $SERVER_PID 2>/dev/null || true' EXIT
for i in $(seq 1 60); do curl -sf -o /dev/null http://localhost:29514/healthz && break; sleep 1; done
grep -E "data migration applied|backfill completed" $J/server.log | sed 's/^/  /'
check "迁移记录版本 v1" "1" "$(python3 -c "import sqlite3;print(sqlite3.connect('$DB').execute('select count(*) from schema_migrations where version=1').fetchone()[0])")"
check "回填的普通差评 delta=-10" "-10" "$(python3 -c "import sqlite3;print(sqlite3.connect('$DB').execute('select credit_delta from reviews where id=$NORMAL').fetchone()[0])")"
check "触底第10条 delta=-10" "-10" "$(python3 -c "import sqlite3;print(sqlite3.connect('$DB').execute('select credit_delta from reviews where id=$FLOOR10').fetchone()[0])")"
check "触底第11条完全钳制 delta=0" "0" "$(python3 -c "import sqlite3;print(sqlite3.connect('$DB').execute('select credit_delta from reviews where id=$FLOOR11').fetchone()[0])")"
check "触顶第40条 delta=+5" "5" "$(python3 -c "import sqlite3;print(sqlite3.connect('$DB').execute('select credit_delta from reviews where id=$CEIL40').fetchone()[0])")"
check "触顶第41条完全钳制 delta=0" "0" "$(python3 -c "import sqlite3;print(sqlite3.connect('$DB').execute('select credit_delta from reviews where id=$CEIL41').fetchone()[0])")"

echo "======== 步骤 3：真实 HTTP 接口复核旧评价申诉（恢复评价前分数）========"
NORMAL_SELLER=$(login 13700000002 123456)
FLOOR=$(login 13700000005 123456)
CEIL=$(login 13700000006 123456)
ADMIN=$(login 13800000001 admin123)

check "普通卖家旧分=90" 90 "$(mycredit $NORMAL_SELLER)"
approve_appeal "$NORMAL_SELLER" "$NORMAL" 10
check "普通差评申诉后恢复评价前 100 分" 100 "$(mycredit $NORMAL_SELLER)"

check "触底卖家旧分=0" 0 "$(mycredit $FLOOR)"
approve_appeal "$FLOOR" "$FLOOR10" 10
check "第10条旧差评回滚恢复到评价前 10 分（而非名义+100）" 10 "$(mycredit $FLOOR)"
approve_appeal "$FLOOR" "$FLOOR11" 0
check "完全钳制的第11条回滚为 no-op，分数仍 10" 10 "$(mycredit $FLOOR)"

check "触顶卖家旧分=300" 300 "$(mycredit $CEIL)"
approve_appeal "$CEIL" "$CEIL40" -5
check "第40条旧好评回滚恢复到评价前 295 分（而非名义 295 名义一致，验证精度）" 295 "$(mycredit $CEIL)"
approve_appeal "$CEIL" "$CEIL41" 0
check "完全钳制的第41条回滚为 no-op，分数仍 295" 295 "$(mycredit $CEIL)"

echo "======== 步骤 4：回填幂等——再次执行迁移不改数据 ========"
# 通过重启服务验证：schema_migrations 已记录 v1，重启不再回填，分数不变
kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null
( DB_PATH=$DB /tmp/realserver_bin >$J/server2.log 2>&1 ) &
SERVER_PID=$!
for i in $(seq 1 60); do curl -sf -o /dev/null http://localhost:29514/healthz && break; sleep 1; done
if grep -q "backfill completed" $J/server2.log; then echo "FAIL: 重启后不应再次回填"; fail=$((fail+1)); else echo "PASS: 重启未重复回填（版本化幂等）"; pass=$((pass+1)); fi
check "回填后普通差评 delta 仍=-10" "-10" "$(python3 -c "import sqlite3;print(sqlite3.connect('$DB').execute('select credit_delta from reviews where id=$NORMAL').fetchone()[0])")"
kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null

echo "================================================"
echo "LEGACY RESULT: PASS=$pass FAIL=$fail"
[ $fail -eq 0 ]
