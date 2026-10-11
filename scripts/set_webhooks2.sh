#!/bin/bash
# ============================================================
# Miogram — Set Telegram webhooks with secret_token & max_connections=100
# (SSL certificate is fetched automatically from the webhook URL by Telegram)
# ============================================================

set -e

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

# ============================================================
# 🌐 دامنه و پورت
# ============================================================
DOMAIN="telegram.sorush.online"
PORT="8443"

# ============================================================
# 🌐 پروکسی برای دسترسی به api.telegram.org (اختیاری)
# اگه روی سرور به api.telegram.org مستقیم دسترسی داری، می‌تونی
# این خط رو کامنت کنی تا از آدرس اصلی استفاده کنه.
# ============================================================
TG_API="http://194.5.207.93:10443"

# ============================================================
# 🔐 WEBHOOK_SECRET باید با مقدار داخل فایل‌های .env.* یکسان باشد
# ============================================================
WEBHOOK_SECRET="kjfnIIUnskjdsugn9823u4"

# توکن‌های ربات‌های فارسی
declare -A PERSIAN=(
  ["main"]="8353134695:AAFMpN5W-9VqVYaWIb7Gj2uYIKIt1AfQqn0"
  ["shard1"]="8890591504:AAHk2d7JJJNhWyheAB4ZCde6xdDysl3BY2c"
  ["shard2"]="8664184348:AAFLVh-r4iuc0WonO95aJqSab0aziQZjRiE"
  ["shard3"]="8871871205:AAGKbTOkmSbLEeGXG2l1_tqBrR0A_AVR-a0"
  ["shard4"]="8652176746:AAEc6onkYELpwmGwCjssawkPzwGu0fx3eLo"
  ["shard5"]="8695650666:AAG8INBKPFV3x5xdM3kDEKEKF15G1Xi6zi8"
)

# ------------------------------------------------------------
# ست کردن وبهوک برای یک ربات
# ------------------------------------------------------------
set_webhook() {
  local token=$1 path=$2 name=$3
  local url="https://${DOMAIN}:${PORT}${path}"

  echo -e "${BLUE}▶ Setting ${name}${NC}"
  echo -e "  URL: ${url}"
  echo -e "  secret_token: ${WEBHOOK_SECRET}"
  echo -e "  max_connections: 100"
  echo -e "  Certificate: (auto — fetched by Telegram from URL)"

  local resp http_code body
  resp=$(curl -s -w "\n%{http_code}" \
    -F "url=${url}" \
    -F "secret_token=${WEBHOOK_SECRET}" \
    -F "max_connections=100" \
    "${TG_API}/bot${token}/setWebhook")
  http_code=$(echo "$resp" | tail -n1)
  body=$(echo "$resp" | head -n-1)

  if [ "$http_code" -eq 200 ] && echo "$body" | grep -q '"ok":true'; then
    echo -e "${GREEN}✅ ${name} - Webhook set successfully${NC}"
  else
    echo -e "${RED}❌ ${name} - HTTP ${http_code}: ${body}${NC}"
  fi
  echo ""
}

# ------------------------------------------------------------
# گرفتن اطلاعات وبهوک فعلی (برای دیباگ)
# ------------------------------------------------------------
get_webhook_info() {
  local token=$1 name=$2
  local resp
  resp=$(curl -s "${TG_API}/bot${token}/getWebhookInfo")
  echo -e "${YELLOW}▶ Info for ${name}${NC}"
  echo "$resp" | python3 -m json.tool 2>/dev/null || echo "$resp"
  echo ""
}

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}  Miogram Webhook Setup${NC}"
echo -e "${YELLOW}  ${DOMAIN}:${PORT}  (cert: auto via URL)${NC}"
echo -e "${YELLOW}  secret_token: ${WEBHOOK_SECRET}${NC}"
echo -e "${YELLOW}  max_connections: 100${NC}"
echo -e "${YELLOW}========================================${NC}"

# ------------------------------------------------------------
# قبل از ست کردن، اطلاعات فعلی رو نشون بده
# ------------------------------------------------------------
echo -e "${BLUE}━━━ Before ━━━${NC}"
for bot in "${!PERSIAN[@]}"; do
  get_webhook_info "${PERSIAN[$bot]}" "Persian - ${bot}"
done

# ------------------------------------------------------------
# حالا ست کن
# ------------------------------------------------------------
echo -e "${BLUE}━━━ Setting ━━━${NC}"
for bot in "${!PERSIAN[@]}"; do
  set_webhook "${PERSIAN[$bot]}" "/persian/bot/${bot}" "Persian - ${bot}"
done

# ------------------------------------------------------------
# بعد از ست کردن، دوباره چک کن
# ------------------------------------------------------------
echo -e "${BLUE}━━━ After ━━━${NC}"
for bot in "${!PERSIAN[@]}"; do
  get_webhook_info "${PERSIAN[$bot]}" "Persian - ${bot}"
done

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}✅ Done.${NC}"
echo -e "${GREEN}========================================${NC}"
