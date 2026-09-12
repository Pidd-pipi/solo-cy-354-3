#!/usr/bin/env bash
# 信誉申诉模块端到端验证脚本（真实 HTTP，可重复运行）
# 前置：
#   cd backend/e2e && rm -f /tmp/e2e_campus.db
#   go run -tags realserver ./cmd/realserver   # 监听 :29514
set -u
BASE=http://localhost:29514/api/v1
J=/tmp/appeal_verify
mkdir -p $J
pass=0; fail=0

check() { # desc expected actual
  if [ "$2" == "$3" ]; then echo "PASS: $1 ($3)"; pass=$((pass+1));
  else echo "FAIL: $1 expected=$2 actual=$3"; fail=$((fail+1)); fi
}

api() { # method path token [json] -> writes body to $J/last.json, prints http code
  local method=$1 path=$2 token=$3 data=${4-}
  if [ -n "$data" ]; then
    curl -s -o $J/last.json -w '%{http_code}' -X "$method" "$BASE$path" \
      -H "Content-Type: application/json" ${token:+-H "Authorization: Bearer $token"} -d "$data"
  else
    curl -s -o $J/last.json -w '%{http_code}' -X "$method" "$BASE$path" \
      ${token:+-H "Authorization: Bearer $token"}
  fi
}
jqr() { jq -r "$1" $J/last.json; }
login() { api POST /users/login '' "{\"phone\":\"$1\",\"password\":\"$2\"}" >/dev/null; jqr '.data.token'; }
mycredit() { api GET /users/me "$1" >/dev/null; jqr '.data.credit_score'; }

echo "================ 0. 登录账号 ================"
BUYER=$(login 13700000001 123456)
SELLER=$(login 13700000002 123456)
OTHER=$(login 13700000003 123456)
ADMIN=$(login 13800000001 admin123)
FLOOR=$(login 13700000005 123456)   # 触底卖家，初始信誉分 4
CEIL=$(login 13700000006 123456)    # 触顶卖家，初始信誉分 298
for t in "$BUYER" "$SELLER" "$OTHER" "$ADMIN" "$FLOOR" "$CEIL"; do [ -z "$t" ] && { echo "FAIL: 登录失败"; exit 1; }; done
echo "PASS: 六个账号均拿到 JWT（触底 4 分 / 触顶 298 分）"; pass=$((pass+1))

echo "================ 1. 前置：两条评价产生信誉分变化 ================"
api POST /reviews "$BUYER" '{"trade_id":1,"rating":"bad","content":"交易体验很差"}' >/dev/null
BAD_REVIEW_ID=$(jqr '.data.id')
check "差评扣 10 分（120->110）" 110 "$(mycredit $SELLER)"
api POST /reviews "$BUYER" '{"trade_id":2,"rating":"good","content":"交易顺利好评"}' >/dev/null
GOOD_REVIEW_ID=$(jqr '.data.id')
check "好评加 5 分（110->115）" 115 "$(mycredit $SELLER)"

echo "================ 2. 链路一：提交申诉（权限/唯一性/空白理由）================"
check "评价人本人不能申诉(403)" 403 "$(api POST /appeals "$BUYER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"我是评价人不应能发起申诉校验用\"}")"
check "无关第三方不能申诉(403)" 403 "$(api POST /appeals "$OTHER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"路人也不应该可以代替发起申诉\"}")"
check "未登录不能申诉(401)" 401 "$(api POST /appeals '' "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"未登录用户发起申诉理应被拦截\"}")"
check "纯空格理由被拒(400)" 400 "$(api POST /appeals "$SELLER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"    \"}")"
check "制表符换行理由被拒(400)" 400 "$(api POST /appeals "$SELLER" '{"review_id":'$BAD_REVIEW_ID',"reason":"\t\n  \r\n"}')"
api POST /appeals "$SELLER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"   \"}" >/dev/null
check "空白理由返回明确业务码 42200" 42200 "$(jqr '.code')"
check "评价不存在(404)" 404 "$(api POST /appeals "$SELLER" '{"review_id":99999,"reason":"对一个不存在的评价进行申诉测试"}')"

api POST /appeals "$SELLER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"  该差评与事实不符，申请撤销信誉分扣减  \"}" >/dev/null
BAD_APPEAL_ID=$(jqr '.data.id')
check "接收方提交申诉成功(pending)" pending "$(jqr '.data.status')"
check "理由两端空格被裁剪存储" "该差评与事实不符，申请撤销信誉分扣减" "$(jqr '.data.reason')"
check "每条评价只能申诉一次(409)" 409 "$(api POST /appeals "$SELLER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"重复申诉理应被拒绝才行\"}")"

