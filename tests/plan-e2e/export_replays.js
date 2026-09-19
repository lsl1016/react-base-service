/**
 * Plan 模式 E2E 回放记录导出
 *
 * 从运行中的服务（POST /react/session/events，与会话回放页 /react/replay 同源）
 * 导出各场景实测会话的权威回放流，落盘 replays/<编号>-<场景>.json，
 * 并生成 replays/manifest.md 索引（场景、plan/session ID、终态、基线说明）。
 *
 * 用法（需服务在 :8180 运行且 DB 中保留对应会话）：
 *   node export_replays.js
 *
 * 归档原则：每次整批 E2E 后，把有代表性的运行（最终通过的基线 / Bug 证据样本）
 * 更新到 REPLAYS 映射并重跑本脚本；manifest 会记录导出时间与终态。
 */
'use strict';
const fs = require('fs');
const path = require('path');
const { httpPost } = require('./driver');

const OUT_DIR = path.join(__dirname, 'replays');
const CALLER = 'demo-app';

/** 场景 → 实测会话映射（2026-09-19 整批验证的代表运行） */
const REPLAYS = [
  { no: '01', name: 'complex-happy-path', scenario: 'S1 复杂正常流（repo 检索 + python 统计 + 中文报告）', baseline: '修复前基线', plan: 'plan_exec_e69dfa14743c4b9a933338ee4fe5be2e', session: 'session_0a46b9df40f74eecbba15e5618ea92df', expect: 'SUCCEEDED' },
  { no: '02', name: 'user-input-ws-resume', scenario: 'S2 USER_INPUT 等待 → WS 恢复 → 续跑完成', baseline: '修复后复测', plan: 'plan_exec_18b332a1dd89498385a54c07223b740d', session: 'session_b336b506bae74631be9b70cf579a7307', expect: 'SUCCEEDED' },
  { no: '03', name: 'user-action-reject-cancel', scenario: 'S3 USER_ACTION 拒绝 → 级联取消', baseline: '修复前基线', plan: 'plan_exec_c498cac741e145ffa8502864db389f67', session: 'session_d0efd363848a4c7299c344aed5a7c6ab', expect: 'CANCELLED' },
  { no: '04', name: 'midrun-cancel', scenario: 'S4 运行中取消（HTTP）+ WS 命令窗口探针', baseline: '修复后复测', plan: 'plan_exec_471a2ae0e9f84676b206644f177fd84a', session: 'session_b018d48d7d704022bcc6284ec000a227', expect: 'CANCELLED' },
  { no: '05', name: 'http-resume-durable', scenario: 'S5 关闭 WS 后 HTTP 恢复（连接无关性）', baseline: '修复前基线', plan: 'plan_exec_8a74af971124434aaf1e231fecd4041c', session: 'session_b39b6b6bea7e4feeb1223910d64b8538', expect: 'SUCCEEDED' },
  { no: '06', name: 'wait-mutex-and-schema', scenario: 'S6 WAIT 态并发互斥 + 自定义 responseSchema 校验', baseline: '修复前基线', plan: 'plan_exec_2b9ed379c2284605a561d8f0b28e2488', session: 'session_0356104f4bba449eb11667c4ef2cb36d', expect: 'SUCCEEDED' },
  { no: '07', name: 'timeout-classification', scenario: 'T7 步骤超时分类（timeoutSeconds=60 + sleep90，验证 P1/P3/P8 修复）', baseline: '修复后复测', plan: 'plan_exec_03588727b41c4098a826a162b5c8b28d', session: 'session_da522769f3064d249d8cc8fedb76dce7', expect: 'CANCELLED（验证完成后清理取消）' },
  { no: '98', name: 'bug-evidence-timeout-misclassified', scenario: 'Bug 证据：修复前步骤超时被误判为用户取消（步骤 3 恰 600s 整被级联 CANCELLED，对应测试报告 §6-P1）', baseline: 'Bug 证据（修复前）', plan: 'plan_exec_b373129cc5934860b9ccf154b99baa0e', session: 'session_72f9ad5573a8445b8146cb76fd4b8f0c', expect: 'CANCELLED（当时的错误终态；修复后应为 FAILED）' },
];

async function main() {
  fs.mkdirSync(OUT_DIR, { recursive: true });
  const rows = [];
  for (const item of REPLAYS) {
    const res = await httpPost('/react/session/events', { sessionId: item.session, callerKey: CALLER });
    if (res.status !== 200 || res.json.errNo !== 0) {
      rows.push({ ...item, ok: false, note: `导出失败: ${JSON.stringify(res.json).slice(0, 80)}` });
      continue;
    }
    const data = res.json.data || {};
    const file = `${item.no}-${item.name}.json`;
    fs.writeFileSync(path.join(OUT_DIR, file), JSON.stringify({
      exportedAt: new Date().toISOString(),
      scenario: item.scenario,
      baseline: item.baseline,
      planExecutionId: item.plan,
      sessionId: item.session,
      expectedOutcome: item.expect,
      replay: data,
    }, null, 1));
    const counts = {};
    for (const ev of data.events || []) counts[ev.type] = (counts[ev.type] || 0) + 1;
    const lastView = [...(data.events || [])].reverse().find(e => e.type === 'plan_view_update');
    const finalStatus = lastView && lastView.payload && lastView.payload.view && lastView.payload.view.status;
    rows.push({ ...item, ok: true, file, title: data.title || '', events: (data.events || []).length, counts, finalStatus });
    console.log(`exported ${file} events=${(data.events || []).length} final=${finalStatus}`);
  }

  const manifest = [
    '# Plan 模式 E2E 回放记录索引',
    '',
    `> 导出时间：${new Date().toISOString()} ｜ 来源：POST /react/session/events（与 /react/replay 回放页同源）`,
    '> 回放 JSON 内含会话标题、全量会话级事件与最终 plan_view_update 快照；步骤级原始事件流体积大（单次可达 5 万+条），不入库，可按 manifest 中的 planExecutionId 从 DB / plan_execution/events 接口重建。',
    '',
    '| 编号 | 场景 | 基线 | Plan / Session | 回放终态 | 事件数 | 文件 |',
    '|---|---|---|---|---|---|---|',
    ...rows.map(r => `| ${r.no} | ${r.scenario} | ${r.baseline} | \`${r.plan}\`<br>\`${r.session}\` | ${r.ok ? (r.finalStatus || '-') : '导出失败'} | ${r.ok ? r.events : '-'} | ${r.ok ? `[${r.file}](${r.file})` : r.note} |`),
    '',
    '## 预期结论对照',
    '',
    ...rows.map(r => `- **${r.no}**：预期 ${r.expect}${r.ok ? `，回放终态 ${r.finalStatus || '(无 view 快照)'}` : '，导出失败'}`),
    '',
  ].join('\n');
  fs.writeFileSync(path.join(OUT_DIR, 'manifest.md'), manifest);
  console.log('manifest.md written');
  process.exit(rows.every(r => r.ok) ? 0 : 1);
}

main().catch(e => { console.error('FATAL', e.message); process.exit(2); });
