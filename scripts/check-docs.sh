#!/usr/bin/env bash
set -uo pipefail

DOCS_DIR="${1:-docs}"
REQUIRED_FIELDS=(title date version type module maintainer status related_code summary)
MANAGED_DIRS=("$DOCS_DIR/system" "$DOCS_DIR/changelog")

if [[ ! -d "$DOCS_DIR" ]]; then
  echo "错误: 目录不存在: $DOCS_DIR" >&2
  exit 2
fi

errors=0
checked=0

check_file() {
  local file="$1"
  case "$(basename "$file")" in README.md) return ;; esac
  checked=$((checked + 1))

  if [[ "$(head -n 1 "$file")" != "---" ]]; then
    echo "$file: 缺少 YAML front-matter（首行不是 ---）"
    errors=$((errors + 1))
    return
  fi

  local fm
  fm=$(awk 'NR>1 { if ($0 == "---") exit; print }' "$file")

  local field
  for field in "${REQUIRED_FIELDS[@]}"; do
    if ! grep -qE "^${field}:" <<<"$fm"; then
      echo "$file: 缺少必填字段 '$field'"
      errors=$((errors + 1))
    fi
  done

  field_val() { grep -E "^$1:" <<<"$fm" | head -n 1 | sed "s/^$1:[[:space:]]*//" | tr -d '"'"'"' ' ; }

  if grep -qE '^related_code:' <<<"$fm"; then
    local listed
    listed=$(awk '/^related_code:/ { f=1; next } f && /^[[:space:]]*-[[:space:]]*[^[:space:]]/ { c++; next } f && /^[^[:space:]#-]/ { f=0 } END { print c+0 }' <<<"$fm")
    if [[ "$listed" -eq 0 ]]; then
      echo "$file: related_code 为空，至少需要 1 个代码路径"
      errors=$((errors + 1))
    fi
  fi

  local ver d t s m
  ver=$(field_val version)
  [[ "$ver" =~ ^v[0-9]+\.[0-9]+$ ]] || { echo "$file: version 格式应为 vX.Y，当前为 '$ver'"; errors=$((errors + 1)); }
  d=$(field_val date)
  [[ "$d" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || { echo "$file: date 格式应为 YYYY-MM-DD，当前为 '$d'"; errors=$((errors + 1)); }
  t=$(field_val type)
  [[ "$t" == system || "$t" == changelog ]] || { echo "$file: type 应为 system 或 changelog，当前为 '$t'"; errors=$((errors + 1)); }
  s=$(field_val status)
  [[ "$s" == active || "$s" == merged || "$s" == archived ]] || { echo "$file: status 应为 active / merged / archived，当前为 '$s'"; errors=$((errors + 1)); }
  m=$(field_val module)
  [[ "$m" != *","* && "$m" != */* ]] || { echo "$file: module 应为单一业务模块 '$m'"; errors=$((errors + 1)); }

  if [[ "$t" == changelog ]]; then
    local base
    base=$(basename "$file")
    [[ "$base" =~ ^[0-9]{8}_v[0-9]+\.[0-9]+_.+\.md$ ]] || { echo "$file: 变更文档文件名不合规"; errors=$((errors + 1)); }
  fi

  if ! grep -qE '^##[[:space:]]*[0-9]*\.?[[:space:]]*历史版本' "$file"; then
    echo "$file: 缺少「历史版本」章节"
    errors=$((errors + 1))
  fi
}

for dir in "${MANAGED_DIRS[@]}"; do
  [[ -d "$dir" ]] || continue
  while IFS= read -r file; do check_file "$file"; done < <(find "$dir" -maxdepth 1 -name '*.md' -type f | sort)
done

echo "----"
echo "已检查 $checked 篇受管文档，发现 $errors 处问题"
[[ "$errors" -eq 0 ]] || exit 1