api POST /appeals "$SELLER" "{\"review_id\":$GOOD_REVIEW_ID,\"reason\":\"怀疑好评为异常评价，申请复核该笔信誉加分\"}" >/dev/null
GOOD_APPEAL_ID=$(jqr '.data.id')

echo "================ 3. 链路二：申诉进度查询 ================"
check "本人查询进度(pending)" pending "$(api GET /appeals/$BAD_APPEAL_ID "$SELLER" >/dev/null; jqr '.data.status')"
check "进度包含原评价快照" "$BAD_REVIEW_ID" "$(api GET /appeals/$BAD_APPEAL_ID "$SELLER" >/dev/null; jqr '.data.review.id')"
check "非发起人查他人申诉(403)" 403 "$(api GET /appeals/$BAD_APPEAL_ID "$OTHER")"
check "我的申诉列表数量=2" 2 "$(api GET /appeals/me "$SELLER" >/dev/null; jqr '.data | length')"
check "未登录查询(401)" 401 "$(api GET /appeals/$BAD_APPEAL_ID '')"

echo "================ 4. 链路三：管理员审核（驳回不变/通过回滚/单次生效）================"
check "学生访问审核队列(403)" 403 "$(api GET '/admin/appeals?status=pending' "$SELLER")"
check "管理员看待审核队列=2" 2 "$(api GET '/admin/appeals?status=pending' "$ADMIN" >/dev/null; jqr '.data | length')"
check "非法审核动作(400)" 400 "$(api POST /admin/appeals/$BAD_APPEAL_ID/review "$ADMIN" '{"action":"maybe"}')"
check "分数在非法动作后不变" 115 "$(mycredit $SELLER)"

api POST /admin/appeals/$GOOD_APPEAL_ID/review "$ADMIN" '{"action":"reject","comment":"评价真实有效，驳回申诉"}' >/dev/null
check "驳回好评申诉" rejected "$(jqr '.data.status')"
check "驳回不回滚分数(delta=0)" 0 "$(jqr '.data.credit_delta')"
check "驳回后信誉分不变(仍115)" 115 "$(mycredit $SELLER)"

api POST /admin/appeals/$BAD_APPEAL_ID/review "$ADMIN" '{"action":"approve","comment":"差评证据不足，撤销信誉分扣减"}' >/dev/null
check "通过差评申诉" approved "$(jqr '.data.status')"
check "通过后记录回滚标记" true "$(jqr '.data.credit_reversed')"
check "通过回滚 delta=+10" 10 "$(jqr '.data.credit_delta')"
check "撤销差评扣分后信誉分恢复(125)" 125 "$(mycredit $SELLER)"
check "已审核申诉不可重复审核(409)" 409 "$(api POST /admin/appeals/$BAD_APPEAL_ID/review "$ADMIN" '{"action":"reject"}')"

echo "================ 5. 边界：触底/触顶只恢复实际变化 + 并发单次生效 ================"
# ---- 触底：卖家 5 在 4 分收到差评，实际只扣 4（名义 -10 被钳制）----
api POST /reviews "$BUYER" '{"trade_id":3,"rating":"bad","content":"触底差评"}' >/dev/null
FLOOR_REVIEW_ID=$(jqr '.data.id')
check "触底评价记录的实际 delta=-4" "-4" "$(jqr '.data.credit_delta')"
check "触底后信誉分为 0（不是 -6）" 0 "$(mycredit $FLOOR)"

# 5a. 10 个并发申诉提交，必须仅 1 条成功、9 条业务冲突、库里仅 1 行
rm -f $J/cc_*.code $J/cc_*.json
for i in $(seq 1 10); do
  curl -s -o $J/cc_$i.json -w '%{http_code}' -X POST "$BASE/appeals" \
    -H "Content-Type: application/json" -H "Authorization: Bearer $FLOOR" \
    -d "{\"review_id\":$FLOOR_REVIEW_ID,\"reason\":\"并发重复提交的申诉理由必须只有一条成功才行\"}" > $J/cc_$i.code &
done
wait
cc200=$(grep -l 200 $J/cc_*.code 2>/dev/null | wc -l)
cc409=$(grep -l 409 $J/cc_*.code 2>/dev/null | wc -l)
check "并发提交：恰好 1 条成功" 1 "$cc200"
check "并发提交：其余 9 条业务冲突" 9 "$cc409"
api GET /appeals/me "$FLOOR" >/dev/null
check "并发提交后库里仅 1 条申诉" 1 "$(jq '[.data[]|select(.review_id=='$FLOOR_REVIEW_ID')]|length' $J/last.json)"
FLOOR_APPEAL_ID=$(jq -r '[.data[]|select(.review_id=='$FLOOR_REVIEW_ID')][0].id' $J/last.json)

