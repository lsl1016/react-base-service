# Python 数据分析规范

对工具结果、上传附件做统计、聚合、清洗、可视化时使用 python_exec。它是白名单沙箱：数据经 stdin 注入，结果经 stdout 输出，无文件/网络/命令能力。

## 数据输入（inputs）

- **前序工具结果**：`{"type": "tool_result", "ref": "<resultRef>"}` —— 后端把该结果按 JSON 解析后放入对应 alias。
- **上传附件**：`{"type": "attachment", "fileId": "<fileId>"}` —— 文件内容（UTF-8 文本）直接注入 alias。
- **内联值**：`{"type": "raw_json"|"text"|"expr", "value": ...}`。
- **URL 拉取**：`{"type": "http", "url": "..."}`。

关键：`inputs['alias']` 已经是数据本身，不是路径。CSV 用 `pd.read_csv(io.StringIO(inputs['alias']))`，严禁把 alias 传给 `pd.read_csv('/path')` 或 `open()`。

## 代码骨架

```python
import json, sys
inputs = json.loads(sys.stdin.read())
import pandas as pd, io
df = pd.read_csv(io.StringIO(inputs['data']))
result = {"rows": int(len(df)), "summary": df.describe().to_dict()}
print(json.dumps(result, ensure_ascii=False))
```

## 产出文件（图表/CSV/Excel）

在 stdout JSON 里放 `artifacts` 数组，元素为 `{"type": "image/png", "name": "chart.png", "content": "<base64 或文本>"}`：

- 二进制（png/pdf/xlsx）必须 base64；文本（csv/json/svg）可直接放原文。
- matplotlib 先 `matplotlib.use('Agg')`；中文标签必须设置 `plt.rcParams['font.sans-serif']=['WenQuanYi Zen Hei']` 与 `plt.rcParams['axes.unicode_minus']=False`（字体已预装，禁止探测字体文件）。
- 产物的 content 不会回填给你（只有 artifactId 描述符）；向用户展示时调用 displayFiles 传 artifactId，禁止在正文输出文件链接。

## 边界

- 只能用白名单库（sys/json/pandas/numpy/math/statistics/datetime/matplotlib/io/base64）。
- 禁止 import os/subprocess/socket、open 文件操作、eval/exec、读环境变量、任何网络访问。
- 结构不明时才先 inspect_data；结果未截断且结构简单时直接写 python。
