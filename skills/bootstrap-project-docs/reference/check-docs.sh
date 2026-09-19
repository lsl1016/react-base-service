#!/usr/bin/env bash
# 文档 front-matter 校验。放在 scripts/check-docs.sh，可接入 CI 或 pre-commit。
# 用法: bash scripts/check-docs.sh [docs目录，默认 docs]
set -uo pipefail

DOCS_DIR="${1:-docs}"
REQUIRED_FIELDS=(title date version type module maintainer status related_code summary)

if [[ ! -d "$DOCS_DIR" ]]; then
  echo "错误: 目录不存在: $DOCS_DIR" >&2
  exit 2
fi

errors=0
checked=0

while IFS= read -r file; do
  case "$(basename "$file")" in README.md|CONVENTION.md) continue ;; esac
  checked=$((checked + 1))

  if [[ "$(head -n 1 "$file")" != "---" ]]; then
    echo "$file: 缺少 YAML front-matter（首行不是 ---）"
    errors=$((errors + 1))
    continue
  fi

  # 提取 front-matter：第一个 --- 之后到第二个 --- 之前
  fm=$(awk 'NR>1 { if ($0 == "---") exit; print }' "$file")

  for field in "${REQUIRED_FIELDS[@]}"; do
    if ! grep -qE "^${field}:" <<<"$fm"; then
      echo "$file: 缺少必填字段 '$field'"
      errors=$((errors + 1))
    fi
  done

  # 取 front-matter 中某个标量字段的值，去掉引号
  field_val() { grep -E "^$1:" <<<"$fm" | head -n 1 | sed "s/^$1:[[:space:]]*//" | tr -d '"'\'' ' ; }

  # related_code 至少 1 项：同行有值（非 []），或下方有非空 '- ' 列表项
  if grep -qE "^related_code:" <<<"$fm"; then
    inline=$(field_val related_code)
    listed=$(awk '
      /^related_code:/ { f=1; next }
      f && /^[[:space:]]*-[[:space:]]*[^[:space:]]/ { c++; next }
      f && /^[^[:space:]#-]/ { f=0 }
      END { print c+0 }' <<<"$fm")
    if [[ -z "$inline" || "$inline" == "[]" ]] && [[ "$listed" -eq 0 ]]; then
      echo "$file: related_code 为空，至少需要 1 个代码路径"
      errors=$((errors + 1))
    fi
  fi

  # version 格式 vX.Y
  ver=$(field_val version)
  if [[ -n "$ver" && ! "$ver" =~ ^v[0-9]+\.[0-9]+$ ]]; then
    echo "$file: version 格式应为 vX.Y，当前为 '$ver'"
    errors=$((errors + 1))
  fi

  # date 格式 YYYY-MM-DD
  d=$(field_val date)
  if [[ -n "$d" && ! "$d" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
    echo "$file: date 格式应为 YYYY-MM-DD，当前为 '$d'"
    errors=$((errors + 1))
  fi

  # type 枚举
  t=$(field_val type)
  if [[ -n "$t" && "$t" != "system" && "$t" != "changelog" ]]; then
    echo "$file: type 应为 system 或 changelog，当前为 '$t'"
    errors=$((errors + 1))
  fi

  # status 枚举
  s=$(field_val status)
  if [[ -n "$s" && "$s" != "active" && "$s" != "merged" && "$s" != "archived" ]]; then
    echo "$file: status 应为 active / merged / archived，当前为 '$s'"
    errors=$((errors + 1))
  fi

  # module 不应是包路径或逗号列表
  m=$(field_val module)
  if [[ "$m" == *","* ]]; then
    echo "$file: module 应为单一业务模块，不能是逗号列表 '$m'"
    errors=$((errors + 1))
  elif [[ "$m" == */* ]]; then
    echo "$file: module 应为业务模块名，不是代码包路径 '$m'"
    errors=$((errors + 1))
  fi

  # 变更文档文件名规范 YYYYMMDD_vX.Y_主题.md
  if [[ "$t" == "changelog" ]]; then
    base=$(basename "$file")
    if [[ ! "$base" =~ ^[0-9]{8}_v[0-9]+\.[0-9]+_.+\.md$ ]]; then
      echo "$file: 变更文档文件名应为 YYYYMMDD_vX.Y_<主题>.md"
      errors=$((errors + 1))
    fi
  fi

  # 必须有历史版本节
  if ! grep -qE "^##[[:space:]]*[0-9]*\.?[[:space:]]*历史版本" "$file"; then
    echo "$file: 缺少「历史版本」章节"
    errors=$((errors + 1))
  fi
done < <(find "$DOCS_DIR" -name '*.md' -type f | sort)

echo "----"
echo "已检查 $checked 篇文档，发现 $errors 处问题"
[[ "$errors" -eq 0 ]] || exit 1