# 5b. 10 个并发管理员通过，必须仅 1 次生效，回滚只执行一次：0 -> 4
rm -f $J/cr_*.code $J/cr_*.json
for i in $(seq 1 10); do
  curl -s -o $J/cr_$i.json -w '%{http_code}' -X POST "$BASE/admin/appeals/$FLOOR_APPEAL_ID/review" \
    -H "Content-Type: application/json" -H "Authorization: Bearer $ADMIN" \
    -d '{"action":"approve"}' > $J/cr_$i.code &
done
wait
cr200=$(grep -l 200 $J/cr_*.code 2>/dev/null | wc -l)
cr409=$(grep -l 409 $J/cr_*.code 2>/dev/null | wc -l)
check "并发审核：恰好 1 次成功" 1 "$cr200"
check "并发审核：其余 9 次业务冲突" 9 "$cr409"
check "触底回滚只恢复实际扣减（0->4，而非 +10）" 4 "$(mycredit $FLOOR)"
api GET /appeals/$FLOOR_APPEAL_ID "$FLOOR" >/dev/null
check "触底申诉记录回滚 delta=+4" 4 "$(jqr '.data.credit_delta')"
check "触底申诉已标记回滚" true "$(jqr '.data.credit_reversed')"

# ---- 触顶：卖家 6 在 298 分收到好评，实际只加 2（名义 +5 被钳制）----
api POST /reviews "$BUYER" '{"trade_id":4,"rating":"good","content":"触顶好评"}' >/dev/null
CEIL_REVIEW_ID=$(jqr '.data.id')
check "触顶评价记录的实际 delta=+2" "2" "$(jqr '.data.credit_delta')"
check "触顶后信誉分为 300（不是 303）" 300 "$(mycredit $CEIL)"
api POST /appeals "$CEIL" "{\"review_id\":$CEIL_REVIEW_ID,\"reason\":\"触顶好评申诉理由需要足够长才行\"}" >/dev/null
CEIL_APPEAL_ID=$(jqr '.data.id')

# 5c. 10 个并发混合决策（通过/驳回交错），仍只有 1 个决策生效
rm -f $J/mx_*.code $J/mx_*.json
for i in $(seq 1 10); do
  if [ $((i % 2)) -eq 0 ]; then act=approve; else act=reject; fi
  curl -s -o $J/mx_$i.json -w '%{http_code}' -X POST "$BASE/admin/appeals/$CEIL_APPEAL_ID/review" \
    -H "Content-Type: application/json" -H "Authorization: Bearer $ADMIN" \
    -d "{\"action\":\"$act\"}" > $J/mx_$i.code &
done
wait
mx200=$(grep -l 200 $J/mx_*.code 2>/dev/null | wc -l)
mx409=$(grep -l 409 $J/mx_*.code 2>/dev/null | wc -l)
check "并发混合审核：恰好 1 次成功" 1 "$mx200"
check "并发混合审核：其余 9 次业务冲突" 9 "$mx409"
api GET /appeals/$CEIL_APPEAL_ID "$CEIL" >/dev/null
CEIL_STATUS=$(jqr '.data.status')
CEIL_DELTA=$(jqr '.data.credit_delta')
CEIL_SCORE=$(mycredit $CEIL)
if [ "$CEIL_STATUS" == "approved" ]; then
  check "触顶：通过则只恢复实际加分（300->298，delta -2）" 298 "$CEIL_SCORE"
  check "触顶：记录回滚 delta=-2" "-2" "$CEIL_DELTA"
else
  check "触顶：驳回则分数维持 300" 300 "$CEIL_SCORE"
  check "触顶：驳回不产生回滚" 0 "$CEIL_DELTA"
fi

echo "================ 6. 学生侧最终进度 ================"
api GET /appeals/me "$SELLER" >/dev/null
jq -r '.data[] | "卖家2 申诉#\(.id) 评价#\(.review_id) 状态=\(.status) 回滚=\(.credit_reversed) delta=\(.credit_delta)"' $J/last.json
check "卖家2 已通过数量" 1 "$(jq '[.data[]|select(.status=="approved")]|length' $J/last.json)"
check "卖家2 已驳回数量" 1 "$(jq '[.data[]|select(.status=="rejected")]|length' $J/last.json)"
api GET '/admin/appeals?status=approved' "$ADMIN" >/dev/null
check "管理员看已通过队列>=2（普通+触底/触顶）" "true" "$(jq '.data | length >= 2' $J/last.json)"

echo "================================================"
echo "RESULT: PASS=$pass FAIL=$fail"
[ $fail -eq 0 ]
