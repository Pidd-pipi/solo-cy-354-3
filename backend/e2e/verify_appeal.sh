#!/usr/bin/env bash
# 信誉申诉模块端到端验证脚本（真实 HTTP）
# 前置：realserver 已监听 :29514
set -u
BASE=http://localhost:29514/api/v1
J=/tmp/appeal_verify
mkdir -p $J
pass=0; fail=0

check() { # desc expected actual
  if [ "$2" == "$3" ]; then echo "PASS: $1 ($3)"; pass=$((pass+1));
  else echo "FAIL: $1 expected=$2 actual=$3"; fail=$((fail+1)); fi
}

api() { # method path token [json]  -> writes body to $J/last.json, prints http code
  local method=$1 path=$2 token=$3 data=${4-}
  if [ -n "$data" ]; then
    code=$(curl -s -o $J/last.json -w '%{http_code}' -X "$method" "$BASE$path" \
      -H "Content-Type: application/json" ${token:+-H "Authorization: Bearer $token"} -d "$data")
  else
    code=$(curl -s -o $J/last.json -w '%{http_code}' -X "$method" "$BASE$path" \
      ${token:+-H "Authorization: Bearer $token"})
  fi
  echo "$code"
}
jqr() { jq -r "$1" $J/last.json; }
login() { api POST /users/login '' "{\"phone\":\"$1\",\"password\":\"$2\"}" >/dev/null; jqr '.data.token'; }
mycredit() { api GET /users/me "$1" >/dev/null; jqr '.data.credit_score'; }

echo "================ 0. 登录四个账号 ================"
BUYER=$(login 13700000001 123456)
SELLER=$(login 13700000002 123456)
OTHER=$(login 13700000003 123456)
ADMIN=$(login 13800000001 admin123)
[ -n "$BUYER" ] && [ -n "$SELLER" ] && [ -n "$OTHER" ] && [ -n "$ADMIN" ] && echo "PASS: 四个账号均拿到 JWT" || echo "FAIL: 登录"

echo "================ 1. 前置：两条评价产生信誉分变化 ================"
echo "卖家初始信誉分: $(mycredit $SELLER)（期望 120）"
api POST /reviews "$BUYER" '{"trade_id":1,"rating":"bad","content":"交易体验很差"}' >/dev/null
BAD_REVIEW_ID=$(jqr '.data.id')
echo "买家对订单1给差评 review_id=$BAD_REVIEW_ID -> 卖家信誉分: $(mycredit $SELLER)（120 + (-10) = 110）"
check "差评扣 10 分" 110 "$(mycredit $SELLER)"

api POST /reviews "$BUYER" '{"trade_id":2,"rating":"good","content":"交易顺利好评"}' >/dev/null
GOOD_REVIEW_ID=$(jqr '.data.id')
echo "买家对订单2给好评 review_id=$GOOD_REVIEW_ID -> 卖家信誉分: $(mycredit $SELLER)（110 + 5 = 115）"
check "好评加 5 分" 115 "$(mycredit $SELLER)"

echo "================ 2. 链路一：提交申诉的权限与唯一性校验 ================"
check "评价人本人不能申诉(403)" 403 "$(api POST /appeals "$BUYER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"我是评价人不应能发起申诉校验用\"}")"
check "无关第三方不能申诉(403)" 403 "$(api POST /appeals "$OTHER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"路人也不应该可以代替发起申诉\"}")"
check "未登录不能申诉(401)" 401 "$(api POST /appeals '' "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"未登录用户发起申诉理应被拦截\"}")"
check "理由过短校验失败(400)" 400 "$(api POST /appeals "$SELLER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"x\"}")"
check "评价不存在(404)" 404 "$(api POST /appeals "$SELLER" '{"review_id":99999,"reason":"对一个不存在的评价进行申诉测试"}')"

api POST /appeals "$SELLER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"该差评与事实不符，买家无理由恶意评价，申请撤销信誉分扣减\"}" >/dev/null
BAD_APPEAL_ID=$(jqr '.data.id')
check "接收方提交申诉成功(200, pending)" pending "$(jqr '.data.status')"
echo "差评申诉单 id=$BAD_APPEAL_ID"
check "每条评价只能申诉一次(409)" 409 "$(api POST /appeals "$SELLER" "{\"review_id\":$BAD_REVIEW_ID,\"reason\":\"重复申诉理应被拒绝才行\"}")"

api POST /appeals "$SELLER" "{\"review_id\":$GOOD_REVIEW_ID,\"reason\":\"怀疑好评为异常评价，申请复核该笔信誉加分\"}" >/dev/null
GOOD_APPEAL_ID=$(jqr '.data.id')
echo "好评申诉单 id=$GOOD_APPEAL_ID"

echo "================ 3. 链路二：申诉进度查询 ================"
check "本人查询进度(200, pending)" pending "$(api GET /appeals/$BAD_APPEAL_ID "$SELLER" >/dev/null; jqr '.data.status')"
check "进度包含原评价快照" "$BAD_REVIEW_ID" "$(api GET /appeals/$BAD_APPEAL_ID "$SELLER" >/dev/null; jqr '.data.review.id')"
check "非发起人查他人申诉(403)" 403 "$(api GET /appeals/$BAD_APPEAL_ID "$OTHER")"
check "我的申诉列表数量=2" 2 "$(api GET /appeals/me "$SELLER" >/dev/null; jqr '.data | length')"
check "未登录查询(401)" 401 "$(api GET /appeals/$BAD_APPEAL_ID '')"

echo "================ 4. 链路三：管理员审核 ================"
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

echo "================ 5. 学生侧最终进度 ================"
api GET /appeals/me "$SELLER" >/dev/null
jq -r '.data[] | "申诉#\(.id) 评价#\(.review_id) 状态=\(.status) 回滚=\(.credit_reversed) delta=\(.credit_delta) 备注=\(.review_comment)"' $J/last.json
check "最终已通过数量" 1 "$(jq '[.data[]|select(.status=="approved")]|length' $J/last.json)"
check "最终已驳回数量" 1 "$(jq '[.data[]|select(.status=="rejected")]|length' $J/last.json)"
check "管理员看已通过队列=1" 1 "$(api GET '/admin/appeals?status=approved' "$ADMIN" >/dev/null; jqr '.data | length')"

echo "================================================"
echo "RESULT: PASS=$pass FAIL=$fail"
[ $fail -eq 0 ]
