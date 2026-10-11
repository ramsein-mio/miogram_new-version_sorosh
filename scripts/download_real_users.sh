#!/bin/bash
# دانلود تصاویر پروفایل کاربران واقعی (is_fake=false)
# نام فایل: user_{user_id}.jpg
# سرعت: ۱ درخواست در ثانیه
set -e
# ---------- تنظیمات ----------
DB_USER="miogram"
DB_NAME="miogram"
DEST_DIR="../bot/profile-users"
PROGRESS_FILE="download_real_progress.txt"
mkdir -p "$DEST_DIR"
# ---------- استخراج توکن‌ها ----------
ENV_FILES=(
    "../envs/.env.main"
    "../envs/.env.shard1"
    "../envs/.env.shard2"
    "../envs/.env.shard3"
    "../envs/.env.shard4"
    "../envs/.env.shard5"
)
TOKENS=()
for env_file in "${ENV_FILES[@]}"; do
    if [[ -f "$env_file" ]]; then
        token=$(grep -E '^BOT_TOKEN=' "$env_file" | cut -d '=' -f2- | tr -d '"' | tr -d "'" | xargs)
        if [[ -n "$token" ]]; then
            TOKENS+=("$token")
            echo "✅ توکن از $env_file بارگذاری شد"
        fi
    fi
done
if [[ ${#TOKENS[@]} -eq 0 ]]; then
    echo "❌ هیچ توکنی یافت نشد."
    exit 1
fi
echo "🔑 تعداد توکن‌ها: ${#TOKENS[@]}"
# ---------- خواندن آخرین id ----------
LAST_ID=0
if [[ -f "$PROGRESS_FILE" ]]; then
    LAST_ID=$(cat "$PROGRESS_FILE" | tr -d '[:space:]')
    if ! [[ "$LAST_ID" =~ ^[0-9]+$ ]]; then
        LAST_ID=0
    fi
    echo "📌 ادامه از id=$LAST_ID"
else
    echo "📌 شروع از ابتدا"
fi
# ---------- دریافت لیست کاربران واقعی ----------
echo "📡 در حال دریافت لیست کاربران واقعی از دیتابیس..."
QUERY="SELECT id, user_id, image FROM users WHERE image IS NOT NULL AND image != '' AND is_fake = false AND id > $LAST_ID ORDER BY id;"
TMP_FILE=$(mktemp)
docker compose exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" -t -A -F ',' -c "$QUERY" > "$TMP_FILE" 2>/dev/null
if [[ $? -ne 0 ]]; then
    echo "⚠️ تلاش با docker-compose..."
    docker-compose exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" -t -A -F ',' -c "$QUERY" > "$TMP_FILE" 2>/dev/null
    if [[ $? -ne 0 ]]; then
        echo "❌ خطا در اجرای psql."
        rm -f "$TMP_FILE"
        exit 1
    fi
fi
TOTAL_LINES=$(grep -c -v '^$' "$TMP_FILE" || true)
if [[ $TOTAL_LINES -eq 0 ]]; then
    echo "✅ همه کاربران واقعی پردازش شده‌اند."
    rm -f "$TMP_FILE"
    exit 0
fi
echo "👤 تعداد کاربران واقعی باقی‌مانده: $TOTAL_LINES"
# ---------- پردازش ----------
SUCCESS=0
FAIL=0
SKIP=0
COUNT=0
while IFS=',' read -r id user_id image; do
    id=$(echo "$id" | xargs)
    user_id=$(echo "$user_id" | xargs)
    image=$(echo "$image" | xargs)
    [[ -z "$id" || -z "$user_id" || -z "$image" ]] && continue
    COUNT=$((COUNT + 1))
    echo "[$COUNT/$TOTAL_LINES] کاربر $user_id (id=$id) ..."
    dest_file="$DEST_DIR/user_${user_id}.jpg"
    if [[ -f "$dest_file" ]]; then
        echo "   ⏭️ فایل user_${user_id}.jpg قبلاً وجود دارد، رد می‌شود."
        SKIP=$((SKIP + 1))
        echo "$id" > "$PROGRESS_FILE"
        sleep 1
        continue
    fi
    SAVED=false
    for token in "${TOKENS[@]}"; do
        get_file_url="https://proxy-telegram-production.up.railway.app/bot${token}/getFile?file_id=${image}"
        response=$(curl -s --max-time 10 "$get_file_url")
        ok=$(echo "$response" | grep -o '"ok":true' | head -1)
        if [[ -z "$ok" ]]; then
            continue
        fi
        file_path=$(echo "$response" | grep -o '"file_path":"[^"]*"' | sed 's/"file_path":"//;s/"$//')
        if [[ -z "$file_path" ]]; then
            continue
        fi
        file_url="https://proxy-telegram-production.up.railway.app/file/bot${token}/${file_path}"
        if curl -s -o "$dest_file" --max-time 30 "$file_url"; then
            echo "   ✅ دانلود شد (user_id=$user_id)"
            SAVED=true
            SUCCESS=$((SUCCESS + 1))
            break
        else
            echo "   ❌ دانلود ناموفق با این توکن"
        fi
    done
    if [[ "$SAVED" == "false" ]]; then
        echo "   ⚠️ با هیچ توکنی پیدا نشد"
        FAIL=$((FAIL + 1))
    fi
    echo "$id" > "$PROGRESS_FILE"
    sleep 1
done < "$TMP_FILE"
rm -f "$TMP_FILE"
echo "------------------------"
echo "✅ دانلود موفق: $SUCCESS"
echo "❌ دانلود ناموفق: $FAIL"
echo "⏭️ رد شده (فایل موجود): $SKIP"
echo "📁 تصاویر در $DEST_DIR/user_{user_id}.jpg ذخیره شدند."
